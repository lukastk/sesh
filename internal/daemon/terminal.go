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
// socket (binary frames = terminal bytes both ways; a `{"type":"resize",…}` text frame
// resizes the pty). This is the daemon-native version of the bridge prototyped in
// _dev/experiments/01_live_terminal_bridge — agent-agnostic (works for claude/codex/pi).
//
// Detach-safety (verified in _dev/experiments/02_detach_safety): a naive second attach
// resizes the session to the smallest/latest client, shrinking the USER's real view. So
// we (a) set `window-size largest` (a smaller UI viewer can no longer shrink the window),
// and (b) attach a GROUPED viewer session (its own independent current-window, killed on
// disconnect) so selecting the thread's window never moves a user watching that session.
//
// Loud refusals: unknown id → 404; a thread with no live pane (headless/dead) → 409.
func (d *Daemon) handleThreadTerminal(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "terminal: id is required")
		return
	}
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

	// Detach-safety (a): never shrink the user's real attachment on our (often smaller) join.
	if err := d.tmuxRun("set-option", "-g", "window-size", "largest"); err != nil {
		writeError(w, http.StatusInternalServerError, "terminal: "+err.Error())
		return
	}
	// Detach-safety (b): a grouped viewer session shares the windows but has its own
	// current window, so pointing it at the thread's window never moves the user.
	viewer := fmt.Sprintf("uiterm-%s-%d", id[:8], time.Now().UnixNano())
	if err := d.tmuxRun("new-session", "-d", "-t", loc.Session, "-s", viewer, "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows)); err != nil {
		writeError(w, http.StatusInternalServerError, "terminal: create viewer session: "+err.Error())
		return
	}
	defer d.tmuxRun("kill-session", "-t", viewer) //nolint:errcheck — best-effort cleanup
	if err := d.tmuxRun("select-window", "-t", fmt.Sprintf("%s:%d", viewer, loc.Window)); err != nil {
		writeError(w, http.StatusInternalServerError, "terminal: select window: "+err.Error())
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow() //nolint:errcheck
	c.SetReadLimit(1 << 20)

	// pty running the attach to the GROUPED viewer session. TMUX is unset so the nested
	// attach is allowed.
	cmd := d.tmuxCmd("attach-session", "-t", viewer)
	cmd.Env = scrubEnv(os.Environ(), "TMUX", "TMUX_PANE")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		c.Close(websocket.StatusInternalError, "pty start failed") //nolint:errcheck
		return
	}
	defer ptmx.Close()       //nolint:errcheck
	defer cmd.Process.Kill() //nolint:errcheck
	defer cmd.Wait()         //nolint:errcheck — reap the tmux client

	ctx, cancel := context.WithCancel(r.Context())
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
