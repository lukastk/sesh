package api

// Ticket lifecycle statuses (SPEC §4):
//
//	triage  — exists; prompt not final; unattached
//	ready   — prompt final; deployable; unattached
//	active  — attached to a thread
//	done    — terminal (the agent may set this)
//	dropped — terminal
const (
	StatusTriage  = "triage"
	StatusReady   = "ready"
	StatusActive  = "active"
	StatusDone    = "done"
	StatusDropped = "dropped"
)

// ValidTicketStatus reports whether s is a known status.
func ValidTicketStatus(s string) bool {
	switch s {
	case StatusTriage, StatusReady, StatusActive, StatusDone, StatusDropped:
		return true
	}
	return false
}

// Ticket is the persistent ticket record. ThreadID is empty unless bound.
type Ticket struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Prompt        string `json:"prompt"`
	Status        string `json:"status"`
	ThreadID      string `json:"thread_id,omitempty"`
	CreatedAtUnix int64  `json:"created_at_unix"`
	// ClosedAtUnix is when the ticket entered a terminal status (done/dropped) —
	// the "closed/scrapped" timestamp. 0 while open; preserved across an idempotent
	// re-set of the same terminal status; cleared back to 0 if the ticket reopens.
	ClosedAtUnix int64 `json:"closed_at_unix,omitempty"`
}

// CreateTicketRequest is the body of POST /v1/tickets.
type CreateTicketRequest struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt,omitempty"`
}

// SetTicketRequest is the body of POST /v1/tickets/set — a partial update of a
// ticket's text fields. A nil pointer leaves that field unchanged (status and
// thread binding go through SetStatusRequest, which has the active-needs-thread
// invariant).
type SetTicketRequest struct {
	ID     string  `json:"id"`
	Name   *string `json:"name,omitempty"`
	Prompt *string `json:"prompt,omitempty"`
}

// TicketResponse wraps a single ticket.
type TicketResponse struct {
	Schema int    `json:"schema"`
	Ticket Ticket `json:"ticket"`
}

// TicketListResponse is returned by GET /v1/tickets.
type TicketListResponse struct {
	Schema  int      `json:"schema"`
	Tickets []Ticket `json:"tickets"`
}

// SetStatusRequest is the body of POST /v1/tickets/status. ThreadID is required
// when transitioning to active (a ticket is active BECAUSE it is attached to a
// thread); it is ignored otherwise.
type SetStatusRequest struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	ThreadID string `json:"thread_id,omitempty"`
}

// ImportTicketRequest is the body of POST /v1/tickets/import — it inserts a full
// ticket record (PRESERVING its id) onto THIS daemon. It is the landing half of a
// cross-machine ticket move: a ticket↔thread binding only produces live derived
// state (needs-input, the TKT columns) when both live on the same daemon, so a
// ticket bound to a thread on another machine is relocated to that machine. On
// arrival the binding is dropped (the old thread is on the old daemon) and an
// `active` status downgrades to `ready` (unattached) — the caller re-binds locally.
type ImportTicketRequest struct {
	Ticket Ticket `json:"ticket"`
}

// MoveTicketRequest is the body of POST /v1/tickets/move. The daemon it is sent to
// COORDINATES the relocation: it pulls the ticket (and every blob its prompt
// references) from From and pushes them to To, then deletes the source — using its
// own peer transport, so only the coordinator must reach both ends. From defaults to
// the coordinator's own machine.
type MoveTicketRequest struct {
	ID   string `json:"id"`
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// UnbindTicketRequest is the body of POST /v1/tickets/unbind — it detaches a
// ticket from its thread (clears thread_id) and, if it was `active` (a status that
// requires a binding), downgrades it to `ready` (unattached, prompt presumed
// final). Other statuses are left as-is. This is "remove from thread".
type UnbindTicketRequest struct {
	ID string `json:"id"`
}

// TicketThread is the bound-thread context returned by a ticket find — enough
// for a remote client (the Obsidian ticket note) to render the panel without a
// second round-trip. Machine == the ticket's owning machine (a ticket and its
// bound thread are always co-located on the same daemon).
type TicketThread struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Parent  string `json:"parent,omitempty"`
	Machine string `json:"machine"`
}

// TicketFindResponse is returned by GET /v1/tickets/find?id=<id>: a MESH-WIDE
// lookup of a ticket by id. Tickets are per-daemon (spread across the mesh), so
// the daemon this hits resolves its own store first, then fans out to every peer
// (each answering local-only) and returns the first hit — the ticket record PLUS
// its owning machine and bound-thread context, in ONE call. Found=false (with a
// 200, not a 404) is a legitimate state: a draft note has no ticket, a deleted
// ticket resolves to nothing. Unreachable lists peers the fan-out could not reach
// (the not-found answer may be incomplete if non-empty).
type TicketFindResponse struct {
	Schema      int           `json:"schema"`
	Found       bool          `json:"found"`
	Machine     string        `json:"machine,omitempty"`
	Ticket      Ticket        `json:"ticket"`
	Thread      *TicketThread `json:"thread,omitempty"`
	Unreachable []string      `json:"unreachable,omitempty"`
}

// TicketNeedsInput is the derived needs-input view of a ticket (SPEC §4):
// status == active AND the bound thread's activity == waiting, REGARDLESS of
// attachment. A dead bound thread is "needs-restart", not needs-input.
type TicketNeedsInput struct {
	Schema         int    `json:"schema"`
	ID             string `json:"id"`
	NeedsInput     bool   `json:"needs_input"`
	NeedsRestart   bool   `json:"needs_restart"`
	Status         string `json:"status"`
	ThreadID       string `json:"thread_id,omitempty"`
	ThreadHead     string `json:"thread_head,omitempty"`
	ThreadBusy     string `json:"thread_busy,omitempty"`
}
