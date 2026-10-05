package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

func (c *Client) PiCommand(ctx context.Context, req api.PiCommandRequest) (api.PiCommandResponse, error) {
	var out api.PiCommandResponse
	err := c.postJSON(ctx, "http://unix/v1/threads/command", req, &out)
	return out, err
}
func (c *Client) PiOperation(ctx context.Context, id, requestID string) (api.PiCommandResponse, error) {
	var out api.PiCommandResponse
	err := c.getJSON(ctx, "http://unix/v1/threads/command?id="+url.QueryEscape(id)+"&request_id="+url.QueryEscape(requestID), &out)
	return out, err
}
