package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/lukastk/sesh/internal/api"
)

// TmuxInfo fetches GET /v1/tmux/info, optionally filtered to one session.
func (c *Client) TmuxInfo(ctx context.Context, session string) (api.TmuxInfoResponse, error) {
	var out api.TmuxInfoResponse
	u := "http://unix/v1/tmux/info"
	if session != "" {
		u += "?session=" + url.QueryEscape(session)
	}
	return out, c.getJSON(ctx, u, &out)
}

// TmuxCreateSession posts POST /v1/tmux/sessions.
func (c *Client) TmuxCreateSession(ctx context.Context, req api.CreateSessionRequest) (api.CreateSessionResponse, error) {
	var out api.CreateSessionResponse
	return out, c.postJSON(ctx, "http://unix/v1/tmux/sessions", req, &out)
}

// TmuxKillSession posts POST /v1/tmux/kill-session.
func (c *Client) TmuxKillSession(ctx context.Context, name string) error {
	return c.postJSON(ctx, "http://unix/v1/tmux/kill-session", api.KillSessionRequest{Name: name}, nil)
}

// TmuxCreatePane posts POST /v1/tmux/panes.
func (c *Client) TmuxCreatePane(ctx context.Context, req api.CreatePaneRequest) (api.CreatePaneResponse, error) {
	var out api.CreatePaneResponse
	return out, c.postJSON(ctx, "http://unix/v1/tmux/panes", req, &out)
}

// TmuxSendText posts POST /v1/tmux/send-text.
func (c *Client) TmuxSendText(ctx context.Context, req api.SendTextRequest) error {
	return c.postJSON(ctx, "http://unix/v1/tmux/send-text", req, nil)
}

// TmuxNav posts POST /v1/tmux/nav: the inner switch-client run by the daemon that
// owns the work tmux server. Used by the http-transport nav path (no ssh hop).
func (c *Client) TmuxNav(ctx context.Context, req api.NavRequest) error {
	return c.postJSON(ctx, "http://unix/v1/tmux/nav", req, nil)
}

// TmuxMasterCurrent fetches GET /v1/tmux/master-current?origin=X[&machine=M] — what the
// origin master's window is currently showing: the active pane's thread id, the client's
// session name, and its window index (session "" / window -1 = no live master client).
// machine == "" resolves on THIS daemon's work server; machine == a peer asks that daemon
// (which owns the work server) to resolve it — the daemon routes over its warm mesh
// connection so the caller never forks a `sesh … --machine` subprocess.
func (c *Client) TmuxMasterCurrent(ctx context.Context, origin, machine string) (session, threadID string, window int, err error) {
	var out api.MasterCurrentResponse
	u := "http://unix/v1/tmux/master-current?origin=" + url.QueryEscape(origin)
	if machine != "" {
		u += "&machine=" + url.QueryEscape(machine)
	}
	if err := c.getJSON(ctx, u, &out); err != nil {
		return "", "", -1, err
	}
	return out.Session, out.ThreadID, out.Window, nil
}

// TmuxStageFile posts POST /v1/tmux/stage-file and returns the staged path.
func (c *Client) TmuxStageFile(ctx context.Context, name string, content []byte) (api.StageFileResponse, error) {
	var out api.StageFileResponse
	return out, c.postJSON(ctx, "http://unix/v1/tmux/stage-file", api.StageFileRequest{Name: name, Content: content}, &out)
}

func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	req, err := c.req(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeOrError(resp, out)
}

func (c *Client) postJSON(ctx context.Context, u string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := c.req(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeOrError(resp, out)
}

// decodeOrError decodes a success body into out (if non-nil), or turns a non-2xx
// response into the daemon's structured error — loud, not a swallowed status.
func decodeOrError(resp *http.Response, out any) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e api.ErrorResponse
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return fmt.Errorf("daemon: %s (HTTP %d)", e.Error, resp.StatusCode)
		}
		return fmt.Errorf("daemon: HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
