package conformance

// thread.context and thread.context-headless cells (schema 60).
//
// A thread's custom context must reach its agent through the agent's OWN hooks —
// claude/codex SessionStart + UserPromptSubmit (attached by sesh at launch), pi's
// sesh-agent-state extension — so it applies to an agent that is already running
// and to every fresh per-turn headless process. Two kinds of evidence, both
// external to sesh:
//
//   - the REPLY: the real model, asked for a tag the context defines, answers
//     with it — and with the new tag after a change, and NONE after a clear;
//   - the TRANSCRIPT: the harness's own record of what it sent. claude/codex keep
//     each injection as a conversation item, so a version appears exactly once
//     per session however many prompts follow; pi records its system-prompt
//     section (and a change notice).
//
// Claude's context can instead arrive from its context file (an `instructions`
// record), and a change to a running process from the hook.
//
// Remote = the thread lives on the peer; every command is routed with --machine,
// and the agent's hooks talk to the peer's daemon.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.context", a, loc,
				func(t *testing.T) { testThreadContext(t, string(a), loc) })
			matrix.RegisterTest("thread.context-headless", a, loc,
				func(t *testing.T) { testThreadContextHeadless(t, string(a), loc) })
		}
	}
}

// contextModels are the models the context cells run on: ones that reliably
// follow a CHANGED instruction mid-conversation (gpt-6-luna under pi's default
// thinking level kept repeating its first answer about half the time).
var contextModels = map[string]string{
	"claude": "haiku",
	"codex":  "gpt-6-astra",
	"pi":     "openai-codex/gpt-6-astra",
}

func tagContext(tag string) string {
	return fmt.Sprintf("Project facts for this thread:\n- The project tag is %s.\nWhen the user asks for the project tag, reply with exactly the tag and nothing else.", tag)
}

func randomTag(prefix string) string {
	return fmt.Sprintf("%s-%04d", prefix, rand.Intn(10000))
}

func setContext(t *testing.T, sb *Sandbox, id, text string) api.SetThreadContextResponse {
	t.Helper()
	out, stderr, err := sb.Runner.Run(t, "context", "set", "--id", id, "--text", text, "--json")
	if err != nil {
		t.Fatalf("context set: %v\n%s", err, stderr)
	}
	var resp api.SetThreadContextResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("decode context set: %v\n%s", err, out)
	}
	if !resp.Changed || resp.Hash == "" || resp.Thread.ContextHash != resp.Hash {
		t.Fatalf("context set answered %+v", resp)
	}
	return resp
}

func clearContext(t *testing.T, sb *Sandbox, id string) {
	t.Helper()
	if _, stderr, err := sb.Runner.Run(t, "context", "clear", "--id", id); err != nil {
		t.Fatalf("context clear: %v\n%s", err, stderr)
	}
}

func showContext(t *testing.T, sb *Sandbox, id string) api.ThreadContextResponse {
	t.Helper()
	out, stderr, err := sb.Runner.Run(t, "context", "show", "--id", id, "--json")
	if err != nil {
		t.Fatalf("context show: %v\n%s", err, stderr)
	}
	var resp api.ThreadContextResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("decode context show: %v\n%s", err, out)
	}
	return resp
}

// injections counts, in the harness's own transcript records, the items carrying
// context version `hash` (or, with hash "", the removal notice).
func injections(t *testing.T, agent string, lines []string, hash string) int {
	t.Helper()
	marker := `version="` + hash + `"`
	if hash == "" {
		marker = `removed="true"`
	}
	n := 0
	for _, l := range lines {
		var rec map[string]any
		if json.Unmarshal([]byte(l), &rec) != nil {
			continue
		}
		switch agent {
		case "claude":
			// claude keeps each hook injection as one attachment record (its
			// request blobs repeat message content, so raw text counts lie).
			a, _ := rec["attachment"].(map[string]any)
			if rec["type"] == "attachment" && a["type"] == "hook_additional_context" && strings.Contains(fmt.Sprint(a["content"]), marker) {
				n++
			}
		case "codex":
			p, _ := rec["payload"].(map[string]any)
			if rec["type"] == "response_item" && p["type"] == "message" && strings.Contains(fmt.Sprint(p["content"]), marker) {
				n++
			}
		case "pi":
			// pi: the change notice (a custom message). The section itself is
			// checked by piSection.
			if rec["type"] == "custom_message" && rec["customType"] == "sesh-thread-context" && strings.Contains(fmt.Sprint(rec["content"]), marker) {
				n++
			}
		}
	}
	return n
}

// piSection returns pi's latest recorded value of the sesh_thread_context
// system-prompt section ("" = removed or never set) and whether it was recorded.
func piSection(lines []string) (string, bool) {
	val, seen := "", false
	for _, l := range lines {
		var rec struct {
			Type    string `json:"type"`
			Message struct {
				Role     string          `json:"role"`
				Sections json.RawMessage `json:"sections"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(l), &rec) != nil || rec.Type != "message" || rec.Message.Role != "system" {
			continue
		}
		var secs map[string]*string
		if json.Unmarshal(rec.Message.Sections, &secs) != nil {
			continue
		}
		if v, ok := secs["sesh_thread_context"]; ok {
			seen = true
			val = ""
			if v != nil {
				val = *v
			}
		}
	}
	return val, seen
}

// assertDelivered checks the transcript evidence that version `hash` reached the
// agent: exactly once for claude/codex, as the current system section for pi.
func assertDelivered(t *testing.T, sb *Sandbox, agent, id, hash string) {
	t.Helper()
	tr := transcriptOf(t, sb, id)
	if agent == "pi" {
		sec, seen := piSection(tr.Lines)
		if !seen || !strings.Contains(sec, "version "+hash) {
			t.Fatalf("pi's system prompt does not carry context version %s (section %q, seen=%v)", hash, sec, seen)
		}
		return
	}
	n := injections(t, agent, tr.Lines, hash)
	if agent == "claude" && n <= 1 && contextFileLoads(tr.Lines, hash) > 0 {
		return // loaded from the context file; at most one hook copy on top
	}
	if n != 1 {
		t.Fatalf("context version %s was injected %d times into %s's conversation, want exactly 1 (transcript %s)", hash, n, agent, tr.Path)
	}
}

// contextFileLoads counts the claude instruction loads carrying context version
// hash: claude records each load of the thread's context file as an `instructions`
// attachment listing the files it read.
func contextFileLoads(lines []string, hash string) int {
	n := 0
	for _, l := range lines {
		var rec struct {
			Type       string `json:"type"`
			Attachment struct {
				Type  string `json:"type"`
				Files []struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				} `json:"files"`
			} `json:"attachment"`
		}
		if json.Unmarshal([]byte(l), &rec) != nil || rec.Type != "attachment" || rec.Attachment.Type != "instructions" {
			continue
		}
		for _, f := range rec.Attachment.Files {
			if strings.Contains(f.Path, "thread-context") && strings.Contains(f.Content, `version="`+hash+`"`) {
				n++
			}
		}
	}
	return n
}

func assertReplyHas(t *testing.T, what, reply, want string) {
	t.Helper()
	if !strings.Contains(reply, want) {
		t.Fatalf("%s: the agent replied %q, want it to contain %q", what, reply, want)
	}
}

func testThreadContext(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := newThreadOnModel(t, sb, agent, "ctx", contextModels[agent], false)
	sb.waitThreadReady(t, th.ID, agent)
	_, pidBefore := liveAgentPID(t, sb, th.ID)

	// Set while the agent is ALREADY running: no restart, the next prompt has it.
	tagA := randomTag("OSPREY")
	a := setContext(t, sb, th.ID, tagContext(tagA))
	if got := showContext(t, sb, th.ID); got.Text != tagContext(tagA) || got.Hash != a.Hash {
		t.Fatalf("context show = %+v", got)
	}
	if row := gridRow(t, sb, false, th.ID); row.ContextHash != a.Hash {
		t.Fatalf("grid row context_hash = %q, want %q", row.ContextHash, a.Hash)
	}
	assertReplyHas(t, "first prompt after set", headfulTurn(t, sb, th.ID, "What is the project tag? Reply with only the tag."), tagA)
	assertDelivered(t, sb, agent, th.ID, a.Hash)

	// More prompts do not repeat the injection (claude/codex keep it in history).
	headfulTurn(t, sb, th.ID, "Reply with only: OK")
	assertDelivered(t, sb, agent, th.ID, a.Hash)

	// A change reaches the same running process at its next prompt.
	tagB := randomTag("HERON")
	b := setContext(t, sb, th.ID, tagContext(tagB))
	assertReplyHas(t, "first prompt after a change", headfulTurn(t, sb, th.ID, "The project tag may have changed. What is the project tag now? Reply with only the tag."), tagB)
	assertDelivered(t, sb, agent, th.ID, b.Hash)
	if agent == "pi" {
		if n := injections(t, agent, transcriptOf(t, sb, th.ID).Lines, b.Hash); n != 1 {
			t.Fatalf("pi got %d change notices for version %s, want 1", n, b.Hash)
		}
	}

	// A clear is told, and the agent stops using the old context.
	clearContext(t, sb, th.ID)
	if row := gridRow(t, sb, false, th.ID); row.ContextHash != "" {
		t.Fatalf("grid row still carries context_hash %q after clear", row.ContextHash)
	}
	reply := headfulTurn(t, sb, th.ID, "Does your sesh thread context currently define a project tag? If it does, reply with the tag; if it does not, reply with exactly NONE.")
	assertReplyHas(t, "first prompt after a clear", reply, "NONE")
	if strings.Contains(reply, tagB) {
		t.Fatalf("after a clear the agent still answered with the old tag: %q", reply)
	}
	lines := transcriptOf(t, sb, th.ID).Lines
	if agent == "pi" {
		if sec, _ := piSection(lines); sec != "" {
			t.Fatalf("pi's system prompt still carries the context after a clear: %q", sec)
		}
	}
	if n := injections(t, agent, lines, ""); n != 1 {
		t.Fatalf("the removal was told %d times, want 1", n)
	}

	// All of it happened in the agent that was running before the first set.
	if _, pid := liveAgentPID(t, sb, th.ID); pid != pidBefore {
		t.Fatalf("the agent process changed (%d -> %d): context must apply without a restart", pidBefore, pid)
	}
}

func testThreadContextHeadless(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := newThreadOnModel(t, sb, agent, "ctx-headless", contextModels[agent], true)
	tagA := randomTag("KESTREL")
	a := setContext(t, sb, th.ID, tagContext(tagA))

	// Turn 1 creates the conversation (claude/pi: --session-id; codex mints its id).
	sb.headlessTurn(t, th.ID, "What is the project tag? Reply with only the tag.")
	assertReplyHas(t, "headless turn 1", sb.headlessReply(t, th.ID).Reply, tagA)
	assertDelivered(t, sb, agent, th.ID, a.Hash)

	// Turn 2 is a NEW process resuming it: SessionStart(resume) must not re-inject.
	sb.headlessTurn(t, th.ID, "Reply with only: OK")
	assertDelivered(t, sb, agent, th.ID, a.Hash)

	// A change reaches the next turn's process.
	tagB := randomTag("MERLIN")
	b := setContext(t, sb, th.ID, tagContext(tagB))
	sb.headlessTurn(t, th.ID, "The project tag may have changed. What is the project tag now? Reply with only the tag.")
	assertReplyHas(t, "headless turn after a change", sb.headlessReply(t, th.ID).Reply, tagB)
	assertDelivered(t, sb, agent, th.ID, b.Hash)
}
