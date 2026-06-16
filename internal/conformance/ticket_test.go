package conformance

import (
	"encoding/json"
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
	// ticket.find is the mesh-wide lookup: a hub daemon resolves a ticket living on a
	// PEER via a real ssh fan-out (the one thing a same-store test can't prove).
	matrix.RegisterTest("ticket.find", matrix.AgentAgnostic, matrix.Remote, testTicketFind)
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

// testTicketMove drives the first-class, daemon-coordinated `sesh ticket move`: a
// HUB daemon (neither source nor destination) that peers with both ends pulls the
// ticket AND the blob its prompt references from SRC and pushes them to DST, then
// deletes the source — all over REAL ssh hops. This proves the principled design:
// cross-daemon movement is the daemon's job, and only the invoked (hub) daemon must
// reach both ends (SRC and DST never peer with each other here).
func testTicketMove(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)

	bin := seshBin(t)
	src := newSandbox(t, matrix.Local) // SRC — the ticket's origin
	src.startDaemon(t)
	dst := newSandbox(t, matrix.Local) // DST — where the target thread lives
	dst.startDaemon(t)
	hub := newSandbox(t, matrix.Local) // HUB — the coordinator, neither SRC nor DST
	hub.startDaemon(t)

	// Only the HUB peers with both ends (SRC and DST do NOT peer with each other).
	for _, p := range []*Sandbox{src, dst} {
		if _, stderr, err := hub.Runner.Run(t, "peer", "add", "--machine", p.Machine, "--ssh", "localhost", "--home", p.Home, "--binary", bin); err != nil {
			t.Fatalf("hub peer add %s: %v\n%s", p.Machine, err, stderr)
		}
	}

	// A blob on SRC + a ticket whose prompt REFERENCES it, bound active to a SRC thread.
	blobHash := src.blobAdd(t, "diagram.txt", "SENTINEL-BLOB-BODY")
	token := "@blob(" + blobHash[:12] + ")"
	thA := src.newThread(t, "pi", "mvA", "/tmp")
	id := src.ticketCreate(t, "relocate me", "fix the layout in "+token)
	if _, stderr, err := src.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "active", "--thread", thA.ID); err != nil {
		t.Fatalf("bind active on SRC: %v\n%s", err, stderr)
	}

	// Move SRC→DST, coordinated by the HUB (the invoked daemon).
	if _, stderr, err := hub.Runner.Run(t, "ticket", "move", "--id", id, "--from", src.Machine, "--to", dst.Machine); err != nil {
		t.Fatalf("ticket move (hub-coordinated): %v\n%s", err, stderr)
	}

	// The record landed on DST: same id, UNATTACHED (active→ready), text + token intact.
	tk := dst.ticketByID(t, id)
	if tk.ID != id || tk.ThreadID != "" || tk.Status != api.StatusReady {
		t.Fatalf("moved record wrong (want same id, unbound, ready): %+v", tk)
	}
	if tk.Name != "relocate me" || tk.Prompt != "fix the layout in "+token {
		t.Errorf("move altered the ticket text: %+v", tk)
	}
	// The blob was CARRIED: it now resolves on DST (same content hash), and the token
	// expands to a real path there — the whole point of carrying blobs along.
	gotBytes, stderr, err := dst.Runner.Run(t, "blob", "get", blobHash[:12])
	if err != nil || gotBytes != "SENTINEL-BLOB-BODY" {
		t.Fatalf("blob not carried to DST: get=%q err=%v\n%s", gotBytes, err, stderr)
	}
	expanded, _, err := dst.Runner.RunStdin(t, tk.Prompt, "blob", "expand")
	if err != nil || strings.Contains(expanded, "@blob(") || !strings.Contains(expanded, blobHash) {
		t.Errorf("moved prompt does not expand on DST: %q (err %v)", expanded, err)
	}

	// The ticket is GONE from SRC (the move is destructive on the source record).
	for _, tk := range src.allTickets(t) {
		if tk.ID == id {
			t.Fatalf("ticket %s still on SRC after the move", id)
		}
	}

	// A second move of the same id (now on DST, not SRC) is loud, not a silent no-op.
	if _, _, err := hub.Runner.Run(t, "ticket", "move", "--id", id, "--from", src.Machine, "--to", dst.Machine); err == nil {
		t.Errorf("moving an id no longer on SRC was accepted")
	}
}

// testTicketFind drives the mesh-wide `sesh ticket find`: a HUB daemon resolves a
// ticket that lives on a PEER by fanning out over a REAL ssh hop — the property a
// same-store lookup can never prove. It also asserts the local-store-hit path, the
// bound-thread context the reply carries, closed_at, and that a ticket found nowhere
// is found=false (a legitimate state, not an error).
func testTicketFind(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)

	bin := seshBin(t)
	hub := newSandbox(t, matrix.Local) // the daemon `find` is invoked on
	hub.startDaemon(t)
	peer := newSandbox(t, matrix.Local) // the ticket actually lives here
	peer.startDaemon(t)

	// The hub peers with the peer (one-directional — the peer never peers back).
	if _, stderr, err := hub.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost", "--home", peer.Home, "--binary", bin); err != nil {
		t.Fatalf("hub peer add: %v\n%s", err, stderr)
	}

	// A ticket on the PEER, bound active to a peer thread (so find carries thread context).
	th := peer.newThread(t, "pi", "findme", "/tmp")
	remoteID := peer.ticketCreate(t, "remote ticket", "the remote work")
	if _, stderr, err := peer.Runner.Run(t, "ticket", "set-status", "--id", remoteID, "--status", "active", "--thread", th.ID); err != nil {
		t.Fatalf("bind active on peer: %v\n%s", err, stderr)
	}

	// find from the HUB fans out and resolves the peer's ticket over real ssh.
	got := hub.ticketFind(t, remoteID)
	if !got.Found {
		t.Fatalf("find did not resolve the peer ticket %s (unreachable: %v)", remoteID, got.Unreachable)
	}
	if got.Machine != peer.Machine {
		t.Errorf("find reported machine %q, want the peer %q", got.Machine, peer.Machine)
	}
	if got.Ticket.ID != remoteID || got.Ticket.Name != "remote ticket" {
		t.Errorf("find returned wrong record: %+v", got.Ticket)
	}
	if got.Thread == nil || got.Thread.ID != th.ID || got.Thread.Name != "findme" || got.Thread.Machine != peer.Machine {
		t.Errorf("find did not carry the bound-thread context: %+v", got.Thread)
	}

	// The local-store-hit path: a ticket on the HUB resolves WITHOUT fan-out, machine=hub.
	localID := hub.ticketCreate(t, "local ticket", "")
	if local := hub.ticketFind(t, localID); !local.Found || local.Machine != hub.Machine {
		t.Errorf("local find wrong: found=%t machine=%q want hub %q", local.Found, local.Machine, hub.Machine)
	}

	// closed_at: closing the peer ticket stamps closed_at_unix, visible through find.
	if _, stderr, err := peer.Runner.Run(t, "ticket", "set-status", "--id", remoteID, "--status", "done"); err != nil {
		t.Fatalf("close peer ticket: %v\n%s", err, stderr)
	}
	if closed := hub.ticketFind(t, remoteID); closed.Ticket.ClosedAtUnix == 0 {
		t.Errorf("find should surface closed_at_unix on a done ticket; got 0 (%+v)", closed.Ticket)
	}

	// A ticket found NOWHERE on the mesh is found=false (no error), not a 404.
	none := hub.ticketFind(t, "00000000-0000-0000-0000-000000000000")
	if none.Found {
		t.Errorf("find of an unknown id reported found=true: %+v", none)
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
	// done stamped closed_at; reopening clears it back to 0 (the daemon owns the clock).
	if tk := sb.ticketByID(t, id); tk.ClosedAtUnix == 0 {
		t.Errorf("done ticket should have closed_at_unix > 0, got %+v", tk)
	}
	if _, stderr, err := sb.Runner.Run(t, "ticket", "set-status", "--id", id, "--status", "ready"); err != nil {
		t.Fatalf("reopen: %v\n%s", err, stderr)
	}
	if tk := sb.ticketByID(t, id); tk.ClosedAtUnix != 0 {
		t.Errorf("reopened ticket should clear closed_at_unix, got %d", tk.ClosedAtUnix)
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

func (sb *Sandbox) ticketFind(t *testing.T, id string) api.TicketFindResponse {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "ticket", "find", "--id", id, "--json")
	if err != nil {
		t.Fatalf("ticket find: %v\n%s", err, stderr)
	}
	var out api.TicketFindResponse
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode find: %v\nraw: %s", err, stdout)
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
