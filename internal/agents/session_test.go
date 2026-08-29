package agents

import (
	"os"
	"path/filepath"
	"testing"
)

// ResolveCurrentSession only chases claude's compaction drift; codex (its late-minted
// id is tracked by DiscoverCodexSession) and pi (a stable --session-id across resume)
// must pass the stored id through UNCHANGED, and an empty stored id stays empty. The
// claude chain-walk itself is covered by the claude package's fixture tests.
func TestResolveCurrentSessionPassthrough(t *testing.T) {
	homes := Homes{Claude: "/nonexistent", Pi: "/nonexistent", Codex: "/nonexistent"}
	cases := []struct {
		kind   Kind
		stored string
	}{
		{Codex, "codex-123"},
		{Pi, "pi-456"},
		{Claude, ""},                // no stored id → nothing to resolve
		{Claude, "no-such-session"}, // claude, but no project dir on disk → unchanged
	}
	for _, c := range cases {
		got, err := ResolveCurrentSession(c.kind, c.stored, "/some/cwd", homes)
		if err != nil {
			t.Errorf("ResolveCurrentSession(%s,%q): %v", c.kind, c.stored, err)
		}
		if got != c.stored {
			t.Errorf("ResolveCurrentSession(%s,%q) = %q, want unchanged", c.kind, c.stored, got)
		}
	}
}

func TestEphemeralCodexSessionRequiresCertainAbsence(t *testing.T) {
	home := t.TempDir()
	homes := Homes{Codex: home}
	const id = "01a04e27-bd13-7f82-ab6d-a93a69cd9cbf"

	// A missing/unreadable rollout tree is a read error, not proof that this
	// id is an internal sub-thread. The one-directional guard must not refuse.
	if EphemeralCodexSession(Codex, id, homes) {
		t.Fatalf("missing sessions tree treated as certain absence")
	}

	day := filepath.Join(home, "sessions", "2026", "08", "29")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	if !EphemeralCodexSession(Codex, id, homes) {
		t.Fatalf("complete rollout tree with no matching id was not recognized as ephemeral")
	}
	if err := os.WriteFile(filepath.Join(day, "rollout-2026-08-29T15-33-30-"+id+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if EphemeralCodexSession(Codex, id, homes) {
		t.Fatalf("persisted rollout id was recognized as ephemeral")
	}
}
