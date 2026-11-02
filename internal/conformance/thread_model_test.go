package conformance

// thread.set-model and thread.switch-model cells (schema 58).
//
// set-model changes only the REGISTERED model: a live agent keeps running its
// launch model, and the next resume launches on the new one. --clear removes it.
//
// switch-model also switches the LIVE agent, session-only, per harness — and the
// evidence is the agent's own: claude's pane is restarted IN PLACE (same pane
// id, new process carrying --model, the conversation remembered across the
// restart, and the transcript's next turn on the new model); codex is switched
// by `/model` (same process, codex's confirmation, the next turn's turn_context
// on the new model); pi is switched over pi-rpc-socket (same process, the
// session reports the new model and writes model_change at once). A thread that
// is mid-turn is refused without --force; a thread with no live agent only has
// its record changed. Remote = the same commands routed with --machine.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tmux"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.set-model", a, loc,
				func(t *testing.T) { testThreadSetModel(t, string(a), loc) })
			matrix.RegisterTest("thread.switch-model", a, loc,
				func(t *testing.T) { testThreadSwitchModel(t, string(a), loc) })
		}
	}
}

func setModel(t *testing.T, sb *Sandbox, verb string, args ...string) (api.SetThreadModelResponse, string, error) {
	t.Helper()
	full := append([]string{"thread", verb, "--json"}, args...)
	out, stderr, err := sb.Runner.Run(t, full...)
	if err != nil {
		return api.SetThreadModelResponse{}, stderr, err
	}
	var resp api.SetThreadModelResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("decode %s: %v\n%s", verb, err, out)
	}
	return resp, stderr, nil
}

func newThreadOnModel(t *testing.T, sb *Sandbox, agent, name, model string, headless bool) api.Thread {
	t.Helper()
	args := []string{"thread", "new", "--agent", agent, "--name", name, "--cwd", "/tmp", "--model", model, "--json"}
	if headless {
		args = append(args, "--headless")
	}
	out, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("thread new: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatal(err)
	}
	return th
}

func transcriptOf(t *testing.T, sb *Sandbox, id string) api.TranscriptResponse {
	t.Helper()
	out, _, err := sb.Runner.Run(t, "thread", "transcript", "--id", id, "--json")
	if err != nil {
		return api.TranscriptResponse{} // no transcript yet
	}
	var tr api.TranscriptResponse
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &tr) != nil {
		return api.TranscriptResponse{}
	}
	return tr
}

// headfulTurn sends text into the live pane and waits for the agent's reply to
// land in its transcript, returning it.
func headfulTurn(t *testing.T, sb *Sandbox, id, text string) string {
	t.Helper()
	before := transcriptOf(t, sb, id).ReplyCount
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", id, "--text", text, "--on-typing", "skip"); err != nil {
		t.Fatalf("thread send: %v\n%s", err, stderr)
	}
	var tr api.TranscriptResponse
	if !waitUntil(150*time.Second, func() bool {
		tr = transcriptOf(t, sb, id)
		return tr.ReplyCount > before && sb.threadStatus(t, id).Busy == api.BusyIdle
	}) {
		pane, _, _ := sb.markedPane(t, id)
		capture, _ := sb.rawTmux(t, "capture-pane", "-p", "-t", pane)
		t.Fatalf("the turn never completed (replies %d -> %d, transcript %q)\n--- pane ---\n%s", before, tr.ReplyCount, tr.Path, capture)
	}
	return tr.LastReply
}

// liveAgentPID is the pid of the agent process under the thread's marked pane.
func liveAgentPID(t *testing.T, sb *Sandbox, id string) (pane string, pid int) {
	t.Helper()
	pane, panePID, ok := sb.markedPane(t, id)
	if !ok {
		t.Fatalf("no marked pane for %s", id)
	}
	agent, running := tmux.AgentUnderPane(panePID)
	if !running {
		t.Fatalf("no agent process under %s's pane", id)
	}
	return pane, agent.PID
}

func testThreadSetModel(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	a, b := testModels[agent][0], testModels[agent][1]
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := newThreadOnModel(t, sb, agent, "pinned", a, false)
	sb.waitThreadReady(t, th.ID, agent)
	_, pidBefore := liveAgentPID(t, sb, th.ID)

	resp, stderr, err := setModel(t, sb, "set-model", "--id", th.ID, "--model", b)
	if err != nil {
		t.Fatalf("set-model: %v\n%s", err, stderr)
	}
	if resp.Outcome != api.ModelRecorded || resp.Thread.Model != b {
		t.Fatalf("set-model answered %+v", resp)
	}
	// The live agent is untouched: same process, still on its launch model.
	if _, pid := liveAgentPID(t, sb, th.ID); pid != pidBefore {
		t.Errorf("set-model restarted the agent (pid %d -> %d)", pidBefore, pid)
	}
	assertLiveLaunchModel(t, sb, th, agent, a)

	// The next launch uses it: stop, resume, and the new process runs on b.
	// claude and codex can only resume a conversation that exists, so they take a
	// turn first (it runs on a: the live agent was never switched).
	if agent != "pi" {
		headfulTurn(t, sb, th.ID, "Reply with exactly the word OK and nothing else.")
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", th.ID); err != nil {
		t.Fatalf("stop: %v\n%s", err, stderr)
	}
	if !waitUntil(15*time.Second, func() bool { return sb.threadStatus(t, th.ID).Head == api.Headless }) {
		t.Fatalf("thread never stopped")
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "resume", "--id", th.ID); err != nil {
		t.Fatalf("resume: %v\n%s", err, stderr)
	}
	sb.waitThreadReady(t, th.ID, agent)
	th.Model = b
	assertLiveLaunchModel(t, sb, th, agent, b)

	// --clear removes it; set-model refuses neither-or-both.
	if resp, stderr, err := setModel(t, sb, "set-model", "--id", th.ID, "--clear"); err != nil || resp.Thread.Model != "" {
		t.Fatalf("set-model --clear: %+v %v\n%s", resp, err, stderr)
	}
	if _, _, err := setModel(t, sb, "set-model", "--id", th.ID); err == nil {
		t.Errorf("set-model with neither --model nor --clear was accepted")
	}
	// A thread that runs no agent refuses.
	out, stderr, err := sb.Runner.Run(t, "thread", "new", "--virtual", "--name", "group", "--json")
	if err != nil {
		t.Fatalf("virtual: %v\n%s", err, stderr)
	}
	var v api.Thread
	json.Unmarshal([]byte(strings.TrimSpace(out)), &v) //nolint:errcheck
	if _, stderr, err := setModel(t, sb, "set-model", "--id", v.ID, "--model", a); err == nil || !strings.Contains(stderr, "runs no agent model") {
		t.Errorf("set-model on a virtual thread: err=%v stderr=%s", err, stderr)
	}
}

func testThreadSwitchModel(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	a, b := testModels[agent][0], testModels[agent][1]
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	// No live agent: only the record changes.
	hl := newThreadOnModel(t, sb, agent, "headless", a, true)
	if resp, stderr, err := setModel(t, sb, "switch-model", "--id", hl.ID, "--model", b); err != nil || resp.Outcome != api.ModelNextTurn || resp.Thread.Model != b {
		t.Fatalf("switch-model on a headless thread: %+v %v\n%s", resp, err, stderr)
	}
	// --id is never inferred.
	if _, stderr, err := sb.Runner.Run(t, "thread", "switch-model", "--model", b); err == nil || !strings.Contains(stderr, "--id") {
		t.Errorf("switch-model without --id: err=%v stderr=%s", err, stderr)
	}

	th := newThreadOnModel(t, sb, agent, "live", a, false)
	sb.waitThreadReady(t, th.ID, agent)
	const word = "PERIWINKLE"
	headfulTurn(t, sb, th.ID, "Remember the word "+word+". Reply with exactly OK and nothing else.")

	// Mid-turn: refused without --force, record untouched.
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--on-typing", "skip", "--text",
		"Write a 200-word explanation of how TCP congestion control works."); err != nil {
		t.Fatalf("send: %v\n%s", err, stderr)
	}
	if !waitUntil(45*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyBusy }) {
		t.Fatalf("the long turn never showed busy")
	}
	if _, stderr, err := setModel(t, sb, "switch-model", "--id", th.ID, "--model", b); err == nil || !strings.Contains(stderr, "mid-turn") {
		t.Errorf("switch-model on a busy thread: err=%v stderr=%s", err, stderr)
	}
	if got := threadByName(t, sb, "live").Model; got != a {
		t.Errorf("a refused switch changed the record to %q", got)
	}
	if !waitUntil(150*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyIdle }) {
		t.Fatalf("the long turn never finished")
	}
	time.Sleep(2 * time.Second) // let the pane settle so the typing/busy probes read quiet

	paneBefore, pidBefore := liveAgentPID(t, sb, th.ID)
	resp, stderr, err := setModel(t, sb, "switch-model", "--id", th.ID, "--model", b)
	if err != nil {
		t.Fatalf("switch-model: %v\n%s", err, stderr)
	}
	if resp.Thread.Model != b {
		t.Errorf("switch-model left the record on %q", resp.Thread.Model)
	}
	paneAfter, pidAfter := liveAgentPID(t, sb, th.ID)
	if paneAfter != paneBefore {
		t.Errorf("the thread moved panes (%s -> %s)", paneBefore, paneAfter)
	}
	th.Model = b
	switch agent {
	case "claude":
		if resp.Outcome != api.ModelRestarted {
			t.Fatalf("claude outcome %q, want %q (%s)", resp.Outcome, api.ModelRestarted, resp.Detail)
		}
		if pidAfter == pidBefore {
			t.Errorf("claude was not restarted (same pid %d)", pidAfter)
		}
		sb.waitThreadReady(t, th.ID, agent)
		assertLiveLaunchModel(t, sb, th, agent, b)
		// The conversation survived the restart, and the next turn ran on b.
		if reply := headfulTurn(t, sb, th.ID, "What word did I ask you to remember? Reply with just the word."); !strings.Contains(strings.ToUpper(reply), word) {
			t.Errorf("the resumed claude forgot the conversation: %q", reply)
		}
		waitObserved(t, sb, agent, th.ID, b)
	case "codex":
		if resp.Outcome != api.ModelSwitched || !strings.Contains(resp.Detail, "Model changed to "+b) {
			t.Fatalf("codex outcome %q / %q", resp.Outcome, resp.Detail)
		}
		if pidAfter != pidBefore {
			t.Errorf("codex was restarted (pid %d -> %d); /model should switch in place", pidBefore, pidAfter)
		}
		headfulTurn(t, sb, th.ID, "Reply with exactly OK and nothing else.")
		waitObserved(t, sb, agent, th.ID, b)
	case "pi":
		if resp.Outcome != api.ModelSwitched {
			t.Fatalf("pi outcome %q (%s)", resp.Outcome, resp.Detail)
		}
		if pidAfter != pidBefore {
			t.Errorf("pi was restarted (pid %d -> %d); set_model should switch in place", pidBefore, pidAfter)
		}
		if got := piLiveModel(t, th); got != b {
			t.Errorf("the live pi session runs %q after the switch, want %q", got, b)
		}
		waitObserved(t, sb, agent, th.ID, b) // pi writes model_change at the switch itself
		if _, stderr, err := setModel(t, sb, "switch-model", "--id", th.ID, "--model", a+":high"); err == nil || !strings.Contains(stderr, "thinking level") {
			t.Errorf("a :thinking suffix was not refused for a live switch: err=%v stderr=%s", err, stderr)
		}
	}
}
