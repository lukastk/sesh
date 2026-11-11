package client

import (
	"context"

	"github.com/lukastk/sesh/internal/api"
)

// SetIdleStop posts POST /v1/threads/idle-stop (schema 66): set a thread's own
// idle-stop override ('' = use the machine's limit, "never", or a duration).
func (c *Client) SetIdleStop(ctx context.Context, id, value string) (api.ThreadResponse, error) {
	var resp api.ThreadResponse
	err := c.postJSON(ctx, "http://unix/v1/threads/idle-stop", api.SetIdleStopRequest{ID: id, Value: value}, &resp)
	return resp, err
}

// IdleStopPreview reads GET /v1/idle-stop/preview (schema 66): what the owning
// daemon's idle-stop sweep would decide for each live thread right now.
func (c *Client) IdleStopPreview(ctx context.Context) (api.IdleStopPreview, error) {
	var out api.IdleStopPreview
	return out, c.getJSON(ctx, "http://unix/v1/idle-stop/preview", &out)
}
