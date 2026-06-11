// Package migrate imports v1 sesh records into v2 (PARITY_ROADMAP E4). It
// reads v1's SQLite store (~/.sesh/sesh.db) directly and maps each session to
// a v2 thread. KEY mapping: v1's uuid was BOTH the thread id AND the agent's
// session id (unified) — v2 splits them, so import reuses the v1 uuid as both,
// keeping the agent's on-disk transcript resolvable (resume works untouched).
// Transcripts are NOT copied: they stay in the agents' own homes. Per-machine:
// v1's store holds peers' rows too (the `machine` column); import only THIS
// machine's sessions.
package migrate

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/lukastk/sesh/internal/api"
)

// V1Session is one imported v1 record (the fields v2 keeps).
type V1Session struct {
	UUID      string
	Name      string
	Agent     string
	Machine   string
	Cwd       string
	Archived  bool
	Parent    string
	CreatedNs int64
	Tags      []string
}

// ReadV1Sessions reads sessions owned by `machine` from a v1 SESH_HOME's store.
func ReadV1Sessions(v1Home, machine string) ([]V1Session, error) {
	dbPath := filepath.Join(v1Home, "sesh.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no v1 store at %s: %w", dbPath, err)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT uuid, name, agent, machine, cwd, archived, parent, created_at
		FROM sessions WHERE machine = ? ORDER BY created_at`, machine)
	if err != nil {
		return nil, fmt.Errorf("read v1 sessions: %w", err)
	}
	defer rows.Close()
	var out []V1Session
	for rows.Next() {
		var s V1Session
		var arch int
		if err := rows.Scan(&s.UUID, &s.Name, &s.Agent, &s.Machine, &s.Cwd, &arch, &s.Parent, &s.CreatedNs); err != nil {
			return nil, err
		}
		s.Archived = arch == 1
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Tags (separate table).
	for i := range out {
		trows, err := db.Query(`SELECT tag FROM tags WHERE session_uuid = ?`, out[i].UUID)
		if err != nil {
			return nil, err
		}
		for trows.Next() {
			var tag string
			if err := trows.Scan(&tag); err != nil {
				trows.Close()
				return nil, err
			}
			out[i].Tags = append(out[i].Tags, tag)
		}
		trows.Close()
	}
	return out, nil
}

// ToThread maps a v1 session to a v2 thread record. Notify defaults on.
func (s V1Session) ToThread() api.Thread {
	tags := s.Tags
	if tags == nil {
		tags = []string{}
	}
	created := s.CreatedNs / 1_000_000_000 // v1 nanos -> v2 seconds
	return api.Thread{
		ID:             s.UUID, // v1 uuid == v2 thread id == agent session id
		Machine:        s.Machine,
		SessionName:    "headless-" + s.UUID, // re-minted on first revive
		Cwd:            s.Cwd,
		AgentKind:      s.Agent,
		Name:           s.Name,
		Tags:           tags,
		CreatedAtUnix:  created,
		AgentSessionID: s.UUID,
		Parent:         s.Parent,
		Archived:       s.Archived,
		Notify:         true,
		HeadlessStarted: true, // the conversation already exists on disk
	}
}
