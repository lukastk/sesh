package daemon

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesTickets(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/tickets", d.handleTicketCreate)
	mux.HandleFunc("GET /v1/tickets", d.handleTicketList)
	mux.HandleFunc("POST /v1/tickets/status", d.handleTicketSetStatus)
	mux.HandleFunc("GET /v1/tickets/needs-input", d.handleTicketNeedsInput)
	mux.HandleFunc("POST /v1/tickets/send-prompt", d.handleTicketSendPrompt)
}

// handleTicketSendPrompt delivers a ticket's prompt to its bound thread's live
// pane (ticket.send-prompt). The ticket must be bound and have a prompt; the
// thread must have a live pane. sesh does NOT track "was the prompt sent" — that
// state does not exist (SPEC §4); this just performs the delivery cleanly.
func (d *Daemon) handleTicketSendPrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "ticket send-prompt: id is required")
		return
	}
	ticket, err := d.store.GetTicket(req.ID)
	if err != nil {
		if errors.Is(err, store.ErrTicketNotFound) {
			writeError(w, http.StatusNotFound, "ticket not found: "+req.ID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ticket.ThreadID == "" {
		writeError(w, http.StatusConflict, "ticket is not bound to a thread")
		return
	}
	if ticket.Prompt == "" {
		writeError(w, http.StatusBadRequest, "ticket has no prompt to send")
		return
	}
	loc, found, err := d.tmux.FindPaneByThreadID(ticket.ThreadID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusConflict, "bound thread has no live pane (dead); cannot send prompt")
		return
	}
	if err := d.tmux.SendText(loc.Pane, ticket.Prompt, true); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "sent": req.ID})
}

func (d *Daemon) handleTicketCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateTicketRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "ticket: name is required")
		return
	}
	ticket := api.Ticket{
		ID:            uuid.NewString(),
		Name:          req.Name,
		Description:   req.Description,
		Prompt:        req.Prompt,
		Status:        api.StatusTriage, // a new ticket starts in triage
		CreatedAtUnix: time.Now().Unix(),
	}
	if err := d.store.InsertTicket(ticket); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.TicketResponse{Schema: api.SchemaVersion, Ticket: ticket})
}

// handleTicketList lists tickets, optionally filtered to a thread (?thread=ID),
// which is ticket.list-by-thread (what an agent is assigned).
func (d *Daemon) handleTicketList(w http.ResponseWriter, r *http.Request) {
	var tickets []api.Ticket
	var err error
	if threadID := r.URL.Query().Get("thread"); threadID != "" {
		tickets, err = d.store.ListTicketsByThread(threadID)
	} else {
		tickets, err = d.store.ListTickets()
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tickets == nil {
		tickets = []api.Ticket{}
	}
	writeJSON(w, http.StatusOK, api.TicketListResponse{Schema: api.SchemaVersion, Tickets: tickets})
}

func (d *Daemon) handleTicketSetStatus(w http.ResponseWriter, r *http.Request) {
	var req api.SetStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || !api.ValidTicketStatus(req.Status) {
		writeError(w, http.StatusBadRequest, "ticket status: id and a valid status are required")
		return
	}
	// active means "attached to a thread" — so a binding is required to enter it.
	if req.Status == api.StatusActive {
		if req.ThreadID == "" {
			writeError(w, http.StatusBadRequest, "ticket: transitioning to active requires --thread (active == attached to a thread)")
			return
		}
		if _, err := d.store.GetThread(req.ThreadID); err != nil {
			if errors.Is(err, store.ErrThreadNotFound) {
				writeError(w, http.StatusBadRequest, "ticket: bound thread not found: "+req.ThreadID)
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := d.store.SetTicketStatus(req.ID, req.Status, req.ThreadID); err != nil {
		if errors.Is(err, store.ErrTicketNotFound) {
			writeError(w, http.StatusNotFound, "ticket not found: "+req.ID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ticket, err := d.store.GetTicket(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.TicketResponse{Schema: api.SchemaVersion, Ticket: ticket})
}

// handleTicketNeedsInput computes the DERIVED needs-input view (never stored):
// status == active AND the bound thread's activity == waiting, regardless of
// attachment. A dead bound thread is needs-restart, not needs-input.
func (d *Daemon) handleTicketNeedsInput(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "ticket needs-input: id is required")
		return
	}
	ticket, err := d.store.GetTicket(id)
	if err != nil {
		if errors.Is(err, store.ErrTicketNotFound) {
			writeError(w, http.StatusNotFound, "ticket not found: "+id)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := api.TicketNeedsInput{Schema: api.SchemaVersion, ID: id, Status: ticket.Status, ThreadID: ticket.ThreadID}
	if ticket.Status != api.StatusActive || ticket.ThreadID == "" {
		writeJSON(w, http.StatusOK, out) // not active => not needs-input
		return
	}
	thread, err := d.store.GetThread(ticket.ThreadID)
	if err != nil {
		if errors.Is(err, store.ErrThreadNotFound) {
			// Bound thread record is gone: like a runtime-less thread (needs-revival).
			out.NeedsRestart = true
			out.ThreadHead, out.ThreadBusy = string(api.Headless), string(api.BusyIdle)
			writeJSON(w, http.StatusOK, out)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	head, busy, err := d.resolveState(thread)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out.ThreadHead, out.ThreadBusy = string(head), string(busy)
	// needs-input: a live agent blocked on the human. needs-restart (revival):
	// no runtime at all. A busy thread needs neither.
	switch {
	case head == api.Headful && busy == api.BusyIdle:
		out.NeedsInput = true
	case head == api.Headless && busy == api.BusyIdle:
		out.NeedsRestart = true
	}
	writeJSON(w, http.StatusOK, out)
}
