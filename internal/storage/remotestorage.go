package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/milahu/btcache-go/internal/backend"
	"github.com/milahu/btcache-go/internal/cache"
)

// RemoteStorage is the glue between anacrolix's torrent engine and our HTTP backend + cache.
// It knows how to: fetch .torrent files (metainfo), map piece -> file ranges, fetch ranges
// from backend, assemble pieces, store them in cache, and return piece bytes.

type RemoteStorage struct {
	fetcher *backend.Fetcher
	cache *cache.SQLCache
	mu sync.Mutex
	// in-flight dedupe map: btih -> piece -> chan result
	inflight map[string]map[int]chan struct{data []byte; err error}
}

func NewRemoteStorage(fetcher *backend.Fetcher, cache *cache.SQLCache) *RemoteStorage {
	return &RemoteStorage{fetcher: fetcher, cache: cache, inflight: map[string]map[int]chan struct{data []byte; err error}{}}
}

// AddTorrentByInfoHash downloads the .torrent from backend and prepares it in memory.
func (rs *RemoteStorage) AddTorrentByInfoHash(btih string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := rs.fetcher.GetTorrent(ctx, btih)
	if err != nil { return err }
	mi, err := metainfo.Load(b)
	if err != nil { return err }
	// TODO: register mi + a handler with anacrolix client. The exact wiring depends on
	// the anacrolix/torrent Storage API. See README and confluence example for how to
	// attach a custom storage backend that will call rs.FetchPiece when the engine
	// needs a piece. For now we keep the metainfo in memory or write it to a folder.
	_ = mi
	// Write .torrent to disk for inspection
	_ = os.WriteFile(filepath.Join("torrents", btih+".torrent"), b, 0644)
	return nil
}

// FetchPiece retrieves piece bytes for the given torrent metainfo and piece index.
// It will first consult the cache, and if missing will map to file ranges, fetch them
// from backend, assemble, store in cache, and return the bytes.
func (rs *RemoteStorage) FetchPiece(ctx context.Context, mi *metainfo.MetaInfo, btih string, pieceIndex int) ([]byte, error) {
	// 1) try cache
	if b, err := rs.cache.Get(btih, pieceIndex); err == nil { return b, nil }

	// 2) dedupe inflight
	rs.mu.Lock()
	mp, ok := rs.inflight[btih]
	if !ok { mp = map[int]chan struct{data []byte; err error}{}; rs.inflight[btih] = mp }
	if ch, ok := mp[pieceIndex]; ok {
		rs.mu.Unlock()
		<-ch
		// waiter notified; read from cache
		return rs.cache.Get(btih, pieceIndex)
	}
	ch := make(chan struct{data []byte; err error}, 1)
	mp[pieceIndex] = ch
	rs.mu.Unlock()

	// 3) compute piece->file ranges
	ranges, pieceLen, err := computeRangesForPiece(mi, pieceIndex)
	if err != nil { ch <- struct{data []byte; err error}{nil, err}; close(ch); return nil, err }

	// 4) fetch ranges and assemble
	buf := make([]byte, 0, pieceLen)
	for _, r := range ranges {
		b, err := rs.fetcher.GetFileRange(ctx, btih, safeTorrentName(mi), r.FilePath, r.Start, r.End)
		if err != nil {
			ch <- struct{data []byte; err error}{nil, err}
			close(ch)
			rs.mu.Lock(); delete(rs.inflight[btih], pieceIndex); rs.mu.Unlock()
			return nil, err
		}
		buf = append(buf, b...)
	}

	// 5) store in cache
	if err := rs.cache.Put(btih, pieceIndex, buf); err != nil {
		// non-fatal: we can still return bytes
		fmt.Printf("warning: cache put failed: %v\n", err)
	}

	// notify waiters
	ch <- struct{data []byte; err error}{buf, nil}
	close(ch)
	rs.mu.Lock(); delete(rs.inflight[btih], pieceIndex); rs.mu.Unlock()
	return buf, nil
}

// Helper types for ranges
type fileRange struct{
	FilePath string
	Start int64
	End int64
}

// computeRangesForPiece computes which file ranges (relative to files in mi) are required to assemble pieceIndex.
// Returns a slice of fileRange in the order they should be concatenated and the expected piece length.
func computeRangesForPiece(mi *metainfo.MetaInfo, pieceIndex int) ([]fileRange, int, error) {
	if mi == nil || mi.Info == nil { return nil, 0, errors.New("nil metainfo") }
	pieceLen := int(mi.Info.PieceLength)
	begin := int64(pieceIndex) * int64(pieceLen)
	end := begin + int64(pieceLen) - 1
	// last piece may be shorter if torrent total length < end
	var totalLen int64
	if mi.Info.IsSingleFile() {
		totalLen = mi.Info.Length
	} else {
		for _, f := range mi.Info.Files {
			totalLen += f.Length
		}
	}
	if begin >= totalLen { return nil, 0, fmt.Errorf("pieceIndex out of range: %d", pieceIndex) }
	if end >= totalLen { end = totalLen - 1 }

	ranges := []fileRange{}
	// walk files
	var fileStart int64 = 0
	if mi.Info.IsSingleFile() {
		// single file: name is Info.Name, size is Info.Length
		fp := mi.Info.Name
		if end < mi.Info.Length {
			ranges = append(ranges, fileRange{FilePath: fp, Start: begin, End: end})
		} else {
			ranges = append(ranges, fileRange{FilePath: fp, Start: begin, End: mi.Info.Length-1})
		}
		return ranges, int(end-begin+1), nil
	}
	for _, f := range mi.Info.Files {
		fileEnd := fileStart + f.Length - 1
		if fileEnd < begin { fileStart += f.Length; continue }
		if fileStart > end { break }
		// overlap
		rs := maxInt64(0, begin-fileStart)
		re := minInt64(f.Length-1, end-fileStart)
		ranges = append(ranges, fileRange{FilePath: filepath.Join(f.Path...), Start: rs, End: re})
		fileStart += f.Length
	}
	return ranges, int(end-begin+1), nil
}

func safeTorrentName(mi *metainfo.MetaInfo) string {
	if mi == nil || mi.Info == nil { return "" }
	return mi.Info.Name
}

func maxInt64(a,b int64) int64 { if a>b { return a }; return b }
func minInt64(a,b int64) int64 { if a<b { return a }; return b }
