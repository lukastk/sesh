package tui

import (
	"fmt"
	"os"
	"time"
)

// Diagnostic logging for the MOUSE/GEOMETRY path, gated on $SESH_TUI_LOG (a
// file path; unset = completely off, not even a stat).
//
// This exists because the sidebar's click behaviour depends on facts that are
// invisible from outside the process: the size tmux last told us the pane is,
// where the model believes the data rows start, and which visible row a click
// Y therefore resolves to. Inspecting tmux from the side can show the pane
// geometry but NOT what the TUI received or decided — so when clicks
// "stop working", this is the only way to tell a wrong pane size from a wrong
// row mapping from an event that never arrived at all.
//
// Enable per-pane by putting it in the tmux environment before the sidebar is
// spawned:
//
//	tmux -L sesh-master set-environment -g SESH_TUI_LOG /tmp/sesh-sidebar.log
//
// then refresh the sidebar. Appends; one line per event.
var debugLogPath = os.Getenv("SESH_TUI_LOG")

func debugEnabled() bool { return debugLogPath != "" }

// debugLog appends one formatted line. Best-effort by design: a diagnostic must
// never take the TUI down, and a failed write is not worth surfacing to a user
// who is mid-repro. Opened per line so the log survives a killed pane with
// nothing buffered — the pane dying IS often the thing under investigation.
func debugLog(format string, args ...any) {
	if !debugEnabled() {
		return
	}
	f, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck — diagnostic path
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

// logClickGeometry records everything that determines where a click lands: the
// event's own coordinates, the size tmux last reported, and every term of the
// rowAtY computation. `got` is the resolved visible-row index (-1 when the Y
// fell outside the data rows) and `name` the row it selected.
func (m *Model) logClickGeometry(x, y int, got int, name string, note string) {
	if !debugEnabled() {
		return
	}
	start, end := m.viewportRange()
	debugLog("CLICK x=%d y=%d | pane=%dx%d sidebar=%v | rowsTop=%d vOffset=%d viewport=[%d,%d) rows=%d | -> idx=%d %q %s",
		x, y, m.width, m.height, m.sidebar,
		m.rowsTop(), m.vOffset, start, end, len(m.visibleMatches()),
		got, name, note)
}
