package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// BlobAdd posts POST /v1/blobs (raw bytes carried base64 in the JSON body).
func (c *Client) BlobAdd(ctx context.Context, name string, data []byte) (api.BlobResponse, error) {
	var out api.BlobResponse
	return out, c.postJSON(ctx, "http://unix/v1/blobs", api.AddBlobRequest{Name: name, Data: data}, &out)
}

// BlobList fetches GET /v1/blobs.
func (c *Client) BlobList(ctx context.Context) (api.BlobListResponse, error) {
	var out api.BlobListResponse
	return out, c.getJSON(ctx, "http://unix/v1/blobs", &out)
}

// BlobGet fetches the raw bytes of a blob plus its original name (X-Blob-Name).
func (c *Client) BlobGet(ctx context.Context, hash string) (name string, data []byte, err error) {
	req, err := c.req(ctx, http.MethodGet, "http://unix/v1/blobs/get?hash="+url.QueryEscape(hash), nil)
	if err != nil {
		return "", nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("blob get: %s: %s", resp.Status, string(body))
	}
	return resp.Header.Get("X-Blob-Name"), body, nil
}

// BlobDelete posts POST /v1/blobs/delete.
func (c *Client) BlobDelete(ctx context.Context, hash string) error {
	return c.postJSON(ctx, "http://unix/v1/blobs/delete", api.DeleteBlobRequest{Hash: hash}, nil)
}

// BlobPath fetches GET /v1/blobs/path?hash= — the blob's path on the serving daemon.
func (c *Client) BlobPath(ctx context.Context, hash string) (api.BlobPathResponse, error) {
	var out api.BlobPathResponse
	return out, c.getJSON(ctx, "http://unix/v1/blobs/path?hash="+url.QueryEscape(hash), &out)
}

// BlobExpand posts POST /v1/blobs/expand — replace @blob() tokens with paths.
func (c *Client) BlobExpand(ctx context.Context, text string) (api.ExpandBlobsResponse, error) {
	var out api.ExpandBlobsResponse
	return out, c.postJSON(ctx, "http://unix/v1/blobs/expand", api.ExpandBlobsRequest{Text: text}, &out)
}
