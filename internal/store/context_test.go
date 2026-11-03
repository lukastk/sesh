package store

import (
	"errors"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func insertCtxThread(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.InsertThread(api.Thread{ID: id, Machine: "m", SessionName: "sesh_" + id, Cwd: "/x", AgentKind: "claude", CreatedAtUnix: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestThreadContextSetGetClear(t *testing.T) {
	s := openTestStore(t)
	insertCtxThread(t, s, "t1")

	if text, at, err := s.GetThreadContext("t1"); err != nil || text != "" || at != 0 {
		t.Fatalf("fresh thread: %q %d %v", text, at, err)
	}
	if _, _, err := s.GetThreadContext("nope"); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("missing thread: %v", err)
	}

	changed, err := s.SetThreadContext("t1", "be brief", 100, "")
	if err != nil || !changed {
		t.Fatalf("set: %v %v", changed, err)
	}
	th, _ := s.GetThread("t1")
	if th.ContextHash == "" || th.ContextHash != ContextHashOf("be brief") {
		t.Fatalf("row hash %q", th.ContextHash)
	}
	if text, at, _ := s.GetThreadContext("t1"); text != "be brief" || at != 100 {
		t.Fatalf("get after set: %q %d", text, at)
	}
	// The same text again is a no-op.
	if changed, err := s.SetThreadContext("t1", "be brief", 200, ""); err != nil || changed {
		t.Fatalf("idempotent set: %v %v", changed, err)
	}

	// Conditional writes.
	if _, err := s.SetThreadContext("t1", "x", 300, "none"); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("expect none on a thread WITH context: %v", err)
	}
	if _, err := s.SetThreadContext("t1", "x", 300, "deadbeefdeadbeef"); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("stale hash: %v", err)
	}
	if changed, err := s.SetThreadContext("t1", "be terse", 300, ContextHashOf("be brief")); err != nil || !changed {
		t.Fatalf("matching hash: %v %v", changed, err)
	}

	// Delivery state is independent of the text, and survives a clear.
	if err := s.SetContextDelivery("t1", "sess-a", ContextHashOf("be terse")); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.SetThreadContext("t1", "", 400, ""); err != nil || !changed {
		t.Fatalf("clear: %v %v", changed, err)
	}
	th, _ = s.GetThread("t1")
	if th.ContextHash != "" {
		t.Fatalf("cleared row still has hash %q", th.ContextHash)
	}
	if text, at, _ := s.GetThreadContext("t1"); text != "" || at != 0 {
		t.Fatalf("get after clear: %q %d", text, at)
	}
	if sess, hash, _ := s.ContextDelivery("t1"); sess != "sess-a" || hash != ContextHashOf("be terse") {
		t.Fatalf("delivery lost by clear: %q %q", sess, hash)
	}
	if _, err := s.SetThreadContext("t1", "y", 500, "none"); err != nil {
		t.Fatalf("expect none after a clear: %v", err)
	}
	if _, err := s.SetThreadContext("nope", "y", 1, ""); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("set on missing thread: %v", err)
	}
}

func TestThreadContextBumpsThreadsRevButDeliveryDoesNot(t *testing.T) {
	s := openTestStore(t)
	insertCtxThread(t, s, "t1")
	rev := func() int64 {
		var r int64
		if err := s.db.QueryRow(`SELECT rev FROM revs WHERE name = 'threads'`).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r0 := rev()
	if _, err := s.SetThreadContext("t1", "hello", 1, ""); err != nil {
		t.Fatal(err)
	}
	r1 := rev()
	if r1 == r0 {
		t.Fatal("setting a context must bump the threads rev (peers and the TUI read the column)")
	}
	if err := s.SetContextDelivery("t1", "sess", ContextHashOf("hello")); err != nil {
		t.Fatal(err)
	}
	if rev() != r1 {
		t.Fatal("recording a delivery must not bump the threads rev (it would churn the mesh on every prompt)")
	}
}

func TestDeleteThreadDropsContext(t *testing.T) {
	s := openTestStore(t)
	insertCtxThread(t, s, "t1")
	if _, err := s.SetThreadContext("t1", "hello", 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteThread("t1"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM thread_context`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("context rows left after delete: %d %v", n, err)
	}
}
