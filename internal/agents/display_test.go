package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures below are the record shapes verified against live files on this
// machine (a 6,819-record pi 1.4.0 session, a 57,292-record Claude Code 2.1.x
// transcript, and a codex-cli 0.147 rollout); each decoder's doc comment records
// the full inventory they were read from.

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// piFixture installs a pi-format session for kind pi OR mypi — one store, one
// format — and returns the session id and cwd.
func piFixture(t *testing.T, lines []string) (session, cwd string, homes Homes) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", home)
	cwd = "/tmp/display-pi"
	session = "11111111-1111-4111-8111-111111111111"
	writeLines(t, filepath.Join(home, "sessions", "--tmp-display-pi--", "2026-10-30T00-00-00-000Z_"+session+".jsonl"), lines)
	return session, cwd, ResolveHomes("")
}

var piDisplayLines = []string{
	`{"type":"session","version":3,"id":"11111111-1111-4111-8111-111111111111","timestamp":"2026-10-30T10:00:00.000Z","cwd":"/tmp/display-pi"}`,
	`{"type":"model_change","id":"mc1","timestamp":"2026-10-30T10:00:01.000Z","provider":"anthropic","modelId":"opus"}`,
	`{"type":"message","id":"u1","timestamp":"2026-10-30T10:00:02.000Z","message":{"role":"user","content":[{"type":"text","text":"read the file"}]}}`,
	`{"type":"message","id":"a1","timestamp":"2026-10-30T10:00:03.500Z","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"thinking","thinking":"planning"},{"type":"text","text":"on it"},{"type":"toolCall","id":"call_1","name":"read","arguments":{"path":"/tmp/x"}}]}}`,
	`{"type":"message","id":"t1","timestamp":"2026-10-30T10:00:04.000Z","message":{"role":"toolResult","toolCallId":"call_1","toolName":"read","isError":true,"content":[{"type":"text","text":"no such file"}]}}`,
	`{"type":"message","id":"a2","timestamp":"2026-10-30T10:00:05.000Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"it is missing"}]}}`,
}

func TestDisplayProjectsPiRecordsWithToolsThinkingAndStatus(t *testing.T) {
	session, cwd, homes := piFixture(t, piDisplayLines)
	for _, kind := range []Kind{Pi, MyPi} {
		page, err := ReadDisplayWindow(kind, session, cwd, homes, DisplaySelector{Direction: DisplayTail, Limit: DisplayDefaultLimit})
		if err != nil {
			t.Fatalf("%s display: %v", kind, err)
		}
		if page.Format != "pi-session-jsonl" {
			t.Fatalf("%s format = %q", kind, page.Format)
		}
		if len(page.Items) != 4 {
			t.Fatalf("%s items = %d: %+v", kind, len(page.Items), page.Items)
		}
		// The session header and model_change carry no conversation content:
		// counted, not rendered as invented prose.
		if page.SkippedRecords != 2 || page.UnparsedLines != 0 {
			t.Fatalf("%s coverage = skipped %d unparsed %d", kind, page.SkippedRecords, page.UnparsedLines)
		}
		if !page.Complete || page.ByteLimited || page.OlderAvailable {
			t.Fatalf("%s coverage flags = complete %v byteLimited %v older %v", kind, page.Complete, page.ByteLimited, page.OlderAvailable)
		}
		if page.TotalLines != 6 || page.StartLine != 2 || page.EndLine != 6 {
			t.Fatalf("%s lines = total %d [%d,%d)", kind, page.TotalLines, page.StartLine, page.EndLine)
		}
		user := page.Items[0]
		if user.ID != "u1" || user.Seq != 2 || user.Role != "user" || user.Text != "read the file" {
			t.Fatalf("%s user item = %+v", kind, user)
		}
		if user.AtUnixMS == 0 {
			t.Fatalf("%s user item lost its native timestamp", kind)
		}
		assistant := page.Items[1]
		if assistant.Role != "assistant" || assistant.Text != "on it" || assistant.Thinking != "planning" {
			t.Fatalf("%s assistant item = %+v", kind, assistant)
		}
		if len(assistant.Tools) != 1 || assistant.Tools[0].Name != "read" || assistant.Tools[0].CallID != "call_1" ||
			!strings.Contains(assistant.Tools[0].Input, "/tmp/x") || assistant.Tools[0].Output != "" {
			t.Fatalf("%s assistant tool = %+v", kind, assistant.Tools)
		}
		result := page.Items[2]
		if result.Role != "tool" || len(result.Tools) != 1 || result.Tools[0].Output != "no such file" || !result.Tools[0].IsError {
			t.Fatalf("%s tool result item = %+v", kind, result)
		}
		// The status comes from pi's own DeriveTurnStatus over the file tail.
		if page.TurnStatus != "idle" {
			t.Fatalf("%s turn status = %q, want idle", kind, page.TurnStatus)
		}
	}
}

func TestDisplayPiTurnStatusFollowsTheFileTailNotThePage(t *testing.T) {
	busy := append(append([]string(nil), piDisplayLines...),
		`{"type":"message","id":"u2","timestamp":"2026-10-30T10:01:00.000Z","message":{"role":"user","content":[{"type":"text","text":"and again"}]}}`)
	session, cwd, homes := piFixture(t, busy)
	// A page BACK through history must still report the conversation's CURRENT
	// status, not the status at the point being read.
	page, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayOlder, AnchorLine: 3, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.TurnStatus != "busy" {
		t.Fatalf("older page turn status = %q, want busy (the file tail is a user turn)", page.TurnStatus)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "u1" {
		t.Fatalf("older page items = %+v", page.Items)
	}
	if page.Complete {
		t.Fatal("an older page must never claim complete file coverage")
	}
}

func TestDisplayClaudeRendersQueuedUserRecordsAndToolResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	cwd := "/tmp/display-claude"
	session := "22222222-2222-4222-8222-222222222222"
	homes := ResolveHomes("")
	writeLines(t, filepath.Join(home, "projects", "-tmp-display-claude", session+".jsonl"), []string{
		`{"type":"user","uuid":"cu1","timestamp":"2026-10-30T10:00:00.000Z","message":{"role":"user","content":"first prompt"}}`,
		`{"type":"attachment","parentUuid":"cu1","attachment":{"type":"environment","snapshot":{"workingDirectory":"/tmp/display-claude"}}}`,
		`{"type":"assistant","uuid":"ca1","timestamp":"2026-10-30T10:00:01.000Z","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"thinking it over"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","uuid":"cu2","timestamp":"2026-10-30T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a\nb","is_error":false}]}}`,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-10-30T10:00:03.000Z","content":"queued while mid-turn"}`,
		`{"type":"queue-operation","operation":"dequeue","timestamp":"2026-10-30T10:00:03.500Z","content":"queued while mid-turn"}`,
		`{"type":"system","uuid":"cs1","subtype":"informational","timestamp":"2026-10-30T10:00:04.000Z","content":"compacted while idle"}`,
		`{"type":"assistant","uuid":"ca2","timestamp":"2026-10-30T10:00:05.000Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`,
	})
	page, err := ReadDisplayWindow(Claude, session, cwd, homes, DisplaySelector{Direction: DisplayTail, Limit: DisplayDefaultLimit})
	if err != nil {
		t.Fatal(err)
	}
	if page.Format != "claude-transcript-jsonl" || page.TurnStatus != "idle" {
		t.Fatalf("claude page = format %q status %q", page.Format, page.TurnStatus)
	}
	var roles, ids []string
	for _, item := range page.Items {
		roles = append(roles, item.Role)
		ids = append(ids, item.ID)
	}
	wantRoles := []string{"user", "assistant", "tool", "user", "system", "assistant"}
	if strings.Join(roles, ",") != strings.Join(wantRoles, ",") {
		t.Fatalf("claude roles = %v, want %v (ids %v)", roles, wantRoles, ids)
	}
	// The enqueue record IS the user's message: claude consumes the queue entry
	// and writes no separate user record for it.
	queued := page.Items[3]
	if queued.Text != "queued while mid-turn" || queued.NativeType != "queue-operation" || queued.ID != "L4" {
		t.Fatalf("queued user item = %+v", queued)
	}
	if page.Items[2].Tools[0].CallID != "toolu_1" || page.Items[2].Tools[0].Output != "a\nb" {
		t.Fatalf("claude tool result = %+v", page.Items[2].Tools)
	}
	if page.Items[1].Tools[0].Name != "Bash" || !strings.Contains(page.Items[1].Tools[0].Input, "ls") {
		t.Fatalf("claude tool call = %+v", page.Items[1].Tools)
	}
	// attachment and the non-enqueue queue operation are skipped, not rendered.
	if page.SkippedRecords != 2 {
		t.Fatalf("claude skipped = %d", page.SkippedRecords)
	}
	if !page.Items[4].Injected {
		t.Fatal("a claude system record must be marked harness-authored")
	}
}

func TestDisplayCodexProjectsMessagesToolCallsAndMarksInjectedContext(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/display-codex"
	session := "33333333-3333-4333-8333-333333333333"
	homes := ResolveHomes(home)
	writeLines(t, filepath.Join(home, "sessions", "2026", "10", "30", "rollout-2026-10-30T10-00-00-"+session+".jsonl"), []string{
		`{"timestamp":"2026-10-30T10:00:00.000Z","type":"session_meta","payload":{"id":"` + session + `","cwd":"/tmp/display-codex"}}`,
		`{"timestamp":"2026-10-30T10:00:01.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-10-30T10:00:02.000Z","type":"response_item","payload":{"type":"message","id":"m0","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/tmp</cwd>\n</environment_context>"}]}}`,
		`{"timestamp":"2026-10-30T10:00:03.000Z","type":"response_item","payload":{"type":"message","id":"m1","role":"user","content":[{"type":"input_text","text":"list the files"}]}}`,
		`{"timestamp":"2026-10-30T10:00:04.000Z","type":"response_item","payload":{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"**Looking**"}]}}`,
		`{"timestamp":"2026-10-30T10:00:05.000Z","type":"response_item","payload":{"type":"function_call","id":"fc1","call_id":"call_a","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`,
		`{"timestamp":"2026-10-30T10:00:06.000Z","type":"response_item","payload":{"type":"function_call_output","id":"fco1","call_id":"call_a","output":[{"type":"input_text","text":"a\nb"}]}}`,
		`{"timestamp":"2026-10-30T10:00:07.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"dup","content":[{"type":"text","text":"two files"}]}}}`,
		`{"timestamp":"2026-10-30T10:00:08.000Z","type":"response_item","payload":{"type":"message","id":"m2","role":"assistant","content":[{"type":"output_text","text":"two files"}]}}`,
		`{"timestamp":"2026-10-30T10:00:09.000Z","type":"event_msg","payload":{"type":"task_complete"}}`,
	})
	page, err := ReadDisplayWindow(Codex, session, cwd, homes, DisplaySelector{Direction: DisplayTail, Limit: DisplayDefaultLimit})
	if err != nil {
		t.Fatal(err)
	}
	if page.Format != "codex-rollout-jsonl" || page.TurnStatus != "idle" {
		t.Fatalf("codex page = format %q status %q", page.Format, page.TurnStatus)
	}
	if len(page.Items) != 6 {
		t.Fatalf("codex items = %d: %+v", len(page.Items), page.Items)
	}
	if !page.Items[0].Injected || page.Items[1].Injected {
		t.Fatalf("codex context injection flags = %v / %v", page.Items[0].Injected, page.Items[1].Injected)
	}
	if page.Items[2].Role != "assistant" || page.Items[2].Thinking != "**Looking**" || page.Items[2].Text != "" {
		t.Fatalf("codex reasoning item = %+v", page.Items[2])
	}
	if page.Items[3].Role != "tool" || page.Items[3].Tools[0].Name != "shell" || page.Items[3].Tools[0].Input != `{"cmd":"ls"}` {
		t.Fatalf("codex call item = %+v", page.Items[3])
	}
	if page.Items[4].Tools[0].Output != "a\nb" || page.Items[4].Tools[0].CallID != "call_a" {
		t.Fatalf("codex output item = %+v", page.Items[4])
	}
	// The duplicate item_completed event must NOT appear: projecting both shapes
	// would show every codex reply twice.
	assistantReplies := 0
	for _, item := range page.Items {
		if item.Role == "assistant" && item.Text == "two files" {
			assistantReplies++
		}
	}
	if assistantReplies != 1 {
		t.Fatalf("codex assistant reply projected %d times", assistantReplies)
	}
}

func TestDisplayPagesBackAndPollsForwardWithoutLosingOrRepeatingItems(t *testing.T) {
	lines := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		lines = append(lines, fmt.Sprintf(
			`{"type":"message","id":"m%02d","timestamp":"2026-10-30T10:00:%02d.000Z","message":{"role":"user","content":[{"type":"text","text":"m%02d"}]}}`, i, i, i))
	}
	session, cwd, homes := piFixture(t, lines)

	newest, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayTail, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(newest.Items) != 5 || newest.Items[0].ID != "m07" || newest.Items[4].ID != "m11" {
		t.Fatalf("newest page = %+v", newest.Items)
	}
	if newest.Complete {
		t.Fatal("a limited tail page must not claim complete coverage")
	}
	if !newest.OlderAvailable {
		t.Fatal("older items exist but were not advertised")
	}

	older, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayOlder, AnchorLine: newest.StartLine, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Items) != 5 || older.Items[0].ID != "m02" || older.Items[4].ID != "m06" {
		t.Fatalf("older page = %+v", older.Items)
	}
	oldest, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayOlder, AnchorLine: older.StartLine, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(oldest.Items) != 2 || oldest.Items[0].ID != "m00" || oldest.OlderAvailable {
		t.Fatalf("oldest page = %+v (older advertised %v)", oldest.Items, oldest.OlderAvailable)
	}

	// Polling forward from the newest page sees nothing until the file grows,
	// and then sees exactly the new records — once.
	poll, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayNewer, AnchorLine: newest.NewerAnchor, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(poll.Items) != 0 {
		t.Fatalf("forward poll on an unchanged file returned %+v", poll.Items)
	}
	grown := append(append([]string(nil), lines...),
		`{"type":"message","id":"m12","timestamp":"2026-10-30T10:00:12.000Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"reply"}]}}`)
	writeLines(t, filepath.Join(homes.Pi, "sessions", "--tmp-display-pi--", "2026-10-30T00-00-00-000Z_"+session+".jsonl"), grown)
	poll2, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayNewer, AnchorLine: poll.NewerAnchor, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(poll2.Items) != 1 || poll2.Items[0].ID != "m12" {
		t.Fatalf("forward poll after growth = %+v", poll2.Items)
	}
	again, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayNewer, AnchorLine: poll2.NewerAnchor, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Items) != 0 {
		t.Fatalf("forward poll repeated an item it already returned: %+v", again.Items)
	}
}

// A filled forward page must hold the rest back without skipping them: the next
// poll from its own anchor returns exactly the items it did not.
func TestDisplayForwardPollHoldsBackTheRestWithoutSkippingThem(t *testing.T) {
	lines := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		lines = append(lines, fmt.Sprintf(
			`{"type":"message","id":"m%02d","timestamp":"2026-10-30T10:00:%02d.000Z","message":{"role":"user","content":[{"type":"text","text":"m%02d"}]}}`, i, i, i))
	}
	session, cwd, homes := piFixture(t, lines)
	first, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayNewer, AnchorLine: -1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "m00" || first.Items[1].ID != "m01" {
		t.Fatalf("first forward page = %+v", first.Items)
	}
	seen := []string{first.Items[0].ID, first.Items[1].ID}
	anchor := first.NewerAnchor
	for round := 0; round < 4; round++ {
		page, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayNewer, AnchorLine: anchor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			seen = append(seen, item.ID)
		}
		anchor = page.NewerAnchor
		if len(page.Items) == 0 {
			break
		}
	}
	if strings.Join(seen, ",") != "m00,m01,m02,m03,m04,m05" {
		t.Fatalf("forward paging saw %v", seen)
	}
}

// A partial final write (the file is being appended to as we read) must be
// re-read on the next poll, not skipped past.
func TestDisplayForwardAnchorNeverSkipsAPartialFinalLine(t *testing.T) {
	session, cwd, homes := piFixture(t, []string{
		`{"type":"message","id":"m0","timestamp":"2026-10-30T10:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"complete"}]}}`,
		`{"type":"message","id":"m1","timestamp":"2026-10-30T10:00:01.00`,
	})
	page, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayTail, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.UnparsedLines != 1 {
		t.Fatalf("page over a partial final line = %d items, %d unparsed", len(page.Items), page.UnparsedLines)
	}
	if page.NewerAnchor != 0 {
		t.Fatalf("forward anchor = %d, want 0 so the partial line is re-read", page.NewerAnchor)
	}
	writeLines(t, filepath.Join(homes.Pi, "sessions", "--tmp-display-pi--", "2026-10-30T00-00-00-000Z_"+session+".jsonl"), []string{
		`{"type":"message","id":"m0","timestamp":"2026-10-30T10:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"complete"}]}}`,
		`{"type":"message","id":"m1","timestamp":"2026-10-30T10:00:01.000Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"finished"}]}}`,
	})
	poll, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayNewer, AnchorLine: page.NewerAnchor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(poll.Items) != 1 || poll.Items[0].ID != "m1" || poll.Items[0].Text != "finished" {
		t.Fatalf("the completed line was lost: %+v", poll.Items)
	}
}

func TestDisplayBoundsEveryFieldAndFlagsTheTruncation(t *testing.T) {
	long := strings.Repeat("x", displayMaxText+500)
	longThink := strings.Repeat("t", displayMaxThinking+500)
	longOut := strings.Repeat("o", displayMaxToolOutput+500)
	session, cwd, homes := piFixture(t, []string{
		`{"type":"message","id":"big","timestamp":"2026-10-30T10:00:00.000Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"` + long + `"},{"type":"thinking","thinking":"` + longThink + `"}]}}`,
		`{"type":"message","id":"bigresult","timestamp":"2026-10-30T10:00:01.000Z","message":{"role":"toolResult","toolCallId":"c","toolName":"bash","isError":false,"content":[{"type":"text","text":"` + longOut + `"}]}}`,
	})
	page, err := ReadDisplayWindow(Pi, session, cwd, homes, DisplaySelector{Direction: DisplayTail, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	if len(item.Text) != displayMaxText || !item.TextTruncated {
		t.Fatalf("text bound = %d bytes, truncated %v", len(item.Text), item.TextTruncated)
	}
	if len(item.Thinking) != displayMaxThinking || !item.ThinkingTruncated {
		t.Fatalf("thinking bound = %d bytes, truncated %v", len(item.Thinking), item.ThinkingTruncated)
	}
	result := page.Items[1]
	if len(result.Tools[0].Output) != displayMaxToolOutput || !result.Tools[0].OutputTruncated {
		t.Fatalf("tool output bound = %d bytes, truncated %v", len(result.Tools[0].Output), result.Tools[0].OutputTruncated)
	}
}

// Truncation must not split a multi-byte rune: the bound is a byte budget, but
// the result stays valid UTF-8.
func TestDisplayTruncationKeepsValidUTF8(t *testing.T) {
	got, cut := truncateDisplay(strings.Repeat("é", displayMaxText), displayMaxText)
	if !cut {
		t.Fatal("an over-budget field was not truncated")
	}
	if len(got) > displayMaxText || !strings.HasSuffix(got, "é") {
		t.Fatalf("truncation split a rune: %d bytes, tail %q", len(got), got[max(0, len(got)-3):])
	}
}

func TestDisplayRefusesUnknownKindsAndBadSelectors(t *testing.T) {
	session, cwd, homes := piFixture(t, piDisplayLines)
	if _, err := DisplayFormat(Kind("shell")); err == nil {
		t.Fatal("a non-conversation kind must be a loud display error")
	}
	for _, sel := range []DisplaySelector{
		{Direction: DisplayTail, Limit: 0},
		{Direction: DisplayTail, Limit: DisplayMaxLimit + 1},
		{Direction: DisplayDirection("sideways"), Limit: 10},
		{Direction: DisplayOlder, AnchorLine: -1, Limit: 10},
		{Direction: DisplayNewer, AnchorLine: -2, Limit: 10},
	} {
		if _, err := ReadDisplayWindow(Pi, session, cwd, homes, sel); err == nil {
			t.Fatalf("selector %+v was accepted", sel)
		}
	}
	// A conversation with no file on this owner is a loud error, not an empty page.
	if _, err := ReadDisplayWindow(Pi, "99999999-9999-4999-8999-999999999999", cwd, homes,
		DisplaySelector{Direction: DisplayTail, Limit: 10}); err == nil {
		t.Fatal("a missing conversation file returned a page")
	}
}

// Regression, found by reading a real 70,844-line rollout: a codex
// `turn_context` record carries payload.summary as a STRING where a reasoning
// item's summary is a LIST. Decoding the detailed payload for every line marked
// 300 valid lines unreadable — and the same mismatch on the FINAL line would
// freeze a forward poll's anchor on it forever.
func TestDisplayCodexUnrelatedPayloadsAreSkippedNotUnreadable(t *testing.T) {
	home := t.TempDir()
	cwd := "/tmp/display-codex-shapes"
	session := "44444444-4444-4444-8444-444444444444"
	homes := ResolveHomes(home)
	path := filepath.Join(home, "sessions", "2026", "10", "30", "rollout-2026-10-30T10-00-00-"+session+".jsonl")
	writeLines(t, path, []string{
		`{"timestamp":"2026-10-30T10:00:00.000Z","type":"response_item","payload":{"type":"message","id":"m1","role":"user","content":[{"type":"input_text","text":"go"}]}}`,
		`{"timestamp":"2026-10-30T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"t1","summary":"auto","cwd":"/tmp"}}`,
		`{"timestamp":"2026-10-30T10:00:02.000Z","type":"world_state","payload":{"full":true,"state":{"collaboration_mode":{"mode":"default"}}}}`,
	})
	page, err := ReadDisplayWindow(Codex, session, cwd, homes, DisplaySelector{Direction: DisplayTail, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.UnparsedLines != 0 {
		t.Fatalf("valid non-conversation codex records were reported unreadable: unparsed=%d", page.UnparsedLines)
	}
	if page.SkippedRecords != 2 || len(page.Items) != 1 {
		t.Fatalf("codex shape page = %d items, %d skipped", len(page.Items), page.SkippedRecords)
	}
	// The anchor must reach the file's last line: a frozen anchor here is what
	// would wedge a live forward poll.
	if page.NewerAnchor != 2 {
		t.Fatalf("forward anchor = %d, want 2 (the final line was readable)", page.NewerAnchor)
	}
}
