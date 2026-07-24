package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
)

func rowsWith(ids ...string) []api.ThreadRow {
	out := make([]api.ThreadRow, len(ids))
	for i, id := range ids {
		out[i] = api.ThreadRow{Thread: api.Thread{ID: id, Name: id}}
	}
	return out
}

func hasRow(rows []api.ThreadRow, id string) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

// futureDeadline is a patch deadline comfortably beyond any test's runtime.
func futureDeadline() time.Time { return time.Now().Add(time.Hour) }

// reachableMachines marks every named machine present + reachable in the model's
// last-fetched mesh view (what applyPending's absence-GC consults).
func reachableMachines(names ...string) []api.MachineView {
	out := make([]api.MachineView, len(names))
	for i, n := range names {
		out[i] = api.MachineView{Machine: n, Reachable: true}
	}
	return out
}

// A hide patch drops the row from the rendered set immediately (no GC on a bare
// apply — the server is still stale), and leaves siblings alone.
func TestApplyPendingHideRemovesRow(t *testing.T) {
	m := Model{rows: rowsWith("a", "b"), pending: []*rowPatch{
		{id: "a", hide: true, deadline: futureDeadline()},
	}}
	m.applyPending(false)
	if hasRow(m.rows, "a") {
		t.Errorf("hide patch did not drop row a: %v", m.rows)
	}
	if !hasRow(m.rows, "b") {
		t.Errorf("hide patch wrongly dropped sibling b")
	}
	if m.pendingFor("a") == nil {
		t.Errorf("apply(false) must not GC the patch (server still stale)")
	}
}

// Once the read path catches up (the row is gone from the fetch) AND its owning
// machine is reporting reachable, the patch is GC'd — absence then proves the
// change landed.
func TestApplyPendingHideGCWhenServerCaughtUp(t *testing.T) {
	m := Model{
		rows:     rowsWith("b"), // a absent: archived-out / deleted
		machines: reachableMachines("m1"),
		pending: []*rowPatch{
			{id: "a", hide: true, machine: "m1", deadline: futureDeadline()},
		},
	}
	m.applyPending(true)
	if m.pendingFor("a") != nil {
		t.Errorf("patch should be GC'd once the row is gone from the fetch of a reachable machine")
	}
}

// A row's ABSENCE proves nothing when its owning machine is offline or missing from
// the mesh view (e.g. a one-tick reachability flap with hide-offline dropping all
// its rows): the patch must survive, or the stale row resurfaces when the machine
// returns. The deadline, not absence, bounds the patch.
func TestApplyPendingAbsenceKeepsPatchWhenMachineNotReporting(t *testing.T) {
	for name, machines := range map[string][]api.MachineView{
		"offline": {{Machine: "m1", Reachable: false}},
		"missing": nil,
	} {
		m := Model{
			rows:     rowsWith("b"),
			machines: machines,
			pending: []*rowPatch{
				{id: "a", hide: true, machine: "m1", deadline: futureDeadline()},
			},
		}
		m.applyPending(true)
		if m.pendingFor("a") == nil {
			t.Errorf("%s: patch GC'd on the row's absence while machine m1 wasn't reporting — never conclude from missing data", name)
		}
	}
}

// The regression that motivated the deadline: a patch must survive an UNBOUNDED
// number of reconcile fetches while the read path is stale — the old fetch-count
// TTL (4) made a patch die faster the busier the UI was (every action fires two
// fetches, burning every OTHER pending patch), resurfacing archived rows
// mid-reconcile.
func TestApplyPendingSurvivesManyStaleFetches(t *testing.T) {
	m := Model{pending: []*rowPatch{
		{id: "a", hide: true, machine: "m1", deadline: futureDeadline()},
	}}
	m.machines = reachableMachines("m1")
	for i := 0; i < 20; i++ { // 5× the old TTL
		m.rows = rowsWith("a", "b") // every fetch still stale: row present, unarchived
		m.applyPending(true)
		if hasRow(m.rows, "a") {
			t.Fatalf("row a resurfaced on stale fetch %d despite an unexpired patch", i+1)
		}
	}
	if m.pendingFor("a") == nil {
		t.Errorf("unexpired patch dropped by fetch count — expiry must be wall-clock only")
	}
}

// Past the deadline the patch is dropped, the server's truth resurfaces, and the
// failure is LOUD: actionErr names the action and the owning machine. A confirmed
// write the mesh never reflected is a sync problem to surface, never to mask.
func TestApplyPendingDeadlineExpiryIsLoud(t *testing.T) {
	m := Model{
		rows:     rowsWith("a", "b"),
		machines: reachableMachines("m1"),
		pending: []*rowPatch{
			{id: "a", hide: true, machine: "m1", desc: `archive "a"`, confirmed: true, deadline: time.Now().Add(-time.Second)},
		},
	}
	m.applyPending(true)
	if !hasRow(m.rows, "a") {
		t.Errorf("row a should resurface once the deadline passes (silent-failure surfacing)")
	}
	if m.pendingFor("a") != nil {
		t.Errorf("expired patch should be dropped")
	}
	if m.actionErr == nil {
		t.Fatalf("deadline expiry must set actionErr — a swallowed sync failure is the silent-failure class")
	}
	for _, want := range []string{`archive "a"`, "m1"} {
		if !strings.Contains(m.actionErr.Error(), want) {
			t.Errorf("expiry warning %q should name %q", m.actionErr, want)
		}
	}
}

// An archive patch carries the archived FIELD, so in a view that still admits the
// changed row (e.g. `all`) the patch reconciles by field once the server catches
// up — the row shows instead of staying wrongly hidden until expiry.
func TestApplyPendingArchivedFieldSatisfiesHidePatch(t *testing.T) {
	caughtUp := api.ThreadRow{Thread: api.Thread{ID: "a", Name: "a", Archived: true}}
	m := Model{
		rows:     []api.ThreadRow{caughtUp},
		machines: reachableMachines("m1"),
		pending: []*rowPatch{
			{id: "a", archived: bptr(true), hide: true, machine: "m1", deadline: futureDeadline()},
		},
	}
	m.applyPending(true)
	if m.pendingFor("a") != nil {
		t.Errorf("archived=true row should satisfy the archive patch (field proof)")
	}
	if !hasRow(m.rows, "a") {
		t.Errorf("satisfied patch must stop hiding the row (it is legitimately visible in this view)")
	}
	// Still stale → the field proof is absent → keep hiding.
	stale := api.ThreadRow{Thread: api.Thread{ID: "a", Name: "a"}}
	m2 := Model{
		rows:     []api.ThreadRow{stale},
		machines: reachableMachines("m1"),
		pending: []*rowPatch{
			{id: "a", archived: bptr(true), hide: true, machine: "m1", deadline: futureDeadline()},
		},
	}
	m2.applyPending(true)
	if hasRow(m2.rows, "a") {
		t.Errorf("stale row must stay hidden while the patch is unexpired")
	}
	if m2.pendingFor("a") == nil {
		t.Errorf("unsatisfied patch dropped early")
	}
}

// The stop patch flips the state axes to headless·idle instantly and reconciles by
// field once the owner's maintainer publishes the pane's death.
func TestRowPatchHeadBusyOverrides(t *testing.T) {
	head, busy := api.Headless, api.BusyIdle
	p := &rowPatch{head: &head, busy: &busy, deadline: futureDeadline()}
	r := api.ThreadRow{Thread: api.Thread{ID: "a"}, Head: api.Headful, Busy: api.BusyBusy}
	if p.satisfied(r) {
		t.Fatalf("a live headful row must not satisfy the stop patch")
	}
	p.apply(&r)
	if r.Head != api.Headless || r.Busy != api.BusyIdle {
		t.Fatalf("stop patch not applied: head=%s busy=%s", r.Head, r.Busy)
	}
	dead := api.ThreadRow{Thread: api.Thread{ID: "a"}, Head: api.Headless, Busy: api.BusyIdle}
	if !p.satisfied(dead) {
		t.Fatalf("headless·idle row should satisfy the stop patch")
	}
}

// holdClearPatch: optimistic only when the effective hold is the thread's OWN —
// a dominating inherited hold means clearing won't un-hold it, so no patch (only
// the owning daemon derives the inherited max).
func TestHoldClearPatchInheritedHoldNotOptimistic(t *testing.T) {
	now := time.Now().Unix()
	own := api.ThreadRow{Thread: api.Thread{ID: "x", Name: "x", OnHoldUntilUnix: now + 3600}, OnHold: true, OnHoldEffectiveUnix: now + 3600}
	inherited := api.ThreadRow{Thread: api.Thread{ID: "y", Name: "y", OnHoldUntilUnix: now + 3600}, OnHold: true, OnHoldEffectiveUnix: now + 7200}

	m := Model{view: ViewHold}
	p := m.holdClearPatch(own)
	if p == nil || p.onHold == nil || *p.onHold {
		t.Fatalf("own-hold clear should patch onHold=false, got %+v", p)
	}
	if !p.hide {
		t.Errorf("clearing in the on-hold view leaves the view — expected an optimistic hide")
	}
	if m.holdClearPatch(inherited) != nil {
		t.Errorf("a dominating inherited hold must NOT be cleared optimistically — the owner derives the max")
	}
}

// TestKeypressOptimismAndRevert (H55 option A): pressing an action key applies
// its optimistic patch IMMEDIATELY — before the routed subprocess has run, so
// the change renders instantly on any machine — and a failure actionMsg reverts
// EXACTLY that action's entry (loud actionErr, refetch restores server truth).
func TestKeypressOptimismAndRevert(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Name: "one", Machine: "mymain", AgentKind: "pi", Notify: true}}
	m := Model{machine: "mymain", rows: []api.ThreadRow{row}, machines: reachableMachines("mymain")}
	m.machines[0].Self = true

	nm, cmd := m.Update(keyMsg("n")) // notify toggle
	got := nm.(Model)
	if cmd == nil {
		t.Fatal("n produced no command")
	}
	// The flip is visible NOW — the cmd (the subprocess) has not run.
	if got.rows[0].Notify {
		t.Fatalf("keypress did not flip notify optimistically")
	}
	p := got.pendingFor("t1")
	if p == nil || p.notify == nil || *p.notify {
		t.Fatalf("keypress did not record the notify patch: %#v", p)
	}
	seq := got.pending[0].seq
	if seq == 0 {
		t.Fatal("recorded patch carries no per-action seq")
	}

	// Failure: revert exactly this entry; the next (stale) fetch shows server truth.
	nm2, _ := got.Update(actionMsg{err: errStub("notify failed"), id: "t1", seq: seq})
	g2 := nm2.(Model)
	if g2.ActionErr() == nil {
		t.Fatal("failed action must surface loudly in actionErr")
	}
	if g2.pendingFor("t1") != nil {
		t.Fatalf("failed action's patch not reverted: %#v", g2.pendingFor("t1"))
	}
	g2.rows = []api.ThreadRow{row} // the reconcile fetch returns server truth
	g2.applyPending(true)
	if !g2.rows[0].Notify {
		t.Fatalf("reverted patch still applied after the reconcile fetch")
	}
}

// errStub is a trivial error for feeding failure actionMsgs.
type errStub string

func (e errStub) Error() string { return string(e) }

// TestPerActionRevertKeepsSiblings: reverting one failed action must not
// disturb ANOTHER in-flight action's patch on the SAME thread (the H36 blocker
// — the old per-thread merged map could only revert everything).
func TestPerActionRevertKeepsSiblings(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Name: "old", Machine: "m1"}}
	m := Model{rows: []api.ThreadRow{row}, machines: reachableMachines("m1")}
	renameSeq := m.recordPatch(row, &rowPatch{name: sptr("new")}, "rename")
	notifySeq := m.recordPatch(row, &rowPatch{notify: bptr(true)}, "notify")
	if renameSeq == notifySeq {
		t.Fatal("actions must get distinct seqs")
	}
	m.dropPatch(renameSeq) // the rename failed
	m.rows = []api.ThreadRow{row}
	m.applyPending(true)
	if m.rows[0].Name != "old" {
		t.Errorf("reverted rename still applied: %q", m.rows[0].Name)
	}
	if !m.rows[0].Notify {
		t.Errorf("sibling notify patch was disturbed by the rename revert")
	}
}

// TestSupersedeSameField: a newer action on the same field REPLACES the older
// entry's optimism AND its reconcile obligation — two quick notify toggles must
// not leave the first entry demanding notify=true forever (it would expire into
// a bogus loud "sync degraded" warning once the server settles on false).
func TestSupersedeSameField(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Machine: "m1"}}
	m := Model{rows: []api.ThreadRow{row}, machines: reachableMachines("m1")}
	m.recordPatch(row, &rowPatch{notify: bptr(true)}, "notify")
	m.recordPatch(row, &rowPatch{notify: bptr(false)}, "notify")
	if len(m.pending) != 1 {
		t.Fatalf("superseded same-field entry not dropped: %d entries", len(m.pending))
	}
	// Server settles on the LATEST value → everything reconciles clean.
	m.rows = []api.ThreadRow{row} // notify=false
	m.applyPending(true)
	if len(m.pending) != 0 || m.actionErr != nil {
		t.Fatalf("supersede left a stale obligation: pending=%d err=%v", len(m.pending), m.actionErr)
	}
}

// TestUnarchiveSupersedesArchiveHide: U (or `a` in the archived view) while the
// ARCHIVE patch is still pending must spend that entry entirely — including its
// ride-along hide — so the restored row is visible at once instead of staying
// hidden until the archive patch expires into a bogus warning.
func TestUnarchiveSupersedesArchiveHide(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Name: "x", Machine: "m1"}}
	m := Model{view: ViewActive, rows: []api.ThreadRow{row}, machines: reachableMachines("m1")}
	m.recordPatch(row, &rowPatch{archived: bptr(true), hide: true}, "archive")
	if hasRow(m.rows, "t1") {
		t.Fatal("archive hide not applied at keypress")
	}
	m.rows = []api.ThreadRow{row} // stale fetch; still hidden by the pending entry
	m.recordPatch(row, &rowPatch{archived: bptr(false)}, "archive")
	if len(m.pending) != 1 {
		t.Fatalf("unarchive did not spend the archive entry: %d entries", len(m.pending))
	}
	m.rows = []api.ThreadRow{row}
	m.applyPending(false)
	if !hasRow(m.rows, "t1") {
		t.Fatal("row still hidden after the unarchive superseded the archive")
	}
}

// TestFlagKeypressOptimism (H55's headline case): `f` flips the row's Flagged
// field at KEYPRESS (there was NO flag patch at all before — a remote flag sat
// visually dead for the whole publish+sync+poll pipeline), and the patch
// reconciles by field once the server catches up.
func TestFlagKeypressOptimism(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "t1", Name: "one", Machine: "mymain", AgentKind: "pi"}}
	m := Model{machine: "mymain", rows: []api.ThreadRow{row}, machines: reachableMachines("mymain")}
	m.machines[0].Self = true

	nm, cmd := m.Update(keyMsg("f"))
	got := nm.(Model)
	if cmd == nil {
		t.Fatal("f produced no command")
	}
	if !got.rows[0].Flagged {
		t.Fatalf("f did not flip Flagged optimistically at keypress")
	}
	// Flag-on also re-enables auto-flagging (the one-rule semantic): the patch
	// asserts flagDisabled=false too.
	p := got.pendingFor("t1")
	if p == nil || p.flagged == nil || !*p.flagged || p.flagDisabled == nil || *p.flagDisabled {
		t.Fatalf("flag patch wrong: %#v", p)
	}
	// Server catches up → GC'd by field proof.
	caught := row
	caught.Flagged = true
	got.rows = []api.ThreadRow{caught}
	got.applyPending(true)
	if got.pendingFor("t1") != nil {
		t.Fatalf("flagged server row should satisfy the flag patch")
	}

	// ^f on a flag-disabled row enables at keypress likewise.
	disabled := row
	disabled.FlagDisabled = true
	m2 := Model{machine: "mymain", rows: []api.ThreadRow{disabled}, machines: reachableMachines("mymain")}
	m2.machines[0].Self = true
	nm2, cmd2 := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	g2 := nm2.(Model)
	if cmd2 == nil {
		t.Fatal("ctrl+f produced no command")
	}
	if g2.rows[0].FlagDisabled {
		t.Fatalf("ctrl+f did not clear FlagDisabled optimistically")
	}
}

// leavesCurrentView decides when an archive/unarchive should hide the row. A HEADLESS
// row leaves the active view when archived; a HEADFUL row does NOT (the default view
// keeps archived-but-headful threads — see builtinViewAdmits), so archiving it in place
// must not optimistically hide it.
func TestLeavesCurrentView(t *testing.T) {
	cases := []struct {
		view        View
		head        api.Head
		newArchived bool
		want        bool
	}{
		{ViewActive, api.Headless, true, true},   // archiving a headless thread in active -> leaves
		{ViewActive, api.Headful, true, false},   // archiving a HEADFUL thread in active -> STAYS (still shown)
		{ViewActive, api.Headless, false, false}, // unarchiving in active -> stays (it's active)
		{ViewArchived, api.Headful, false, true}, // unarchiving in archived -> leaves
		{ViewArchived, api.Headful, true, false}, // archiving in archived -> stays
		{ViewAll, api.Headful, true, false},      // all shows both
		{ViewAll, api.Headless, false, false},
	}
	for _, c := range cases {
		m := Model{view: c.view}
		next := api.ThreadRow{Thread: api.Thread{ID: "x"}, Head: c.head}
		next.Archived = c.newArchived
		if got := m.leavesViewWith(next); got != c.want {
			t.Errorf("view=%d head=%s newArchived=%v: leavesViewWith=%v, want %v", c.view, c.head, c.newArchived, got, c.want)
		}
	}
}

// TestLeavesViewWithHold proves the on-hold axis drives the optimistic hide the
// same way archive does: holding a thread leaves the active view; releasing it
// leaves the on-hold view; the `all` view keeps both.
func TestLeavesViewWithHold(t *testing.T) {
	row := api.ThreadRow{Thread: api.Thread{ID: "x"}}
	cases := []struct {
		view   View
		onHold bool
		want   bool
	}{
		{ViewActive, true, true},   // holding in active -> leaves
		{ViewActive, false, false}, // not held, still in active -> stays
		{ViewHold, false, true},    // releasing in on-hold -> leaves
		{ViewHold, true, false},    // held, stays in on-hold
		{ViewAll, true, false},     // all shows both
		{ViewAll, false, false},
	}
	for _, c := range cases {
		m := Model{view: c.view}
		next := row
		next.OnHold = c.onHold
		if got := m.leavesViewWith(next); got != c.want {
			t.Errorf("view=%d onHold=%v: leavesViewWith=%v, want %v", c.view, c.onHold, got, c.want)
		}
	}
}
