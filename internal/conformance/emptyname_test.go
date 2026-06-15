package conformance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

// thread new allows an EMPTY name (a nameless thread): the session name comes from
// a [[session_name]] rule / the cwd (sanitizeName falls back to "thread" for the
// default), not the name — which is display-only. Not a matrix cell: a focused
// regression for the relaxed name-required check.
func TestEmptyThreadName(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	stdout, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", "pi", "--name", "", "--cwd", "/tmp", "--json")
	if err != nil {
		t.Fatalf("thread new with an empty name was rejected: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &th); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, stdout)
	}
	if th.Name != "" {
		t.Errorf("nameless thread got name %q, want empty", th.Name)
	}
	if th.SessionName == "" {
		t.Errorf("nameless thread has no session name (naming must fall back)")
	}
	if !threadInList(t, sb, th.ID) {
		t.Errorf("nameless thread %s not listed", th.ID)
	}
}
