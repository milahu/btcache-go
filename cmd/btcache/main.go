package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

type Config struct {
	BackendURL      string
	BackendUsername string
	BackendPassword string
	CacheDir        string
	ListenPort      int
}

type CachingClient struct {
	config     Config
	client     *torrent.Client
	httpClient *http.Client
	pieceCache *PieceCache
}

type PieceCache struct {
	baseDir string
}

func NewPieceCache(baseDir string) (*PieceCache, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}
	return &PieceCache{
		baseDir: baseDir,
	}, nil
}

func (pc *PieceCache) GetPiecePath(infohash metainfo.Hash, pieceIndex int) string {
	hashStr := infohash.HexString()
	return filepath.Join(pc.baseDir, "chunks", "btih", hashStr, fmt.Sprintf("piece-%d", pieceIndex))
}

func (pc *PieceCache) HasPiece(infohash metainfo.Hash, pieceIndex int) bool {
	path := pc.GetPiecePath(infohash, pieceIndex)
	_, err := os.Stat(path)
	return err == nil
}

func (pc *PieceCache) StorePiece(infohash metainfo.Hash, pieceIndex int, data []byte) error {
	path := pc.GetPiecePath(infohash, pieceIndex)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (pc *PieceCache) LoadPiece(infohash metainfo.Hash, pieceIndex int) ([]byte, error) {
	path := pc.GetPiecePath(infohash, pieceIndex)
	return os.ReadFile(path)
}

func (cc *CachingClient) fetchPieceFromBackend(infohash metainfo.Hash, pieceIndex int) ([]byte, error) {
	// Load metainfo from cache or backend
	mi, err := fetchTorrentFile(cc.config, infohash.HexString())
	if err != nil {
		return nil, fmt.Errorf("failed to load metainfo: %w", err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to get info from metainfo: %w", err)
	}
	pieceLength := int64(info.PieceLength)
	pieceOffset := int64(pieceIndex) * pieceLength

	// Find which file(s) this piece belongs to
	var fileIndex int
	var fileOffset int64
	var found bool
	var offset int64 = 0
	for i, f := range info.Files {
		if pieceOffset < offset+f.Length {
			fileIndex = i
			fileOffset = pieceOffset - offset
			found = true
			break
		}
		offset += f.Length
	}
	if !found {
		return nil, fmt.Errorf("piece offset out of range")
	}
	file := info.Files[fileIndex]
	// Calculate how much to read from this file
	readLen := pieceLength
	if fileOffset+readLen > file.Length {
		readLen = file.Length - fileOffset
	}

	// Build URL for the file
	infohashStr := infohash.HexString()
	filePath := file.Path
	// Join path elements for URL
	urlPath := ""
	for _, p := range filePath {
		urlPath = filepath.Join(urlPath, p)
	}
	url := fmt.Sprintf("%s/cas/btih/%s/%s", cc.config.BackendURL, infohashStr, urlPath)
	log.Printf("fetching piece %d from %s", pieceIndex, url)

	// Build HTTP range header
	rangeHeader := fmt.Sprintf("bytes=%d-%d", fileOffset, fileOffset+readLen-1)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", rangeHeader)
	if cc.config.BackendUsername != "" {
		req.SetBasicAuth(cc.config.BackendUsername, cc.config.BackendPassword)
	}

	resp, err := cc.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backend returned status: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}

// Custom piece completion that marks all pieces as complete initially
// but fetches from backend when actually needed
type ProxyPieceCompletion struct {
	cc       *CachingClient
	infoHash metainfo.Hash
}

func (pc *ProxyPieceCompletion) Get(pieceIndex int) (bool, error) {
	// Always return true to indicate we have the piece
	// The actual data will be fetched on demand
	return true, nil
}

func (pc *ProxyPieceCompletion) Set(pieceIndex int, complete bool) error {
	// We don't modify completion status since we're a proxy
	return nil
}

func (pc *ProxyPieceCompletion) Close() error {
	return nil
}

func getTorrentsFromBackend(config Config) ([]string, error) {
	/*
		// TODO: Implement fetching torrent list from qBittorrent API
		log.Println("TODO: Implement getTorrentsFromBackend - returning empty list for now")
		return []string{}, nil
	*/
	infohashes := []string{
		// FIXME use infohashes from config
		"0000000000000000000000000000000000000000",
	}
	return infohashes, nil
}

func fetchTorrentFile(config Config, infohash string) (*metainfo.MetaInfo, error) {
	url := fmt.Sprintf("%s/cas/btih/%s.torrent", config.BackendURL, infohash)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	if config.BackendUsername != "" {
		req.SetBasicAuth(config.BackendUsername, config.BackendPassword)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch torrent: %s", resp.Status)
	}

	return metainfo.Load(resp.Body)
}

func NewCachingClient(config Config) (*CachingClient, error) {
	// Create cache directory
	if err := os.MkdirAll(config.CacheDir, 0755); err != nil {
		return nil, err
	}

	pieceCache, err := NewPieceCache(config.CacheDir)
	if err != nil {
		return nil, err
	}

	// Configure torrent client with file-based storage
	torrentConfig := torrent.NewDefaultClientConfig()
	torrentConfig.DataDir = config.CacheDir
	torrentConfig.ListenPort = config.ListenPort
	torrentConfig.Seed = true

	client, err := torrent.NewClient(torrentConfig)
	if err != nil {
		return nil, err
	}

	return &CachingClient{
		config:     config,
		client:     client,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		pieceCache: pieceCache,
	}, nil
}

func (cc *CachingClient) Close() error {
	cc.client.Close()
	return nil
}

func (cc *CachingClient) AddTorrent(mi *metainfo.MetaInfo) error {
	t, err := cc.client.AddTorrent(mi)
	if err != nil {
		return err
	}

	// Set up a hook to intercept piece requests
	// This is a simplified approach - in practice you'd need to
	// modify the storage to intercept reads
	t.DownloadAll()
	log.Printf("Added torrent: %s (infohash: %s)", t.Name(), t.InfoHash().HexString())

	return nil
}

func main() {
	config := Config{
		BackendURL:      "http://localhost",
		BackendUsername: "",
		BackendPassword: "",
		CacheDir:        "./cache",
		ListenPort:      50007,
	}

	// Create caching client
	cachingClient, err := NewCachingClient(config)
	if err != nil {
		log.Fatal(err)
	}
	defer cachingClient.Close()

	// Get torrent list from backend
	infohashes, err := getTorrentsFromBackend(config)
	if err != nil {
		log.Fatal(err)
	}

	// Add all torrents
	for _, infohash := range infohashes {
		mi, err := fetchTorrentFile(config, infohash)
		if err != nil {
			log.Printf("Failed to fetch torrent %s: %v", infohash, err)
			continue
		}

		if err := cachingClient.AddTorrent(mi); err != nil {
			log.Printf("Failed to add torrent %s: %v", infohash, err)
			continue
		}
	}

	log.Printf("Caching proxy started on :%d", config.ListenPort)
	log.Printf("Cache directory: %s", config.CacheDir)
	log.Printf("Press Ctrl+C to stop")

	// Keep running
	select {}
}
