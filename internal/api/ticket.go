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

// UnbindTicketRequest is the body of POST /v1/tickets/unbind — it detaches a
// ticket from its thread (clears thread_id) and, if it was `active` (a status that
// requires a binding), downgrades it to `ready` (unattached, prompt presumed
// final). Other statuses are left as-is. This is "remove from thread".
type UnbindTicketRequest struct {
	ID string `json:"id"`
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
