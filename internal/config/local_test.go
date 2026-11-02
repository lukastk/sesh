package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLocal(t *testing.T, home, text string) {
	t.Helper()
	if err := os.WriteFile(LocalConfigPath(home), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalOverlayMergesTablesAndReplacesEverythingElse(t *testing.T) {
	home := t.TempDir()
	writeSpawnConfig(t, home, `[spawn]
mode = "yolo"
[spawn.claude]
args = ["--verbose"]
model = "opus"
[spawn.pi]
model = "openai-codex/gpt-6-luna"

[[cwd_label]]
match = '^/shared$'
label = 'shared'

[[cwd_label]]
match = '^/also-shared$'
label = 'also'

[browser_api]
allowed_origins = ["app://obsidian.md", "http://localhost"]
`)
	writeLocal(t, home, `[spawn.claude]
model = "sonnet"
[spawn.codex]
model = "gpt-6-astra"

[[cwd_label]]
match = '^/local$'
label = 'local'

[browser_api]
allowed_origins = ["app://obsidian.md"]
`)
	sp, err := LoadSpawn(home)
	if err != nil {
		t.Fatal(err)
	}
	// Tables merge key by key: the local model wins, the shared args and mode stay.
	if sp.ModelFor("claude") != "sonnet" || len(sp.ArgsFor("claude")) != 1 || sp.ModeFor("claude") != "yolo" {
		t.Errorf("claude: model=%q args=%v mode=%q", sp.ModelFor("claude"), sp.ArgsFor("claude"), sp.ModeFor("claude"))
	}
	if sp.ModelFor("codex") != "gpt-6-astra" || sp.ModelFor("pi") != "openai-codex/gpt-6-luna" {
		t.Errorf("codex=%q (local-only) pi=%q (shared-only)", sp.ModelFor("codex"), sp.ModelFor("pi"))
	}
	// An array of tables is REPLACED, not appended.
	labels, err := LoadCwdLabels(home)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := labels.LabelFor("/local", "/home/nobody"); got != "local" {
		t.Errorf("the local cwd_label is not in effect: %q", got)
	}
	if got, _ := labels.LabelFor("/shared", "/home/nobody"); got == "shared" {
		t.Errorf("a shared cwd_label survived the local list (arrays must replace)")
	}
	// A plain array is replaced too.
	api, err := LoadBrowserAPI(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.AllowedOrigins) != 1 || !api.AllowedOrigins["app://obsidian.md"] {
		t.Errorf("allowed_origins = %v, want the local list alone", api.AllowedOrigins)
	}
}

func TestLocalOverlayEitherFileAlone(t *testing.T) {
	home := t.TempDir()
	if _, err := readConfig(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no files: err=%v, want ErrNotExist", err)
	}
	if sp, err := LoadSpawn(home); err != nil || sp.ModeFor("") != "" {
		t.Fatalf("no files: %v %q", err, sp.ModeFor(""))
	}
	writeLocal(t, home, "[spawn]\nmode = \"yolo\"\n")
	if sp, err := LoadSpawn(home); err != nil || sp.ModeFor("claude") != "yolo" {
		t.Fatalf("local only: %v %q", err, sp.ModeFor("claude"))
	}
	// With no overlay the shared bytes are returned unchanged.
	os.Remove(LocalConfigPath(home))
	text := "# kept verbatim\n[spawn]\nmode = \"default\"\n"
	writeSpawnConfig(t, home, text)
	raw, err := readConfig(home)
	if err != nil || string(raw) != text {
		t.Fatalf("shared only: %q %v", raw, err)
	}
}

func TestLocalOverlayErrorsNameTheirFile(t *testing.T) {
	home := t.TempDir()
	writeSpawnConfig(t, home, "[spawn]\nmode = \"yolo\"\n")
	writeLocal(t, home, "[spawn\n")
	if _, err := LoadSpawn(home); err == nil || !strings.Contains(err.Error(), "config.local.toml") {
		t.Fatalf("a syntax error in the local file: %v", err)
	}
	// A value the merged document rejects names both files.
	writeLocal(t, home, "[spawn]\nmode = \"chaos\"\n")
	if _, err := LoadSpawn(home); err == nil || !strings.Contains(err.Error(), "chaos") {
		t.Fatalf("an invalid local value: %v", err)
	}
	if d := ConfigDesc(home); !strings.Contains(d, "config.toml + ") || !strings.HasSuffix(d, "config.local.toml") {
		t.Errorf("ConfigDesc = %q", d)
	}
}

func TestLocalOverlayFollowsASymlinkedSharedFile(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared.toml")
	os.WriteFile(shared, []byte("[spawn.claude]\nmodel = \"opus\"\n"), 0o644)
	os.Symlink(shared, ConfigPath(home))
	writeLocal(t, home, "[spawn.codex]\nmodel = \"gpt-6-luna\"\n")
	sp, err := LoadSpawn(home)
	if err != nil || sp.ModelFor("claude") != "opus" || sp.ModelFor("codex") != "gpt-6-luna" {
		t.Fatalf("%v claude=%q codex=%q", err, sp.ModelFor("claude"), sp.ModelFor("codex"))
	}
}

func TestSetSpawnModelRefusesAShadowedKey(t *testing.T) {
	home := t.TempDir()
	writeSpawnConfig(t, home, "[spawn]\nmode = \"yolo\"\n")
	writeLocal(t, home, "[spawn.claude]\nmodel = \"sonnet\"\n")
	before := readText(t, ConfigPath(home))
	if _, err := SetSpawnModel(home, "claude", "opus"); err == nil || !strings.Contains(err.Error(), "config.local.toml") {
		t.Fatalf("a shadowed write: err=%v", err)
	}
	if got := readText(t, ConfigPath(home)); got != before {
		t.Errorf("a refused write changed config.toml: %q", got)
	}
	// An agent the local file does not set is written normally.
	if _, err := SetSpawnModel(home, "codex", "gpt-6-luna"); err != nil {
		t.Fatal(err)
	}
}
