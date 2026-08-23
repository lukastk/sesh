package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// ShellNew posts POST /v1/shells (record a tracked tmux session).
func (c *Client) ShellNew(ctx context.Context, req api.NewShellRequest) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/shells", req, &out)
}

// ShellPromote posts POST /v1/shells/promote (adopt an existing untracked session).
func (c *Client) ShellPromote(ctx context.Context, req api.PromoteShellRequest) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/shells/promote", req, &out)
}

// ShellSessions gets GET /v1/shells/sessions (every live session on this
// daemon's work server, classified).
func (c *Client) ShellSessions(ctx context.Context) (api.ShellSessionsResponse, error) {
	var out api.ShellSessionsResponse
	return out, c.getJSON(ctx, "http://unix/v1/shells/sessions", &out)
}

// ShellInfo gets GET /v1/shells/info?id=... (locator + live window/pane tree).
func (c *Client) ShellInfo(ctx context.Context, id string) (api.ShellInfoResponse, error) {
	var out api.ShellInfoResponse
	return out, c.getJSON(ctx, "http://unix/v1/shells/info?id="+url.QueryEscape(id), &out)
}
