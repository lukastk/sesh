package client

import (
	"context"

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
