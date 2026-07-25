package conformance

import (
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

// The TUI sits OUTSIDE the feature matrix, so it gets its own anti-rigging track,
// structurally identical to TestMatrixComplete: a fixed list of CLAIMS about the
// real TUI, each backed by a real test that drives the actual Model against a real
// daemon and asserts against INDEPENDENTLY-fetched truth (never golden blobs, never
// fixtures). TestTUIClaimsComplete fails the build if any declared claim lacks a
// test; an unimplemented claim is a loud Skip, never a blank.
var declaredTUIClaims = []string{
	"grid-render-real-state",    // a row's glyph tracks its REAL activity, both directions
	"descendant-running-glyph",  // a parent's HB ↓ glyph tracks whether a REAL descendant is running a turn, both directions
	"grid-fanout-cross-machine", // the grid shows a peer's thread via the mesh
	"navigation-cursor",         // key nav moves the selection over real rows
	"selection-anchored",        // the cursor stays on the SAME thread when a new row appears above it on a poll (never shifts onto a neighbour — the archive-the-wrong-row footgun)
	"mesh-render-offline",       // a downed peer renders OFFLINE, threads hidden by default (H35), `o` reveals the last-known rows
	"action-stop",               // the stop key really ends the runtime, keeps the record
	"action-delete",             // the delete key really drops the record
	"action-archive",            // the archive key really parks the thread
	"action-nav",                // the nav key really switches the tmux client
	"action-nav-headless",       // Enter on a HEADLESS thread promotes it (headful) then enters
	"action-nav-attach",         // Enter from a plain shell (no tmux) attaches the terminal to the thread
	"action-nav-quits",          // a SUCCESSFUL nav quits the TUI (popup closes); a FAILED nav stays open with the error
	"sidebar-nav-stays",         // --sidebar (issue #8): a SUCCESSFUL nav really lands on the master server AND the TUI stays open (persistent pane)
	"action-nav-in-client",      // Enter on a LOCAL thread from the work socket switches EXACTLY this TUI's client (--client), with multiple clients attached
	"action-nav-remote-dead",    // Enter on a DEAD thread on ANOTHER machine resumes it THERE (routed over the mesh) and enters it
	"quit-esc",                  // Esc quits from normal mode; inside the line prompt it only closes the prompt
	"view-cycle-tab",            // Tab opens the VIEW PICKER (list of all views; enter/click applies, esc cancels) against REAL archived state; tab+enter reproduces the old cycle
	"action-rename",             // the r line-prompt really renames the thread on the daemon
	"action-tag",                // the t line-prompt really adds a tag on the daemon
	"action-untag",              // T opens a picker over the thread's tags; enter removes the highlighted one on the daemon, others survive
	"action-reparent",           // P pastes a parent uuid → thread reparent on the daemon (nests the node); empty = root; a cycle is refused loudly, record untouched
	"action-fork",               // f forks the selected thread into a new headless copy (`thread new --fork-from`); the copy carries the source conversation, the source is untouched
	"cursor-wrap",               // up/down wrap around the row list
	"id-toggle",                 // i toggles a real-tid8 ID column (the TUI's only id surface)
	"cursor-preselect",          // --cursor: the pane carrier resolves the REAL pane's thread and the first fetch lands the cursor on it
	"uuid-popup-copy",           // y shows the full real uuid in a popup; c pipes it through the real clipboard exec path
	"columns-config",            // the column system: defaults hide HEAD/BUSY text, [tui] config + overrides render exactly the named set, full-width NAME grows to content (untruncated within the cap; see column-max-width)
	"cwd-label-column",          // the CWD column renders a real thread's real cwd through the [[cwd_label]] rules; unconfigured = ~-relative
	"columns-reorder",           // [[tui.column]] position/after/before reposition columns over the default set (config→render)
	"column-colors",             // [[tui.column_color]] (+ NAME/CWD defaults) tint cells; colour is emitted and does NOT shift column widths/content
	"filter-narrow",             // / + typing narrows to matching real rows with a live matched/total count
	"filter-rank",               // fuzzy ranking: a word-boundary match outranks a mid-word match
	"filter-caret",              // caret editing: arrows/home position, runes insert AT the caret
	"filter-target-uuid",        // ctrl+t toggles the search target to uuid; a tid prefix narrows to exactly that thread
	"filter-esc-applies",        // Esc APPLIES the filter (stays active, / re-edits); normal-mode Esc still quits
	"filter-start-flag",         // --filter (the popup binding) opens already filtering
	"action-flag",               // F toggles the flag on the daemon + ⚑/⌀ render; ^f gates; F re-enables (one rule)
	"view-flag-pierce",          // a flagged child pierces a collapsed parent; unflagging re-hides it
	"custom-views",              // a [[tui.views]] predicate view shows exactly its rows against REAL ticket state, both directions
	"action-hold",               // h parks the thread on the daemon (future on_hold_until) + leaves the active view; h again releases it; H opens the explicit-date prompt
	"view-hold",                 // the default active view HIDES on-hold threads; the `on hold` view is the complement
	"view-active-archived-live", // the default view KEEPS an archived thread while it is still headful (real live pane) + hides an archived headless one; the shown row carries the ⊘ glyph
	"view-archived-order",       // the archived view orders by most-recently-archived first (archived_at DESC), against REAL archive timestamps
	"tree-render-fold",          // children collapse under their parent by default; →/← fold with ▾/▸ + rails over real threads
	"tree-config-expand",        // [tui] expand_children / --expand starts nodes expanded
	"tree-orphan-promotes",      // a child whose parent left the view promotes to top level
	"scroll-vertical",           // ctrl+j/k scroll the viewport + j/k cursor-follow over a >screenful row set; ▲/▼ indicators flag clipped rows
	"scroll-horizontal",         // h/l pan columns when the row is wider than the window; ‹/› flag clipped columns; a clipped column is brought into view
	"notify-toggle",             // n flips the real notify gate; the NTF column renders the muted state
	"action-mutate-remote",      // notify/stop/archive on a thread owned by ANOTHER machine ROUTE to the owner (the local daemon doesn't own it) — the cross-machine gap that direct-client calls silently missed
	"master-cursor",             // preselecting a NESTED CHILD (the master prefix+s jump path) auto-expands its ancestors so the collapsed child becomes the visible cursor target
	"tickets-view",              // K opens the tickets view, lists the thread's REAL bound tickets, and the status-change + delete + create actions land on the daemon
	"tickets-view-remote",       // tickets view on a thread owned by ANOTHER machine: every op routes to the thread's machine (the cross-machine "bound thread not found" bug)
	"tickets-view-filter",       // the K view defaults to showing ACTIVE tickets; Tab opens a status picker (incl. all) that narrows the list
	"tickets-columns",           // the ticket_name + ticket_input columns render a thread's REAL ticket summary (newest open ticket name + active-on-idle needs-input)
	"action-virtual-enter",      // Enter on a VIRTUAL row warns loudly (persistent actionErr naming realize) instead of entering; ◇ glyph rendered; f refuses too; record untouched
	"action-new-virtual",        // v opens a name prompt and creates a ROOT virtual group on the daemon (--no-parent beats inference); empty submit cancels; cursor preselects the new row
	"action-pin",                // p pins the selected top-level thread on the daemon (• marker renders); u un-pins it
	"action-reorder",            // m enters move mode; ↑ repositions the pinned row above another on the daemon
	"action-new-divider",        // D opens a label prompt and creates a real pinned divider on the daemon
	"column-max-width",          // full-width columns are capped by default (a long NAME truncates); `w` toggles the cap off to show full text; a [[tui.column_width]] override raises the cap (config→render)
	"thread-details",            // I opens a read-only takeover showing a thread's REAL fields (full uuid, machine, agent, cwd, live axis); esc closes back to the grid
	"mouse-click",               // a left CLICK selects the row under the pointer; a click on the ▸/▾ fold marker collapses/expands that thread's subtree — over a REAL parent/child tree + render
}

var boundTUIClaims = map[string]func(*testing.T){}

func registerTUIClaim(name string, fn func(*testing.T)) {
	if _, dup := boundTUIClaims[name]; dup {
		panic("duplicate TUI claim: " + name)
	}
	boundTUIClaims[name] = fn
}

// TestTUIClaims runs every bound claim as a subtest.
func TestTUIClaims(t *testing.T) {
	for _, name := range declaredTUIClaims {
		fn := boundTUIClaims[name]
		if fn == nil {
			continue // reported by TestTUIClaimsComplete
		}
		t.Run(name, fn)
	}
}

// TestTUIClaimsComplete fails the build if a declared claim has no test.
func TestTUIClaimsComplete(t *testing.T) {
	for _, c := range declaredTUIClaims {
		if _, ok := boundTUIClaims[c]; !ok {
			t.Errorf("TUI claim %q has no bound test", c)
		}
	}
}

func init() {
	registerTUIClaim("grid-render-real-state", claimGridRenderRealState)
	registerTUIClaim("descendant-running-glyph", claimDescendantRunningGlyph)
	registerTUIClaim("grid-fanout-cross-machine", claimGridFanout)
	registerTUIClaim("navigation-cursor", claimNavigationCursor)
	registerTUIClaim("selection-anchored", claimSelectionAnchored)
	registerTUIClaim("mesh-render-offline", claimMeshRenderOffline)
	registerTUIClaim("action-stop", claimActionStop)
	registerTUIClaim("action-delete", claimActionDelete)
	registerTUIClaim("action-archive", claimActionArchive)
	registerTUIClaim("action-fork", claimActionFork)
	registerTUIClaim("action-nav", claimActionNav)
	registerTUIClaim("action-nav-headless", claimActionNavHeadless)
	registerTUIClaim("action-nav-attach", claimActionNavAttach)
	registerTUIClaim("action-nav-quits", claimActionNavQuits)
	registerTUIClaim("sidebar-nav-stays", claimSidebarNavStays)
	registerTUIClaim("action-nav-in-client", claimActionNavInClient)
	registerTUIClaim("action-nav-remote-dead", claimActionNavRemoteDead)
	registerTUIClaim("tickets-view", claimTicketsView)
	registerTUIClaim("tickets-view-remote", claimTicketsViewRemote)
	registerTUIClaim("tickets-view-filter", claimTicketsViewFilter)
	registerTUIClaim("tickets-columns", claimTicketsColumns)
}

// claimTicketsView: K opens the tickets view for the selected thread, lists its
// REAL bound tickets, and the in-view status-change + delete actions land on the
// daemon (they exec `sesh ticket …`, which routes to the owner). The $EDITOR field
// edit is not driven here — tea.ExecProcess needs a live Program — but its save
// path is `ticket set`, itself a green conformance cell.
func claimTicketsView(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "tkthread")
	id := sb.ticketCreate(t, "fix-the-thing", "do it")
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind active: %v\n%s", err, stderr)
	}

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "tkthread")

	// K opens the view and loads the thread's bound tickets (a real `ticket list`).
	m = runKey(t, m, "K")
	if !m.TicketViewOpen() {
		t.Fatalf("K did not open the tickets view")
	}
	if tks := m.Tickets(); len(tks) != 1 || tks[0].ID != id {
		t.Fatalf("tickets view listed %v, want the bound ticket %s", ticketIDs(m.Tickets()), id)
	}

	// Drill in → status item → change active→done (a real set-status on the daemon).
	m = runKey(t, m, "enter") // detail (cursor on name)
	m = runKey(t, m, "down")  // prompt
	m = runKey(t, m, "down")  // status
	m = runKey(t, m, "enter") // status picker (cursor on the current status, active)
	m = runKey(t, m, "down")  // → done
	m = runKey(t, m, "enter") // set-status done
	if m.TicketErr() != nil {
		t.Fatalf("status change errored: %v", m.TicketErr())
	}
	if got := sb.ticketByID(t, id).Status; got != api.StatusDone {
		t.Fatalf("status change via the tickets view did not land: status=%q, want done", got)
	}

	// Delete through the view (detail → delete → y) — really gone from the daemon.
	m = runKey(t, m, "down")  // status(2) → thread(3)
	m = runKey(t, m, "down")  // → send(4)
	m = runKey(t, m, "down")  // → delete(5)
	m = runKey(t, m, "enter") // confirm popup
	m = runKey(t, m, "y")     // delete
	if m.TicketErr() != nil {
		t.Fatalf("delete via the tickets view errored: %v", m.TicketErr())
	}
	for _, tk := range sb.allTickets(t) {
		if tk.ID == id {
			t.Fatalf("ticket still present after delete via the tickets view")
		}
	}

	// Create a new ticket via `n` (name prompt) — it's created AND bound active to
	// this thread, so it really lands on the daemon under this thread.
	m = runKey(t, m, "n")
	for _, ch := range []string{"v", "i", "a", "T", "U", "I"} {
		m = runKey(t, m, ch)
	}
	m = runKey(t, m, "enter") // create + bind
	if m.TicketErr() != nil {
		t.Fatalf("create via the tickets view errored: %v", m.TicketErr())
	}
	created := sb.ticketsByThread(t, th.ID)
	if len(created) != 1 || created[0].Name != "viaTUI" || created[0].Status != api.StatusActive {
		t.Fatalf("create via `n` did not land a bound active ticket named viaTUI: %+v", created)
	}
}

// claimTicketsViewRemote: the tickets view acts on a thread that lives on ANOTHER
// machine — every ticket op (list, create+bind, status) must route to the THREAD's
// machine (--machine), not the local daemon. With no SESH_TICKET_OWNER, tickets are
// local to a daemon, so a bind on the wrong daemon 400s "bound thread not found"
// (the exact cross-machine bug). The local daemon must hold NO copy.
func claimTicketsViewRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)

	// The peer owns the thread (a real daemon over ssh-localhost).
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	th := peer.newHeadlessThread(t, "pi", "remote-tkt")

	// The local client knows the peer; the TUI runs here with --all-machines.
	bin := seshBin(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	m := tui.New(local.Home+"/daemon.sock", true).
		WithExec(bin, []string{"SESH_HOME=" + local.Home, "SESH_MACHINE=" + local.Machine}).
		WithLocal(local.Machine, local.TmuxSocket)
	m, _ = renderUntilRow(t, m, "remote-tkt") // wait for the mesh to replicate the peer's thread

	// Open the view + create a ticket via `n` — the create AND the active bind must
	// route to the peer (the thread's machine), or the bind 400s.
	m = runKey(t, m, "K")
	if !m.TicketViewOpen() {
		t.Fatalf("K did not open the tickets view for the remote thread")
	}
	m = runKey(t, m, "n")
	for _, ch := range []string{"r", "e", "m", "o", "t", "e"} {
		m = runKey(t, m, ch)
	}
	m = runKey(t, m, "enter")
	if m.TicketErr() != nil {
		t.Fatalf("remote create+bind errored (routing not carried to the thread's machine?): %v", m.TicketErr())
	}
	// It landed on the PEER, bound active...
	onPeer := peer.ticketsByThread(t, th.ID)
	if len(onPeer) != 1 || onPeer[0].Name != "remote" || onPeer[0].Status != api.StatusActive {
		t.Fatalf("remote ticket did not land bound-active on the peer: %+v", onPeer)
	}
	// ...and the LOCAL daemon holds no copy (it was never the writer).
	if lt := local.allTickets(t); len(lt) != 0 {
		t.Fatalf("local daemon holds %d tickets, want 0 (the write must go to the peer)", len(lt))
	}
}

// claimTicketsViewFilter: K defaults to showing ACTIVE tickets; Tab opens a status
// picker (incl. "all") that narrows the list. Two real bound tickets, one active +
// one done, prove the default hides the done one and the picker reveals it.
func claimTicketsViewFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "filt")

	act := sb.ticketCreate(t, "still-going", "")
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", act, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind active: %v\n%s", err, stderr)
	}
	dn := sb.ticketCreate(t, "all-finished", "")
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", dn, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind done ticket: %v\n%s", err, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", dn, "--status", "done"); err != nil {
		t.Fatalf("mark done: %v\n%s", err, stderr)
	}

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "filt")

	// K opens the view; both tickets load but the DEFAULT filter is `active`.
	m = runKey(t, m, "K")
	if m.TicketFilter() != "active" {
		t.Fatalf("default ticket filter = %q, want active", m.TicketFilter())
	}
	if got := len(m.Tickets()); got != 2 {
		t.Fatalf("loaded %d tickets, want 2 (both bound)", got)
	}
	if vis := m.VisibleTickets(); len(vis) != 1 || vis[0].ID != act {
		t.Fatalf("default view shows %v, want only the active ticket %s", ticketIDs(m.VisibleTickets()), act)
	}

	// Tab → status picker → pick "all" (active idx 2 → all idx 5): down x3, enter.
	m = runKey(t, m, "tab")
	for i := 0; i < 3; i++ {
		m = runKey(t, m, "down")
	}
	m = runKey(t, m, "enter")
	if m.TicketFilter() != "all" || len(m.VisibleTickets()) != 2 {
		t.Fatalf("after picking all: filter=%q visible=%d, want all/2", m.TicketFilter(), len(m.VisibleTickets()))
	}

	// Tab again → pick "done" (all idx 5 → done idx 3): up x2, enter.
	m = runKey(t, m, "tab")
	m = runKey(t, m, "up")
	m = runKey(t, m, "up")
	m = runKey(t, m, "enter")
	if vis := m.VisibleTickets(); m.TicketFilter() != "done" || len(vis) != 1 || vis[0].ID != dn {
		t.Fatalf("after picking done: filter=%q visible=%v, want done + [%s]", m.TicketFilter(), ticketIDs(m.VisibleTickets()), dn)
	}
}

// claimTicketsColumns: the ticket_name + ticket_input columns render a thread's
// REAL ticket summary — the newest open ticket's name, and a "!" when an active
// ticket sits on a headful·idle thread (the maintainer-derived needs-input).
func claimTicketsColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "colthread", "/tmp") // headful; idle after ready
	sb.waitThreadReady(t, th.ID, "pi")
	id := sb.ticketCreate(t, "review-pr", "")
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind active: %v\n%s", err, stderr)
	}

	cols, err := tui.ResolveColumns([]string{"name", "ticket_name", "ticket_input"})
	if err != nil {
		t.Fatalf("resolve columns: %v", err)
	}
	m := tui.New(sb.Home+"/daemon.sock", false).WithColumns(cols).WithLocal(sb.Machine, sb.TmuxSocket)

	// Poll the rendered grid until the owning daemon's maintainer has stamped the
	// ticket summary onto the row (an active ticket on a headful·idle thread).
	var row api.ThreadRow
	if !waitUntil(25*time.Second, func() bool {
		m, _ = render(t, m)
		r, ok := rowByName(m, "colthread")
		if ok {
			row = r
		}
		return ok && r.TicketName == "review-pr" && r.TicketNeedsInput
	}) {
		t.Fatalf("ticket columns never reflected the active ticket: name=%q needsInput=%v", row.TicketName, row.TicketNeedsInput)
	}
	_, view := render(t, m)
	for _, want := range []string{"TKT-NAME", "TKT!", "review-pr"} {
		if !strings.Contains(view, want) {
			t.Errorf("rendered grid is missing %q:\n%s", want, view)
		}
	}
}

// claimActionNavRemoteDead: Enter on a thread that is DEAD on ANOTHER machine must
// revive it THERE (resume routed over the mesh — the same `--machine` routing the
// CLI uses) and then enter it. Hit live: a dead codex thread on mymain was
// un-enterable from the macbook TUI ("resume it there first") while the picker
// routed it fine. Asserted on the real servers: the peer's session is genuinely
// recreated (it was genuinely gone) and a client lands on it.
func claimActionNavRemoteDead(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)

	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	th := peer.newThread(t, "pi", "rdead", "/tmp")
	peer.waitThreadReady(t, th.ID, "pi")
	// Really kill the runtime: the session must be GONE so the nav can't cheat.
	if _, stderr, err := peer.Runner.Run(t, "thread", "stop", "--id", th.ID); err != nil {
		t.Fatalf("thread stop: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool { return tmuxSessionCount(peer.TmuxSocket) == 0 }) {
		t.Fatalf("peer session did not die after stop")
	}

	master := "sesh-tuirdead-" + th.ID[:8]
	t.Cleanup(func() { exec.Command("tmux", "-L", master, "kill-server").Run() }) //nolint:errcheck
	mustTmux(t, master, "new-session", "-d", "-s", "m", "-n", "home")
	mustTmux(t, master, "new-window", "-t", "m", "-n", peer.Machine)
	mustTmux(t, master, "select-window", "-t", "m:home")

	bin := seshBin(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	navEnv := []string{"SESH_HOME=" + local.Home, "SESH_MACHINE=" + local.Machine, "SESH_MASTER_SOCKET=" + master}

	m := tui.New(local.Home+"/daemon.sock", true).WithExec(bin, navEnv).WithTmux("/tmp/notwork,1,1")
	// Wait for the replicated row to SETTLE to idle (stop leaves a brief stale
	// "waiting" snapshot in the mesh; Enter on it would skip the revival).
	m, _ = renderUntilRowState(t, m, "rdead", api.Headless, api.BusyIdle)

	if m = runKey(t, m, "enter"); m.ActionErr() != nil {
		t.Fatalf("nav on remote dead thread errored: %v", m.ActionErr())
	}
	// The thread was REVIVED on the peer (its session exists again — it was gone)
	// and entered (a client landed on it, master flipped to the peer's window).
	if !waitUntil(20*time.Second, func() bool { return innerClientSession(t, peer.TmuxSocket) == th.SessionName }) {
		t.Errorf("remote dead thread not revived+entered: peer client on %q, want %q", innerClientSession(t, peer.TmuxSocket), th.SessionName)
	}
	if got := activeWindowOf(t, master); got != peer.Machine {
		t.Errorf("master active window = %q, want %q", got, peer.Machine)
	}
}

// claimActionNavInClient: the TUI's Enter on a LOCAL thread, when the TUI runs inside
// the work socket's tmux, switches EXACTLY the client the TUI renders on — passed as
// --client, resolved by the caller while it held the tty. With several clients on the
// work server (the production topology: master-window supervisors + direct attaches),
// a ttyless ambient pick moved an arbitrary client — the live A4 bug. Two real
// clients; the model carries client B's identity; Enter must move B and only B.
func claimActionNavInClient(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "icnav", "/tmp")
	sb.waitThreadReady(t, th.ID, "pi")
	if _, err := sb.rawTmux(t, "new-session", "-d", "-s", "icscr", "-c", "/tmp"); err != nil {
		t.Fatalf("scratch session: %v", err)
	}

	// Two REAL clients on the scratch session (outer isolated servers).
	dir := t.TempDir()
	octl := func(sock string, args ...string) {
		c := exec.Command("tmux", append([]string{"-S", sock}, args...)...)
		c.Env = sandboxEnv(nil)
		c.Run() //nolint:errcheck
	}
	for _, n := range []string{"a", "b"} {
		sock := filepath.Join(dir, n+".sock")
		octl(sock, "-f", "/dev/null", "new-session", "-d", "-s", "o", "-x", "120", "-y", "40")
		octl(sock, "send-keys", "-t", "o:0", "-l", "env -u TMUX tmux -L "+sb.TmuxSocket+" attach -t icscr")
		octl(sock, "send-keys", "-t", "o:0", "Enter")
	}
	t.Cleanup(func() {
		octl(filepath.Join(dir, "a.sock"), "kill-server")
		octl(filepath.Join(dir, "b.sock"), "kill-server")
	})
	if !waitUntil(10*time.Second, func() bool { return workClientTotal(t, sb) == 2 }) {
		t.Fatalf("expected 2 work clients, got %v", workClientSessions(t, sb))
	}
	// Pick one client as "the TUI's" (B = the second by name order; identity is what
	// matters, not which).
	var names []string
	for name := range clientSessionsByName(t, sb) {
		names = append(names, name)
	}
	sort.Strings(names)
	bClient := names[len(names)-1]

	bin := seshBin(t)
	// The nav subprocess ALSO reads $TMUX (its in-client guard) — pin it to the same
	// work-socket value the model sees, overriding whatever tmux go test runs under.
	fakeTmux := "/tmp/x/" + sb.TmuxSocket + ",1,1"
	navEnv := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine, "SESH_TMUX_SOCKET=" + sb.TmuxSocket, "SESH_MASTER_SOCKET=" + sb.MasterSocket, "TMUX=" + fakeTmux}
	// $TMUX on the WORK socket => the in-client path; the model carries B's identity.
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, navEnv).
		WithLocal(sb.Machine, sb.TmuxSocket).
		WithTmux(fakeTmux).
		WithClient(bClient)
	m, _ = renderUntilRow(t, m, "icnav")
	if m = runKey(t, m, "enter"); m.ActionErr() != nil {
		t.Fatalf("in-client nav errored: %v", m.ActionErr())
	}

	// B (the TUI's client) is on the thread; the other client did not move.
	if !waitUntil(10*time.Second, func() bool {
		cs := clientSessionsByName(t, sb)
		other := ""
		for name, sess := range cs {
			if name != bClient {
				other = sess
			}
		}
		return cs[bClient] == th.SessionName && other == "icscr"
	}) {
		t.Errorf("TUI in-client nav: want B(%s)->%q + other->icscr, got %v", bClient, th.SessionName, clientSessionsByName(t, sb))
	}
}

// claimActionNavQuits: after Enter successfully enters a thread, the TUI must QUIT —
// in the popup flow, a TUI that stays open sits on top of the very thread the user
// just entered (the "popup doesn't close" bug). Both directions: a successful nav
// produces a quit; a FAILED nav keeps the TUI open with the error on screen.
func claimActionNavQuits(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	th := local.newThread(t, "pi", "navq", "/tmp")
	local.waitThreadReady(t, th.ID, "pi")

	master := "sesh-tuinavq-" + th.ID[:8]
	t.Cleanup(func() { exec.Command("tmux", "-L", master, "kill-server").Run() }) //nolint:errcheck
	mustTmux(t, master, "new-session", "-d", "-s", "m", "-n", "home")
	mustTmux(t, master, "new-window", "-t", "m", "-n", local.Machine)
	mustTmux(t, master, "select-window", "-t", "m:home")

	bin := seshBin(t)
	navEnv := []string{"SESH_HOME=" + local.Home, "SESH_MACHINE=" + local.Machine, "SESH_TMUX_SOCKET=" + local.TmuxSocket, "SESH_MASTER_SOCKET=" + master}
	m := tui.New(local.Home+"/daemon.sock", false).WithExec(bin, navEnv).WithLocal(local.Machine, local.TmuxSocket).WithTmux("/tmp/notwork,1,1")
	m, _ = renderUntilRow(t, m, "navq")

	step := func(m tui.Model) (tui.Model, tea.Cmd) {
		nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("enter")})
		if cmd == nil {
			t.Fatalf("enter produced no command")
		}
		nm2, cmd2 := nm.(tui.Model).Update(cmd()) // cmd() runs the REAL nav
		return nm2.(tui.Model), cmd2
	}

	// Success direction: the nav lands -> the model requests quit.
	m, after := step(m)
	if m.ActionErr() != nil {
		t.Fatalf("nav errored: %v", m.ActionErr())
	}
	if after == nil {
		t.Fatalf("successful nav produced no follow-up command (expected quit)")
	}
	if _, ok := after().(tea.QuitMsg); !ok {
		t.Errorf("successful nav did not QUIT the TUI (got %T) — the popup would stay open over the entered thread", after())
	}

	// Failure direction: no master server -> the nav fails -> the TUI STAYS OPEN
	// with the error (quitting would eat it).
	mustTmux(t, master, "kill-server")
	m, after = step(m)
	if m.ActionErr() == nil {
		t.Errorf("failed nav reported no error")
	}
	if after != nil {
		if _, ok := after().(tea.QuitMsg); ok {
			t.Errorf("FAILED nav quit the TUI — it must stay open showing the error")
		}
	}
}

// claimSidebarNavStays (issue #8): in --sidebar mode a SUCCESSFUL nav must (a)
// really land — the master server's active window switches to the machine window,
// the same observable the master nav path owns — and (b) NOT quit the TUI: the
// sidebar is a persistent pane beside the thread, not a popup over it. Real
// daemon, real pi thread, real master tmux server (mirrors claimActionNavQuits,
// whose quit direction stays the popup contract).
func claimSidebarNavStays(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	thA := local.newThread(t, "pi", "sbnav-a", "/tmp")
	local.waitThreadReady(t, thA.ID, "pi")
	th := local.newThread(t, "pi", "sbnav-b", "/tmp")
	local.waitThreadReady(t, th.ID, "pi")

	master := "sesh-tuisbnav-" + th.ID[:8]
	t.Cleanup(func() { exec.Command("tmux", "-L", master, "kill-server").Run() }) //nolint:errcheck
	mustTmux(t, master, "new-session", "-d", "-s", "m", "-n", "home")
	mustTmux(t, master, "new-window", "-t", "m", "-n", local.Machine)
	mustTmux(t, master, "select-window", "-t", "m:home")

	bin := seshBin(t)
	navEnv := []string{"SESH_HOME=" + local.Home, "SESH_MACHINE=" + local.Machine, "SESH_TMUX_SOCKET=" + local.TmuxSocket, "SESH_MASTER_SOCKET=" + master}
	m := tui.New(local.Home+"/daemon.sock", false).WithExec(bin, navEnv).WithLocal(local.Machine, local.TmuxSocket).WithTmux("/tmp/notwork,1,1").WithSidebar().WithSidebarFollow(local.Machine)
	m, _ = renderUntilRow(t, m, "sbnav-b")
	// The post-nav focus handoff reads the AMBIENT $TMUX at run time; scrub it so
	// running the follow-up cmd below can never select-pane in the developer's own
	// live tmux (the model's nav context is the injected WithTmux, unaffected).
	t.Setenv("TMUX", "")

	// FOLLOW: give thread B's session a second window and park it there, so the
	// inner switch has an observable effect (the --thread window landing, the
	// same observable the nav-window cells own). Arrowing onto B fires the
	// follow IMMEDIATELY (no debounce) via the FAST PATH — a direct daemon
	// call (client.TmuxNav on the sandbox daemon, no subprocess) — which must
	// succeed, land the session on the thread's window, keep the TUI open, and
	// hand NO focus (the completion only re-arms; the user is still arrowing).
	sessB := th.SessionName
	mustTmux(t, local.TmuxSocket, "new-window", "-t", sessB+":", "-n", "scratch")
	mustTmux(t, local.TmuxSocket, "select-window", "-t", sessB+":scratch")
	nmF, followCmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nmF.(tui.Model)
	if followCmd == nil {
		t.Fatalf("arrow move in sidebar mode fired no follow (must be immediate, no debounce)")
	}
	followMsg := followCmd() // the REAL follow nav (daemon fast path)
	nmF3, afterFollow := m.Update(followMsg)
	m = nmF3.(tui.Model)
	if m.ActionErr() != nil {
		t.Fatalf("follow nav errored: %v", m.ActionErr())
	}
	if afterFollow != nil {
		if _, quit := afterFollow().(tea.QuitMsg); quit {
			t.Fatalf("follow completion must never quit")
		}
	}
	out, err := exec.Command("tmux", "-L", local.TmuxSocket, "display-message", "-t", sessB, "-p", "#{window_index}").Output()
	if err != nil {
		t.Fatalf("read work session window: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "0" {
		t.Errorf("follow did not land the work session on the thread's window (active window %s, want 0)", got)
	}

	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("enter")})
	if cmd == nil {
		t.Fatalf("enter produced no command")
	}
	nm2, after := nm.(tui.Model).Update(cmd()) // cmd() runs the REAL nav
	m = nm2.(tui.Model)
	if m.ActionErr() != nil {
		t.Fatalf("sidebar nav errored: %v", m.ActionErr())
	}
	// (a) The nav REALLY happened: the master's active window is the machine window.
	out2, err2 := exec.Command("tmux", "-L", master, "display-message", "-t", "m", "-p", "#{window_name}").Output()
	if err2 != nil {
		t.Fatalf("read master active window: %v", err2)
	}
	if got := strings.TrimSpace(string(out2)); got != local.Machine {
		t.Errorf("master active window = %q after sidebar nav, want %q (nav did not land)", got, local.Machine)
	}
	// (b) The TUI STAYS: no quit follows a successful sidebar nav.
	if after != nil {
		if _, quit := after().(tea.QuitMsg); quit {
			t.Errorf("sidebar nav QUIT the TUI — a persistent pane must stay open")
		}
	}
}

// claimActionNavAttach: when the TUI runs OUTSIDE tmux (a plain shell), Enter doesn't
// switch a client (there is none) — it asks the caller to ATTACH the terminal to the
// thread. Asserts the model quits with a pending `tmux nav --to <thread> --attach`.
func claimActionNavAttach(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	th := local.newThread(t, "pi", "attd", "/tmp")
	local.waitThreadReady(t, th.ID, "pi")

	bin := seshBin(t)
	// m.tmux == "" => a plain shell => Enter attaches (not switch).
	m := tui.New(local.Home+"/daemon.sock", false).WithExec(bin, nil).WithLocal(local.Machine, local.TmuxSocket).WithTmux("")
	m, _ = renderUntilRow(t, m, "attd")
	m = runKey(t, m, "enter")

	argv, ok := m.PendingAttach()
	if !ok {
		t.Fatalf("Enter outside tmux did not request an attach (PendingAttach=false)")
	}
	joined := strings.Join(argv, " ")
	want := local.Machine + ":" + th.SessionName
	if !strings.Contains(joined, "nav") || !strings.Contains(joined, "--attach") || !strings.Contains(joined, want) {
		t.Errorf("attach argv = %v; want it to `nav --to %s --attach`", argv, want)
	}
}

// claimActionNavHeadless: a HEADLESS thread has no pane, so Enter must PROMOTE it
// (headless -> headed, conversation resumed) and THEN enter — not silently nav to a
// non-existent session. Asserts the real promotion (a session that never existed for a
// headless thread now exists) and that the nav landed a client on it.
func claimActionNavHeadless(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// A LOCAL headless thread (promotion routes to the local daemon) with one real turn
	// so it has a resumable session id.
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	th := local.newHeadlessThread(t, "pi", "hlnav")
	local.headlessTurn(t, th.ID, "Reply with exactly: ok")

	// A master window for this machine, so the (master-path) nav has a window to switch to.
	master := "sesh-tuihlmaster-" + th.ID[:8]
	t.Cleanup(func() { exec.Command("tmux", "-L", master, "kill-server").Run() }) //nolint:errcheck
	mustTmux(t, master, "new-session", "-d", "-s", "m", "-n", "home")
	mustTmux(t, master, "new-window", "-t", "m", "-n", local.Machine)
	mustTmux(t, master, "select-window", "-t", "m:home")

	bin := seshBin(t)
	// A local thread's inner switch uses cfg.TmuxSocket, so the nav subprocess needs the
	// sandbox's work socket (in production the TUI inherits it from its env).
	navEnv := []string{"SESH_HOME=" + local.Home, "SESH_MACHINE=" + local.Machine, "SESH_TMUX_SOCKET=" + local.TmuxSocket, "SESH_MASTER_SOCKET=" + master}
	// In tmux but NOT on the work socket => the master nav path (deterministic regardless
	// of the ambient $TMUX go test runs under).
	m := tui.New(local.Home+"/daemon.sock", false).WithExec(bin, navEnv).WithLocal(local.Machine, local.TmuxSocket).WithTmux("/tmp/notwork,1,1")
	// Wait for the row to SETTLE to idle (the turn above leaves a brief "working"
	// snapshot; Enter on a stale row would skip the revival).
	m, _ = renderUntilRowState(t, m, "hlnav", api.Headless, api.BusyIdle)

	// Enter -> promote then enter.
	if m = runKey(t, m, "enter"); m.ActionErr() != nil {
		t.Fatalf("nav-on-headless errored: %v", m.ActionErr())
	}
	// The promotion created a real session (headless threads NEVER have one) and the nav
	// landed a client on it — the observable proof of promote-then-enter.
	if !waitUntil(15*time.Second, func() bool { return innerClientSession(t, local.TmuxSocket) == "sesh_hlnav" }) {
		t.Errorf("after Enter on a headless thread, no client on sesh_hlnav (promote+enter failed); got %q", innerClientSession(t, local.TmuxSocket))
	}
}

// claimActionNav: the nav key really switches the tmux client to the selected
// thread's session — across the real master/inner tmux nesting, over a real ssh
// hop, via the nav primitive. Asserted on the real tmux servers.
func claimActionNav(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)

	// The peer machine: a real daemon. Its tmux socket is the inner mytmux.
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	th := peer.newThread(t, "pi", "navme", "/tmp") // session sesh_navme on the peer
	peer.waitThreadReady(t, th.ID, "pi")

	// A master tmux with a window for the peer machine, NOT currently focused.
	master := "sesh-tuinavmaster-" + th.ID[:8]
	t.Cleanup(func() { exec.Command("tmux", "-L", master, "kill-server").Run() }) //nolint:errcheck
	mustTmux(t, master, "new-session", "-d", "-s", "m", "-n", "home")
	mustTmux(t, master, "new-window", "-t", "m", "-n", peer.Machine)
	mustTmux(t, master, "select-window", "-t", "m:home")

	// The local client machine: knows the peer (incl. its tmux socket) and the
	// master socket. The TUI runs here.
	bin := seshBin(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	navEnv := []string{"SESH_HOME=" + local.Home, "SESH_MACHINE=" + local.Machine, "SESH_MASTER_SOCKET=" + master}

	// In tmux but not on the work socket => the master nav path (the peer thread is remote
	// anyway). Deterministic regardless of the ambient $TMUX go test runs under.
	m := tui.New(local.Home+"/daemon.sock", true).WithExec(bin, navEnv).WithTmux("/tmp/notwork,1,1")
	m, _ = renderUntilRow(t, m, "navme") // wait for the mesh sync to replicate it

	// Enter -> nav. Asserts on the REAL servers:
	if m = runKey(t, m, "enter"); m.ActionErr() != nil {
		t.Fatalf("nav action errored: %v", m.ActionErr())
	}
	// outer: the master switched to the peer's window.
	if got := activeWindowOf(t, master); got != peer.Machine {
		t.Errorf("master active window = %q, want %q (outer switch failed)", got, peer.Machine)
	}
	// inner: the peer's tmux now has a client on the thread's session (bare-shell
	// kick, since nothing was attached).
	if !waitUntil(5*time.Second, func() bool { return innerClientSession(t, peer.TmuxSocket) == "sesh_navme" }) {
		t.Errorf("peer tmux client not on sesh_navme; got %q (inner switch failed)", innerClientSession(t, peer.TmuxSocket))
	}
}

func mustTmux(t *testing.T, socket string, args ...string) {
	t.Helper()
	if out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("tmux -L %s %v: %v\n%s", socket, args, err, out)
	}
}

func activeWindowOf(t *testing.T, socket string) string {
	t.Helper()
	out, _ := exec.Command("tmux", "-L", socket, "list-windows", "-F", "#{?window_active,#{window_name},}").Output()
	return strings.TrimSpace(string(out))
}

func innerClientSession(t *testing.T, socket string) string {
	t.Helper()
	out, _ := exec.Command("tmux", "-L", socket, "list-clients", "-F", "#{client_session}").Output()
	return strings.TrimSpace(string(out))
}

// runKey drives a keypress, runs the command it produces (the real action), and
// feeds the result back through Update — how a test exercises an in-app action end
// to end. The returned model's LastErr() carries any action error.
func runKey(t *testing.T, m tui.Model, key string) tui.Model {
	t.Helper()
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	m2 := nm.(tui.Model)
	if cmd == nil {
		return m2
	}
	nm2, _ := m2.Update(cmd())
	return nm2.(tui.Model)
}

// claimActionStop: the stop key really ends the runtime (real session + agent
// process dead) but KEEPS the record (still listed, now dead) — the stop/delete
// split, observed on the real daemon + tmux server.
func claimActionStop(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "stopme", "/tmp")
	var pid int
	if !waitUntil(agentStartTimeout, func() bool {
		_, p, ok := sb.markedPane(t, th.ID)
		pid = p
		return ok && agentRunningUnder(p, "pi")
	}) {
		t.Fatalf("agent never came up")
	}

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "stopme") // single thread => cursor on it
	if m = runKey(t, m, "x"); m.ActionErr() != nil {
		t.Fatalf("stop action errored: %v", m.ActionErr())
	}
	// The REAL runtime is dead...
	if !waitUntil(10*time.Second, func() bool { return !pidAlive(pid) }) {
		t.Errorf("stop action did not kill the agent process")
	}
	if !waitUntil(10*time.Second, func() bool {
		_, err := sb.rawTmux(t, "has-session", "-t", "=sesh_stopme")
		return err != nil
	}) {
		t.Errorf("stop action did not kill the tmux session")
	}
	// ...but the record is KEPT (dead, resumable) — unlike delete.
	if !threadInList(t, sb, th.ID) {
		t.Errorf("stop dropped the record (should keep it)")
	}
}

// claimActionFork: `f` forks the selected thread into a NEW headless copy via
// `thread new --fork-from`. It proves the TUI key drives the real fork mechanism:
// a source pi conversation (sentinel OBSIDIAN) is forked through the key, and the
// brand-new thread's transcript carries that turn (a real copy, not an empty
// thread) while the source is untouched.
func claimActionFork(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	src := sb.newHeadlessThread(t, "pi", "trunk")
	sb.headlessTurn(t, src.ID, "Reply with exactly the word OBSIDIAN and nothing else")
	srcBefore := forkedTranscript(t, sb, src.ID)

	before := map[string]bool{}
	for _, th := range sb.listThreads(t) {
		before[th.ID] = true
	}

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "trunk") // single thread => cursor on it
	if m = runKey(t, m, "F"); m.ActionErr() != nil {
		t.Fatalf("fork action errored: %v", m.ActionErr())
	}

	// A NEW headless thread appeared (same agent, different id) — the copy.
	var forkID string
	for _, th := range sb.listThreads(t) {
		if before[th.ID] {
			continue
		}
		if th.AgentKind != "pi" {
			t.Fatalf("fork created a %s thread, want pi", th.AgentKind)
		}
		if th.AgentSessionID == src.AgentSessionID {
			t.Fatalf("fork reused the source session id (no branch happened)")
		}
		if th.Name != "trunk (fork)" {
			t.Errorf("fork name = %q, want %q (keep source name marked as a fork)", th.Name, "trunk (fork)")
		}
		forkID = th.ID
	}
	if forkID == "" {
		t.Fatalf("fork did not create a new thread")
	}
	// The copy carries the source conversation...
	if br := forkedTranscript(t, sb, forkID); !strings.Contains(br, "OBSIDIAN") {
		t.Errorf("forked copy lost the source turn (not a real copy)")
	}
	// ...and the source transcript is untouched.
	if forkedTranscript(t, sb, src.ID) != srcBefore {
		t.Errorf("the SOURCE transcript changed (fork must not touch it)")
	}
}

// claimActionDelete: `d` opens a y/n confirmation; cancelling keeps the record,
// confirming with `y` really drops it. (The daemon's orphan guard refuses a live
// delete, so the thread is dead-by-construction.)
func claimActionDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "delme") // dead-by-construction (no runtime)

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "delme")
	// `d` opens the confirmation; it must NOT delete on its own, and a non-y key cancels.
	m = runKey(t, m, "d")
	if !m.Confirming() {
		t.Fatalf("d did not open the delete confirmation")
	}
	m = runKey(t, m, "n")
	if m.Confirming() {
		t.Fatalf("n did not dismiss the confirmation")
	}
	if !threadInList(t, sb, th.ID) {
		t.Fatalf("a cancelled delete still dropped the record")
	}
	// Confirm with `y` → the record is really gone from the daemon.
	m = runKey(t, m, "d")
	if m = runKey(t, m, "y"); m.ActionErr() != nil {
		t.Fatalf("delete action errored: %v", m.ActionErr())
	}
	// OPTIMISTIC: the row is gone from the grid IMMEDIATELY — runKey ran the delete
	// + its actionMsg but NOT the reconcile fetch, so this proves the row dropped
	// without waiting for the mesh read path (the latency fix), not a slow refetch.
	if _, ok := rowByName(m, "delme"); ok {
		t.Errorf("deleted row still rendered immediately after confirm (optimistic hide missing)")
	}
	if threadInList(t, sb, th.ID) {
		t.Errorf("delete did not drop the record after confirmation")
	}
}

// claimActionArchive: `a` opens a y/n confirmation; confirming with `y` really
// parks the thread (record kept, hidden from the active grid).
func claimActionArchive(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "parkme")

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "parkme")
	// `a` archives INSTANTLY — no confirmation popup (H54: act-then-undo).
	m = runKey(t, m, "a")
	if m.Confirming() {
		t.Fatalf("a opened a confirmation — archive must be instant")
	}

	// OPTIMISTIC: the row leaves the active grid IMMEDIATELY — runKey ran the archive
	// + its actionMsg but NOT the reconcile fetch, so the row dropped without waiting
	// for the mesh read path to reflect the archived flag (the latency fix).
	if _, ok := rowByName(m, "parkme"); ok {
		t.Errorf("archived row still rendered immediately (optimistic hide missing)")
	}

	// Record kept but hidden from the active list (the daemon truth)...
	if hasThread(sb.listThreads(t), th.ID) {
		t.Errorf("archived thread still in the active list")
	}
	if !hasThread(sb.listThreadsArchived(t), th.ID) {
		t.Errorf("archived thread missing from the archived list (record not kept)")
	}
	// ...and gone from the rendered active grid (after the maintainer re-reads the
	// archived flag and the TUI filters it).
	m = renderUntilGone(t, m, "parkme")

	// U UNDOES the archive: the record returns to the active list on the
	// daemon (the observable truth) and the row re-renders in the grid.
	m = runKey(t, m, "U")
	if !waitUntil(10*time.Second, func() bool { return hasThread(sb.listThreads(t), th.ID) }) {
		t.Fatalf("U did not un-archive the thread on the daemon")
	}
	m, _ = renderUntilRow(t, m, "parkme")
	_ = m
}

// render runs the model's REAL fetch command against the daemon and renders the
// result. No fixtures: the rows come from the live HTTP+JSON grid.
func render(t *testing.T, m tui.Model) (tui.Model, string) {
	t.Helper()
	msg := m.Init()() // execute the fetch cmd -> real grid response
	nm, _ := m.Update(msg)
	m2 := nm.(tui.Model)
	return m2, m2.View()
}

// rowLine returns the rendered line mentioning name.
func rowLine(view, name string) string {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	return ""
}

// claimGridRenderRealState: the rendered glyph for a thread matches its REAL
// activity, and FLIPS when reality changes (idle ● -> working ◐ across a real
// turn). This is the matrix's both-directions honesty applied to rendering.
func claimGridRenderRealState(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newThread(t, "pi", "tuithread", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")

	m := tui.New(sb.Home+"/daemon.sock", false)

	// DATA is real (from the daemon, not a fixture): a row with our real machine,
	// name, and the independently-fetched activity. RENDER is faithful: the view
	// line shows the glyph for that activity.
	m, row := renderUntilRow(t, m, "tuithread")
	_, view := render(t, m)
	if row.Machine != sb.Machine {
		t.Errorf("row machine = %q, want the real %q (fixture?)", row.Machine, sb.Machine)
	}
	if st := sb.threadStatus(t, th.ID); row.Head != st.Head || row.Busy != st.Busy {
		t.Errorf("row state = %s/%s, but the daemon says %s/%s", row.Head, row.Busy, st.Head, st.Busy)
	}
	if line := rowLine(view, "tuithread"); !strings.Contains(line, tui.HeadGlyph(row)) || !strings.Contains(line, tui.BusyGlyph(row)) {
		t.Errorf("rendered line does not show BOTH axis glyphs for the row's state: %q", line)
	}

	// Change reality: a real turn -> the row's BUSY axis AND its rendered glyph
	// must flip (the matrix's both-directions rule, applied to the TUI), while
	// the HEAD axis stays headful.
	sb.sendKeys(t, pane, "Write a detailed 150-word explanation of how DNS works")
	if !waitUntil(30*time.Second, func() bool {
		mm, v := render(t, m)
		r, ok := rowByName(mm, "tuithread")
		return ok && r.Busy == api.BusyBusy && r.Head == api.Headful &&
			strings.Contains(rowLine(v, "tuithread"), tui.BusyGlyph(r)) &&
			strings.Contains(rowLine(v, "tuithread"), tui.HeadGlyph(r))
	}) {
		t.Fatalf("TUI grid never reflected the busy state of a real turn")
	}
}

// rowByName returns the model row with the given thread name.
func rowByName(m tui.Model, name string) (api.ThreadRow, bool) {
	for _, r := range m.Rows() {
		if r.Name == name {
			return r, true
		}
	}
	return api.ThreadRow{}, false
}

// The TUI now reads the mesh, which is eventually-consistent: a just-created thread
// appears within a maintainer tick (local) or a sync (peer), not instantly. So
// claims poll the render rather than rendering once.

func renderUntilRow(t *testing.T, m tui.Model, name string) (tui.Model, api.ThreadRow) {
	t.Helper()
	var got api.ThreadRow
	if !waitUntil(20*time.Second, func() bool {
		m, _ = render(t, m)
		r, ok := rowByName(m, name)
		if ok {
			got = r
		}
		return ok
	}) {
		_, view := render(t, m)
		t.Fatalf("row %q never appeared in the TUI; view:\n%s", name, view)
	}
	return m, got
}

// renderUntilRowState waits for the named row to reach the given activity — used
// before Enter when the test just changed a thread's runtime (the maintainer's
// snapshot lags by a tick; Enter on a stale row exercises the wrong path).
func renderUntilRowState(t *testing.T, m tui.Model, name string, wantHead api.Head, wantBusy api.Busy) (tui.Model, api.ThreadRow) {
	t.Helper()
	var got api.ThreadRow
	if !waitUntil(25*time.Second, func() bool {
		m, _ = render(t, m)
		r, ok := rowByName(m, name)
		if ok && r.Head == wantHead && r.Busy == wantBusy {
			got = r
			return true
		}
		return false
	}) {
		_, view := render(t, m)
		t.Fatalf("row %q never reached %s/%s in the TUI; view:\n%s", name, wantHead, wantBusy, view)
	}
	return m, got
}

func renderUntilCount(t *testing.T, m tui.Model, n int) tui.Model {
	t.Helper()
	if !waitUntil(20*time.Second, func() bool {
		m, _ = render(t, m)
		return len(m.Rows()) >= n
	}) {
		t.Fatalf("TUI never showed >= %d rows", n)
	}
	return m
}

func renderUntilGone(t *testing.T, m tui.Model, name string) tui.Model {
	t.Helper()
	if !waitUntil(20*time.Second, func() bool {
		m, _ = render(t, m)
		_, ok := rowByName(m, name)
		return !ok
	}) {
		t.Fatalf("row %q never disappeared from the TUI", name)
	}
	return m
}

// claimGridFanout: the grid (--all-machines) renders a PEER's thread, fetched via
// the mesh — proving cross-machine state is real, not local-only.
func claimGridFanout(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	bin := seshBin(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	there := peer.newHeadlessThread(t, "pi", "onpeer")

	m := tui.New(local.Home+"/daemon.sock", true) // all-machines
	// The peer thread appears once the mesh sync replicates it (eventually consistent).
	m, row := renderUntilRow(t, m, "onpeer")
	_ = there
	// The row is the PEER's (real cross-machine data), and it is rendered.
	if row.Machine != peer.Machine {
		t.Errorf("peer row machine = %q, want %q", row.Machine, peer.Machine)
	}
	if _, view := render(t, m); rowLine(view, "onpeer") == "" {
		t.Errorf("peer thread not visible in the rendered view")
	}
}

// claimMeshRenderOffline: the rendered mesh view reflects offline browsing under
// the H35 contract — a peer that goes down renders as OFFLINE with its threads
// HIDDEN by default (the footer reports the hidden count), and pressing `o`
// reveals the last-known threads (retained, not dropped). The pre-H35 version of
// this claim asserted the old always-listed behavior and had been failing since
// hide-offline became the default.
func claimMeshRenderOffline(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	local := newSandbox(t, matrix.Local)
	local.startDaemon(t)
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	bin := seshBin(t)
	if _, stderr, err := local.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	peer.newHeadlessThread(t, "pi", "onpeer")

	m := tui.New(local.Home+"/daemon.sock", true) // all-machines
	m, _ = renderUntilRow(t, m, "onpeer")         // synced and rendered while up

	// Take the peer down: the render must flag it OFFLINE, hide its threads by
	// default, and report the hidden count in the footer (H35).
	if _, stderr, err := peer.daemonRunner.Run(t, "daemon", "stop"); err != nil {
		t.Fatalf("stop peer daemon: %v\n%s", err, stderr)
	}
	if !waitUntil(20*time.Second, func() bool {
		mm, view := render(t, m)
		m = mm
		return strings.Contains(view, "OFFLINE") && strings.Contains(view, "hidden") && rowLine(view, "onpeer") == ""
	}) {
		_, view := render(t, m)
		t.Fatalf("TUI never rendered the downed peer as OFFLINE with its threads hidden; view:\n%s", view)
	}
	// `o` reveals the peer's LAST-KNOWN threads — offline browsing (retained,
	// not dropped).
	m = runKey(t, m, "o")
	if !waitUntil(15*time.Second, func() bool {
		mm, view := render(t, m)
		m = mm
		return rowLine(view, "onpeer") != ""
	}) {
		_, view := render(t, m)
		t.Fatalf("`o` did not reveal the downed peer's last-known threads; view:\n%s", view)
	}
}

// claimNavigationCursor: cursor keys move the selection over the REAL rows.
func claimNavigationCursor(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	a := sb.newHeadlessThread(t, "pi", "alpha")
	b := sb.newHeadlessThread(t, "pi", "beta")

	m := tui.New(sb.Home+"/daemon.sock", false)
	m = renderUntilCount(t, m, 2)
	first, _ := m.Selected()
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(tui.Model)
	second, _ := m.Selected()
	if first.ID == second.ID {
		t.Errorf("cursor did not move on KeyDown")
	}
	// The two selections are real, distinct threads we created.
	ids := map[string]bool{a.ID: true, b.ID: true}
	if !ids[first.ID] || !ids[second.ID] {
		t.Errorf("selected rows are not the real threads: %s, %s", first.ID, second.ID)
	}
}

// claimSelectionAnchored: the selection is anchored to a SPECIFIC thread, not a row
// index. With the cursor on "delta" (below "beta"), a NEW thread "alpha" that sorts to
// the TOP appears on the next poll — pushing every row down one. A positional cursor
// would now point at "beta" (the archive/delete-the-wrong-row footgun); the anchored
// cursor tracks "delta" to its new index. Driven against a REAL daemon: each render()
// is a real fetch, so the anchoring runs on the exact poll that first includes alpha.
func claimSelectionAnchored(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "beta")
	delta := sb.newHeadlessThread(t, "pi", "delta")

	m := tui.New(sb.Home+"/daemon.sock", false)
	m = renderUntilCount(t, m, 2)

	// Put the cursor on delta (sorts after beta → index 1).
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = nm.(tui.Model)
	if sel, ok := m.Selected(); !ok || sel.ID != delta.ID {
		t.Fatalf("cursor should be on delta before the new row; got %+v ok=%v", sel, ok)
	}

	// A new thread that sorts ABOVE both existing rows appears on the next poll.
	sb.newHeadlessThread(t, "pi", "alpha")
	m, _ = renderUntilRow(t, m, "alpha")

	// The selection must still be delta — NOT whatever slid into its old slot.
	sel, ok := m.Selected()
	if !ok || sel.ID != delta.ID {
		t.Fatalf("selection must stay anchored to delta after alpha appeared above it; got %+v ok=%v cursor=%d", sel, ok, m.Cursor())
	}
	if sel.Name == "alpha" || sel.Name == "beta" {
		t.Fatalf("selection shifted onto a neighbour (%q) — anchoring failed", sel.Name)
	}
}
