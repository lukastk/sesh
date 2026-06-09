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
		// ticket.send-prompt touches the agent send path; all three agents now
		// spawn deterministically (codex cwd pre-trusted at spawn).
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("ticket.send-prompt", a, loc,
				func(t *testing.T) { testTicketSendPrompt(t, string(a), loc) })
		}
	}
	matrix.RegisterTest("ticket.ownership", matrix.AgentAgnostic, matrix.Remote, testTicketOwnership)
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
	if !waitUntil(30*time.Second, func() bool { return sb.threadStatus(t, th.ID).Activity == api.ActivityWorking }) {
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
		return !ni.NeedsInput && ni.ThreadActivity == string(api.ActivityWorking)
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
