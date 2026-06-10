package conformance

// thread.fork cells (PARITY_ROADMAP D3): rewind-and-branch over REAL
// conversations. Per agent: a source with TWO real turns (sentinels A then B)
// forks at --message-id 1 — the fork's transcript carries A but NOT B
// (deterministic divergence proof), a real turn CONTINUES the forked
// conversation (it remembers A), and the source transcript is byte-untouched.
// A full fork (no message-id) carries both. Remote = routed fork on the peer.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, agent := range []matrix.Agent{matrix.Claude, matrix.Codex, matrix.Pi} {
		a := agent
		matrix.RegisterTest("thread.fork", a, matrix.Local,
			func(t *testing.T) { testFork(t, string(a), matrix.Local) })
		matrix.RegisterTest("thread.fork", a, matrix.Remote,
			func(t *testing.T) { testFork(t, string(a), matrix.Remote) })
	}
}

func forkedTranscript(t *testing.T, sb *Sandbox, id string) string {
	t.Helper()
	out, stderr, err := sb.Runner.Run(t, "thread", "transcript", "--id", id, "--json")
	if err != nil {
		t.Fatalf("transcript %s: %v\n%s", id, err, stderr)
	}
	return out
}

func testFork(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	src := sb.newHeadlessThread(t, agent, "trunk")
	sb.headlessTurn(t, src.ID, "Reply with exactly the word OBSIDIAN and nothing else")
	sb.headlessTurn(t, src.ID, "Reply with exactly the word FELDSPAR and nothing else")

	srcBefore := forkedTranscript(t, sb, src.ID)
	if !strings.Contains(srcBefore, "OBSIDIAN") || !strings.Contains(srcBefore, "FELDSPAR") {
		t.Fatalf("[%s/%s] source turns missing", agent, loc)
	}

	// Fork after turn 1: the branch knows A, not B.
	out, stderr, err := sb.Runner.Run(t, "thread", "new", "--fork-from", src.ID[:8], "--message-id", "1", "--name", "branch1", "--json")
	if err != nil {
		t.Fatalf("[%s/%s] fork: %v\n%s", agent, loc, err, stderr)
	}
	var forked struct {
		ID             string `json:"id"`
		AgentSessionID string `json:"agent_session_id"`
		AgentKind      string `json:"agent_kind"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &forked); err != nil {
		t.Fatalf("decode fork: %v\n%s", err, out)
	}
	if forked.AgentKind != agent || forked.AgentSessionID == "" || forked.AgentSessionID == src.AgentSessionID {
		t.Fatalf("[%s/%s] fork identity wrong: %+v (src session %s)", agent, loc, forked, src.AgentSessionID)
	}
	br := forkedTranscript(t, sb, forked.ID)
	if !strings.Contains(br, "OBSIDIAN") {
		t.Errorf("[%s/%s] branch lost the pre-fork turn", agent, loc)
	}
	if strings.Contains(br, "FELDSPAR") {
		t.Errorf("[%s/%s] branch carries the POST-fork turn (no rewind happened)", agent, loc)
	}

	// The branch CONTINUES as a real conversation — and remembers the prefix.
	sb.headlessTurn(t, forked.ID, "What word did I ask you to reply with earlier? Answer with just that word.")
	if r := sb.headlessReply(t, forked.ID); !strings.Contains(r.Reply, "OBSIDIAN") {
		t.Errorf("[%s/%s] branch lost its memory: %q", agent, loc, r.Reply)
	}

	// The source is untouched (same transcript path content, modulo nothing).
	if forkedTranscript(t, sb, src.ID) != srcBefore {
		t.Errorf("[%s/%s] the SOURCE transcript changed (fork must not touch it)", agent, loc)
	}

	if agent != "pi" || loc != matrix.Local {
		return
	}
	// Full fork (no --message-id) carries both turns; an out-of-range
	// message-id is loud; forking a turn-less thread is loud.
	out, _, err = sb.Runner.Run(t, "thread", "new", "--fork-from", src.ID, "--name", "fullbranch", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var full struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(strings.TrimSpace(out)), &full) //nolint:errcheck
	fb := forkedTranscript(t, sb, full.ID)
	if !strings.Contains(fb, "OBSIDIAN") || !strings.Contains(fb, "FELDSPAR") {
		t.Errorf("full fork missing turns")
	}
	if _, _, err := sb.Runner.Run(t, "thread", "new", "--fork-from", src.ID, "--message-id", "9", "--name", "x"); err == nil {
		t.Errorf("out-of-range --message-id succeeded silently")
	}
	bare := sb.newHeadlessThread(t, "pi", "noturns")
	if _, _, err := sb.Runner.Run(t, "thread", "new", "--fork-from", bare.ID, "--name", "y"); err == nil {
		t.Errorf("forking a turn-less thread succeeded silently")
	}
}
