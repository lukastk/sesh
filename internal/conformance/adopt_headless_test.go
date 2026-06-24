package conformance

// thread.adopt-headless cells: bring an EXISTING conversation that is NOT running
// anywhere under management as a durable HEADLESS thread (pane-less adopt). The
// honest external effect proven here is CONTINUITY: a turn planted in a real
// source conversation is recalled by the adopted thread — so adopt bound to that
// REAL pre-existing conversation rather than minting a fresh one. Negatives
// (missing session id / unknown agent) are loud. LOCAL by design (the conversation
// transcript lives where its owning daemon resumes it; --machine routing of every
// verb is covered by route.parity).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, agent := range matrix.AllAgents {
		a := agent
		matrix.RegisterTest("thread.adopt-headless", a, matrix.Local,
			func(t *testing.T) { testAdoptHeadless(t, string(a)) })
	}
}

func testAdoptHeadless(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// 1. Build a REAL conversation: a headless source thread with one turn that
	// plants a codeword (this also mints codex's session id, which appears only
	// after the first turn).
	src := sb.newHeadlessThreadAt(t, agent, "src", "/tmp")
	codeword := "CORKBOARD_" + strings.ToUpper(agent)
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", src.ID, "--text",
		"Remember this codeword: "+codeword+". Just reply: ok."); err != nil {
		t.Fatalf("plant codeword: %v\n%s", err, stderr)
	}
	if !waitUntil(120*time.Second, func() bool {
		r := sb.headlessReply(t, src.ID)
		return !r.Working && r.HaveReply
	}) {
		t.Fatalf("[%s] source turn never completed", agent)
	}
	sessionID := sb.getThread(t, src.ID).AgentSessionID
	if sessionID == "" {
		t.Fatalf("[%s] source conversation has no session id after a turn", agent)
	}

	// 2. Delete the source RECORD (the transcript on disk survives) so the adopted
	// thread is the sole manager of the conversation — no two threads forking it.
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", src.ID); err != nil {
		t.Fatalf("delete source record: %v\n%s", err, stderr)
	}

	// 3. HEADLESS adopt: no pane, identity asserted via --agent + --session-id.
	out, _, err := sb.Runner.Run(t, "thread", "adopt", "--name", "corkboard",
		"--agent", agent, "--session-id", sessionID, "--cwd", "/tmp", "--json")
	if err != nil {
		t.Fatalf("[%s] headless adopt failed: %v", agent, err)
	}
	var th struct {
		ID             string `json:"id"`
		AgentKind      string `json:"agent_kind"`
		AgentSessionID string `json:"agent_session_id"`
		SessionName    string `json:"session_name"`
		Name           string `json:"name"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatalf("decode adopt: %v\n%s", err, out)
	}
	if th.AgentKind != agent {
		t.Errorf("[%s] adopted kind = %s", agent, th.AgentKind)
	}
	if th.AgentSessionID != sessionID {
		t.Errorf("[%s] adopted session = %s, want %s (the asserted id)", agent, th.AgentSessionID, sessionID)
	}
	if th.Name != "corkboard" {
		t.Errorf("[%s] adopted name = %q", agent, th.Name)
	}
	// It is a real HEADLESS thread: no tmux session backs it, state headless·idle.
	if _, err := sb.rawTmux(t, "has-session", "-t", "="+th.SessionName); err == nil {
		t.Errorf("[%s] headless-adopted thread unexpectedly has a tmux session %q", agent, th.SessionName)
	}
	if st := sb.threadStatus(t, th.ID); st.Head != api.Headless || st.Busy != api.BusyIdle {
		t.Errorf("[%s] adopted state = %s/%s, want headless/idle", agent, st.Head, st.Busy)
	}

	// 4. CONTINUITY: a turn on the adopted thread RESUMES the real conversation —
	// it recalls the codeword. This fails if adopt started a fresh conversation.
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", th.ID, "--text",
		"What was the codeword I told you earlier? Reply with ONLY the codeword."); err != nil {
		t.Fatalf("[%s] resume turn: %v\n%s", agent, err, stderr)
	}
	if !waitUntil(120*time.Second, func() bool {
		r := sb.headlessReply(t, th.ID)
		return !r.Working && r.HaveReply
	}) {
		t.Fatalf("[%s] adopted resume turn never completed", agent)
	}
	if r := sb.headlessReply(t, th.ID); !strings.Contains(r.Reply, codeword) {
		t.Errorf("[%s] adopt did not bind to the existing conversation: reply %q lacks %q", agent, r.Reply, codeword)
	}

	// Negatives (CLI-reachable, agent-agnostic so assert once on claude).
	if agent != "claude" {
		return
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "adopt", "--name", "x", "--agent", "claude"); err == nil {
		t.Errorf("headless adopt with no --session-id succeeded silently")
	} else if !strings.Contains(stderr, "requires --session-id") {
		t.Errorf("missing-session-id error wrong: %s", stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "adopt", "--name", "x", "--agent", "bogus", "--session-id", uuid.NewString()); err == nil {
		t.Errorf("headless adopt with an unknown agent succeeded silently")
	} else if !strings.Contains(stderr, "unknown agent") {
		t.Errorf("unknown-agent error wrong: %s", stderr)
	}
}
