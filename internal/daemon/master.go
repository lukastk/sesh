package daemon

import (
	"net/http"
	"os"
	"os/exec"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"github.com/lukastk/sesh/internal/config"
)

func (d *Daemon) routesMaster(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/master/terminal", d.handleMasterTerminal)
}

// handleMasterTerminal runs the ui_config `master_command` (e.g. "mmt-start") in a pty
// bridged to a WebSocket — backing the app's "Master" mode. It runs via `$SHELL -ic` so
// shell functions/aliases resolve (mmt-start is a shell function from myrig, defined in
// the user's rc — a non-interactive `-c` shell would not source it). The command is the
// CONFIGURED value, never a client param, so this can't run arbitrary client input. TMUX
// is unset so the command's own tmux attach isn't refused as nested. Reachable cross-
// machine over the peer's transport, so the app can open any machine's master.
//
// Loud 409 if master_command is unset (no silent empty terminal).
func (d *Daemon) handleMasterTerminal(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadUIConfig(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfg.MasterCommand == "" {
		writeError(w, http.StatusConflict, `master: no master_command set in ui_config.toml (e.g. master_command = "mmt-start")`)
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

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	cmd := exec.Command(shell, "-ic", cfg.MasterCommand)
	cmd.Env = append(scrubEnv(os.Environ(), "TMUX", "TMUX_PANE", "TERM"), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		c.Close(websocket.StatusInternalError, "master: pty start failed") //nolint:errcheck
		return
	}
	defer ptmx.Close()       //nolint:errcheck
	defer cmd.Process.Kill() //nolint:errcheck
	defer cmd.Wait()         //nolint:errcheck

	bridgePTY(r.Context(), c, ptmx)
}
