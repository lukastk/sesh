package agents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPiHomeMatchesAgentOverride(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	if got := ResolveHomes("").Pi; got != os.Getenv("PI_CODING_AGENT_DIR") {
		t.Fatal(got)
	}
	t.Setenv("PI_CODING_AGENT_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := ResolveHomes("").Pi; got != filepath.Join(home, ".pi", "agent") {
		t.Fatal(got)
	}
}
