/*
package storage

import (
    "context"
    "crypto/sha1"
    "fmt"
    "os"
    "path/filepath"

    "github.com/anacrolix/torrent"

    "github.com/anacrolix/torrent/metainfo"
)

type ProxyStorage struct {
    MI       *metainfo.MetaInfo
    RS       *RemoteStorage
    CacheDir string
}

func (s *ProxyStorage) ReadPiece(t *torrent.Torrent, index int) ([]byte, error) {
    cacheFile := filepath.Join(s.CacheDir, fmt.Sprintf("%d.chunk", index))
    if data, err := os.ReadFile(cacheFile); err == nil {
        return data, nil
    }

    btih := t.InfoHash().HexString()
    data, err := s.RS.FetchPiece(context.Background(), s.MI, btih, index)
    if err != nil {
        return nil, err
    }

    info, _ := s.MI.UnmarshalInfo()
    if len(info.Pieces) >= (index+1)*20 {
        h := sha1.Sum(data)
        expected := info.Pieces[index*20 : (index+1)*20]
        for i := 0; i < 20; i++ {
            if h[i] != expected[i] {
                return nil, fmt.Errorf("piece %d hash mismatch", index)
            }
        }
    }

    if err := os.MkdirAll(s.CacheDir, 0o755); err == nil {
        os.WriteFile(cacheFile, data, 0o644)
    }

    return data, nil
}

func (s *ProxyStorage) WritePiece(t *torrent.Torrent, index int, data []byte) error { return nil }
func (s *ProxyStorage) Flush(t *torrent.Torrent) error                               { return nil }
func (s *ProxyStorage) Close() error                                         { return nil }
*/

/*
package storage

import (
    "context"
    "errors"
    "path/filepath"
    "sync"

    "github.com/anacrolix/torrent/metainfo"
    "github.com/anacrolix/torrent/storage"
)

// ProxyStorage implements storage.ClientImpl for anacrolix/torrent.
// It acts as the "StorageOpener" that the torrent engine calls when opening torrents.
type ProxyStorage struct {
    RS       *RemoteStorage
    CacheDir string
    mu       sync.Mutex
}

// --- storage.ClientImpl interface ---

func (p *ProxyStorage) OpenTorrent(ctx context.Context, info *metainfo.Info, hash metainfo.Hash) (storage.TorrentImpl, error) {
    if info == nil {
        return nil, errors.New("nil info")
    }

    // Create a per-torrent subdir for any cached pieces (optional)
    torrentDir := filepath.Join(p.CacheDir, hash.HexString())

    t := &proxyTorrent{
        info:       info,
        hash:       hash,
        rs:         p.RS,
        cacheDir:   torrentDir,
        pieceLocks: make(map[int]*sync.Mutex),
    }

    return t, nil
}

func (p *ProxyStorage) Close() error {
    return nil
}

// --- TorrentImpl stub (the object returned by OpenTorrent) ---

type proxyTorrent struct {
    info       *metainfo.Info
    hash       metainfo.Hash
    rs         *RemoteStorage
    cacheDir   string
    pieceLocks map[int]*sync.Mutex
    mu         sync.Mutex
}

// Implement storage.TorrentImpl

func (t *proxyTorrent) Piece(p metainfo.Piece) storage.PieceImpl {
    // Return a lightweight proxyPiece that will fetch or serve data lazily.
    return &proxyPiece{
        torrent: t,
        index:   int(p.Index()),
    }
}

func (t *proxyTorrent) Files() []storage.File {
    // We can return nil for now; optional for your proxy setup
    return nil
}

func (t *proxyTorrent) Flush() error { return nil }
func (t *proxyTorrent) Close() error { return nil }

// --- storage.PieceImpl stub ---

type proxyPiece struct {
    torrent *proxyTorrent
    index   int
}

// Implement minimal PieceImpl interface

func (p *proxyPiece) ReadAt(b []byte, off int64) (int, error) {
    // Here you’ll later call RemoteStorage.FetchPiece() etc.
    return 0, errors.New("not implemented")
}

func (p *proxyPiece) WriteAt(b []byte, off int64) (int, error) {
    return 0, errors.New("read-only proxy storage")
}

func (p *proxyPiece) Close() error { return nil }
*/

/*
package storage

import (
	"context"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)



type ProxyStorage struct {
	RS       *RemoteStorage
	CacheDir string
}

// This makes ProxyStorage satisfy storage.ClientImpl.
func (ps *ProxyStorage) OpenTorrent(ctx context.Context, info *metainfo.Info, ih metainfo.Hash) (storage.TorrentImpl, error) {
	return &ProxyTorrent{
		Info: info,
		RS:   ps.RS,
	}, nil
}



type ProxyTorrent struct {
	Info *metainfo.Info
	RS   *RemoteStorage
}

// Implement the bare minimum
func (pt *ProxyTorrent) Piece(p metainfo.Piece) storage.PieceImpl {
	return &ProxyPiece{
		RS:    pt.RS,
		Piece: p,
	}
}

func (pt *ProxyTorrent) Close() error { return nil }



type ProxyPiece struct {
	RS    *RemoteStorage
	Piece metainfo.Piece
}

func (pp *ProxyPiece) ReadAt(b []byte, off int64) (int, error) {
	// you can implement your remote fetch logic here
	return 0, nil
}

func (pp *ProxyPiece) WriteAt(b []byte, off int64) (int, error) {
	// optionally support caching or uploads
	return 0, nil
}
*/

package storage

import (
	"context"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

type ProxyStorage struct {
	RS       *RemoteStorage
	CacheDir string
}

// implement storage.ClientImpl
func (ps *ProxyStorage) OpenTorrent(ctx context.Context, info *metainfo.Info, ih metainfo.Hash) (storage.TorrentImpl, error) {
	// create and return a proxy torrent that implements storage.TorrentImpl
	pt := &proxyTorrent{info: info, rs: ps.RS}
	return pt, nil
}

func (ps *ProxyStorage) Close() error { return nil }

// proxyTorrent implements storage.TorrentImpl
type proxyTorrent struct {
	info *metainfo.Info
	rs   *RemoteStorage
}

func (pt *proxyTorrent) Piece(p metainfo.Piece) storage.PieceImpl {
	return &proxyPiece{t: pt, index: int(p.Index())}
}
func (pt *proxyTorrent) Files() []storage.File { return nil }
func (pt *proxyTorrent) Flush() error          { return nil }
func (pt *proxyTorrent) Close() error          { return nil }

// proxyPiece implements storage.PieceImpl
type proxyPiece struct {
	t     *proxyTorrent
	index int
}

func (pp *proxyPiece) ReadAt(b []byte, off int64) (int, error) {
	// call t.rs.FetchPieceChunk or similar; return data into b
	return 0, nil
}
func (pp *proxyPiece) WriteAt(b []byte, off int64) (int, error) { return 0, nil }
func (pp *proxyPiece) Close() error                             { return nil }
