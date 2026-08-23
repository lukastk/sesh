package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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

// ThreadHold posts POST /v1/threads/hold: park the thread until onHoldUntilUnix
// (0 = clear the hold). The caller supplies the absolute instant.
func (c *Client) ThreadHold(ctx context.Context, id string, onHoldUntilUnix int64) error {
	return c.postJSON(ctx, "http://unix/v1/threads/hold", api.HoldThreadRequest{ID: id, OnHoldUntilUnix: onHoldUntilUnix}, nil)
}

// ThreadReportState posts POST /v1/threads/report-state — an in-agent
// reporter's turn-lifecycle fact (schema 43, _dev/STATE_AUTHORITY.md).
func (c *Client) ThreadReportState(ctx context.Context, req api.ReportStateRequest) error {
	return c.postJSON(ctx, "http://unix/v1/threads/report-state", req, nil)
}

// ThreadFlag posts POST /v1/threads/flag (schema 44): on|off|disable|enable.
func (c *Client) ThreadFlag(ctx context.Context, id, action string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/flag", api.FlagThreadRequest{ID: id, Action: action}, nil)
}

// ThreadWait performs ONE bounded server-owned wait (schema 43): the daemon
// polls its maintained state until the condition or timeoutMs (server-capped
// at 10s). Callers loop until their own deadline.
func (c *Client) ThreadWait(ctx context.Context, id, until string, timeoutMs int) (api.ThreadWaitResponse, error) {
	var out api.ThreadWaitResponse
	u := "http://unix/v1/threads/wait?id=" + url.QueryEscape(id) +
		"&until=" + url.QueryEscape(until) + "&timeout_ms=" + strconv.Itoa(timeoutMs)
	return out, c.getJSON(ctx, u, &out)
}

// ThreadReparent posts POST /v1/threads/reparent (” parent = make root).
func (c *Client) ThreadReparent(ctx context.Context, id, parent string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/reparent", api.ReparentThreadRequest{ID: id, Parent: parent}, nil)
}

// ThreadPin posts POST /v1/threads/pin: pin/reposition the thread at order (a
// non-nil absolute fractional key) or, with a nil order, un-pin it. The caller
// computes the float from the merged cross-machine view.
func (c *Client) ThreadPin(ctx context.Context, id string, order *float64) error {
	return c.postJSON(ctx, "http://unix/v1/threads/pin", api.PinThreadRequest{ID: id, PinOrder: order}, nil)
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
	return c.ThreadStopForce(ctx, id, false)
}

// ThreadStopForce is ThreadStop with the SHELL-thread override: killing a shell
// thread kills its whole tmux session, so one hosting other threads' agent panes
// refuses unless force is set.
func (c *Client) ThreadStopForce(ctx context.Context, id string, force bool) error {
	return c.postJSON(ctx, "http://unix/v1/threads/stop", api.StopThreadRequest{ID: id, Force: force}, nil)
}

// ThreadDelete posts POST /v1/threads/delete (drop the record). force drops a
// live thread's record anyway (orphaning its agent).
func (c *Client) ThreadDelete(ctx context.Context, id string, force bool) error {
	return c.postJSON(ctx, "http://unix/v1/threads/delete", api.DeleteThreadRequest{ID: id, Force: force}, nil)
}

// Snapshot fetches GET /v1/snapshot — this machine's threads with their live state,
// served from the daemon's background maintainer (an O(1) read, never a probe).
func (c *Client) Snapshot(ctx context.Context) (api.MachineSnapshot, error) {
	snap, _, _, err := c.SnapshotConditional(ctx, "", "")
	return snap, err
}

// SnapshotConditional fetches GET /v1/snapshot conditionally (issue #1):
//   - since (a cursor from a previous response's Generation) asks a schema-41
//     daemon for a DELTA — only the rows changed since that cursor (snap.Delta
//     true, snap.Removed for deletions). An older daemon ignores it and serves
//     the full payload (snap.Generation empty).
//   - etag (from a previous fetch's ETag) is sent as If-None-Match; a daemon
//     whose full payload is byte-unchanged answers 304 with no body
//     (notModified=true, zero snapshot). Pre-schema-40 daemons ignore it.
//
// Callers use one mode at a time: the mesh sync sends `since` once it holds a
// cursor, else `etag`.
func (c *Client) SnapshotConditional(ctx context.Context, etag, since string) (snap api.MachineSnapshot, newETag string, notModified bool, err error) {
	u := "http://unix/v1/snapshot"
	if since != "" {
		u += "?since=" + url.QueryEscape(since)
	}
	req, err := c.req(ctx, http.MethodGet, u, nil)
	if err != nil {
		return snap, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return snap, "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return snap, etag, true, nil
	case http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
			return snap, "", false, err
		}
		return snap, resp.Header.Get("ETag"), false, nil
	default:
		return snap, "", false, fmt.Errorf("client: snapshot returned %d", resp.StatusCode)
	}
}

// Mesh fetches GET /v1/mesh — the merged cross-machine view (this machine's live
// snapshot + every cached peer, with staleness), read locally from the cache.
func (c *Client) Mesh(ctx context.Context) (api.MeshSnapshot, error) {
	var out api.MeshSnapshot
	return out, c.getJSON(ctx, "http://unix/v1/mesh", &out)
}

// MeshNudge posts POST /v1/mesh/nudge (schema 45): ask the LOCAL daemon to
// re-sync its cached snapshot of `machine` immediately — called after a routed
// write to that peer so the change shows in local reads in ~an RTT.
func (c *Client) MeshNudge(ctx context.Context, machine string) error {
	var out map[string]any
	return c.postJSON(ctx, "http://unix/v1/mesh/nudge", api.MeshNudgeRequest{Machine: machine}, &out)
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

// ThreadRealize posts POST /v1/threads/realize (convert a VIRTUAL grouping
// thread in place into a real, never-started headless thread).
func (c *Client) ThreadRealize(ctx context.Context, req api.RealizeThreadRequest) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/realize", req, &out)
}

// ThreadPane fetches GET /v1/threads/pane?id= (thread.resolve-pane).
func (c *Client) ThreadPane(ctx context.Context, id string) (api.ResolvePaneResponse, error) {
	var out api.ResolvePaneResponse
	return out, c.getJSON(ctx, "http://unix/v1/threads/pane?id="+url.QueryEscape(id), &out)
}

// ThreadCapture fetches GET /v1/threads/capture?id=&lines= — the live text of a
// thread's tmux pane. lines==0 captures only the visible area; lines>0 captures the
// last N lines (including scrollback). Backs `sesh thread capture`.
func (c *Client) ThreadCapture(ctx context.Context, id string, lines int) (api.ThreadCaptureResponse, error) {
	var out api.ThreadCaptureResponse
	u := "http://unix/v1/threads/capture?id=" + url.QueryEscape(id)
	if lines > 0 {
		u += "&lines=" + strconv.Itoa(lines)
	}
	return out, c.getJSON(ctx, u, &out)
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

// ThreadSendTo is ThreadSend addressing a SPECIFIC pane of a shell thread's
// session (pane id, or a window index meaning that window's active pane). Empty
// pane + nil window is the session's active pane.
func (c *Client) ThreadSendTo(ctx context.Context, id, text, pane string, window *int) error {
	return c.postJSON(ctx, "http://unix/v1/threads/send",
		api.ThreadSendRequest{ID: id, Text: text, Pane: pane, Window: window}, nil)
}

// ThreadSendHeadless posts POST /v1/threads/send-headless (deliver a turn to a
// headless thread; runs in the background).
func (c *Client) ThreadSendHeadless(ctx context.Context, id, text string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/send-headless", api.ThreadSendRequest{ID: id, Text: text}, nil)
}

// ThreadSendHeadlessMode is ThreadSendHeadless with a [spawn]-mode override
// for the turn (” = the config default).
func (c *Client) ThreadSendHeadlessMode(ctx context.Context, id, text, mode string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/send-headless", api.ThreadSendRequest{ID: id, Text: text, Mode: mode}, nil)
}

// ThreadSendHeadlessModel is ThreadSendHeadless with a per-turn model override
// (” = the thread's pinned model / the agent default).
func (c *Client) ThreadSendHeadlessModel(ctx context.Context, id, text, model string) error {
	return c.postJSON(ctx, "http://unix/v1/threads/send-headless", api.ThreadSendRequest{ID: id, Text: text, Model: model}, nil)
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

// ThreadAdopt posts POST /v1/threads/adopt. With a pane it adopts a live agent
// (sessionID, when non-empty, asserts its conversation id, bypassing
// auto-detection). With an empty pane it is a HEADLESS adopt: sessionID +
// agentKind are required and register an existing, not-running conversation as a
// durable headless thread in cwd.
func (c *Client) ThreadAdopt(ctx context.Context, pane, name, sessionID, agentKind, cwd string) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/adopt", api.AdoptThreadRequest{Pane: pane, Name: name, SessionID: sessionID, AgentKind: agentKind, Cwd: cwd}, &out)
}

// ThreadMeta posts POST /v1/threads/meta (” value deletes the key).
func (c *Client) ThreadMeta(ctx context.Context, id, key, value string) (api.ThreadResponse, error) {
	var out api.ThreadResponse
	return out, c.postJSON(ctx, "http://unix/v1/threads/meta", api.MetaThreadRequest{ID: id, Key: key, Value: value}, &out)
}

// ThreadImport posts POST /v1/threads/import (raw record insert; v1 migration).
func (c *Client) ThreadImport(ctx context.Context, th api.Thread) error {
	return c.postJSON(ctx, "http://unix/v1/threads/import", th, nil)
}
