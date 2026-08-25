package codexfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rollout fixtures are the REAL line shapes, copied from live rollouts rather
// than invented: `current` from a codex 0.149.1 rollout captured for this test,
// `legacyEra` from a 2026-07-06 rollout (codex ~0.145), trimmed to the lines
// that matter.

// current: codex >= ~0.149 records the reply ONLY as a response_item message.
// There is no event_msg/agent_message line anywhere in these files.
const currentRollout = `{"type":"session_meta","payload":{"id":"01a03a38-8e83-7813-b420-d620f6b862ef","cwd":"/tmp/work"}}
{"type":"event_msg","payload":{"type":"task_started"}}
{"type":"response_item","payload":{"type":"message","id":"m1","role":"user","content":[{"type":"input_text","text":"Reply with exactly the word PERIWINKLE and nothing else"}]}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"m2","content":[{"type":"Text","text":"PERIWINKLE"}],"phase":"final_answer"}}}
{"type":"response_item","payload":{"type":"message","id":"m2","role":"assistant","content":[{"type":"output_text","text":"PERIWINKLE"}],"phase":"final_answer"}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"PERIWINKLE"}}
`

// legacyEra: a codex ~0.145 rollout carries BOTH the old event_msg/agent_message
// line AND the response_item message, for the SAME reply. This is the fixture
// that pins the no-double-count rule.
const legacyEraRollout = `{"type":"session_meta","payload":{"id":"019f36a9-bc54-7da2-b752-78544096b949","cwd":"/tmp/work"}}
{"type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"first question"}]}}
{"type":"event_msg","payload":{"type":"agent_message","message":"FIRST"}}
{"type":"response_item","payload":{"type":"message","id":"a1","role":"assistant","content":[{"type":"output_text","text":"FIRST"}]}}
{"type":"response_item","payload":{"type":"message","id":"u2","role":"user","content":[{"type":"input_text","text":"second question"}]}}
{"type":"event_msg","payload":{"type":"agent_message","message":"SECOND"}}
{"type":"response_item","payload":{"type":"message","id":"a2","role":"assistant","content":[{"type":"output_text","text":"SECOND"}]}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"SECOND"}}
`

func writeRollout(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

// TestLastReplyCurrentFormat is the regression for the drift that returned
// ("", 0) for every codex thread on codex >= ~0.149 — no last_reply and no
// reply count, i.e. a broken dedup marker for the subscribe engine, not just a
// red cell.
func TestLastReplyCurrentFormat(t *testing.T) {
	text, count, err := LastReply(writeRollout(t, currentRollout))
	if err != nil {
		t.Fatalf("LastReply: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (a current-format rollout must not read as zero replies)", count)
	}
	if text != "PERIWINKLE" {
		t.Errorf("text = %q, want the sentinel", text)
	}
}

// TestLastReplyLegacyEraNotDoubleCounted is the OTHER half, and the reason only
// ONE shape is matched: 232 of Lukas's 513 live rollouts carry the legacy
// event_msg/agent_message line alongside the response_item message, for the same
// reply. Matching both would double every count on all of them.
func TestLastReplyLegacyEraNotDoubleCounted(t *testing.T) {
	text, count, err := LastReply(writeRollout(t, legacyEraRollout))
	if err != nil {
		t.Fatalf("LastReply: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 — this rollout has 2 replies recorded in TWO shapes; "+
			"counting both shapes doubles it and corrupts the dedup marker", count)
	}
	if text != "SECOND" {
		t.Errorf("text = %q, want the last reply", text)
	}
}

// TestLastReplyIgnoresNonReplies pins what must NOT count: other roles, other
// response_item types, and an assistant turn that produced no text.
func TestLastReplyIgnoresNonReplies(t *testing.T) {
	body := `{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"system preamble"}]}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"a question"}]}}
{"type":"response_item","payload":{"type":"reasoning","content":[{"type":"text","text":"thinking out loud"}]}}
{"type":"response_item","payload":{"type":"function_call","name":"shell"}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":null}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"THE ONLY REPLY"}]}}
`
	text, count, err := LastReply(writeRollout(t, body))
	if err != nil {
		t.Fatalf("LastReply: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (developer/user/reasoning/function_call and text-less "+
			"assistant turns are not replies)", count)
	}
	if text != "THE ONLY REPLY" {
		t.Errorf("text = %q", text)
	}
}

// TestLastReplyMultiPartContent: content is a LIST of parts and the reply is
// their concatenation.
func TestLastReplyMultiPartContent(t *testing.T) {
	body := `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"AL"},{"type":"output_text","text":"PHA"}]}}
`
	text, _, err := LastReply(writeRollout(t, body))
	if err != nil {
		t.Fatalf("LastReply: %v", err)
	}
	if text != "ALPHA" {
		t.Errorf("text = %q, want the concatenated parts", text)
	}
}

// TestLastReplyEmptyAndAborted: a session with no agent reply reads as zero —
// the honest answer, not an error (one live rollout is exactly this).
func TestLastReplyEmptyAndAborted(t *testing.T) {
	body := `{"type":"session_meta","payload":{"id":"x","cwd":"/tmp"}}
{"type":"event_msg","payload":{"type":"task_started"}}
{"type":"world_state","payload":{}}
{"type":"turn_context","payload":{}}
`
	text, count, err := LastReply(writeRollout(t, body))
	if err != nil || count != 0 || text != "" {
		t.Errorf("aborted session: got (%q, %d, %v), want (\"\", 0, nil)", text, count, err)
	}
}

// TestLastReplyUnknownLineTypesDoNotTruncate guards a sharp edge:
// OffsetReader.ReadNew BREAKS on the first parse error, so a payload field whose
// real JSON type differs from the struct would silently drop the REST of the
// file. Newer codex versions add line types (world_state, turn_context) and the
// reply must still be found after them.
func TestLastReplyUnknownLineTypesDoNotTruncate(t *testing.T) {
	body := `{"type":"world_state","payload":{"anything":{"deeply":["nested",1,true,null]}}}
{"type":"turn_context","payload":{"cwd":"/tmp","approval_policy":"never"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total":123}}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"AFTER THE UNKNOWNS"}]}}
`
	text, count, err := LastReply(writeRollout(t, body))
	if err != nil {
		t.Fatalf("LastReply: %v", err)
	}
	if count != 1 || !strings.Contains(text, "AFTER THE UNKNOWNS") {
		t.Errorf("got (%q, %d) — an unknown line type truncated the read", text, count)
	}
}
