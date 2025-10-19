// cmd/btcache/main.go

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	// NOTE we use a fork with http storage (HttpStorageImpl)
	// https://github.com/milahu/anacrolix-torrent
	"github.com/anacrolix/torrent/storage"

	"gopkg.in/yaml.v3"
)

func main() {
	cfg := getTorrentClientConfig()
	if cfg == nil {
		return
	}

	// Create torrent client
	log.Printf("Starting torrent client at %s:%d", cfg.ListenHost("tcp4"), cfg.ListenPort)
	client, err := torrent.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "new client: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	httpStorage := (cfg.DefaultStorage).(*storage.HttpStorageImpl)
	err = httpStorage.InheritTorrentClientConfig(cfg)
	if err != nil {
		log.Printf("httpStorage.InheritTorrentClientConfig err: %s\n", err)
	}

	// Run initially
	fetchAndLoadTorrents(client)

	// Run periodically in the background
	interval := time.Duration(httpStorage.Opts.MetadataFilesUpdateInterval) * time.Second
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fetchAndLoadTorrents(client)
			}
		}
	}()

	// TODO run HTTP API server -> stats, metrics, add/remove torrents

	// keep process alive
	select {}
}

func getTorrentClientConfig() *torrent.ClientConfig {
	ListenHost4 := flag.String(
		"ListenHost4",
		"",
		"Listen on this IPv4 host. Example: 127.0.0.1",
	)
	ListenHost6 := flag.String(
		"ListenHost6",
		"",
		"Listen on this IPv6 host. Example: ::1",
	)
	ListenPort := flag.Int(
		"ListenPort",
		6881,
		"Listen on this port",
	)
	MetadataFilesURL := flag.String(
		"MetadataFilesURL",
		"http://localhost/torrents/",
		"Get metadata files from the directory listing at this URL",
	)
	MetadataFilesUpdateInterval := flag.Int64(
		"MetadataFilesUpdateInterval",
		3600,
		"Update metadata files every N seconds from the directory listing at MetadataFilesURL",
	)
	ContentFilesURL := flag.String(
		"ContentFilesURL",
		"http://localhost/",
		"Get content files from the CAS filesystem at this URL",
	)
	MetadataCacheDir := flag.String(
		"MetadataCacheDir",
		"btcache-metadata",
		"Cache metadata files in this directory",
	)
	PieceCacheDir := flag.String(
		"PieceCacheDir",
		"btcache-pieces",
		"Cache content pieces in this directory",
	)
	DisableUpnp := flag.Bool(
		"DisableUpnp",
		false,
		"Disable UPnP / NAT-PMP port mapping",
	)
	RequireEncryption := flag.Bool(
		"RequireEncryption",
		false,
		"Require encrypted connections",
	)
	DisableEncryption := flag.Bool(
		"DisableEncryption",
		false,
		"Disable encrypted connections",
	)
	DisableDHT := flag.Bool(
		"DisableDHT",
		false,
		"Disable DHT",
	)
	DisableIPv6 := flag.Bool(
		"DisableIPv6",
		false,
		"Disable IPv6",
	)
	DisableUTP := flag.Bool(
		"DisableUTP",
		false,
		"Disable UTP",
	)
	// TODO? config file
	// configFile := flag.String("config", "", "Load configuration from YAML file")
	// writeFile := flag.String("write-config", "", "Write default configuration to YAML file")
	help := flag.Bool("help", false, "Show help message")

	flag.Parse()

	if *help {
		// NOTE options are sorted by ABC
		flag.Usage()
		return nil
	}

	// Configure torrent client
	cfg := torrent.NewDefaultClientConfig()

	cfg.Seed = true

	// never check pieces
	cfg.PieceHashersPerTorrent = 0

	// fmt.Printf("default torrent client config:\n")
	// printStructFields(cfg)

	// FIXME not used
	cfg.DataDir = "./btcache-data"

	cfg.ListenPort = *ListenPort

	// no, this is too simple
	// cfg.ListenHost = func(network string) string {
	// 	return *listenHost
	// }
	if *ListenHost4 != "" && *ListenHost6 != "" {
		cfg.ListenHost = func(network string) string {
			if network == "tcp4" || network == "udp4" {
				return *ListenHost4
			}
			if network == "tcp6" || network == "udp6" {
				return *ListenHost6
			}
			return ""
		}
	} else if *ListenHost4 != "" {
		cfg.ListenHost = func(network string) string {
			if network == "tcp4" || network == "udp4" {
				return *ListenHost4
			}
			return ""
		}
	} else if *ListenHost6 != "" {
		cfg.ListenHost = func(network string) string {
			if network == "tcp6" || network == "udp6" {
				return *ListenHost6
			}
			return ""
		}
	}

	if *DisableIPv6 {
		cfg.DisableIPv6 = true
	}

	// cfg.NoDHT aka cfg.DisableDHT
	if *DisableDHT {
		cfg.NoDHT = true
	}

	// cfg.DisableTrackers = true
	// cfg.DisableWebseeds = true
	// cfg.DisablePEX = true

	// cfg.NoDefaultPortForwarding aka cfg.DisableUpnp
	if *DisableUpnp {
		cfg.NoDefaultPortForwarding = true
	}

	// cfg.HeaderObfuscationPolicy.Preferred = false

	if *RequireEncryption {
		cfg.HeaderObfuscationPolicy.Preferred = true
		cfg.HeaderObfuscationPolicy.RequirePreferred = true
	} else if *DisableEncryption {
		cfg.HeaderObfuscationPolicy.Preferred = false
		cfg.HeaderObfuscationPolicy.RequirePreferred = false
	}

	// cfg.PublicIp4 = "todo"

	// enable debug logging
	// cfg.Debug = true

	// debug: use TCP only
	if *DisableUTP {
		cfg.DisableUTP = true
	}

	// Configure default storage
	// FIXME not used
	contentFileStorageOpts := storage.NewFileClientOpts{
		ClientBaseDir: "./btcache-content",
		TorrentDirMaker: func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
			return baseDir + "/" + infoHash.HexString()
		},
	}
	contentFileStorage := storage.NewFileOpts(contentFileStorageOpts)
	httpStorageOpts := storage.HttpStorageOpts{
		// TODO rename to MetadataFilesURL
		// backend HTTP server with metadata files (.torrent files)
		// metadata file URLs are parsed from the directory listing
		MetadataURL:                 *MetadataFilesURL,
		MetadataFilesUpdateInterval: *MetadataFilesUpdateInterval,
		// TODO? replace with MetadataFileStorageOpts
		// local cache for metadata files (.torrent files)
		// NOTE the source of truth for metadata files is the MetadataURL
		// this is only used for caching
		MetadataCacheDir: *MetadataCacheDir,
		// TODO rename to ContentFilesURL
		//   != ContentPiecesURL
		// backend HTTP server with content files
		// note: "func (tio HttpTorrentImplIO) ReadAt"
		//   assumes a cas filesystem https://github.com/milahu/cas-filesystem-spec
		ContentURL: *ContentFilesURL,
		// TODO?
		// example: cas filesystem https://github.com/milahu/cas-filesystem-spec
		// ContentURLFormat: "http://localhost/cas/{hashtype}/{hash}/{torrent_name}/{file_path}",
		// TODO?
		// // backend HTTP server with content pieces
		// PieceURL: "http://localhost",
		// local cache for content pieces
		PieceCacheDir: *PieceCacheDir,
		// no. duplication is bad
		// ContentFileStorageOpts: fileStorageOpts,
		ContentFileStorage: contentFileStorage,
		// TODO? fileStorage stores files, pieceStorage stores pieces
		// ContentPieceStorage: contentPieceStorage,
		// TODO http authentication
		HTTPTimeoutSeconds: 30,
	}

	// create default storage
	cfg.DefaultStorage = storage.NewHttpStorage(httpStorageOpts)

	// fmt.Printf("custom torrent client config:\n")
	// printStructFields(cfg)

	return cfg
}

func httpGet(httpContext context.Context, opt storage.HttpStorageOpts, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(httpContext, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	if opt.HTTPUserAgent != "" {
		req.Header.Set("User-Agent", opt.HTTPUserAgent)
	}

	if opt.HttpRequestDirector != nil {
		err = opt.HttpRequestDirector(req)
		if err != nil {
			return nil, fmt.Errorf("modifying HTTP request: %w", err)
		}
	}

	// TODO why set the Host header?
	// req.Host = opt.HostHeader
	// req.Host = "localhost"

	httpClient := http.DefaultClient

	// Fetch directory listing
	// resp, err := http.Get(httpStorage.Opts.MetadataURL)
	resp, err := httpClient.Do(req)

	return resp, err
}

// Fetch and load .torrent files
// TODO remove torrents when they are no longer in the directory listing
func fetchAndLoadTorrents(client *torrent.Client) error {
	// panics if DefaultStorage has wrong type
	httpStorage := (client.GetConfig().DefaultStorage).(*storage.HttpStorageImpl)

	// Ensure cache directory exists
	if err := os.MkdirAll(httpStorage.Opts.MetadataCacheDir, 0755); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}

	opt := httpStorage.Opts

	// FIXME retry requests on timeout
	httpTimeout := time.Duration(opt.HTTPTimeoutSeconds) * time.Second
	httpContext, httpContextCancel := context.WithTimeout(context.Background(), httpTimeout)
	defer httpContextCancel()

	// Fetch directory listing
	// resp, err := http.Get(httpStorage.Opts.MetadataURL)
	// resp, err := httpClient.Do(req)
	resp, err := httpGet(httpContext, opt, httpStorage.Opts.MetadataURL)
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
			resp, err := httpGet(httpContext, opt, torrentURL)
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

func getTorrentClientConfigV2() {
	configFile := flag.String("config", "", "Load configuration from YAML file")
	writeFile := flag.String("write-config", "", "Write default configuration to YAML file")
	help := flag.Bool("help", false, "Show help message")

	flag.Parse()

	if *help {
		flag.Usage()
		return
	}

	switch {
	case *writeFile != "":
		cfg := torrent.NewDefaultClientConfig()
		if err := WriteConfig(*writeFile, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Default config written to %s\n", *writeFile)
		return

	case *configFile != "":
		cfg, err := ReadConfig(*configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Loaded config: %+v\n", cfg)
		return

	default:
		flag.Usage()
		return
	}

	if false {
		main()
	}
}

func printStructFields(v interface{}) {
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		fmt.Printf("%s = %v\n", typ.Field(i).Name, val.Field(i).Interface())
	}
}

func setStructField(v interface{}, field string, value interface{}) {
	val := reflect.ValueOf(v).Elem() // must be a pointer
	f := val.FieldByName(field)
	if f.IsValid() && f.CanSet() {
		f.Set(reflect.ValueOf(value))
	}
}

// WriteConfig writes the current config to a YAML file.
func WriteConfig(filename string, cfg *torrent.ClientConfig) error {
	// data, err := yaml.Marshal(cfg)
	data, err := yaml.Marshal(filterMarshalable(cfg))
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

// type FunctionDescriptor struct {
// 	Type   string
// 	Params map[string]interface{}
// }

// type ClientConfig struct {
// 	ClientDhtConfig           // embedded → flattened
// 	ListenPort     int
// 	SecretToken    string                // blacklisted
// 	OnPeerConnect  func(string) error    // not serializable
// 	OnPeerFuncDesc *FunctionDescriptor   // descriptor for above
// }

var torrentConfigFieldBlacklist = map[string]bool{
	"Callbacks":         true,
	"Extensions":        true,
	"MinPeerExtensions": true,
}

// Recursively copy v into a marshal-safe version (no funcs, chans, complex types)
// Recursive filter with:
// - Embedded struct flattening
// - Field blacklisting
// - Function field skipping
func filterMarshalable(v interface{}) interface{} {
	val := reflect.ValueOf(v)
	if !val.IsValid() {
		return nil
	}

	switch val.Kind() {
	case reflect.Ptr:
		if val.IsNil() {
			return nil
		}
		return filterMarshalable(val.Elem().Interface())

	case reflect.Struct:
		typ := val.Type()
		out := make(map[string]interface{})

		for i := 0; i < val.NumField(); i++ {
			f := val.Field(i)
			ft := typ.Field(i)

			if ft.PkgPath != "" { // unexported
				continue
			}
			if torrentConfigFieldBlacklist[ft.Name] { // blacklisted
				continue
			}
			switch f.Kind() {
			case reflect.Func, reflect.Chan, reflect.Interface, reflect.UnsafePointer:
				continue
			}

			// Flatten embedded structs
			if ft.Anonymous && f.Kind() == reflect.Struct {
				embedded := filterMarshalable(f.Interface())
				if submap, ok := embedded.(map[string]interface{}); ok {
					for k, v := range submap {
						out[k] = v
					}
				}
				continue
			}

			out[ft.Name] = filterMarshalable(f.Interface())
		}
		return out

	case reflect.Slice, reflect.Array:
		out := make([]interface{}, val.Len())
		for i := 0; i < val.Len(); i++ {
			out[i] = filterMarshalable(val.Index(i).Interface())
		}
		return out

	case reflect.Map:
		out := make(map[interface{}]interface{})
		for _, key := range val.MapKeys() {
			out[key.Interface()] = filterMarshalable(val.MapIndex(key).Interface())
		}
		return out

	default:
		return v
	}
}

// ReadConfig reads YAML config and merges it into defaults.
func ReadConfig(filename string) (*torrent.ClientConfig, error) {
	cfg := torrent.NewDefaultClientConfig()
	data, err := os.ReadFile(filename)
	if err != nil {
		return cfg, err
	}

	// Unmarshal into a map[string]interface{} first
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return cfg, err
	}

	val := reflect.ValueOf(&cfg).Elem()
	for key, value := range m {
		field := val.FieldByNameFunc(func(name string) bool {
			return name == key // case-sensitive (simple version)
		})
		if field.IsValid() && field.CanSet() {
			v := reflect.ValueOf(value)
			if v.Type().ConvertibleTo(field.Type()) {
				field.Set(v.Convert(field.Type()))
			}
		}
	}
	return cfg, nil
}
