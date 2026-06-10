// Package backup imports thread transcripts into a single SQLite file and
// reconstructs them (PARITY_ROADMAP D4, v1's backup ported). Idempotent via
// sha256: an unchanged transcript is skipped. The backup file is
// self-contained and portable (the unit `sesh copy` ships). v2 adaptation:
// entries key on the THREAD id (sesh's stable identity) and carry the agent
// session id (the transcript's own identity — claude's native path needs it).
package backup

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
)

// Entry is one backed-up transcript.
type Entry struct {
	ThreadID       string
	AgentSessionID string
	Agent          string
	Machine        string
	Name           string
	Cwd            string
	SHA256         string
	Bytes          int64
	Content        []byte
}

// Open opens (creating) a backup SQLite file.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)", path))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS transcripts(
		thread_id TEXT PRIMARY KEY, agent_session_id TEXT, agent TEXT, machine TEXT,
		name TEXT, cwd TEXT, sha256 TEXT, bytes INTEGER, content BLOB, updated_at INTEGER)`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// OpenExisting opens a backup file that MUST already exist: a typo'd restore
// path must fail loudly, not silently create an empty database and report a
// false "0 written" success.
func OpenExisting(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no such backup file: %s", path)
		}
		return nil, err
	}
	return Open(path)
}

// Result reports a backup/restore run.
type Result struct {
	Written, Skipped, Missing int
	// Unsupported lists thread ids that could not be restored NATIVELY (pi/
	// codex paths are nondeterministic); restore --to-dir handles every agent.
	Unsupported []string
}

// Backup copies each thread's transcript into the backup db (idempotent by
// sha256). A thread with no transcript on disk counts as Missing — reported,
// never silently dropped.
func Backup(db *sql.DB, threads []api.Thread, homes agents.Homes, nowNanos int64) (Result, error) {
	var res Result
	for _, th := range threads {
		path, found, err := agents.TranscriptPath(agents.Kind(th.AgentKind), th.AgentSessionID, th.Cwd, homes)
		if err != nil || !found || path == "" {
			res.Missing++
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			res.Missing++
			continue
		}
		sum := sha256hex(content)
		var existing string
		_ = db.QueryRow(`SELECT sha256 FROM transcripts WHERE thread_id=?`, th.ID).Scan(&existing)
		if existing == sum {
			res.Skipped++
			continue
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO transcripts
			(thread_id,agent_session_id,agent,machine,name,cwd,sha256,bytes,content,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			th.ID, th.AgentSessionID, th.AgentKind, th.Machine, th.Name, th.Cwd,
			sum, int64(len(content)), content, nowNanos); err != nil {
			return res, err
		}
		res.Written++
	}
	return res, nil
}

// Entries reads backed-up transcripts (all, or one thread id).
func Entries(db *sql.DB, threadID string) ([]Entry, error) {
	q := `SELECT thread_id,agent_session_id,agent,machine,name,cwd,sha256,bytes,content FROM transcripts`
	var args []any
	if threadID != "" {
		q += ` WHERE thread_id=?`
		args = append(args, threadID)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ThreadID, &e.AgentSessionID, &e.Agent, &e.Machine, &e.Name, &e.Cwd, &e.SHA256, &e.Bytes, &e.Content); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ResolveID resolves a full thread id or unique prefix inside the backup.
// Unknown or ambiguous is loud.
func ResolveID(db *sql.DB, input string) (string, error) {
	if input == "" {
		return "", nil
	}
	rows, err := db.Query(`SELECT thread_id FROM transcripts WHERE thread_id=? OR thread_id LIKE ?`, input, input+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var matches []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return "", err
		}
		if u == input {
			return u, nil
		}
		matches = append(matches, u)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no transcript matching %q in backup", input)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous prefix %q matches %d transcripts: %v", input, len(matches), matches)
	}
}

// RestoreTarget chooses where restored transcripts land.
type RestoreTarget struct {
	Native     bool   // reconstruct at the agent's real location (claude only)
	Dir        string // or write <thread-id>.jsonl into this dir
	Homes      agents.Homes
	RewriteCwd string // rewrite cwd for cross-cwd recovery (native)
	Force      bool   // overwrite existing files
}

// Restore writes transcripts from the backup db to disk per target. Native
// restore skips nondeterministic agents into Unsupported (reported, never a
// mid-run abort that would leave a partial restore looking complete).
func Restore(db *sql.DB, threadID string, t RestoreTarget) (Result, error) {
	var res Result
	entries, err := Entries(db, threadID)
	if err != nil {
		return res, err
	}
	for _, e := range entries {
		if t.Native && !NativeRestorable(e.Agent) {
			res.Unsupported = append(res.Unsupported, e.ThreadID)
			continue
		}
		dst, err := restorePath(e, t)
		if err != nil {
			return res, err
		}
		if !t.Force {
			if _, err := os.Stat(dst); err == nil {
				res.Skipped++
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(dst, e.Content, 0o644); err != nil {
			return res, err
		}
		res.Written++
	}
	return res, nil
}

// NativeRestorable reports whether an agent's native transcript path is
// deterministically reconstructible (claude only — see agents.NativePath).
func NativeRestorable(agent string) bool {
	return agent == "claude"
}

func restorePath(e Entry, t RestoreTarget) (string, error) {
	if t.Dir != "" {
		return filepath.Join(t.Dir, e.ThreadID+".jsonl"), nil
	}
	if t.Native {
		cwd := e.Cwd
		if t.RewriteCwd != "" {
			cwd = t.RewriteCwd
		}
		return agents.NativePath(agents.Kind(e.Agent), e.AgentSessionID, cwd, t.Homes)
	}
	return "", fmt.Errorf("restore target: set Native or Dir")
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
