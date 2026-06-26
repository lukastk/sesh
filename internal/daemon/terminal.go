package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesTerminal(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/threads/terminal", d.handleThreadTerminal)
}

// tmuxCmd builds a `tmux -L <socket> <args…>` exec.Cmd for the work server. (The
// daemon's tmux.Server.run is unexported; the terminal handler needs raw exec for the
// pty attach anyway, so it shells out directly against the same socket.)
func (d *Daemon) tmuxCmd(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-L", d.tmux.Socket()}, args...)...)
}

func (d *Daemon) tmuxRun(args ...string) error {
	var stderr bytes.Buffer
	c := d.tmuxCmd(args...)
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

// handleThreadTerminal gives the client a REAL interactive terminal onto a headful
// thread's tmux pane over a WebSocket: a pty running `tmux attach` is bridged to the
// socket (binary frames = terminal bytes both ways; a `{"type":"resize",...}` text frame
// resizes the pty). This is the daemon-native version of the bridge prototyped in
// _dev/experiments/01_live_terminal_bridge -- agent-agnostic (works for claude/codex/pi).
//
// The pty attaches DIRECTLY to the thread's real tmux session (no grouped "viewer" clone).
// Consequences, all intended for one user moving between his own machines one at a time:
// the client follows the session's own current-window, so an adjacent window the user opens
// in that session is reachable; and sizing is left entirely to the work server's conf policy
// (`window-size latest` -- the window follows whichever client is most-recently active). The
// daemon does NOT override window-size, so a small client attaching can resize the session
// to fit it -- which IS the desired "adapt to whoever is typing" behaviour. (The old design
// created a grouped uiterm-* viewer and forced `window-size largest` for detach-safety; that
// protected simultaneous multi-machine viewing at the cost of the active client not fitting,
// and a stale bridge could leave `largest` stuck -- both removed.)
//
// Loud refusals: unknown id -> 404; a thread with no live pane (headless/dead) -> 409.
func (d *Daemon) handleThreadTerminal(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "terminal: id is required")
		return
	}
	// GetThread first so an unknown id is a clean 404 (FindPaneByThreadID alone would
	// report it as 409 "no live pane", conflating "unknown" with "headless/dead").
	if _, err := d.store.GetThread(id); err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			writeError(w, http.StatusNotFound, "thread not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	loc, found, err := d.tmux.FindPaneByThreadID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusConflict, "terminal: thread "+id+" has no live pane (it is headless or dead — resume it first)")
		return
	}

	cols := atoiDefault(r.URL.Query().Get("cols"), 120)
	rows := atoiDefault(r.URL.Query().Get("rows"), 32)

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow() //nolint:errcheck
	c.SetReadLimit(1 << 20)

	// pty running a DIRECT attach to the thread's real session. TMUX is unset so the nested
	// attach is allowed. TERM is forced to xterm-256color: the supervised daemon often runs
	// with TERM unset/dumb, which the pty/tmux would otherwise inherit and break any agent
	// that clears the screen ("terminal does not support clear"). The UI terminal is xterm.js
	// (xterm-256color), so drop any inherited TERM and set the one the client actually speaks.
	// The real session was created with `-c <thread cwd>`, so a popup/new window opened inside
	// it (the work-conf `t` binding) lands in the thread cwd -- no per-bridge `-c` needed.
	cmd := d.tmuxCmd("attach-session", "-t", loc.Session)
	cmd.Env = append(scrubEnv(os.Environ(), "TMUX", "TMUX_PANE", "TERM"), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		c.Close(websocket.StatusInternalError, "pty start failed") //nolint:errcheck
		return
	}
	// Teardown IN ORDER: kill the tmux attach client, reap it, then close the pty. Three
	// separate `defer`s would run LIFO (Wait before Kill), so Wait would block forever on a
	// still-attached client and Kill would never fire — leaking the attach as a lingering
	// client on the real session. (The old code masked this with the uiterm reaper; with a
	// direct attach and no reaper, the bridge MUST tear its own attach down.)
	defer func() {
		_ = cmd.Process.Kill() // detach the client
		_ = cmd.Wait()         // reap the zombie
		_ = ptmx.Close()
	}()

	bridgePTY(r.Context(), c, ptmx)
}

// Keepalive cadence for the terminal bridge: ping the client every bridgePingInterval and
// give it bridgePingTimeout to pong. A half-open connection (a phone that slept or dropped
// its network without a close frame) is otherwise invisible until the OS TCP keepalive fires
// (~2h on Linux) -- which would strand the daemon's tmux attach for hours.
const (
	bridgePingInterval = 15 * time.Second
	bridgePingTimeout  = 10 * time.Second
)

// bridgePTY pumps a pty <-> WebSocket: pty output → binary frames; a client binary frame →
// pty input; a {"type":"resize",cols,rows} text frame resizes the pty. Returns when either
// side closes. Shared by the thread-terminal and master-terminal handlers.
func bridgePTY(parent context.Context, c *websocket.Conn, ptmx *os.File) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// pty -> WebSocket (binary terminal bytes).
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Keepalive: detect a dead/half-open client fast (see bridgePingInterval). On a failed
	// ping, cancel -> both pumps return -> the deferred pty close kills the tmux attach.
	go func() {
		defer cancel()
		t := time.NewTicker(bridgePingInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, bridgePingTimeout)
				err := c.Ping(pctx)
				pcancel()
				if err != nil {
					return
				}
			}
		}
	}()

	// WebSocket -> pty. Binary = keystrokes; a text {"type":"resize",cols,rows} resizes.
	for {
		typ, data, rerr := c.Read(ctx)
		if rerr != nil {
			return
		}
		if typ == websocket.MessageText {
			var m struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &m) == nil && m.Type == "resize" && m.Cols > 0 && m.Rows > 0 {
				pty.Setsize(ptmx, &pty.Winsize{Cols: m.Cols, Rows: m.Rows}) //nolint:errcheck
				continue
			}
		}
		if _, werr := ptmx.Write(data); werr != nil {
			return
		}
	}
}

// scrubEnv returns environ without the named keys.
func scrubEnv(environ []string, drop ...string) []string {
	out := environ[:0:0]
	for _, kv := range environ {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		skip := false
		for _, d := range drop {
			if k == d {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}
