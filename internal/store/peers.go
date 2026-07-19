package store

import "fmt"

// PeerSnapshotRow is one cached peer machine's last-synced snapshot. Payload is a
// JSON array of api.ThreadSnapshot (decoded by the caller); the store stays
// schema-agnostic about its contents.
type PeerSnapshotRow struct {
	Machine      string
	SyncedAtUnix int64
	Reachable    bool
	Payload      string
}

// UpsertPeerSnapshot records a SUCCESSFUL sync of a peer: its threads payload,
// the sync time, and reachable=true.
func (s *Store) UpsertPeerSnapshot(machine string, syncedAtUnix int64, payload string) error {
	_, err := s.db.Exec(`
		INSERT INTO peer_snapshots (machine, synced_at_unix, reachable, payload)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(machine) DO UPDATE SET
			synced_at_unix = excluded.synced_at_unix,
			reachable      = 1,
			payload        = excluded.payload`,
		machine, syncedAtUnix, payload)
	if err != nil {
		return fmt.Errorf("store: upsert peer snapshot %q: %w", machine, err)
	}
	return nil
}

// TouchPeerSnapshot refreshes an existing peer cache entry's synced_at +
// reachable WITHOUT rewriting its payload — the 304 Not-Modified path of the
// mesh sync (the peer answered; its threads are byte-unchanged). Returns
// touched=false when the peer has no cache row (e.g. it was removed and
// re-added while the syncer's in-memory ETag survived) so the caller knows a
// full refetch is required — a silent no-op here would leave the peer
// permanently payload-less while looking freshly synced.
func (s *Store) TouchPeerSnapshot(machine string, syncedAtUnix int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE peer_snapshots SET synced_at_unix = ?, reachable = 1 WHERE machine = ?`,
		syncedAtUnix, machine)
	if err != nil {
		return false, fmt.Errorf("store: touch peer snapshot %q: %w", machine, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: touch peer snapshot %q: rows affected: %w", machine, err)
	}
	return n > 0, nil
}

// MarkPeerUnreachable flags an existing peer's cache entry stale (reachable=0)
// WITHOUT touching its payload or synced_at — so its last-known threads stay
// listable for offline browsing. No-op if the peer was never synced.
func (s *Store) MarkPeerUnreachable(machine string) error {
	_, err := s.db.Exec(`UPDATE peer_snapshots SET reachable = 0 WHERE machine = ?`, machine)
	if err != nil {
		return fmt.Errorf("store: mark peer unreachable %q: %w", machine, err)
	}
	return nil
}

// LoadPeerSnapshots returns every cached peer snapshot.
func (s *Store) LoadPeerSnapshots() ([]PeerSnapshotRow, error) {
	rows, err := s.db.Query(`SELECT machine, synced_at_unix, reachable, payload FROM peer_snapshots ORDER BY machine`)
	if err != nil {
		return nil, fmt.Errorf("store: load peer snapshots: %w", err)
	}
	defer rows.Close()
	var out []PeerSnapshotRow
	for rows.Next() {
		var r PeerSnapshotRow
		var reachable int
		if err := rows.Scan(&r.Machine, &r.SyncedAtUnix, &reachable, &r.Payload); err != nil {
			return nil, err
		}
		r.Reachable = reachable != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeletePeerSnapshot drops a peer from the cache (e.g. when it is removed from the
// registry).
func (s *Store) DeletePeerSnapshot(machine string) error {
	_, err := s.db.Exec(`DELETE FROM peer_snapshots WHERE machine = ?`, machine)
	if err != nil {
		return fmt.Errorf("store: delete peer snapshot %q: %w", machine, err)
	}
	return nil
}
