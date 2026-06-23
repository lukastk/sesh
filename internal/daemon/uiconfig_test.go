package daemon

import (
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// TestUIConfigDefaultChatViewPersists exercises the daemon's persist cycle the
// way GET/POST /v1/ui-config does — fromAPIUIConfig → SaveUIConfig → LoadUIConfig
// → toAPIUIConfig — and asserts default_chat_view survives the round-trip (the
// recurring "saves but never loads back" pointer-decode bug) and that an absent
// value resolves to empty (the app falls back to "terminal").
func TestUIConfigDefaultChatViewPersists(t *testing.T) {
	dir := t.TempDir()

	// Absent → empty.
	got := toAPIUIConfig(config.DefaultUIConfig())
	if got.DefaultChatView != "" {
		t.Errorf("default DefaultChatView = %q, want empty", got.DefaultChatView)
	}

	// POST a value → it persists and GET returns it.
	in := api.UIConfig{CollapseParents: true, DefaultChatView: "transcript"}
	if err := config.SaveUIConfig(dir, fromAPIUIConfig(in)); err != nil {
		t.Fatalf("SaveUIConfig: %v", err)
	}
	loaded, err := config.LoadUIConfig(dir)
	if err != nil {
		t.Fatalf("LoadUIConfig: %v", err)
	}
	if out := toAPIUIConfig(loaded); out.DefaultChatView != "transcript" {
		t.Errorf("round-tripped DefaultChatView = %q, want transcript", out.DefaultChatView)
	}
}
