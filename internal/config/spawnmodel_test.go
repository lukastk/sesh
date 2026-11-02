package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const commentedConfig = `# top-of-file comment, must survive
[spawn]
mode = "yolo" # trailing comment, must survive

# a comment between tables
[spawn.claude]
args = ["--verbose"]

[tui]
columns = ["name"]
`

func writeSpawnConfig(t *testing.T, home, text string) {
	t.Helper()
	if err := os.WriteFile(ConfigPath(home), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetSpawnModelInsertsReplacesAndRemoves(t *testing.T) {
	home := t.TempDir()
	writeSpawnConfig(t, home, commentedConfig)

	// Insert into an existing table: one new line under the header, nothing else moves.
	if _, err := SetSpawnModel(home, "claude", "opus"); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(commentedConfig, "[spawn.claude]\n", "[spawn.claude]\nmodel = \"opus\"\n", 1)
	if got := readText(t, ConfigPath(home)); got != want {
		t.Fatalf("insert:\n%s\nwant:\n%s", got, want)
	}

	// Replace in place.
	if _, err := SetSpawnModel(home, "claude", "claude-fable-5-1[1m]"); err != nil {
		t.Fatal(err)
	}
	want = strings.Replace(commentedConfig, "[spawn.claude]\n", "[spawn.claude]\nmodel = \"claude-fable-5-1[1m]\"\n", 1)
	if got := readText(t, ConfigPath(home)); got != want {
		t.Fatalf("replace:\n%s\nwant:\n%s", got, want)
	}

	// A missing table joins its [spawn] family: after the family's last content
	// line, ahead of the blank line and the next table.
	if _, err := SetSpawnModel(home, "pi", "anthropic/claude-haiku-4-5"); err != nil {
		t.Fatal(err)
	}
	piBlock := "\n[spawn.pi]\nmodel = \"anthropic/claude-haiku-4-5\"\n"
	want = strings.Replace(want, "args = [\"--verbose\"]\n", "args = [\"--verbose\"]\n"+piBlock, 1)
	got := readText(t, ConfigPath(home))
	if got != want {
		t.Fatalf("new table:\n%s\nwant:\n%s", got, want)
	}
	sp, err := LoadSpawn(home)
	if err != nil {
		t.Fatal(err)
	}
	if sp.ModelFor("claude") != "claude-fable-5-1[1m]" || sp.ModelFor("pi") != "anthropic/claude-haiku-4-5" || sp.ModelFor("codex") != "" {
		t.Fatalf("read back: claude=%q pi=%q codex=%q", sp.ModelFor("claude"), sp.ModelFor("pi"), sp.ModelFor("codex"))
	}
	if sp.ModeFor("claude") != "yolo" || len(sp.ArgsFor("claude")) != 1 {
		t.Fatalf("the edit disturbed mode/args: mode=%q args=%v", sp.ModeFor("claude"), sp.ArgsFor("claude"))
	}

	// Remove: the key goes; a table that still has keys stays; a table left
	// empty goes too, so set-then-unset restores the original bytes.
	if _, err := SetSpawnModel(home, "claude", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := SetSpawnModel(home, "pi", ""); err != nil {
		t.Fatal(err)
	}
	if got = readText(t, ConfigPath(home)); got != commentedConfig {
		t.Fatalf("remove did not restore the original text:\n%s", got)
	}
	// Removing an absent key is a no-op, not an error.
	if _, err := SetSpawnModel(home, "codex", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSetSpawnModelWritesThroughASymlink(t *testing.T) {
	home := t.TempDir()
	synced := filepath.Join(t.TempDir(), "shared-config.toml")
	if err := os.WriteFile(synced, []byte(commentedConfig), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(synced, ConfigPath(home)); err != nil {
		t.Fatal(err)
	}
	wrote, err := SetSpawnModel(home, "codex", "gpt-6-astra")
	if err != nil {
		t.Fatal(err)
	}
	if wrote != synced {
		t.Fatalf("wrote %s, want the link target %s", wrote, synced)
	}
	fi, err := os.Lstat(ConfigPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config.toml is no longer a symlink: %v", fi.Mode())
	}
	if !strings.Contains(readText(t, synced), "[spawn.codex]\nmodel = \"gpt-6-astra\"\n") {
		t.Fatalf("the target did not get the default:\n%s", readText(t, synced))
	}
	tfi, _ := os.Stat(synced)
	if tfi.Mode().Perm() != 0o640 {
		t.Fatalf("target permissions changed to %v", tfi.Mode().Perm())
	}
	// Nothing stray left beside the target.
	entries, _ := os.ReadDir(filepath.Dir(synced))
	if len(entries) != 1 {
		t.Fatalf("leftover files beside the target: %v", entries)
	}
}

func TestSetSpawnModelRefusals(t *testing.T) {
	home := t.TempDir()

	// A dangling symlink means the synced target is missing: never paper over it.
	if err := os.Symlink(filepath.Join(home, "gone.toml"), ConfigPath(home)); err != nil {
		t.Fatal(err)
	}
	if _, err := SetSpawnModel(home, "claude", "opus"); err == nil || !strings.Contains(err.Error(), "missing file") {
		t.Fatalf("dangling symlink: err=%v", err)
	}
	os.Remove(ConfigPath(home))

	// No file at all: created.
	if _, err := SetSpawnModel(home, "claude", "opus"); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, ConfigPath(home)); got != "\n[spawn.claude]\nmodel = \"opus\"\n" {
		t.Fatalf("fresh file: %q", got)
	}

	for _, form := range []string{
		"[spawn]\nclaude = { mode = \"yolo\" }\n",
		"[spawn]\nclaude.mode = \"yolo\"\n",
	} {
		writeSpawnConfig(t, home, form)
		if _, err := SetSpawnModel(home, "claude", "opus"); err == nil || !strings.Contains(err.Error(), "cannot edit in place") {
			t.Errorf("%q: err=%v", form, err)
		}
		if got := readText(t, ConfigPath(home)); got != form {
			t.Errorf("%q was modified by a refused write: %q", form, got)
		}
	}

	writeSpawnConfig(t, home, "")
	for _, bad := range []struct{ agent, model, want string }{
		{"gemini", "x", "unknown agent"},
		{"claude", " opus", "whitespace"},
		{"claude", "op\nus", "control character"},
	} {
		if _, err := SetSpawnModel(home, bad.agent, bad.model); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%+v: err=%v", bad, err)
		}
	}
}

func TestSpawnModelConfigValidation(t *testing.T) {
	home := t.TempDir()
	writeSpawnConfig(t, home, "[spawn]\nmodel = \"opus\"\n")
	if _, err := LoadSpawn(home); err == nil || !strings.Contains(err.Error(), "per-agent") {
		t.Fatalf("global [spawn] model: err=%v", err)
	}
	writeSpawnConfig(t, home, "[spawn.codex]\nmodel = \"\"\n")
	if _, err := LoadSpawn(home); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("empty model: err=%v", err)
	}
	writeSpawnConfig(t, home, "[spawn.mypi]\nmodel = \"anthropic/claude-haiku-4-5\"\n")
	sp, err := LoadSpawn(home)
	if err != nil {
		t.Fatal(err)
	}
	if sp.ModelFor("mypi") != "anthropic/claude-haiku-4-5" || sp.ModelFor("pi") != "" {
		t.Fatalf("mypi has its own table: mypi=%q pi=%q", sp.ModelFor("mypi"), sp.ModelFor("pi"))
	}
}
