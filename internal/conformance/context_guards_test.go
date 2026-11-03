package conformance

// context.guards and context.edit cells (schema 60): the owner refuses what no
// harness could receive whole and what no agent could receive at all, a
// conditional write refuses a lost update, and `sesh context edit` round-trips
// the context through $EDITOR.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("context.guards", matrix.AgentAgnostic, loc, func(t *testing.T) { testContextGuards(t, loc) })
	}
	matrix.RegisterTest("context.edit", matrix.AgentAgnostic, matrix.Local, testContextEditCLI)
}

func testContextGuards(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "guarded")

	refused := func(what, wantErr string, args ...string) {
		t.Helper()
		_, stderr, err := sb.Runner.Run(t, args...)
		if err == nil || !strings.Contains(stderr, wantErr) {
			t.Fatalf("%s: err=%v stderr=%q, want a refusal containing %q", what, err, stderr, wantErr)
		}
	}
	// One byte over the limit is refused, and nothing is stored.
	refused("oversize", "the limit is 8000", "context", "set", "--id", th.ID, "--text", strings.Repeat("x", api.MaxThreadContextBytes+1))
	if got := showContext(t, sb, th.ID); got.Text != "" || got.Hash != "" {
		t.Fatalf("a refused write stored %+v", got)
	}
	// Exactly at the limit is accepted.
	full := setContext(t, sb, th.ID, strings.Repeat("y", api.MaxThreadContextBytes))

	// A blank text is not a silent clear.
	refused("blank", "context clear", "context", "set", "--id", th.ID, "--text", "   ")
	// A lost update is refused; the right hash goes through.
	refused("stale expect-hash", "changed since it was read", "context", "set", "--id", th.ID, "--text", "v2", "--expect-hash", "deadbeefdeadbeef")
	refused("expect none on a thread with context", "changed since it was read", "context", "set", "--id", th.ID, "--text", "v2", "--expect-hash", "none")
	if _, stderr, err := sb.Runner.Run(t, "context", "set", "--id", th.ID, "--text", "v2", "--expect-hash", full.Hash); err != nil {
		t.Fatalf("matching expect-hash refused: %v %s", err, stderr)
	}
	// The same text again changes nothing.
	out, stderr, err := sb.Runner.Run(t, "context", "set", "--id", th.ID, "--text", "v2", "--json")
	if err != nil || !strings.Contains(out, `"changed":false`) {
		t.Fatalf("idempotent set: %v %s %s", err, out, stderr)
	}
	refused("conditional clear, stale", "changed since it was read", "context", "clear", "--id", th.ID, "--expect-hash", full.Hash)
	clearContext(t, sb, th.ID)
	if got := showContext(t, sb, th.ID); got.Hash != "" {
		t.Fatalf("clear left %+v", got)
	}

	// A thread that runs no agent cannot have a context.
	virt := sb.newVirtualThread(t, "group")
	refused("virtual thread", "runs no agent", "context", "set", "--id", virt.ID, "--text", "x")
	refused("unknown thread", "no thread with id", "context", "set", "--id", "ffffffff", "--text", "x")
}

// testContextEditCLI drives `sesh context edit` with an editor script: the editor
// is given the current context, the saved file becomes the new one (minus the
// editor's trailing newline), an unchanged save changes nothing, and an emptied
// file removes it. Local only: the editor runs on the caller's machine, which is
// what an http-routed --machine does too (this harness's remote is ssh, where the
// command would run on the peer with no terminal).
func testContextEditCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "cli-edit")
	setContext(t, sb, th.ID, "first")

	dir := t.TempDir()
	seen, next := filepath.Join(dir, "seen"), filepath.Join(dir, "next")
	editor := filepath.Join(dir, "editor.sh")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ncat \"$1\" > "+seen+"\ncat "+next+" > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	edit := func(content string) string {
		t.Helper()
		if err := os.WriteFile(next, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(seshBin(t), "context", "edit", "--id", th.ID)
		cmd.Env = sandboxEnv(map[string]string{"SESH_HOME": sb.Home, "SESH_MACHINE": sb.Machine,
			"SESH_TMUX_SOCKET": sb.TmuxSocket, "SESH_MASTER_SOCKET": sb.MasterSocket, "EDITOR": editor, "VISUAL": ""})
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("context edit: %v\n%s", err, out)
		}
		return string(out)
	}
	out := edit("second\nline two\n")
	if b, _ := os.ReadFile(seen); string(b) != "first" {
		t.Fatalf("the editor was given %q", b)
	}
	if got := showContext(t, sb, th.ID).Text; got != "second\nline two" {
		t.Fatalf("after edit: %q (%s)", got, out)
	}
	if out = edit("second\nline two\n"); !strings.Contains(out, "unchanged") {
		t.Fatalf("an unchanged save reported %q", out)
	}
	edit("\n")
	if got := showContext(t, sb, th.ID); got.Hash != "" {
		t.Fatalf("an emptied file left %+v", got)
	}
}
