package conformance

import (
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	// Lifecycle ops on the record (agent-agnostic; tested via cheap headless pi
	// threads, except delete which uses a live headed thread to prove it leaves
	// the runtime untouched). Both localities (Remote = --machine routing).
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("thread.rename", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testThreadRename(t, loc) })
		matrix.RegisterTest("thread.tag", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testThreadTag(t, loc) })
		matrix.RegisterTest("thread.archive", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testThreadArchive(t, loc) })
		matrix.RegisterTest("thread.delete", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testThreadDelete(t, loc) })
	}
}

func testThreadRename(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "before")

	if _, stderr, err := sb.Runner.Run(t, "thread", "rename", "--id", th.ID, "--name", "after"); err != nil {
		t.Fatalf("rename: %v\n%s", err, stderr)
	}
	if got := sb.threadFromList(t, th.ID).Name; got != "after" {
		t.Errorf("name = %q, want after", got)
	}
}

func testThreadTag(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "tagme")

	if _, stderr, err := sb.Runner.Run(t, "thread", "tag", "--id", th.ID, "--add", "urgent", "--add", "wip"); err != nil {
		t.Fatalf("tag add: %v\n%s", err, stderr)
	}
	tags := sb.threadFromList(t, th.ID).Tags
	if !contains(tags, "urgent") || !contains(tags, "wip") {
		t.Fatalf("after add, tags = %v", tags)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "tag", "--id", th.ID, "--remove", "wip"); err != nil {
		t.Fatalf("tag remove: %v\n%s", err, stderr)
	}
	tags = sb.threadFromList(t, th.ID).Tags
	if contains(tags, "wip") || !contains(tags, "urgent") {
		t.Errorf("after remove, tags = %v (want [urgent])", tags)
	}
}

func testThreadArchive(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := sb.newHeadlessThread(t, "pi", "park")

	// archive hides it from the active list but keeps the record.
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", th.ID); err != nil {
		t.Fatalf("archive: %v\n%s", err, stderr)
	}
	if hasThread(sb.listThreads(t), th.ID) {
		t.Errorf("archived thread still in the active list")
	}
	if !hasThread(sb.listThreadsArchived(t), th.ID) {
		t.Errorf("archived thread missing from the archived list (record was not kept)")
	}
	// unarchive restores it to the active list.
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", th.ID, "--unarchive"); err != nil {
		t.Fatalf("unarchive: %v\n%s", err, stderr)
	}
	if !hasThread(sb.listThreads(t), th.ID) {
		t.Errorf("unarchived thread not back in the active list")
	}

	// THE GATE (schema 57): archiving parks a FINISHED thread, so a live runtime is
	// refused BY NAME — a real pi in a real pane, not a record field. Archiving it
	// would have hidden a working agent from the list where it would be looked at.
	live := sb.newThread(t, "pi", "stillgoing", "/tmp")
	sb.waitThreadReady(t, live.ID, "pi")
	out, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", live.ID)
	if err == nil {
		t.Fatalf("archived a LIVE thread: %s", out)
	}
	if !strings.Contains(out+stderr, "still live") || !strings.Contains(out+stderr, "thread stop") {
		t.Errorf("the live refusal must name the runtime and the remedy: %s%s", out, stderr)
	}
	if sb.threadFromList(t, live.ID).Archived {
		t.Errorf("the refused thread was archived anyway")
	}

	// --force parks it as it is — the override a schedule's --on-turn-end policy and
	// `delegate`'s worker archive rely on, and the only way to the archived-but-live
	// state the default view keeps visible.
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", live.ID, "--force"); err != nil {
		t.Fatalf("archive --force on a live thread: %v\n%s", err, stderr)
	}
	if !sb.threadFromList(t, live.ID).Archived {
		t.Errorf("--force did not park the live thread")
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", live.ID, "--unarchive"); err != nil {
		t.Fatalf("unarchive the forced one: %v\n%s", err, stderr)
	}

	// The remedy really is the remedy: once its runtime is gone, the SAME plain
	// archive goes through.
	if _, stderr, err := sb.Runner.Run(t, "thread", "stop", "--id", live.ID); err != nil {
		t.Fatalf("stop: %v\n%s", err, stderr)
	}
	if !waitUntil(20*time.Second, func() bool { return sb.threadStatus(t, live.ID).Head == api.Headless }) {
		t.Fatalf("stopped thread never lost its runtime")
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", live.ID); err != nil {
		t.Fatalf("archive after stop: %v\n%s", err, stderr)
	}

	// A FLAG alone refuses as well: it is output nobody has read, and archiving
	// would hide it. Clearing it makes the same command succeed.
	flagged := sb.newHeadlessThread(t, "pi", "flagged-park")
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--on", "--id", flagged.ID); err != nil {
		t.Fatalf("flag --on: %v\n%s", err, stderr)
	}
	out, stderr, err = sb.Runner.Run(t, "thread", "archive", "--id", flagged.ID)
	if err == nil {
		t.Fatalf("archived a FLAGGED thread: %s", out)
	}
	if !strings.Contains(out+stderr, "flagged") || !strings.Contains(out+stderr, "thread flag --off") {
		t.Errorf("the flag refusal must name the flag and the remedy: %s%s", out, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "flag", "--off", "--id", flagged.ID); err != nil {
		t.Fatalf("flag --off: %v\n%s", err, stderr)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "archive", "--id", flagged.ID); err != nil {
		t.Fatalf("archive after clearing the flag: %v\n%s", err, stderr)
	}
}

// testThreadDelete proves delete drops the RECORD but leaves the runtime
// untouched — the distinction from stop — AND that the live-thread orphan guard
// refuses a plain delete (you must stop first, or --force).
func testThreadDelete(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	// Prefix resolution: delete by an 8-char id PREFIX resolves to the full uuid
	// like every other verb (the daemon's delete is an exact-match lookup, so a bare
	// prefix used to 404). An unknown prefix is loud — never a silent no-op.
	pre := sb.newHeadlessThread(t, "pi", "delpre")
	// Child promotion: deleting a parent promotes its children to the deleted
	// thread's OWN parent (grandparent; root here) — a parent id never dangles.
	kid := sb.newHeadlessThreadParented(t, "pi", "delkid", pre.ID)
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", pre.ID[:8]); err != nil {
		t.Fatalf("delete by id prefix failed: %v\n%s", err, stderr)
	}
	if hasThread(sb.listThreads(t), pre.ID) {
		t.Errorf("prefix delete did not drop the record")
	}
	if got := sb.threadFromList(t, kid.ID).Parent; got != "" {
		t.Errorf("child not promoted to root after parent delete: parent=%q", got)
	}
	if _, _, err := sb.Runner.Run(t, "thread", "delete", "--id", "zzzzzzzz"); err == nil {
		t.Errorf("delete of an unknown id prefix should be loud, not a silent no-op")
	}

	th := sb.newThread(t, "pi", "deleteme", "/tmp") // a live HEADED thread
	var pid int
	if !waitUntil(agentStartTimeout, func() bool {
		_, p, ok := sb.markedPane(t, th.ID)
		pid = p
		return ok && agentRunningUnder(p, "pi")
	}) {
		t.Fatalf("agent never came up")
	}

	// Orphan guard: a plain delete of a LIVE thread is refused (and leaves both
	// record and runtime intact).
	if _, _, err := sb.Runner.Run(t, "thread", "delete", "--id", th.ID); err == nil {
		t.Errorf("delete of a live thread should be refused without --force")
	}
	if !hasThread(sb.listThreads(t), th.ID) {
		t.Errorf("refused delete still dropped the record")
	}

	// --force drops the record of a live thread (deliberately orphaning the agent).
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", th.ID, "--force"); err != nil {
		t.Fatalf("delete --force: %v\n%s", err, stderr)
	}
	// Record is gone...
	if hasThread(sb.listThreads(t), th.ID) {
		t.Errorf("deleted thread still listed")
	}
	// ...but the runtime is untouched (session + agent still alive) — unlike kill.
	if _, err := sb.rawTmux(t, "has-session", "-t", "=sesh_deleteme"); err != nil {
		t.Errorf("delete killed the tmux session (should leave runtime untouched)")
	}
	// pidAlive (signal-0) is the reliable "process still alive" probe; the ps-based
	// agent re-check gets a short retry window (it can flake under full-suite load).
	if !pidAlive(pid) {
		t.Errorf("delete killed the pane process (should leave runtime untouched)")
	}
	if !waitUntil(5*time.Second, func() bool { return agentRunningUnder(pid, "pi") }) {
		t.Errorf("delete killed the agent process (should leave runtime untouched)")
	}
	// Clean up the orphaned session ourselves.
	sb.rawTmux(t, "kill-session", "-t", "=sesh_deleteme") //nolint:errcheck
}
