package conformance

// The schedule.* cells (_dev/SCHEDULING.md §13). Honest means: the real clock
// (10 s intervals, never a mocked time), real agents in real panes, the real
// ssh hop for remote, and the OBSERVABLE effect asserted — the text in the
// pane, the turn that started, the thread that got archived — never the
// engine's own counters. `run-now` is what makes the guard and lifecycle cells
// fast: it fires the exact run the clock would, on demand.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("schedule.crud", matrix.AgentAgnostic, loc, func(t *testing.T) { testScheduleCRUD(t, loc) })
		matrix.RegisterTest("schedule.guards", matrix.AgentAgnostic, loc, func(t *testing.T) { testScheduleGuards(t, loc) })
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("schedule.message", a, loc, func(t *testing.T) { testScheduleMessage(t, string(a), loc) })
			matrix.RegisterTest("schedule.spawn", a, loc, func(t *testing.T) { testScheduleSpawn(t, string(a), loc) })
		}
	}
	matrix.RegisterTest("schedule.catchup", matrix.AgentAgnostic, matrix.Local, testScheduleCatchup)
	matrix.RegisterTest("schedule.lifecycle", matrix.AgentAgnostic, matrix.Local, testScheduleLifecycle)
}

// scheduleJSON runs a schedule verb with --json and decodes the record it prints.
func (sb *Sandbox) scheduleJSON(t *testing.T, args ...string) api.Schedule {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, append(append([]string{"schedule"}, args...), "--json")...)
	if err != nil {
		t.Fatalf("schedule %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	var sc api.Schedule
	if err := json.Unmarshal([]byte(stdout), &sc); err != nil {
		t.Fatalf("decode schedule json: %v\nraw: %s", err, stdout)
	}
	return sc
}

// scheduleGet reads a schedule back from the OWNER's daemon.
func (sb *Sandbox) scheduleGet(t *testing.T, ref string) api.Schedule {
	t.Helper()
	stdout, stderr, err := sb.daemonRunner.Run(t, "schedule", "show", "--id", ref, "--json")
	if err != nil {
		t.Fatalf("schedule show: %v\n%s", err, stderr)
	}
	var sc api.Schedule
	if err := json.Unmarshal([]byte(stdout), &sc); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, stdout)
	}
	return sc
}

// scheduleRunNow fires a schedule and returns the daemon's outcome line.
func (sb *Sandbox) scheduleRunNow(t *testing.T, ref string, force bool) (api.ScheduleRunNowResponse, error) {
	t.Helper()
	args := []string{"schedule", "run-now", "--id", ref, "--json"}
	if force {
		args = append(args, "--force")
	}
	stdout, stderr, err := sb.Runner.Run(t, args...)
	var out api.ScheduleRunNowResponse
	if stdout != "" {
		json.Unmarshal([]byte(stdout), &out) //nolint:errcheck
	}
	if err != nil && out.Outcome == "" {
		return out, fmt.Errorf("%v\n%s", err, stderr)
	}
	return out, nil
}

func (sb *Sandbox) scheduleRuns(t *testing.T, ref string) []api.ScheduleRun {
	t.Helper()
	stdout, stderr, err := sb.daemonRunner.Run(t, "schedule", "runs", "--id", ref, "--limit", "0", "--json")
	if err != nil {
		t.Fatalf("schedule runs: %v\n%s", err, stderr)
	}
	var out api.ScheduleRunsResponse
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode runs: %v\nraw: %s", err, stdout)
	}
	return out.Runs
}

// meshSnap reads a thread's row from the OWNER's mesh view (the maintainer's
// published snapshot — what the scheduler's guards read). ok=false when absent.
func (sb *Sandbox) meshSnap(t *testing.T, id string) (api.ThreadSnapshot, bool) {
	t.Helper()
	stdout, _, err := sb.daemonRunner.Run(t, "mesh", "--json")
	if err != nil {
		return api.ThreadSnapshot{}, false
	}
	var mesh api.MeshSnapshot
	if err := json.Unmarshal([]byte(stdout), &mesh); err != nil {
		return api.ThreadSnapshot{}, false
	}
	for _, m := range mesh.Machines {
		for _, th := range m.Threads {
			if th.ID == id {
				return th, true
			}
		}
	}
	return api.ThreadSnapshot{}, false
}

func (sb *Sandbox) paneText(t *testing.T, pane string) string {
	out, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
	return out
}

// --- crud ---

func testScheduleCRUD(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "crud-target")

	sc := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--text", "hello", "--name", "hourly", "--if", "idle,detached", "--when-busy", "skip")
	if sc.ID == "" || sc.Machine != sb.Machine || sc.Spec != "every 1h0m0s" || sc.TZ == "" || sc.Catchup != "skip" || !sc.Enabled {
		t.Fatalf("created record: %+v", sc)
	}
	now := time.Now().Unix()
	if sc.NextFireUnix < now+3500 || sc.NextFireUnix > now+3700 {
		t.Fatalf("first fire not ~1h out: next=%d now=%d", sc.NextFireUnix, now)
	}
	if len(sc.Rules.If) != 2 || sc.Rules.WhenBusy != "skip" || sc.ThreadID != th.ID {
		t.Fatalf("rules/target lost: %+v", sc)
	}
	// Loud refusals at creation: an unknown guard word, a never-firing cron, a duplicate name.
	for _, bad := range [][]string{
		{"message", "--id", th.ID, "--every", "1h", "--text", "x", "--if", "sleepy"},
		{"message", "--id", th.ID, "--cron", "0 0 31 2 *", "--text", "x"},
		{"message", "--id", th.ID, "--every", "1h", "--text", "x", "--name", "hourly"},
		{"message", "--id", th.ID, "--every", "2s", "--text", "x"},
		{"message", "--id", th.ID, "--text", "x"},
	} {
		if _, stderr, err := sb.Runner.Run(t, append([]string{"schedule"}, bad...)...); err == nil {
			t.Fatalf("schedule %v must be refused; it succeeded", bad)
		} else if strings.TrimSpace(stderr) == "" {
			t.Fatalf("schedule %v refused silently", bad)
		}
	}
	// list / show by name and by prefix.
	stdout, _, err := sb.Runner.Run(t, "schedule", "list", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed api.SchedulesResponse
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil || len(listed.Schedules) != 1 || listed.Schedules[0].ID != sc.ID {
		t.Fatalf("list: %v %s", err, stdout)
	}
	if got := sb.scheduleGet(t, "hourly"); got.ID != sc.ID {
		t.Fatalf("show by name resolved %s", got.ID)
	}
	if got := sb.scheduleGet(t, sc.ID[:8]); got.ID != sc.ID {
		t.Fatalf("show by prefix resolved %s", got.ID)
	}
	// edit the clock: spec + next_fire change.
	edited := sb.scheduleJSON(t, "edit", "--id", "hourly", "--every", "2h", "--text", "hello again")
	if edited.Spec != "every 2h0m0s" || edited.NextFireUnix < now+7100 || edited.Text != "hello again" {
		t.Fatalf("edit: %+v", edited)
	}
	// pause / resume.
	if _, stderr, err := sb.Runner.Run(t, "schedule", "pause", "--id", "hourly"); err != nil {
		t.Fatalf("pause: %v\n%s", err, stderr)
	}
	if got := sb.scheduleGet(t, "hourly"); got.Enabled {
		t.Fatalf("still enabled after pause")
	}
	if _, stderr, err := sb.Runner.Run(t, "schedule", "resume", "--id", "hourly"); err != nil {
		t.Fatalf("resume: %v\n%s", err, stderr)
	}
	if got := sb.scheduleGet(t, "hourly"); !got.Enabled || got.NextFireUnix < time.Now().Unix()+7100 {
		t.Fatalf("resume must re-enable and recompute next_fire from now: %+v", got)
	}
	// remove.
	if _, stderr, err := sb.Runner.Run(t, "schedule", "remove", "--id", "hourly"); err != nil {
		t.Fatalf("remove: %v\n%s", err, stderr)
	}
	if _, _, err := sb.Runner.Run(t, "schedule", "show", "--id", "hourly"); err == nil {
		t.Fatalf("show after remove must fail")
	}
	if _, _, err := sb.Runner.Run(t, "schedule", "remove", "--id", "hourly"); err == nil {
		t.Fatalf("removing twice must be loud")
	}
}

// --- message ---

func testScheduleMessage(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newThread(t, agent, "sched-msg", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, agent)
	nonce := fmt.Sprintf("SCHEDMSG%d", time.Now().UnixNano()%100000)

	// 1. A real interval into a live pane: within one period the text lands
	//    and the agent starts a turn on it.
	sc := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "10s", "--name", "tick",
		"--text", "Reply with exactly the word "+nonce+" and nothing else.")
	if !waitUntil(30*time.Second, func() bool { return strings.Contains(sb.paneText(t, pane), nonce) }) {
		t.Fatalf("scheduled message never reached the pane within a period:\n%s", sb.paneText(t, pane))
	}
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyBusy }) {
		t.Fatalf("agent never started working on the scheduled message")
	}
	if !waitUntil(15*time.Second, func() bool {
		for _, r := range sb.scheduleRuns(t, sc.ID) {
			if strings.HasPrefix(r.Outcome, "fired") {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no fired run recorded: %+v", sb.scheduleRuns(t, sc.ID))
	}
	if _, stderr, err := sb.Runner.Run(t, "schedule", "pause", "--id", sc.ID); err != nil {
		t.Fatalf("pause: %v\n%s", err, stderr)
	}
	waitUntil(180*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyIdle })

	// 2. Stop the thread: a headless target under --when-headless turn gets a
	//    REAL headless turn (the daemon's own send-headless; codex needs its
	//    stamped session id, minted by the first turn above).
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", th.ID); err != nil {
		t.Fatalf("stop: %v\n%s", err, stderr)
	}
	if !waitUntil(15*time.Second, func() bool { return sb.threadStatus(t, th.ID).Head == api.Headless }) {
		t.Fatalf("thread never read headless after stop")
	}
	nonce2 := nonce + "B"
	turn := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "turn", "--when-headless", "turn",
		"--text", "Reply with exactly the word "+nonce2+" and nothing else.")
	out, err := sb.scheduleRunNow(t, turn.ID, false)
	if err != nil || out.Outcome != api.OutcomeFired || !strings.Contains(out.Detail, "headless turn") {
		t.Fatalf("run-now (headless turn): %+v %v", out, err)
	}
	if !waitUntil(180*time.Second, func() bool {
		stdout, _, err := sb.Runner.Run(t, "thread", "headless-reply", "--id", th.ID, "--json")
		return err == nil && strings.Contains(stdout, nonce2)
	}) {
		t.Fatalf("the headless turn's reply never carried the sentinel")
	}

	// (The headless turn's completion reaches the maintainer's snapshot a tick
	// later; the guards read that snapshot, so wait for idle before judging.)
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyIdle }) {
		t.Fatalf("thread never idle after the headless turn")
	}
	// 3. --when-headless skip: a headless target is skipped, recorded.
	skip := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "skip", "--when-headless", "skip", "--text", "never")
	if out, err := sb.scheduleRunNow(t, skip.ID, false); err != nil || out.Outcome != api.OutcomeSkipped || out.Detail != "headless" {
		t.Fatalf("run-now (skip): %+v %v", out, err)
	}

	// 4. --when-headless revive: the daemon resumes the conversation into a
	//    pane and pastes; the agent is back in a real pane with the text.
	nonce3 := nonce + "C"
	revive := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "revive", "--when-headless", "revive",
		"--text", "Reply with exactly the word "+nonce3+" and nothing else.")
	out, err = sb.scheduleRunNow(t, revive.ID, false)
	if err != nil || out.Outcome != api.OutcomeFired || !strings.Contains(out.Detail, "revived") {
		t.Fatalf("run-now (revive): %+v %v", out, err)
	}
	newPane, pid, ok := sb.markedPane(t, th.ID)
	if !ok || !agentRunningUnder(pid, agent) {
		t.Fatalf("revive left no live %s pane bearing the marker", agent)
	}
	if !waitUntil(10*time.Second, func() bool { return strings.Contains(sb.paneText(t, newPane), nonce3) }) {
		t.Fatalf("revived pane never showed the pasted text:\n%s", sb.paneText(t, newPane))
	}
	if sb.scheduleGet(t, revive.ID).FireCount != 1 {
		t.Fatalf("revive run not counted")
	}
}

// --- guards ---

func testScheduleGuards(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "guarded", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")
	nonce := fmt.Sprintf("GUARD%d", time.Now().UnixNano()%100000)
	sc := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "nudge", "--text", nonce)

	// A REAL busy thread: the default --when-busy skip records 'busy' and
	// delivers nothing.
	sb.sendKeys(t, pane, "Write a detailed 400-word explanation of how DNS resolution works, step by step.")
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyBusy }) {
		t.Fatalf("thread never became busy")
	}
	out, err := sb.scheduleRunNow(t, sc.ID, false)
	if err != nil || out.Outcome != api.OutcomeSkipped || out.Detail != "busy" {
		t.Fatalf("busy target: %+v %v", out, err)
	}
	if strings.Contains(sb.paneText(t, pane), nonce) {
		t.Fatalf("a skipped run pasted anyway")
	}
	waitUntil(180*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyIdle })

	// --if idle implies a dwell: right after the turn the target is idle but
	// not for 60s — skipped with the dwell named; --idle-for 0 fires.
	dwell := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "dwell", "--if", "idle", "--text", nonce+"DWELL")
	out, err = sb.scheduleRunNow(t, dwell.ID, false)
	if err != nil || out.Outcome != api.OutcomeSkipped || !strings.HasPrefix(out.Detail, "idle for only") {
		t.Fatalf("dwell guard: %+v %v", out, err)
	}
	nodwell := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "nodwell", "--if", "idle", "--idle-for", "0", "--text", nonce+"NODWELL")
	out, err = sb.scheduleRunNow(t, nodwell.ID, false)
	if err != nil || out.Outcome != api.OutcomeFired {
		t.Fatalf("--idle-for 0: %+v %v", out, err)
	}
	if !waitUntil(10*time.Second, func() bool { return strings.Contains(sb.paneText(t, pane), nonce+"NODWELL") }) {
		t.Fatalf("fired run never reached the pane")
	}
	waitUntil(180*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyIdle })

	// A held thread is skipped; --ignore-hold fires; run-now --force fires regardless.
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", th.ID, "--until-unix", fmt.Sprint(time.Now().Add(time.Hour).Unix())); err != nil {
		t.Fatalf("hold: %v\n%s", err, stderr)
	}
	// The guard reads the maintainer's SNAPSHOT (the mesh view), which derives
	// on_hold a tick after the record write; wait on that, not on the grid.
	if !waitUntil(10*time.Second, func() bool { snap, ok := sb.meshSnap(t, th.ID); return ok && snap.OnHold }) {
		t.Fatalf("hold never reached the snapshot")
	}
	held := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "held", "--text", nonce+"HELD")
	if out, err := sb.scheduleRunNow(t, held.ID, false); err != nil || out.Outcome != api.OutcomeSkipped || out.Detail != "on hold" {
		t.Fatalf("held target: %+v %v", out, err)
	}
	ignore := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "ignore-hold", "--ignore-hold", "--text", nonce+"IGNORE")
	if out, err := sb.scheduleRunNow(t, ignore.ID, false); err != nil || out.Outcome != api.OutcomeFired {
		t.Fatalf("--ignore-hold: %+v %v", out, err)
	}
	if out, err := sb.scheduleRunNow(t, held.ID, true); err != nil || out.Outcome != api.OutcomeFired {
		t.Fatalf("run-now --force on a held target: %+v %v", out, err)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", th.ID, "--clear"); err != nil {
		t.Fatalf("hold --clear: %v\n%s", err, stderr)
	}
	waitUntil(180*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyIdle })

	// Archived: skipped unless --allow-archived.
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", th.ID); err != nil {
		t.Fatalf("archive: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool { return sb.threadRecord(t, th.ID).Archived }) {
		t.Fatalf("archive never landed on the record")
	}
	if out, err := sb.scheduleRunNow(t, held.ID, false); err != nil || out.Outcome != api.OutcomeSkipped || out.Detail != "archived" {
		t.Fatalf("archived target: %+v %v", out, err)
	}
	allow := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "allow-archived", "--allow-archived", "--text", nonce+"ARCH")
	if out, err := sb.scheduleRunNow(t, allow.ID, false); err != nil || out.Outcome != api.OutcomeFired {
		t.Fatalf("--allow-archived: %+v %v", out, err)
	}
	// Every decision is on the record.
	if got := sb.scheduleGet(t, held.ID); got.SkipCount != 2 || got.FireCount != 1 {
		t.Fatalf("held schedule counters: %+v", got)
	}
}

// --- spawn ---

func testScheduleSpawn(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	nonce := fmt.Sprintf("ORCHID%d", time.Now().UnixNano()%100000)

	// 1. A real interval spawns a headless run; the run row names the thread,
	//    which exists, is auto-flag-disabled, and answers the prompt.
	sc := sb.scheduleJSON(t, "spawn", "--agent", agent, "--cwd", "/tmp", "--every", "10s", "--name", "seed",
		"--prompt", "Reply with exactly the word "+nonce+" and nothing else.")
	if !sc.Headless || sc.SpawnMode == "" {
		t.Fatalf("spawn defaults: %+v", sc)
	}
	var runThread string
	if !waitUntil(30*time.Second, func() bool {
		for _, r := range sb.scheduleRuns(t, sc.ID) {
			if r.ThreadID != "" {
				runThread = r.ThreadID
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no run with a thread within a period: %+v", sb.scheduleRuns(t, sc.ID))
	}
	if _, stderr, err := sb.Runner.Run(t, "schedule", "pause", "--id", sc.ID); err != nil {
		t.Fatalf("pause: %v\n%s", err, stderr)
	}
	rec := sb.threadRecord(t, runThread)
	if rec.AgentKind != agent || !strings.HasPrefix(rec.Name, "seed-") || !rec.FlagDisabled {
		t.Fatalf("run thread: %+v", rec)
	}
	if !waitUntil(180*time.Second, func() bool {
		stdout, _, err := sb.Runner.Run(t, "thread", "headless-reply", "--id", runThread, "--json")
		return err == nil && strings.Contains(stdout, nonce)
	}) {
		t.Fatalf("the run's headless turn never answered with the sentinel")
	}
	// keep (the default): the finished run is still an ordinary, un-archived record.
	if !waitUntil(15*time.Second, func() bool {
		for _, r := range sb.scheduleRuns(t, sc.ID) {
			if r.ThreadID == runThread && r.EndedAtUnix > 0 {
				return strings.Contains(r.Detail, "keep")
			}
		}
		return false
	}) {
		t.Fatalf("run never ended with the keep policy: %+v", sb.scheduleRuns(t, sc.ID))
	}
	if rec := sb.threadRecord(t, runThread); rec.Archived {
		t.Fatalf("keep must not archive the run thread")
	}

	// 2. --on-turn-end stop+archive: after the first turn the run's thread is archived.
	eph := sb.scheduleJSON(t, "spawn", "--agent", agent, "--cwd", "/tmp", "--every", "1h", "--name", "ephemeral", "--on-turn-end", "stop+archive",
		"--prompt", "Reply with exactly the word "+nonce+"EPH and nothing else.")
	out, err := sb.scheduleRunNow(t, eph.ID, false)
	if err != nil || out.Outcome != api.OutcomeFired || out.ThreadID == "" {
		t.Fatalf("run-now (ephemeral): %+v %v", out, err)
	}
	if !waitUntil(180*time.Second, func() bool { return sb.threadRecord(t, out.ThreadID).Archived }) {
		t.Fatalf("stop+archive never archived the run thread after its turn")
	}

	// 3. --if-previous skip: while a run is still going, the next is skipped.
	long := sb.scheduleJSON(t, "spawn", "--agent", agent, "--cwd", "/tmp", "--every", "1h", "--name", "long",
		"--prompt", "Write a detailed 500-word essay about the history of the tmux terminal multiplexer.")
	first, err := sb.scheduleRunNow(t, long.ID, false)
	if err != nil || first.Outcome != api.OutcomeFired {
		t.Fatalf("first long run: %+v %v", first, err)
	}
	second, err := sb.scheduleRunNow(t, long.ID, false)
	if err != nil || second.Outcome != api.OutcomeSkipped || !strings.HasPrefix(second.Detail, "previous run still going") {
		t.Fatalf("overlap guard: %+v %v", second, err)
	}

	// 4. --headed: a run into a REAL pane with the prompt pasted, then stop at
	//    turn end leaves no pane.
	headed := sb.scheduleJSON(t, "spawn", "--agent", agent, "--cwd", "/tmp", "--every", "1h", "--name", "headed", "--headed", "--on-turn-end", "stop",
		"--prompt", "Reply with exactly the word "+nonce+"HEAD and nothing else.")
	out, err = sb.scheduleRunNow(t, headed.ID, false)
	if err != nil || out.Outcome != api.OutcomeFired || out.ThreadID == "" {
		t.Fatalf("run-now (headed): %+v %v", out, err)
	}
	pane, pid, ok := sb.markedPane(t, out.ThreadID)
	if !ok || !agentRunningUnder(pid, agent) {
		t.Fatalf("headed run has no live %s pane", agent)
	}
	if !strings.Contains(sb.paneText(t, pane), nonce+"HEAD") {
		t.Fatalf("headed run's prompt not in its pane:\n%s", sb.paneText(t, pane))
	}
	if !waitUntil(180*time.Second, func() bool { _, _, ok := sb.markedPane(t, out.ThreadID); return !ok }) {
		t.Fatalf("stop at turn end never removed the headed run's pane")
	}
}

// --- catch-up ---

func testScheduleCatchup(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "catchup-target")
	// Cheap runs: a headless target under --when-headless skip records a skip
	// per fire, so a replay would be visible as extra runs.
	skip := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "10s", "--name", "skipper", "--when-headless", "skip", "--text", "x")
	once := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "10s", "--name", "oncer", "--when-headless", "skip", "--catchup", "once", "--text", "x")
	// Let each fire at least once so the baseline is proven, then stop the daemon.
	if !waitUntil(30*time.Second, func() bool { return len(sb.scheduleRuns(t, skip.ID)) >= 1 && len(sb.scheduleRuns(t, once.ID)) >= 1 }) {
		t.Fatalf("baseline fires never happened")
	}
	skipRunsBefore := len(sb.scheduleRuns(t, skip.ID))
	onceRunsBefore := len(sb.scheduleRuns(t, once.ID))
	if _, stderr, err := sb.daemonRunner.Run(t, "daemon", "stop"); err != nil {
		t.Fatalf("daemon stop: %v\n%s", err, stderr)
	}
	// Sleep through several occurrences plus the miss grace (90s): the next
	// due instant is then unmistakably MISSED, not merely late.
	time.Sleep(125 * time.Second)
	sb.startDaemon(t)
	time.Sleep(3 * time.Second)
	got := sb.scheduleGet(t, skip.ID)
	if got.Missed < 2 || got.NextFireUnix <= time.Now().Unix()-1 {
		t.Fatalf("catchup=skip after a daemon outage: %+v", got)
	}
	// No replay: at most one NEW run (a real period may have elapsed since the
	// restart), never the ~12 slept through.
	if runs := sb.scheduleRuns(t, skip.ID); len(runs) > skipRunsBefore+1 {
		t.Fatalf("catchup=skip replayed missed occurrences: %d runs (%d before the outage)", len(runs), skipRunsBefore)
	}
	o := sb.scheduleGet(t, once.ID)
	if o.Missed < 2 || o.LastFireUnix < time.Now().Unix()-10 {
		t.Fatalf("catchup=once must fire exactly one catch-up run on start: %+v", o)
	}
	if runs := sb.scheduleRuns(t, once.ID); len(runs) < onceRunsBefore+1 || len(runs) > onceRunsBefore+2 {
		t.Fatalf("catchup=once: %d runs (%d before the outage; want exactly one catch-up, maybe one more period)", len(runs), onceRunsBefore)
	}
}

// --- lifecycle ---

func testScheduleLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	// The breaker at 3 for a fast proof.
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte("[schedules]\ndisable_after_failures = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "life-target")

	// max_fires: one delivered run, then disabled with the reason.
	one := sb.scheduleJSON(t, "message", "--id", th.ID, "--every", "1h", "--name", "one", "--max-fires", "1", "--when-headless", "skip", "--text", "x")
	if out, err := sb.scheduleRunNow(t, one.ID, false); err != nil || out.Outcome != api.OutcomeSkipped {
		t.Fatalf("a skip must not count toward max_fires: %+v %v", out, err)
	}
	if got := sb.scheduleGet(t, one.ID); !got.Enabled {
		t.Fatalf("disabled by a skip: %+v", got)
	}
	if out, err := sb.scheduleRunNow(t, one.ID, true); err != nil || out.Outcome != api.OutcomeFired {
		t.Fatalf("forced fire: %+v %v", out, err)
	}
	if got := sb.scheduleGet(t, one.ID); got.Enabled || !strings.Contains(got.DisabledReason, "max_fires") {
		t.Fatalf("max_fires did not disable: %+v", got)
	}

	// A one-shot fires once and disables itself.
	at := time.Now().Add(15 * time.Second).Format("2006-01-02 15:04:05")
	shot := sb.scheduleJSON(t, "message", "--id", th.ID, "--at", at, "--name", "shot", "--when-headless", "skip", "--text", "x")
	if !waitUntil(90*time.Second, func() bool {
		got := sb.scheduleGet(t, shot.ID)
		return !got.Enabled && got.DisabledReason == "one-shot fired"
	}) {
		t.Fatalf("one-shot: %+v", sb.scheduleGet(t, shot.ID))
	}

	// The breaker: a codex thread that never had a turn has no session id, so
	// REVIVING it is codex's justified N/A refusal every time — three failing
	// runs disable the schedule, naming the reason.
	cx := sb.newHeadlessThread(t, "codex", "no-session")
	broken := sb.scheduleJSON(t, "message", "--id", cx.ID, "--every", "1h", "--name", "broken", "--when-headless", "revive", "--text", "x")
	for i := 1; i <= 3; i++ {
		out, _ := sb.scheduleRunNow(t, broken.ID, false)
		if out.Outcome != api.OutcomeFailed {
			t.Fatalf("run %d: %+v", i, out)
		}
	}
	if got := sb.scheduleGet(t, broken.ID); got.Enabled || !strings.Contains(got.DisabledReason, "3 consecutive failures") || got.FailStreak != 3 {
		t.Fatalf("breaker: %+v", got)
	}

	// Deleting the target removes its schedules in the same transaction, and
	// the delete says so.
	before, _, _ := sb.Runner.Run(t, "schedule", "list", "--json")
	if !strings.Contains(before, one.ID) || !strings.Contains(before, shot.ID) {
		t.Fatalf("schedules missing before delete: %s", before)
	}
	stdout, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", th.ID)
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "also removed 2 message schedule(s)") {
		t.Fatalf("delete did not report the cascade: %q", stdout)
	}
	after, _, _ := sb.Runner.Run(t, "schedule", "list", "--json")
	if strings.Contains(after, one.ID) || strings.Contains(after, shot.ID) || !strings.Contains(after, broken.ID) {
		t.Fatalf("cascade wrong: %s", after)
	}
}
