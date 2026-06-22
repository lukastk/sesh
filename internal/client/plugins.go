package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// PluginsList fetches GET /v1/plugins — every plugin manifest on the targeted
// daemon's host, with its capabilities.
func (c *Client) PluginsList(ctx context.Context) (api.PluginsListResponse, error) {
	var out api.PluginsListResponse
	return out, c.getJSON(ctx, "http://unix/v1/plugins", &out)
}

// PluginRun runs POST /v1/plugins/{name}/{capability} with the given field values —
// the daemon runs the configured command on its host and returns the structured
// result (mapped items for a list capability, command output for an action).
func (c *Client) PluginRun(ctx context.Context, name, capability string, fields map[string]string) (api.PluginRunResponse, error) {
	var out api.PluginRunResponse
	u := "http://unix/v1/plugins/" + url.PathEscape(name) + "/" + url.PathEscape(capability)
	return out, c.postJSON(ctx, u, api.PluginRunRequest{Fields: fields}, &out)
}
