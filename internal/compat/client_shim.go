package compat

import (
	"context"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// torrentShim adapts *storage.Torrent to storage.TorrentImpl
type torrentShim struct {
	inner *storage.Torrent
}

func (t *torrentShim) Piece(index int) storage.PieceImpl {
	return t.inner.Piece(index)
}

func (t *torrentShim) Close() error {
	return t.inner.Close()
}

// ClientShim wraps *storage.Client and exposes storage.ClientImpl
type ClientShim struct {
	inner *storage.Client
}

func NewClientShim(inner *storage.Client) *ClientShim {
	return &ClientShim{inner: inner}
}

func (c *ClientShim) OpenTorrent(ctx context.Context, info *metainfo.Info, ih metainfo.Hash) (storage.TorrentImpl, error) {
	t, err := c.inner.OpenTorrent(ctx, info, ih)
	if err != nil {
		return nil, err
	}
	return &torrentShim{inner: t}, nil
}

func (c *ClientShim) Close() error {
	return nil
}
