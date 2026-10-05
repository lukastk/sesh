package main

import (
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/config"
)

func TestPiCommandSelectorValidationPrecedesAnyDispatch(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"lost recovery id with text", []string{"--text", "/compact", "--request-id", "", "--timeout", "1s"}, "exactly one"},
		{"lost recovery id alone", []string{"--request-id", "", "--timeout", "1s"}, "refusing to start a new command"},
		{"blank recovery id", []string{"--request-id", " ", "--timeout", "1s"}, "refusing to start a new command"},
		{"empty command plus poll", []string{"--text", "", "--request-id", "315f89fc-9cd9-407c-a273-e48ae51f862a", "--timeout", "1s"}, "exactly one"},
		{"empty command", []string{"--text", "", "--timeout", "1s"}, "must start with /"},
		{"missing timeout", []string{"--text", "/compact"}, "requires --timeout"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// No daemon is needed: these must refuse before resolving or writing a thread.
			err := threadCommand(config.Config{Home: t.TempDir()}, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}
