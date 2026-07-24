package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// TestSidebarNavDoesNotQuit (issue #8): in SIDEBAR mode a successful nav keeps
// the TUI running (it is a persistent pane beside the thread, not a popup over
// it) and notes what was entered; the normal mode still quits so the popup gets
// out of the way.
func TestSidebarNavDoesNotQuit(t *testing.T) {
	t.Setenv("TMUX", "") // focusSiblingPane must no-op outside tmux (unit context)

	sb := Model{sidebar: true}
	nm, cmd := sb.Update(navDoneMsg{name: "worker"})
	got := nm.(Model)
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatalf("sidebar nav QUIT the TUI — a persistent pane must stay open")
		}
	}
	if !strings.Contains(got.note, `entered "worker"`) {
		t.Errorf("sidebar nav note %q missing the entered-thread feedback", got.note)
	}

	// Normal (popup) mode: unchanged — a successful nav quits.
	popup := Model{}
	_, cmd = popup.Update(navDoneMsg{})
	if cmd == nil {
		t.Fatalf("popup nav produced no command (expected quit)")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatalf("popup nav did not quit the TUI")
	}
}

// TestSidebarSingleClickEnters (Lukas): in SIDEBAR mode ONE click on a row
// enters it (non-nil nav cmd, cursor moved) — the sidebar is a jump list, not a
// select-then-double-click grid; the offline gate still refuses instantly; the
// normal grid keeps its single-click-selects behavior (pinned by
// TestClickSelectsRow).
func TestSidebarSingleClickEnters(t *testing.T) {
	m := flatModel("a", "b", "c")
	m.sidebar = true
	nm, cmd := m.handleLeftClick(click(20, firstRowY+1))
	got := nm.(Model)
	if got.Cursor() != 1 {
		t.Fatalf("sidebar click should select row 1, cursor=%d", got.Cursor())
	}
	if cmd == nil {
		t.Fatalf("sidebar single click should ENTER (non-nil nav cmd)")
	}

	// Offline owner: refused instantly and loudly, no nav cmd.
	off := flatModel("a", "b")
	off.sidebar = true
	off.rows[1].Machine = "peer"
	off.machines = []api.MachineView{{Machine: "peer", Reachable: false}}
	nm2, cmd2 := off.handleLeftClick(click(20, firstRowY+1))
	g2 := nm2.(Model)
	if cmd2 != nil || g2.ActionErr() == nil {
		t.Fatalf("sidebar click on an offline row: cmd=%v err=%v — want instant loud refusal", cmd2, g2.ActionErr())
	}
}

// TestSidebarColumnsPreset pins the --sidebar column preset: NAME only, and it
// resolves cleanly through the normal column machinery.
func TestSidebarColumnsPreset(t *testing.T) {
	cols, err := ResolveColumns(SidebarColumns())
	if err != nil {
		t.Fatalf("sidebar preset does not resolve: %v", err)
	}
	if len(cols) != 1 || cols[0] != ColName {
		t.Fatalf("sidebar preset = %v, want [%s]", cols, ColName)
	}
}
