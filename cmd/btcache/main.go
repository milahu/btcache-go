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

	"github.com/milahu/btcache-go/internal/backend"
	"github.com/milahu/btcache-go/internal/cache"
	"github.com/milahu/btcache-go/internal/qb"
	"github.com/milahu/btcache-go/internal/storage"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Minimal config loading (for skeleton we use env vars / flags); see README for how to extend
	cfg, err := storage.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Create cache (sqlite-backed)
	sqlCache, err := cache.NewSQLCache(cfg.CacheDBPath, cfg.MaxCacheBytes)
	if err != nil {
		log.Fatalf("create cache: %v", err)
	}
	defer sqlCache.Close()

	// Backend fetcher
	fetcher := backend.NewFetcher(cfg.BackendBaseURL, cfg.BackendUser, cfg.BackendPass, httpClientOptions{})

	// qBittorrent client wrapper
	qbClient := qb.NewClient(cfg.QBURL, cfg.QBUser, cfg.QBPass)

	// Remote storage layer that talks to fetcher + cache. This implements the layer
	// that anacrolix/torrent will call to read pieces. See package docs in
	// internal/storage for wiring details.
	rs := storage.NewRemoteStorage(fetcher, sqlCache)

	// Start eviction goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sqlCache.RunEvictor(ctx, time.Minute)

	// Sync torrents from qBittorrent (tag "proxy") and add torrents to client.
	// The exact anacrolix/torrent client wiring is in internal/storage and requires
	// you to call rs.AttachToClient(client) after creating the anacrolix client.
	// See README for detailed steps and TODOs.
	hashes, err := qbClient.ListHashesWithTag("proxy")
	if err != nil {
		log.Fatalf("qb list: %v", err)
	}
	fmt.Printf("found %d proxy hashes\n", len(hashes))

	// Add torrents (download .torrent from backend and register in storage layer)
	for _, h := range hashes {
		if err := rs.AddTorrentByInfoHash(h); err != nil {
			log.Printf("add torrent %s: %v", h, err)
		}
	}

	// Wait for signals
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	log.Printf("shutting down")
}

// Minimal http client options struct used as placeholder in this skeleton
type httpClientOptions struct{}
