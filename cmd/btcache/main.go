//go:build !noboltdb && !wasm
// +build !noboltdb,!wasm

// cmd/btcache/main.go
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/generics"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"go.etcd.io/bbolt"
)

// based on anacrolix-torrent/tests/issue-930/main.go

/////////////////////////////////////////////////////////////////////
/////////////////////////////////////////////////////////////////////
/////////////////////////////////////////////////////////////////////

// BTCacheClient implements storage.ClientInterface
type BTCacheClient struct {
	BaseURL    string // backend for pieces
	HTTPClient *http.Client
	IndexURL   string // HTTP directory with .torrent files
	CacheDir   string // local cache directory for .torrent files
}

// func NewBTCacheClient(baseURL string) ClientImplCloser {
func NewBTCacheClient() storage.ClientImplCloser {
	btcc := &BTCacheClient{
		// BaseURL: baseURL,
		BaseURL:    "http://localhost", // backend for pieces
		HTTPClient: &http.Client{},
		IndexURL:   "http://localhost/torrents/", // directory listing
		CacheDir:   "./server-torrents",          // local cache
	}
	return btcc
}

func (btcc *BTCacheClient) Close() error {
	return nil
}

func (btcc *BTCacheClient) OpenTorrent(
	_ context.Context,
	_ *metainfo.Info,
	infoHash metainfo.Hash,
) (storage.TorrentImpl, error) {
	t := &BTCacheTorrent{btcc, infoHash}
	return storage.TorrentImpl{
		Piece: t.Piece,
		Close: t.Close,
	}, nil
}

func (btct *BTCacheTorrent) Piece(p metainfo.Piece) storage.PieceImpl {
	ret := &BTCachePiece{
		cl: btct.cl,
		p:  p,
		ih: btct.ih,
	}
	copy(ret.key[:], btct.ih[:])
	binary.BigEndian.PutUint32(ret.key[20:], uint32(p.Index()))
	return ret
}

func (btct BTCacheTorrent) Close() error { return nil }

/////////////////////////////////////////////////////////////////////
/////////////////////////////////////////////////////////////////////
/////////////////////////////////////////////////////////////////////

// based on anacrolix-torrent/storage/bolt-piece.go

type BTCachePiece struct {
	cl  *BTCacheClient
	p   metainfo.Piece
	ih  metainfo.Hash
	key [24]byte
}

var (
	_             storage.PieceImpl = (*BTCachePiece)(nil)
	dataBucketKey                   = []byte("data")
)

func (btcp *BTCachePiece) pc() storage.PieceCompletionGetSetter {
	return BTCachePieceCompletion{btcp.cl}
}

func (btcp *BTCachePiece) pk() metainfo.PieceKey {
	return metainfo.PieceKey{btcp.ih, btcp.p.Index()}
}

func (btcp *BTCachePiece) Completion() storage.Completion {
	c, err := btcp.pc().Get(btcp.pk())
	switch err {
	case bbolt.ErrDatabaseNotOpen:
		return storage.Completion{}
	case nil:
	default:
		panic(err)
	}
	return c
}

func (btcp *BTCachePiece) MarkComplete() error {
	return btcp.pc().Set(btcp.pk(), true)
}

func (btcp *BTCachePiece) MarkNotComplete() error {
	return btcp.pc().Set(btcp.pk(), false)
}

func (f *filePiece) ReadAt(buf []byte, off int64) (int, error) {
func (btcp *BTCachePiece) ReadAt(b []byte, off int64) (n int, err error) {

	// check if cached
	if btcp.btcc.isPieceCached(f.t.infoHash, f.p.Index()) {
		return f.t.bt.readPieceFromCache(f.t.infoHash, f.p.Index(), buf, off)
	}
	// fetch from backend and store to cache
	if err := f.t.bt.fetchPieceToCache(f.t.infoHash, f.p.Index()); err != nil {
		// fallback to original impl
		return f.orig.ReadAt(buf, off)
	}
	return f.t.bt.readPieceFromCache(f.t.infoHash, f.p.Index(), buf, off)
}

func (btcp *BTCachePiece) ReadAt(b []byte, off int64) (n int, err error) {
	err = btcp.db.View(func(tx *bbolt.Tx) error {
		db := tx.Bucket(dataBucketKey)
		if db == nil {
			return io.EOF
		}
		ci := off / chunkSize
		off %= chunkSize
		for len(b) != 0 {
			ck := btcp.chunkKey(int(ci))
			_b := db.Get(ck[:])
			// If the chunk is the wrong size, assume it's missing as we can't rely on the data.
			if len(_b) != chunkSize {
				return io.EOF
			}
			n1 := copy(b, _b[off:])
			off = 0
			ci++
			b = b[n1:]
			n += n1
		}
		return nil
	})
	return
}

func (btcp *BTCachePiece) chunkKey(index int) (ret [26]byte) {
	copy(ret[:], btcp.key[:])
	binary.BigEndian.PutUint16(ret[24:], uint16(index))
	return
}

func (btcp *BTCachePiece) WriteAt(b []byte, off int64) (n int, err error) {
	err = btcp.db.Update(func(tx *bbolt.Tx) error {
		db, err := tx.CreateBucketIfNotExists(dataBucketKey)
		if err != nil {
			return err
		}
		ci := off / chunkSize
		off %= chunkSize
		for len(b) != 0 {
			_b := make([]byte, chunkSize)
			ck := btcp.chunkKey(int(ci))
			copy(_b, db.Get(ck[:]))
			n1 := copy(_b[off:], b)
			db.Put(ck[:], _b)
			if n1 > len(b) {
				break
			}
			b = b[n1:]
			off = 0
			ci++
			n += n1
		}
		return nil
	})
	return
}

/////////////////////////////////////////////////////////////////////
/////////////////////////////////////////////////////////////////////
/////////////////////////////////////////////////////////////////////

// based on anacrolix-torrent/storage/bolt-piece-completion.go

const (
	boltDbCompleteValue   = "c"
	boltDbIncompleteValue = "i"
)

var completionBucketKey = []byte("completion")

type BTCachePieceCompletion struct {
	db *bbolt.DB
}

func (btcpc BTCachePieceCompletion) Persistent() bool {
	return true
}

var _ storage.PieceCompletion = (*BTCachePieceCompletion)(nil)

func NewBoltPieceCompletion(dir string) (ret storage.PieceCompletion, err error) {
	os.MkdirAll(dir, 0o750)
	p := filepath.Join(dir, ".torrent.bolt.db")
	db, err := bbolt.Open(p, 0o660, &bbolt.Options{
		Timeout: time.Second,
	})
	if err != nil {
		return
	}
	db.NoSync = true
	ret = &BTCachePieceCompletion{db}
	return
}

func (btcpc BTCachePieceCompletion) Get(pk metainfo.PieceKey) (cn Completion, err error) {
	err = btcpc.db.View(func(tx *bbolt.Tx) error {
		cb := tx.Bucket(completionBucketKey)
		if cb == nil {
			return nil
		}
		ih := cb.Bucket(pk.InfoHash[:])
		if ih == nil {
			return nil
		}
		var key [4]byte
		binary.BigEndian.PutUint32(key[:], uint32(pk.Index))
		cn.Ok = true
		switch string(ih.Get(key[:])) {
		case boltDbCompleteValue:
			cn.Complete = true
		case boltDbIncompleteValue:
			cn.Complete = false
		default:
			cn.Ok = false
		}
		return nil
	})
	return
}

func (btcpc BTCachePieceCompletion) Set(pk metainfo.PieceKey, b bool) error {
	if c, err := btcpc.Get(pk); err == nil && c.Ok && c.Complete == b {
		return nil
	}
	return btcpc.db.Update(func(tx *bbolt.Tx) error {
		c, err := tx.CreateBucketIfNotExists(completionBucketKey)
		if err != nil {
			return err
		}
		ih, err := c.CreateBucketIfNotExists(pk.InfoHash[:])
		if err != nil {
			return err
		}
		var key [4]byte
		binary.BigEndian.PutUint32(key[:], uint32(pk.Index))
		return ih.Put(key[:], []byte(func() string {
			if b {
				return boltDbCompleteValue
			} else {
				return boltDbIncompleteValue
			}
		}()))
	})
}

func (btcpc *BTCachePieceCompletion) Close() error {
	return btcpc.db.Close()
}

/////////////////////////////////////////////////////////////////////
/////////////////////////////////////////////////////////////////////
/////////////////////////////////////////////////////////////////////

var _ storage.ClientImpl = (*BTCacheClient)(nil)

// func (bt *BTCacheClient) Open(infoHash metainfo.Hash) (torrent.Storage, error) {
// 	return &BTCacheClientStorage{bt.BaseURL, bt.HTTPClient, infoHash}, nil
// }

// BTCacheClientStorage implements torrent.Storage
type BTCacheClientStorage struct {
	BaseURL    string
	HTTPClient *http.Client
	InfoHash   metainfo.Hash
}

type BTCacheStorage struct {
	*storage.Torrent                // embed the original
	btcc             *BTCacheClient // pointer to your backend handler
}

type BTCacheTorrent struct {
	cl *BTCacheClient
	ih metainfo.Hash
}

// func (bt *BTCacheClient) WrapStorage(t *torrent.Torrent) *BTCacheStorage {
// 	return &BTCacheStorage{
// 		Torrent: t.Storage(),
// 		client:  bt,
// 	}
// }

// Implement ReadPiece, WritePiece, etc. as needed
// For a proxy you typically implement ReadPiece to fetch via HTTP range request
func (s *BTCacheClientStorage) ReadPiece(p metainfo.Piece) ([]byte, error) {
	// Compute URL based on piece index and file mapping
	url := fmt.Sprintf("%s/cas/btih/%s/%s", s.BaseURL, s.InfoHash.HexString(), p.String())
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", p.Offset(), p.Offset()+p.Length()-1))
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// Other Storage interface methods can be no-op for a proxy
func (s *BTCacheClientStorage) WritePiece(p metainfo.Piece, data []byte) error { return nil }
func (s *BTCacheClientStorage) Close() error                                   { return nil }

// Fetch .torrent files from HTTP index and load using BTCacheClient
func (bt *BTCacheClient) FetchAndCacheTorrents(client *torrent.Client) error {
	// Ensure cache directory exists
	if err := os.MkdirAll(bt.CacheDir, 0755); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}

	// Fetch directory listing
	resp, err := http.Get(bt.IndexURL)
	if err != nil {
		return fmt.Errorf("failed to fetch index: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Parse <a href="..."> links ending with .torrent
	re := regexp.MustCompile(`<a href="([^"]+\.torrent)"`)
	matches := re.FindAllStringSubmatch(string(body), -1)

	for _, m := range matches {
		filename, err := url.QueryUnescape(m[1])
		if err != nil {
			log.Printf("failed to unescape %s: %v", m[1], err)
			continue
		}

		localPath := filepath.Join(bt.CacheDir, filename)

		// Skip if already cached
		if _, err := os.Stat(localPath); err != nil {
			// Download and cache
			torrentURL := bt.IndexURL + m[1]
			log.Printf("fetching %s ...", torrentURL)
			resp, err := http.Get(torrentURL)
			if err != nil {
				log.Printf("failed to fetch %s: %v", torrentURL, err)
				continue
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				log.Printf("failed to read %s: %v", torrentURL, err)
				continue
			}
			if err := os.WriteFile(localPath, data, 0644); err != nil {
				log.Printf("failed to save %s: %v", localPath, err)
				continue
			}
			log.Printf("cached torrent: %s", filename)
		} else {
			log.Printf("torrent cached: %s", filename)
		}

		// Load metainfo
		data, err := os.ReadFile(localPath)
		if err != nil {
			log.Printf("failed to read %s: %v", localPath, err)
			continue
		}
		mi, err := metainfo.Load(bytes.NewReader(data))
		if err != nil {
			log.Printf("failed to parse %s: %v", filename, err)
			continue
		}

		// Add torrent with custom storage
		t, err := client.AddTorrent(mi)
		if t == nil || err != nil {
			log.Printf("failed to add torrent %s: %s", filename, err)
			continue
		}

		log.Printf("added torrent: %s", t.Name())

		// Replace storage with custom BTCacheClient
		// wrapped := bt.WrapStorage(t)
		// t.SetStorage(wrapped)

		// get original storage pointer
		st := t.Storage() // returns *storage.Torrent

		// Override Piece function to add caching/fetching logic
		origPiece := st.Piece
		/*
			st.TorrentImpl.Piece = func(p metainfo.Piece) storage.PieceImpl {
				// try cache; if present, return an implementation that reads from cache
				if bt.isPieceCached(t.InfoHash(), p.Index()) {
					// return storage.NewFilePieceImpl(bt.cachePathForPiece(t.InfoHash(), p.Index()))
					return NewFilePieceImpl(bt.cachePathForPiece(t.InfoHash(), p.Index()))
				}

				// otherwise fetch from backend, store to cache, then return underlying piece impl
				if err := bt.fetchPieceToCache(t.InfoHash(), p.Index()); err != nil {
					log.Printf("fetchPieceToCache failed: %v", err)
					// fallback to original implementation to avoid breaking engine
					return origPiece(p)
				}

				// now that we have cached file, return original impl (or a file-backed impl)
				return origPiece(p)
			}
		*/
		/*
			st.TorrentImpl.Piece = func(p metainfo.Piece) storage.PieceImpl {
				orig := origPiece(p)
				return storage.PieceImpl{
					ReadAt: func(buf []byte, off int64) (int, error) {
						if bt.isPieceCached(t.InfoHash(), p.Index()) {
							return bt.readPieceFromCache(t.InfoHash(), p.Index(), buf, off)
						}
						if err := bt.fetchPieceToCache(t.InfoHash(), p.Index()); err != nil {
							return orig.ReadAt(buf, off)
						}
						return bt.readPieceFromCache(t.InfoHash(), p.Index(), buf, off)
					},
					Completion:   orig.Completion,
					MarkComplete: orig.MarkComplete,
					Len:          orig.Len,
					FilePath:     orig.FilePath,
				}
			}
		*/
		st.TorrentImpl.Piece = func(p metainfo.Piece) storage.PieceImpl {
			orig := origPiece(p)
			// wrap ReadAt via a closure
			readAt := func(buf []byte, off int64) (int, error) {
				if bt.isPieceCached(t.InfoHash(), p.Index()) {
					return bt.readPieceFromCache(t.InfoHash(), p.Index(), buf, off)
				}
				if err := bt.fetchPieceToCache(t.InfoHash(), p.Index()); err != nil {
					return orig.ReadAt(buf, off)
				}
				return bt.readPieceFromCache(t.InfoHash(), p.Index(), buf, off)
			}
			// now return the original PieceImpl but replace ReadAt
			// FIXME cmd/btcache/main.go:214:4: cannot assign to orig.ReadAt (neither addressable nor a map index expression)
			orig.ReadAt = readAt
			return orig
		}

		// if needed, also override PieceWithHash similarly
		origPieceWithHash := st.PieceWithHash
		st.TorrentImpl.PieceWithHash = func(p metainfo.Piece, pieceHash g.Option[[]byte]) storage.PieceImpl {
			// same logic
			return origPieceWithHash(p, pieceHash)
		}

		// re-install the (mutated) storage pointer in the torrent (usually it's the same pointer,
		// but calling SetStorage keeps semantics consistent with upstream helpers).
		// t.SetStorage(st)
		t.SetStorage(st)

		// Mark all pieces complete for proxy
		t.SetHaveAllPieces()
	}

	return nil
}

func (bt *BTCacheClient) cachePathForPiece(infoHash metainfo.Hash, pieceIndex int) string {
	return filepath.Join(bt.CacheDir, infoHash.HexString(), fmt.Sprintf("piece-%d.bin", pieceIndex))
}

func (bt *BTCacheClient) isPieceCached(infoHash metainfo.Hash, pieceIndex int) bool {
	path := bt.cachePathForPiece(infoHash, pieceIndex)
	_, err := os.Stat(path)
	return err == nil
}

func (bt *BTCacheClient) fetchPieceToCache(infoHash metainfo.Hash, pieceIndex int) error {
	url := fmt.Sprintf("%s/%s/%d", bt.BaseURL, infoHash.HexString(), pieceIndex)
	resp, err := bt.HTTPClient.Get(url)
	if err != nil {
		return fmt.Errorf("fetchPieceToCache: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetchPieceToCache: unexpected status %d", resp.StatusCode)
	}

	// Ensure directory exists
	path := bt.cachePathForPiece(infoHash, pieceIndex)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

///////////////////////////////////////////////////////////////////////////////////////////////////////////////
///////////////////////////////////////////////////////////////////////////////////////////////////////////////
///////////////////////////////////////////////////////////////////////////////////////////////////////////////

func (b *BTCacheClient) OpenTorrent(ctx context.Context, info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	log.Printf("BTCacheClient OpenTorrent infohash %s", infoHash.HexString())
	ti := storage.TorrentImpl{
		// NOTE: the signature expects generics.Option[[]byte] on current master.
		PieceWithHash: func(p metainfo.Piece, pieceHash generics.Option[[]byte]) storage.PieceImpl {
			return &HTTPPiece{
				Info:        info,
				InfoHash:    infoHash,
				PieceIndex:  p.Index(), // metainfo.Piece.Index() is a method
				PieceLen:    info.PieceLength,
				BaseURL:     b.BaseURL,
				HTTPClient:  b.HTTPClient,
				torrentName: info.Name,
			}
		},
		Close: func() error { return nil },
	}
	return ti, nil
}

// HTTPPiece implements storage.PieceImpl by fetching ranges from HTTP backend.
type HTTPPiece struct {
	Info        *metainfo.Info
	InfoHash    metainfo.Hash
	PieceIndex  int
	PieceLen    int64
	BaseURL     string
	HTTPClient  *http.Client
	torrentName string

	mu sync.Mutex
}

func (hp *HTTPPiece) ReadAt(b []byte, off int64) (int, error) {
	log.Printf("HTTPPiece ReadAt piece %d off %d len %d", hp.PieceIndex, off, len(b))
	if off < 0 || off >= hp.PieceLen {
		return 0, io.EOF
	}
	want := int64(len(b))
	if off+want > hp.PieceLen {
		want = hp.PieceLen - off
	}
	pieceStart := int64(hp.PieceIndex) * hp.PieceLen
	globalStart := pieceStart + off
	globalEnd := pieceStart + off + want - 1 // inclusive

	// single-file torrent?
	if len(hp.Info.Files) == 0 {
		// single-file: file-relative offsets equal global offsets
		return hp.fetchIntoBufferForFile(hp.torrentName, globalStart, globalEnd, b[:want])
	}

	// multi-file torrent: walk files and request overlapping ranges
	var fileBase int64
	dstOff := int64(0)
	for i := range hp.Info.Files {
		fi := hp.Info.Files[i]
		fileLen := fi.Length
		fileEnd := fileBase + fileLen - 1

		if globalEnd < fileBase {
			break
		}
		if globalStart > fileEnd {
			fileBase += fileLen
			continue
		}
		// overlap with this file
		fileReqStart := max64(0, globalStart-fileBase)
		fileReqEnd := min64(fileLen-1, globalEnd-fileBase)
		nWant := fileReqEnd - fileReqStart + 1

		fp := strings.Join(fi.Path, "/")
		n, err := hp.fetchIntoBufferForFile(fp, fileReqStart, fileReqEnd, b[dstOff:dstOff+nWant])
		dstOff += int64(n)
		if err != nil {
			if dstOff > 0 {
				return int(dstOff), nil
			}
			return int(dstOff), err
		}
		if dstOff >= want {
			break
		}
		fileBase += fileLen
	}
	return int(dstOff), nil
}

func (hp *HTTPPiece) fetchIntoBufferForFile(filePath string, start, end int64, buf []byte) (int, error) {
	log.Printf("HTTPPiece fetching %s bytes %d-%d", filePath, start, end)
	btihHex := fmt.Sprintf("%x", hp.InfoHash[:])
	escapedTorrentName := url.PathEscape(hp.torrentName)
	parts := strings.Split(filePath, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	escapedFilePath := strings.Join(parts, "/")

	fullURL := fmt.Sprintf("%s/cas/btih/%s/%s/%s", strings.TrimRight(hp.BaseURL, "/"), btihHex, escapedTorrentName, escapedFilePath)
	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := hp.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("backend returned %s", resp.Status)
	}

	n, readErr := io.ReadFull(resp.Body, buf)
	if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
		// allow short read
		return n, nil
	}
	if readErr != nil {
		return n, readErr
	}
	return n, nil
}

// write/completion stubs
func (hp *HTTPPiece) WriteAt(b []byte, off int64) (int, error) { return len(b), nil }
func (hp *HTTPPiece) MarkComplete() error                      { return nil }
func (hp *HTTPPiece) MarkNotComplete() error                   { return nil }
func (hp *HTTPPiece) Completion() storage.Completion {
	return storage.Completion{Ok: true, Complete: true}
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

func usageAndExit() {
	fmt.Fprintf(os.Stderr, "usage: %s /path/to/some.torrent\n", os.Args[0])
	os.Exit(2)
}

func main() {
	// if len(os.Args) < 2 {
	// 	usageAndExit()
	// }
	// torrentFile := os.Args[1]

	// // Load .torrent file
	// f, err := os.Open(torrentFile)
	// if err != nil {
	// 	fmt.Fprintf(os.Stderr, "open torrent: %v\n", err)
	// 	os.Exit(1)
	// }
	// defer f.Close()

	// mi, err := metainfo.Load(f)
	// if err != nil {
	// 	fmt.Fprintf(os.Stderr, "load metainfo: %v\n", err)
	// 	os.Exit(1)
	// }
	// info, err := mi.UnmarshalInfo()
	// if err != nil {
	// 	fmt.Fprintf(os.Stderr, "unmarshal info: %v\n", err)
	// 	os.Exit(1)
	// }

	// // Use the canonical bytes from the .torrent file to compute infohash
	// infoHash := mi.HashInfoBytes()

	// Create normal network client
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = "./cache"
	cfg.ListenPort = 5007
	cfg.ListenHost = func(network string) string {
		if network == "tcp4" || network == "udp4" {
			return "127.0.0.1"
		}
		if network == "tcp6" || network == "udp6" {
			return "::1"
		}
		return ""
	}
	cfg.DisableIPv6 = true
	cfg.Seed = true
	// cfg.NoDHT aka cfg.DisableDHT
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.DisableWebseeds = true
	cfg.DisablePEX = true
	// cfg.NoDefaultPortForwarding aka cfg.DisableUpnp
	cfg.NoDefaultPortForwarding = true
	cfg.HeaderObfuscationPolicy.Preferred = false
	// cfg.HeaderObfuscationPolicy.RequirePreferred = false
	// cfg.PublicIp4 = "todo"
	// Set debug logging
	cfg.Debug = true

	// FIXME wrong format
	// expected: handshake Handshake: extensions=0000000000100005 (ltep, fast, dht)
	// log.Printf("our extension bits: %08x", cfg.Extensions)

	fileStorageOpts := storage.NewFileClientOpts{
		ClientBaseDir: "./server-content",
		TorrentDirMaker: func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
			return baseDir + "/" + infoHash.HexString()
		},
	}
	httpStorageOpts := storage.HttpStorageOpts{
		BaseURL:    "http://localhost", // backend for pieces
		HTTPClient: &http.Client{},
		IndexURL:   "http://localhost/torrents/", // directory listing
		// TODO? replace with MetadataFileStorageOpts
		CacheDir:   "./server-metadata",          // local cache
		// TODO? rename to ContentFileStorageOpts
		FileStorageOpts: fileStorageOpts,
	}
	cfg.DefaultStorage = storage.NewHttpStorage(httpStorageOpts)

	log.Printf("Starting torrent client at %s:%d", cfg.ListenHost("tcp4"), cfg.ListenPort)
	client, err := torrent.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "new client: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	// Create our storage impl and actually use it when adding the torrent.
	btcc := NewBTCacheClient()

	if err := btcc.FetchAndCacheTorrents(client); err != nil {
		log.Fatal(err)
	}

	// keep process alive so client can operate (in real program you will run HTTP server etc.)
	select {}
}
