package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

func TestParseSessionRows(t *testing.T) {
	enc := func(ss ...api.ShellSession) []byte {
		var b strings.Builder
		e := json.NewEncoder(&b)
		for _, x := range ss {
			if err := e.Encode(x); err != nil {
				t.Fatal(err)
			}
		}
		return []byte(b.String())
	}
	out := enc(
		api.ShellSession{Machine: "mymain", Name: "a", Path: "/tmp", Class: api.ShellClassGhost},
		api.ShellSession{Machine: "mymain", Name: "b", Class: api.ShellClassAgent, AgentThreads: []string{"t-aaa", "unknown-thread-id"}},
		api.ShellSession{Name: "c", Class: api.ShellClassShell, ThreadID: "sh-1"},
	)
	rows, err := parseSessionRows("mymain", out, map[string]string{"t-aaa": "corkboard"})
	if err != nil {
		t.Fatalf("parseSessionRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if rows[0].Class != api.ShellClassGhost || rows[0].Path != "/tmp" {
		t.Fatalf("row 0 wrong: %+v", rows[0])
	}
	// Agent-thread ids are resolved to NAMES from the grid; one the grid does not
	// carry must not silently vanish — it renders by short id.
	if len(rows[1].Threads) != 2 || rows[1].Threads[0] != "corkboard" || rows[1].Threads[1] != "unknown-" {
		t.Fatalf("row 1 threads = %v, want [corkboard unknown-]", rows[1].Threads)
	}
	// A peer that did not stamp the machine gets the one we asked.
	if rows[2].Machine != "mymain" {
		t.Fatalf("row 2 machine = %q, want the queried machine", rows[2].Machine)
	}

	// Promotability: everything except an already-tracked shell.
	if !rows[0].promotable() || !rows[1].promotable() {
		t.Fatal("ghost and agent sessions must be promotable")
	}
	if rows[2].promotable() {
		t.Fatal("a session that is ALREADY a shell thread must not be promotable")
	}
	stale := sessionRow{ShellSession: api.ShellSession{Class: api.ShellClassStale}}
	if !stale.promotable() {
		t.Fatal("a STALE marker must be promotable — re-promoting is the repair")
	}

	// Malformed JSONL is LOUD, never a silently dropped session (a dropped row
	// would read as "that session does not exist", the bug this viewer fixes).
	if _, err := parseSessionRows("mymain", []byte("{not json}\n"), nil); err == nil {
		t.Fatal("malformed JSONL must be a loud error, got nil")
	}
}

// TestReachableMachines: the fan-out probes ONLY machines the mesh says are
// reachable — an asleep peer must cost nothing (it would otherwise hang the
// viewer on an ssh timeout).
func TestReachableMachines(t *testing.T) {
	m := Model{machine: "mymain", machines: []api.MachineView{
		{Machine: "mymain", Self: true, Reachable: true},
		{Machine: "macbook", Reachable: true},
		{Machine: "pocket4", Reachable: false},
	}}
	got := m.reachableMachines()
	if len(got) != 2 || got[0] != "macbook" || got[1] != "mymain" {
		t.Fatalf("reachable = %v, want [macbook mymain]", got)
	}

	// No mesh view yet (first frame, or a hand-built Model): fall back to self
	// rather than returning an empty list, which would render an empty viewer
	// that looks like "you have no sessions".
	m2 := Model{machine: "mymain"}
	if got := m2.reachableMachines(); len(got) != 1 || got[0] != "mymain" {
		t.Fatalf("fallback = %v, want [mymain]", got)
	}
}

func ghostRow(machine, name string) sessionRow {
	return sessionRow{ShellSession: api.ShellSession{Machine: machine, Name: name, Class: api.ShellClassGhost}}
}

func shellModel(rows ...sessionRow) Model {
	return Model{shells: true, shellRows: rows, machine: "mymain"}
}

func TestShellViewerKeys(t *testing.T) {
	rows := []sessionRow{ghostRow("mymain", "a"), ghostRow("mymain", "b")}

	t.Run("cursor moves and clamps", func(t *testing.T) {
		m := shellModel(rows...)
		next, _ := m.handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		if next.(Model).shellCursor != 1 {
			t.Fatalf("cursor = %d, want 1", next.(Model).shellCursor)
		}
		// past the end: clamps rather than running off
		next, _ = next.(Model).handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		if next.(Model).shellCursor != 1 {
			t.Fatalf("cursor = %d, want it clamped at 1", next.(Model).shellCursor)
		}
	})

	t.Run("esc closes", func(t *testing.T) {
		m := shellModel(rows...)
		next, _ := m.handleShellKey(tea.KeyMsg{Type: tea.KeyEsc})
		if next.(Model).shells {
			t.Fatal("esc must close the viewer")
		}
	})

	t.Run("x asks before killing, and a non-y answer cancels", func(t *testing.T) {
		m := shellModel(rows...)
		next, cmd := m.handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
		nm := next.(Model)
		if !nm.shellConfirmKill {
			t.Fatal("x must open the confirmation, not kill")
		}
		if cmd != nil {
			t.Fatal("x must not shell out before the confirmation is answered")
		}
		// Anything other than y cancels — the destructive default is NO.
		next2, cmd2 := nm.handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
		if next2.(Model).shellConfirmKill {
			t.Fatal("a non-y answer must dismiss the confirmation")
		}
		if cmd2 != nil {
			t.Fatal("a cancelled kill must issue no command")
		}
	})

	t.Run("the confirmation swallows navigation keys", func(t *testing.T) {
		m := shellModel(rows...)
		m.shellConfirmKill = true
		next, _ := m.handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		if next.(Model).shellCursor != 0 {
			t.Fatal("the cursor must not move under the kill confirmation — the answer would apply to a different session")
		}
	})

	t.Run("enter closes the viewer and navs", func(t *testing.T) {
		m := shellModel(rows...)
		next, cmd := m.handleShellKey(tea.KeyMsg{Type: tea.KeyEnter})
		if next.(Model).shells {
			t.Fatal("enter must close the takeover so the user lands in the session")
		}
		if cmd == nil {
			t.Fatal("enter must issue a nav command")
		}
	})

	t.Run("an empty list refuses enter and kill without panicking", func(t *testing.T) {
		m := shellModel()
		if _, cmd := m.handleShellKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
			t.Fatal("enter on an empty list must do nothing")
		}
		next, _ := m.handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
		if next.(Model).shellConfirmKill {
			t.Fatal("x on an empty list must not open a confirmation")
		}
	})
}

// The viewer renders the classification and the session's start path, and warns
// that a kill takes the agent panes with it.
func TestShellsViewRenders(t *testing.T) {
	m := shellModel(sessionRow{
		ShellSession: api.ShellSession{Machine: "mymain", Name: "appgarden", Path: "/dev/ag",
			Class: api.ShellClassAgent, Windows: 2, Panes: 3},
		Threads: []string{"corkboard"}})
	m.width = 200
	out := m.shellsView()
	for _, want := range []string{"appgarden", "agent", "/dev/ag", "corkboard"} {
		if !strings.Contains(out, want) {
			t.Fatalf("view missing %q:\n%s", want, out)
		}
	}
	m.shellConfirmKill = true
	warn := m.shellsView()
	if !strings.Contains(warn, "corkboard") || !strings.Contains(warn, "headless") {
		t.Fatalf("kill confirmation must name the agent threads it would drop to headless:\n%s", warn)
	}
}

func TestShellViewerPromoteKey(t *testing.T) {
	t.Run("P promotes a ghost", func(t *testing.T) {
		m := shellModel(ghostRow("mymain", "scratch"))
		_, cmd := m.handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
		if cmd == nil {
			t.Fatal("P on a ghost must issue a promote command")
		}
	})
	t.Run("P refuses a session that is already a shell thread", func(t *testing.T) {
		m := shellModel(sessionRow{ShellSession: api.ShellSession{
			Machine: "mymain", Name: "tracked", Class: api.ShellClassShell, ThreadID: "sh-1"}})
		next, cmd := m.handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")})
		if cmd != nil {
			t.Fatal("P on an already-tracked session must not shell out")
		}
		if !strings.Contains(next.(Model).shellNote, "already a shell thread") {
			t.Fatalf("expected a note saying it is already tracked, got %q", next.(Model).shellNote)
		}
	})
	t.Run("P promotes a STALE marker (re-promoting is the repair)", func(t *testing.T) {
		m := shellModel(sessionRow{ShellSession: api.ShellSession{
			Machine: "mymain", Name: "orphan", Class: api.ShellClassStale, ThreadID: "gone"}})
		if _, cmd := m.handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("P")}); cmd == nil {
			t.Fatal("P on a stale-marker session must promote")
		}
	})
	t.Run("p is NOT the promote key (it is the global palette)", func(t *testing.T) {
		m := shellModel(ghostRow("mymain", "scratch"))
		if _, cmd := m.handleShellKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")}); cmd != nil {
			t.Fatal("lowercase p must not promote — it shadows the command palette")
		}
	})
}
