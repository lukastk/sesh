package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
		`INSERT INTO tickets (id, name, prompt, status, thread_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Prompt, t.Status, threadID, t.CreatedAtUnix,
	)
	if err != nil {
		return fmt.Errorf("store: insert ticket: %w", err)
	}
	return nil
}

// GetTicket returns a ticket by id, or ErrTicketNotFound.
func (s *Store) GetTicket(id string) (api.Ticket, error) {
	row := s.db.QueryRow(
		`SELECT id, name, prompt, status, COALESCE(thread_id, ''), created_at
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
		`SELECT id, name, prompt, status, COALESCE(thread_id, ''), created_at
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
		`SELECT id, name, prompt, status, COALESCE(thread_id, ''), created_at
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
	err := r.Scan(&t.ID, &t.Name, &t.Prompt, &t.Status, &t.ThreadID, &t.CreatedAtUnix)
	return t, err
}

// UpdateTicketFields applies a partial update of a ticket's text fields. A nil
// pointer leaves that column unchanged. Returns ErrTicketNotFound if no row
// matched. With no fields set it is a no-op (and still verifies existence).
func (s *Store) UpdateTicketFields(id string, name, prompt *string) error {
	sets := []string{}
	args := []any{}
	if name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if prompt != nil {
		sets = append(sets, "prompt = ?")
		args = append(args, *prompt)
	}
	if len(sets) == 0 {
		// Nothing to change: just assert the ticket exists (loud if not).
		if _, err := s.GetTicket(id); err != nil {
			return err
		}
		return nil
	}
	args = append(args, id)
	res, err := s.db.Exec(`UPDATE tickets SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("store: update ticket fields: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTicketNotFound
	}
	return nil
}

// DeleteTicket removes a ticket record. Returns ErrTicketNotFound if absent.
func (s *Store) DeleteTicket(id string) error {
	res, err := s.db.Exec(`DELETE FROM tickets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete ticket: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTicketNotFound
	}
	return nil
}

// OpenTicketCounts returns, per thread id, the number of bound tickets that
// are still OPEN (not done, not dropped) — joined into grid rows and mesh
// snapshots so the TUI's `ticketed` predicate reflects real ticket state.
func (s *Store) OpenTicketCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT thread_id, COUNT(*) FROM tickets
		WHERE thread_id != '' AND status NOT IN (?, ?) GROUP BY thread_id`,
		api.StatusDone, api.StatusDropped)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// TicketDigest summarises a thread's OPEN (not done/dropped) tickets for the
// grid/snapshot: how many, the newest one's name (for the ticket-name column),
// and whether any is active (so the owning daemon can derive a needs-input glyph
// by combining HasActive with the thread's live head/busy).
type TicketDigest struct {
	Count      int
	NewestName string
	HasActive  bool
}

// OpenTicketDigests returns one TicketDigest per thread that has >=1 open ticket.
// Threads with no open tickets are absent from the map.
func (s *Store) OpenTicketDigests() (map[string]TicketDigest, error) {
	// DESC so the first row seen per thread is the newest (matches the list order).
	rows, err := s.db.Query(`SELECT thread_id, name, status FROM tickets
		WHERE thread_id != '' AND status NOT IN (?, ?) ORDER BY created_at DESC, id`,
		api.StatusDone, api.StatusDropped)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]TicketDigest{}
	for rows.Next() {
		var id, name, status string
		if err := rows.Scan(&id, &name, &status); err != nil {
			return nil, err
		}
		d := out[id]
		if d.Count == 0 {
			d.NewestName = name // first seen = newest, since ordered DESC
		}
		d.Count++
		if status == api.StatusActive {
			d.HasActive = true
		}
		out[id] = d
	}
	return out, rows.Err()
}

// UnbindTicket detaches a ticket from its thread: it clears thread_id and, if the
// ticket was `active` (a status that REQUIRES a binding), downgrades it to `ready`
// (unattached). Any other status is preserved. Returns ErrTicketNotFound if absent.
func (s *Store) UnbindTicket(id string) error {
	res, err := s.db.Exec(
		`UPDATE tickets SET thread_id = NULL,
		   status = CASE WHEN status = ? THEN ? ELSE status END
		 WHERE id = ?`,
		api.StatusActive, api.StatusReady, id)
	if err != nil {
		return fmt.Errorf("store: unbind ticket: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTicketNotFound
	}
	return nil
}

// SetHookMuted persists a hook's mute state.
func (s *Store) SetHookMuted(name string, muted bool) error {
	var err error
	if muted {
		_, err = s.db.Exec(`INSERT OR IGNORE INTO hook_mutes (name) VALUES (?)`, name)
	} else {
		_, err = s.db.Exec(`DELETE FROM hook_mutes WHERE name = ?`, name)
	}
	return err
}

// HookMutes returns the persisted muted hook names.
func (s *Store) HookMutes() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT name FROM hook_mutes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}
