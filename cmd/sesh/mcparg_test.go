package main

import (
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
	// What a pre-63 daemon answers: the thread, without the field.
	err := checkMcpArgsApplied(want, api.Thread{ID: "t1"})
	if err == nil || !strings.Contains(err.Error(), "t1") {
		t.Errorf("dropped: %v", err)
	}
}
