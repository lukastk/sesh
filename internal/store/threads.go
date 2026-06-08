package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lukastk/sesh/internal/api"
)

// ErrThreadNotFound is returned when a thread id has no record.
var ErrThreadNotFound = errors.New("thread not found")

// InsertThread persists a new thread. The UNIQUE(session_name) constraint makes
// a duplicate session a loud error rather than a silent clobber.
func (s *Store) InsertThread(t api.Thread) error {
	tags, err := json.Marshal(t.Tags)
	if err != nil {
		return err
	}
	headless := 0
	if t.Headless {
		headless = 1
	}
	_, err = s.db.Exec(
		`INSERT INTO threads (id, machine, session_name, cwd, agent_kind, name, tags, headless, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Machine, t.SessionName, t.Cwd, t.AgentKind, t.Name, string(tags), headless, t.CreatedAtUnix,
	)
	if err != nil {
		return fmt.Errorf("store: insert thread: %w", err)
	}
	return nil
}

// GetThread returns a thread by id, or ErrThreadNotFound.
func (s *Store) GetThread(id string) (api.Thread, error) {
	row := s.db.QueryRow(
		`SELECT id, machine, session_name, cwd, agent_kind, name, tags, headless, created_at
		 FROM threads WHERE id = ?`, id)
	t, err := scanThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Thread{}, ErrThreadNotFound
	}
	return t, err
}

// ListThreads returns all threads on this machine, newest first.
func (s *Store) ListThreads() ([]api.Thread, error) {
	rows, err := s.db.Query(
		`SELECT id, machine, session_name, cwd, agent_kind, name, tags, headless, created_at
		 FROM threads ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list threads: %w", err)
	}
	defer rows.Close()
	var out []api.Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteThread removes a thread record. Returns ErrThreadNotFound if absent.
func (s *Store) DeleteThread(id string) error {
	res, err := s.db.Exec(`DELETE FROM threads WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete thread: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanThread(r scanner) (api.Thread, error) {
	var t api.Thread
	var tags string
	var headless int
	if err := r.Scan(&t.ID, &t.Machine, &t.SessionName, &t.Cwd, &t.AgentKind, &t.Name, &tags, &headless, &t.CreatedAtUnix); err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(tags), &t.Tags); err != nil {
		return t, fmt.Errorf("store: decode thread tags: %w", err)
	}
	t.Headless = headless == 1
	return t, nil
}
