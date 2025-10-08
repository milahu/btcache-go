package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	anacrolixStorage "github.com/anacrolix/torrent/storage"

	"github.com/milahu/btcache-go/internal/backend"
	"github.com/milahu/btcache-go/internal/cache"
	"github.com/milahu/btcache-go/internal/compat"
	"github.com/milahu/btcache-go/internal/qb"
	internalStorage "github.com/milahu/btcache-go/internal/storage"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Minimal config loading (replace with your own)
	cfg, err := internalStorage.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// SQLite cache
	sqlCache, err := cache.NewSQLCache(cfg.CacheDBPath, cfg.MaxCacheBytes)
	if err != nil {
		log.Fatalf("create cache: %v", err)
	}
	defer sqlCache.Close()

	// Backend fetcher
	// FIXME cannot use nil as int value in argument to backend.NewFetcher
	fetcherTimeout := 30
	fetcher := backend.NewFetcher(cfg.BackendBaseURL, cfg.BackendUser, cfg.BackendPass, fetcherTimeout)

	// Create remote storage with client = nil
	rs := internalStorage.NewRemoteStorage(nil, fetcher, sqlCache)
	ps := &internalStorage.ProxyStorage{
		RS:       rs,
		CacheDir: "./chunks",
	}
	baseClient := anacrolixStorage.NewClient(ps)
	shim := compat.NewClientShim(baseClient)
	clientCfg := torrent.ClientConfig{
		Seed:     true,
		NoUpload: false,
	}
	// client := torrent.NewClient(&clientCfg)
	client, err := torrent.NewClient(&clientCfg)
	if err != nil {
		log.Fatalf("create client: %v", err)
	}
	defer client.Close()
	client.SetDefaultStorage(shim)
	rs.AttachToClient(client)

	// qBittorrent client
	qbClient := qb.NewClient(cfg.QBURL, cfg.QBUser, cfg.QBPass)

	// Get all finished torrents with tag "proxy"
	hashes, err := qbClient.ListHashesWithTag("proxy")
	if err != nil {
		log.Fatalf("qb list: %v", err)
	}
	fmt.Printf("found %d proxy hashes\n", len(hashes))

	// Add torrents from backend and mark 100% complete
	for _, h := range hashes {
		if err := rs.AddTorrentByInfoHash(h); err != nil {
			log.Printf("add torrent %s: %v", h, err)
			continue
		}
		ih, err := metainfo.NewHashFromHex(h)
		if err != nil {
			log.Printf("invalid infohash %s: %v", h, err)
			continue
		}

		t := client.Torrent(ih)
		if t == nil {
			log.Printf("cannot find torrent %s in client after adding", h)
			continue
		}
		t.SetHaveAllPieces()
	}

	// Start eviction goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sqlCache.RunEvictor(ctx, time.Minute)

	// Wait for termination signals
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	log.Printf("shutting down")
}

// Minimal http client options struct used as placeholder in this skeleton
type httpClientOptions struct{}
