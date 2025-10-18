//go:build !noboltdb && !wasm
// +build !noboltdb,!wasm

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
	"github.com/anacrolix/torrent/storage"
)

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
	// never check pieces
	cfg.PieceHashersPerTorrent = 0
	cfg.DisableUTP = true // TODO remove

	// FIXME wrong format
	// expected: handshake Handshake: extensions=0000000000100005 (ltep, fast, dht)
	// log.Printf("our extension bits: %08x", cfg.Extensions)

	fileStorageOpts := storage.NewFileClientOpts{
		ClientBaseDir: "./server-content",
		TorrentDirMaker: func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
			return baseDir + "/" + infoHash.HexString()
		},
	}
	fileStorage := storage.NewFileOpts(fileStorageOpts)
	httpStorageOpts := storage.HttpStorageOpts{
		MetadataURL: "http://localhost/torrents/", // directory listing
		// TODO? replace with MetadataFileStorageOpts
		MetadataCacheDir: "./server-metadata", // local cache
		ContentURL:       "http://localhost",  // backend for pieces
		PieceCacheDir:    "./server-pieces",
		// no. duplication is bad
		// ContentFileStorageOpts: fileStorageOpts,
		ContentFileStorage: fileStorage,
		// TODO http authentication
		HTTPClient: &http.Client{},
	}
	cfg.DefaultStorage = storage.NewHttpStorage(httpStorageOpts)

	log.Printf("Starting torrent client at %s:%d", cfg.ListenHost("tcp4"), cfg.ListenPort)
	client, err := torrent.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "new client: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	// cfg.DefaultStorage.SetTorrentClient(client) // cycle
	fetchAndCacheTorrents(client)

	// keep process alive so client can operate (in real program you will run HTTP server etc.)
	select {}
}

// Fetch .torrent files from HTTP index and load using BTCacheClient
func fetchAndCacheTorrents(client *torrent.Client) error {
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
			// torrentURL := client.GetConfig().DefaultStorage.GetOpts().MetadataURL + m[1]
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
			if err := os.WriteFile(localPath, data, 0644); err != nil {
				log.Printf("failed to save %s: %v", localPath, err)
				continue
			}
			log.Printf("writing torrent to cache: %s", localPath)
		} else {
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

		// Add torrent with custom storage
		t, err := client.AddTorrent(mi)
		if t == nil || err != nil {
			log.Printf("failed to add torrent %s: %s", filename, err)
			continue
		}

		log.Printf("added torrent: %s", t.Name())

		// Replace storage with custom httpStorage.OptsCacheClient
		// wrapped := httpStorage.Opts.WrapStorage(t)
		// t.SetStorage(wrapped)

		// Mark all pieces complete for proxy
		t.SetHaveAllPieces()
	}

	return nil
}
