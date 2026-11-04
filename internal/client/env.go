package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// ThreadEnv gets GET /v1/threads/env — a thread's environment variables.
func (c *Client) ThreadEnv(ctx context.Context, id string) (api.ThreadEnvResponse, error) {
	var out api.ThreadEnvResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads/env?id="+url.QueryEscape(id), &out)
}

// SetThreadEnv posts POST /v1/threads/env — change a thread's environment variables.
func (c *Client) SetThreadEnv(ctx context.Context, req api.SetThreadEnvRequest) (api.SetThreadEnvResponse, error) {
	var out api.SetThreadEnvResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/env", req, &out)
}

// RestartThread posts POST /v1/threads/restart — replace a thread's running agent
// with a new process on the same conversation.
func (c *Client) RestartThread(ctx context.Context, req api.RestartThreadRequest) (api.RestartThreadResponse, error) {
	var out api.RestartThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/restart", req, &out)
}
