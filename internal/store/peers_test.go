package store

import (
	"testing"
)

// TestTouchPeerSnapshot covers the mesh sync's 304 path: freshness + reachability
// refresh WITHOUT rewriting the payload, and touched=false on a missing row (the
// caller must then refetch full — a silent no-op would leave the peer
// payload-less while looking freshly synced).
func TestTouchPeerSnapshot(t *testing.T) {
	s := openTestStore(t)

	if touched, err := s.TouchPeerSnapshot("ghost", 100); err != nil || touched {
		t.Fatalf("touch of a never-synced peer = (%v, %v), want (false, nil)", touched, err)
	}

	if err := s.UpsertPeerSnapshot("peer1", 100, `[{"id":"a"}]`); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.MarkPeerUnreachable("peer1"); err != nil {
		t.Fatalf("mark unreachable: %v", err)
	}

	touched, err := s.TouchPeerSnapshot("peer1", 200)
	if err != nil || !touched {
		t.Fatalf("touch = (%v, %v), want (true, nil)", touched, err)
	}
	rows, err := s.LoadPeerSnapshots()
	if err != nil || len(rows) != 1 {
		t.Fatalf("load: %v (%d rows)", err, len(rows))
	}
	r := rows[0]
	if r.SyncedAtUnix != 200 {
		t.Errorf("synced_at = %d, want 200 (touch must refresh freshness)", r.SyncedAtUnix)
	}
	if !r.Reachable {
		t.Errorf("reachable = false, want true (a 304 is a successful sync)")
	}
	if r.Payload != `[{"id":"a"}]` {
		t.Errorf("payload = %s, want untouched (a 304 means byte-unchanged)", r.Payload)
	}
}
