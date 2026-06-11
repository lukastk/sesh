package client

import (
	"context"
	"strconv"
	"net/url"
	"strings"

	"github.com/lukastk/sesh/internal/api"
)

// ThreadNew posts POST /v1/threads.
func (c *Client) ThreadNew(ctx context.Context, req api.NewThreadRequest) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads", req, &out)
}

// ThreadList fetches GET /v1/threads. includeArchived also returns parked
// threads; allMachines fans out across the mesh (this machine + every peer).
func (c *Client) ThreadList(ctx context.Context, includeArchived, allMachines bool) (api.ThreadListResponse, error) {
	var out api.ThreadListResponse
	q := []string{}
	if includeArchived {
		q = append(q, "archived=1")
	}
	if allMachines {
		q = append(q, "all-machines=1")
	}
	u := "http://unix/v1/threads"
	if len(q) > 0 {
		u += "?" + strings.Join(q, "&")
	}
	return out, c.getJSON(ctx, u, &out)
}

// ThreadNotify posts POST /v1/threads/notify (the per-thread gate).
func (c *Client) ThreadNotify(ctx context.Context, id string, on bool) error {
	return c.postJSON(ctx, "http://unix/v1/threads/notify", api.NotifyThreadRequest{ID: id, On: on}, nil)
}

// ThreadReparent posts POST /v1/threads/reparent ('' parent = make root).
func (c *Client) ThreadReparent(ctx context.Context, id, parent string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/reparent", api.ReparentThreadRequest{ID: id, Parent: parent}, nil)
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

// ThreadStop posts POST /v1/threads/stop (end the runtime, keep the record).
func (c *Client) ThreadStop(ctx context.Context, id string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/stop", api.StopThreadRequest{ID: id}, nil)
}

// ThreadDelete posts POST /v1/threads/delete (drop the record). force drops a
// live thread's record anyway (orphaning its agent).
func (c *Client) ThreadDelete(ctx context.Context, id string, force bool) error {
	return c.postJSON(ctx, "http://unix/v1/threads/delete", api.DeleteThreadRequest{ID: id, Force: force}, nil)
}

// Snapshot fetches GET /v1/snapshot — this machine's threads with their live state,
// served from the daemon's background maintainer (an O(1) read, never a probe).
func (c *Client) Snapshot(ctx context.Context) (api.MachineSnapshot, error) {
	var out api.MachineSnapshot
	return out, c.getJSON(ctx, "http://unix/v1/snapshot", &out)
}

// Mesh fetches GET /v1/mesh — the merged cross-machine view (this machine's live
// snapshot + every cached peer, with staleness), read locally from the cache.
func (c *Client) Mesh(ctx context.Context) (api.MeshSnapshot, error) {
	var out api.MeshSnapshot
	return out, c.getJSON(ctx, "http://unix/v1/mesh", &out)
}

// ThreadGrid fetches GET /v1/threads/grid — every thread with live status.
func (c *Client) ThreadGrid(ctx context.Context, includeArchived, allMachines bool) (api.ThreadGridResponse, error) {
	var out api.ThreadGridResponse
	q := []string{}
	if includeArchived {
		q = append(q, "archived=1")
	}
	if allMachines {
		q = append(q, "all-machines=1")
	}
	u := "http://unix/v1/threads/grid"
	if len(q) > 0 {
		u += "?" + strings.Join(q, "&")
	}
	return out, c.getJSON(ctx, u, &out)
}

// ThreadResume posts POST /v1/threads/resume (revive a dead headed thread).
func (c *Client) ThreadResume(ctx context.Context, id string) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/resume", api.ThreadResumeRequest{ID: id}, &out)
}

// ThreadHeadful posts POST /v1/threads/headful (promote a live headless thread into a
// headed tmux pane).
func (c *Client) ThreadHeadful(ctx context.Context, id string) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/headful", api.ThreadHeadfulRequest{ID: id}, &out)
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

// ThreadSendHeadlessMode is ThreadSendHeadless with a [spawn]-mode override
// for the turn ('' = the config default).
func (c *Client) ThreadSendHeadlessMode(ctx context.Context, id, text, mode string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/send-headless", api.ThreadSendRequest{ID: id, Text: text, Mode: mode}, nil)
}

// ThreadHeadlessReply fetches GET /v1/threads/headless-reply?id=.
func (c *Client) ThreadHeadlessReply(ctx context.Context, id string) (api.HeadlessReplyResponse, error) {
	var out api.HeadlessReplyResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads/headless-reply?id="+url.QueryEscape(id), &out)
}

// ThreadTranscript fetches GET /v1/threads/transcript?id=&tail= (tail < 0 =
// the whole transcript).
func (c *Client) ThreadTranscript(ctx context.Context, id string, tail int) (api.TranscriptResponse, error) {
	var out api.TranscriptResponse
	url := "http://unix/v1/threads/transcript?id=" + id
	if tail >= 0 {
		url += "&tail=" + strconv.Itoa(tail)
	}
	return out, c.getJSON(ctx, url, &out)
}

// ThreadAdopt posts POST /v1/threads/adopt.
func (c *Client) ThreadAdopt(ctx context.Context, pane, name string) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/adopt", api.AdoptThreadRequest{Pane: pane, Name: name}, &out)
}

// ThreadMeta posts POST /v1/threads/meta ('' value deletes the key).
func (c *Client) ThreadMeta(ctx context.Context, id, key, value string) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/meta", api.MetaThreadRequest{ID: id, Key: key, Value: value}, &out)
}

// ThreadImport posts POST /v1/threads/import (raw record insert; v1 migration).
func (c *Client) ThreadImport(ctx context.Context, th api.Thread) error {
	return c.postJSON(ctx, "http://unix/v1/threads/import", th, nil)
}
