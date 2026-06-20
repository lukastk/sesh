package conformance

// thread.model cells (Phase 1 of the model-selection feature): `thread new
// --model <m>` pins an opaque model on the thread and `send-headless --model`
// overrides it for one turn. The assertion is HONEST — it checks the model the
// agent ACTUALLY ran, not just that argv carried --model:
//
//   - pi / claude: spawn headless pinned to a DISTINCTIVE model, run a turn, and
//     assert that model appears in the agent's own transcript (pi records modelId
//     on its model_change + assistant lines; claude records model on each
//     assistant message). The per-turn override is proved by a second model that
//     can ONLY appear in the transcript if the override was honored (the thread's
//     stored model is the first one).
//   - codex (on a ChatGPT account) exposes no active-model id in its json/rollout
//     and supports only the default model, so a positive "ran model X" is not
//     observable. The honest observable is that the EXACT model string reaches
//     codex and codex ACTS on it: an unsupported model makes the turn fail LOUDLY
//     with that model name echoed back (which also proves `codex exec --model`
//     placement parsed — a misplaced flag would be an arg-parse error instead).
//
// LOCAL-only by design: model injection is locality-independent command-builder
// logic; `--machine` routing of `thread new`/`send-headless` is proven by
// route.parity. The command-builder placement itself is unit-tested in
// internal/agents (TestHeadedCommandModelPlacement / ...ResumeCommand...).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, a := range matrix.AllAgents {
		a := a
		matrix.RegisterTest("thread.model", a, matrix.Local,
			func(t *testing.T) { testThreadModel(t, string(a)) })
	}
}

func testThreadModel(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	switch agent {
	case "pi", "claude":
		// stored/override are two models whose transcript fingerprints are
		// DISJOINT, so each substring can only appear if that exact model ran.
		stored, storedMark := "anthropic/claude-haiku-4-5", "haiku-4-5"
		override, overrideMark := "anthropic/claude-3-5-haiku-latest", "3-5-haiku-latest"
		if agent == "claude" {
			stored, storedMark = "haiku", "haiku"
			override, overrideMark = "sonnet", "sonnet"
		}

		th := sb.newHeadlessThreadModel(t, agent, "model", stored)
		if th.Model != stored {
			t.Errorf("record model = %q, want %q", th.Model, stored)
		}

		// Turn 1 uses the thread's pinned model.
		sb.headlessTurn(t, th.ID, "Reply with exactly: ok")
		tr := sb.transcriptText(t, th.ID)
		if !strings.Contains(tr, storedMark) {
			t.Fatalf("[%s] pinned model %q did not run: transcript lacks %q", agent, stored, storedMark)
		}
		if strings.Contains(tr, overrideMark) {
			t.Fatalf("[%s] override model leaked before it was requested (test models not disjoint)", agent)
		}

		// Turn 2 overrides the model for THIS turn only. The override mark can only
		// appear if --model on send-headless was honored (the stored model differs).
		if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", th.ID,
			"--text", "Reply with exactly: ok", "--model", override); err != nil {
			t.Fatalf("send-headless --model: %v\n%s", err, stderr)
		}
		if !waitUntilReply(sb, t, th.ID) {
			t.Fatalf("[%s] override turn never completed", agent)
		}
		tr = sb.transcriptText(t, th.ID)
		if !strings.Contains(tr, overrideMark) {
			t.Fatalf("[%s] per-turn --model override %q was not honored: transcript lacks %q", agent, override, overrideMark)
		}
		// The thread's stored model is unchanged by a per-turn override.
		if got := sb.getThread(t, th.ID); got.Model != stored {
			t.Errorf("[%s] per-turn override changed the stored model to %q, want %q", agent, got.Model, stored)
		}

	case "codex":
		// An unsupported model reaches codex via `exec --model` and codex rejects it
		// LOUDLY, echoing the exact model string sesh injected — the honest proof the
		// flag is wired (codex exposes no active-model id to assert positively).
		const badModel = "gpt-5.5-codex"
		th := sb.newHeadlessThreadModel(t, "codex", "model", badModel)
		if th.Model != badModel {
			t.Errorf("record model = %q, want %q", th.Model, badModel)
		}
		if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", th.ID,
			"--text", "Reply with exactly: ok"); err != nil {
			t.Fatalf("send-headless (codex): %v\n%s", err, stderr)
		}
		if !waitUntilReply(sb, t, th.ID) {
			t.Fatalf("[codex] turn never completed")
		}
		r := sb.headlessReply(t, th.ID)
		if !strings.HasPrefix(r.Reply, "ERROR:") {
			t.Fatalf("[codex] turn with an unsupported model did not fail loudly: %q", r.Reply)
		}
		if !strings.Contains(r.Reply, badModel) {
			t.Errorf("[codex] failure does not echo the injected model %q (flag may not have reached codex): %q", badModel, r.Reply)
		}

		// And the per-turn send-headless --model path ALSO reaches codex: on a
		// default-model thread, overriding to the unsupported model makes the turn
		// fail with the model echoed (without the flag, the default would succeed).
		def := sb.newHeadlessThreadModel(t, "codex", "modeldef", "")
		if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", def.ID,
			"--text", "Reply with exactly: ok", "--model", badModel); err != nil {
			t.Fatalf("send-headless --model (codex): %v\n%s", err, stderr)
		}
		if !waitUntilReply(sb, t, def.ID) {
			t.Fatalf("[codex] override turn never completed")
		}
		if r := sb.headlessReply(t, def.ID); !strings.Contains(r.Reply, badModel) {
			t.Errorf("[codex] per-turn --model did not reach codex: %q", r.Reply)
		}
	}
}

// ---- helpers ----

func (sb *Sandbox) newHeadlessThreadModel(t *testing.T, agent, name, model string) api.Thread {
	t.Helper()
	args := []string{"thread", "new", "--agent", agent, "--name", name, "--cwd", t.TempDir(), "--headless", "--json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	stdout, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("thread new --headless --model %q (%s): %v\n%s", model, agent, err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(stdout), &th); err != nil {
		t.Fatalf("decode headless thread: %v\nraw: %s", err, stdout)
	}
	return th
}

func (sb *Sandbox) getThread(t *testing.T, id string) api.Thread {
	t.Helper()
	for _, th := range sb.listThreads(t) {
		if th.ID == id {
			return th
		}
	}
	t.Fatalf("thread %s not found in list", id)
	return api.Thread{}
}

// transcriptText returns the agent's transcript lines joined — the observable the
// model assertion reads (the agent records the model it actually ran).
func (sb *Sandbox) transcriptText(t *testing.T, id string) string {
	t.Helper()
	out, stderr, err := sb.Runner.Run(t, "thread", "transcript", "--id", id, "--json")
	if err != nil {
		t.Fatalf("transcript: %v\n%s", err, stderr)
	}
	var tr struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &tr); err != nil {
		t.Fatalf("decode transcript: %v\n%s", err, out)
	}
	return strings.Join(tr.Lines, "\n")
}

func waitUntilReply(sb *Sandbox, t *testing.T, id string) bool {
	return waitUntil(150*time.Second, func() bool {
		r := sb.headlessReply(t, id)
		return !r.Working && r.HaveReply
	})
}
