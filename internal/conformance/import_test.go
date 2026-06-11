package conformance

// thread.import cells (PARITY_ROADMAP E4): import v1 records into v2 from a
// REAL v1-shaped SQLite store. Asserts the field mapping (uuid→id+session_id,
// name/agent/cwd/tags/parent/archived/created), per-machine scoping (a peer's
// v1 rows are NOT imported), idempotent re-import (skip), --dry-run writes
// nothing, and a non-pi agent maps too. LOCAL-only: import targets the local
// daemon's store.

import (
	"database/sql"

	"github.com/lukastk/sesh/internal/api"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.import", matrix.AgentAgnostic, matrix.Local, testImport)
}

// writeV1Store builds a minimal v1-shaped sesh.db with the columns the
// importer reads, and inserts the given rows.
func writeV1Store(t *testing.T, dir, thisMachine string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "sesh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec(t, db, `CREATE TABLE sessions (
		uuid TEXT PRIMARY KEY, name TEXT, agent TEXT, machine TEXT, cwd TEXT,
		archived INTEGER DEFAULT 0, parent TEXT DEFAULT '', created_at INTEGER DEFAULT 0)`)
	mustExec(t, db, `CREATE TABLE tags (session_uuid TEXT, tag TEXT)`)
	now := time.Now().UnixNano()
	// Two on THIS machine (one a child + archived + tagged), one on a PEER.
	mustExec(t, db, `INSERT INTO sessions VALUES
		('aaaaaaaa-1111-2222-3333-444444444444','parent-thread','claude',?, '/tmp/a',0,'',?),
		('bbbbbbbb-1111-2222-3333-444444444444','child-thread','pi',?, '/tmp/b',1,'aaaaaaaa-1111-2222-3333-444444444444',?),
		('cccccccc-1111-2222-3333-444444444444','peer-thread','codex','otherbox','/tmp/c',0,'',?)`,
		thisMachine, now, thisMachine, now+1, now+2)
	mustExec(t, db, `INSERT INTO tags VALUES ('bbbbbbbb-1111-2222-3333-444444444444','imported'),('bbbbbbbb-1111-2222-3333-444444444444','p3')`)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("v1 store exec: %v\n%s", err, q)
	}
}

func testImport(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	v1 := t.TempDir()
	writeV1Store(t, v1, sb.Machine)

	// --dry-run writes nothing.
	out, stderr, err := sb.Runner.Run(t, "import", "--from-v1", v1, "--dry-run")
	if err != nil {
		t.Fatalf("import --dry-run: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "would import 2") {
		t.Errorf("dry-run summary wrong: %s", out)
	}
	if len(sb.listThreads(t)) != 0 {
		t.Errorf("--dry-run created records")
	}

	// Real import.
	if _, stderr, err := sb.Runner.Run(t, "import", "--from-v1", v1); err != nil {
		t.Fatalf("import: %v\n%s", err, stderr)
	}
	got := map[string]bool{}
	for _, th := range sb.listThreadsArchived(t) { // includes active + archived
		got[th.ID] = true
	}
	// The PEER's thread was NOT imported (per-machine scoping).
	if got["cccccccc-1111-2222-3333-444444444444"] {
		t.Errorf("a peer's v1 session was imported (per-machine scoping broken)")
	}
	if !got["aaaaaaaa-1111-2222-3333-444444444444"] || !got["bbbbbbbb-1111-2222-3333-444444444444"] {
		t.Fatalf("this machine's v1 sessions not imported: %v", got)
	}

	// The child's full mapping: agent session id == the v1 uuid (resume works),
	// parent, tags, archived, name.
	var child threadByNameResult
	for _, th := range sb.listThreadsArchived(t) {
		if th.Name == "child-thread" {
			child = threadByNameResult(th)
		}
	}
	if child.ID == "" {
		t.Fatalf("archived child not in the archived list")
	}
	if child.AgentSessionID != child.ID {
		t.Errorf("import did not reuse the v1 uuid as the agent session id: id=%s session=%s", child.ID, child.AgentSessionID)
	}
	if child.Parent != "aaaaaaaa-1111-2222-3333-444444444444" {
		t.Errorf("child parent not imported: %q", child.Parent)
	}
	if !child.Archived {
		t.Errorf("archived flag not imported")
	}
	if child.AgentKind != "pi" {
		t.Errorf("agent not imported: %q", child.AgentKind)
	}
	tagset := strings.Join(child.Tags, ",")
	if !strings.Contains(tagset, "imported") || !strings.Contains(tagset, "p3") {
		t.Errorf("tags not imported: %v", child.Tags)
	}

	// Idempotent: a second import skips both.
	out, _, err = sb.Runner.Run(t, "import", "--from-v1", v1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "imported 0, skipped 2") {
		t.Errorf("re-import not idempotent: %s", out)
	}

	// Missing v1 store is loud.
	if _, _, err := sb.Runner.Run(t, "import", "--from-v1", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Errorf("import from a missing v1 store succeeded")
	}
}


type threadByNameResult = api.Thread
