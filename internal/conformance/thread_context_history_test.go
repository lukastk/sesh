package conformance

// thread.context-rewind and thread.context-compact cells: a context must survive
// the two things that rewrite an agent's history. Both are driven through the
// harness's own interface in a real pane, because neither has a sesh verb and a
// rewind fires no event a test could stand in for.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range []matrix.Agent{matrix.Claude, matrix.Codex} {
			a := a
			matrix.RegisterTest("thread.context-rewind", a, loc,
				func(t *testing.T) { testContextRewind(t, string(a), loc) })
		}
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("thread.context-compact", a, loc,
				func(t *testing.T) { testContextCompact(t, string(a), loc) })
		}
	}
}

func paneKeys(t *testing.T, sb *Sandbox, pane string, keys ...string) {
	t.Helper()
	if out, err := sb.rawTmux(t, append([]string{"send-keys", "-t", pane}, keys...)...); err != nil {
		t.Fatalf("send-keys %v: %v %s", keys, err, out)
	}
}

func waitPaneText(t *testing.T, sb *Sandbox, pane, want string) {
	t.Helper()
	var capture string
	if !waitUntil(150*time.Second, func() bool {
		capture, _ = sb.rawTmux(t, "capture-pane", "-p", "-t", pane)
		return strings.Contains(capture, want)
	}) {
		t.Fatalf("the pane never showed %q:\n%s", want, capture)
	}
}

// rewindOnePrompt rewinds the conversation to before its last prompt with the
// harness's own TUI, leaving an empty composer.
func rewindOnePrompt(t *testing.T, sb *Sandbox, agent, pane string) {
	t.Helper()
	switch agent {
	case "claude":
		paneKeys(t, sb, pane, "-l", "/rewind")
		time.Sleep(time.Second)
		paneKeys(t, sb, pane, "Enter")
		waitPaneText(t, sb, pane, "Restore the code")
		time.Sleep(time.Second)
		paneKeys(t, sb, pane, "Up")
		time.Sleep(time.Second)
		paneKeys(t, sb, pane, "Enter")
		waitPaneText(t, sb, pane, "Restore conversation")
		time.Sleep(time.Second)
		paneKeys(t, sb, pane, "Enter")
	case "codex":
		paneKeys(t, sb, pane, "Escape")
		time.Sleep(time.Second)
		paneKeys(t, sb, pane, "Escape")
		waitPaneText(t, sb, pane, "rewind")
		time.Sleep(time.Second)
		paneKeys(t, sb, pane, "Enter")
		waitPaneText(t, sb, pane, "Conversation reverted")
	default:
		t.Fatalf("no rewind for %s", agent)
	}
	time.Sleep(2 * time.Second)
	paneKeys(t, sb, pane, "C-u") // a rewind puts the removed prompt back in the composer
	time.Sleep(time.Second)
}

// testContextRewind is the sequence that once lost a context: the daemon recorded
// a delivery that the rewind then removed from the conversation.
func testContextRewind(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	th := newThreadOnModel(t, sb, agent, "ctx-rewind", contextModels[agent], false)
	sb.waitThreadReady(t, th.ID, agent)
	headfulTurn(t, sb, th.ID, "Reply with only: FIRST")
	headfulTurn(t, sb, th.ID, "Reply with only: SECOND")

	tag := randomTag("CONDOR")
	setContext(t, sb, th.ID, tagContext(tag))
	for _, verb := range []string{"stop", "resume"} {
		if _, stderr, err := sb.Runner.Run(t, "thread", verb, "--id", th.ID); err != nil {
			t.Fatalf("thread %s: %v\n%s", verb, err, stderr)
		}
	}
	sb.waitThreadReady(t, th.ID, agent)
	pane, _, ok := sb.markedPane(t, th.ID)
	if !ok {
		t.Fatal("no pane after the restart")
	}
	// A completed turn first: codex fires a restart's SessionStart at the next
	// prompt, so without this the context would only arrive after the rewind and
	// there would be nothing for the rewind to remove.
	assertReplyHas(t, "before the rewind", headfulTurn(t, sb, th.ID, "What is the project tag? Reply with only the tag."), tag)
	rewindOnePrompt(t, sb, agent, pane)

	if _, stderr, err := sb.Runner.Run(t, "thread", "send", "--id", th.ID, "--text",
		"What is the project tag in your instructions? Reply with only the tag, or NONE if there is none. Do not use tools."); err != nil {
		t.Fatalf("thread send: %v\n%s", err, stderr)
	}
	waitPaneText(t, sb, pane, tag)
}

func compactNow(t *testing.T, sb *Sandbox, agent, id string) {
	t.Helper()
	if agent == "pi" {
		if out, stderr, err := sb.Runner.Run(t, "thread", "command", "--id", id, "--text", "/compact", "--timeout", "3m", "--json"); err != nil {
			t.Fatalf("pi compact: %v\n%s\n%s", err, out, stderr)
		}
	} else {
		pane, _, ok := sb.markedPane(t, id)
		if !ok {
			t.Fatal("no pane")
		}
		paneKeys(t, sb, pane, "-l", "/compact")
		time.Sleep(time.Second)
		paneKeys(t, sb, pane, "Enter")
	}
	marker := map[string]string{"claude": `"compact_boundary"`, "codex": `"type":"compacted"`, "pi": `"type":"compaction"`}[agent]
	if !waitUntil(150*time.Second, func() bool {
		return strings.Contains(strings.Join(transcriptOf(t, sb, id).Lines, "\n"), marker) && sb.threadStatus(t, id).Busy == api.BusyIdle
	}) {
		t.Fatalf("no %s in the transcript after /compact", marker)
	}
}

// afterCompaction returns the transcript records after the last compaction.
func afterCompaction(lines []string, agent string) []string {
	marker := map[string]string{"claude": `"compact_boundary"`, "codex": `"type":"compacted"`, "pi": `"type":"compaction"`}[agent]
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], marker) {
			return lines[i+1:]
		}
	}
	return nil
}

func testContextCompact(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	var opts []sandboxOpt
	if agent == "pi" {
		// pi refuses to compact a session under keepRecentTokens; this real-pi
		// fixture lowers it. The session still needs the longer turn below.
		opts = append(opts, withSandboxEnv("PI_CODING_AGENT_DIR", piRPCSettings(t)))
	}
	sb := newSandbox(t, loc, opts...)
	sb.startDaemon(t)
	th := newThreadOnModel(t, sb, agent, "ctx-compact", contextModels[agent], false)
	sb.waitThreadReady(t, th.ID, agent)

	tagA := randomTag("PETREL")
	setContext(t, sb, th.ID, tagContext(tagA))
	assertReplyHas(t, "before compaction", headfulTurn(t, sb, th.ID, "What is the project tag? Reply with only the tag."), tagA)
	if agent == "pi" {
		headfulTurn(t, sb, th.ID, "Write about 400 words on how tide pools form. Do not use tools.")
	}

	// Changed while idle, so no compaction summary can already hold it.
	tagB := randomTag("GANNET")
	b := setContext(t, sb, th.ID, tagContext(tagB))
	compactNow(t, sb, agent, th.ID)
	assertReplyHas(t, "after compaction",
		headfulTurn(t, sb, th.ID, "The project tag may have changed. What is the project tag now? Reply with only the tag."), tagB)

	lines := afterCompaction(transcriptOf(t, sb, th.ID).Lines, agent)
	switch agent {
	case "claude":
		if contextFileLoads(lines, b.Hash) == 0 {
			t.Fatalf("claude did not reload the context file after compaction (%d records)", len(lines))
		}
	case "codex":
		if injections(t, agent, lines, b.Hash) == 0 {
			t.Fatalf("codex got no context after compaction (%d records)", len(lines))
		}
	case "pi":
		if sec, seen := piSection(lines); !seen || !strings.Contains(sec, "version "+b.Hash) {
			t.Fatalf("pi's section after compaction: %q (seen=%v)", sec, seen)
		}
	}

	// The context file is a copy on the owner: it follows the store and goes with
	// the thread. Local only: this reads the owner's disk directly.
	if agent != "claude" || loc != matrix.Local {
		return
	}
	file := filepath.Join(sb.Home, agents.ContextDirName, th.ID, "CLAUDE.md")
	if got, err := os.ReadFile(file); err != nil || !strings.Contains(string(got), tagB) {
		t.Fatalf("context file: %q %v", got, err)
	}
	clearContext(t, sb, th.ID)
	if got, err := os.ReadFile(file); err != nil || len(got) != 0 {
		t.Fatalf("context file after a clear: %q %v", got, err)
	}
	if _, stderr, err := sb.Runner.Run(t, "thread", "delete", "--id", th.ID, "--force"); err != nil {
		t.Fatalf("thread delete: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
		t.Fatalf("the context folder outlived its thread: %v", err)
	}
}
