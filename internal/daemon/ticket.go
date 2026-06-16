package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/store"
)

func (d *Daemon) routesTickets(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/tickets", d.handleTicketCreate)
	mux.HandleFunc("GET /v1/tickets", d.handleTicketList)
	mux.HandleFunc("GET /v1/tickets/get", d.handleTicketGet)
	mux.HandleFunc("GET /v1/tickets/find", d.handleTicketFind)
	mux.HandleFunc("POST /v1/tickets/set", d.handleTicketSet)
	mux.HandleFunc("POST /v1/tickets/delete", d.handleTicketDelete)
	mux.HandleFunc("POST /v1/tickets/status", d.handleTicketSetStatus)
	mux.HandleFunc("POST /v1/tickets/import", d.handleTicketImport)
	mux.HandleFunc("POST /v1/tickets/unbind", d.handleTicketUnbind)
	mux.HandleFunc("POST /v1/tickets/move", d.handleTicketMove)
	mux.HandleFunc("GET /v1/tickets/needs-input", d.handleTicketNeedsInput)
	mux.HandleFunc("POST /v1/tickets/send-prompt", d.handleTicketSendPrompt)
}

// handleTicketImport lands a full ticket record on THIS daemon, preserving its id
// — the receiving half of a cross-machine ticket move. The binding is dropped (the
// source thread lives on the source daemon) and an `active` status downgrades to
// `ready`, so the arrived ticket is unattached; the caller re-binds it to a LOCAL
// thread. A colliding id is a loud error (the source must not have been deleted
// yet), never a silent overwrite.
func (d *Daemon) handleTicketImport(w http.ResponseWriter, r *http.Request) {
	var req api.ImportTicketRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := d.importTicketLocal(req.Ticket)
	if err != nil {
		writeError(w, importStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, api.TicketResponse{Schema: api.SchemaVersion, Ticket: t})
}

// errTicketIDExists is the sentinel for an import id collision (a half-done move
// must never silently overwrite) — mapped to 409 over the wire.
var errTicketIDExists = errors.New("ticket import: id already exists on this daemon")

func importStatus(err error) int {
	switch {
	case errors.Is(err, errTicketIDExists):
		return http.StatusConflict
	case errors.Is(err, errImportInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

var errImportInvalid = errors.New("ticket import: invalid record")

// importTicketLocal lands a ticket on THIS daemon, preserving its id but arriving
// UNATTACHED (binding dropped, active→ready). Shared by the import endpoint and the
// daemon-coordinated move. A colliding id is errTicketIDExists (never overwritten).
func (d *Daemon) importTicketLocal(t api.Ticket) (api.Ticket, error) {
	if t.ID == "" || t.Name == "" {
		return api.Ticket{}, fmt.Errorf("%w: id and name are required", errImportInvalid)
	}
	if !api.ValidTicketStatus(t.Status) {
		return api.Ticket{}, fmt.Errorf("%w: invalid status %q", errImportInvalid, t.Status)
	}
	t.ThreadID = ""
	if t.Status == api.StatusActive {
		t.Status = api.StatusReady
	}
	if _, err := d.store.GetTicket(t.ID); err == nil {
		return api.Ticket{}, fmt.Errorf("%w: %s", errTicketIDExists, t.ID)
	} else if !errors.Is(err, store.ErrTicketNotFound) {
		return api.Ticket{}, err
	}
	if err := d.store.InsertTicket(t); err != nil {
		return api.Ticket{}, err
	}
	return t, nil
}

// handleTicketUnbind detaches a ticket from its thread (clears thread_id) and, if
// it was `active`, downgrades it to `ready` (unattached). "Remove from thread".
func (d *Daemon) handleTicketUnbind(w http.ResponseWriter, r *http.Request) {
	var req api.UnbindTicketRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "ticket unbind: id is required")
		return
	}
	if err := d.store.UnbindTicket(req.ID); err != nil {
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

// handleTicketSendPrompt delivers a ticket's prompt to its bound thread's live
// pane (ticket.send-prompt). The ticket must be bound and have a prompt; the
// thread must have a live pane. sesh does NOT track "was the prompt sent" — that
// state does not exist (SPEC §4); this just performs the delivery cleanly.
func (d *Daemon) handleTicketSendPrompt(w http.ResponseWriter, r *http.Request) {
	var req api.SendPromptRequest
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
	// Expand @blob(…) references to absolute paths before delivery; a token pointing
	// at no blob is a loud 400 (never type a dangling token at the agent).
	prompt, err := d.expandPrompt(ticket.Prompt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Optionally prepend the ticket's identity (name + id) so the agent knows which
	// ticket it is working on. Per-call --prepend/--no-prepend (req.Prepend) overrides the
	// [ticket] send_prepend config default.
	tcfg, err := config.LoadTicket(d.cfg.Home)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	prepend := tcfg.SendPrependDefault()
	if req.Prepend != nil {
		prepend = *req.Prepend
	}
	if prepend {
		prompt = fmt.Sprintf("Ticket %q (%s)\n\n%s", ticket.Name, ticket.ID, prompt)
	}
	if err := d.tmux.SendText(loc.Pane, prompt, true); err != nil {
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

// handleTicketGet returns a single ticket by id (?id=ID) — the mechanism behind
// `sesh ticket get`, used by the TUI/myrig ticket editors and by agents.
func (d *Daemon) handleTicketGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "ticket get: id is required")
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
	writeJSON(w, http.StatusOK, api.TicketResponse{Schema: api.SchemaVersion, Ticket: ticket})
}

// handleTicketSet applies a partial update of a ticket's text fields (name,
// prompt). Status and thread binding go through /v1/tickets/status (which owns
// the active-needs-thread invariant), never here.
func (d *Daemon) handleTicketSet(w http.ResponseWriter, r *http.Request) {
	var req api.SetTicketRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "ticket set: id is required")
		return
	}
	if err := d.store.UpdateTicketFields(req.ID, req.Name, req.Prompt); err != nil {
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

// handleTicketDelete removes a ticket record (the ticket-delete action in the
// TUI / myrig). It is purely a record op — no thread or pane is touched.
func (d *Daemon) handleTicketDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "ticket delete: id is required")
		return
	}
	if err := d.store.DeleteTicket(req.ID); err != nil {
		if errors.Is(err, store.ErrTicketNotFound) {
			writeError(w, http.StatusNotFound, "ticket not found: "+req.ID)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": api.SchemaVersion, "deleted": req.ID})
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
	if err := d.store.SetTicketStatus(req.ID, req.Status, req.ThreadID, time.Now().Unix()); err != nil {
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
