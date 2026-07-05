package conformance

// thread.virtual + thread.realize cells.
//
// thread.virtual: `thread new --virtual` records a pure grouping node — no
// agent, no pane, no transcript. The honest external effects proven here:
// the record exists with agent_kind "virtual" and NO tmux session; grouping
// machinery works on it (children parent under it, reparent, hold INHERITANCE
// flows through it); every agent verb refuses LOUDLY (send-headless, headful,
// capture, transcript, fork — the fail-closed contract); and deleting the
// group PROMOTES its children to the deleted node's parent (no dangling parent
// ids). Remote shares the same body — every verb routes over a real ssh hop.
//
// thread.realize: converting a virtual thread in place must produce a REAL
// conversation, so the proof is a real agent turn on the realized thread
// (+ a continuity turn locally). Local proves the --cwd override and the
// missing-cwd refusal; remote proves the stored-cwd default over routing.

import (
	"context"
	"encoding/json"
	"strconv"
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
		matrix.RegisterTest("thread.virtual", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testVirtual(t, loc) })
	}
	for _, agent := range matrix.AllAgents {
		a := agent
		matrix.RegisterTest("thread.realize", a, matrix.Local,
			func(t *testing.T) { testRealizeLocal(t, string(a)) })
		matrix.RegisterTest("thread.realize", a, matrix.Remote,
			func(t *testing.T) { testRealizeRemote(t, string(a)) })
	}
	registerTUIClaim("action-virtual-enter", claimActionVirtualEnter)
	registerTUIClaim("action-new-virtual", claimActionNewVirtual)
}

// newVirtualThread creates a virtual grouping thread via the CLI (routed over
// ssh for a remote sandbox) and returns the decoded record.
func (sb *Sandbox) newVirtualThread(t *testing.T, name string, extra ...string) api.Thread {
	t.Helper()
	args := append([]string{"thread", "new", "--virtual", "--name", name, "--json"}, extra...)
	stdout, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("thread new --virtual: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &th); err != nil {
		t.Fatalf("decode virtual thread: %v\nraw: %s", err, stdout)
	}
	return th
}

// refuseVirtual asserts a verb refuses loudly on a virtual thread, naming
// virtualness (never a silent no-op or an opaque failure).
func refuseVirtual(t *testing.T, sb *Sandbox, what string, args ...string) {
	t.Helper()
	_, stderr, err := sb.Runner.Run(t, args...)
	if err == nil {
		t.Errorf("%s on a virtual thread succeeded silently", what)
	} else if !strings.Contains(stderr, "virtual") {
		t.Errorf("%s refusal does not name virtualness: %s", what, stderr)
	}
}

func testVirtual(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	// Create: agent_kind virtual, no cwd required, logical session name, and
	// NOTHING spawned (no tmux session), resolving headless·idle.
	v := sb.newVirtualThread(t, "group")
	if v.AgentKind != api.VirtualAgentKind {
		t.Fatalf("agent_kind = %q, want virtual", v.AgentKind)
	}
	if v.Cwd != "" {
		t.Errorf("virtual thread stored a cwd it was never given: %q", v.Cwd)
	}
	if v.SessionName != "virtual-"+v.ID {
		t.Errorf("session_name = %q, want virtual-<id>", v.SessionName)
	}
	if _, err := sb.rawTmux(t, "has-session", "-t", "="+v.SessionName); err == nil {
		t.Errorf("a virtual thread has a tmux session %q (nothing must spawn)", v.SessionName)
	}
	if st := sb.threadStatus(t, v.ID); st.Head != api.Headless || st.Busy != api.BusyIdle {
		t.Errorf("virtual state = %s/%s, want headless/idle", st.Head, st.Busy)
	}

	// Creation conflicts are loud: --virtual is mutually exclusive with every
	// agent-shaped flag (CLI-side for --agent, daemon-side for the rest).
	if _, _, err := sb.Runner.Run(t, "thread", "new", "--virtual", "--agent", "pi", "--name", "x"); err == nil {
		t.Errorf("--virtual --agent should refuse loudly")
	}
	if _, _, err := sb.Runner.Run(t, "thread", "new", "--virtual", "--headless", "--name", "x"); err == nil {
		t.Errorf("--virtual --headless should refuse loudly")
	}

	// Grouping machinery applies unchanged: a child born under it, a sibling
	// reparented under it.
	child := sb.newHeadlessThreadParented(t, "pi", "kid", v.ID)
	solo := sb.newHeadlessThread(t, "pi", "solo")
	if _, stderr, err := sb.Runner.Run(t, "thread", "reparent", "--id", solo.ID, "--parent", v.ID); err != nil {
		t.Fatalf("reparent under virtual: %v\n%s", err, stderr)
	}
	if got := sb.threadFromList(t, solo.ID).Parent; got != v.ID {
		t.Fatalf("reparent under virtual did not stick: parent=%q", got)
	}

	// Hold INHERITANCE flows through a virtual parent (H26 derives over records,
	// agentless or not): hold the group → the child reads on_hold with the
	// group's deadline as its effective one. Read from the OWNING daemon's own
	// socket (remote sandboxes expose it locally — ssh-localhost peer).
	c := client.New(sb.Home + "/daemon.sock")
	future := time.Now().Add(48 * time.Hour).Unix()
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", v.ID, "--until-unix", strconv.FormatInt(future, 10)); err != nil {
		t.Fatalf("hold virtual parent: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool {
		r, ok := snapRowOnHold(t, c, child.ID)
		return ok && r.OnHold && r.OnHoldEffectiveUnix == future && r.OnHoldUntilUnix == 0
	}) {
		r, _ := snapRowOnHold(t, c, child.ID)
		t.Errorf("child did not inherit the virtual parent's hold: on_hold=%v eff=%d own=%d", r.OnHold, r.OnHoldEffectiveUnix, r.OnHoldUntilUnix)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "hold", "--id", v.ID, "--clear"); err != nil {
		t.Fatalf("clear hold: %v\n%s", err, stderr)
	}

	// Agent verbs refuse LOUDLY, naming virtualness (fail-closed, actionable).
	refuseVirtual(t, sb, "send-headless", "thread", "send-headless", "--id", v.ID, "--text", "hi")
	refuseVirtual(t, sb, "headful", "thread", "headful", "--id", v.ID)
	refuseVirtual(t, sb, "resume", "thread", "resume", "--id", v.ID)
	refuseVirtual(t, sb, "capture", "thread", "capture", "--id", v.ID)
	refuseVirtual(t, sb, "transcript", "thread", "transcript", "--id", v.ID)
	refuseVirtual(t, sb, "fork", "thread", "new", "--fork-from", v.ID, "--name", "copy")
	// Realizing a NON-virtual thread is the mirror refusal.
	if _, stderr, err := sb.Runner.Run(t, "thread", "realize", "--id", child.ID, "--agent", "pi"); err == nil {
		t.Errorf("realize of a non-virtual thread succeeded silently")
	} else if !strings.Contains(stderr, "not virtual") {
		t.Errorf("realize refusal unclear: %s", stderr)
	}

	// Deleting the group PROMOTES its children to the group's own parent
	// (root here) — records intact, parent ids never dangle.
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", v.ID); err != nil {
		t.Fatalf("delete virtual parent: %v\n%s", err, stderr)
	}
	if hasThread(sb.listThreads(t), v.ID) {
		t.Errorf("deleted virtual thread still listed")
	}
	for _, kid := range []api.Thread{child, solo} {
		if got := sb.threadFromList(t, kid.ID).Parent; got != "" {
			t.Errorf("child %s not promoted to root after group delete: parent=%q", kid.Name, got)
		}
	}
}

func testRealizeLocal(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	// A virtual group with structure hanging off it: a tag and a child. All of
	// it must survive the conversion (realize is IN PLACE — same id).
	v := sb.newVirtualThread(t, "grp") // deliberately NO cwd
	if _, stderr, err := sb.Runner.Run(t, "thread", "tag", "--id", v.ID, "--add", "keepme"); err != nil {
		t.Fatalf("tag: %v\n%s", err, stderr)
	}
	child := sb.newHeadlessThreadParented(t, "pi", "kid", v.ID)

	// No cwd stored and none given → loud refusal, not a made-up default.
	if _, stderr, err := sb.Runner.Run(t, "thread", "realize", "--id", v.ID, "--agent", agent); err == nil {
		t.Errorf("realize without any cwd succeeded silently")
	} else if !strings.Contains(stderr, "cwd") {
		t.Errorf("missing-cwd refusal unclear: %s", stderr)
	}
	// Negatives that need no agent: run once (claude branch) to keep cells lean.
	if agent == "claude" {
		if _, _, err := sb.Runner.Run(t, "thread", "realize", "--id", v.ID, "--agent", "virtual"); err == nil {
			t.Errorf("realize --agent virtual succeeded silently")
		}
		if _, _, err := sb.Runner.Run(t, "thread", "realize", "--id", "zzzzzzzz", "--agent", "claude", "--cwd", "/tmp"); err == nil {
			t.Errorf("realize of an unknown id succeeded silently")
		}
	}

	// Realize with an explicit --cwd: the record converts in place to exactly
	// the never-started-headless shape.
	out, stderr, err := sb.Runner.Run(t, "thread", "realize", "--id", v.ID, "--agent", agent, "--cwd", "/tmp", "--json")
	if err != nil {
		t.Fatalf("realize: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatalf("decode realize: %v\nraw: %s", err, out)
	}
	if th.ID != v.ID {
		t.Fatalf("realize changed the thread id: %s -> %s", v.ID, th.ID)
	}
	if th.AgentKind != agent || th.Cwd != "/tmp" || th.SessionName != "headless-"+v.ID {
		t.Errorf("realized record wrong: kind=%s cwd=%s session=%s", th.AgentKind, th.Cwd, th.SessionName)
	}
	if th.HeadlessStarted {
		t.Errorf("realized thread must be never-started (the first turn creates the conversation)")
	}
	switch agent {
	case "codex":
		if th.AgentSessionID != "" {
			t.Errorf("codex must mint its own session id on the first turn, got pre-set %q", th.AgentSessionID)
		}
	default: // pi, claude: sesh pre-assigns
		if th.AgentSessionID == "" {
			t.Errorf("[%s] realize did not pre-mint an agent session id", agent)
		}
	}
	if tags := sb.threadFromList(t, v.ID).Tags; !contains(tags, "keepme") {
		t.Errorf("tags lost across realize: %v", tags)
	}
	if got := sb.threadFromList(t, child.ID).Parent; got != v.ID {
		t.Errorf("child lost its parent across realize: %q", got)
	}

	// The REAL proof: a first turn runs on the realized thread, and a second
	// turn recalls it — a durable, resumable conversation, not just a record.
	codeword := "REALIZED_" + strings.ToUpper(agent)
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", v.ID, "--text",
		"Remember this codeword: "+codeword+". Just reply: ok."); err != nil {
		t.Fatalf("[%s] first turn on realized thread: %v\n%s", agent, err, stderr)
	}
	if !waitUntil(120*time.Second, func() bool {
		r := sb.headlessReply(t, v.ID)
		return !r.Working && r.HaveReply
	}) {
		t.Fatalf("[%s] first turn never completed", agent)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", v.ID, "--text",
		"What was the codeword I told you earlier? Reply with ONLY the codeword."); err != nil {
		t.Fatalf("[%s] continuity turn: %v\n%s", agent, err, stderr)
	}
	if !waitUntil(120*time.Second, func() bool {
		r := sb.headlessReply(t, v.ID)
		return !r.Working && r.HaveReply && strings.Contains(r.Reply, codeword)
	}) {
		r := sb.headlessReply(t, v.ID)
		t.Fatalf("[%s] realized thread is not a continuous conversation: reply %q lacks %q", agent, r.Reply, codeword)
	}

	// A realized thread is no longer virtual — a second realize refuses.
	if _, stderr, err := sb.Runner.Run(t, "thread", "realize", "--id", v.ID, "--agent", agent, "--cwd", "/tmp"); err == nil {
		t.Errorf("re-realize succeeded silently")
	} else if !strings.Contains(stderr, "not virtual") {
		t.Errorf("re-realize refusal unclear: %s", stderr)
	}
}

func testRealizeRemote(t *testing.T, agent string) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Remote)
	sb.startDaemon(t)

	// Created WITH a stored cwd; realize gives none → the stored one is the
	// default (the other cwd branch from the local cell). Every verb here
	// routes to the owner over a real ssh hop.
	v := sb.newVirtualThread(t, "rgrp", "--cwd", "/tmp")
	out, stderr, err := sb.Runner.Run(t, "thread", "realize", "--id", v.ID, "--agent", agent, "--json")
	if err != nil {
		t.Fatalf("routed realize: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatalf("decode realize: %v\nraw: %s", err, out)
	}
	if th.AgentKind != agent || th.Cwd != "/tmp" {
		t.Errorf("routed realize record wrong: kind=%s cwd=%s (want %s, /tmp from creation)", th.AgentKind, th.Cwd, agent)
	}
	// The record converted on the PEER (its own daemon socket, not the router).
	c := client.New(sb.Home + "/daemon.sock")
	list, err := c.ThreadList(context.Background(), false, false)
	if err != nil || !func() bool {
		for _, x := range list.Threads {
			if x.ID == v.ID && x.AgentKind == agent {
				return true
			}
		}
		return false
	}() {
		t.Fatalf("realize did not land on the owning peer (err=%v)", err)
	}

	// One REAL turn on the realized thread, routed: the conversation actually
	// runs on the peer.
	marker := "RROUTED_" + strings.ToUpper(agent)
	if _, stderr, err := sb.Runner.Run(t, "thread", "send-headless", "--id", v.ID, "--text",
		"Reply with exactly this marker and nothing else: "+marker); err != nil {
		t.Fatalf("[%s] routed turn on realized thread: %v\n%s", agent, err, stderr)
	}
	if !waitUntil(120*time.Second, func() bool {
		r := sb.headlessReply(t, v.ID)
		return !r.Working && r.HaveReply && strings.Contains(r.Reply, marker)
	}) {
		r := sb.headlessReply(t, v.ID)
		t.Fatalf("[%s] routed realized thread never conversed: working=%v have=%v reply=%q", agent, r.Working, r.HaveReply, r.Reply)
	}
}

// claimActionVirtualEnter: Enter on a VIRTUAL row is a loud persistent warning
// (Lukas's decision: warn, don't fold) — the TUI stays open, nothing shells
// out, the record is untouched; the row renders the ◇ glyph; f (fork) refuses
// with a clear message too.
func claimActionVirtualEnter(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	v := sb.newVirtualThread(t, "vgroup")

	bin := seshBin(t)
	env := []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}
	m := tui.New(sb.Home+"/daemon.sock", false).WithExec(bin, env).WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "vgroup")

	// The virtual glyph is on screen.
	var view string
	m, view = render(t, m)
	if !strings.Contains(view, "◇") {
		t.Errorf("virtual row does not render the ◇ glyph:\n%s", view)
	}

	// Enter: loud warning naming virtual + realize; TUI still running.
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() == nil {
		t.Fatalf("Enter on a virtual row did not warn")
	}
	if msg := m.ActionErr().Error(); !strings.Contains(msg, "virtual") || !strings.Contains(msg, "realize") {
		t.Errorf("warning should name virtual + realize: %q", msg)
	}
	// The warning PERSISTS across a render/refetch (actionErr semantics, H2).
	m, _ = render(t, m)
	if m.ActionErr() == nil {
		t.Errorf("virtual-enter warning was cleared by a reconcile fetch")
	}
	// Nothing happened on the daemon: still virtual, still no session.
	if got := sb.getThread(t, v.ID).AgentKind; got != api.VirtualAgentKind {
		t.Fatalf("Enter mutated the record: agent_kind=%q", got)
	}
	if _, err := sb.rawTmux(t, "has-session", "-t", "="+v.SessionName); err == nil {
		t.Errorf("Enter on a virtual row spawned a tmux session")
	}

	// f: fork refuses client-side with a clear message.
	m = runKey(t, m, "f")
	if m.ActionErr() == nil || !strings.Contains(m.ActionErr().Error(), "virtual") {
		t.Errorf("fork on a virtual row should warn about virtualness, got %v", m.ActionErr())
	}
}

// claimActionNewVirtual: `v` opens a name prompt and creates a NEW root virtual
// grouping thread on the daemon (option 1 of the design: create the group, then
// `P` children under it) — root even though parent inference would apply (the
// exec'd `thread new` passes --no-parent), and the cursor preselects the new
// row. An empty submit cancels and creates nothing.
func claimActionNewVirtual(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	sb.newHeadlessThread(t, "pi", "anchor") // the selection (the machine carrier)

	before := map[string]bool{}
	for _, th := range sb.listThreads(t) {
		before[th.ID] = true
	}

	m := tui.New(sb.Home+"/daemon.sock", false).
		WithExec(seshBin(t), []string{"SESH_HOME=" + sb.Home, "SESH_MACHINE=" + sb.Machine}).
		WithLocal(sb.Machine, sb.TmuxSocket)
	m, _ = renderUntilRow(t, m, "anchor")

	// Empty submit cancels: no new record.
	m = runKey(t, m, "v")
	if !m.Prompting() {
		t.Fatalf("v did not open the name prompt")
	}
	m = runSpecial(t, m, tea.KeyEnter)
	for _, th := range sb.listThreads(t) {
		if !before[th.ID] {
			t.Fatalf("empty submit created a thread: %q", th.Name)
		}
	}

	// Named submit creates a ROOT virtual thread on the daemon.
	m = runKey(t, m, "v")
	m = typeText(t, m, "grp x")
	m = runSpecial(t, m, tea.KeyEnter)
	if m.ActionErr() != nil {
		t.Fatalf("new virtual group errored: %v", m.ActionErr())
	}
	var created api.Thread
	for _, th := range sb.listThreads(t) {
		if before[th.ID] {
			continue
		}
		created = th
	}
	if created.ID == "" {
		t.Fatalf("v did not create a thread")
	}
	if created.AgentKind != api.VirtualAgentKind || created.Name != "grp x" || created.Parent != "" {
		t.Fatalf("created record wrong: kind=%s name=%q parent=%q (want virtual/grp x/root)",
			created.AgentKind, created.Name, created.Parent)
	}
	// The cursor preselects the new group once the refetch brings it in.
	if !waitUntil(15*time.Second, func() bool {
		var v string
		m, v = render(t, m)
		_ = v
		row, ok := m.Selected()
		return ok && row.ID == created.ID
	}) {
		row, _ := m.Selected()
		t.Errorf("cursor did not land on the new group (selected %q)", row.Name)
	}
}
