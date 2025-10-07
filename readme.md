# btcache-go

A caching reverse-proxy BitTorrent client, written in Go

## overview

btcache-go runs on a frontend server with fast network and small disk,
caching torrent pieces
fetched from a backend server with a slow network but large storage.

### internal packages

- qbclient: Fetch torrents with tag "proxy" from qBittorrent backend.
- backend: Fetch .torrent files and file ranges from backend HTTP server.
- cache: SQLite-based LRU cache for chunks.
- storage: Piece-to-file mapping and anacrolix storage adapter.

## TODO

1. Wire RemoteStorage into an anacrolix/torrent client
  (see `github.com/anacrolix/torrent/storage` and the `confluence` example).
  The anacrolix library allows you to implement a custom storage backend
  so that when the torrent engine needs a piece, it will call into your code.
  Implement that adapter so anacrolix will call `RemoteStorage.FetchPiece(...)` when serving peers.
2. Configure authentication and URLs
  (the config skeleton returns defaults; replace with proper YAML parsing).
  Don't embed credentials in logs.
3. Adjust concurrency limits and implement a rate-limiter in `backend.Fetcher`
  if you need to strictly cap bytes/sec.
4. Add metrics, monitoring, and tests.
  Unit-test `computeRangesForPiece` thoroughly
  (single-file and multi-file torrents, edge-case last piece).

## see also

- https://code.google.com/archive/p/btcache/
- https://doi.org/10.1109/ICSESS49938.2020.9237696
  - https://sci-hub.ru/https://doi.org/10.1109/ICSESS49938.2020.9237696
