package conformance

// thread.revive-held-session (sesh#16): a claude conversation can be OWNED by a
// BACKGROUND SESSION — `claude --bg [--resume <id>]` at launch, or the `/background`
// slash command run inside a live session, which writes a `continued-in` record into
// the old transcript, registers the successor and exits the pane. sesh resolves a
// thread's session FORWARD
// through that chain (it must: the pre-handoff file is frozen), so the session a
// revive tries to resume is exactly the held one.
//
// WHAT CLAUDE ACTUALLY DOES THEN — measured, not inferred, and it is not one
// behaviour but two, which is the whole reason this cell exists:
//
//   - SAME-VERSION holder: an interactive `claude --resume` silently RE-EXECS ITSELF as
//     `claude attach <id>`. The revive appears to succeed and the pane is marked, but it
//     is a VIEW onto the background session, not a conversation that pane owns. True for
//     a settled holder (state "done") and a live one (state "blocked") alike.
//   - VERSION-MISMATCHED holder (the worker was started by an older claude build): the
//     resume REFUSES and exits 1 — "That session is running in the background (<id>) …
//     Add --fork-session to branch off a copy instead." THIS IS THE PRODUCTION CASE:
//     Lukas's two stuck holders were created by 2.1.277/278 workers while the reviving
//     claude was 2.1.286, and claude auto-updates near-daily, so any holder that outlives
//     a release becomes unreviveable. The symptom was the generic "agent exited
//     immediately after launch" with claude's real reason buried in captured pane text,
//     and the threads sat that way for 5 and 10 days.
//   - `--print --resume` refuses in EVERY case, version-matched or not.
//
// So sesh does NOT pre-refuse — claude decides, and for a version-matched holder its
// answer (attach) is a working outcome sesh must not break. What sesh adds, and what
// this cell pins:
//   (1) `doctor` reports every held thread with the holder's state and what a revive
//       would actually do, so the state is visible before someone meets it;
//   (2) a failed revive names the holder and every remedy in typeable form;
//   (3) --force stops the holder (`claude stop`, conversation KEPT) and does a REAL
//       resume, reported in the output — the explicit way out of both halves.
//
// Everything is real: a real claude in a real tmux pane against an ISOLATED claude
// config dir, a real turn so the conversation exists on disk, a real `claude --bg
// --resume` to put claude's OWN registry into each held state, and the real verbs. A
// BASELINE revive is asserted first, so nothing here can pass vacuously.
//
// Remote = the same over a real `--machine` ssh hop; the registry read, the refusal
// and the `claude stop` all happen on the OWNER, whose daemon carries the isolated
// CLAUDE_CONFIG_DIR.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.revive-held-session", matrix.Claude, loc,
			func(t *testing.T) { testReviveHeldSession(t, loc) })
	}
}

// claudeCmd runs a claude verb against the isolated config dir, from cwd.
func claudeCmd(t *testing.T, cfgDir, cwd string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("claude", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+cfgDir)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// bgSession is what claude's OWN registry says about a background session. The cell
// reads claude directly rather than sesh's view of it, so it is checking sesh against
// the real thing.
type bgSession struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// backgroundSessions maps full session uuid -> registry entry, background ones only.
func backgroundSessions(t *testing.T, cfgDir, cwd string) map[string]bgSession {
	t.Helper()
	out, err := claudeCmd(t, cfgDir, cwd, "agents", "--json")
	if err != nil {
		t.Fatalf("claude agents --json: %v\n%s", err, out)
	}
	var rows []struct {
		bgSession
		Kind      string `json:"kind"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("claude agents --json is not a JSON list (%v):\n%s", err, out)
	}
	held := map[string]bgSession{}
	for _, r := range rows {
		if r.Kind == "background" && r.SessionID != "" {
			held[r.SessionID] = r.bgSession
		}
	}
	return held
}

// holdLeaf puts claude's real registry into the held state for the thread's CURRENT
// leaf session and waits until the registry agrees, returning (leaf, short id).
// wantLive picks the prompt: a foreground `sleep` keeps the holder "blocked" for well
// over a minute (measured), a one-shot reply settles it to "done".
func (sb *Sandbox) holdLeaf(t *testing.T, cfgDir, cwd, threadID string) (string, string) {
	t.Helper()
	return sb.holdLeafWith(t, "claude", cfgDir, cwd, threadID)
}

// holdLeafWith is holdLeaf with an explicit claude binary, so a leg can hold the
// session with a DIFFERENT build than the one the revive will use.
func (sb *Sandbox) holdLeafWith(t *testing.T, bin, cfgDir, cwd, threadID string) (string, string) {
	t.Helper()
	leaf := sb.leafSession(t, threadID)
	cmd := exec.Command(bin, "--bg", "--resume", leaf, "Reply with exactly: HELD")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+cfgDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s --bg --resume %s: %v\n%s", bin, leaf, err, out)
	}
	var short string
	if !waitUntil(90*time.Second, func() bool {
		short = backgroundSessions(t, cfgDir, cwd)[leaf].ID
		return short != ""
	}) {
		t.Fatalf("precondition: claude never registered %s as a background session (registry: %+v)",
			leaf, backgroundSessions(t, cfgDir, cwd))
	}
	return leaf, short
}

// leafSession asks sesh which session a thread's conversation has drifted to — the
// SAME resolution revive uses, so the cell holds exactly the session revive will try
// to resume rather than guessing at the record's anchor.
func (sb *Sandbox) leafSession(t *testing.T, threadID string) string {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "transcript", "--id", threadID, "--tail", "1", "--json")
	if err != nil {
		t.Fatalf("thread transcript: %v\n%s", err, stderr)
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("thread transcript --json: %v\n%s", err, stdout)
	}
	if out.Path == "" {
		t.Fatalf("thread %s has no transcript on disk yet — the cell needs a real turn first", threadID)
	}
	return strings.TrimSuffix(filepath.Base(out.Path), ".jsonl")
}

// paneArgv is the command line of the process in the thread's marked pane. It is the
// observable that distinguishes a REAL resume from claude re-execing into an attach —
// the two are indistinguishable from the pane's rendered text.
func (sb *Sandbox) paneArgv(t *testing.T, threadID, when string) string {
	t.Helper()
	_, pid, ok := sb.markedPane(t, threadID)
	if !ok {
		t.Fatalf("%s: no marked pane for %s", when, threadID)
	}
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("%s: read argv of pane pid %d: %v", when, pid, err)
	}
	return strings.TrimSpace(string(out))
}

// stopAndWait kills the thread's runtime and waits for the process to go, so a
// following revive is a real revive and a dying claude cannot still be writing.
func (sb *Sandbox) stopAndWait(t *testing.T, threadID, when string) {
	t.Helper()
	_, pid, ok := sb.markedPane(t, threadID)
	if !ok {
		t.Fatalf("%s: no marked pane for %s before stop", when, threadID)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", threadID); err != nil {
		t.Fatalf("%s: stop: %v\n%s", when, err, stderr)
	}
	if !waitUntil(30*time.Second, func() bool { return !processAlive(pid) }) {
		t.Fatalf("%s: pid %d still alive 30s after thread stop", when, pid)
	}
}

// doctorText runs doctor and returns everything it printed (a non-zero exit is fine:
// warns are expected here).
func (sb *Sandbox) doctorText(t *testing.T) string {
	t.Helper()
	stdout, stderr, _ := sb.Runner.Run(t, "doctor")
	return stdout + stderr
}

func testReviveHeldSession(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	cfgDir := setupClaudeConfigDir(t)
	sb := newSandbox(t, loc, withSandboxEnv("CLAUDE_CONFIG_DIR", cfgDir))
	sb.startDaemon(t)

	cwd := t.TempDir()
	th := sb.newThread(t, "claude", "heldme", cwd)
	pane := sb.waitThreadReady(t, th.ID, "claude")
	// A real turn, so the conversation exists on disk — `claude --bg --resume` needs a
	// transcript, and so does sesh's leaf resolution.
	sb.answeredSend(t, th.ID, pane, "first turn")

	// BASELINE: this thread IS revivable, by a REAL resume. Everything below only means
	// something against this, and the argv check is what makes "real resume" a checked
	// claim rather than an assumption.
	sb.stopAndWait(t, th.ID, "baseline")
	if _, stderr, err := sb.Runner.Run(t, "thread", "headful", "--id", th.ID); err != nil {
		t.Fatalf("BASELINE revive failed before any hold existed — the cell cannot prove anything: %v\n%s", err, stderr)
	}
	sb.waitThreadReady(t, th.ID, "claude")
	if argv := sb.paneArgv(t, th.ID, "baseline"); !strings.Contains(argv, "--resume") || strings.Contains(argv, " attach ") {
		t.Fatalf("BASELINE pane should be a real `claude --resume`, got: %s", argv)
	}
	if d := sb.doctorText(t); strings.Contains(d, "claude background session:") {
		t.Fatalf("BASELINE: doctor already reports a held thread before anything was held:\n%s", d)
	}
	sb.stopAndWait(t, th.ID, "pre-hold")

	// ---- PHASE A: a SAME-VERSION holder. claude re-execs an interactive resume into
	// an attach, so the revive "works" while the pane is a view onto the holder. ----
	leaf, short := sb.holdLeaf(t, cfgDir, cwd, th.ID)

	// doctor must report it — naming the thread, the holder, its state and the remedy.
	// This is the half that makes the hold visible instead of discovered days later.
	doctor := sb.doctorText(t)
	for _, want := range []string{"claude background session", th.ID[:8], short, "--force", "attach"} {
		if !strings.Contains(doctor, want) {
			t.Errorf("same-version hold: doctor must report the held thread and name %q; got:\n%s", want, doctor)
		}
	}

	// The revive succeeds, but as an ATTACH — pinned on the argv, because the pane's
	// rendered text looks identical to a resume.
	if _, stderr, err := sb.Runner.Run(t, "thread", "headful", "--id", th.ID); err != nil {
		t.Fatalf("same-version hold: revive failed, but claude re-execs into attach for a settled holder: %v\n%s", err, stderr)
	}
	if argv := sb.paneArgv(t, th.ID, "same-version hold"); !strings.Contains(argv, "attach "+short) {
		t.Errorf("same-version hold: expected claude to re-exec as `claude attach %s` (the measured behaviour this cell pins), got: %s", short, argv)
	}
	sb.stopAndWait(t, th.ID, "same-version hold")

	// --force stops the holder and does a REAL resume. Observable effects, all three:
	// the reply names what it stopped, claude's registry no longer holds the session,
	// and the pane is a resume rather than an attach.
	stdout, stderr, err := sb.Runner.Run(t, "thread", "headful", "--id", th.ID, "--force")
	if err != nil {
		t.Fatalf("same-version hold: revive --force failed: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, short) {
		t.Errorf("--force must report the background session it stopped (%s); got:\n%s", short, stdout)
	}
	if e := backgroundSessions(t, cfgDir, cwd)[leaf]; e.ID != "" {
		t.Errorf("session %s is STILL registered as background session %s after --force", leaf, e.ID)
	}
	pane = sb.waitThreadReady(t, th.ID, "claude")
	if argv := sb.paneArgv(t, th.ID, "after --force"); !strings.Contains(argv, "--resume") || strings.Contains(argv, " attach ") {
		t.Errorf("after --force the pane must be a real `claude --resume`, not an attach; got: %s", argv)
	}
	// The conversation SURVIVED the stop (`claude stop` keeps it) — a further real turn.
	sb.answeredSend(t, th.ID, pane, "after forced revive")
	if d := sb.doctorText(t); strings.Contains(d, "claude background session:") {
		t.Errorf("doctor still reports a held thread after --force cleared it:\n%s", d)
	}
	sb.stopAndWait(t, th.ID, "post-force")

	// ---- WHAT THIS CELL DOES NOT COVER, said plainly rather than left to be assumed.
	//
	// The CROSS-BUILD refusal — the production failure, where the holder's worker was
	// started by an older claude build, `claude --resume` refuses outright and the thread
	// cannot be revived at all — is NOT reproduced here, and it is not for want of
	// trying. The holder's worker build is chosen by whichever claude owns claude's
	// DAEMON for that config dir, not by the binary that runs `--bg`; and that daemon
	// self-restarts onto whatever ~/.local/bin/claude currently is ("binary at … changed
	// — self-restarting for upgrade"), which is precisely how production got there. So a
	// cell cannot pin the holder to an old build without fighting claude's own upgrade
	// path, and a fixture that fought it would be testing the fight.
	//
	// That leg is covered instead by: TestHeldSessionRefusal (the message names the
	// holder, its state and every remedy), a measured reproduction outside the suite
	// (claude 2.1.284 holding, 2.1.286 reviving → exit 1 with claude's own refusal in the
	// pane), and the production incident itself. If claude ever makes the holder build
	// selectable, this is the cell to extend.
}
