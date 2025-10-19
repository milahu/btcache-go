// cmd/btcache/main.go

package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	// NOTE we use a fork with http storage (HttpStorageImpl)
	"github.com/anacrolix/torrent/storage"
)

func main() {
	// Configure torrent client
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
	// cfg.DisableIPv6 = true
	cfg.Seed = true
	// cfg.NoDHT aka cfg.DisableDHT
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.DisableWebseeds = true
	cfg.DisablePEX = true
	// cfg.NoDefaultPortForwarding aka cfg.DisableUpnp
	cfg.NoDefaultPortForwarding = true
	// cfg.HeaderObfuscationPolicy.Preferred = false
	// cfg.PublicIp4 = "todo"
	// enable debug logging
	cfg.Debug = true
	// never check pieces
	cfg.PieceHashersPerTorrent = 0
	// cfg.DisableUTP = true // debug: use TCP only

	// Configure default storage
	contentFileStorageOpts := storage.NewFileClientOpts{
		ClientBaseDir: "./server-content",
		TorrentDirMaker: func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
			return baseDir + "/" + infoHash.HexString()
		},
	}
	contentFileStorage := storage.NewFileOpts(contentFileStorageOpts)
	httpStorageOpts := storage.HttpStorageOpts{
		MetadataURL: "http://localhost/torrents/", // directory listing
		// TODO? replace with MetadataFileStorageOpts
		MetadataCacheDir: "./server-metadata", // local cache
		ContentURL:       "http://localhost",  // backend for pieces
		PieceCacheDir:    "./server-pieces",
		// no. duplication is bad
		// ContentFileStorageOpts: fileStorageOpts,
		ContentFileStorage: contentFileStorage,
		// TODO? fileStorage stores files, pieceStorage stores pieces
		// ContentPieceStorage: contentPieceStorage,
		// TODO http authentication
		HTTPClient: &http.Client{},
	}
	cfg.DefaultStorage = storage.NewHttpStorage(httpStorageOpts)

	// Create torrent client
	log.Printf("Starting torrent client at %s:%d", cfg.ListenHost("tcp4"), cfg.ListenPort)
	client, err := torrent.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "new client: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	// cfg.DefaultStorage.SetTorrentClient(client) // cycle
	fetchAndLoadTorrents(client)

	// keep process alive so client can operate (in real program you will run HTTP server etc.)
	select {}
}

// Fetch and load .torrent files
func fetchAndLoadTorrents(client *torrent.Client) error {
	// panics if DefaultStorage has wrong type
	httpStorage := (client.GetConfig().DefaultStorage).(*storage.HttpStorageImpl)

	// Ensure cache directory exists
	if err := os.MkdirAll(httpStorage.Opts.MetadataCacheDir, 0755); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}

	// Fetch directory listing
	resp, err := http.Get(httpStorage.Opts.MetadataURL)
	if err != nil {
		return fmt.Errorf("failed to fetch index: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Parse directory listing
	// Parse <a href="..."> links ending with .torrent
	re := regexp.MustCompile(`<a href="([^"]+\.torrent)"`)
	matches := re.FindAllStringSubmatch(string(body), -1)

	for _, m := range matches {
		filename, err := url.QueryUnescape(m[1])
		if err != nil {
			log.Printf("failed to unescape %s: %v", m[1], err)
			continue
		}

		localPath := filepath.Join(httpStorage.Opts.MetadataCacheDir, filename)

		// Skip if already cached
		if _, err := os.Stat(localPath); err != nil {
			// Download and cache
			torrentURL := httpStorage.Opts.MetadataURL + m[1]
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
			// write cache
			if err := os.WriteFile(localPath, data, 0644); err != nil {
				log.Printf("failed to save %s: %v", localPath, err)
				continue
			}
			log.Printf("writing torrent to cache: %s", localPath)
		} else {
			// read cache
			log.Printf("reading torrent from cache: %s", localPath)
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

		// infoHash := mi.HashInfoBytes().HexString()

		// Add torrent
		t, err := client.AddTorrent(mi)
		if t == nil || err != nil {
			log.Printf("failed to add torrent %s: %s", filename, err)
			continue
		}

		log.Printf("adding torrent: %s", t.Name())

		// Mark all pieces as complete
		err = t.SetHaveAllPieces()
		if err != nil {
			log.Printf("failed to mark all pieces as complete for torrent %s: %v", filename, err)
			continue
		}
	}

	return nil
}
