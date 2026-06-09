package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// ThreadNew posts POST /v1/threads.
func (c *Client) ThreadNew(ctx context.Context, req api.NewThreadRequest) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads", req, &out)
}

// ThreadList fetches GET /v1/threads.
func (c *Client) ThreadList(ctx context.Context) (api.ThreadListResponse, error) {
	var out api.ThreadListResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads", &out)
}

// ThreadKill posts POST /v1/threads/kill?id=.
func (c *Client) ThreadKill(ctx context.Context, id string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/kill?id="+url.QueryEscape(id), struct{}{}, nil)
}

// ThreadPane fetches GET /v1/threads/pane?id= (thread.resolve-pane).
func (c *Client) ThreadPane(ctx context.Context, id string) (api.ResolvePaneResponse, error) {
	var out api.ResolvePaneResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads/pane?id="+url.QueryEscape(id), &out)
}

// ThreadStatus fetches GET /v1/threads/status?id= (thread.runtime-state).
func (c *Client) ThreadStatus(ctx context.Context, id string) (api.ThreadStatusResponse, error) {
	var out api.ThreadStatusResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads/status?id="+url.QueryEscape(id), &out)
}
