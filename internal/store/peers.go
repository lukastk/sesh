package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lukastk/sesh/internal/api"
)

// The peer cache is PER-THREAD rows (peer_threads) + per-machine freshness meta
// (peer_meta) — the mesh scale pass (_dev/MESH_SCALE.md). It replaced a single
// JSON blob per machine, which forced every one-row delta to re-marshal and
// rewrite the machine's ENTIRE thread set. A row change now costs a row write;
// archived threads (the overwhelming majority of the mesh) cost a disk row and
// nothing per-tick. The store stays dumb here: diffing/eventing live in the
// daemon's meshView, which is the in-memory authority — these tables are its
// durable backing (boot seed + offline browsing across restarts).

// PeerMeta is one peer machine's cache freshness row.
type PeerMeta struct {
	Machine      string
	SyncedAtUnix int64
	Reachable    bool
}

// PeerCache is one peer machine's full cached state, as loaded at boot.
type PeerCache struct {
	PeerMeta
	Threads []api.ThreadSnapshot
}

// ReplacePeerThreads records a FULL successful sync of a peer: its complete
// thread set replaces any cached rows, and the meta row flips fresh+reachable —
// all in one transaction, so a reader never sees a half-replaced set.
func (s *Store) ReplacePeerThreads(machine string, syncedAtUnix int64, threads []api.ThreadSnapshot) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: replace peer threads %q: begin: %w", machine, err)
	}
	defer tx.Rollback() //nolint:errcheck — no-op after Commit
	if _, err := tx.Exec(`DELETE FROM peer_threads WHERE machine = ?`, machine); err != nil {
		return fmt.Errorf("store: replace peer threads %q: clear: %w", machine, err)
	}
	for _, th := range threads {
		b, err := json.Marshal(th)
		if err != nil {
			return fmt.Errorf("store: replace peer threads %q: marshal %s: %w", machine, th.ID, err)
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO peer_threads (machine, id, snapshot) VALUES (?, ?, ?)`,
			machine, th.ID, string(b)); err != nil {
			return fmt.Errorf("store: replace peer threads %q: insert %s: %w", machine, th.ID, err)
		}
	}
	if err := upsertPeerMetaTx(tx, machine, syncedAtUnix, true); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: replace peer threads %q: commit: %w", machine, err)
	}
	return nil
}

// UpsertPeerThreads applies a DELTA sync round: removals first (a re-created id
// appears in both lists), then changed-row upserts — one transaction. This is
// the O(changed) write path that replaced the full-blob rewrite. It deliberately
// does NOT touch peer_meta: that would dirty a second page on every round for a
// freshness value the view holds authoritatively and flushMeta persists within
// 60 s (a crash can only under-claim boot freshness — the safe direction).
func (s *Store) UpsertPeerThreads(machine string, changed []api.ThreadSnapshot, removed []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert peer threads %q: begin: %w", machine, err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, id := range removed {
		if _, err := tx.Exec(`DELETE FROM peer_threads WHERE machine = ? AND id = ?`, machine, id); err != nil {
			return fmt.Errorf("store: upsert peer threads %q: remove %s: %w", machine, id, err)
		}
	}
	for _, th := range changed {
		b, err := json.Marshal(th)
		if err != nil {
			return fmt.Errorf("store: upsert peer threads %q: marshal %s: %w", machine, th.ID, err)
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO peer_threads (machine, id, snapshot) VALUES (?, ?, ?)`,
			machine, th.ID, string(b)); err != nil {
			return fmt.Errorf("store: upsert peer threads %q: upsert %s: %w", machine, th.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: upsert peer threads %q: commit: %w", machine, err)
	}
	return nil
}

func upsertPeerMetaTx(tx *sql.Tx, machine string, syncedAtUnix int64, reachable bool) error {
	r := 0
	if reachable {
		r = 1
	}
	if _, err := tx.Exec(`
		INSERT INTO peer_meta (machine, synced_at_unix, reachable) VALUES (?, ?, ?)
		ON CONFLICT(machine) DO UPDATE SET
			synced_at_unix = excluded.synced_at_unix,
			reachable      = excluded.reachable`, machine, syncedAtUnix, r); err != nil {
		return fmt.Errorf("store: upsert peer meta %q: %w", machine, err)
	}
	return nil
}

// SetPeerMeta persists a peer's freshness/reachability alone (the periodic
// meta flush + clean-shutdown flush; content transitions persist theirs
// in-transaction above). Creates the row if absent.
func (s *Store) SetPeerMeta(machine string, syncedAtUnix int64, reachable bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: set peer meta %q: begin: %w", machine, err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := upsertPeerMetaTx(tx, machine, syncedAtUnix, reachable); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set peer meta %q: commit: %w", machine, err)
	}
	return nil
}

// MarkPeerUnreachable flags an existing peer's cache stale (reachable=0)
// WITHOUT touching its rows or synced_at — its last-known threads stay
// listable for offline browsing. No-op if the peer was never synced.
func (s *Store) MarkPeerUnreachable(machine string) error {
	_, err := s.db.Exec(`UPDATE peer_meta SET reachable = 0 WHERE machine = ?`, machine)
	if err != nil {
		return fmt.Errorf("store: mark peer unreachable %q: %w", machine, err)
	}
	return nil
}

// DeletePeerMachine drops a peer machine from the cache entirely (rows + meta,
// one transaction) — the peer-removal path.
func (s *Store) DeletePeerMachine(machine string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete peer machine %q: begin: %w", machine, err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM peer_threads WHERE machine = ?`, machine); err != nil {
		return fmt.Errorf("store: delete peer machine %q: rows: %w", machine, err)
	}
	if _, err := tx.Exec(`DELETE FROM peer_meta WHERE machine = ?`, machine); err != nil {
		return fmt.Errorf("store: delete peer machine %q: meta: %w", machine, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete peer machine %q: commit: %w", machine, err)
	}
	return nil
}

// LoadPeerCache loads the whole peer cache — every machine's meta + decoded
// thread rows — for the daemon's boot-time view seed. This is the ONE
// remaining full decode, paid once per daemon start. A row whose snapshot no
// longer decodes is skipped (derived data; the next sync heals it); a machine
// with thread rows but no meta row (shouldn't happen — content writes carry
// meta in-tx) is surfaced with zero meta rather than dropped.
func (s *Store) LoadPeerCache() ([]PeerCache, error) {
	metas, err := s.LoadPeerMetas()
	if err != nil {
		return nil, err
	}
	byMachine := map[string]*PeerCache{}
	order := []string{}
	for _, m := range metas {
		byMachine[m.Machine] = &PeerCache{PeerMeta: m}
		order = append(order, m.Machine)
	}
	rows, err := s.db.Query(`SELECT machine, snapshot FROM peer_threads ORDER BY machine, id`)
	if err != nil {
		return nil, fmt.Errorf("store: load peer cache: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var machine, snapshot string
		if err := rows.Scan(&machine, &snapshot); err != nil {
			return nil, err
		}
		pc := byMachine[machine]
		if pc == nil {
			pc = &PeerCache{PeerMeta: PeerMeta{Machine: machine}}
			byMachine[machine] = pc
			order = append(order, machine)
		}
		var th api.ThreadSnapshot
		if json.Unmarshal([]byte(snapshot), &th) != nil {
			continue // stale/undecodable cached row: skip, resync fixes it
		}
		pc.Threads = append(pc.Threads, th)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PeerCache, 0, len(order))
	for _, m := range order {
		out = append(out, *byMachine[m])
	}
	return out, nil
}

// LoadPeerMetas returns every peer machine's freshness row.
func (s *Store) LoadPeerMetas() ([]PeerMeta, error) {
	rows, err := s.db.Query(`SELECT machine, synced_at_unix, reachable FROM peer_meta ORDER BY machine`)
	if err != nil {
		return nil, fmt.Errorf("store: load peer metas: %w", err)
	}
	defer rows.Close()
	var out []PeerMeta
	for rows.Next() {
		var m PeerMeta
		var reachable int
		if err := rows.Scan(&m.Machine, &m.SyncedAtUnix, &reachable); err != nil {
			return nil, err
		}
		m.Reachable = reachable != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// ThreadsRev reads the record-change counter bumped by the migration-23
// triggers on every threads/tickets write. The maintainer polls it (one
// integer read per tick) to decide whether a FULL record sweep is needed —
// unchanged rev means no record mutated, so only live-runtime threads need
// re-deriving (_dev/MESH_SCALE.md C3).
func (s *Store) ThreadsRev() (int64, error) {
	var rev int64
	if err := s.db.QueryRow(`SELECT rev FROM revs WHERE name = 'threads'`).Scan(&rev); err != nil {
		return 0, fmt.Errorf("store: threads rev: %w", err)
	}
	return rev, nil
}
