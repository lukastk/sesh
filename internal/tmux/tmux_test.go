package tmux

import (
	"strings"
	"testing"
)

// line builds a tab-separated list-panes record with the canonical field set
// (session_name, session_attached, session_path, then window/pane fields).
func line(fields ...string) string {
	if len(fields) != paneFieldCount {
		panic("test line needs exactly the pane field count")
	}
	return strings.Join(fields, fieldSep)
}

func TestParsePanesGroupsSessionsWindowsPanes(t *testing.T) {
	// session "work": window 0 (two panes), window 1 (one pane).
	out := strings.Join([]string{
		line("work", "1", "/srv", "0", "zsh", "0", "%0", "0", "1", "100", "zsh", "title0", "/a", "thr_1"),
		line("work", "1", "/srv", "0", "zsh", "0", "%1", "1", "0", "101", "vim", "title1", "/a", ""),
		line("work", "1", "/srv", "1", "log", "1", "%2", "0", "1", "102", "tail", "title2", "/b", ""),
	}, "\n")

	sessions, err := parsePanes("mac", "mytmux", out)
	if err != nil {
		t.Fatalf("parsePanes: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	s := sessions[0]
	if s.Machine != "mac" || s.Socket != "mytmux" || s.Name != "work" || !s.Attached {
		t.Fatalf("session header wrong: %+v", s)
	}
	// session_path is the session's START dir and is deliberately NOT any pane's
	// live cwd (/a, /b here) — see api.TmuxSession.Path.
	if s.Path != "/srv" {
		t.Fatalf("session path = %q, want /srv", s.Path)
	}
	if len(s.Windows) != 2 {
		t.Fatalf("want 2 windows, got %d", len(s.Windows))
	}
	if len(s.Windows[0].Panes) != 2 || len(s.Windows[1].Panes) != 1 {
		t.Fatalf("window pane counts wrong: %d, %d", len(s.Windows[0].Panes), len(s.Windows[1].Panes))
	}
	if s.Windows[0].Panes[0].ThreadID != "thr_1" {
		t.Errorf("thread id not parsed: %q", s.Windows[0].Panes[0].ThreadID)
	}
	if !s.Windows[0].Panes[0].Active || s.Windows[0].Panes[1].Active {
		t.Errorf("pane active flags wrong")
	}
	if s.Windows[0].Panes[1].Command != "vim" || s.Windows[0].Panes[1].PID != 101 {
		t.Errorf("pane fields wrong: %+v", s.Windows[0].Panes[1])
	}
}

func TestParsePanesEmpty(t *testing.T) {
	sessions, err := parsePanes("mac", "mytmux", "")
	if err != nil {
		t.Fatalf("parsePanes empty: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("want 0 sessions, got %d", len(sessions))
	}
}

func TestParsePanesLoudOnFieldMismatch(t *testing.T) {
	// A short record (a stray tab in a title, say) must error loudly, never be
	// silently dropped.
	_, err := parsePanes("mac", "mytmux", "work\t1\t0")
	if err == nil {
		t.Fatal("expected a loud error on malformed record, got nil")
	}
}
