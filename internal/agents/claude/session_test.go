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
func msgLineTS(uuid, parent, ts string) string {
	return `{"type":"user","uuid":"` + uuid + `","parentUuid":"` + parent + `","timestamp":"` + ts + `"}`
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

// A resume/rewind fork: claude copies the conversation into a NEW file (preserving each
// message's uuid — only the per-line sessionId is rewritten) and continues there, with
// NO compaction boundary. So the fork shares its FIRST conversation-message uuid (the
// root) with the frozen original. This faithfully reproduces the "duplicate session"
// bug: the stale anchor (the spawn-minted id) is frozen mid-conversation while the live
// conversation continued in the fork. ResolveLeafSession must advance the anchor to the
// fork (the most-recently-extended file of the shared-root lineage).
func TestResolveLeafSessionResumeFork(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/u/dev/proj"

	// anchor: the spawn-minted session, frozen at the fork point (ends 12:40).
	writeSession(t, home, cwd, "0d3dc297",
		metaJSON("mode"), metaJSON("file-history-snapshot"),
		msgLineTS("m-root", "", "2026-06-24T08:32:03Z"),
		msgLineTS("m-2", "m-root", "2026-06-24T12:40:21Z"))

	// fork: same root uuid (copied), continues past the fork to 12:59 = the live tip.
	writeSession(t, home, cwd, "dedd01d1",
		metaJSON("mode"), metaJSON("file-history-snapshot"),
		msgLineTS("m-root", "", "2026-06-24T08:32:03Z"),
		msgLineTS("m-2", "m-root", "2026-06-24T12:40:21Z"),
		msgLineTS("m-3", "m-2", "2026-06-24T12:59:27Z"))

	for _, stored := range []string{"0d3dc297", "dedd01d1"} {
		got, err := ResolveLeafSession(home, cwd, stored)
		if err != nil {
			t.Fatalf("resolve(%s): %v", stored, err)
		}
		if got != "dedd01d1" {
			t.Errorf("resolve(%s) = %q, want the live fork dedd01d1", stored, got)
		}
	}
}

// A resume fork must not contaminate an unrelated conversation in the same cwd: the
// lineage key is the conversation-root uuid, so distinct conversations stay separate.
func TestResolveLeafSessionResumeForkIndependent(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/u/dev/proj"

	writeSession(t, home, cwd, "A0",
		metaJSON("mode"), msgLineTS("rootA", "", "2026-06-24T08:00:00Z"))
	writeSession(t, home, cwd, "A1",
		metaJSON("mode"),
		msgLineTS("rootA", "", "2026-06-24T08:00:00Z"),
		msgLineTS("a-2", "rootA", "2026-06-24T09:00:00Z"))

	// A second, unrelated conversation (different root) — newer ts, but not in A's lineage.
	writeSession(t, home, cwd, "B0",
		metaJSON("mode"),
		msgLineTS("rootB", "", "2026-06-24T10:00:00Z"),
		msgLineTS("b-2", "rootB", "2026-06-24T11:00:00Z"))

	if got, _ := ResolveLeafSession(home, cwd, "A0"); got != "A1" {
		t.Errorf("resolve(A0) = %q, want A1 (its own fork tip, not the newer unrelated B0)", got)
	}
	if got, _ := ResolveLeafSession(home, cwd, "B0"); got != "B0" {
		t.Errorf("resolve(B0) = %q, want B0 (sole file of its conversation)", got)
	}
}

func bgAgentHeader(title string) []string {
	return []string{
		`{"type":"ai-title","aiTitle":"` + title + `"}`,
		`{"type":"agent-name","agentName":"` + title + `"}`,
	}
}

// A claude BACKGROUND AGENT (`--fork-session --resume <parent>`) copies the parent
// conversation (same root uuid) into a new session id and keeps extending it — so by the
// most-recent-tip heuristic it looks like the parent thread's live fork. It is NOT: it is an
// independent, often still-running sibling, and resolving a thread onto it makes `claude
// --resume` fail ("still running as a background agent"). Its file opens with an
// ai-title/agent-name header a normal session never has; the resolver must drop it so the
// thread stays on its OWN conversation. (This is the exact corkboard-thread bug.)
func TestResolveLeafSessionSkipsBackgroundAgentFork(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/u/dev/proj"

	// anchor: the thread's real conversation, last extended 12:40.
	writeSession(t, home, cwd, "anchor",
		metaJSON("mode"), metaJSON("file-history-snapshot"),
		msgLineTS("m-root", "", "2026-06-24T08:32:03Z"),
		msgLineTS("m-2", "m-root", "2026-06-24T12:40:21Z"))

	// bgfork: a background agent forked off the anchor — SAME root uuid, header lines, and a
	// NEWER last timestamp (13:59). Without the skip it would win as the "live tip".
	bg := append(bgAgentHeader("Review notes and sketch first version"),
		metaJSON("mode"), metaJSON("file-history-snapshot"),
		msgLineTS("m-root", "", "2026-06-24T08:32:03Z"),
		msgLineTS("m-2", "m-root", "2026-06-24T12:40:21Z"),
		msgLineTS("m-bg", "m-2", "2026-06-24T13:59:27Z"))
	writeSession(t, home, cwd, "bgfork", bg...)

	if got, err := ResolveLeafSession(home, cwd, "anchor"); err != nil || got != "anchor" {
		t.Fatalf("resolve(anchor) = (%q,%v), want (anchor,nil) — the bg-agent fork must NOT be followed", got, err)
	}
}

// The bg-agent skip must NOT weaken real compaction tracing (the ability we depend on): a
// thread whose conversation compacted, and which ALSO has a background-agent fork hanging off
// the pre-compaction tip, still resolves forward THROUGH the compaction chain to its own leaf
// while ignoring the bg fork.
func TestResolveLeafSessionCompactionTracingSurvivesBGAgent(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/u/dev/proj"

	// anchor: normal session, last preserved message m-leaf (the compaction branch point).
	writeSession(t, home, cwd, "anchor",
		metaJSON("mode"),
		msgLineTS("m-root", "", "2026-06-24T08:00:00Z"),
		msgLineTS("m-leaf", "m-root", "2026-06-24T09:00:00Z"))

	// compChild: born by compaction off anchor (lpu = m-leaf). The true leaf, 09:10.
	writeSession(t, home, cwd, "compChild",
		metaJSON("mode"), boundaryLine("m-leaf", "b-1"),
		`{"type":"user","uuid":"m-leaf","isCompactSummary":true,"parentUuid":"b-1"}`,
		msgLineTS("m-c", "m-leaf", "2026-06-24T09:10:00Z"))

	// bgfork: a background agent forked off the anchor (same root), extended LATER (10:00) than
	// the real compaction leaf — a decoy that must be ignored.
	bg := append(bgAgentHeader("scratch"),
		metaJSON("mode"),
		msgLineTS("m-root", "", "2026-06-24T08:00:00Z"),
		msgLineTS("m-leaf", "m-root", "2026-06-24T09:00:00Z"),
		msgLineTS("m-bg", "m-leaf", "2026-06-24T10:00:00Z"))
	writeSession(t, home, cwd, "bgfork", bg...)

	for _, stored := range []string{"anchor", "compChild"} {
		got, err := ResolveLeafSession(home, cwd, stored)
		if err != nil {
			t.Fatalf("resolve(%s): %v", stored, err)
		}
		if got != "compChild" {
			t.Errorf("resolve(%s) = %q, want compChild (compaction leaf), NOT the bg-agent fork", stored, got)
		}
	}
}

// Combined: a resume fork that THEN compacts. The walk hops to the resume tip, then
// follows that tip's compaction chain to the final leaf.
func TestResolveLeafSessionResumeThenCompaction(t *testing.T) {
	home := t.TempDir()
	cwd := "/home/u/dev/proj"

	// anchor: frozen original (ends 08:40).
	writeSession(t, home, cwd, "anchor",
		metaJSON("mode"),
		msgLineTS("m-root", "", "2026-06-24T08:32:00Z"),
		msgLineTS("m-a", "m-root", "2026-06-24T08:40:00Z"))

	// fork: same root, continues to m-leaf (09:00) = the resume tip; then it compacts.
	writeSession(t, home, cwd, "fork",
		metaJSON("mode"),
		msgLineTS("m-root", "", "2026-06-24T08:32:00Z"),
		msgLineTS("m-a", "m-root", "2026-06-24T08:40:00Z"),
		msgLineTS("m-leaf", "m-a", "2026-06-24T09:00:00Z"))

	// compChild: born by compaction off fork (lpu = m-leaf, owned by fork).
	writeSession(t, home, cwd, "compChild",
		metaJSON("mode"), boundaryLine("m-leaf", "b-1"),
		`{"type":"user","uuid":"m-leaf","isCompactSummary":true,"parentUuid":"b-1"}`,
		msgLineTS("m-c", "m-leaf", "2026-06-24T09:10:00Z"))

	for _, stored := range []string{"anchor", "fork", "compChild"} {
		got, err := ResolveLeafSession(home, cwd, stored)
		if err != nil {
			t.Fatalf("resolve(%s): %v", stored, err)
		}
		if got != "compChild" {
			t.Errorf("resolve(%s) = %q, want compChild (resume tip then compaction leaf)", stored, got)
		}
	}
}
