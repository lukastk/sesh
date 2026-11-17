package store

import (
	"fmt"
	"time"
)

// HeadlessTurnRow is the record of one headless turn process the daemon started.
type HeadlessTurnRow struct {
	ThreadID      string
	PID           int
	StartedAtUnix int64
}

// SetHeadlessTurn records (or replaces) a thread's running turn process.
func (s *Store) SetHeadlessTurn(threadID string, pid int, now time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO headless_turns (thread_id, pid, started_unix) VALUES (?, ?, ?)
		 ON CONFLICT(thread_id) DO UPDATE SET pid = excluded.pid, started_unix = excluded.started_unix`,
		threadID, pid, now.Unix())
	if err != nil {
		return fmt.Errorf("store: record headless turn of %s: %w", threadID, err)
	}
	return nil
}

// ClearHeadlessTurn forgets a thread's turn process (its turn ended).
func (s *Store) ClearHeadlessTurn(threadID string) error {
	if _, err := s.db.Exec(`DELETE FROM headless_turns WHERE thread_id = ?`, threadID); err != nil {
		return fmt.Errorf("store: clear headless turn of %s: %w", threadID, err)
	}
	return nil
}

// HeadlessTurns lists every recorded turn process.
func (s *Store) HeadlessTurns() ([]HeadlessTurnRow, error) {
	rows, err := s.db.Query(`SELECT thread_id, pid, started_unix FROM headless_turns`)
	if err != nil {
		return nil, fmt.Errorf("store: list headless turns: %w", err)
	}
	defer rows.Close()
	var out []HeadlessTurnRow
	for rows.Next() {
		var r HeadlessTurnRow
		if err := rows.Scan(&r.ThreadID, &r.PID, &r.StartedAtUnix); err != nil {
			return nil, fmt.Errorf("store: scan headless turns: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
