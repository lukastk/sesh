package client

import (
	"context"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// ThreadNew posts POST /v1/threads.
func (c *Client) ThreadNew(ctx context.Context, req api.NewThreadRequest) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads", req, &out)
}

// ThreadList fetches GET /v1/threads.
func (c *Client) ThreadList(ctx context.Context) (api.ThreadListResponse, error) {
	var out api.ThreadListResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads", &out)
}

// ThreadKill posts POST /v1/threads/kill?id=.
func (c *Client) ThreadKill(ctx context.Context, id string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/kill?id="+url.QueryEscape(id), struct{}{}, nil)
}

// ThreadPane fetches GET /v1/threads/pane?id= (thread.resolve-pane).
func (c *Client) ThreadPane(ctx context.Context, id string) (api.ResolvePaneResponse, error) {
	var out api.ResolvePaneResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads/pane?id="+url.QueryEscape(id), &out)
}

// ThreadStatus fetches GET /v1/threads/status?id= (thread.runtime-state).
func (c *Client) ThreadStatus(ctx context.Context, id string) (api.ThreadStatusResponse, error) {
	var out api.ThreadStatusResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads/status?id="+url.QueryEscape(id), &out)
}

// ThreadSend posts POST /v1/threads/send (headful send into the live pane).
func (c *Client) ThreadSend(ctx context.Context, id, text string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/send", api.ThreadSendRequest{ID: id, Text: text}, nil)
}

// ThreadSendHeadless posts POST /v1/threads/send-headless (deliver a turn to a
// headless thread; runs in the background).
func (c *Client) ThreadSendHeadless(ctx context.Context, id, text string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/send-headless", api.ThreadSendRequest{ID: id, Text: text}, nil)
}

// ThreadHeadlessReply fetches GET /v1/threads/headless-reply?id=.
func (c *Client) ThreadHeadlessReply(ctx context.Context, id string) (api.HeadlessReplyResponse, error) {
	var out api.HeadlessReplyResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads/headless-reply?id="+url.QueryEscape(id), &out)
}
