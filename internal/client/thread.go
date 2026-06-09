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

// ThreadList fetches GET /v1/threads. includeArchived also returns parked threads.
func (c *Client) ThreadList(ctx context.Context, includeArchived bool) (api.ThreadListResponse, error) {
	var out api.ThreadListResponse
	u := "http://unix/v1/threads"
	if includeArchived {
		u += "?archived=1"
	}
	return out, c.getJSON(ctx, u, &out)
}

// ThreadRename posts POST /v1/threads/rename.
func (c *Client) ThreadRename(ctx context.Context, id, name string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/rename", api.RenameThreadRequest{ID: id, Name: name}, nil)
}

// ThreadTag posts POST /v1/threads/tag.
func (c *Client) ThreadTag(ctx context.Context, id string, add, remove []string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/tag", api.TagThreadRequest{ID: id, Add: add, Remove: remove}, nil)
}

// ThreadArchive posts POST /v1/threads/archive.
func (c *Client) ThreadArchive(ctx context.Context, id string, archived bool) error {
	return c.postJSON(ctx, "http://unix/v1/threads/archive", api.ArchiveThreadRequest{ID: id, Archived: archived}, nil)
}

// ThreadDelete posts POST /v1/threads/delete (drop the record only).
func (c *Client) ThreadDelete(ctx context.Context, id string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/delete", api.DeleteThreadRequest{ID: id}, nil)
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
