package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// Thread context persistence (migration 28, api/context.go). The text is in
// thread_context; threads.context_hash mirrors "has context" onto the row. A
// cleared context keeps its thread_context row (text ”) so the delivery record —
// which version the agent session last received — survives the clear: that is how
// the next delivery knows to tell the agent the context was removed.

// ErrContextConflict is a conditional write whose expected hash no longer matches.
var ErrContextConflict = errors.New("thread context changed since it was read")

// ContextHashOf is the version id of a context text ("" for no context).
func ContextHashOf(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

// GetThreadContext returns a thread's context text and when it was last set
// ("", 0 when it has none). ErrThreadNotFound when the thread does not exist.
func (s *Store) GetThreadContext(id string) (text string, updatedAt int64, err error) {
	if _, err := s.contextHash(s.db, id); err != nil {
		return "", 0, err
	}
	err = s.db.QueryRow(`SELECT text, updated_at FROM thread_context WHERE thread_id = ?`, id).Scan(&text, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("store: read thread context: %w", err)
	}
	if text == "" {
		updatedAt = 0
	}
	return text, updatedAt, nil
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func (s *Store) contextHash(q queryRower, id string) (string, error) {
	var h string
	err := q.QueryRow(`SELECT context_hash FROM threads WHERE id = ?`, id).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrThreadNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: read context hash: %w", err)
	}
	return h, nil
}

// SetThreadContext sets (text != "") or clears (text == "") a thread's context.
// expectHash, when non-empty, makes it conditional: "none" expects no context,
// anything else the current hash; a mismatch is ErrContextConflict. It reports
// whether anything changed (setting the same text again is a no-op).
func (s *Store) SetThreadContext(id, text string, now int64, expectHash string) (changed bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("store: set thread context: begin: %w", err)
	}
	defer tx.Rollback()
	cur, err := s.contextHash(tx, id)
	if err != nil {
		return false, err
	}
	if expectHash != "" {
		want := expectHash
		if want == "none" {
			want = ""
		}
		if cur != want {
			return false, ErrContextConflict
		}
	}
	next := ContextHashOf(text)
	if next == cur {
		return false, nil
	}
	if _, err := tx.Exec(`INSERT INTO thread_context (thread_id, text, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET text = excluded.text, updated_at = excluded.updated_at`, id, text, now); err != nil {
		return false, fmt.Errorf("store: set thread context: %w", err)
	}
	if _, err := tx.Exec(`UPDATE threads SET context_hash = ? WHERE id = ?`, next, id); err != nil {
		return false, fmt.Errorf("store: set thread context hash: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: set thread context: commit: %w", err)
	}
	return true, nil
}

// ContextDelivery returns which agent session last received which context version
// ("" hash = it was told there is none, or nothing was ever delivered).
func (s *Store) ContextDelivery(id string) (session, hash string, err error) {
	err = s.db.QueryRow(`SELECT delivered_session, delivered_hash FROM thread_context WHERE thread_id = ?`, id).Scan(&session, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("store: read context delivery: %w", err)
	}
	return session, hash, nil
}

// SetContextDelivery records that agent session `session` now holds context
// version `hash`. It never touches the threads row (no mesh churn).
func (s *Store) SetContextDelivery(id, session, hash string) error {
	if _, err := s.db.Exec(`INSERT INTO thread_context (thread_id, text, updated_at, delivered_session, delivered_hash) VALUES (?, '', 0, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET delivered_session = excluded.delivered_session, delivered_hash = excluded.delivered_hash`, id, session, hash); err != nil {
		return fmt.Errorf("store: record context delivery: %w", err)
	}
	return nil
}
