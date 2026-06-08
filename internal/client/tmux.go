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

// TmuxCreatePane posts POST /v1/tmux/panes.
func (c *Client) TmuxCreatePane(ctx context.Context, req api.CreatePaneRequest) (api.CreatePaneResponse, error) {
	var out api.CreatePaneResponse
	return out, c.postJSON(ctx, "http://unix/v1/tmux/panes", req, &out)
}

// TmuxSendText posts POST /v1/tmux/send-text.
func (c *Client) TmuxSendText(ctx context.Context, req api.SendTextRequest) error {
	return c.postJSON(ctx, "http://unix/v1/tmux/send-text", req, nil)
}

// TmuxStageFile posts POST /v1/tmux/stage-file and returns the staged path.
func (c *Client) TmuxStageFile(ctx context.Context, name string, content []byte) (api.StageFileResponse, error) {
	var out api.StageFileResponse
	return out, c.postJSON(ctx, "http://unix/v1/tmux/stage-file", api.StageFileRequest{Name: name, Content: content}, &out)
}

func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
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
