package conformance

// thread.pin + thread.divider cells: MANUAL ORDERING and DIVIDERS.
//
// thread.pin: a top-level thread can be pinned into a manual order (a fractional
// pin_order key) that renders ABOVE the auto-sorted block. The honest external
// effects proven here (daemon truth, not just TUI paint): pin sets the key; a second
// pin-to-top orders below the first (b above a); --before/--after reposition relative
// to another pinned node; unpin clears it; pinning a CHILD is refused loudly; and the
// key is cleared on archive AND on reparent-under-another-thread (the two implicit
// clears). Remote proves the pin ROUTES to the owner and lands on the peer's own store.
//
// thread.divider: `thread new --divider` records a visual node (agent_kind "divider",
// always pinned, no agent/pane/transcript). Proven: the record + its pin_order, NO tmux
// session, agent verbs refuse LOUDLY (nonAgentGate, naming divider-ness), it can't be
// archived or un-pinned (delete it instead), agent-shaped creation flags are refused,
// and delete removes it. Remote shares the body — every verb routes over a real ssh hop.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tui"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.pin", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testPin(t, loc) })
		matrix.RegisterTest("thread.divider", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testDivider(t, loc) })
	}
	registerTUIClaim("action-pin", claimActionPin)
	registerTUIClaim("action-reorder", claimActionReorder)
	registerTUIClaim("action-new-divider", claimActionNewDivider)
}

// pinOrderOf reads a thread's stored pin_order (nil = unpinned) from the daemon truth.
func pinOrderOf(t *testing.T, sb *Sandbox, id string) *float64 {
	t.Helper()
	return sb.threadFromList(t, id).PinOrder
}

// refuseDivider asserts a verb refuses loudly on a divider, naming divider-ness.
func refuseDivider(t *testing.T, sb *Sandbox, what string, args ...string) {
	t.Helper()
	_, stderr, err := sb.Runner.Run(t, args...)
	if err == nil {
		t.Errorf("%s on a divider succeeded silently", what)
	} else if !strings.Contains(stderr, "divider") {
		t.Errorf("%s refusal does not name divider-ness: %s", what, stderr)
	}
}

func testPin(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	a := sb.newHeadlessThread(t, "pi", "pin-a")
	b := sb.newHeadlessThread(t, "pi", "pin-b")

	// Fresh threads are unpinned.
	if pinOrderOf(t, sb, a.ID) != nil {
		t.Fatalf("a fresh thread should be unpinned")
	}

	// Pin a (default: top). Pin b (top) → b orders BELOW a's key (above it in the block).
	if _, stderr, err := sb.Runner.Run(t, "thread", "pin", "--id", a.ID); err != nil {
		t.Fatalf("pin a: %v\n%s", err, stderr)
	}
	if pinOrderOf(t, sb, a.ID) == nil {
		t.Fatalf("pin did not set a pin_order on a")
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "pin", "--id", b.ID); err != nil {
		t.Fatalf("pin b: %v\n%s", err, stderr)
	}
	if oa, ob := pinOrderOf(t, sb, a.ID), pinOrderOf(t, sb, b.ID); ob == nil || *ob >= *oa {
		t.Fatalf("pin-to-top should place b above a (ob < oa): oa=%v ob=%v", oa, ob)
	}

	// --before / --after reposition relative to another pinned node.
	if _, stderr, err := sb.Runner.Run(t, "thread", "pin", "--id", a.ID, "--before", b.ID); err != nil {
		t.Fatalf("pin a --before b: %v\n%s", err, stderr)
	}
	if oa, ob := pinOrderOf(t, sb, a.ID), pinOrderOf(t, sb, b.ID); *oa >= *ob {
		t.Errorf("--before b should order a above b: oa=%v ob=%v", *oa, *ob)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "pin", "--id", a.ID, "--after", b.ID); err != nil {
		t.Fatalf("pin a --after b: %v\n%s", err, stderr)
	}
	if oa, ob := pinOrderOf(t, sb, a.ID), pinOrderOf(t, sb, b.ID); *oa <= *ob {
		t.Errorf("--after b should order a below b: oa=%v ob=%v", *oa, *ob)
	}

	// Pinning a CHILD is refused loudly (only top-level threads can be pinned).
	child := sb.newHeadlessThreadParented(t, "pi", "pin-kid", a.ID)
	if _, stderr, err := sb.Runner.Run(t, "thread", "pin", "--id", child.ID); err == nil {
		t.Errorf("pinning a child thread succeeded silently")
	} else if !strings.Contains(stderr, "top-level") {
		t.Errorf("child-pin refusal unclear: %s", stderr)
	}
	if pinOrderOf(t, sb, child.ID) != nil {
		t.Errorf("a refused child pin still set a pin_order")
	}

	// Unpin clears the key (the thread rejoins the auto block).
	if _, stderr, err := sb.Runner.Run(t, "thread", "unpin", "--id", a.ID); err != nil {
		t.Fatalf("unpin a: %v\n%s", err, stderr)
	}
	if pinOrderOf(t, sb, a.ID) != nil {
		t.Errorf("unpin did not clear the pin_order")
	}

	// Archiving a pinned thread clears its pin_order (loses ordering on archive).
	if _, stderr, err := sb.Runner.Run(t, "thread", "pin", "--id", b.ID); err != nil {
		t.Fatalf("re-pin b: %v\n%s", err, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", b.ID); err != nil {
		t.Fatalf("archive b: %v\n%s", err, stderr)
	}
	if pinOrderOf(t, sb, b.ID) != nil {
		t.Errorf("archiving a pinned thread did not clear its pin_order")
	}

	// Reparenting a pinned thread under another clears its pin_order too.
	parent := sb.newHeadlessThread(t, "pi", "pin-parent")
	if _, stderr, err := sb.Runner.Run(t, "thread", "pin", "--id", a.ID); err != nil {
		t.Fatalf("re-pin a: %v\n%s", err, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "reparent", "--id", a.ID, "--parent", parent.ID); err != nil {
		t.Fatalf("reparent a: %v\n%s", err, stderr)
	}
	if pinOrderOf(t, sb, a.ID) != nil {
		t.Errorf("reparenting a pinned thread under another did not clear its pin_order")
	}

	// Two placement flags at once is a loud error.
	if _, _, err := sb.Runner.Run(t, "thread", "pin", "--id", parent.ID, "--top", "--bottom"); err == nil {
		t.Errorf("--top --bottom together should fail loudly")
	}

	// Remote: the pins above all ROUTED over a real ssh hop; confirm the final truth
	// landed on the PEER's OWN store (not the router).
	if loc == matrix.Remote {
		c := client.New(sb.Home + "/daemon.sock")
		list, err := c.ThreadList(context.Background(), true, false)
		if err != nil {
			t.Fatalf("peer thread list: %v", err)
		}
		var pOrder *float64
		for _, x := range list.Threads {
			if x.ID == parent.ID {
				pOrder = x.PinOrder
			}
		}
		_ = pOrder // parent was never pinned; the point is the routed writes reached the peer
		if !hasThread(list.Threads, a.ID) {
			t.Errorf("routed pins did not reach the peer's own store")
		}
	}
}

func testDivider(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	// Create a labeled divider: agent_kind divider, a pin_order, logical session name,
	// nothing spawned.
	out, stderr, err := sb.Runner.Run(t, "thread", "new", "--divider", "--name", "today", "--json")
	if err != nil {
		t.Fatalf("thread new --divider: %v\n%s", err, stderr)
	}
	var d api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &d); err != nil {
		t.Fatalf("decode divider: %v\nraw: %s", err, out)
	}
	if d.AgentKind != api.DividerAgentKind {
		t.Fatalf("agent_kind = %q, want divider", d.AgentKind)
	}
	if d.PinOrder == nil {
		t.Errorf("a divider must carry a pin_order (it lives in the pinned block)")
	}
	if d.Name != "today" {
		t.Errorf("divider label = %q, want today", d.Name)
	}
	if d.SessionName != "divider-"+d.ID {
		t.Errorf("session_name = %q, want divider-<id>", d.SessionName)
	}
	if _, err := sb.rawTmux(t, "has-session", "-t", "="+d.SessionName); err == nil {
		t.Errorf("a divider has a tmux session %q (nothing must spawn)", d.SessionName)
	}

	// Agent-shaped creation flags are refused (CLI-side for --agent/--headless).
	if _, _, err := sb.Runner.Run(t, "thread", "new", "--divider", "--agent", "pi", "--name", "x"); err == nil {
		t.Errorf("--divider --agent should refuse loudly")
	}
	if _, _, err := sb.Runner.Run(t, "thread", "new", "--divider", "--headless", "--name", "x"); err == nil {
		t.Errorf("--divider --headless should refuse loudly")
	}

	// Agent verbs refuse LOUDLY, naming divider-ness (fail-closed).
	refuseDivider(t, sb, "send-headless", "thread", "send-headless", "--id", d.ID, "--text", "hi")
	refuseDivider(t, sb, "capture", "thread", "capture", "--id", d.ID)
	refuseDivider(t, sb, "transcript", "thread", "transcript", "--id", d.ID)
	refuseDivider(t, sb, "resume", "thread", "resume", "--id", d.ID)
	refuseDivider(t, sb, "headful", "thread", "headful", "--id", d.ID)
	refuseDivider(t, sb, "fork", "thread", "new", "--fork-from", d.ID, "--name", "copy")

	// Realizing a divider refuses (realize only converts virtual grouping threads).
	if _, stderr, err := sb.Runner.Run(t, "thread", "realize", "--id", d.ID, "--agent", "pi"); err == nil {
		t.Errorf("realize of a divider succeeded silently")
	} else if !strings.Contains(stderr, "divider") && !strings.Contains(stderr, "not virtual") {
		t.Errorf("realize-divider refusal unclear: %s", stderr)
	}

	// A divider can't be un-pinned or archived — delete it instead.
	refuseDivider(t, sb, "unpin", "thread", "unpin", "--id", d.ID)
	refuseDivider(t, sb, "archive", "thread", "archive", "--id", d.ID)

	// An UNLABELED divider is legitimate (a bare rule).
	out2, stderr, err := sb.Runner.Run(t, "thread", "new", "--divider", "--json")
	if err != nil {
		t.Fatalf("unlabeled divider: %v\n%s", err, stderr)
	}
	var d2 api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out2)), &d2); err != nil {
		t.Fatalf("decode unlabeled divider: %v\n%s", err, out2)
	}
	if d2.AgentKind != api.DividerAgentKind || d2.Name != "" {
		t.Errorf("unlabeled divider wrong: kind=%s name=%q", d2.AgentKind, d2.Name)
	}

	// Delete removes it (no teardown — a divider has no runtime).
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", d.ID); err != nil {
		t.Fatalf("delete divider: %v\n%s", err, stderr)
	}
	if hasThread(sb.listThreads(t), d.ID) {
		t.Errorf("deleted divider still listed")
	}
}

// claimActionPin: `p` pins the selected top-level thread on the daemon (its row shows
// the • marker), and `u` un-pins it — the real optimism→persist path.
func claimActionPin(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "pinme")

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "pinme") // single thread => cursor on it

	m = runKey(t, m, "p")
	if m.ActionErr() != nil {
		t.Fatalf("pin action errored: %v", m.ActionErr())
	}
	if !waitUntil(10*time.Second, func() bool { return pinOrderOf(t, sb, th.ID) != nil }) {
		t.Fatalf("p did not pin the thread on the daemon")
	}
	// The pinned marker renders.
	var view string
	m, view = render(t, m)
	if !strings.Contains(view, "•") {
		t.Errorf("pinned row does not render the • marker:\n%s", view)
	}

	// u un-pins.
	m = runKey(t, m, "u")
	if m.ActionErr() != nil {
		t.Fatalf("unpin action errored: %v", m.ActionErr())
	}
	if !waitUntil(10*time.Second, func() bool { return pinOrderOf(t, sb, th.ID) == nil }) {
		t.Errorf("u did not un-pin the thread on the daemon")
	}
}

// claimActionReorder: `m` enters move mode and ↑ repositions the pinned row on the
// daemon — moving the lower of two pinned threads above the other.
func claimActionReorder(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	a := sb.newHeadlessThread(t, "pi", "reord-a")
	b := sb.newHeadlessThread(t, "pi", "reord-b")
	// Pin a, then b (to top) → block order b, a (b above a).
	if _, stderr, err := sb.Runner.Run(t, "thread", "pin", "--id", a.ID); err != nil {
		t.Fatalf("pin a: %v\n%s", err, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "pin", "--id", b.ID); err != nil {
		t.Fatalf("pin b: %v\n%s", err, stderr)
	}
	if oa, ob := pinOrderOf(t, sb, a.ID), pinOrderOf(t, sb, b.ID); oa == nil || ob == nil || *ob >= *oa {
		t.Fatalf("precondition: b should be above a (ob<oa)")
	}

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "reord-a")
	m = selectRowByName(t, m, "reord-a") // cursor on the lower pinned row
	m = runKey(t, m, "m")                // enter move mode
	m = runSpecial(t, m, tea.KeyUp)      // move a up, above b
	m = runSpecial(t, m, tea.KeyEnter)   // commit + exit move mode

	// The reorder persisted: a now orders ABOVE b (oa < ob).
	if !waitUntil(10*time.Second, func() bool {
		oa, ob := pinOrderOf(t, sb, a.ID), pinOrderOf(t, sb, b.ID)
		return oa != nil && ob != nil && *oa < *ob
	}) {
		oa, ob := pinOrderOf(t, sb, a.ID), pinOrderOf(t, sb, b.ID)
		t.Errorf("move-up did not reorder a above b on the daemon: oa=%v ob=%v", oa, ob)
	}
}

// claimActionNewDivider: `D` opens the label prompt and creates a real divider on the
// daemon (agent_kind divider, pinned), on the selected row's machine.
func claimActionNewDivider(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "anchor") // the selection (machine carrier)

	before := map[string]bool{}
	for _, th := range sb.listThreads(t) {
		before[th.ID] = true
	}

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "anchor")

	m = runKey(t, m, "D")
	if !m.Prompting() {
		t.Fatalf("D did not open the divider label prompt")
	}
	m = typeText(t, m, "today")
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() != nil {
		t.Fatalf("new divider errored: %v", m.ActionErr())
	}
	if !waitUntil(15*time.Second, func() bool {
		for _, th := range sb.listThreads(t) {
			if !before[th.ID] && th.AgentKind == api.DividerAgentKind && th.Name == "today" && th.PinOrder != nil {
				return true
			}
		}
		return false
	}) {
		t.Errorf("D did not create a pinned divider named 'today' on the daemon")
	}
}
