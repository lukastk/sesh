package client

import (
	"context"

	"github.com/lukastk/sesh/internal/api"
)

// UIConfigGet fetches GET /v1/ui-config — the daemon's sesh-ui UI preferences
// (<SESH_HOME>/ui_config.toml), with defaults applied for missing keys.
func (c *Client) UIConfigGet(ctx context.Context) (api.UIConfigResponse, error) {
	var out api.UIConfigResponse
	return out, c.getJSON(ctx, "http://unix/v1/ui-config", &out)
}

// UIConfigSet posts POST /v1/ui-config — replace the UI config; returns the saved config.
func (c *Client) UIConfigSet(ctx context.Context, cfg api.UIConfig) (api.UIConfigResponse, error) {
	var out api.UIConfigResponse
	return out, c.postJSON(ctx, "http://unix/v1/ui-config", cfg, &out)
}
