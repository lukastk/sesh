package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeRollout fabricates a codex rollout file (session_meta first line — the
// exact shape codexRolloutMeta parses) under home/sessions. The name's iso-ts
// prefix is what DiscoverCodexSession sorts on, so ts ordering = recency.
func writeRollout(t *testing.T, home, ts, id, cwd string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "07", "26")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`, id, cwd)
	if err := os.WriteFile(filepath.Join(dir, "rollout-"+ts+"-"+id+".jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverCodexSessionExcludesClaimed pins the legacy-fallback contract
// (schema 46, ticket 49d4299b): newest-in-cwd wins, EXCEPT sessions already
// claimed by other threads — discovery must never hand one thread a sibling's
// conversation (the silent wrong-thread resume this ticket was about).
func TestDiscoverCodexSessionExcludesClaimed(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/proj"
	writeRollout(t, home, "2026-07-26T10-00-00", "older-id", cwd)
	writeRollout(t, home, "2026-07-26T11-00-00", "newer-id", cwd)
	writeRollout(t, home, "2026-07-26T12-00-00", "other-cwd-id", "/tmp/elsewhere")

	// No exclusions: the newest matching-cwd rollout wins (the other cwd never).
	id, found, err := DiscoverCodexSession(home, cwd, 0, nil)
	if err != nil || !found || id != "newer-id" {
		t.Fatalf("unexcluded: got (%q, %v, %v), want newer-id", id, found, err)
	}

	// The newest is claimed by another thread: discovery must fall PAST it.
	id, found, err = DiscoverCodexSession(home, cwd, 0, map[string]bool{"newer-id": true})
	if err != nil || !found || id != "older-id" {
		t.Fatalf("newest excluded: got (%q, %v, %v), want older-id", id, found, err)
	}

	// Everything in the cwd claimed: nothing to discover — found=false, no error.
	id, found, err = DiscoverCodexSession(home, cwd, 0, map[string]bool{"newer-id": true, "older-id": true})
	if err != nil || found {
		t.Fatalf("all excluded: got (%q, %v, %v), want not found", id, found, err)
	}
}
