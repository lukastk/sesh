package conformance

// Free (non-matrix) regression test: `peer remove` really drops the peer from the
// registry, and removing an unknown peer is a LOUD error, not a silent success.

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func TestPeerRemove(t *testing.T) {
	sb := newSandbox(t, matrix.Local)

	if _, stderr, err := sb.Runner.Run(t, "peer", "add", "--machine", "ghost", "--ssh", "nobody@ghost.invalid", "--home", "/tmp/ghost-home"); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	out, _, err := sb.Runner.Run(t, "peer", "list")
	if err != nil || !strings.Contains(out, "ghost") {
		t.Fatalf("peer list missing ghost after add: %v\n%s", err, out)
	}

	if _, stderr, err := sb.Runner.Run(t, "peer", "remove", "--machine", "ghost"); err != nil {
		t.Fatalf("peer remove: %v\n%s", err, stderr)
	}
	out, _, err = sb.Runner.Run(t, "peer", "list")
	if err != nil {
		t.Fatalf("peer list: %v", err)
	}
	if strings.Contains(out, "ghost") {
		t.Errorf("ghost still listed after remove:\n%s", out)
	}

	// Unknown peer: loud.
	if _, stderr, err := sb.Runner.Run(t, "peer", "remove", "--machine", "nosuch"); err == nil {
		t.Errorf("removing an unknown peer succeeded silently")
	} else if !strings.Contains(stderr, "nosuch") {
		t.Errorf("unknown-peer error does not name the peer: %s", stderr)
	}
}
