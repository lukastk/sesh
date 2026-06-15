package conformance

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("ticket.create", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTicketCreate(t, loc) })
		matrix.RegisterTest("ticket.set-status", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTicketSetStatus(t, loc) })
		matrix.RegisterTest("ticket.list-by-thread", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTicketListByThread(t, loc) })
		matrix.RegisterTest("ticket.needs-input", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTicketNeedsInput(t, loc) })
		matrix.RegisterTest("ticket.get", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTicketGet(t, loc) })
		matrix.RegisterTest("ticket.set", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTicketSet(t, loc) })
		matrix.RegisterTest("ticket.unbind", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTicketUnbind(t, loc) })
		// ticket.send-prompt touches the agent send path; all three agents now
		// spawn deterministically (codex cwd pre-trusted at spawn).
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("ticket.send-prompt", a, loc,
				func(t *testing.T) { testTicketSendPrompt(t, string(a), loc) })
		}
	}
	matrix.RegisterTest("ticket.ownership", matrix.AgentAgnostic, matrix.Remote, testTicketOwnership)
	// ticket.move is the cross-machine relocate (get|import|delete over a real ssh
	// hop) — the capability behind "bind a ticket to a thread on any machine".
	matrix.RegisterTest("ticket.move", matrix.AgentAgnostic, matrix.Remote, testTicketMove)
	// list --current auto-detects the calling pane's thread; the inference is a
	// local-pane concept (Local only — routing is covered by route.parity).
	matrix.RegisterTest("ticket.list-current", matrix.AgentAgnostic, matrix.Local, testTicketListCurrent)
}

// testTicketGet asserts `ticket get` returns the full record and that --field
// prints a single field RAW (the clipboard/agent path) — and that there is no
// description field anymore.
func testTicketGet(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	id := sb.ticketCreate(t, "investigate flake", "the retry test is flaky under load")
	stdout, stderr, err := sb.Runner.Run(t, "ticket", "get", "--id", id, "--json")
	if err != nil {
		t.Fatalf("ticket get: %v\n%s", err, stderr)
	}
	var tk api.Ticket
	if err := json.Unmarshal([]byte(stdout), &tk); err != nil {
		t.Fatalf("decode get: %v\nraw: %s", err, stdout)
	}
	if tk.ID != id || tk.Name != "investigate flake" || tk.Prompt != "the retry test is flaky under load" {
		t.Errorf("get returned wrong record: %+v", tk)
	}
	// --field prompt prints the raw prompt (no JSON, no extra newline) — exactly
	// what the clipboard command pipes.
	stdout, stderr, err = sb.Runner.Run(t, "ticket", "get", "--id", id, "--field", "prompt")
	if err != nil {
		t.Fatalf("ticket get --field prompt: %v\n%s", err, stderr)
	}
	if stdout != "the retry test is flaky under load" {
		t.Errorf("get --field prompt = %q, want the raw prompt with no decoration", stdout)
	}
	// An unknown field is loud.
	if _, _, err := sb.Runner.Run(t, "ticket", "get", "--id", id, "--field", "description"); err == nil {
		t.Errorf("get --field description was accepted (description is gone)")
	}
}

// testTicketSet asserts a partial text-field update: only the flags passed are
// applied (name/prompt independently), and an empty update is a loud no-op.
func testTicketSet(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	id := sb.ticketCreate(t, "old name", "old prompt")
	// Set only the name — the prompt must be untouched.
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set", "--id", id, "--name", "new name"); err != nil {
		t.Fatalf("set --name: %v\n%s", err, stderr)
	}
	if tk := sb.ticketByID(t, id); tk.Name != "new name" || tk.Prompt != "old prompt" {
		t.Errorf("after set --name: %+v (prompt must be unchanged)", tk)
	}
	// Set only the prompt — the (new) name must be untouched.
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set", "--id", id, "--prompt", "new prompt"); err != nil {
		t.Fatalf("set --prompt: %v\n%s", err, stderr)
	}
	if tk := sb.ticketByID(t, id); tk.Name != "new name" || tk.Prompt != "new prompt" {
		t.Errorf("after set --prompt: %+v (name must be unchanged)", tk)
	}
	// An empty update is loud (nothing to change).
	if _, _, err := sb.Runner.Run(t, "ticket", "set", "--id", id); err == nil {
		t.Errorf("ticket set with no fields was accepted")
	}
}

// testTicketListCurrent asserts `ticket list --current` resolves the calling
// thread (via $SESH_THREAD_ID here) and returns exactly its tickets.
func testTicketListCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	th := sb.newThread(t, "pi", "cur", "/tmp")
	bound := sb.ticketCreate(t, "mine", "")
	_ = sb.ticketCreate(t, "someone else's", "") // unbound; must NOT appear
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", bound, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind active: %v\n%s", err, stderr)
	}

	// Inference path: a runner whose env carries SESH_THREAD_ID = the thread, as a
	// spawned pane/headless turn would (resolveThreadID validates it against the daemon).
	lr := sb.Runner.(*localRunner)
	env := map[string]string{"SESH_THREAD_ID": th.ID}
	for k, v := range lr.env {
		env[k] = v
	}
	cur := &localRunner{bin: lr.bin, env: env}
	stdout, stderr, err := cur.Run(t, "ticket", "list", "--current", "--json")
	if err != nil {
		t.Fatalf("ticket list --current: %v\n%s", err, stderr)
	}
	var got []api.Ticket
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var tk api.Ticket
		if err := json.Unmarshal([]byte(line), &tk); err != nil {
			t.Fatalf("decode: %v\nline: %s", err, line)
		}
		got = append(got, tk)
	}
	if len(got) != 1 || got[0].ID != bound {
		t.Fatalf("list --current = %v, want only the bound ticket %s", ticketIDs(got), bound)
	}
}

// testTicketUnbind asserts `ticket unbind` detaches a ticket from its thread:
// thread_id is cleared and an `active` ticket downgrades to `ready` (unattached).
func testTicketUnbind(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, "pi", "ub", "/tmp")
	id := sb.ticketCreate(t, "detach me", "the work")
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind active: %v\n%s", err, stderr)
	}
	if tk := sb.ticketByID(t, id); tk.Status != api.StatusActive || tk.ThreadID != th.ID {
		t.Fatalf("pre-unbind state wrong: %+v", tk)
	}

	// Unbind: thread cleared, active -> ready, the text fields untouched.
	if _, stderr, err := sb.Runner.Run(t, "ticket", "unbind", "--id", id); err != nil {
		t.Fatalf("unbind: %v\n%s", err, stderr)
	}
	tk := sb.ticketByID(t, id)
	if tk.ThreadID != "" {
		t.Errorf("after unbind thread_id = %q, want empty", tk.ThreadID)
	}
	if tk.Status != api.StatusReady {
		t.Errorf("after unbind status = %q, want ready", tk.Status)
	}
	if tk.Name != "detach me" || tk.Prompt != "the work" {
		t.Errorf("unbind altered text fields: %+v", tk)
	}
	// Unknown id is loud (no silent success).
	if _, _, err := sb.Runner.Run(t, "ticket", "unbind", "--id", "00000000-dead-beef-0000-000000000000"); err == nil {
		t.Errorf("unbind of an unknown id was accepted")
	}
}

// testTicketMove is the cross-machine relocation behind "bind a ticket to a thread
// on ANY machine". A ticket↔thread live join only works co-located, so binding a
// ticket on machine A to a thread on machine B RELOCATES the ticket to B:
// `ticket get --json | ticket import --machine B` (preserving the id, dropping the
// binding, active→ready), then delete on A, then bind on B. Real ssh hops both ways.
func testTicketMove(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)

	src := newSandbox(t, matrix.Local) // machine A (the ticket's origin)
	src.startDaemon(t)
	dst := newSandbox(t, matrix.Local) // machine B (where the target thread lives)
	dst.startDaemon(t)

	// A client that peers with BOTH daemons — the cockpit machine doing the move.
	bin := seshBin(t)
	client := &localRunner{bin: bin, env: map[string]string{
		"SESH_HOME":    t.TempDir(),
		"SESH_MACHINE": "mover-client",
	}}
	for _, p := range []*Sandbox{src, dst} {
		if _, stderr, err := client.Run(t, "peer", "add", "--machine", p.Machine, "--ssh", "localhost", "--home", p.Home, "--binary", bin); err != nil {
			t.Fatalf("peer add %s: %v\n%s", p.Machine, err, stderr)
		}
	}

	// A thread on each daemon; the ticket starts ACTIVE bound to A's thread (the
	// realistic case: it was being worked on A, now we want it on B).
	thA := src.newThread(t, "pi", "mvA", "/tmp")
	thB := dst.newThread(t, "pi", "mvB", "/tmp")
	id := src.ticketCreate(t, "relocate me", "the cross-machine work")
	if _, stderr, err := src.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "active", "--thread", thA.ID); err != nil {
		t.Fatalf("bind active on A: %v\n%s", err, stderr)
	}

	// 1) Read the full record from A and import it onto B (preserving the id). The
	//    import arrives UNATTACHED: binding dropped, active→ready.
	rec, stderr, err := client.Run(t, "ticket", "get", "--machine", src.Machine, "--id", id, "--json")
	if err != nil {
		t.Fatalf("ticket get --machine A: %v\n%s", err, stderr)
	}
	importCmd := exec.Command(bin, "ticket", "import", "--machine", dst.Machine)
	importCmd.Env = sandboxEnv(client.env)
	importCmd.Stdin = strings.NewReader(rec)
	if out, ierr := importCmd.CombinedOutput(); ierr != nil {
		t.Fatalf("ticket import --machine B: %v\n%s", ierr, out)
	}
	if tk := dst.ticketByID(t, id); tk.ID != id || tk.ThreadID != "" || tk.Status != api.StatusReady {
		t.Fatalf("imported record wrong (want same id, unbound, ready): %+v", tk)
	}
	if tk := dst.ticketByID(t, id); tk.Name != "relocate me" || tk.Prompt != "the cross-machine work" {
		t.Errorf("import dropped text fields: %+v", tk)
	}

	// 2) Delete from A, then bind to B's thread — the ticket now lives WITH its thread.
	if _, stderr, err := client.Run(t, "ticket", "delete", "--machine", src.Machine, "--id", id); err != nil {
		t.Fatalf("delete on A: %v\n%s", err, stderr)
	}
	if _, stderr, err := client.Run(t, "ticket", "set-status", "--machine", dst.Machine, "--id", id, "--status", "active", "--thread", thB.ID); err != nil {
		t.Fatalf("bind active on B: %v\n%s", err, stderr)
	}

	// Final: present + active + bound to B's thread on B, and GONE from A.
	if tk := dst.ticketByID(t, id); tk.Status != api.StatusActive || tk.ThreadID != thB.ID {
		t.Fatalf("ticket not active-bound on B: %+v", tk)
	}
	for _, tk := range src.allTickets(t) {
		if tk.ID == id {
			t.Fatalf("ticket %s still on A after the move (should be gone)", id)
		}
	}

	// Loud collision: importing an id that already exists on the target is refused
	// (so a half-done move never silently overwrites).
	dup := exec.Command(bin, "ticket", "import", "--machine", dst.Machine)
	dup.Env = sandboxEnv(client.env)
	dup.Stdin = strings.NewReader(rec)
	if out, derr := dup.CombinedOutput(); derr == nil {
		t.Errorf("importing a colliding id was accepted (want loud conflict); out=%s", out)
	}
}

// testTicketOwnership asserts the single-writer ownership model: a NON-owner
// machine's ticket write routes (over a real ssh hop) to the canonical owner, so
// the record lands on the OWNER's store — not silently locally. This is the
// kill-the-vault-sync-race property of the design.
func testTicketOwnership(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)

	owner := newSandbox(t, matrix.Local) // the canonical always-on owner machine
	owner.startDaemon(t)

	bin := seshBin(t)
	clientHome := t.TempDir()
	client := &localRunner{bin: bin, env: map[string]string{
		"SESH_HOME":         clientHome,
		"SESH_MACHINE":      "client-nonowner",
		"SESH_TICKET_OWNER": owner.Machine, // writes belong to the owner
	}}
	if _, stderr, err := client.Run(t, "peer", "add", "--machine", owner.Machine, "--ssh", "localhost", "--home", owner.Home, "--binary", bin); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}

	// Create a ticket from the NON-owner; it must route to the owner.
	stdout, stderr, err := client.Run(t, "ticket", "create", "--name", "owned by canonical node", "--json")
	if err != nil {
		t.Fatalf("client ticket create: %v\n%s", err, stderr)
	}
	var created api.Ticket
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("decode created ticket: %v\nraw: %s", err, stdout)
	}

	// The write landed on the OWNER's store (single writer).
	found := false
	for _, tk := range owner.allTickets(t) {
		if tk.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ticket %s created by a non-owner did NOT land on the owner's store", created.ID)
	}

	// A read from the non-owner routes to the owner and sees the same record
	// (the non-owner holds no divergent local copy).
	var clientSees bool
	stdout, _, err = client.Run(t, "ticket", "list", "--json")
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
			if strings.Contains(line, created.ID) {
				clientSees = true
			}
		}
	}
	if !clientSees {
		t.Errorf("non-owner read did not route to the owner / see the ticket")
	}
}

// testTicketSendPrompt asserts a ticket's prompt is delivered to its bound
// thread: after send-prompt, the agent begins a turn (activity -> working).
func testTicketSendPrompt(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, agent, "sp", "/tmp")
	sb.waitThreadReady(t, th.ID, agent)

	id := sb.ticketCreate(t, "do the thing", "Write a detailed 150-word explanation of how HTTPS works")
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind active: %v\n%s", err, stderr)
	}

	if _, stderr, err := sb.Runner.Run(t, "ticket", "send-prompt", "--id", id); err != nil {
		t.Fatalf("send-prompt: %v\n%s", err, stderr)
	}
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, th.ID).Busy == api.BusyBusy }) {
		t.Fatalf("agent never started a turn after send-prompt (prompt not delivered?)")
	}
}

// testTicketCreate asserts a created ticket persists with the expected fields and
// starts in triage.
func testTicketCreate(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	id := sb.ticketCreate(t, "fix the parser", "the parser drops trailing commas")
	tk := sb.ticketByID(t, id)
	if tk.Name != "fix the parser" || tk.Prompt != "the parser drops trailing commas" {
		t.Errorf("ticket fields wrong: %+v", tk)
	}
	if tk.Status != api.StatusTriage {
		t.Errorf("new ticket status = %q, want triage", tk.Status)
	}
}

// testTicketSetStatus walks the agent-free transitions (incl. agent-driven done)
// and asserts the loud guards: an invalid status and active-without-a-thread are
// both rejected.
func testTicketSetStatus(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	id := sb.ticketCreate(t, "ship it", "")
	for _, st := range []string{api.StatusReady, api.StatusDone} {
		if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", st); err != nil {
			t.Fatalf("set-status %s: %v\n%s", st, err, stderr)
		}
		if got := sb.ticketByID(t, id).Status; got != st {
			t.Errorf("status = %q, want %q", got, st)
		}
	}
	// Invalid status is rejected.
	if _, _, err := sb.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "bogus"); err == nil {
		t.Errorf("invalid status was accepted")
	}
	// active requires a thread binding (active == attached to a thread).
	if _, _, err := sb.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "active"); err == nil {
		t.Errorf("active without a thread was accepted")
	}
}

// testTicketListByThread asserts list --thread returns exactly the tickets bound
// to that thread.
func testTicketListByThread(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, "pi", "tk", "/tmp")
	bound := sb.ticketCreate(t, "bound", "")
	other := sb.ticketCreate(t, "unbound", "")
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", bound, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind ticket active: %v\n%s", err, stderr)
	}

	listed := sb.ticketsByThread(t, th.ID)
	if len(listed) != 1 || listed[0].ID != bound {
		t.Fatalf("list --thread returned %v, want only %s", ticketIDs(listed), bound)
	}
	for _, tk := range listed {
		if tk.ID == other {
			t.Errorf("unbound ticket leaked into list-by-thread")
		}
	}
}

// testTicketNeedsInput asserts the DERIVED needs-input view tracks the bound
// thread's live activity, including the dead != needs-input distinction.
func testTicketNeedsInput(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	th := sb.newThread(t, "pi", "ni", "/tmp")
	pane := sb.waitThreadReady(t, th.ID, "pi")

	id := sb.ticketCreate(t, "needs input demo", "")
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind active: %v\n%s", err, stderr)
	}

	// Idle thread -> needs-input true.
	if !waitUntil(15*time.Second, func() bool { return sb.ticketNeedsInput(t, id).NeedsInput }) {
		t.Fatalf("idle bound thread should make the ticket need input")
	}

	// Working thread -> NOT needs-input.
	sb.sendKeys(t, pane, "Write a detailed 150-word explanation of how TLS works")
	if !waitUntil(30*time.Second, func() bool {
		ni := sb.ticketNeedsInput(t, id)
		return !ni.NeedsInput && ni.ThreadBusy == string(api.BusyBusy)
	}) {
		t.Fatalf("working bound thread should NOT need input")
	}

	// Dead thread -> needs-RESTART, not needs-input (the distinction the waiting
	// vs dead split exists for).
	if out, err := sb.rawTmux(t, "kill-session", "-t", "=sesh_ni"); err != nil {
		t.Fatalf("kill-session: %v\n%s", err, out)
	}
	if !waitUntil(10*time.Second, func() bool {
		ni := sb.ticketNeedsInput(t, id)
		return ni.NeedsRestart && !ni.NeedsInput
	}) {
		ni := sb.ticketNeedsInput(t, id)
		t.Fatalf("dead bound thread should be needs-restart not needs-input; got %+v", ni)
	}
}

// ---- helpers ----

func (sb *Sandbox) ticketCreate(t *testing.T, name, prompt string) string {
	t.Helper()
	args := []string{"ticket", "create", "--name", name}
	if prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	stdout, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("ticket create: %v\n%s", err, stderr)
	}
	return strings.TrimSpace(stdout)
}

func (sb *Sandbox) ticketByID(t *testing.T, id string) api.Ticket {
	t.Helper()
	for _, tk := range sb.allTickets(t) {
		if tk.ID == id {
			return tk
		}
	}
	t.Fatalf("ticket %s not found", id)
	return api.Ticket{}
}

func (sb *Sandbox) allTickets(t *testing.T) []api.Ticket { return sb.ticketList(t, "") }
func (sb *Sandbox) ticketsByThread(t *testing.T, id string) []api.Ticket {
	return sb.ticketList(t, id)
}

func (sb *Sandbox) ticketList(t *testing.T, threadID string) []api.Ticket {
	t.Helper()
	args := []string{"ticket", "list", "--json"}
	if threadID != "" {
		args = append(args, "--thread", threadID)
	}
	stdout, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("ticket list: %v\n%s", err, stderr)
	}
	var out []api.Ticket
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var tk api.Ticket
		if err := json.Unmarshal([]byte(line), &tk); err != nil {
			t.Fatalf("decode ticket line %q: %v", line, err)
		}
		out = append(out, tk)
	}
	return out
}

func (sb *Sandbox) ticketNeedsInput(t *testing.T, id string) api.TicketNeedsInput {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "ticket", "needs-input", "--id", id, "--json")
	if err != nil {
		t.Fatalf("ticket needs-input: %v\n%s", err, stderr)
	}
	var ni api.TicketNeedsInput
	if err := json.Unmarshal([]byte(stdout), &ni); err != nil {
		t.Fatalf("decode needs-input: %v\nraw: %s", err, stdout)
	}
	return ni
}

func ticketIDs(tks []api.Ticket) []string {
	var ids []string
	for _, tk := range tks {
		ids = append(ids, tk.ID)
	}
	return ids
}
