package qb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client is a minimal wrapper to query qBittorrent's API for torrents with tag "proxy"
type Client struct {
	baseURL string
	user    string
	pass    string
	httpc   *http.Client
}

func NewClient(baseURL, user, pass string) *Client {
	return &Client{baseURL: baseURL, user: user, pass: pass, httpc: &http.Client{}}
}

// qbittorrent /api/v2/torrents/info?tags=proxy
func (c *Client) ListHashesWithTag(tag string) ([]string, error) {
	reqURL := fmt.Sprintf("%s/api/v2/torrents/info?tags=%s", c.baseURL, tag)
	req, _ := http.NewRequest("GET", reqURL, nil)
	// If auth is required, add cookie or Authorization header here.
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("qb returned %d: %s", resp.StatusCode, string(b))
	}
	var arr []struct{
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(arr))
	for _, a := range arr {
		out = append(out, a.Hash)
	}
	return out, nil
}
