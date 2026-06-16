package store

import "fmt"

// migrations is the ordered list of schema steps. Each is applied once, inside a
// transaction, and the meta.version is bumped to its index. Append-only: never
// edit a shipped migration, add a new one.
var migrations = []string{
	// 1: bookkeeping table.
	`CREATE TABLE IF NOT EXISTS meta (
		id      INTEGER PRIMARY KEY CHECK (id = 1),
		version INTEGER NOT NULL
	);`,
	// 2: threads. A thread is pinned to (machine, session_name); pane and
	// runtime state are never stored, always re-derived live.
	`CREATE TABLE IF NOT EXISTS threads (
		id           TEXT PRIMARY KEY,
		machine      TEXT NOT NULL,
		session_name TEXT NOT NULL,
		cwd          TEXT NOT NULL,
		agent_kind   TEXT NOT NULL,
		name         TEXT NOT NULL,
		tags         TEXT NOT NULL DEFAULT '[]',
		headless     INTEGER NOT NULL DEFAULT 0,
		created_at   INTEGER NOT NULL,
		UNIQUE (session_name)
	);`,
	// 3: tickets. A ticket projects a lifecycle onto a thread; it may be bound to
	// at most one thread (a thread may have many tickets). needs-input is NOT
	// stored — it is derived from the bound thread's live activity.
	`CREATE TABLE IF NOT EXISTS tickets (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		prompt      TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL,
		thread_id   TEXT,
		created_at  INTEGER NOT NULL
	);`,
	// 4: headless thread bookkeeping. agent_session_id is the agent's own
	// conversation id (sesh pre-assigns it for pi/claude; codex generates its
	// own on the first turn). headless_started flags whether the first turn has
	// run (first turn creates the session; later turns resume it).
	`ALTER TABLE threads ADD COLUMN agent_session_id TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE threads ADD COLUMN headless_started INTEGER NOT NULL DEFAULT 0;`,
	// 5: archive — a parked-but-keepable state, hidden from the active list but
	// distinct from dead (runtime gone) and deleted (record gone).
	`ALTER TABLE threads ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;`,
	// 6: the mesh cache — one row per PEER machine holding its last-synced snapshot
	// (payload = JSON array of ThreadSnapshot), so the cross-machine list is read
	// locally and survives a peer going offline (reachable=0, payload retained).
	`CREATE TABLE IF NOT EXISTS peer_snapshots (
		machine        TEXT PRIMARY KEY,
		synced_at_unix INTEGER NOT NULL,
		reachable      INTEGER NOT NULL,
		payload        TEXT NOT NULL
	);`,
	// 7: parent/child — a thread may have a parent thread (tree views, child
	// spawns). Plain uuid reference, '' = root; cycle-guarded at the API layer.
	`ALTER TABLE threads ADD COLUMN parent TEXT NOT NULL DEFAULT '';`,
	// 8: hook mutes — `sesh hooks disable <name>` persists across daemon
	// restarts (the hook DEFINITION lives in config.toml; the mute is runtime
	// state, so it lives here).
	`CREATE TABLE IF NOT EXISTS hook_mutes (name TEXT PRIMARY KEY);`,
	// 9: per-thread notifications — hooks carry SESH_NOTIFY so the user's
	// notify hook can respect a thread's mute. Default ON.
	`ALTER TABLE threads ADD COLUMN notify INTEGER NOT NULL DEFAULT 1;`,
	// 10: subscriptions — (subscriber, subscribee) edges + the last-delivered
	// reply count (seeds the delivery tracker across daemon restarts so a turn
	// is never re-delivered).
	`CREATE TABLE IF NOT EXISTS subscriptions (
		subscriber TEXT NOT NULL,
		subscribee TEXT NOT NULL,
		last_count INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (subscriber, subscribee)
	);`,
	// 11: meta — arbitrary per-thread KV (JSON object column; powers the TUI's
	// meta.<key> predicates and future dynamic columns). APPENDED last:
	// migrations apply by index, inserting mid-list desyncs deployed DBs.
	`ALTER TABLE threads ADD COLUMN meta TEXT NOT NULL DEFAULT '{}';`,
	// 12: drop UNIQUE(session_name). A thread's runtime identity is its pane's
	// @sesh-thread-id marker, not its session — so MANY threads may share one
	// tmux session (own windows, or splits in one window). session_name is now
	// descriptive, not a key. SQLite can't ALTER...DROP CONSTRAINT, so rebuild
	// the table: same columns, no UNIQUE, copy rows, swap. Multiple statements
	// in one element => the whole rebuild is atomic in this migration's tx.
	`CREATE TABLE threads_new (
		id               TEXT PRIMARY KEY,
		machine          TEXT NOT NULL,
		session_name     TEXT NOT NULL,
		cwd              TEXT NOT NULL,
		agent_kind       TEXT NOT NULL,
		name             TEXT NOT NULL,
		tags             TEXT NOT NULL DEFAULT '[]',
		headless         INTEGER NOT NULL DEFAULT 0,
		created_at       INTEGER NOT NULL,
		agent_session_id TEXT NOT NULL DEFAULT '',
		headless_started INTEGER NOT NULL DEFAULT 0,
		archived         INTEGER NOT NULL DEFAULT 0,
		parent           TEXT NOT NULL DEFAULT '',
		notify           INTEGER NOT NULL DEFAULT 1,
		meta             TEXT NOT NULL DEFAULT '{}'
	);
	INSERT INTO threads_new SELECT
		id, machine, session_name, cwd, agent_kind, name, tags, headless,
		created_at, agent_session_id, headless_started, archived, parent,
		notify, meta
	FROM threads;
	DROP TABLE threads;
	ALTER TABLE threads_new RENAME TO threads;`,
	// 13: drop tickets.description — it was superfluous (name + prompt suffice).
	// SQLite 3.35+ (modernc.org/sqlite is well past it) supports DROP COLUMN, so a
	// plain ALTER is enough here (no constraint to rebuild around, unlike #12). Any
	// existing description text is discarded.
	`ALTER TABLE tickets DROP COLUMN description;`,
	// 14: tickets.closed_at — the unix time a ticket entered a terminal status
	// (done/dropped); 0 while open. The "done/scrapped timestamp" the ticket-note
	// projection reads. Set by the daemon on a terminal transition (preserved across
	// an idempotent re-set, cleared to 0 if reopened).
	`ALTER TABLE tickets ADD COLUMN closed_at INTEGER NOT NULL DEFAULT 0;`,
}

// migrate applies any unapplied migrations. The current version lives in
// meta.version; a fresh database starts at 0.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(migrations[0]); err != nil {
		return fmt.Errorf("store: bootstrap meta: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO meta (id, version) VALUES (1, 0) ON CONFLICT(id) DO NOTHING`); err != nil {
		return fmt.Errorf("store: seed meta: %w", err)
	}

	var current int
	if err := s.db.QueryRow("SELECT version FROM meta WHERE id = 1").Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}

	for i := current; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: apply migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec("UPDATE meta SET version = ? WHERE id = 1", i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: bump version to %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", i+1, err)
		}
	}
	return nil
}
