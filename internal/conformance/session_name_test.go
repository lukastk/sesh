package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("thread.session-name", matrix.AgentAgnostic, matrix.Local, testSessionNameConfig)
}

// testSessionNameConfig: `[[session_name]]` rules in <SESH_HOME>/config.toml name a
// thread's REAL tmux session from its cwd (regex match in ~-relative... here absolute
// temp paths; named groups + {tid8} templating). Asserted via raw list-sessions, on
// both the headed-spawn path and the revival minting path (a never-paned thread gets
// its templated name on first revive). A non-matching cwd keeps the default
// sesh_<name>, and a daemon with a BROKEN config refuses to start (loud, never a
// silently wrong name).
func testSessionNameConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)

	// A box-like dir layout + rules over it, written BEFORE the daemon starts.
	boxes := t.TempDir()
	boxDir := filepath.Join(boxes, "20260610_abc123__mybox")
	subDir := filepath.Join(boxDir, "internal", "x")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `
[[session_name]]
match = '^` + regexpQuoteDir(boxes) + `/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)$'
name  = '{boxname} <{boxid}> ({tid8})'

[[session_name]]
match = '^` + regexpQuoteDir(boxes) + `/[0-9]{8}_(?P<boxid>[a-z0-9]+)__(?P<boxname>[^/]+)/(?P<rel>.+)$'
name  = '{boxname}/{rel} <{boxid}> ({tid8})'
`
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)

	// Headed spawn in the box root: the REAL session bears the templated name.
	th := sb.newThread(t, "pi", "boxed", boxDir)
	want := "mybox <abc123> (" + th.ID[:8] + ")"
	if th.SessionName != want {
		t.Errorf("box-root session name = %q, want %q", th.SessionName, want)
	}
	if !sessionExistsOn(sb.TmuxSocket, want) {
		t.Errorf("no real tmux session named %q; have %v", want, masterSessionNamesOn(sb.TmuxSocket))
	}

	// Subfolder rule.
	th2 := sb.newThread(t, "pi", "subbed", subDir)
	want2 := "mybox/internal/x <abc123> (" + th2.ID[:8] + ")"
	if th2.SessionName != want2 || !sessionExistsOn(sb.TmuxSocket, want2) {
		t.Errorf("subfolder session name = %q (exists=%v), want %q", th2.SessionName, sessionExistsOn(sb.TmuxSocket, want2), want2)
	}

	// Non-matching cwd: the default convention stands.
	th3 := sb.newThread(t, "pi", "plain", "/tmp")
	if th3.SessionName != "sesh_plain" {
		t.Errorf("non-matching cwd session name = %q, want sesh_plain", th3.SessionName)
	}

	// Revival minting: a never-paned (headless-born) thread in the box gets the
	// templated name on its first revive.
	hl := sb.newHeadlessThreadAt(t, "pi", "hlbox", boxDir)
	sb.headlessTurn(t, hl.ID, "Reply with exactly: ok")
	if _, stderr, err := sb.Runner.Run(t, "thread", "resume", "--id", hl.ID); err != nil {
		t.Fatalf("revive: %v\n%s", err, stderr)
	}
	wantHL := "mybox <abc123> (" + hl.ID[:8] + ")"
	if !waitUntil(15*time.Second, func() bool { return sessionExistsOn(sb.TmuxSocket, wantHL) }) {
		t.Errorf("revived thread's session %q never appeared; have %v", wantHL, masterSessionNamesOn(sb.TmuxSocket))
	}

	// A BROKEN config refuses the daemon loudly.
	bad := newSandbox(t, matrix.Local)
	if err := os.WriteFile(filepath.Join(bad.Home, "config.toml"), []byte("[[session_name]]\nmatch='('\nname='x'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := bad.daemonRunner.Run(t, "daemon", "start"); err == nil {
		t.Errorf("daemon started despite a broken session_name config")
	} else if !strings.Contains(stderr, "session_name") {
		t.Errorf("broken-config error does not mention session_name: %s", stderr)
	}
}

// regexpQuoteDir escapes a literal directory path for use inside a regex.
func regexpQuoteDir(p string) string {
	r := strings.NewReplacer(`.`, `\.`, `+`, `\+`, `(`, `\(`, `)`, `\)`, `[`, `\[`, `]`, `\]`)
	return r.Replace(p)
}

func sessionExistsOn(socket, name string) bool {
	for _, s := range masterSessionNamesOn(socket) {
		if s == name {
			return true
		}
	}
	return false
}
