package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tmux"
)

// thread.idle-stop (schema 66, _dev/IDLE_STOP.md): with [idle_stop] after set
// on the OWNING daemon, a real agent sitting idle at its prompt is stopped —
// its process really dies — while the record (and its flag) survives with the
// stop recorded on it; a sibling with `idle-stop set --never` is kept; `thread
// resume` revives the conversation and clears the record. The eligibility
// table (busy, background, wake-ups, attached input, shells) is unit-tested in
// internal/daemon/idlestop_test.go.
func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.idle-stop", a, loc,
				func(t *testing.T) { testIdleStop(t, string(a), loc) })
		}
	}
}

const idleStopLimit = 40 * time.Second

func testIdleStop(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte("[idle_stop]\nafter = \"40s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)

	victim := sb.newThread(t, agent, "idle-victim", "/tmp")
	keeper := sb.newThread(t, agent, "idle-keeper", "/tmp")
	sb.waitThreadReady(t, victim.ID, agent)
	sb.waitThreadReady(t, keeper.ID, agent)
	if _, stderr, err := sb.Runner.Run(t, "idle-stop", "set", "--never", "--id", keeper.ID); err != nil {
		t.Fatalf("idle-stop set --never: %v\n%s", err, stderr)
	}
	// One real turn first: a conversation that never took a turn has nothing to
	// resume (claude refuses "No conversation found"), whatever stopped it.
	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", victim.ID,
		"--text", "Reply with the single word OK, nothing else.", "--wait", "--timeout", "120s"); err != nil {
		t.Fatalf("send --wait: %v\n%s", err, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--on", "--id", victim.ID); err != nil {
		t.Fatalf("thread flag --on: %v\n%s", err, stderr)
	}
	victimPID := sb.resolvePane(t, victim.ID).Pane.PanePID
	agentProc, running := tmux.AgentUnderPane(victimPID)
	if !running {
		t.Fatalf("no %s process under the victim's pane (pid %d)", agent, victimPID)
	}
	start := time.Now()

	// The preview sees both, with the right verdict basis.
	stdout, stderr, err := sb.Runner.Run(t, "idle-stop", "preview", "--json")
	if err != nil {
		t.Fatalf("idle-stop preview: %v\n%s", err, stderr)
	}
	var preview api.IdleStopPreview
	if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
		t.Fatalf("preview json: %v\n%s", err, stdout)
	}
	if preview.After != idleStopLimit.String() {
		t.Errorf("preview after = %q, want %s", preview.After, idleStopLimit)
	}
	verdicts := map[string]api.IdleStopCandidate{}
	for _, c := range preview.Threads {
		verdicts[c.ID] = c
	}
	if c, ok := verdicts[keeper.ID]; !ok || c.Stop || !strings.Contains(c.Reason, "never") {
		t.Errorf("preview for the --never thread: %+v (present=%v), want keep because never", c, ok)
	}
	if _, ok := verdicts[victim.ID]; !ok {
		t.Errorf("preview omits the live victim thread: %+v", preview.Threads)
	}

	// (1) The victim is stopped: headless, and its agent process is gone.
	if !waitUntil(idleStopLimit+60*time.Second, func() bool {
		return sb.threadSnapshot(t, victim.ID).Head == api.Headless
	}) {
		s := sb.threadSnapshot(t, victim.ID)
		out, _, _ := sb.Runner.Run(t, "idle-stop", "preview")
		t.Fatalf("victim still %s·%s %s after the limit (last_active %ds ago)\npreview:\n%s",
			s.Head, s.Busy, time.Since(start).Round(time.Second), time.Now().Unix()-s.LastActiveUnix, out)
	}
	if took := time.Since(start); took < idleStopLimit-5*time.Second {
		t.Errorf("victim stopped after %s, before its %s limit", took.Round(time.Second), idleStopLimit)
	}
	if !waitUntil(10*time.Second, func() bool { return !processAlive(agentProc.PID) }) {
		t.Fatalf("victim reads headless but its %s process %d is still alive", agent, agentProc.PID)
	}
	rec := sb.threadRecord(t, victim.ID)
	if rec.IdleStoppedAtUnix == 0 || !strings.Contains(rec.IdleStoppedReason, "limit 40s") {
		t.Errorf("stop not recorded on the thread: at=%d reason=%q", rec.IdleStoppedAtUnix, rec.IdleStoppedReason)
	}
	if !rec.Flagged || rec.Archived {
		t.Errorf("idle stop changed the record: flagged=%v archived=%v, want flagged and not archived", rec.Flagged, rec.Archived)
	}

	// (2) The --never sibling is still live, past the limit.
	if s := sb.threadSnapshot(t, keeper.ID); s.Head != api.Headful {
		t.Errorf("the --never thread was stopped too (%s)", s.Head)
	}

	// (3) Resume brings it back and clears the record.
	if _, stderr, err := sb.Runner.Run(t, "thread", "resume", "--id", victim.ID); err != nil {
		t.Fatalf("thread resume: %v\n%s", err, stderr)
	}
	sb.waitThreadReady(t, victim.ID, agent)
	if rec := sb.threadRecord(t, victim.ID); rec.IdleStoppedAtUnix != 0 || rec.IdleStoppedReason != "" {
		t.Errorf("resume did not clear the idle-stop record: at=%d reason=%q", rec.IdleStoppedAtUnix, rec.IdleStoppedReason)
	}
}
