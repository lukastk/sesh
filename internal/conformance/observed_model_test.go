package conformance

// thread.observed-model cells (schema 58): the OWNING daemon reads the model a
// thread's transcript says it last ran on and carries it as observed_model on
// the thread's status, its grid row (what the TUI renders) and `info`. It is
// derived from the harness's own records, never from the thread record: a
// headless turn with a one-turn --model override (which leaves the registered
// model alone) moves observed_model and not Thread.Model. Before any turn there
// is nothing observed. Remote = the same reads routed with --machine.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.observed-model", a, loc,
				func(t *testing.T) { testObservedModel(t, string(a), loc) })
		}
	}
}

// observedAs reports whether an observed model id is what asking for `asked`
// runs, in the harness's spelling: claude records the resolved id for an alias
// (haiku -> claude-haiku-…), codex and pi record exactly what they were given.
func observedAs(agent, asked, observed string) bool {
	if agent == "claude" && !strings.Contains(asked, "-") {
		return strings.HasPrefix(observed, "claude-"+asked+"-")
	}
	return observed == asked
}

// headlessTurnArgs is headlessTurn with extra send-headless flags.
func (sb *Sandbox) headlessTurnArgs(t *testing.T, id, text string, extra ...string) {
	t.Helper()
	args := append([]string{"thread", "send-headless", "--id", id, "--text", text}, extra...)
	if _, stderr, err := sb.Runner.Run(t, args...); err != nil {
		t.Fatalf("send-headless: %v\n%s", err, stderr)
	}
	if !waitUntil(150*time.Second, func() bool { return !sb.headlessReply(t, id).Working }) {
		t.Fatalf("headless turn never completed")
	}
	if r := sb.headlessReply(t, id); !r.HaveReply || strings.HasPrefix(r.Reply, "ERROR:") {
		t.Fatalf("the turn produced no valid reply: %+v", r)
	}
}

// waitObserved waits for every owner-side view of the thread to report an
// observed model matching want, failing with what each view said.
func waitObserved(t *testing.T, sb *Sandbox, agent, id, want string) {
	t.Helper()
	var status, grid, info string
	ok := waitUntil(30*time.Second, func() bool {
		status = sb.threadStatus(t, id).ObservedModel
		grid = gridRow(t, sb, false, id).ObservedModel
		out, _, err := sb.Runner.Run(t, "info", "--json", id) // flags before the positional id
		if err != nil {
			return false
		}
		var v struct {
			ObservedModel string `json:"observed_model"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(out)), &v) != nil {
			return false
		}
		info = v.ObservedModel
		return observedAs(agent, want, status) && observedAs(agent, want, grid) && observedAs(agent, want, info)
	})
	if !ok {
		t.Fatalf("observed model never became %s's %q: status=%q grid=%q info=%q", agent, want, status, grid, info)
	}
}

func testObservedModel(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	registered, override := testModels[agent][0], testModels[agent][1]
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	out, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", agent, "--name", "observed", "--cwd", "/tmp", "--headless", "--model", registered, "--json")
	if err != nil {
		t.Fatalf("thread new: %v\n%s", err, stderr)
	}
	var th struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatal(err)
	}
	// No turn yet: nothing is observed, even though a model is registered.
	time.Sleep(4 * time.Second) // past one observer sweep
	if got := sb.threadStatus(t, th.ID).ObservedModel; got != "" {
		t.Fatalf("a turnless thread observed %q", got)
	}

	sb.headlessTurnArgs(t, th.ID, "Reply with exactly the word OK and nothing else.")
	waitObserved(t, sb, agent, th.ID, registered)

	// A one-turn override runs another model without touching the record: the
	// observed model follows the transcript, the registered one stays.
	sb.headlessTurnArgs(t, th.ID, "Reply with exactly the word OK and nothing else.", "--model", override)
	waitObserved(t, sb, agent, th.ID, override)
	if got := gridRow(t, sb, false, th.ID).Model; got != registered {
		t.Errorf("the override changed the registered model to %q (want %q)", got, registered)
	}
}
