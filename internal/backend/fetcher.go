package backend

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"
)

// Fetcher fetches .torrent files and byte ranges for files from the backend HTTP server.
// It uses basic auth and respects a simple concurrency semaphore to avoid saturating backend.

type Fetcher struct {
	baseURL    *url.URL
	authHeader string
	client     *http.Client
	sem        chan struct{}
}

func NewFetcher(base, user, pass string, maxConcurrent int) *Fetcher {
	u, _ := url.Parse(base)
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: false}}
	return &Fetcher{
		baseURL:    u,
		authHeader: "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)),
		client:     &http.Client{Transport: tr, Timeout: 60 * time.Second},
		sem:        make(chan struct{}, maxConcurrent),
	}
}

func (f *Fetcher) GetTorrent(ctx context.Context, btih string) ([]byte, error) {
	u := *f.baseURL
	u.Path = path.Join(u.Path, fmt.Sprintf("%s.torrent", btih))
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("Authorization", f.authHeader)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get torrent %s: status %d %s", btih, resp.StatusCode, string(b))
	}
	return io.ReadAll(resp.Body)
}

// GetFileRange fetches bytes [start,end] (inclusive) from a file path under a torrent
func (f *Fetcher) GetFileRange(ctx context.Context, btih, torrentName, filePath string, start, end int64) ([]byte, error) {
	// throttle
	f.sem <- struct{}{}
	defer func(){ <-f.sem }()

	u := *f.baseURL
	// url path: /cas/btih/{btih}/{torrent_name}/{file_path}
	u.Path = path.Join(u.Path, btih, torrentName, filePath)
	// path.Join cleans slashes - ensure filePath encoding
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("Authorization", f.authHeader)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 206 && resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get range %s: status %d %s", u.String(), resp.StatusCode, string(b))
	}
	return io.ReadAll(resp.Body)
}
