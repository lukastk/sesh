package daemon

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/agents/claude"
	"github.com/lukastk/sesh/internal/api"
)

// leafMap builds a resolver over an explicit anchor->leaf table, recording which
// threads it was asked about so the NARROWING is observable rather than assumed.
func leafMap(table map[string]string, asked *[]string) func(string, string) (string, error) {
	return func(sessionID, cwd string) (string, error) {
		if asked != nil {
			*asked = append(*asked, sessionID)
		}
		if leaf, ok := table[sessionID]; ok {
			return leaf, nil
		}
		return sessionID, nil // no drift
	}
}

func TestMatchHeldThreadsFindsTheHeldThread(t *testing.T) {
	// The production shape: the thread's record anchors at `anchor`, claude drifted
	// the conversation forward to `leaf`, and `leaf` is the held background session.
	sessions := []claude.BackgroundSession{
		{ID: "92e176d4", SessionID: "leaf", Cwd: "/work/french", State: "blocked", Name: "French listening deck"},
	}
	threads := []api.Thread{
		{ID: "296e85ae-dead-beef", AgentKind: "claude", Cwd: "/work/french", AgentSessionID: "anchor"},
	}
	var asked []string
	got, err := matchHeldThreads(threads, sessions, leafMap(map[string]string{"anchor": "leaf"}, &asked))
	if err != nil {
		t.Fatalf("matchHeldThreads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d held threads, want 1: %+v", len(got), got)
	}
	if got[0].Thread.ID != "296e85ae-dead-beef" || got[0].Background.ID != "92e176d4" || got[0].Leaf != "leaf" {
		t.Errorf("wrong pairing: %+v", got[0])
	}
}

// The thread must be matched on its RESOLVED LEAF, not its recorded anchor. Matching
// the anchor would miss every real case — the whole reason these threads are stuck is
// that claude moved the conversation to a new id.
func TestMatchHeldThreadsMatchesTheLeafNotTheAnchor(t *testing.T) {
	sessions := []claude.BackgroundSession{{ID: "bg", SessionID: "anchor", Cwd: "/work/x", State: "done"}}
	threads := []api.Thread{{ID: "t1", AgentKind: "claude", Cwd: "/work/x", AgentSessionID: "anchor"}}
	// The anchor is held, but the conversation has MOVED ON to a free leaf, so the
	// thread is revivable and must not be reported.
	got, err := matchHeldThreads(threads, sessions, leafMap(map[string]string{"anchor": "leaf-free"}, nil))
	if err != nil {
		t.Fatalf("matchHeldThreads: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a thread whose LEAF is free must not be reported held (its stale anchor being held is irrelevant): %+v", got)
	}
}

// Narrowing by project dir is an exactness claim, not just a speed trick: a session
// can only be a thread's leaf if both live in the same project dir. The resolver must
// therefore be consulted for the same-cwd thread and NOT for the others — resolving
// every claude thread would scan every transcript on the machine.
func TestMatchHeldThreadsNarrowsToTheSameProjectDir(t *testing.T) {
	sessions := []claude.BackgroundSession{{ID: "bg", SessionID: "leaf", Cwd: "/work/french", State: "blocked"}}
	threads := []api.Thread{
		{ID: "same", AgentKind: "claude", Cwd: "/work/french", AgentSessionID: "a-same"},
		{ID: "other", AgentKind: "claude", Cwd: "/work/finnish", AgentSessionID: "a-other"},
		{ID: "prefix", AgentKind: "claude", Cwd: "/work/french-listening", AgentSessionID: "a-prefix"},
	}
	var asked []string
	got, err := matchHeldThreads(threads, sessions, leafMap(map[string]string{"a-same": "leaf"}, &asked))
	if err != nil {
		t.Fatalf("matchHeldThreads: %v", err)
	}
	if len(got) != 1 || got[0].Thread.ID != "same" {
		t.Fatalf("want only the same-cwd thread held, got %+v", got)
	}
	sort.Strings(asked)
	if len(asked) != 1 || asked[0] != "a-same" {
		t.Errorf("resolver was asked about %v, want exactly [a-same] — a thread in another project dir can never be the holder's leaf, and resolving it costs a full transcript scan", asked)
	}
}

// Skips that must hold, each for its own reason.
func TestMatchHeldThreadsSkips(t *testing.T) {
	sessions := []claude.BackgroundSession{{ID: "bg", SessionID: "leaf", Cwd: "/work/x", State: "done"}}
	table := map[string]string{"anchor": "leaf"}
	cases := []struct {
		name   string
		thread api.Thread
		why    string
	}{
		{"archived", api.Thread{ID: "t", AgentKind: "claude", Cwd: "/work/x", AgentSessionID: "anchor", Archived: true},
			"an archived thread is not revived, so a hold on it is not a problem to report"},
		{"codex", api.Thread{ID: "t", AgentKind: "codex", Cwd: "/work/x", AgentSessionID: "anchor"},
			"only claude has background sessions"},
		{"pi", api.Thread{ID: "t", AgentKind: "pi", Cwd: "/work/x", AgentSessionID: "anchor"},
			"only claude has background sessions"},
		{"no session id", api.Thread{ID: "t", AgentKind: "claude", Cwd: "/work/x"},
			"a thread with no conversation has none to hold"},
		{"no cwd", api.Thread{ID: "t", AgentKind: "claude", AgentSessionID: "anchor"},
			"without a cwd there is no project dir, so nothing can be matched"},
	}
	for _, c := range cases {
		got, err := matchHeldThreads([]api.Thread{c.thread}, sessions, leafMap(table, nil))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(got) != 0 {
			t.Errorf("%s: want no hold reported (%s), got %+v", c.name, c.why, got)
		}
	}
}

// A background session with no cwd cannot be narrowed against anything, so the check
// would be silently INCOMPLETE. It refuses instead — doctor turns that into a "could
// not check" warning rather than a false all-clear.
func TestMatchHeldThreadsRefusesCwdlessSession(t *testing.T) {
	sessions := []claude.BackgroundSession{{ID: "bg", SessionID: "leaf", State: "done"}}
	threads := []api.Thread{{ID: "t", AgentKind: "claude", Cwd: "/work/x", AgentSessionID: "leaf"}}
	if _, err := matchHeldThreads(threads, sessions, leafMap(nil, nil)); err == nil {
		t.Fatal("want a loud error for a background session with no cwd, got nil")
	}
}

// A resolver failure must propagate: "I could not resolve this thread's live session"
// is not "this thread is fine".
func TestMatchHeldThreadsPropagatesResolverError(t *testing.T) {
	sessions := []claude.BackgroundSession{{ID: "bg", SessionID: "leaf", Cwd: "/work/x", State: "done"}}
	threads := []api.Thread{{ID: "t1234567", AgentKind: "claude", Cwd: "/work/x", AgentSessionID: "anchor"}}
	_, err := matchHeldThreads(threads, sessions, func(string, string) (string, error) {
		return "", fmt.Errorf("transcript unreadable")
	})
	if err == nil || !strings.Contains(err.Error(), "transcript unreadable") {
		t.Fatalf("want the resolver error propagated, got %v", err)
	}
}

// The refusal is the whole user-facing value of the pre-flight: it must name the
// holder, what it is doing, every remedy in typeable form, and the cost of forcing.
func TestHeldSessionRefusalNamesTheRemedy(t *testing.T) {
	bg := claude.BackgroundSession{ID: "92e176d4", SessionID: "leaf", State: "working", Name: "French listening deck"}
	msg := heldSessionRefusal("296e85ae-1111", bg, "leaf")
	for _, want := range []string{
		"92e176d4",               // the SHORT id — the only form claude stop/attach take
		"claude attach 92e176d4", // look without stopping
		"claude stop 92e176d4",   // the by-hand remedy
		"--force",                // the sesh-side remedy
		"working",                // what stopping it would cost
		"interrupts a turn",      // said explicitly, not implied
		"296e85ae-1111",          // which thread
		"BACKGROUND SESSION",     // names the mechanism so it is searchable
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must mention %q; got:\n%s", want, msg)
		}
	}
	// It must NOT claim the conversation is lost — `claude stop` keeps it, and a
	// message implying otherwise would push someone towards `claude rm`.
	if strings.Contains(strings.ToLower(msg), "lost") || strings.Contains(strings.ToLower(msg), "discard") {
		t.Errorf("refusal must not suggest the conversation is lost; got:\n%s", msg)
	}
}

// The pin itself. The end-to-end proof that it reaches the claude process and stops
// ← ← is the thread.claude-no-agent-view cell; this guards the value and the override.
func TestPinClaudeAgentView(t *testing.T) {
	env := map[string]string{}
	pinClaudeAgentView(env)
	if env["CLAUDE_CODE_DISABLE_AGENT_VIEW"] != "1" {
		t.Fatalf("pin did not set CLAUDE_CODE_DISABLE_AGENT_VIEW=1: %v", env)
	}
	env = map[string]string{"CLAUDE_CODE_DISABLE_AGENT_VIEW": "0"}
	pinClaudeAgentView(env)
	if env["CLAUDE_CODE_DISABLE_AGENT_VIEW"] != "1" {
		t.Fatalf("a user-set 0 must be overridden inside a sesh pane: %v", env)
	}
}
