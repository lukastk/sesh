package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// FsList fetches GET /v1/fs/list?path= — the immediate subdirectories of an
// allow-listed, home-rooted path on the targeted daemon's host.
func (c *Client) FsList(ctx context.Context, path string) (api.FsListResponse, error) {
	var out api.FsListResponse
	return out, c.getJSON(ctx, "http://unix/v1/fs/list?path="+url.QueryEscape(path), &out)
}
