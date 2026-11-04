package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

func TestCheckMcpArgsApplied(t *testing.T) {
	want := map[string][]string{"s": {"a", "b"}}
	if err := checkMcpArgsApplied(nil, api.Thread{}); err != nil {
		t.Errorf("no arguments asked for: %v", err)
	}
	if err := checkMcpArgsApplied(want, api.Thread{McpArgs: map[string][]string{"s": {"a", "b"}}}); err != nil {
		t.Errorf("applied: %v", err)
	}
	// A daemon that accepted the arguments and lost them: the thread, without the field.
	err := checkMcpArgsApplied(want, api.Thread{ID: "t1"})
	if err == nil || !strings.Contains(err.Error(), "t1") {
		t.Errorf("dropped: %v", err)
	}
}

func TestMcpArgFieldErrorNamesTheCure(t *testing.T) {
	refused := errors.New(`daemon: json: unknown field "mcp_args" (HTTP 400)`)
	if err := mcpArgFieldError(refused); !strings.Contains(err.Error(), "upgrade") || !errors.Is(err, refused) {
		t.Errorf("got %v", err)
	}
	other := errors.New("daemon: refused")
	if err := mcpArgFieldError(other); err != other {
		t.Errorf("an unrelated error changed: %v", err)
	}
}
