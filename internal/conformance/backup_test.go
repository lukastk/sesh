package conformance

// thread.backup cells (PARITY_ROADMAP D4 + D2's copy): real transcripts into
// the portable SQLite and back. Per agent: backup a REAL turn's transcript,
// wipe THE TEST THREAD'S OWN file from disk, restore, and prove byte
// equality (--to-dir, every agent) — claude additionally restores NATIVELY
// and the agent RESUMES the conversation with memory (the data-safety
// contract). Idempotency: a second backup skips unchanged. copy --to-dir
// composes the same machinery; cross-machine copy of a non-claude thread is
// refused loudly. Remote = the whole backup verb routed to the peer.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, agent := range []matrix.Agent{matrix.Claude, matrix.Codex, matrix.Pi} {
		a := agent
		matrix.RegisterTest("thread.backup", a, matrix.Local,
			func(t *testing.T) { testBackupRestore(t, string(a)) })
		matrix.RegisterTest("thread.backup", a, matrix.Remote,
			func(t *testing.T) { testBackupRouted(t, string(a)) })
	}
}

// transcriptPathOf reads the on-disk transcript path via the D0 endpoint.
func transcriptPathOf(t *testing.T, sb *Sandbox, id string) string {
	t.Helper()
	out, stderr, err := sb.Runner.Run(t, "thread", "transcript", "--id", id, "--json")
	if err != nil {
		t.Fatalf("transcript: %v\n%s", err, stderr)
	}
	var tr struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Path == "" {
		t.Fatalf("no transcript path for %s", id)
	}
	return tr.Path
}

func testBackupRestore(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, agent, "vaulted")
	sb.headlessTurn(t, th.ID, "Reply with exactly the word TAMARIND and nothing else")

	bk := filepath.Join(t.TempDir(), "vault.sqlite")
	out, stderr, err := sb.Runner.Run(t, "backup", "--to", bk, "--id", th.ID)
	if err != nil {
		t.Fatalf("[%s] backup: %v\n%s", agent, err, stderr)
	}
	if !strings.Contains(out, "1 written") {
		t.Fatalf("[%s] backup wrote nothing: %s", agent, out)
	}
	// Idempotent: unchanged content is skipped.
	out, _, err = sb.Runner.Run(t, "backup", "--to", bk, "--id", th.ID)
	if err != nil || !strings.Contains(out, "1 unchanged") {
		t.Errorf("[%s] second backup not idempotent: %v %s", agent, err, out)
	}

	// Wipe OUR OWN transcript file (recorded first), restore --to-dir, compare bytes.
	orig := transcriptPathOf(t, sb, th.ID)
	origBytes, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(orig); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, stderr, err := sb.Runner.Run(t, "restore", "--from", bk, "--id", th.ID[:8], "--to-dir", dir); err != nil {
		t.Fatalf("[%s] restore: %v\n%s", agent, err, stderr)
	}
	got, err := os.ReadFile(filepath.Join(dir, th.ID+".jsonl"))
	if err != nil {
		t.Fatalf("[%s] restored file missing: %v", agent, err)
	}
	if !bytes.Equal(got, origBytes) {
		t.Fatalf("[%s] restored bytes differ from the original transcript", agent)
	}

	if agent == "claude" {
		// NATIVE restore puts the file back at the agent's real location and
		// the conversation RESUMES with memory — the data-safety contract.
		if _, stderr, err := sb.Runner.Run(t, "restore", "--from", bk, "--id", th.ID, "--native", "--force"); err != nil {
			t.Fatalf("native restore: %v\n%s", err, stderr)
		}
		if _, err := os.Stat(orig); err != nil {
			t.Fatalf("native restore did not recreate %s: %v", orig, err)
		}
		sb.headlessTurn(t, th.ID, "What word did I ask you to reply with? Answer with just that word.")
		if r := sb.headlessReply(t, th.ID); !strings.Contains(r.Reply, "TAMARIND") {
			t.Errorf("restored conversation lost its memory: %q", r.Reply)
		}
	} else {
		// pi/codex: native restore reports the entry Unsupported, loudly.
		out, _, err := sb.Runner.Run(t, "restore", "--from", bk, "--id", th.ID, "--native")
		if err != nil {
			t.Fatalf("native restore run: %v", err)
		}
		if !strings.Contains(out, "NOT natively restorable") {
			t.Errorf("[%s] unsupported native restore not reported: %s", agent, out)
		}
		// Put the file back for cleanliness (the sandbox owns this thread).
		os.WriteFile(orig, origBytes, 0o644) //nolint:errcheck
	}

	if agent != "pi" {
		return
	}
	// copy --to-dir composes the machinery; cross-machine copy of a non-claude
	// thread is refused loudly (v1's false-success guard). A bogus restore
	// path is loud (OpenExisting), never an empty-db "0 written" success.
	cdir := t.TempDir()
	if _, stderr, err := sb.Runner.Run(t, "copy", th.ID, "--to-dir", cdir); err != nil {
		t.Fatalf("copy --to-dir: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(cdir, th.ID+".jsonl")); err != nil {
		t.Errorf("copy landed nothing: %v", err)
	}
	if _, stderr, err := sb.Runner.Run(t, "copy", th.ID, "--to", "anywhere"); err == nil {
		t.Errorf("cross-machine copy of a pi thread succeeded (must refuse)")
	} else if !strings.Contains(stderr, "nondeterministic") {
		t.Errorf("copy refusal reason wrong: %s", stderr)
	}
	if _, _, err := sb.Runner.Run(t, "restore", "--from", "/nonexistent.sqlite", "--to-dir", cdir); err == nil {
		t.Errorf("restore from a missing file succeeded silently")
	}
}

// testBackupRouted: the whole backup verb routed to a peer — the backup file
// lands on the PEER's disk containing the peer thread's real transcript.
func testBackupRouted(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Remote)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, agent, "rvault")
	sb.headlessTurn(t, th.ID, "Reply with exactly: ok")

	bk := filepath.Join(t.TempDir(), "rvault.sqlite") // shared fs: ssh-localhost peer
	out, stderr, err := sb.Runner.Run(t, "backup", "--to", bk, "--id", th.ID)
	if err != nil {
		t.Fatalf("routed backup: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "1 written") {
		t.Fatalf("routed backup wrote nothing: %s", out)
	}
	if _, err := os.Stat(bk); err != nil {
		t.Errorf("backup file missing: %v", err)
	}
}
