package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestThreadCRUD(t *testing.T) {
	s := openTestStore(t)
	th := api.Thread{
		ID: "t1", Machine: "mac", SessionName: "sesh_a", Cwd: "/x",
		AgentKind: "pi", Name: "a", Tags: []string{"foo", "bar"}, CreatedAtUnix: 123,
	}
	if err := s.InsertThread(th); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetThread("t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionName != "sesh_a" || len(got.Tags) != 2 || got.Tags[0] != "foo" {
		t.Fatalf("round trip wrong: %+v", got)
	}

	list, err := s.ListThreads()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}

	if err := s.DeleteThread("t1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetThread("t1"); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("want ErrThreadNotFound, got %v", err)
	}
}

func TestThreadDuplicateSessionRejected(t *testing.T) {
	s := openTestStore(t)
	a := api.Thread{ID: "a", Machine: "m", SessionName: "dup", Cwd: "/x", AgentKind: "pi", Name: "a", Tags: []string{}}
	b := api.Thread{ID: "b", Machine: "m", SessionName: "dup", Cwd: "/x", AgentKind: "pi", Name: "b", Tags: []string{}}
	if err := s.InsertThread(a); err != nil {
		t.Fatalf("insert a: %v", err)
	}
	if err := s.InsertThread(b); err == nil {
		t.Fatal("expected duplicate session_name to be rejected, got nil")
	}
}

func TestDeleteMissingThread(t *testing.T) {
	s := openTestStore(t)
	if err := s.DeleteThread("nope"); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("want ErrThreadNotFound, got %v", err)
	}
}

func TestHeadlessSession(t *testing.T) {
	s := openTestStore(t)
	th := api.Thread{ID: "h1", Machine: "m", SessionName: "headless-h1", Cwd: "/x", AgentKind: "codex", Name: "h", Tags: []string{}, Headless: true}
	if err := s.InsertThread(th); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _ := s.GetThread("h1")
	if !got.Headless || got.AgentSessionID != "" || got.HeadlessStarted {
		t.Fatalf("fresh headless wrong: %+v", got)
	}
	// First turn discovers codex's session id and marks started.
	if err := s.SetHeadlessSession("h1", "codex-sess-xyz"); err != nil {
		t.Fatalf("set headless session: %v", err)
	}
	got, _ = s.GetThread("h1")
	if got.AgentSessionID != "codex-sess-xyz" || !got.HeadlessStarted {
		t.Fatalf("after first turn wrong: %+v", got)
	}
}
