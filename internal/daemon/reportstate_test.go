package daemon

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/agents/claude"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// TestReportStateValidation pins the report-state contract: loud on missing
// fields / unknown events / unknown threads / non-agent nodes / stale seq, and
// correct authority application for the valid transitions (turn_started ⇒
// reported busy, turn_ended ⇒ reported idle, release ⇒ authority withdrawn,
// idempotent when already absent).
func TestReportStateValidation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	d := &Daemon{store: st}

	if err := st.InsertThread(api.Thread{ID: "tid-1", Machine: "test", SessionName: "s1", AgentKind: "pi"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	if err := st.InsertThread(api.Thread{ID: "tid-v", Machine: "test", SessionName: "virtual-v", AgentKind: api.VirtualAgentKind}); err != nil {
		t.Fatalf("InsertThread virtual: %v", err)
	}

	report := func(id, source, event string, seq int64) (int, error) {
		t.Helper()
		return d.reportState(api.ReportStateRequest{ThreadID: id, Source: source, Event: event, Seq: seq}, 1000)
	}

	// Loud refusals, each with the right status.
	for _, tc := range []struct {
		name       string
		id, src    string
		event      string
		seq        int64
		wantStatus int
		wantSubstr string
	}{
		{"missing thread_id", "", "sesh:test", api.ReportTurnStarted, 1, http.StatusBadRequest, "thread_id is required"},
		{"missing source", "tid-1", "", api.ReportTurnStarted, 1, http.StatusBadRequest, "source is required"},
		{"unknown event", "tid-1", "sesh:test", "working", 1, http.StatusBadRequest, "unknown event"},
		{"unknown thread", "nope", "sesh:test", api.ReportTurnStarted, 1, http.StatusNotFound, ""},
		{"non-agent node", "tid-v", "sesh:test", api.ReportTurnStarted, 1, http.StatusConflict, "runs no agent"},
	} {
		status, err := report(tc.id, tc.src, tc.event, tc.seq)
		if err == nil {
			t.Fatalf("%s: want error, got none", tc.name)
		}
		if status != tc.wantStatus {
			t.Fatalf("%s: status = %d, want %d (err: %v)", tc.name, status, tc.wantStatus, err)
		}
		if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
			t.Fatalf("%s: error %q missing %q", tc.name, err, tc.wantSubstr)
		}
	}
	if _, ok := d.reportedBusy("tid-1"); ok {
		t.Fatal("refused reports must not create authority")
	}

	// turn_started ⇒ reported busy.
	if _, err := report("tid-1", "sesh:pi-ext", api.ReportTurnStarted, 10); err != nil {
		t.Fatalf("turn_started: %v", err)
	}
	if busy, ok := d.reportedBusy("tid-1"); !ok || !busy {
		t.Fatalf("after turn_started: busy=%v ok=%v, want true/true", busy, ok)
	}

	// A stale (and an equal) seq is refused loudly and leaves state untouched —
	// a late-racing turn_ended must never be reordered before the turn after it.
	for _, seq := range []int64{9, 10} {
		status, err := report("tid-1", "sesh:pi-ext", api.ReportTurnEnded, seq)
		if err == nil || status != http.StatusConflict || !strings.Contains(err.Error(), "stale seq") {
			t.Fatalf("seq %d: want 409 stale-seq, got status=%d err=%v", seq, status, err)
		}
	}
	if busy, ok := d.reportedBusy("tid-1"); !ok || !busy {
		t.Fatal("stale report must not change authority")
	}

	// turn_ended ⇒ reported idle (authority PRESENT, value idle — distinct from
	// no authority, which would mean heuristic).
	if _, err := report("tid-1", "sesh:pi-ext", api.ReportTurnEnded, 11); err != nil {
		t.Fatalf("turn_ended: %v", err)
	}
	if busy, ok := d.reportedBusy("tid-1"); !ok || busy {
		t.Fatalf("after turn_ended: busy=%v ok=%v, want false/true", busy, ok)
	}

	// release withdraws authority; releasing again (absent) is idempotent-ok.
	if _, err := report("tid-1", "sesh:pi-ext", api.ReportRelease, 12); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok := d.reportedBusy("tid-1"); ok {
		t.Fatal("release must withdraw authority")
	}
	if _, err := report("tid-1", "sesh:pi-ext", api.ReportRelease, 13); err != nil {
		t.Fatalf("re-release (absent): %v", err)
	}
}

// TestReportStateBlocked pins the blocked overlay's transitions: blocked always
// implies busy (mid-turn by definition, even with no prior entry), carries its
// reason, unblocked resumes the turn, and BOTH turn boundaries clear it.
func TestReportStateBlocked(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	d := &Daemon{store: st}
	if err := st.InsertThread(api.Thread{ID: "tid-b", Machine: "test", SessionName: "sb", AgentKind: "claude"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	report := func(event, reason string, seq int64) {
		t.Helper()
		if _, err := d.reportState(api.ReportStateRequest{ThreadID: "tid-b", Source: "sesh:claude-hook", Event: event, Seq: seq, Reason: reason}, 1000); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	assertState := func(wantBusy, wantBlocked bool, wantReason string) {
		t.Helper()
		a, ok := d.reportedState("tid-b")
		if !ok {
			t.Fatal("no authority entry")
		}
		if a.busy != wantBusy || a.blocked != wantBlocked || a.blockedReason != wantReason {
			t.Fatalf("state busy=%v blocked=%v reason=%q, want %v/%v/%q", a.busy, a.blocked, a.blockedReason, wantBusy, wantBlocked, wantReason)
		}
	}

	// blocked with NO prior entry (daemon restarted mid-turn): busy implied.
	report(api.ReportBlocked, "needs permission to use Bash", 1)
	assertState(true, true, "needs permission to use Bash")

	// unblocked resumes the turn: busy stays, blocked clears.
	report(api.ReportUnblocked, "", 2)
	assertState(true, false, "")

	// blocked again, then turn_ended clears the overlay with the turn.
	report(api.ReportBlocked, "question", 3)
	assertState(true, true, "question")
	report(api.ReportTurnEnded, "", 4)
	assertState(false, false, "")

	// blocked, then a NEW turn (turn_started) is never still blocked.
	report(api.ReportBlocked, "again", 5)
	report(api.ReportTurnStarted, "", 6)
	assertState(true, false, "")
}

// TestAuthorityClearedByLiveness proves the pane-liveness bound with a REAL
// maintainer tick against a real (empty, isolated) tmux server: a thread whose
// reporter claimed busy but whose pane does not exist resolves headless·idle
// and its authority entry is CLEARED — a reporter that died with its agent can
// never pin busy. StateAuthority stays unset on the headless row (the field
// labels only the headful busy source).
func TestAuthorityClearedByLiveness(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	sock := "seshauth-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	if _, err := exec.Command("tmux", "-L", sock, "-f", "/dev/null", "new-session", "-d", "-s", "idle", "-x", "80", "-y", "20").CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	d := &Daemon{store: st, tmux: tmux.NewServer(sock)}
	m := newMaintainer(d)
	d.maint = m

	if err := st.InsertThread(api.Thread{ID: "tid-a", Machine: "test", SessionName: "gone", AgentKind: "pi"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	if _, err := d.reportState(api.ReportStateRequest{ThreadID: "tid-a", Source: "sesh:pi-ext", Event: api.ReportTurnStarted, Seq: 1}, 1000); err != nil {
		t.Fatalf("reportState: %v", err)
	}
	if busy, ok := d.reportedBusy("tid-a"); !ok || !busy {
		t.Fatal("precondition: reported busy authority in place")
	}

	m.tick()

	if _, ok := d.reportedBusy("tid-a"); ok {
		t.Fatal("tick over a pane-less thread must CLEAR its authority (liveness bound)")
	}
	snap, ok := m.stateOf("tid-a")
	if !ok {
		t.Fatal("maintainer published no snapshot")
	}
	if snap.Head != api.Headless || snap.Busy != api.BusyIdle {
		t.Fatalf("pane-less thread = %s·%s, want headless·idle (reported busy must not leak)", snap.Head, snap.Busy)
	}
	if snap.StateAuthority != "" {
		t.Fatalf("headless row carries state_authority %q, want unset", snap.StateAuthority)
	}
}

// TestReportStateStampsAgentSession pins the schema-46 session-id capture
// (ticket 49d4299b): a report carrying agent_session_id stamps the thread
// record (the authoritative capture for codex's late-minted id — including on
// turn_ended_no_authority, the codex notify event, which otherwise returns
// early); a matching id is a no-op; a DIFFERING stored id is corrected (the
// live pane's reporter is the thread's conversation — this self-heals a past
// mis-discovery); reports without the field never touch the stored id.
func TestReportStateStampsAgentSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	d := &Daemon{store: st}
	if err := st.InsertThread(api.Thread{ID: "tid-cx", Machine: "test", SessionName: "s", AgentKind: "codex"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	storedID := func() string {
		t.Helper()
		th, err := st.GetThread("tid-cx")
		if err != nil {
			t.Fatalf("GetThread: %v", err)
		}
		return th.AgentSessionID
	}

	// The codex path: turn_ended_no_authority + agent_session_id stamps.
	if _, err := d.reportState(api.ReportStateRequest{
		ThreadID: "tid-cx", Source: "sesh:codex-notify", Event: api.ReportTurnEndedNoAuthority,
		Seq: 1, AgentSessionID: "codex-session-1",
	}, 1000); err != nil {
		t.Fatalf("stamp report: %v", err)
	}
	if got := storedID(); got != "codex-session-1" {
		t.Fatalf("stored id = %q, want codex-session-1", got)
	}

	// Same id again: no-op, no error.
	if _, err := d.reportState(api.ReportStateRequest{
		ThreadID: "tid-cx", Source: "sesh:codex-notify", Event: api.ReportTurnEndedNoAuthority,
		Seq: 2, AgentSessionID: "codex-session-1",
	}, 1001); err != nil {
		t.Fatalf("idempotent report: %v", err)
	}

	// A report WITHOUT the field leaves the stored id alone.
	if _, err := d.reportState(api.ReportStateRequest{
		ThreadID: "tid-cx", Source: "sesh:codex-notify", Event: api.ReportTurnEndedNoAuthority, Seq: 3,
	}, 1002); err != nil {
		t.Fatalf("field-less report: %v", err)
	}
	if got := storedID(); got != "codex-session-1" {
		t.Fatalf("field-less report changed the stored id to %q", got)
	}

	// A differing id corrects the record (e.g. a stale mis-discovered stamp).
	if _, err := d.reportState(api.ReportStateRequest{
		ThreadID: "tid-cx", Source: "sesh:codex-notify", Event: api.ReportTurnEndedNoAuthority,
		Seq: 4, AgentSessionID: "codex-session-2",
	}, 1003); err != nil {
		t.Fatalf("correcting report: %v", err)
	}
	if got := storedID(); got != "codex-session-2" {
		t.Fatalf("stored id = %q, want the corrected codex-session-2", got)
	}
}

// TestReportStateRefusesForeignSessionStamp reproduces the 2026-08-05
// corruption in miniature and pins the backstop.
//
// A claude BACKGROUND agent runs under claude's machine-global daemon, not the
// thread's pane, and inherits SESH_THREAD_ID from whatever process started that
// daemon — so it reports under a FOREIGN thread id. Live, that wrote thread
// bd2d0b3c's conversation onto thread 86304b66's record: 86304b66 was left
// pointing at a stranger's transcript and bd2d0b3c at one frozen hours earlier,
// which is why it could no longer be resumed.
//
// The stamp must be refused on the evidence that the reported session's
// transcript lives under a different cwd — and ONLY the stamp: the lifecycle
// event still applies, so a thread whose recorded cwd drifts from its agent's
// real cwd loses auto-correction, never its busy/idle tracking.
func TestReportStateRefusesForeignSessionStamp(t *testing.T) {
	claudeHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	const (
		ownCwd     = "/home/u/dev/soogun"
		foreignCwd = "/home/u/dev/ituc"
	)
	writeTranscript := func(cwd, sessionID string) {
		t.Helper()
		dir := claude.ProjectDir(claudeHome, cwd)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTranscript(ownCwd, "sess-own")
	writeTranscript(ownCwd, "sess-own-compacted")
	writeTranscript(foreignCwd, "sess-foreign")

	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	d := &Daemon{store: st}
	if err := st.InsertThread(api.Thread{
		ID: "tid-own", Machine: "test", SessionName: "s", AgentKind: "claude",
		Cwd: ownCwd, AgentSessionID: "sess-own",
	}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	storedID := func() string {
		t.Helper()
		th, err := st.GetThread("tid-own")
		if err != nil {
			t.Fatalf("GetThread: %v", err)
		}
		return th.AgentSessionID
	}
	busy := func() bool {
		t.Helper()
		d.authMu.Lock()
		defer d.authMu.Unlock()
		if a := d.auth["tid-own"]; a != nil {
			return a.busy
		}
		return false
	}

	// The background agent reports: turn_started carrying ITS OWN session id.
	if _, err := d.reportState(api.ReportStateRequest{
		ThreadID: "tid-own", Source: "sesh:claude-hook", Event: api.ReportTurnStarted,
		Seq: 1, AgentSessionID: "sess-foreign",
	}, 1000); err != nil {
		t.Fatalf("foreign-session report errored: %v", err)
	}
	if got := storedID(); got != "sess-own" {
		t.Fatalf("stored id = %q — a foreign conversation's session id overwrote the record (the 2026-08-05 bug)", got)
	}
	// ...but the lifecycle half still landed: we refuse the claim of identity,
	// not the report.
	if !busy() {
		t.Fatal("refusing the stamp also dropped the lifecycle event — busy/idle tracking must survive")
	}

	// A GENUINE drift (claude compaction mints a new id under the SAME cwd) is
	// still corrected — the backstop must not break schema 46's whole purpose.
	if _, err := d.reportState(api.ReportStateRequest{
		ThreadID: "tid-own", Source: "sesh:claude-hook", Event: api.ReportTurnStarted,
		Seq: 2, AgentSessionID: "sess-own-compacted",
	}, 1001); err != nil {
		t.Fatalf("same-cwd drift report: %v", err)
	}
	if got := storedID(); got != "sess-own-compacted" {
		t.Fatalf("stored id = %q, want sess-own-compacted — a legitimate compaction drift was refused", got)
	}

	// A session not yet on disk is a RACE, not a lie: still stamped (fail open).
	if _, err := d.reportState(api.ReportStateRequest{
		ThreadID: "tid-own", Source: "sesh:claude-hook", Event: api.ReportTurnStarted,
		Seq: 3, AgentSessionID: "sess-unwritten",
	}, 1002); err != nil {
		t.Fatalf("unwritten-session report: %v", err)
	}
	if got := storedID(); got != "sess-unwritten" {
		t.Fatalf("stored id = %q, want sess-unwritten — a not-yet-written transcript must not be read as foreign", got)
	}
}
