package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("tmux.status-options", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testStatusOptions(t, loc) })
	}
}

// statusRowFormat is the CONTRACT a work-server status conf renders the thread
// row with (myrig's tmux.work.conf carries exactly this, inside its own
// colours): pure format lookups on the pane's status options — no `#()` job,
// so no shell is ever forked for a redraw. Inside a `#{?…}` branch a literal
// comma is spelled `#,`.
const statusRowFormat = "#{?@sesh-name,sesh: #{@sesh-name} [#{=8:@sesh-thread-id}] · #{@sesh-agent}" +
	"#{?@sesh-tags, · [#{@sesh-tags}],}" +
	"#{?@sesh-archived,  🗄 archived,}" +
	"#{?@sesh-flag-disabled,  #[fg=colour244]⌁ auto-flag off,}" +
	"#{?@sesh-flagged,  #[fg=red#,bold]⚑ FLAGGED,},}"

// testStatusOptions: a REAL pi thread in a real pane on the sandbox work
// server; the owning daemon must stamp the pane's status options, keep them
// current within a tick as the record is mutated through the real verbs
// (routed over the real ssh hop for the remote cell), render the status row
// through the contract format with tmux alone, and clear them when the pane
// is unstamped.
func testStatusOptions(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if strings.Contains(statusRowFormat, "#(") {
		t.Fatalf("the status row contract forks a job: %q", statusRowFormat)
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	const name = "sesh, status" // a comma: the value must survive the format's own separator
	th := sb.newThread(t, "pi", name, "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")

	opt := func(key string) string {
		out, err := sb.rawTmux(t, "show-options", "-p", "-t", pane, "-v", key)
		if err != nil {
			return "<error: " + strings.TrimSpace(out) + ">"
		}
		return strings.TrimSpace(out)
	}
	listing := func() string {
		out, _ := sb.rawTmux(t, "show-options", "-p", "-t", pane)
		return out
	}
	render := func() string {
		out, err := sb.rawTmux(t, "display-message", "-p", "-t", pane, statusRowFormat)
		if err != nil {
			t.Fatalf("display-message: %v\n%s", err, out)
		}
		return strings.TrimSpace(out)
	}
	run := func(args ...string) {
		t.Helper()
		if _, stderr, err := sb.Runner.Run(t, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr)
		}
	}

	// 1. Stamped by the owning daemon within a few ticks of the spawn.
	if !waitUntil(5*time.Second, func() bool { return opt("@sesh-name") == name }) {
		t.Fatalf("the daemon never stamped @sesh-name on the thread's pane; options:\n%s", listing())
	}
	for key, want := range map[string]string{"@sesh-agent": "pi", "@sesh-tags": "", "@sesh-archived": "", "@sesh-flagged": "", "@sesh-flag-disabled": ""} {
		if got := opt(key); got != want {
			t.Fatalf("%s = %q, want %q; options:\n%s", key, got, want, listing())
		}
		if !strings.Contains(listing(), key) {
			t.Fatalf("%s is not set on the pane (an empty option must still EXIST); options:\n%s", key, listing())
		}
	}

	// 2. The contract format renders the row from lookups alone.
	id8 := th.ID[:8]
	if got, want := render(), "sesh: "+name+" ["+id8+"] · pi"; got != want {
		t.Fatalf("rendered row = %q, want %q", got, want)
	}

	// 3. Every record mutation, through the real verbs, lands within a tick.
	run("thread", "rename", "--id", th.ID, "--name", "renamed")
	run("thread", "tag", "--id", th.ID, "--add", "urgent")
	run("thread", "flag", "--on", "--id", th.ID)
	run("thread", "archive", "--id", th.ID) // archived-but-headful stays live (H40)
	want := "sesh: renamed [" + id8 + "] · pi · [urgent]  🗄 archived  #[fg=red,bold]⚑ FLAGGED"
	if !waitUntil(5*time.Second, func() bool { return render() == want }) {
		t.Fatalf("after rename+tag+flag+archive the rendered row is %q, want %q; options:\n%s", render(), want, listing())
	}
	run("thread", "flag", "--disable", "--id", th.ID)
	if !waitUntil(5*time.Second, func() bool { return opt("@sesh-flag-disabled") == "1" }) {
		t.Fatalf("@sesh-flag-disabled never became 1; options:\n%s", listing())
	}
	if got := render(); !strings.Contains(got, "⌁ auto-flag off") {
		t.Fatalf("rendered row %q lacks the auto-flag-off marker", got)
	}

	// 4. Unstamping the pane (a runtime change with no record write) clears
	// the options on the still-running pane; the row renders empty.
	if out, err := sb.rawTmux(t, "set-option", "-p", "-t", pane, "-u", "@sesh-thread-id"); err != nil {
		t.Fatalf("unstamp: %v\n%s", err, out)
	}
	if !waitUntil(5*time.Second, func() bool { return !strings.Contains(listing(), "@sesh-name") }) {
		t.Fatalf("status options were not cleared after the pane lost its marker; options:\n%s", listing())
	}
	if got := render(); got != "" {
		t.Fatalf("rendered row after unstamp = %q, want empty", got)
	}
}
