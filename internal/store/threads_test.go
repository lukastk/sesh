package store

import (
	"errors"
	"path/filepath"
	"strings"
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

	list, err := s.ListThreads(false)
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

// Many threads may share one tmux session (own windows, or splits in one
// window) — runtime identity is the pane's @sesh-thread-id marker, not the
// session name. So session_name is no longer unique (migration 12).
func TestThreadDuplicateSessionAllowed(t *testing.T) {
	s := openTestStore(t)
	a := api.Thread{ID: "a", Machine: "m", SessionName: "dup", Cwd: "/x", AgentKind: "pi", Name: "a", Tags: []string{}}
	b := api.Thread{ID: "b", Machine: "m", SessionName: "dup", Cwd: "/x", AgentKind: "pi", Name: "b", Tags: []string{}}
	if err := s.InsertThread(a); err != nil {
		t.Fatalf("insert a: %v", err)
	}
	if err := s.InsertThread(b); err != nil {
		t.Fatalf("two threads in one session must be allowed, got: %v", err)
	}
	all, err := s.ListThreads(true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	n := 0
	for _, th := range all {
		if th.SessionName == "dup" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 threads sharing session 'dup', got %d", n)
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
	th := api.Thread{ID: "h1", Machine: "m", SessionName: "headless-h1", Cwd: "/x", AgentKind: "codex", Name: "h", Tags: []string{}}
	if err := s.InsertThread(th); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _ := s.GetThread("h1")
	if got.AgentSessionID != "" || got.HeadlessStarted {
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

func TestThreadOpsRenameTagArchive(t *testing.T) {
	s := openTestStore(t)
	th := api.Thread{ID: "o1", Machine: "m", SessionName: "s1", Cwd: "/x", AgentKind: "pi", Name: "old", Tags: []string{}}
	if err := s.InsertThread(th); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameThread("o1", "new"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadTags("o1", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetThread("o1")
	if got.Name != "new" || len(got.Tags) != 2 {
		t.Fatalf("rename/tag wrong: %+v", got)
	}

	// archive hides from the active list but keeps the record, and stamps archived_at.
	if err := s.SetThreadArchived("o1", true, 1000); err != nil {
		t.Fatal(err)
	}
	active, _ := s.ListThreads(false)
	all, _ := s.ListThreads(true)
	if len(active) != 0 || len(all) != 1 {
		t.Fatalf("archive filter wrong: active=%d all=%d", len(active), len(all))
	}
	if got, _ := s.GetThread("o1"); got.ArchivedAtUnix != 1000 {
		t.Fatalf("archive did not stamp archived_at: got %d, want 1000", got.ArchivedAtUnix)
	}
	// An idempotent re-archive PRESERVES the original stamp (does not bump it).
	if err := s.SetThreadArchived("o1", true, 2000); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetThread("o1"); got.ArchivedAtUnix != 1000 {
		t.Fatalf("re-archive should preserve archived_at: got %d, want 1000", got.ArchivedAtUnix)
	}
	// Un-archive clears archived_at to 0.
	if err := s.SetThreadArchived("o1", false, 3000); err != nil {
		t.Fatal(err)
	}
	if active, _ := s.ListThreads(false); len(active) != 1 {
		t.Fatalf("unarchive failed: active=%d", len(active))
	}
	if got, _ := s.GetThread("o1"); got.ArchivedAtUnix != 0 {
		t.Fatalf("unarchive should clear archived_at: got %d, want 0", got.ArchivedAtUnix)
	}
	// A fresh archive after un-archive re-stamps (the cleared 0 lets the CASE fire).
	if err := s.SetThreadArchived("o1", true, 4000); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetThread("o1"); got.ArchivedAtUnix != 4000 {
		t.Fatalf("re-archive after unarchive should re-stamp: got %d, want 4000", got.ArchivedAtUnix)
	}
}

// TestThreadHold round-trips the hold columns: a thread starts neither held nor
// released, a set persists the absolute instant, and a clear returns both to 0.
// The hold and the release are MUTUALLY EXCLUSIVE by construction — one write
// statement sets both — so setting either must zero the other; a thread that could
// be held and released at once would have no defined effective state. The "on hold
// now" derivation lives at the daemon layer (against its clock); the store only
// persists deadlines.
func TestThreadHold(t *testing.T) {
	s := openTestStore(t)
	th := api.Thread{ID: "h1", Machine: "m", SessionName: "s1", Cwd: "/x", AgentKind: "pi", Name: "n", Tags: []string{}}
	if err := s.InsertThread(th); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetThread("h1"); got.OnHoldUntilUnix != 0 {
		t.Fatalf("new thread should not be on hold, got %d", got.OnHoldUntilUnix)
	}
	if err := s.SetThreadHold("h1", 1893456000, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetThread("h1"); got.OnHoldUntilUnix != 1893456000 {
		t.Fatalf("hold not persisted: %d", got.OnHoldUntilUnix)
	}
	// A RELEASE replaces the hold — never coexists with it.
	if err := s.SetThreadHold("h1", 0, 1893460000); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetThread("h1"); got.HoldReleaseUntilUnix != 1893460000 || got.OnHoldUntilUnix != 0 {
		t.Fatalf("release not persisted / hold not zeroed: release=%d hold=%d", got.HoldReleaseUntilUnix, got.OnHoldUntilUnix)
	}
	// ...and a hold replaces the release, the other direction.
	if err := s.SetThreadHold("h1", 1893456000, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetThread("h1"); got.OnHoldUntilUnix != 1893456000 || got.HoldReleaseUntilUnix != 0 {
		t.Fatalf("hold did not replace the release: hold=%d release=%d", got.OnHoldUntilUnix, got.HoldReleaseUntilUnix)
	}
	if err := s.SetThreadHold("h1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetThread("h1"); got.OnHoldUntilUnix != 0 || got.HoldReleaseUntilUnix != 0 {
		t.Fatalf("clear left state behind: hold=%d release=%d", got.OnHoldUntilUnix, got.HoldReleaseUntilUnix)
	}
	if err := s.SetThreadHold("nope", 123, 0); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("hold on missing thread: want ErrThreadNotFound, got %v", err)
	}
}

func TestDeleteThreadPromotesChildren(t *testing.T) {
	s := openTestStore(t)
	mk := func(id, parent string) {
		t.Helper()
		if err := s.InsertThread(api.Thread{ID: id, Machine: "m", SessionName: "s-" + id, Cwd: "/x", AgentKind: "pi", Name: id, Tags: []string{}, Parent: parent}); err != nil {
			t.Fatal(err)
		}
	}
	mk("g", "")
	mk("p", "g")
	mk("c1", "p")
	mk("c2", "p")
	mk("u", "")

	// Deleting a mid-tree node promotes its children to the grandparent.
	if err := s.DeleteThread("p"); err != nil {
		t.Fatalf("delete p: %v", err)
	}
	for _, id := range []string{"c1", "c2"} {
		if got, _ := s.GetThread(id); got.Parent != "g" {
			t.Fatalf("%s: want parent g after promote, got %q", id, got.Parent)
		}
	}
	if got, _ := s.GetThread("u"); got.Parent != "" {
		t.Fatalf("unrelated thread reparented: %q", got.Parent)
	}
	// Deleting a root promotes its children to root.
	if err := s.DeleteThread("g"); err != nil {
		t.Fatalf("delete g: %v", err)
	}
	if got, _ := s.GetThread("c1"); got.Parent != "" {
		t.Fatalf("c1: want root after deleting g, got %q", got.Parent)
	}
	if err := s.DeleteThread("nope"); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("delete missing: want ErrThreadNotFound, got %v", err)
	}
}

func TestRealizeThread(t *testing.T) {
	s := openTestStore(t)
	if err := s.InsertThread(api.Thread{ID: "v1", Machine: "m", SessionName: "virtual-v1", Cwd: "", AgentKind: api.VirtualAgentKind, Name: "group", Tags: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RealizeThread("v1", "pi", "sess-123", "/work", "headless-v1", "somemodel"); err != nil {
		t.Fatalf("realize: %v", err)
	}
	got, err := s.GetThread("v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentKind != "pi" || got.AgentSessionID != "sess-123" || got.Cwd != "/work" || got.SessionName != "headless-v1" || got.Model != "somemodel" {
		t.Fatalf("realize did not apply: %+v", got)
	}
	if got.HeadlessStarted {
		t.Fatal("a realized thread must be never-started (first turn creates the conversation)")
	}
	// Realizing a NON-virtual thread must refuse (the WHERE guard — also the
	// concurrent double-realize case).
	if err := s.RealizeThread("v1", "claude", "x", "/work", "headless-v1", ""); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("realize non-virtual: want ErrThreadNotFound, got %v", err)
	}
	if err := s.RealizeThread("missing", "pi", "x", "/work", "h", ""); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("realize missing: want ErrThreadNotFound, got %v", err)
	}
}

// TestMigrationClearsDanglingParents proves the one-time data-fix sweep
// (migration 19): a parent id pointing at no existing thread is reset to root,
// while valid parent links survive. Simulated by rolling meta.version back to
// the pre-sweep version and re-running migrate on rows seeded with a dangler.
// TestThreadPin covers the manual-ordering key: set/reposition, unpin (clear),
// and the two implicit clears — archiving a pinned thread and reparenting it under
// another thread both drop pin_order back to NULL.
func TestThreadPin(t *testing.T) {
	s := openTestStore(t)
	mk := func(id, parent string) {
		t.Helper()
		if err := s.InsertThread(api.Thread{ID: id, Machine: "m", SessionName: "s-" + id, Cwd: "/x", AgentKind: "pi", Name: id, Tags: []string{}, Parent: parent}); err != nil {
			t.Fatal(err)
		}
	}
	pinOf := func(id string) *float64 {
		t.Helper()
		th, err := s.GetThread(id)
		if err != nil {
			t.Fatal(err)
		}
		return th.PinOrder
	}
	mk("root", "")
	mk("a", "")

	if pinOf("a") != nil {
		t.Fatalf("fresh thread should be unpinned, got %v", *pinOf("a"))
	}
	// Set — including a 0 key, which must read back as pinned (NOT "unpinned").
	zero := 0.0
	if err := s.SetThreadPin("a", &zero); err != nil {
		t.Fatal(err)
	}
	if p := pinOf("a"); p == nil || *p != 0 {
		t.Fatalf("pin=0: want &0, got %v", p)
	}
	// Reposition.
	v := -2.5
	if err := s.SetThreadPin("a", &v); err != nil {
		t.Fatal(err)
	}
	if p := pinOf("a"); p == nil || *p != -2.5 {
		t.Fatalf("reposition: want &-2.5, got %v", p)
	}
	// Unpin.
	if err := s.SetThreadPin("a", nil); err != nil {
		t.Fatal(err)
	}
	if p := pinOf("a"); p != nil {
		t.Fatalf("unpin: want nil, got %v", *p)
	}
	// Archive clears the pin.
	if err := s.SetThreadPin("a", &v); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadArchived("a", true, 100); err != nil {
		t.Fatal(err)
	}
	if p := pinOf("a"); p != nil {
		t.Fatalf("archive should clear pin, got %v", *p)
	}
	// Reparent under another thread clears the pin; reparent-to-root leaves it.
	if err := s.SetThreadArchived("a", false, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadPin("a", &v); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadParent("a", "root"); err != nil {
		t.Fatal(err)
	}
	if p := pinOf("a"); p != nil {
		t.Fatalf("reparent-to-child should clear pin, got %v", *p)
	}
	if err := s.SetThreadPin("root", &v); err != nil {
		t.Fatal(err)
	}
	if err := s.SetThreadParent("root", ""); err != nil {
		t.Fatal(err)
	}
	if p := pinOf("root"); p == nil || *p != -2.5 {
		t.Fatalf("reparent-to-root must keep pin, got %v", p)
	}
	if err := s.SetThreadPin("missing", &v); !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("pin missing: want ErrThreadNotFound, got %v", err)
	}
}

func TestMigrationClearsDanglingParents(t *testing.T) {
	s := openTestStore(t)
	mk := func(id, parent string) {
		t.Helper()
		if err := s.InsertThread(api.Thread{ID: id, Machine: "m", SessionName: "s-" + id, Cwd: "/x", AgentKind: "pi", Name: id, Tags: []string{}, Parent: parent}); err != nil {
			t.Fatal(err)
		}
	}
	mk("a", "")
	mk("b", "a")
	mk("c", "ghost") // dangling: ghost never existed (historical delete)
	// Re-run EXACTLY the dangling-parent sweep (migration 19). It's an idempotent
	// UPDATE, so applying it directly is faithful — and unlike re-running migrate()
	// from an earlier version, it doesn't re-apply the later non-idempotent
	// migrations (e.g. the ADD COLUMN steps that followed it). Located by content so
	// the test survives further appends.
	var sweep string
	for _, m := range migrations {
		if strings.Contains(m, "parent NOT IN (SELECT id FROM threads)") {
			sweep = m
		}
	}
	if sweep == "" {
		t.Fatal("dangling-parent sweep migration not found")
	}
	if _, err := s.db.Exec(sweep); err != nil {
		t.Fatalf("re-run sweep: %v", err)
	}
	if got, _ := s.GetThread("c"); got.Parent != "" {
		t.Fatalf("dangling parent not cleared: %q", got.Parent)
	}
	if got, _ := s.GetThread("b"); got.Parent != "a" {
		t.Fatalf("valid parent link damaged: %q", got.Parent)
	}
}
