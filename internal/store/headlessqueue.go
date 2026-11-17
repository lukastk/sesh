package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// QueuedMessage is one message waiting for a headless thread's turn to end
// (schema 75, migration 33). Seq is the FIFO key and is never reused.
type QueuedMessage struct {
	Seq          int64
	ThreadID     string
	Sender       string
	Text         string
	QueuedAtUnix int64
}

// EnqueueHeadless appends a message to a thread's queue and returns its seq.
func (s *Store) EnqueueHeadless(threadID, sender, text string, now time.Time) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO headless_queue (thread_id, sender, text, queued_at_unix) VALUES (?, ?, ?, ?)`,
		threadID, sender, text, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: enqueue headless message: %w", err)
	}
	return res.LastInsertId()
}

// HeadlessQueue lists a thread's queued messages in delivery order.
func (s *Store) HeadlessQueue(threadID string) ([]QueuedMessage, error) {
	rows, err := s.db.Query(
		`SELECT seq, thread_id, sender, text, queued_at_unix FROM headless_queue WHERE thread_id = ? ORDER BY seq`,
		threadID)
	if err != nil {
		return nil, fmt.Errorf("store: list headless queue: %w", err)
	}
	defer rows.Close()
	var out []QueuedMessage
	for rows.Next() {
		var m QueuedMessage
		if err := rows.Scan(&m.Seq, &m.ThreadID, &m.Sender, &m.Text, &m.QueuedAtUnix); err != nil {
			return nil, fmt.Errorf("store: scan headless queue: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// HeadlessQueueHead returns the oldest queued message of a thread, ok=false when
// its queue is empty.
func (s *Store) HeadlessQueueHead(threadID string) (QueuedMessage, bool, error) {
	var m QueuedMessage
	err := s.db.QueryRow(
		`SELECT seq, thread_id, sender, text, queued_at_unix FROM headless_queue WHERE thread_id = ? ORDER BY seq LIMIT 1`,
		threadID).Scan(&m.Seq, &m.ThreadID, &m.Sender, &m.Text, &m.QueuedAtUnix)
	if errors.Is(err, sql.ErrNoRows) {
		return QueuedMessage{}, false, nil
	}
	if err != nil {
		return QueuedMessage{}, false, fmt.Errorf("store: headless queue head: %w", err)
	}
	return m, true, nil
}

// HeadlessQueueCount is how many messages wait for a thread.
func (s *Store) HeadlessQueueCount(threadID string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM headless_queue WHERE thread_id = ?`, threadID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count headless queue: %w", err)
	}
	return n, nil
}

// DeleteHeadless removes one queued message of threadID. It reports whether the
// row existed; a seq that belongs to another thread is not found.
func (s *Store) DeleteHeadless(threadID string, seq int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM headless_queue WHERE thread_id = ? AND seq = ?`, threadID, seq)
	if err != nil {
		return false, fmt.Errorf("store: delete headless message: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RestoreHeadless puts back a message that was popped for a turn that could not
// start because another turn was in flight. It keeps its original seq, so the
// FIFO order is unchanged.
func (s *Store) RestoreHeadless(m QueuedMessage) error {
	_, err := s.db.Exec(
		`INSERT INTO headless_queue (seq, thread_id, sender, text, queued_at_unix) VALUES (?, ?, ?, ?, ?)`,
		m.Seq, m.ThreadID, m.Sender, m.Text, m.QueuedAtUnix)
	if err != nil {
		return fmt.Errorf("store: restore headless message %d: %w", m.Seq, err)
	}
	return nil
}

// ThreadsWithHeadlessQueue lists the threads that still hold queued messages, with
// their counts. The daemon reads it at start: a queue that survived a restart is
// reported, never delivered behind the operator's back.
func (s *Store) ThreadsWithHeadlessQueue() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT thread_id, COUNT(*) FROM headless_queue GROUP BY thread_id`)
	if err != nil {
		return nil, fmt.Errorf("store: headless queue threads: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("store: scan headless queue threads: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}
