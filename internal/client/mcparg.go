package client

import (
	"context"

	"github.com/lukastk/sesh/internal/api"
)

// ThreadMcpArgs posts POST /v1/threads/mcp-args and returns the updated thread.
func (c *Client) ThreadMcpArgs(ctx context.Context, req api.SetThreadMcpArgsRequest) (api.Thread, error) {
	var out api.ThreadResponse
	return out.Thread, c.postJSON(ctx, "http://unix/v1/threads/mcp-args", req, &out)
}
