package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// ThreadContext gets GET /v1/threads/context — a thread's custom context text.
func (c *Client) ThreadContext(ctx context.Context, id string) (api.ThreadContextResponse, error) {
	var out api.ThreadContextResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads/context?id="+url.QueryEscape(id), &out)
}

// SetThreadContext posts POST /v1/threads/context — set or clear a thread's context.
func (c *Client) SetThreadContext(ctx context.Context, req api.SetThreadContextRequest) (api.SetThreadContextResponse, error) {
	var out api.SetThreadContextResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/context", req, &out)
}

// DeliverThreadContext posts POST /v1/threads/context/deliver — an agent hook
// asking what context to inject now.
func (c *Client) DeliverThreadContext(ctx context.Context, req api.ThreadContextDeliverRequest) (api.ThreadContextDeliverResponse, error) {
	var out api.ThreadContextDeliverResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/context/deliver", req, &out)
}
