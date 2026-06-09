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
	started := 0
	if t.HeadlessStarted {
		started = 1
	}
	_, err = s.db.Exec(
		`INSERT INTO threads (id, machine, session_name, cwd, agent_kind, name, tags, headless, created_at, agent_session_id, headless_started)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Machine, t.SessionName, t.Cwd, t.AgentKind, t.Name, string(tags), headless, t.CreatedAtUnix, t.AgentSessionID, started,
	)
	if err != nil {
		return fmt.Errorf("store: insert thread: %w", err)
	}
	return nil
}

// GetThread returns a thread by id, or ErrThreadNotFound.
func (s *Store) GetThread(id string) (api.Thread, error) {
	row := s.db.QueryRow(
		`SELECT id, machine, session_name, cwd, agent_kind, name, tags, headless, created_at, agent_session_id, headless_started, archived
		 FROM threads WHERE id = ?`, id)
	t, err := scanThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Thread{}, ErrThreadNotFound
	}
	return t, err
}

// ListThreads returns this machine's threads, newest first. Archived threads are
// excluded unless includeArchived is set (the active list hides them).
func (s *Store) ListThreads(includeArchived bool) ([]api.Thread, error) {
	q := `SELECT id, machine, session_name, cwd, agent_kind, name, tags, headless, created_at, agent_session_id, headless_started, archived
		 FROM threads`
	if !includeArchived {
		q += ` WHERE archived = 0`
	}
	q += ` ORDER BY created_at DESC, id`
	rows, err := s.db.Query(q)
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

// SetHeadlessSession records the agent session id and marks the headless thread
// as started (after its first turn). codex discovers its id on the first turn;
// pi/claude keep the pre-assigned one.
func (s *Store) SetHeadlessSession(id, agentSessionID string) error {
	res, err := s.db.Exec(
		`UPDATE threads SET agent_session_id = ?, headless_started = 1 WHERE id = ?`,
		agentSessionID, id)
	if err != nil {
		return fmt.Errorf("store: set headless session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

// RenameThread updates a thread's display name.
func (s *Store) RenameThread(id, name string) error {
	return s.updateThread(`UPDATE threads SET name = ? WHERE id = ?`, name, id)
}

// SetThreadTags replaces a thread's tag set.
func (s *Store) SetThreadTags(id string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	return s.updateThread(`UPDATE threads SET tags = ? WHERE id = ?`, string(b), id)
}

// SetThreadArchived parks/unparks a thread (record kept).
func (s *Store) SetThreadArchived(id string, archived bool) error {
	v := 0
	if archived {
		v = 1
	}
	return s.updateThread(`UPDATE threads SET archived = ? WHERE id = ?`, v, id)
}

// SetThreadAgentSession records a headed thread's captured agent session id (used
// by resume; for codex it is discovered after the first turn).
func (s *Store) SetThreadAgentSession(id, agentSessionID string) error {
	return s.updateThread(`UPDATE threads SET agent_session_id = ? WHERE id = ?`, agentSessionID, id)
}

// SetThreadHeaded flips a headless thread to headed and gives it a real tmux session
// name (used by `thread headful` promotion).
func (s *Store) SetThreadHeaded(id, sessionName string) error {
	res, err := s.db.Exec(`UPDATE threads SET headless = 0, session_name = ? WHERE id = ?`, sessionName, id)
	if err != nil {
		return fmt.Errorf("store: set thread headed: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

func (s *Store) updateThread(query string, arg any, id string) error {
	res, err := s.db.Exec(query, arg, id)
	if err != nil {
		return fmt.Errorf("store: update thread: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrThreadNotFound
	}
	return nil
}

func scanThread(r scanner) (api.Thread, error) {
	var t api.Thread
	var tags string
	var headless, started, archived int
	if err := r.Scan(&t.ID, &t.Machine, &t.SessionName, &t.Cwd, &t.AgentKind, &t.Name, &tags, &headless, &t.CreatedAtUnix, &t.AgentSessionID, &started, &archived); err != nil {
		return t, err
	}
	t.Archived = archived == 1
	if err := json.Unmarshal([]byte(tags), &t.Tags); err != nil {
		return t, fmt.Errorf("store: decode thread tags: %w", err)
	}
	t.Headless = headless == 1
	t.HeadlessStarted = started == 1
	return t, nil
}
