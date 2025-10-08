package storage

import (
	// "bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/milahu/btcache-go/internal/backend"
	"github.com/milahu/btcache-go/internal/cache"
)

// RemoteStorage is the glue between anacrolix's torrent engine and our HTTP backend + cache.
// It knows how to: fetch .torrent files (metainfo), map piece -> file ranges, fetch ranges
// from backend, assemble pieces, store them in cache, and return piece bytes.

type RemoteStorage struct {
	client  *torrent.Client
	fetcher *backend.Fetcher
	cache   *cache.SQLCache
	mu      sync.Mutex
	// in-flight dedupe map: btih -> piece -> chan result
	inflight map[string]map[int]chan struct {
		data []byte
		err  error
	}
}

func NewRemoteStorage(
	client *torrent.Client,
	fetcher *backend.Fetcher,
	cache *cache.SQLCache,
) *RemoteStorage {
	return &RemoteStorage{
		client:  client, // can be nil initially
		fetcher: fetcher,
		cache:   cache,
		inflight: map[string]map[int]chan struct {
			data []byte
			err  error
		}{},
	}
}

func (rs *RemoteStorage) AttachToClient(client *torrent.Client) {
	rs.client = client
}

// FIXME also handle hybrid and v2 torrents
// AddTorrentByInfoHash downloads the .torrent from backend and prepares it in memory.
func (rs *RemoteStorage) AddTorrentByInfoHash(btih string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := rs.fetcher.GetTorrent(ctx, btih)
	if err != nil {
		return fmt.Errorf("fetch torrent: %w", err)
	}
	// mi, err := metainfo.Load(bytes.NewReader(b))
	// if err != nil {
	// 	return fmt.Errorf("parse torrent: %w", err)
	// }
	// optional: ensure target directory exists
	torrentDir := "torrents"
	if err := os.MkdirAll(torrentDir, 0o755); err != nil {
		return fmt.Errorf("mkdir torrents/: %w", err)
	}
	// write the .torrent file to disk
	path := filepath.Join(torrentDir, btih+".torrent")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write torrent: %w", err)
	}
	// add torrent to anacrolix client
	t, err := rs.client.AddTorrentFromFile(path)
	if err != nil {
		return fmt.Errorf("add torrent to client: %w", err)
	}
	// Wait until metadata is ready (should be instant since we have it)
	<-t.GotInfo()
	// Mark all pieces as complete
	if err := t.SetHaveAllPieces(); err != nil {
		return fmt.Errorf("mark torrent complete: %w", err)
	}
	// Optional: stop verification or unwanted downloading
	t.AllowDataDownload() // ensure seeding allowed
	t.DownloadAll()       // mark interest, but we already "have" everything
	// t.SetStopped(false) // FIXME t.SetStopped undefined
	log.Printf("added torrent %s", btih)
	return nil
}

// FetchPiece retrieves piece bytes for the given torrent metainfo and piece index.
// It will first consult the cache, and if missing will map to file ranges, fetch them
// from backend, assemble, store in cache, and return the bytes.
func (rs *RemoteStorage) FetchPiece(ctx context.Context, mi *metainfo.MetaInfo, btih string, pieceIndex int) ([]byte, error) {
	// 1) try cache
	if b, err := rs.cache.Get(btih, pieceIndex); err == nil {
		return b, nil
	}

	// 2) dedupe inflight
	rs.mu.Lock()
	mp, ok := rs.inflight[btih]
	if !ok {
		mp = map[int]chan struct {
			data []byte
			err  error
		}{}
		rs.inflight[btih] = mp
	}
	if ch, ok := mp[pieceIndex]; ok {
		rs.mu.Unlock()
		<-ch
		// waiter notified; read from cache
		return rs.cache.Get(btih, pieceIndex)
	}
	ch := make(chan struct {
		data []byte
		err  error
	}, 1)
	mp[pieceIndex] = ch
	rs.mu.Unlock()

	// 3) compute piece->file ranges
	ranges, pieceLen, err := computeRangesForPiece(mi, pieceIndex)
	if err != nil {
		ch <- struct {
			data []byte
			err  error
		}{nil, err}
		close(ch)
		return nil, err
	}

	// 4) fetch ranges and assemble
	buf := make([]byte, 0, pieceLen)
	for _, r := range ranges {
		b, err := rs.fetcher.GetFileRange(ctx, btih, safeTorrentName(mi), r.Path, r.Start, r.End)
		if err != nil {
			ch <- struct {
				data []byte
				err  error
			}{nil, err}
			close(ch)
			rs.mu.Lock()
			delete(rs.inflight[btih], pieceIndex)
			rs.mu.Unlock()
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
	ch <- struct {
		data []byte
		err  error
	}{buf, nil}
	close(ch)
	rs.mu.Lock()
	delete(rs.inflight[btih], pieceIndex)
	rs.mu.Unlock()
	return buf, nil
}

// Helper types for ranges
type fileRange struct {
	Path   string // full path relative to torrent root
	Start  int64  // byte offset within this file
	End    int64
	Length int64 // length of this fragment
}

// computeRangesForPiece computes which file ranges (relative to files in mi) are required to assemble pieceIndex.
// Returns a slice of fileRange in the order they should be concatenated and the expected piece length.
func computeRangesForPiece(mi *metainfo.MetaInfo, pieceIndex int) ([]fileRange, int64, error) {
	if mi == nil {
		return nil, 0, errors.New("nil metainfo")
	}

	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, 0, fmt.Errorf("unmarshal metainfo: %w", err)
	}

	pieceLength := int64(info.PieceLength)
	offset := pieceLength * int64(pieceIndex)
	end := offset + pieceLength

	files := info.UpvertedFiles()

	// Compute file offsets manually (since FileOffsets() no longer exists)
	offsets := make([]int64, len(files))
	var currentOffset int64
	for i, fi := range files {
		offsets[i] = currentOffset
		currentOffset += fi.Length
	}

	var ranges []fileRange
	var totalLen int64

	for i, fi := range files {
		fstart := offsets[i]
		fend := fstart + fi.Length

		if fend <= offset || fstart >= end {
			continue // file doesn't intersect this piece
		}

		// Clamp to piece boundaries
		startInFile := max64(0, offset-fstart)
		endInFile := min64(fi.Length, end-fstart)
		length := endInFile - startInFile

		ranges = append(ranges, fileRange{
			Path:   strings.Join(fi.Path, "/"),
			Start:  startInFile,
			Length: length,
		})
		totalLen += length
	}

	return ranges, totalLen, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// safeTorrentName returns the torrent's name or an empty string if unavailable.
func safeTorrentName(mi *metainfo.MetaInfo) string {
	if mi == nil {
		return ""
	}

	info, err := mi.UnmarshalInfo()
	if err != nil {
		return ""
	}

	return info.Name
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
