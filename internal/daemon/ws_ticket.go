package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
)

const (
	wsTicketTTL      = 30 * time.Second
	wsTicketCapacity = 1024
)

func validCanonicalUUID(raw string) bool {
	id, err := uuid.Parse(raw)
	return err == nil && raw == id.String()
}

type wsTicket struct {
	value     string
	path      string
	threadID  string
	protocol  string
	owner     string
	expiresAt time.Time
}

type wsTicketRegistry struct {
	mu      sync.Mutex
	tickets map[string]wsTicket
}

func newWSTicketRegistry() *wsTicketRegistry {
	return &wsTicketRegistry{tickets: map[string]wsTicket{}}
}

func (r *wsTicketRegistry) mint(path, threadID, protocol, owner string, now time.Time) (wsTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, ticket := range r.tickets {
		if !ticket.expiresAt.After(now) {
			delete(r.tickets, key)
		}
	}
	if len(r.tickets) >= wsTicketCapacity {
		return wsTicket{}, fmt.Errorf("websocket ticket capacity reached; retry after existing tickets expire")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return wsTicket{}, fmt.Errorf("generate websocket ticket: %w", err)
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	ticket := wsTicket{value: value, path: path, threadID: threadID, protocol: protocol, owner: owner, expiresAt: now.Add(wsTicketTTL)}
	r.tickets[value] = ticket
	return ticket, nil
}

// consume deletes a known ticket before checking its binding. A ticket presented
// once is never reusable, including after a wrong-route or failed upgrade attempt.
func (r *wsTicketRegistry) consume(value, path, threadID, protocol string, now time.Time) (wsTicket, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var foundKey string
	var ticket wsTicket
	for key, candidate := range r.tickets {
		if subtle.ConstantTimeCompare([]byte(key), []byte(value)) == 1 {
			foundKey, ticket = key, candidate
			break
		}
	}
	if foundKey == "" {
		return wsTicket{}, false
	}
	delete(r.tickets, foundKey)
	if !ticket.expiresAt.After(now) || ticket.path != path || ticket.threadID != threadID || ticket.protocol != protocol {
		return wsTicket{}, false
	}
	return ticket, true
}

type wsOwnerContextKey struct{}

func wsOwnerFromContext(ctx context.Context) string {
	owner, _ := ctx.Value(wsOwnerContextKey{}).(string)
	return owner
}

// authenticateWSTicket consumes any presented known ticket before validating the
// complete query shape. Duplicate or extra parameters therefore cannot be used to
// probe a ticket and then replay it with a cleaner URL. The thread owner is resolved
// again at use time so a grant cannot silently follow a moved/re-associated thread.
func (d *Daemon) authenticateWSTicket(r *http.Request, now time.Time) (string, bool) {
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return "", false
	}
	q := r.URL.Query()
	threadID := q.Get("id")
	protocol := q.Get("protocol")
	var consumed wsTicket
	accepted := false
	for _, value := range q["ticket"] {
		if value == "" {
			continue
		}
		if ticket, ok := d.wsTickets.consume(value, r.URL.Path, threadID, protocol, now); ok && !accepted {
			consumed, accepted = ticket, true
		}
	}
	if !accepted {
		return "", false
	}
	if len(q) != 3 || len(q["id"]) != 1 || len(q["protocol"]) != 1 || len(q["ticket"]) != 1 ||
		threadID == "" || protocol == "" || !validCanonicalUUID(threadID) {
		return "", false
	}
	owner, err := d.browserThreadOwner(threadID)
	if err != nil || owner != consumed.owner {
		return "", false
	}
	return owner, true
}

func (d *Daemon) routesWSTickets(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/ws-ticket", d.handleWSTicket)
}

func (d *Daemon) handleWSTicket(w http.ResponseWriter, r *http.Request) {
	var req api.WSTicketRequest
	if err := decodeBrowserJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validCanonicalUUID(req.ThreadID) {
		writeError(w, http.StatusBadRequest, "ws-ticket: thread_id must be a canonical UUID")
		return
	}
	path := ""
	switch {
	case req.Route == "rpc" && req.Protocol == "live-v1":
		path = "/v1/threads/rpc"
	case req.Route == "terminal" && req.Protocol == "terminal-v1":
		path = "/v1/threads/terminal"
	default:
		writeError(w, http.StatusBadRequest, "ws-ticket: allowed route/protocol pairs are rpc/live-v1 and terminal/terminal-v1")
		return
	}
	owner, err := d.browserThreadOwner(req.ThreadID)
	if err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	if err := d.authorizeBrowserOwner(owner); err != nil {
		writeError(w, browserOwnerStatus(err), err.Error())
		return
	}
	ticket, err := d.wsTickets.mint(path, req.ThreadID, req.Protocol, owner, time.Now())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "ws-ticket: capacity unavailable")
		return
	}
	q := make(url.Values)
	q.Set("id", req.ThreadID)
	q.Set("protocol", req.Protocol)
	q.Set("ticket", ticket.value)
	writeJSON(w, http.StatusOK, api.WSTicketResponse{
		Schema:          api.SchemaVersion,
		Ticket:          ticket.value,
		ExpiresAtUnixMs: ticket.expiresAt.UnixMilli(),
		Path:            path + "?" + q.Encode(),
	})
}
