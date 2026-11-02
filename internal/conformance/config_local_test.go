package conformance

// config.local cells: <SESH_HOME>/config.local.toml overlays config.toml on the
// OWNING daemon — tables merge key by key, any other value (arrays included) is
// replaced. Proven through the daemon's own behaviour: start-time policy
// ([defaults] agent decides which harness `thread new` spawns without --agent),
// per-creation policy ([spawn.<agent>] model, merged across both files, as
// `model defaults` reports it), and `model set-default` refusing a key the
// overlay shadows while leaving config.toml untouched. Remote = routed.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("config.local", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testConfigLocal(t, loc) })
	}
}

func testConfigLocal(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	shared := "[defaults]\nagent = \"claude\"\n\n[spawn.claude]\nmodel = \"opus\"\n\n[spawn.pi]\nmodel = \"openai-codex/gpt-6-luna\"\n"
	local := "[defaults]\nagent = \"pi\"\n\n[spawn.claude]\nmodel = \"sonnet\"\n\n[spawn.codex]\nmodel = \"gpt-6-astra\"\n"
	if err := os.WriteFile(filepath.Join(sb.Home, "config.toml"), []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sb.Home, "config.local.toml"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)

	// Start-time policy: the overlay's [defaults] agent wins.
	out, stderr, err := sb.Runner.Run(t, "thread", "new", "--name", "which-harness", "--cwd", "/tmp", "--headless", "--json")
	if err != nil {
		t.Fatalf("thread new without --agent: %v\n%s", err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &th); err != nil {
		t.Fatal(err)
	}
	if th.AgentKind != "pi" {
		t.Errorf("thread new without --agent spawned %q, want the overlay's pi", th.AgentKind)
	}
	if th.Model != "openai-codex/gpt-6-luna" {
		t.Errorf("the pi thread recorded %q, want the SHARED pi default (the overlay does not set one)", th.Model)
	}

	// Per-creation policy, merged key by key.
	out, stderr, err = sb.Runner.Run(t, "model", "defaults", "--json")
	if err != nil {
		t.Fatalf("model defaults: %v\n%s", err, stderr)
	}
	var d api.ModelDefaultsResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &d); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"claude": "sonnet", "codex": "gpt-6-astra", "pi": "openai-codex/gpt-6-luna", "mypi": ""}
	for a, w := range want {
		if d.Defaults[a] != w {
			t.Errorf("default %s = %q, want %q", a, d.Defaults[a], w)
		}
	}
	if !strings.HasSuffix(d.LocalConfigPath, "config.local.toml") {
		t.Errorf("the reply does not name the overlay: %q", d.LocalConfigPath)
	}

	// A shared write the overlay would shadow is refused; config.toml is untouched.
	if _, stderr, err := sb.Runner.Run(t, "model", "set-default", "--agent", "claude", "--model", "haiku"); err == nil || !strings.Contains(stderr, "config.local.toml") {
		t.Errorf("a shadowed set-default: err=%v stderr=%s", err, stderr)
	}
	if got := readFile(t, filepath.Join(sb.Home, "config.toml")); got != shared {
		t.Errorf("a refused set-default changed config.toml:\n%s", got)
	}
	// An unshadowed one goes to the shared file.
	if _, stderr, err := sb.Runner.Run(t, "model", "set-default", "--agent", "mypi", "--model", "openai-codex/gpt-6-luna"); err != nil {
		t.Fatalf("set-default mypi: %v\n%s", err, stderr)
	}
	if !strings.Contains(readFile(t, filepath.Join(sb.Home, "config.toml")), "[spawn.mypi]") {
		t.Errorf("the unshadowed default was not written to config.toml")
	}
	if got := readFile(t, filepath.Join(sb.Home, "config.local.toml")); got != local {
		t.Errorf("a write touched config.local.toml:\n%s", got)
	}
}
