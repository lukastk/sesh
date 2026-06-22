package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A faithful fixture of claude's compaction layout (real compaction can't be triggered
// in-test, so we craft the exact on-disk shape verified live): a compaction-born file
// BEGINS with meta lines then a compact_boundary whose logicalParentUuid is a message
// uuid that lives — as an original message — in the PARENT file, and the parent's last
// preserved message is REPLAYED near the top of the child. These tests assert
// ResolveLeafSession follows the chain to the leaf, returns the stored id when there's
// no compaction, keeps independent chains in one cwd separate, and never self-links an
// in-session compaction (the cycle the naive "other file with the uuid" rule would hit).

func metaJSON(t string) string { return `{"type":"` + t + `"}` }
func msgLine(uuid, parent string) string {
	return `{"type":"user","uuid":"` + uuid + `","parentUuid":"` + parent + `"}`
}
func boundaryLine(lpu, uuid string) string {
	return `{"type":"system","subtype":"compact_boundary","logicalParentUuid":"` + lpu + `","uuid":"` + uuid + `","parentUuid":null}`
}

// writeSession writes <claudeHome>/projects/<enc cwd>/<sid>.jsonl from raw lines.
func writeSession(t *testing.T, claudeHome, cwd, sid string, lines ...string) {
	t.Helper()
	dir := ProjectDir(claudeHome, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLeafSessionLinearChain(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/u/dev/proj"

	// root: a normal session (no opening boundary). Its last preserved message before
	// the first compaction is uuid "m-root-leaf".
	writeSession(t, home, cwd, "root",
		metaJSON("mode"), metaJSON("file-history-snapshot"),
		msgLine("m-root-1", ""), msgLine("m-root-leaf", "m-root-1"))

	// childA: born by compaction off root (lpu = m-root-leaf). The replayed copy of
	// m-root-leaf is near the top; childA then continues with its own new content,
	// ending at m-A-leaf (the branch point for the next compaction).
	writeSession(t, home, cwd, "childA",
		metaJSON("mode"), metaJSON("file-history-snapshot"),
		boundaryLine("m-root-leaf", "b-A"),
		`{"type":"user","uuid":"m-root-leaf","isCompactSummary":true,"parentUuid":"b-A"}`,
		msgLine("m-A-1", "m-root-leaf"), msgLine("m-A-leaf", "m-A-1"))

	// childB: born by compaction off childA (lpu = m-A-leaf). The leaf.
	writeSession(t, home, cwd, "childB",
		metaJSON("mode"), metaJSON("file-history-snapshot"),
		boundaryLine("m-A-leaf", "b-B"),
		`{"type":"user","uuid":"m-A-leaf","isCompactSummary":true,"parentUuid":"b-B"}`,
		msgLine("m-B-1", "m-A-leaf"))

	for _, stored := range []string{"root", "childA", "childB"} {
		got, err := ResolveLeafSession(home, cwd, stored)
		if err != nil {
			t.Fatalf("resolve(%s): %v", stored, err)
		}
		if got != "childB" {
			t.Errorf("resolve(%s) = %q, want leaf childB", stored, got)
		}
	}
}

func TestResolveLeafSessionNoCompaction(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/u/dev/proj"
	writeSession(t, home, cwd, "solo",
		metaJSON("mode"), msgLine("m1", ""), msgLine("m2", "m1"))
	got, err := ResolveLeafSession(home, cwd, "solo")
	if err != nil {
		t.Fatal(err)
	}
	if got != "solo" {
		t.Errorf("no-compaction resolve = %q, want the stored id solo", got)
	}
	// A stored id whose project dir does not exist resolves to itself (not an error).
	got, err = ResolveLeafSession(home, "/nonexistent/cwd", "ghost")
	if err != nil || got != "ghost" {
		t.Errorf("missing-dir resolve = (%q,%v), want (ghost,nil)", got, err)
	}
}

func TestResolveLeafSessionIndependentChains(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/u/dev/proj" // two threads, one cwd

	writeSession(t, home, cwd, "root1",
		metaJSON("mode"), msgLine("m1-1", ""), msgLine("m1-leaf", "m1-1"))
	writeSession(t, home, cwd, "childA1",
		metaJSON("mode"), boundaryLine("m1-leaf", "b1"),
		`{"type":"user","uuid":"m1-leaf","isCompactSummary":true,"parentUuid":"b1"}`,
		msgLine("m1-A", "m1-leaf"))

	writeSession(t, home, cwd, "root2",
		metaJSON("mode"), msgLine("m2-1", ""), msgLine("m2-leaf", "m2-1"))
	writeSession(t, home, cwd, "childA2",
		metaJSON("mode"), boundaryLine("m2-leaf", "b2"),
		`{"type":"user","uuid":"m2-leaf","isCompactSummary":true,"parentUuid":"b2"}`,
		msgLine("m2-A", "m2-leaf"))

	if got, _ := ResolveLeafSession(home, cwd, "root1"); got != "childA1" {
		t.Errorf("chain1 resolve = %q, want childA1", got)
	}
	if got, _ := ResolveLeafSession(home, cwd, "root2"); got != "childA2" {
		t.Errorf("chain2 resolve = %q, want childA2 (cross-chain contamination)", got)
	}
}

// TestResolveLeafSessionInSessionCompaction guards the cycle the naive rule hits: a
// NORMAL session that compacts IN-PLACE (deep boundary, same file id) whose
// logicalParentUuid is a message ORIGINAL to itself must not be treated as
// compaction-born nor self-link. From it we still follow the real chain to the leaf.
func TestResolveLeafSessionInSessionCompaction(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/u/dev/proj"

	// d300-like: real conversation FIRST, then a deep in-session boundary referencing
	// its own earlier message m-x, then continues. NOT born-by-compaction.
	writeSession(t, home, cwd, "parent",
		metaJSON("mode"), metaJSON("file-history-snapshot"),
		msgLine("p-1", ""), msgLine("m-x", "p-1"),
		boundaryLine("m-x", "b-inseg"),
		msgLine("p-after", "m-x"))

	// child: born off parent at m-x (the same uuid the in-session boundary referenced).
	writeSession(t, home, cwd, "child",
		metaJSON("mode"), boundaryLine("m-x", "b-child"),
		`{"type":"user","uuid":"m-x","isCompactSummary":true,"parentUuid":"b-child"}`,
		msgLine("c-1", "m-x"))

	if got, _ := ResolveLeafSession(home, cwd, "parent"); got != "child" {
		t.Errorf("resolve(parent) = %q, want child (in-session boundary must not break the chain)", got)
	}
	// parent must NOT be claimed as a child of anything (no self/cycle link).
	if got, _ := ResolveLeafSession(home, cwd, "child"); got != "child" {
		t.Errorf("resolve(child) = %q, want child (leaf; no cycle back to parent)", got)
	}
}
