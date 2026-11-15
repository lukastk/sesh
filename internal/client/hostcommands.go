package client

import (
	"context"
	"net/url"
	"strconv"

	"github.com/lukastk/sesh/internal/api"
)

// HostCommands fetches GET /v1/host-commands: the targeted daemon's providers.
func (c *Client) HostCommands(ctx context.Context) (api.HostCommandsResponse, error) {
	var out api.HostCommandsResponse
	return out, c.getJSON(ctx, "http://unix/v1/host-commands", &out)
}

// HostCommandStart starts a run (POST /v1/host-commands/run).
func (c *Client) HostCommandStart(ctx context.Context, req api.HostCommandRunRequest) (api.HostCommandRun, error) {
	var out api.HostCommandRun
	return out, c.postJSON(ctx, "http://unix/v1/host-commands/run", req, &out)
}

// HostCommandRun reads a run, long-polling up to waitMs (server-capped) for it to finish.
func (c *Client) HostCommandRun(ctx context.Context, runID string, waitMs int) (api.HostCommandRun, error) {
	var out api.HostCommandRun
	q := url.Values{"id": {runID}, "wait_ms": {strconv.Itoa(waitMs)}}
	return out, c.getJSON(ctx, "http://unix/v1/host-commands/run?"+q.Encode(), &out)
}
