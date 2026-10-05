package daemon

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/lukastk/sesh/internal/agents/pi"
	"github.com/lukastk/sesh/internal/api"
)

// openPi resolves the thread's recorded session and verifies its socket is inside
// the marked pane. Never fall back to terminal input: that would submit the draft.
func (d *Daemon) openPi(th api.Thread, pane string) (*pi.Client, error) {
	path, ok := piRPCSocketPath(th.AgentSessionID)
	if !ok {
		return nil, fmt.Errorf("Pi RPC socket missing for thread %s; ensure pi-rpc-socket is loaded (run /reload in Pi); nothing pasted", th.ID)
	}
	c, err := pi.Dial(path)
	if err != nil {
		return nil, fmt.Errorf("Pi RPC connect: %w; nothing pasted", err)
	}
	if err = c.CheckPane(pane, d.tmux.SocketPath()); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// deliverHeaded is the ONE transport selector, also used by initial --msg sends.
func (d *Daemon) deliverHeaded(th api.Thread, pane, text string) error {
	if th.AgentKind != "pi" {
		return d.tmux.SendText(pane, text, true)
	}
	c, err := d.openPi(th, pane)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Message(text); err != nil {
		return fmt.Errorf("Pi RPC delivery: %w; delivery may be uncertain, do not automatically retry; nothing pasted", err)
	}
	return nil
}

// Command requests are short: the extension retains their outcome, so long
// compactions never hold the daemon routing proxy or HTTP client past 15 seconds.
func (d *Daemon) handlePiCommand(w http.ResponseWriter, r *http.Request) {
	var req api.PiCommandRequest
	if r.Method == http.MethodPost {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			writeError(w, 400, "command text is required")
			return
		}
		if _, err := uuid.Parse(req.RequestID); err != nil {
			writeError(w, 400, "request_id must be a UUID")
			return
		}
	} else {
		req.ID = r.URL.Query().Get("id")
		req.RequestID = r.URL.Query().Get("request_id")
		if req.RequestID == "" {
			writeError(w, 400, "request_id is required")
			return
		}
	}
	if req.ID == "" {
		writeError(w, 400, "thread id is required")
		return
	}
	th, err := d.store.GetThread(req.ID)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	if th.AgentKind != "pi" {
		writeError(w, 400, "thread command is Pi-only; other agents use terminal input via thread send")
		return
	}
	loc, found, err := d.tmux.FindPaneByThreadID(th.ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if !found {
		writeError(w, 409, "Pi thread has no live pane; command outcome may be lost if it exited")
		return
	}
	c, err := d.openPi(th, loc.Pane)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	defer c.Close()
	if err := c.RequireCommands(); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	var op api.PiOperation
	if r.Method == http.MethodPost {
		op, err = c.Command(req.RequestID, req.Text)
	} else {
		op, err = c.Operation(req.RequestID)
	}
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, api.PiCommandResponse{Schema: api.SchemaVersion, RequestID: req.RequestID, Operation: op})
}
