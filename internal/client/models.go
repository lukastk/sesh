package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// ModelDefaults gets GET /v1/models/defaults — every agent's configured default model.
func (c *Client) ModelDefaults(ctx context.Context) (api.ModelDefaultsResponse, error) {
	var out api.ModelDefaultsResponse
	return out, c.getJSON(ctx, "http://unix/v1/models/defaults", &out)
}

// SetModelDefault posts POST /v1/models/default — write (model != "") or
// remove (model == "") one agent's default model in the daemon's config.toml.
func (c *Client) SetModelDefault(ctx context.Context, agent, model string) (api.ModelDefaultsResponse, error) {
	var out api.ModelDefaultsResponse
	return out, c.postJSON(ctx, "http://unix/v1/models/default", api.SetModelDefaultRequest{Agent: agent, Model: model}, &out)
}

// ModelList gets GET /v1/models — the models the daemon's machine can use for
// one harness, as the harness reports them. refresh bypasses the daemon's cache.
func (c *Client) ModelList(ctx context.Context, agent string, refresh bool) (api.ModelListResponse, error) {
	var out api.ModelListResponse
	u := "http://unix/v1/models?agent=" + url.QueryEscape(agent)
	if refresh {
		u += "&refresh=1"
	}
	return out, c.getJSON(ctx, u, &out)
}
