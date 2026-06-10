package conformance

// thread.transcript cells (PARITY_ROADMAP D0): the transcript-resolution layer
// against REAL agent files — after a real turn, the owner-side read locates
// the agent's own transcript, the lines contain what was really said, and
// last_reply extraction matches the sentinel with a monotone reply count.
// Remote = routed read off the peer (transcript content is never replicated).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, agent := range []matrix.Agent{matrix.Claude, matrix.Codex, matrix.Pi} {
		a := agent
		matrix.RegisterTest("thread.transcript", a, matrix.Local,
			func(t *testing.T) { testTranscript(t, string(a), matrix.Local) })
		matrix.RegisterTest("thread.transcript", a, matrix.Remote,
			func(t *testing.T) { testTranscript(t, string(a), matrix.Remote) })
	}
}

type transcriptJSON struct {
	Path       string   `json:"path"`
	Lines      []string `json:"lines"`
	LastReply  string   `json:"last_reply"`
	ReplyCount int      `json:"reply_count"`
}

func testTranscript(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, agent, "scribed")

	// Before any turn: loud (no transcript exists — never an empty success).
	if _, stderr, err := sb.Runner.Run(t, "thread", "transcript", "--id", th.ID); err == nil {
		t.Errorf("[%s/%s] transcript before any turn succeeded silently", agent, loc)
	} else if !strings.Contains(stderr, "transcript") {
		t.Errorf("pre-turn error wrong: %s", stderr)
	}

	// A real turn with a sentinel, then the read.
	sb.headlessTurn(t, th.ID, "Reply with exactly the word PERIWINKLE and nothing else")
	out, stderr, err := sb.Runner.Run(t, "thread", "transcript", "--id", th.ID, "--json")
	if err != nil {
		t.Fatalf("[%s/%s] transcript: %v\n%s", agent, loc, err, stderr)
	}
	var tr transcriptJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &tr); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if tr.Path == "" || len(tr.Lines) == 0 {
		t.Fatalf("[%s/%s] empty transcript read: path=%q lines=%d", agent, loc, tr.Path, len(tr.Lines))
	}
	joined := strings.Join(tr.Lines, "\n")
	if !strings.Contains(joined, "PERIWINKLE") {
		t.Errorf("[%s/%s] transcript lines missing what was really said", agent, loc)
	}
	if !strings.Contains(tr.LastReply, "PERIWINKLE") {
		t.Errorf("[%s/%s] last_reply = %q, want the sentinel", agent, loc, tr.LastReply)
	}
	if tr.ReplyCount < 1 {
		t.Errorf("[%s/%s] reply_count = %d, want >= 1", agent, loc, tr.ReplyCount)
	}

	if agent != "pi" || loc != matrix.Local {
		return
	}

	// The reply count is MONOTONE (the subscribe engine's dedup contract): a
	// second turn increments it and last_reply moves.
	sb.headlessTurn(t, th.ID, "Reply with exactly the word AUBERGINE and nothing else")
	out, _, err = sb.Runner.Run(t, "thread", "transcript", "--id", th.ID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var tr2 transcriptJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &tr2); err != nil {
		t.Fatal(err)
	}
	if tr2.ReplyCount <= tr.ReplyCount {
		t.Errorf("reply_count not monotone: %d -> %d", tr.ReplyCount, tr2.ReplyCount)
	}
	if !strings.Contains(tr2.LastReply, "AUBERGINE") {
		t.Errorf("last_reply did not advance: %q", tr2.LastReply)
	}

	// --tail honors the bound.
	out, _, err = sb.Runner.Run(t, "thread", "transcript", "--id", th.ID, "--tail", "2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var tr3 transcriptJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &tr3); err != nil {
		t.Fatal(err)
	}
	if len(tr3.Lines) > 2 {
		t.Errorf("--tail 2 returned %d lines", len(tr3.Lines))
	}
}
