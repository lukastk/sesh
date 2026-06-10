package conformance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	// Headless threads (stateless-per-turn): a durable conversation with no tmux
	// window; a turn runs the agent's --print/exec interface and the thread is
	// "working" only while that turn process is in flight. All three agents +
	// both localities (Remote = --machine routing).
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.new.headless", a, loc,
				func(t *testing.T) { testThreadNewHeadless(t, string(a), loc) })
			matrix.RegisterTest("thread.send.headless", a, loc,
				func(t *testing.T) { testThreadSendHeadless(t, string(a), loc) })
		}
	}
}

// testThreadNewHeadless asserts a headless thread is a record with NO tmux
// window: it is headless, appears in the list, and has no tmux session.
func testThreadNewHeadless(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newHeadlessThread(t, agent, "hl")
	if th.AgentKind != agent {
		t.Errorf("agent_kind = %q, want %q", th.AgentKind, agent)
	}
	if !hasThread(sb.listThreads(t), th.ID) {
		t.Errorf("headless thread missing from list")
	}
	// No tmux session backs a headless thread.
	if _, err := sb.rawTmux(t, "has-session", "-t", "="+th.SessionName); err == nil {
		t.Errorf("headless thread unexpectedly has a tmux session %q", th.SessionName)
	}
	// It is IDLE (the unified no-runtime state) with no turn in flight.
	if got := sb.threadStatus(t, th.ID).Activity; got != api.ActivityIdle {
		t.Errorf("fresh headless thread activity = %s, want idle", got)
	}
}

// testThreadSendHeadless asserts a turn is delivered to a headless thread and
// processed: the thread goes working WHILE the turn runs (the liveness signal),
// returns to waiting, and a real reply is produced — for each agent.
func testThreadSendHeadless(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newHeadlessThread(t, agent, "hl")
	const token = "SESHHL_4kq9"
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", th.ID, "--text",
		"Reply with exactly this token and nothing else: "+token); err != nil {
		t.Fatalf("send-headless: %v\n%s", err, stderr)
	}

	// The thread is "working" while the turn process is in flight (the live
	// signal the design promises).
	if !waitUntil(45*time.Second, func() bool { return sb.threadStatus(t, th.ID).Activity == api.ActivityWorking }) {
		t.Fatalf("headless thread never went working after a turn")
	}
	// ...then returns to IDLE once the turn completes (the unified no-runtime state).
	if !waitUntil(120*time.Second, func() bool { return !sb.headlessReply(t, th.ID).Working }) {
		t.Fatalf("headless turn never completed")
	}
	if got := sb.threadStatus(t, th.ID).Activity; got != api.ActivityIdle {
		t.Errorf("after turn, activity = %s, want idle", got)
	}

	// A real reply was produced (the agent processed the turn), and it carries the
	// token we asked for.
	reply := sb.headlessReply(t, th.ID)
	if !reply.HaveReply || reply.Reply == "" || strings.HasPrefix(reply.Reply, "ERROR:") {
		t.Fatalf("no valid reply produced: %+v", reply)
	}
	if !strings.Contains(reply.Reply, token) {
		t.Errorf("reply does not contain the token %q: %q", token, reply.Reply)
	}

	// UNIFIED-MODEL direction 1: a headless turn on a HEADED-BORN idle thread
	// resumes ITS conversation — headless-ness is runtime, not a stored mode.
	hd := sb.newThread(t, agent, "hdturn", "/tmp")
	sb.waitThreadReady(t, hd.ID, agent)
	fact := "VERMILION_" + strings.ToUpper(agent)
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", hd.ID, "--text",
		"Remember this codeword: "+fact+". Just reply: ok."); err != nil {
		t.Fatalf("plant fact: %v\n%s", err, stderr)
	}
	if !waitUntil(45*time.Second, func() bool { return sb.threadStatus(t, hd.ID).Activity == api.ActivityWorking }) {
		t.Fatalf("fact turn never started")
	}
	if !waitUntil(90*time.Second, func() bool { return sb.threadStatus(t, hd.ID).Activity == api.ActivityWaiting }) {
		t.Fatalf("fact turn never completed")
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", hd.ID); err != nil {
		t.Fatalf("stop headed: %v\n%s", err, stderr)
	}
	if !waitUntil(15*time.Second, func() bool { return sb.threadStatus(t, hd.ID).Activity == api.ActivityIdle }) {
		t.Fatalf("headed thread never went idle after stop")
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", hd.ID, "--text",
		"What was the codeword I told you earlier? Reply with ONLY the codeword."); err != nil {
		t.Fatalf("send-headless on a headed-born idle thread: %v\n%s", err, stderr)
	}
	if !waitUntil(120*time.Second, func() bool {
		r := sb.headlessReply(t, hd.ID)
		return !r.Working && r.HaveReply
	}) {
		t.Fatalf("headed-born turn never completed")
	}
	if r := sb.headlessReply(t, hd.ID); !strings.Contains(r.Reply, fact) {
		t.Errorf("headed-born turn lost the conversation: reply %q lacks %q", r.Reply, fact)
	}

	// UNIFIED-MODEL direction 2: a headless turn against a LIVE pane is refused
	// loudly (it would fork the conversation the pane owns).
	live := sb.newThread(t, agent, "liveguard", "/tmp")
	sb.waitThreadReady(t, live.ID, agent)
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", live.ID, "--text", "nope"); err == nil {
		t.Errorf("send-headless against a LIVE pane succeeded — must be a loud conflict")
	} else if !strings.Contains(stderr, "live pane") {
		t.Errorf("live-pane refusal has the wrong message: %s", stderr)
	}
}

// ---- helpers ----

func (sb *Sandbox) newHeadlessThread(t *testing.T, agent, name string) api.Thread {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", agent, "--name", name, "--cwd", "/tmp", "--headless", "--json")
	if err != nil {
		t.Fatalf("thread new --headless (%s): %v\n%s", agent, err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(stdout), &th); err != nil {
		t.Fatalf("decode headless thread: %v\nraw: %s", err, stdout)
	}
	return th
}

func (sb *Sandbox) headlessReply(t *testing.T, id string) api.HeadlessReplyResponse {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "headless-reply", "--id", id, "--json")
	if err != nil {
		t.Fatalf("headless-reply: %v\n%s", err, stderr)
	}
	var resp api.HeadlessReplyResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode headless-reply: %v\nraw: %s", err, stdout)
	}
	return resp
}
