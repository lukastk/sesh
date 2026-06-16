package store

import (
	"errors"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func TestTicketCRUDAndBinding(t *testing.T) {
	s := openTestStore(t)
	tk := api.Ticket{ID: "t1", Name: "a", Prompt: "p", Status: api.StatusTriage, CreatedAtUnix: 1}
	if err := s.InsertTicket(tk); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetTicket("t1")
	if err != nil || got.Status != api.StatusTriage || got.ThreadID != "" {
		t.Fatalf("get: %v %+v", err, got)
	}

	// Bind to a thread via active.
	if err := s.SetTicketStatus("t1", api.StatusActive, "thread-x", 100); err != nil {
		t.Fatalf("set active: %v", err)
	}
	got, _ = s.GetTicket("t1")
	if got.Status != api.StatusActive || got.ThreadID != "thread-x" {
		t.Fatalf("after active: %+v", got)
	}
	if got.ClosedAtUnix != 0 {
		t.Fatalf("active ticket should have closed_at=0, got %d", got.ClosedAtUnix)
	}

	byThread, err := s.ListTicketsByThread("thread-x")
	if err != nil || len(byThread) != 1 {
		t.Fatalf("list by thread: %v len=%d", err, len(byThread))
	}
	if other, _ := s.ListTicketsByThread("nobody"); len(other) != 0 {
		t.Fatalf("unrelated thread returned %d tickets", len(other))
	}

	// done keeps the binding (no threadID passed) and stamps closed_at.
	if err := s.SetTicketStatus("t1", api.StatusDone, "", 500); err != nil {
		t.Fatalf("set done: %v", err)
	}
	got, _ = s.GetTicket("t1")
	if got.Status != api.StatusDone || got.ThreadID != "thread-x" {
		t.Fatalf("after done: %+v", got)
	}
	if got.ClosedAtUnix != 500 {
		t.Fatalf("done should stamp closed_at=500, got %d", got.ClosedAtUnix)
	}

	// Re-setting the SAME terminal status preserves the first close time.
	if err := s.SetTicketStatus("t1", api.StatusDone, "", 999); err != nil {
		t.Fatalf("re-set done: %v", err)
	}
	if got, _ = s.GetTicket("t1"); got.ClosedAtUnix != 500 {
		t.Fatalf("re-set done should preserve closed_at=500, got %d", got.ClosedAtUnix)
	}

	// Reopening to a non-terminal status clears closed_at back to 0.
	if err := s.SetTicketStatus("t1", api.StatusActive, "thread-x", 1000); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, _ = s.GetTicket("t1"); got.ClosedAtUnix != 0 {
		t.Fatalf("reopen should clear closed_at, got %d", got.ClosedAtUnix)
	}
}

func TestSetStatusMissingTicket(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetTicketStatus("nope", api.StatusReady, "", 0); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("want ErrTicketNotFound, got %v", err)
	}
}
