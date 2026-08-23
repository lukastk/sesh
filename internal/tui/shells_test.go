package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

// pane builds a TmuxPane carrying (or not carrying) a thread marker.
func pane(id, threadID string) api.TmuxPane {
	return api.TmuxPane{Pane: id, ThreadID: threadID}
}

// TestClassifySession pins the whole stage-1 classification rule: a session
// hosting ANY @sesh-thread-id-marked pane is an agent session; anything else is
// a ghost — the promote target.
func TestClassifySession(t *testing.T) {
	names := map[string]string{"t-aaa": "corkboard", "t-bbb": "worker"}

	t.Run("no marked pane is a ghost", func(t *testing.T) {
		s := api.TmuxSession{Name: "appgarden", Path: "/home/l/dev/appgarden", Attached: true,
			Windows: []api.TmuxWindow{{Panes: []api.TmuxPane{pane("%0", ""), pane("%1", "")}}}}
		got := classifySession("mymain", s, names)
		if got.Class != classGhost {
			t.Fatalf("class = %q, want ghost", got.Class)
		}
		if got.Machine != "mymain" || got.Name != "appgarden" || got.Path != "/home/l/dev/appgarden" || !got.Attached {
			t.Fatalf("row header wrong: %+v", got)
		}
		if got.Windows != 1 || got.Panes != 2 {
			t.Fatalf("counts = %d windows / %d panes, want 1/2", got.Windows, got.Panes)
		}
		if len(got.Threads) != 0 {
			t.Fatalf("ghost must name no threads, got %v", got.Threads)
		}
	})

	t.Run("one marked pane makes it an agent session", func(t *testing.T) {
		s := api.TmuxSession{Name: "work", Windows: []api.TmuxWindow{
			{Panes: []api.TmuxPane{pane("%0", ""), pane("%1", "t-aaa")}},
		}}
		got := classifySession("mymain", s, names)
		if got.Class != classAgent {
			t.Fatalf("class = %q, want agent", got.Class)
		}
		if len(got.Threads) != 1 || got.Threads[0] != "corkboard" {
			t.Fatalf("threads = %v, want [corkboard]", got.Threads)
		}
	})

	t.Run("threads are deduped across windows and named from the grid", func(t *testing.T) {
		s := api.TmuxSession{Name: "work", Windows: []api.TmuxWindow{
			{Panes: []api.TmuxPane{pane("%0", "t-aaa"), pane("%1", "t-aaa")}},
			{Panes: []api.TmuxPane{pane("%2", "t-bbb")}},
		}}
		got := classifySession("mymain", s, names)
		if len(got.Threads) != 2 || got.Threads[0] != "corkboard" || got.Threads[1] != "worker" {
			t.Fatalf("threads = %v, want [corkboard worker] deduped + sorted", got.Threads)
		}
		if got.Panes != 3 || got.Windows != 2 {
			t.Fatalf("counts = %d windows / %d panes, want 2/3", got.Windows, got.Panes)
		}
	})

	t.Run("an unknown thread id still classifies, shown short", func(t *testing.T) {
		s := api.TmuxSession{Name: "work", Windows: []api.TmuxWindow{
			{Panes: []api.TmuxPane{pane("%0", "deadbeef-cafe-0000")}},
		}}
		got := classifySession("mymain", s, names)
		if got.Class != classAgent {
			t.Fatalf("class = %q, want agent", got.Class)
		}
		// A thread the grid does not carry (another view, or archived) must not
		// silently vanish from the row — it renders by short id.
		if len(got.Threads) != 1 || got.Threads[0] != "deadbeef" {
			t.Fatalf("threads = %v, want [deadbeef]", got.Threads)
		}
	})
}

// TestClassifySessionIgnoresSessionScopedMarker is the guard for the trap that
// shaped this whole design: tmux user options INHERIT down to panes during
// format expansion, so if sesh ever stamped @sesh-thread-id at SESSION scope,
// every unmarked pane would report it and every ghost would misclassify as an
// agent session. Classification reads the PANE's value, and the only reason that
// is trustworthy is that sesh stamps that option at pane scope ONLY.
//
// This test pins the consequence: a session whose panes carry no marker of their
// own is a ghost, no matter what the session itself is called or holds.
func TestClassifySessionIgnoresSessionScopedMarker(t *testing.T) {
	// api.TmuxSession has no session-marker field precisely because the pane
	// marker must never be set at session scope; the enumeration reads
	// #{@sesh-thread-id} per PANE. An unmarked pane arrives with ThreadID "".
	s := api.TmuxSession{Name: "boxsess", Windows: []api.TmuxWindow{
		{Panes: []api.TmuxPane{pane("%0", ""), pane("%1", "")}},
	}}
	got := classifySession("mymain", s, nil)
	if got.Class != classGhost {
		t.Fatalf("class = %q, want ghost — a session with no PANE-marked panes is untracked", got.Class)
	}
}

func TestParseSessionRows(t *testing.T) {
	enc := func(ss ...api.TmuxSession) []byte {
		var b strings.Builder
		e := json.NewEncoder(&b)
		for _, s := range ss {
			if err := e.Encode(s); err != nil {
				t.Fatal(err)
			}
		}
		return []byte(b.String())
	}

	out := enc(
		api.TmuxSession{Name: "a", Path: "/tmp", Windows: []api.TmuxWindow{{Panes: []api.TmuxPane{pane("%0", "")}}}},
		api.TmuxSession{Name: "b", Path: "/usr", Windows: []api.TmuxWindow{{Panes: []api.TmuxPane{pane("%1", "t-aaa")}}}},
	)
	rows, err := parseSessionRows("mymain", out, map[string]string{"t-aaa": "corkboard"})
	if err != nil {
		t.Fatalf("parseSessionRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].Class != classGhost || rows[1].Class != classAgent {
		t.Fatalf("classes = %q, %q; want ghost, agent", rows[0].Class, rows[1].Class)
	}
	if rows[0].Path != "/tmp" {
		t.Fatalf("session path lost: %+v", rows[0])
	}

	// Malformed JSONL is LOUD, never a silently dropped session (a dropped row
	// would read as "that session does not exist", which is the whole bug class
	// this viewer exists to fix).
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

func shellModel(rows ...sessionRow) Model {
	return Model{shells: true, shellRows: rows, machine: "mymain"}
}

func TestShellViewerKeys(t *testing.T) {
	rows := []sessionRow{
		{Machine: "mymain", Name: "a", Class: classGhost},
		{Machine: "mymain", Name: "b", Class: classGhost},
	}

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
	m := shellModel(sessionRow{Machine: "mymain", Name: "appgarden", Path: "/dev/ag",
		Class: classAgent, Windows: 2, Panes: 3, Threads: []string{"corkboard"}})
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
