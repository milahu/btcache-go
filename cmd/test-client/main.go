package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Printf("Usage: %s <infohash> <peer_ip:port>\n", os.Args[0])
		fmt.Printf("Example: %s 1C02BDB8D63F1A2D8A49B2A429A2E9F8E38E7F17 127.0.0.1:50007\n", os.Args[0])
		os.Exit(1)
	}

	infohash := os.Args[1]
	peerAddr := os.Args[2]

	// Configure client to ONLY use manually added peers
	cfg := torrent.NewDefaultClientConfig()
	cfg.ListenPort = 6881
	cfg.DataDir = "./test-downloads"

	// Disable all peer discovery mechanisms
	cfg.DisableTrackers = true
	cfg.DisableWebtorrent = true
	cfg.DisableWebseeds = true
	cfg.DisablePEX = true
	cfg.NoDHT = true
	cfg.DisableIPv6 = true

	// Set debug logging
	cfg.Debug = true

	/*
		if true {
			// Add this to the configuration
			cfg.Logger = verboseLogger{}

			// And replace the client creation with this:
			cfg.Dialer = &verboseDialer{}
		}
	*/

	client, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	log.Printf("=== STARTING VERBOSE BITTORRENT CLIENT ===")
	log.Printf("Infohash: %s", infohash)
	log.Printf("Target peer: %s", peerAddr)

	// Add magnet URI
	magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s", infohash)
	log.Printf("Adding magnet URI: %s", magnet)

	t, err := client.AddMagnet(magnet)
	if err != nil {
		log.Fatal("Failed to add magnet:", err)
	}

	// Set up event handlers for detailed logging
	// setupTorrentLogging(t)

	// Wait for metadata
	log.Printf("🔄 Waiting for metadata...")
	<-t.GotInfo()

	info := t.Info()
	log.Printf("✅ GOT METADATA")
	log.Printf("   Torrent name: %s", t.Name())
	log.Printf("   Total size: %d bytes", info.TotalLength())
	log.Printf("   Piece length: %d bytes", info.PieceLength)
	log.Printf("   Number of pieces: %d", info.NumPieces())
	log.Printf("   Files:")
	for _, file := range info.UpvertedFiles() {
		log.Printf("     - %s (%d bytes)", file.DisplayPath(info), file.Length)
	}

	// Parse and add peer
	addr, err := net.ResolveTCPAddr("tcp", peerAddr)
	if err != nil {
		log.Fatal("Failed to parse peer address:", err)
	}

	log.Printf("👥 Adding peer: %s", peerAddr)
	t.AddPeers([]torrent.PeerInfo{{Addr: addr}})

	// Download everything
	log.Printf("⬇️  Starting download...")
	t.DownloadAll()

	// Monitor progress with detailed logging
	startTime := time.Now()
	lastBytes := int64(0)
	lastPieceCount := 0

	ticker := time.NewTicker(1 * time.Second) // More frequent updates
	defer ticker.Stop()

	log.Printf("📊 Starting progress monitoring...")

	for range ticker.C {
		completed := t.BytesCompleted()
		total := info.TotalLength()
		stats := t.Stats()

		if total == 0 {
			log.Printf("⏳ Waiting for piece data...")
			continue
		}

		percent := float64(completed) / float64(total) * 100

		// Calculate speeds
		elapsed := time.Since(startTime).Seconds()
		instantSpeed := float64(completed-lastBytes) / 1024 // KB/s for last second
		averageSpeed := float64(completed) / elapsed / 1024 // Average KB/s

		// Count completed pieces
		currentPieceCount := 0
		for i := 0; i < info.NumPieces(); i++ {
			if t.PieceState(i).Complete {
				currentPieceCount++
			}
		}
		piecesSinceLast := currentPieceCount - lastPieceCount

		log.Printf("📈 PROGRESS: %.1f%% (%d/%d bytes) | "+
			"Speed: %.1f KB/s (avg: %.1f KB/s) | "+
			"Pieces: %d/%d (+%d) | "+
			"Active Peers: %d | "+
			//"Pending Requests: %d"+
			"",
			percent, completed, total,
			instantSpeed, averageSpeed,
			currentPieceCount, info.NumPieces(), piecesSinceLast,
			stats.ActivePeers,
			// stats.PendingRequests,
		)

		lastBytes = completed
		lastPieceCount = currentPieceCount

		if completed >= total {
			totalTime := time.Since(startTime)
			log.Printf("🎉 DOWNLOAD COMPLETE!")
			log.Printf("   Total time: %v", totalTime)
			log.Printf("   Average speed: %.1f KB/s", float64(total)/totalTime.Seconds()/1024)
			break
		}

		// Log detailed piece states periodically
		if time.Since(startTime).Seconds() > 10 && time.Since(startTime).Seconds() < 20 {
			logPieceStates(t, info)
		}

		// If no peers after a while, try re-adding
		if stats.ActivePeers == 0 && time.Since(startTime) > 10*time.Second {
			log.Printf("🔄 No active peers, re-adding peer: %s", peerAddr)
			t.AddPeers([]torrent.PeerInfo{{Addr: addr}})
		}
	}
}

/*
func setupTorrentLogging(t *torrent.Torrent) {
	// Log when peers are connected/disconnected
	go func() {
		for {
			select {
			case peerStats := <-t.PeerStats():
				log.Printf("👥 PEER STATS: %+v", peerStats)
			}
		}
	}()

	// Log piece completion events
	go func() {
		for {
			select {
			case piece := <-t.PieceCompleted():
				log.Printf("✅ PIECE COMPLETED: %d", piece.Index)
			}
		}
	}()

	// Log when data is read
	t.SetOnRead(func(data []byte, offset int64) {
		log.Printf("📖 DATA READ: %d bytes at offset %d", len(data), offset)
	})

	// Log when data is written
	t.SetOnWrite(func(data []byte, offset int64) {
		log.Printf("📝 DATA WRITTEN: %d bytes at offset %d", len(data), offset)
	})
}
*/

func logPieceStates(t *torrent.Torrent, info *metainfo.Info) {
	log.Printf("🔍 DETAILED PIECE STATES:")
	for i := 0; i < info.NumPieces() && i < 20; i++ { // Limit to first 20 pieces
		state := t.PieceState(i)
		var status string
		if state.Complete {
			status = "COMPLETE"
			// else if state.Partial {
			// 	status = fmt.Sprintf("PARTIAL (%d blocks)", state.Blocks)
			// }
		} else if state.Priority == torrent.PiecePriorityNormal {
			status = "PENDING"
		} else if state.Priority == torrent.PiecePriorityNone {
			status = "SKIPPED"
		} else {
			status = fmt.Sprintf("PRIORITY_%d", state.Priority)
		}

		log.Printf("   Piece %d: %s", i, status)
	}
	if info.NumPieces() > 20 {
		log.Printf("   ... and %d more pieces", info.NumPieces()-20)
	}
}

/*
// Custom type to implement logger interface for even more low-level logging
type verboseLogger struct{}

func (l verboseLogger) Output(calldepth int, s string) error {
	log.Printf("[TORRENT-LIB] %s", s)
	return nil
}

type verboseDialer struct {
	net.Dialer
}

func (d *verboseDialer) Dial(network, address string) (net.Conn, error) {
	log.Printf("🌐 NETWORK DIAL: %s %s", network, address)
	conn, err := d.Dialer.Dial(network, address)
	if err != nil {
		log.Printf("❌ DIAL FAILED: %s %s: %v", network, address, err)
		return nil, err
	}
	log.Printf("✅ CONNECTION ESTABLISHED: %s %s", network, address)

	// Wrap the connection to log all reads/writes
	return &verboseConn{Conn: conn, peer: address}, nil
}

type verboseConn struct {
	net.Conn
	peer string
}

func (c *verboseConn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	if n > 0 {
		log.Printf("📥 RECV from %s: %d bytes", c.peer, n)
		// Log first few bytes as hex for protocol analysis
		if n > 0 && n <= 8 {
			log.Printf("   First %d bytes: %x", n, b[:n])
		}
	}
	if err != nil {
		log.Printf("❌ READ ERROR from %s: %v", c.peer, err)
	}
	return n, err
}

func (c *verboseConn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	if n > 0 {
		log.Printf("📤 SEND to %s: %d bytes", c.peer, n)
		// Log first few bytes as hex for protocol analysis
		if n > 0 && n <= 8 {
			log.Printf("   First %d bytes: %x", n, b[:n])
		}
	}
	if err != nil {
		log.Printf("❌ WRITE ERROR to %s: %v", c.peer, err)
	}
	return n, err
}

func (c *verboseConn) Close() error {
	log.Printf("🔌 CLOSING connection to %s", c.peer)
	return c.Conn.Close()
}
*/
