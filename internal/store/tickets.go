package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/lukastk/sesh/internal/api"
)

// ErrTicketNotFound is returned when a ticket id has no record.
var ErrTicketNotFound = errors.New("ticket not found")

// InsertTicket persists a new ticket.
func (s *Store) InsertTicket(t api.Ticket) error {
	var threadID any
	if t.ThreadID != "" {
		threadID = t.ThreadID
	}
	_, err := s.db.Exec(
		`INSERT INTO tickets (id, name, description, prompt, status, thread_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Description, t.Prompt, t.Status, threadID, t.CreatedAtUnix,
	)
	if err != nil {
		return fmt.Errorf("store: insert ticket: %w", err)
	}
	return nil
}

// GetTicket returns a ticket by id, or ErrTicketNotFound.
func (s *Store) GetTicket(id string) (api.Ticket, error) {
	row := s.db.QueryRow(
		`SELECT id, name, description, prompt, status, COALESCE(thread_id, ''), created_at
		 FROM tickets WHERE id = ?`, id)
	t, err := scanTicket(row)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Ticket{}, ErrTicketNotFound
	}
	return t, err
}

// ListTicketsByThread returns the tickets bound to a thread, newest first.
func (s *Store) ListTicketsByThread(threadID string) ([]api.Ticket, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, prompt, status, COALESCE(thread_id, ''), created_at
		 FROM tickets WHERE thread_id = ? ORDER BY created_at DESC, id`, threadID)
	if err != nil {
		return nil, fmt.Errorf("store: list tickets by thread: %w", err)
	}
	defer rows.Close()
	return scanTickets(rows)
}

// ListTickets returns all tickets, newest first.
func (s *Store) ListTickets() ([]api.Ticket, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, prompt, status, COALESCE(thread_id, ''), created_at
		 FROM tickets ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list tickets: %w", err)
	}
	defer rows.Close()
	return scanTickets(rows)
}

// SetTicketStatus updates a ticket's status and (when binding to active) its
// thread. Passing threadID = "" leaves the binding unchanged for non-active
// transitions; for active it is required by the caller.
func (s *Store) SetTicketStatus(id, status, threadID string) error {
	var res sql.Result
	var err error
	if threadID != "" {
		res, err = s.db.Exec(`UPDATE tickets SET status = ?, thread_id = ? WHERE id = ?`, status, threadID, id)
	} else {
		res, err = s.db.Exec(`UPDATE tickets SET status = ? WHERE id = ?`, status, id)
	}
	if err != nil {
		return fmt.Errorf("store: set ticket status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTicketNotFound
	}
	return nil
}

func scanTickets(rows *sql.Rows) ([]api.Ticket, error) {
	var out []api.Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanTicket(r scanner) (api.Ticket, error) {
	var t api.Ticket
	err := r.Scan(&t.ID, &t.Name, &t.Description, &t.Prompt, &t.Status, &t.ThreadID, &t.CreatedAtUnix)
	return t, err
}
