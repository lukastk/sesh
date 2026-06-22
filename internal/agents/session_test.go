package agents

import "testing"

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
