package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeHostManifest(t *testing.T, home, name, body string) {
	t.Helper()
	dir := HostCommandsDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadHostCommandsMissingDirIsEmpty(t *testing.T) {
	got, err := LoadHostCommands(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("missing dir: got %v, %v; want none", got, err)
	}
}

func TestLoadHostCommandsParses(t *testing.T) {
	home := t.TempDir()
	writeHostManifest(t, home, "boxyard.toml", `
description = "boxes"
[[command]]
name = "create-box"
argv = ["boxyard", "new", "--box-name", "{name}"]
timeout = "2m"
[[command.arg]]
name = "name"
label = "Box name"
required = true
[[command]]
name = "list"
argv = ["boxyard", "list"]
`)
	got, err := LoadHostCommands(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "boxyard" || len(got[0].Commands) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Commands[0].Timeout != 2*time.Minute || got[0].Commands[1].Timeout != HostCommandDefaultTimeout {
		t.Fatalf("timeouts %s %s", got[0].Commands[0].Timeout, got[0].Commands[1].Timeout)
	}
}

func TestLoadHostCommandsRefusesMalformed(t *testing.T) {
	cases := map[string]struct{ file, body, want string }{
		"unknown key":       {"p.toml", "[[command]]\nname = \"a\"\nargv = [\"x\"]\ncomand = 1\n", "unknown keys"},
		"undeclared arg":    {"p.toml", "[[command]]\nname = \"a\"\nargv = [\"x\", \"{y}\"]\n", "undeclared arg {y}"},
		"unused arg":        {"p.toml", "[[command]]\nname = \"a\"\nargv = [\"x\"]\n[[command.arg]]\nname = \"y\"\n", "never uses it"},
		"placeholder argv0": {"p.toml", "[[command]]\nname = \"a\"\nargv = [\"{y}\"]\n[[command.arg]]\nname = \"y\"\n", "must not contain a placeholder"},
		"empty argv":        {"p.toml", "[[command]]\nname = \"a\"\nargv = []\n", "argv is required"},
		"duplicate command": {"p.toml", "[[command]]\nname = \"a\"\nargv = [\"x\"]\n[[command]]\nname = \"a\"\nargv = [\"x\"]\n", "duplicate command"},
		"bad timeout":       {"p.toml", "[[command]]\nname = \"a\"\nargv = [\"x\"]\ntimeout = \"soon\"\n", "not a positive duration"},
		"timeout over cap":  {"p.toml", "[[command]]\nname = \"a\"\nargv = [\"x\"]\ntimeout = \"1h\"\n", "exceeds"},
		"bad provider name": {"Bad Name.toml", "[[command]]\nname = \"a\"\nargv = [\"x\"]\n", "not a valid provider name"},
		"bad command name":  {"p.toml", "[[command]]\nname = \"A b\"\nargv = [\"x\"]\n", "is not valid"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			writeHostManifest(t, home, c.file, c.body)
			_, err := LoadHostCommands(home)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error containing %q, got %v", c.want, err)
			}
		})
	}
}
