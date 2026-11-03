package daemon

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func TestDecideDelivery(t *testing.T) {
	const text = "Reply in French."
	h := store.ContextHashOf(text)
	old := store.ContextHashOf("older")
	base := deliveryInput{threadID: "t1", family: agents.Claude, session: "S", recorded: "S", text: text}
	type want struct {
		inject  string // "ctx", "cleared", ""
		system  bool
		record  bool
		skipped string // substring
	}
	cases := []struct {
		name string
		mod  func(*deliveryInput)
		want want
	}{
		// --- claude/codex: fresh histories get the current version ---
		{"startup gets it", func(in *deliveryInput) { in.event, in.source = api.ContextEventSessionStart, "startup" }, want{inject: "ctx", record: true}},
		{"compact gets it again even if delivered", func(in *deliveryInput) {
			in.event, in.source, in.deliveredSession, in.deliveredHash = api.ContextEventSessionStart, "compact", "S", h
		}, want{inject: "ctx"}},
		{"clear gets it, new session accepted", func(in *deliveryInput) {
			in.event, in.source, in.session = api.ContextEventSessionStart, "clear", "S2"
		}, want{inject: "ctx", record: true}},
		{"startup with no context records the session only", func(in *deliveryInput) {
			in.event, in.source, in.text = api.ContextEventSessionStart, "startup", ""
		}, want{record: true}},
		// --- resume / prompt: once per version per session ---
		{"prompt, never delivered", func(in *deliveryInput) { in.event = api.ContextEventPrompt }, want{inject: "ctx", record: true}},
		{"prompt, already delivered", func(in *deliveryInput) {
			in.event, in.deliveredSession, in.deliveredHash = api.ContextEventPrompt, "S", h
		}, want{skipped: "already delivered"}},
		{"resume, already delivered", func(in *deliveryInput) {
			in.event, in.source, in.deliveredSession, in.deliveredHash = api.ContextEventSessionStart, "resume", "S", h
		}, want{skipped: "already delivered"}},
		{"prompt, changed since", func(in *deliveryInput) {
			in.event, in.deliveredSession, in.deliveredHash = api.ContextEventPrompt, "S", old
		}, want{inject: "ctx", record: true}},
		{"prompt, removed since", func(in *deliveryInput) {
			in.event, in.deliveredSession, in.deliveredHash, in.text = api.ContextEventPrompt, "S", old, ""
		}, want{inject: "cleared", record: true}},
		{"prompt, no context ever: no write", func(in *deliveryInput) { in.event, in.text = api.ContextEventPrompt, "" }, want{}},
		{"prompt, no context, already told", func(in *deliveryInput) {
			in.event, in.text, in.deliveredSession, in.deliveredHash = api.ContextEventPrompt, "", "S", ""
		}, want{}},
		// --- who may receive it ---
		{"nested agent run is refused", func(in *deliveryInput) {
			in.event, in.session = api.ContextEventPrompt, "NESTED"
		}, want{skipped: "another agent run"}},
		{"codex before its first turn has no recorded session", func(in *deliveryInput) {
			in.family, in.event, in.source, in.recorded, in.session = agents.Codex, api.ContextEventSessionStart, "startup", "", "C1"
		}, want{inject: "ctx", record: true}},
		{"claude compaction leaf is the thread's", func(in *deliveryInput) {
			in.event, in.session = api.ContextEventPrompt, "LEAF"
			in.leafOf = func() string { return "LEAF" }
		}, want{inject: "ctx", record: true}},
		{"a session this thread already served", func(in *deliveryInput) {
			in.event, in.session, in.deliveredSession, in.deliveredHash = api.ContextEventPrompt, "S2", "S2", h
		}, want{skipped: "already delivered"}},
		// --- pi: system prompt every run, notice on change ---
		{"pi first run", func(in *deliveryInput) { in.family, in.event = agents.Pi, api.ContextEventTurn }, want{system: true, record: true}},
		{"pi steady state", func(in *deliveryInput) {
			in.family, in.event, in.deliveredSession, in.deliveredHash = agents.Pi, api.ContextEventTurn, "S", h
		}, want{system: true}},
		{"pi changed", func(in *deliveryInput) {
			in.family, in.event, in.deliveredSession, in.deliveredHash = agents.Pi, api.ContextEventTurn, "S", old
		}, want{system: true, inject: "ctx", record: true}},
		{"pi removed", func(in *deliveryInput) {
			in.family, in.event, in.deliveredSession, in.deliveredHash, in.text = agents.Pi, api.ContextEventTurn, "S", old, ""
		}, want{inject: "cleared", record: true}},
		{"pi nested run refused", func(in *deliveryInput) {
			in.family, in.event, in.session = agents.Pi, api.ContextEventTurn, "NESTED"
		}, want{skipped: "another agent run"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			c.mod(&in)
			got, err := decideDelivery(in)
			if err != nil {
				t.Fatal(err)
			}
			switch c.want.inject {
			case "ctx":
				if !strings.Contains(got.inject, text) || !strings.Contains(got.inject, `<sesh-thread-context thread="t1"`) {
					t.Errorf("inject = %q, want the wrapped context", got.inject)
				}
			case "cleared":
				if !strings.Contains(got.inject, `removed="true"`) {
					t.Errorf("inject = %q, want the removal notice", got.inject)
				}
			default:
				if got.inject != "" {
					t.Errorf("inject = %q, want none", got.inject)
				}
			}
			if c.want.system != (got.system != "") {
				t.Errorf("system = %q, want present=%v", got.system, c.want.system)
			}
			if got.system != "" && (!strings.Contains(got.system, text) || strings.Contains(got.system, "<sesh-thread-context")) {
				t.Errorf("pi system section = %q: want the body without sesh's outer tag", got.system)
			}
			if got.record != c.want.record {
				t.Errorf("record = %v, want %v", got.record, c.want.record)
			}
			if c.want.skipped == "" && got.skipped != "" || c.want.skipped != "" && !strings.Contains(got.skipped, c.want.skipped) {
				t.Errorf("skipped = %q, want %q", got.skipped, c.want.skipped)
			}
		})
	}
}

func TestDecideDeliveryRejectsMalformed(t *testing.T) {
	for _, in := range []deliveryInput{
		{family: agents.Claude, event: api.ContextEventPrompt},                // no session
		{family: agents.Claude, event: api.ContextEventTurn, session: "S"},    // pi's event from claude
		{family: agents.Pi, event: api.ContextEventPrompt, session: "S"},      // claude's event from pi
		{family: agents.Codex, event: "PostToolUse", session: "S", text: "x"}, // unknown
	} {
		if _, err := decideDelivery(in); err == nil {
			t.Errorf("%+v: want an error", in)
		}
	}
}

func TestRenderedContextFitsClaudeInlineLimit(t *testing.T) {
	text := strings.Repeat("é", api.MaxThreadContextBytes/2) // 8000 bytes, 4000 UTF-16 units
	ascii := strings.Repeat("x", api.MaxThreadContextBytes)
	for _, s := range []string{text, ascii} {
		r := renderContext("00000000-0000-0000-0000-000000000000", store.ContextHashOf(s), s)
		// Claude Code inlines hook context of at most 10,000 characters (UTF-16 units).
		units := 0
		for _, c := range r {
			if c >= 0x10000 {
				units += 2
			} else {
				units++
			}
		}
		if units > 10000 {
			t.Fatalf("rendered context is %d UTF-16 units; claude would spill it to a file", units)
		}
	}
}

func TestValidateContextText(t *testing.T) {
	if err := validateContextText(strings.Repeat("x", api.MaxThreadContextBytes)); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	for _, bad := range []string{strings.Repeat("x", api.MaxThreadContextBytes+1), "a\x00b", "\xff"} {
		if err := validateContextText(bad); err == nil {
			t.Errorf("%q: want refused", bad[:min(len(bad), 10)])
		}
	}
}
