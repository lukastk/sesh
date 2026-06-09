package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	// tmux.current is client-side (Local only). info/create-session/create-pane/
	// send-text run both Local and Remote (Remote = `--machine` routing into a
	// peer daemon over a real ssh hop, Phase 4). stage-file stays Local until
	// remote file transfer lands; tmux.nav is its own Phase 4 cell.
	matrix.RegisterTest("tmux.current", matrix.AgentAgnostic, matrix.Local, testTmuxCurrent)
	matrix.RegisterTest("tmux.stage-file", matrix.AgentAgnostic, matrix.Local, testTmuxStageFile)
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("tmux.info", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTmuxInfo(t, loc) })
		matrix.RegisterTest("tmux.create-session", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTmuxCreateSession(t, loc) })
		matrix.RegisterTest("tmux.create-pane", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTmuxCreatePane(t, loc) })
		matrix.RegisterTest("tmux.send-text", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testTmuxSendText(t, loc) })
	}
}

// testTmuxCurrent runs `sesh tmux current` INSIDE a real tmux pane and asserts it
// resolves the true locator + the owning thread id stamped on that pane. This is
// the client-side resolver (no daemon needed).
func testTmuxCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	bin := seshBin(t)

	// Build a session whose pane carries our env (so `current` reports our
	// machine) — created directly via tmux to keep the daemon out of it.
	if out, err := sb.rawTmux(t, "new-session", "-d", "-s", "cur",
		"-e", "SESH_HOME="+sb.Home,
		"-e", "SESH_MACHINE="+sb.Machine,
		"-e", "SESH_TMUX_SOCKET="+sb.TmuxSocket,
	); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	pane := sb.paneOf(t, "cur")

	// Stamp the owning thread id on the pane (the marker `current` must read).
	const threadID = "thr_current_test"
	if out, err := sb.rawTmux(t, "set-option", "-p", "-t", pane, "@sesh-thread-id", threadID); err != nil {
		t.Fatalf("set @sesh-thread-id: %v\n%s", err, out)
	}

	// Run `sesh tmux current --json` in the pane, redirecting to a file we read.
	outFile := filepath.Join(sb.Home, "current.json")
	cmd := bin + " tmux current --json > " + outFile + " 2>&1"
	if out, err := sb.rawTmux(t, "send-keys", "-t", pane, "-l", cmd); err != nil {
		t.Fatalf("send-keys cmd: %v\n%s", err, out)
	}
	if out, err := sb.rawTmux(t, "send-keys", "-t", pane, "Enter"); err != nil {
		t.Fatalf("send-keys enter: %v\n%s", err, out)
	}

	var cur api.TmuxCurrentResponse
	ok := waitUntil(8*time.Second, func() bool {
		data, err := os.ReadFile(outFile)
		if err != nil || len(data) == 0 {
			return false
		}
		return json.Unmarshal(data, &cur) == nil && cur.Session != ""
	})
	if !ok {
		data, _ := os.ReadFile(outFile)
		t.Fatalf("`tmux current` produced no parseable output; file:\n%s", data)
	}

	if cur.Session != "cur" {
		t.Errorf("session = %q, want %q", cur.Session, "cur")
	}
	if cur.Pane != pane {
		t.Errorf("pane = %q, want %q", cur.Pane, pane)
	}
	if cur.ThreadID != threadID {
		t.Errorf("thread_id = %q, want %q", cur.ThreadID, threadID)
	}
	if cur.Machine != sb.Machine {
		t.Errorf("machine = %q, want %q", cur.Machine, sb.Machine)
	}
	if cur.Socket != sb.TmuxSocket {
		t.Errorf("socket = %q, want %q", cur.Socket, sb.TmuxSocket)
	}
}

// testTmuxInfo creates sessions directly via tmux (the world), then asserts the
// daemon-served walker reports them faithfully.
func testTmuxInfo(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	for _, name := range []string{"alpha", "beta"} {
		if out, err := sb.rawTmux(t, "new-session", "-d", "-s", name); err != nil {
			t.Fatalf("new-session %s: %v\n%s", name, err, out)
		}
	}

	sessions := tmuxInfoSessions(t, sb)
	if got := sessionNames(sessions); !contains(got, "alpha") || !contains(got, "beta") {
		t.Fatalf("info missing sessions; got %v", got)
	}
	for _, s := range sessions {
		if s.Machine != sb.Machine {
			t.Errorf("session %q machine = %q, want %q", s.Name, s.Machine, sb.Machine)
		}
		if len(s.Windows) == 0 || len(s.Windows[0].Panes) == 0 {
			t.Errorf("session %q has no windows/panes", s.Name)
		}
	}

	// --session filter returns exactly that session.
	stdout, stderr, err := sb.Runner.Run(t, "tmux", "info", "--session", "alpha")
	if err != nil {
		t.Fatalf("info --session: %v\n%s", err, stderr)
	}
	filtered := parseJSONL(t, stdout)
	if len(filtered) != 1 || filtered[0].Name != "alpha" {
		t.Fatalf("--session alpha returned %v", sessionNames(filtered))
	}
}

// testTmuxCreateSession creates a session via sesh and verifies it exists in tmux
// with the injected environment — checked directly through tmux, not sesh.
func testTmuxCreateSession(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	if _, stderr, err := sb.Runner.Run(t, "tmux", "create-session", "--name", "made", "--env", "SESH_THREAD_ID=thr_xyz"); err != nil {
		t.Fatalf("create-session: %v\n%s", err, stderr)
	}

	if out, err := sb.rawTmux(t, "has-session", "-t", "=made"); err != nil {
		t.Fatalf("tmux does not have session 'made': %v\n%s", err, out)
	}
	env, err := sb.rawTmux(t, "show-environment", "-t", "=made", "SESH_THREAD_ID")
	if err != nil {
		t.Fatalf("show-environment: %v\n%s", err, env)
	}
	if !strings.Contains(env, "SESH_THREAD_ID=thr_xyz") {
		t.Errorf("injected env missing; got %q", strings.TrimSpace(env))
	}
}

// testTmuxCreatePane splits a session via sesh and verifies the pane count grew
// and the returned pane id really exists.
func testTmuxCreatePane(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	if out, err := sb.rawTmux(t, "new-session", "-d", "-s", "base"); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	if n := sb.paneCount(t, "base"); n != 1 {
		t.Fatalf("pre-split pane count = %d, want 1", n)
	}

	stdout, stderr, err := sb.Runner.Run(t, "tmux", "create-pane", "--target", "base")
	if err != nil {
		t.Fatalf("create-pane: %v\n%s", err, stderr)
	}
	newPane := strings.TrimSpace(stdout)

	if n := sb.paneCount(t, "base"); n != 2 {
		t.Errorf("post-split pane count = %d, want 2", n)
	}
	panes, _ := sb.rawTmux(t, "list-panes", "-t", "=base", "-F", "#{pane_id}")
	if !strings.Contains(panes, newPane) {
		t.Errorf("returned pane %q not present in %q", newPane, strings.TrimSpace(panes))
	}
}

// testTmuxSendText sends a command into a pane and asserts (via capture-pane)
// that the text actually executed there.
func testTmuxSendText(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)
	if out, err := sb.rawTmux(t, "new-session", "-d", "-s", "typing"); err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	pane := sb.paneOf(t, "typing")

	const marker = "tmuxmark_8f3a"
	if _, stderr, err := sb.Runner.Run(t, "tmux", "send-text", "--target", pane, "--text", "echo "+marker, "--enter"); err != nil {
		t.Fatalf("send-text: %v\n%s", err, stderr)
	}

	// The marker must appear on its OWN line (the echo output), proving --enter
	// executed the command — not merely that text was typed.
	ok := waitUntil(5*time.Second, func() bool {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
		for _, line := range strings.Split(cap, "\n") {
			if strings.TrimSpace(line) == marker {
				return true
			}
		}
		return false
	})
	if !ok {
		cap, _ := sb.rawTmux(t, "capture-pane", "-t", pane, "-p")
		t.Fatalf("marker output not found in pane; capture:\n%s", cap)
	}
}

// testTmuxStageFile stages a local file via sesh and asserts the bytes really
// landed at the returned path.
func testTmuxStageFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	src := filepath.Join(t.TempDir(), "image.png")
	content := []byte("\x89PNG fake bytes \x00\x01\x02 staged")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := sb.Runner.Run(t, "tmux", "stage-file", "--to", sb.Machine, src)
	if err != nil {
		t.Fatalf("stage-file: %v\n%s", err, stderr)
	}
	staged := strings.TrimSpace(stdout)
	if staged == "" {
		t.Fatal("stage-file printed no path")
	}
	got, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("read staged file %q: %v", staged, err)
	}
	if string(got) != string(content) {
		t.Errorf("staged content mismatch:\n got %q\nwant %q", got, content)
	}
}

// ---- helpers ----

func (sb *Sandbox) paneOf(t *testing.T, session string) string {
	t.Helper()
	out, err := sb.rawTmux(t, "list-panes", "-t", "="+session, "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("list-panes %s: %v\n%s", session, err, out)
	}
	lines := strings.Fields(strings.TrimSpace(out))
	if len(lines) == 0 {
		t.Fatalf("session %s has no panes", session)
	}
	return lines[0]
}

func (sb *Sandbox) paneCount(t *testing.T, session string) int {
	t.Helper()
	out, err := sb.rawTmux(t, "list-panes", "-t", "="+session, "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("list-panes %s: %v\n%s", session, err, out)
	}
	return len(strings.Fields(strings.TrimSpace(out)))
}

func tmuxInfoSessions(t *testing.T, sb *Sandbox) []api.TmuxSession {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "tmux", "info")
	if err != nil {
		t.Fatalf("tmux info: %v\n%s", err, stderr)
	}
	return parseJSONL(t, stdout)
}

func parseJSONL(t *testing.T, s string) []api.TmuxSession {
	t.Helper()
	var out []api.TmuxSession
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var sess api.TmuxSession
		if err := json.Unmarshal([]byte(line), &sess); err != nil {
			t.Fatalf("parse JSONL line %q: %v", line, err)
		}
		out = append(out, sess)
	}
	return out
}

func sessionNames(sessions []api.TmuxSession) []string {
	var names []string
	for _, s := range sessions {
		names = append(names, s.Name)
	}
	return names
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
