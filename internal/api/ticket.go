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
	Description   string `json:"description"`
	Prompt        string `json:"prompt"`
	Status        string `json:"status"`
	ThreadID      string `json:"thread_id,omitempty"`
	CreatedAtUnix int64  `json:"created_at_unix"`
}

// CreateTicketRequest is the body of POST /v1/tickets.
type CreateTicketRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
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
	ThreadActivity string `json:"thread_activity,omitempty"`
}
