package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Held-paste states (migration 35).
const (
	HeldPasteHeld   = "held"   // waiting for a quiet pane
	HeldPasteFailed = "failed" // could not be delivered; text kept until resent or dropped
)

// HeldPaste is one paste the daemon holds for a pane (migration 35).
type HeldPaste struct {
	Seq          int64
	ThreadID     string
	Sender       string
	Text         string
	QueuedAtUnix int64
	State        string
	Reason       string
}

// InsertHeldPaste records a held paste before it waits, so its text survives a
// deadline and a restart. Returns its seq.
func (s *Store) InsertHeldPaste(threadID, sender, text string, now time.Time) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO held_pastes (thread_id, sender, text, queued_at_unix, state) VALUES (?, ?, ?, ?, 'held')`,
		threadID, sender, text, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: record held paste: %w", err)
	}
	return res.LastInsertId()
}

// DeleteHeldPaste removes a held paste that was delivered or dropped.
func (s *Store) DeleteHeldPaste(seq int64) error {
	if _, err := s.db.Exec(`DELETE FROM held_pastes WHERE seq = ?`, seq); err != nil {
		return fmt.Errorf("store: delete held paste %d: %w", seq, err)
	}
	return nil
}

// FailHeldPaste marks a held paste undelivered, keeping its text and the reason.
func (s *Store) FailHeldPaste(seq int64, reason string) error {
	if _, err := s.db.Exec(`UPDATE held_pastes SET state = 'failed', reason = ? WHERE seq = ?`, reason, seq); err != nil {
		return fmt.Errorf("store: fail held paste %d: %w", seq, err)
	}
	return nil
}

// GetUndeliveredPaste returns one failed held paste of threadID.
func (s *Store) GetUndeliveredPaste(threadID string, seq int64) (HeldPaste, bool, error) {
	var h HeldPaste
	err := s.db.QueryRow(
		`SELECT seq, thread_id, sender, text, queued_at_unix, state, reason FROM held_pastes WHERE thread_id = ? AND seq = ? AND state = 'failed'`,
		threadID, seq).Scan(&h.Seq, &h.ThreadID, &h.Sender, &h.Text, &h.QueuedAtUnix, &h.State, &h.Reason)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HeldPaste{}, false, nil
		}
		return HeldPaste{}, false, fmt.Errorf("store: read held paste %d: %w", seq, err)
	}
	return h, true, nil
}

// UndeliveredPastes lists a thread's failed held pastes, oldest first.
func (s *Store) UndeliveredPastes(threadID string) ([]HeldPaste, error) {
	rows, err := s.db.Query(
		`SELECT seq, thread_id, sender, text, queued_at_unix, state, reason FROM held_pastes WHERE thread_id = ? AND state = 'failed' ORDER BY seq`,
		threadID)
	if err != nil {
		return nil, fmt.Errorf("store: list undelivered pastes: %w", err)
	}
	defer rows.Close()
	var out []HeldPaste
	for rows.Next() {
		var h HeldPaste
		if err := rows.Scan(&h.Seq, &h.ThreadID, &h.Sender, &h.Text, &h.QueuedAtUnix, &h.State, &h.Reason); err != nil {
			return nil, fmt.Errorf("store: scan undelivered pastes: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// LostHeldPastes marks every paste still 'held' as failed, with reason. The daemon
// calls it at start: a held paste does not survive a restart, and its text is kept
// so the operator can resend it. Returns the affected rows.
func (s *Store) LostHeldPastes(reason string) ([]HeldPaste, error) {
	rows, err := s.db.Query(
		`SELECT seq, thread_id, sender, text, queued_at_unix, state, reason FROM held_pastes WHERE state = 'held' ORDER BY seq`)
	if err != nil {
		return nil, fmt.Errorf("store: list held pastes: %w", err)
	}
	var out []HeldPaste
	for rows.Next() {
		var h HeldPaste
		if err := rows.Scan(&h.Seq, &h.ThreadID, &h.Sender, &h.Text, &h.QueuedAtUnix, &h.State, &h.Reason); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: scan held pastes: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, h := range out {
		if err := s.FailHeldPaste(h.Seq, reason); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}
