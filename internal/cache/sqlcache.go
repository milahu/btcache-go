// internal/cache/sqlcache.go
package cache

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// SQLCache stores metadata about cached pieces and enforces eviction.
// Piece content is stored as files under chunks/btih/{btih}/{pieceIndex}

type SQLCache struct {
	db *sql.DB
	root string
	maxBytes int64
	mu sync.Mutex
}

func NewSQLCache(dbPath string, maxBytes int64) (*SQLCache, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_busy_timeout=5000")
	if err != nil { return nil, err }
	c := &SQLCache{db: db, root: "chunks/btih", maxBytes: maxBytes}
	if err := c.initSchema(); err != nil { return nil, err }
	if err := os.MkdirAll(c.root, 0755); err != nil { return nil, err }
	return c, nil
}

func (c *SQLCache) initSchema() error {
	_, err := c.db.Exec(`
CREATE TABLE IF NOT EXISTS pieces (
  btih TEXT NOT NULL,
  piece_index INTEGER NOT NULL,
  size INTEGER NOT NULL,
  last_access INTEGER NOT NULL,
  requests INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (btih, piece_index)
);
CREATE INDEX IF NOT EXISTS idx_pieces_last_access ON pieces(last_access);
`)
	return err
}

func (c *SQLCache) Close() error { return c.db.Close() }

// PathFor returns filesystem path where piece should be stored
func (c *SQLCache) PathFor(btih string, piece int) string {
	return filepath.Join(c.root, btih, fmt.Sprintf("%d", piece))
}

// Put atomically writes a piece to disk and records metadata.
func (c *SQLCache) Put(btih string, piece int, data []byte) error {
	p := c.PathFor(btih, piece)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil { return err }
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil { return err }
	if err := os.Rename(tmp, p); err != nil { return err }

	now := time.Now().Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.Exec(`INSERT OR REPLACE INTO pieces(btih,piece_index,size,last_access,requests) VALUES(?,?,?,?,COALESCE((SELECT requests FROM pieces WHERE btih=? AND piece_index=?),0))`,
		btih, piece, len(data), now, btih, piece)
	return err
}

func (c *SQLCache) Get(btih string, piece int) ([]byte, error) {
	p := c.PathFor(btih, piece)
	b, err := os.ReadFile(p)
	if err != nil { return nil, err }
	// update metadata
	now := time.Now().Unix()
	c.mu.Lock()
	_, _ = c.db.Exec(`UPDATE pieces SET last_access=?, requests=requests+1 WHERE btih=? AND piece_index=?`, now, btih, piece)
	c.mu.Unlock()
	return b, nil
}

// TotalBytes returns total cached bytes according to DB
func (c *SQLCache) TotalBytes() (int64, error) {
	var s sql.NullInt64
	row := c.db.QueryRow(`SELECT SUM(size) FROM pieces`)
	if err := row.Scan(&s); err != nil { return 0, err }
	if !s.Valid { return 0, nil }
	return s.Int64, nil
}

// RunEvictor runs a periodic eviction loop to keep cache under maxBytes.
func (c *SQLCache) RunEvictor(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done(): return
		case <-t.C:
			c.evictIfNeeded()
		}
	}
}

func (c *SQLCache) evictIfNeeded() {
	c.mu.Lock()
	defer c.mu.Unlock()
	total, err := c.TotalBytes()
	if err != nil { return }
	if total <= c.maxBytes { return }
	// evict by least recently used (last_access asc) until under threshold
	rows, err := c.db.Query(`SELECT btih, piece_index, size FROM pieces ORDER BY last_access ASC`)
	if err != nil { return }
	defer rows.Close()
	for rows.Next() {
		var btih string
		var piece int
		var size int64
		if err := rows.Scan(&btih, &piece, &size); err != nil { break }
		// delete file
		p := c.PathFor(btih, piece)
		os.Remove(p)
		_, _ = c.db.Exec(`DELETE FROM pieces WHERE btih=? AND piece_index=?`, btih, piece)
		total -= size
		if total <= int64(float64(c.maxBytes)*0.95) { break }
	}
}
