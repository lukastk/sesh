package main

import (
	"strings"
	"testing"
)

func TestHoldingCreateCommandTargetsMachineDaemon(t *testing.T) {
	got := holdingCreateCommand(
		"/Users/lukas/bin/sesh current",
		"/Users/lukas/My Sesh",
		"mac'book",
		"work socket",
	)

	wants := []string{
		"SESH_HOME='/Users/lukas/My Sesh'",
		"SESH_MACHINE='mac'\\''book'",
		"SESH_TMUX_SOCKET='work socket'",
		"'/Users/lukas/bin/sesh current' tmux create-session --name scratch --dir \"$HOME\"",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("holdingCreateCommand() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "new-session") {
		t.Fatalf("holdingCreateCommand() starts tmux directly: %q", got)
	}
}

func TestWorkAttachDelegatesEmptyServerCreation(t *testing.T) {
	const ensureHolding = "'/opt/sesh' tmux create-session --name scratch"
	got := workAttach("work socket", "/tmp/client marker", ensureHolding)

	wants := []string{
		"tmux -L 'work socket' list-sessions",
		ensureHolding + " >/dev/null",
		"> '/tmp/client marker'",
		"exec tmux -u -L 'work socket' attach",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("workAttach() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "new-session") {
		t.Fatalf("workAttach() starts tmux directly: %q", got)
	}
}
