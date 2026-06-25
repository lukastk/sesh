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
// we (a) force `window-size largest` FOR THE LIFETIME OF THE BRIDGE (a smaller UI viewer
// can no longer shrink the window) and wind it back to `latest` once no viewer remains —
// the override must be transient, since a permanent `largest` clobbers the cockpit conf
// and clips fullscreen TUIs (see normalizeWindowSize), and (b) attach a GROUPED viewer
// session (its own independent current-window, killed on disconnect) so selecting the
// thread's window never moves a user watching that session.
//
// Loud refusals: unknown id → 404; a thread with no live pane (headless/dead) → 409.
func (d *Daemon) handleThreadTerminal(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "terminal: id is required")
		return
	}
	th, err := d.store.GetThread(id)
	if err != nil {
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

	// Detach-safety (b): a grouped viewer session shares the windows but has its own
	// current window, so pointing it at the thread's window never moves the user.
	//
	// Register the viewer BEFORE creating it so the reaper (which kills any uiterm-*
	// it does not know about) can never race-kill a live bridge: by the time the
	// session is visible to tmux it is already in the tracked set.
	viewer := fmt.Sprintf("uiterm-%s-%d", id[:8], time.Now().UnixNano())
	d.registerViewer(viewer)
	// On exit: drop from the tracked set, kill the grouped viewer, then re-normalize the
	// work server's window-size (restores `latest` once this was the last live viewer).
	defer func() {
		d.unregisterViewer(viewer)
		d.tmuxRun("kill-session", "-t", viewer) //nolint:errcheck — best-effort cleanup
		d.normalizeWindowSize()
	}()

	// Detach-safety (a): force `window-size largest` while a bridge is live so a smaller
	// web viewer can't shrink the user's real (cockpit) attachment. This is a TRANSIENT
	// override, NOT a permanent one — the OLD behaviour set `largest` globally and never
	// restored it, silently clobbering the cockpit conf's `window-size latest` forever, so
	// a fullscreen TUI sized to a stale taller client and its bottom rows (a Claude Code
	// modal/input box) rendered below the viewport = the cockpit clipping bug. The override
	// is wound back by normalizeWindowSize() (above, and in the reaper) once no viewer
	// session remains — keyed on the actual uiterm-* sessions, not handler return, because
	// an idle bridge's disconnect can go undetected (the same reason the reaper exists).
	if err := d.tmuxRun("set-option", "-g", "window-size", "largest"); err != nil {
		writeError(w, http.StatusInternalServerError, "terminal: "+err.Error())
		return
	}
	// `-c <thread cwd>` sets the viewer session's working dir so a popup/new
	// window opened inside the app's terminal (e.g. the work-conf `t` binding,
	// `display-popup -E "zsh -l"` which inherits the SESSION dir) lands in the
	// thread cwd — matching the real cockpit (whose work session was created
	// with -c). Without it the viewer inherits the DAEMON's cwd (~).
	if err := d.tmuxRun("new-session", "-d", "-t", loc.Session, "-s", viewer, "-c", th.Cwd, "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows)); err != nil {
		writeError(w, http.StatusInternalServerError, "terminal: create viewer session: "+err.Error())
		return
	}
	// (the viewer session is killed by the combined exit defer registered above)
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
	// attach is allowed. TERM is forced to xterm-256color: the supervised daemon often runs
	// with TERM unset/dumb, which the pty/tmux would otherwise inherit and break any agent
	// that clears the screen ("terminal does not support clear"). The UI terminal is xterm.js
	// (xterm-256color), so drop any inherited TERM and set the one the client actually speaks.
	cmd := d.tmuxCmd("attach-session", "-t", viewer)
	cmd.Env = append(scrubEnv(os.Environ(), "TMUX", "TMUX_PANE", "TERM"), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		c.Close(websocket.StatusInternalError, "pty start failed") //nolint:errcheck
		return
	}
	defer ptmx.Close()       //nolint:errcheck
	defer cmd.Process.Kill() //nolint:errcheck
	defer cmd.Wait()         //nolint:errcheck — reap the tmux client

	bridgePTY(r.Context(), c, ptmx)
}

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

// viewerReapTick is how often the reaper sweeps orphaned uiterm-* sessions.
const viewerReapTick = 60 * time.Second

func (d *Daemon) registerViewer(name string) {
	d.viewerMu.Lock()
	d.viewers[name] = true
	d.viewerMu.Unlock()
}

func (d *Daemon) unregisterViewer(name string) {
	d.viewerMu.Lock()
	delete(d.viewers, name)
	d.viewerMu.Unlock()
}

// normalizeWindowSize winds the work server's `window-size` back to `latest` (tmux's own
// default, and the policy the cockpit conf sets) whenever NO live-terminal viewer session
// remains. A bridge forces `largest` for detach-safety (terminal.go) — but that override
// must not outlive the bridge: a lingering `largest` sizes a fullscreen TUI in the master
// cockpit to a stale taller client and clips its bottom rows (the Claude Code modal/input
// box). The presence of a `uiterm-*` session is the authoritative "a bridge is live"
// signal (more reliable than handler-return, since an idle bridge's disconnect can go
// undetected). Called on every bridge exit AND by the reaper — so the startup reap, which
// kills all orphan viewers, self-heals any server a previous daemon left stuck on largest.
// No-op while any viewer remains (the live bridge still wants largest).
func (d *Daemon) normalizeWindowSize() {
	out, err := d.tmuxCmd("list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return // no server / no sessions — nothing to normalize
	}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(name, "uiterm-") {
			return // a live bridge still needs largest
		}
	}
	d.tmuxRun("set-option", "-g", "window-size", "latest") //nolint:errcheck — best-effort restore
}

func (d *Daemon) viewerTracked(name string) bool {
	d.viewerMu.Lock()
	defer d.viewerMu.Unlock()
	return d.viewers[name]
}

// reapViewers kills every uiterm-* session on the work server that this daemon is NOT
// actively bridging. The per-WebSocket defer in handleThreadTerminal cleans up viewers
// on a clean disconnect, but a daemon crash/restart/-9 leaves its in-flight viewers
// orphaned (tmux outlives the daemon), and they pile up forever. This is the sweep:
//   - at startup, the tracked set is empty, so EVERY uiterm-* is an orphan from a prior
//     process and gets killed (no live bridge can predate this process);
//   - periodically, anything not in the tracked set is an orphan.
//
// Killing a grouped viewer never affects the real thread session (a separate member of
// the same group) — only the unattached clone goes away.
func (d *Daemon) reapViewers() {
	out, err := d.tmuxCmd("list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return // no server / no sessions yet — nothing to reap
	}
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.HasPrefix(name, "uiterm-") || d.viewerTracked(name) {
			continue
		}
		d.tmuxRun("kill-session", "-t", name) //nolint:errcheck — best-effort orphan sweep
	}
	// Having swept orphan viewers, wind `window-size` back to `latest` if no bridge is
	// left — so the startup sweep (tracked set empty → every uiterm-* is an orphan and
	// dies) self-heals a work server a previous daemon left stuck on `largest`.
	d.normalizeWindowSize()
}

func (d *Daemon) startReaper() {
	d.viewerMu.Lock()
	d.reaperStarted = true
	d.viewerMu.Unlock()
	go d.viewerReaper()
}

func (d *Daemon) stopReaper() {
	d.viewerMu.Lock()
	started := d.reaperStarted
	d.viewerMu.Unlock()
	if !started {
		return
	}
	close(d.reaperStop)
	<-d.reaperDone
}

func (d *Daemon) viewerReaper() {
	defer close(d.reaperDone)
	d.reapViewers() // startup sweep: kill every orphan left by a prior daemon process
	t := time.NewTicker(viewerReapTick)
	defer t.Stop()
	for {
		select {
		case <-d.reaperStop:
			return
		case <-t.C:
			d.reapViewers()
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
