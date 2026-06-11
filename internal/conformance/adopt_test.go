package conformance

// thread.adopt cells (PARITY_ROADMAP D5): a MANUALLY-launched real agent (a
// pane sesh did not spawn) is brought under management with its TRUE
// conversation identity — claude via argv, pi via its RPC socket, codex via
// its open rollout — and then behaves as a fully managed thread (headful
// status). Negatives: a non-agent pane, an already-managed pane, and a
// non-existent pane are loud. LOCAL-only by design (pane ids are per-server —
// the tmux.current precedent).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, agent := range []matrix.Agent{matrix.Claude, matrix.Codex, matrix.Pi} {
		a := agent
		matrix.RegisterTest("thread.adopt", a, matrix.Local,
			func(t *testing.T) { testAdopt(t, string(a)) })
	}
}

// manualAgentPane launches an agent MANUALLY on the work server (no sesh
// involvement) and returns its pane id.
func manualAgentPane(t *testing.T, sb *Sandbox, session, command string) string {
	t.Helper()
	if _, err := sb.rawTmux(t, "new-session", "-d", "-s", session, "-x", "200", "-y", "50", command); err != nil {
		t.Fatalf("manual session: %v", err)
	}
	t.Cleanup(func() { sb.rawTmux(t, "kill-session", "-t", "="+session) }) //nolint:errcheck
	out, err := sb.rawTmux(t, "list-panes", "-t", "="+session, "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v", err)
	}
	return strings.TrimSpace(out)
}

func testAdopt(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	codexHome := ""
	if lr, ok := sb.daemonRunner.(*localRunner); ok {
		codexHome = lr.env["SESH_CODEX_HOME"]
	}

	var pane, wantSession string
	switch agent {
	case "claude":
		wantSession = uuid.NewString()
		pane = manualAgentPane(t, sb, "foreign-claude", "claude --session-id "+wantSession)
	case "pi":
		wantSession = uuid.NewString()
		pane = manualAgentPane(t, sb, "foreign-pi", "pi --session-id "+wantSession)
	case "codex":
		// codex is only identifiable once a rollout exists: trust /tmp, launch,
		// and drive one manual turn through the pane.
		if err := agents.EnsureCodexTrust(codexHome, "/tmp"); err != nil {
			t.Fatal(err)
		}
		pane = manualAgentPane(t, sb, "foreign-codex", "cd /tmp && CODEX_HOME="+codexHome+" codex")
		time.Sleep(8 * time.Second) // codex TUI up
		sb.sendKeys(t, pane, "Reply with exactly: ok")
	}

	// Adopt loops until the agent is identifiable (codex: rollout minted by
	// the manual turn; claude/pi: process up).
	var adopted string
	if !waitUntil(90*time.Second, func() bool {
		out, _, err := sb.Runner.Run(t, "thread", "adopt", "--pane", pane, "--name", "ward", "--json")
		if err != nil {
			return false
		}
		adopted = out
		return true
	}) {
		_, stderr, _ := sb.Runner.Run(t, "thread", "adopt", "--pane", pane, "--name", "ward")
		t.Fatalf("[%s] adopt never succeeded; last loud reason: %s", agent, stderr)
	}
	var th struct {
		ID             string `json:"id"`
		AgentKind      string `json:"agent_kind"`
		AgentSessionID string `json:"agent_session_id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(adopted)), &th); err != nil {
		t.Fatalf("decode: %v\n%s", err, adopted)
	}
	if th.AgentKind != agent {
		t.Fatalf("[%s] adopted kind = %s", agent, th.AgentKind)
	}
	if agent == "codex" {
		if th.AgentSessionID == "" {
			t.Fatalf("codex adopted without a session id")
		}
	} else if th.AgentSessionID != wantSession {
		t.Fatalf("[%s] adopted session = %s, want %s (the TRUE identity)", agent, th.AgentSessionID, wantSession)
	}

	// Managed for real: the maintainer sees a live headful thread.
	if !waitUntil(30*time.Second, func() bool {
		return sb.threadStatus(t, th.ID).Head == "headful"
	}) {
		t.Fatalf("[%s] adopted thread never reported headful", agent)
	}
	// Re-adopting the same pane is loud.
	if _, stderr, err := sb.Runner.Run(t, "thread", "adopt", "--pane", pane, "--name", "again"); err == nil {
		t.Errorf("[%s] re-adopt succeeded silently", agent)
	} else if !strings.Contains(stderr, "already belongs") {
		t.Errorf("re-adopt error wrong: %s", stderr)
	}

	// Explicit --session-id override: a claude launched WITHOUT an id in its argv
	// (a bare `-r` in real life) can't be auto-detected — but an explicitly-supplied
	// id adopts it with that exact identity. (Auto-detect must still fail loudly.)
	if agent == "claude" {
		barePane := manualAgentPane(t, sb, "bare-claude", "claude")
		if !waitUntil(30*time.Second, func() bool {
			_, stderr, err := sb.Runner.Run(t, "thread", "adopt", "--pane", barePane, "--name", "bare-auto")
			return err != nil && strings.Contains(stderr, "carries no --session-id")
		}) {
			t.Errorf("bare claude (no id in argv) auto-adopt did not fail loudly")
		}
		bareSession := uuid.NewString()
		out, _, err := sb.Runner.Run(t, "thread", "adopt", "--pane", barePane, "--name", "bare-explicit", "--session-id", bareSession, "--json")
		if err != nil {
			t.Fatalf("explicit --session-id adopt failed: %v", err)
		}
		var bt struct {
			AgentSessionID string `json:"agent_session_id"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &bt); err != nil {
			t.Fatalf("decode explicit adopt: %v\n%s", err, out)
		}
		if bt.AgentSessionID != bareSession {
			t.Errorf("explicit adopt session = %s, want %s (the supplied id)", bt.AgentSessionID, bareSession)
		}
	}

	if agent != "pi" {
		return
	}
	shellPane := manualAgentPane(t, sb, "plain-shell", "sleep 300")
	if _, stderr, err := sb.Runner.Run(t, "thread", "adopt", "--pane", shellPane, "--name", "x"); err == nil {
		t.Errorf("adopting a shell pane succeeded silently")
	} else if !strings.Contains(stderr, "no coding agent") {
		t.Errorf("non-agent error wrong: %s", stderr)
	}
	if _, _, err := sb.Runner.Run(t, "thread", "adopt", "--pane", "%9999", "--name", "x"); err == nil {
		t.Errorf("adopting a non-existent pane succeeded silently")
	}
}
