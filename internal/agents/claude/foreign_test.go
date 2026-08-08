package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// TestForeignProjectDir pins the one-directional evidence contract behind the
// report-state stamp backstop (the 2026-08-05 background-agent incident): a
// session whose transcript sits under a DIFFERENT project dir than the thread's
// cwd is proof the reporter is not that thread's agent, while every other
// outcome — under cwd, nowhere on disk yet, no claude home — must read as "no
// contradiction" so a race can never be mistaken for a lie.
func TestForeignProjectDir(t *testing.T) {
	home := t.TempDir()
	const (
		ownCwd     = "/home/u/dev/mybox"
		foreignCwd = "/home/u/dev/otherbox"
	)
	write := func(cwd, sessionID string) {
		t.Helper()
		dir := ProjectDir(home, cwd)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(ownCwd, "mine")
	write(foreignCwd, "theirs")

	// The incident shape: the reported session lives under another cwd.
	got, foreign := ForeignProjectDir(home, ownCwd, "theirs")
	if !foreign {
		t.Fatal("a session whose transcript lives under a DIFFERENT project dir was not reported foreign — this is the exact 2026-08-05 corruption vector")
	}
	if want := ProjectDirName(foreignCwd); got != want {
		t.Fatalf("foreign project dir = %q, want %q", got, want)
	}

	// Everything else must read as "no contradiction".
	for _, tc := range []struct {
		name            string
		home, cwd, sess string
	}{
		{"under cwd (normal, incl. compaction minting a new id)", home, ownCwd, "mine"},
		{"not on disk anywhere yet (a race, not a lie)", home, ownCwd, "unwritten"},
		{"no claude home", "", ownCwd, "theirs"},
		{"no cwd", home, "", "theirs"},
		{"no session id", home, ownCwd, ""},
		{"claude home has no projects dir", t.TempDir(), ownCwd, "theirs"},
	} {
		if other, foreign := ForeignProjectDir(tc.home, tc.cwd, tc.sess); foreign {
			t.Fatalf("%s: reported foreign (%q) — only positive evidence may refuse a stamp", tc.name, other)
		}
	}
}
