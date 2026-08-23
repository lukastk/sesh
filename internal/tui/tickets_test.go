package tui

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// TestTicketDetailShowsFullTicketID guards the detail view against the confusion that
// led a ticket send to the wrong id: the view showed only the (truncated) thread id in
// both the header and the "thread" row, with the ticket's own id nowhere on screen, so
// the ticket id was guessed to be the thread id. The detail view must render the ticket's
// FULL id, distinct from the truncated thread id.
func TestTicketDetailShowsFullTicketID(t *testing.T) {
	const ticketID = "4d4e8592-b9d7-43cd-8f5d-d3df37c5c9f0"
	const threadID = "538e103f-71c8-4be9-9a34-5edfb88efc2c"
	m := Model{
		ticketMode:   ticketDetail,
		ticketCursor: 0,
		tickets: []api.Ticket{{
			ID:       ticketID,
			Name:     "sesh - new thread type",
			Prompt:   "# spec\nfirst line",
			Status:   "active",
			ThreadID: threadID,
		}},
	}
	out := m.ticketDetailView()
	if !strings.Contains(out, ticketID) {
		t.Fatalf("detail view must show the full ticket id %q; got:\n%s", ticketID, out)
	}
	// The ticket id and thread id share a prefix only by coincidence here; the point is
	// the ticket id row must be the ticket's, not the thread's truncated id.
	if strings.Contains(out, threadID) {
		t.Errorf("detail view unexpectedly shows the FULL thread id (thread is intentionally truncated); got:\n%s", out)
	}
	// The id row is a non-selectable header: the cursor still starts on the first
	// navigable item (name), and navigation still spans exactly the td* items.
	if m.ticketDetail != tdName {
		t.Errorf("cursor should start on tdName, got %d", m.ticketDetail)
	}
}
