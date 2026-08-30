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
	// 15: tickets.notes — free-text scratch field, primarily where an agent records
	// what it did on close (and which commit closed the ticket). Empty by default;
	// set via `ticket set --notes`/`--append-note` or appended on `ticket set-status
	// --note`. Surfaced in the Obsidian ticket-note top panel.
	`ALTER TABLE tickets ADD COLUMN notes TEXT NOT NULL DEFAULT '';`,
	// 16: threads.model — the agent model pinned to a thread (`thread new --model`),
	// applied on headed spawn, resume, and every headless turn. Opaque pass-through
	// string ('' = the agent's own default); no curated list. APPENDED last.
	`ALTER TABLE threads ADD COLUMN model TEXT NOT NULL DEFAULT '';`,
	// 17: threads.on_hold_until — park a thread until a future instant (unix secs);
	// 0 = not on hold. "On hold right now" is derived live (this > the daemon's
	// clock), so a thread auto-leaves hold once the instant passes — the store only
	// holds the absolute deadline the user set. APPENDED last.
	`ALTER TABLE threads ADD COLUMN on_hold_until INTEGER NOT NULL DEFAULT 0;`,
	// 18: threads.archived_at — the unix time a thread was most recently archived
	// (0 while un-archived). The daemon stamps it on the archive transition (the
	// caller passes `now`; a CASE preserves an existing value across an idempotent
	// re-archive and clears it to 0 on un-archive). The TUI's archived view orders by
	// it (most recently archived first). APPENDED last.
	`ALTER TABLE threads ADD COLUMN archived_at INTEGER NOT NULL DEFAULT 0;`,
	// 19: data fix — clear DANGLING parent ids. Historically DeleteThread left
	// children pointing at the deleted id (they silently rendered as roots and
	// could never be repaired, the grandparent being gone). DeleteThread now
	// promotes children in the same tx; this one-time sweep resets the already-
	// dangling ones to root ('' — the grandparent is unrecoverable for these).
	// APPENDED last.
	`UPDATE threads SET parent = '' WHERE parent != '' AND parent NOT IN (SELECT id FROM threads);`,
	// 20: threads.pin_order — the manual-ordering sort key (NULLABLE REAL; NULL = not
	// pinned, i.e. the thread sits in the auto-sorted block). A pinned top-level thread
	// renders above the auto block ordered by this fractional key; a divider always
	// carries one. The value is computed client-side from the merged cross-machine view
	// (the daemon only persists it). Cleared to NULL on archive (SetThreadArchived) and
	// on reparent-to-child (SetThreadParent). NULLABLE so 0/negative keys are valid keys,
	// distinct from "unpinned". APPENDED last.
	`ALTER TABLE threads ADD COLUMN pin_order REAL;`,
	// The flagged system (api schema 44, ticket df4fb07a): flagged = needs the
	// user's attention (auto-set on unattended turn ends / question stalls,
	// NEVER auto-cleared — manual unflag only); flag_reason = why (e.g. the
	// question the agent asked; cleared with the flag); flag_disabled =
	// suppress auto-flagging (parent-monitored children; manual flag-on
	// re-enables). One migration element, multi-statement (atomic in its tx).
	// APPENDED last (migrations are append-only).
	`ALTER TABLE threads ADD COLUMN flagged INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE threads ADD COLUMN flag_reason TEXT NOT NULL DEFAULT '';
	 ALTER TABLE threads ADD COLUMN flag_disabled INTEGER NOT NULL DEFAULT 0;`,
	// 23: THE MESH SCALE PASS (_dev/MESH_SCALE.md) — replace the per-machine
	// peer-snapshot JSON blob with PER-THREAD rows, so a one-row delta costs a
	// one-row write (the blob forced a full re-marshal + full rewrite of every
	// peer thread on ANY change — 1.4 MB of flash per active round on the
	// phone), plus the `revs` change counter + triggers that let the maintainer
	// sweep only live threads. Existing blobs are converted via JSON1
	// (json_each), guarded by json_valid: a corrupt cache row is SKIPPED, the
	// same as the old decode path skipped an undecodable blob — the peer cache
	// is derived data and the next sync heals it. peer_snapshots is then
	// DROPPED deliberately: a rolled-back binary must fail LOUDLY ("no such
	// table") rather than silently serve a frozen stale blob; the rollback
	// recipe (recreate the empty table, resync refills it in seconds) is in
	// _dev/MESH_SCALE.md. NB the triggers attach to the CURRENT threads/tickets
	// tables — any future rebuild-by-copy-rename migration (the #12 pattern)
	// silently drops them and MUST recreate them. APPENDED last.
	`CREATE TABLE peer_threads (
		machine  TEXT NOT NULL,
		id       TEXT NOT NULL,
		snapshot TEXT NOT NULL,
		PRIMARY KEY (machine, id)
	);
	CREATE TABLE peer_meta (
		machine        TEXT PRIMARY KEY,
		synced_at_unix INTEGER NOT NULL,
		reachable      INTEGER NOT NULL
	);
	INSERT INTO peer_meta (machine, synced_at_unix, reachable)
		SELECT machine, synced_at_unix, reachable FROM peer_snapshots;
	INSERT OR REPLACE INTO peer_threads (machine, id, snapshot)
		SELECT ps.machine, json_extract(je.value, '$.id'), je.value
		FROM peer_snapshots ps, json_each(ps.payload) je
		WHERE json_valid(ps.payload) AND json_extract(je.value, '$.id') IS NOT NULL;
	DROP TABLE peer_snapshots;
	CREATE TABLE revs (name TEXT PRIMARY KEY, rev INTEGER NOT NULL);
	INSERT INTO revs (name, rev) VALUES ('threads', 0);
	CREATE TRIGGER threads_rev_ins AFTER INSERT ON threads BEGIN UPDATE revs SET rev = rev + 1 WHERE name = 'threads'; END;
	CREATE TRIGGER threads_rev_upd AFTER UPDATE ON threads BEGIN UPDATE revs SET rev = rev + 1 WHERE name = 'threads'; END;
	CREATE TRIGGER threads_rev_del AFTER DELETE ON threads BEGIN UPDATE revs SET rev = rev + 1 WHERE name = 'threads'; END;
	CREATE TRIGGER tickets_rev_ins AFTER INSERT ON tickets BEGIN UPDATE revs SET rev = rev + 1 WHERE name = 'threads'; END;
	CREATE TRIGGER tickets_rev_upd AFTER UPDATE ON tickets BEGIN UPDATE revs SET rev = rev + 1 WHERE name = 'threads'; END;
	CREATE TRIGGER tickets_rev_del AFTER DELETE ON tickets BEGIN UPDATE revs SET rev = rev + 1 WHERE name = 'threads'; END;`,
	// 24: peer_threads WITHOUT ROWID. As a rowid table its (machine, id) PRIMARY
	// KEY was a SEPARATE autoindex, so a one-row delta upsert dirtied two b-tree
	// leaves (table + index) — measured 12–16 KB of WAL per round with the
	// in-transaction meta touch, i.e. ~30–50 KB/s of flash on the phone at
	// active cadence. A clustered WITHOUT ROWID table is ONE leaf per row (4 KB
	// per round, the meta touch now batched into the periodic flush). Rebuild
	// by copy-rename (no triggers live on this table). APPENDED last.
	`CREATE TABLE peer_threads_new (
		machine  TEXT NOT NULL,
		id       TEXT NOT NULL,
		snapshot TEXT NOT NULL,
		PRIMARY KEY (machine, id)
	) WITHOUT ROWID;
	INSERT INTO peer_threads_new (machine, id, snapshot) SELECT machine, id, snapshot FROM peer_threads;
	DROP TABLE peer_threads;
	ALTER TABLE peer_threads_new RENAME TO peer_threads;`,
	// 25: threads.hold_release_until — the RELEASE deadline (api 48). Since the
	// hold-inheritance rule (effective = max(own, ancestors' own)) a thread's own
	// hold had only two states, set or 0, so a child could never be un-held while
	// an ancestor held it: clearing its own hold left it parked and reported
	// success. While now < this value the thread ignores ancestor holds and the
	// inheritance walk stops at it (its subtree is freed with it). Mutually
	// exclusive with on_hold_until — SetThreadHold writes BOTH columns in one
	// statement, so the exclusivity is structural rather than a convention.
	// Dated like a hold, so it auto-expires. APPENDED last.
	`ALTER TABLE threads ADD COLUMN hold_release_until INTEGER NOT NULL DEFAULT 0;`,
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
