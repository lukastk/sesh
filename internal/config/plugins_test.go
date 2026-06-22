package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writePluginFile(t *testing.T, home, name, body string) {
	t.Helper()
	dir := PluginsDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPluginsMissingDirIsEmpty(t *testing.T) {
	home := t.TempDir()
	specs, err := LoadPlugins(home)
	if err != nil {
		t.Fatalf("LoadPlugins on a home with no plugins dir: %v", err)
	}
	if len(specs) != 0 {
		t.Fatalf("want 0 plugins, got %d", len(specs))
	}
}

func TestLoadPluginsValid(t *testing.T) {
	home := t.TempDir()
	writePluginFile(t, home, "boxyard.toml", `
name = "boxyard"
description = "boxes"

[[list]]
name = "boxes"
command = ["boxyard", "list", "--output-format", "json"]
id = "{creation_timestamp_utc}_{box_subid}__{name}"
label = "{name}"
groups = "groups"
path = "~/dev/{creation_timestamp_utc}_{box_subid}__{name}"

[[action]]
name = "create-box"
command = ["boxyard", "new", "--box-name", "{name}"]
[[action.field]]
name = "name"
label = "Box name"
type = "text"
required = true
`)
	specs, err := LoadPlugins(home)
	if err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "boxyard" {
		t.Fatalf("unexpected specs: %+v", specs)
	}
	if len(specs[0].Lists) != 1 || len(specs[0].Actions) != 1 {
		t.Fatalf("want 1 list + 1 action, got %+v", specs[0])
	}
	if specs[0].Actions[0].Fields[0].Name != "name" {
		t.Fatalf("field not parsed: %+v", specs[0].Actions[0])
	}
}

func TestLoadPluginsLoudOnBadManifest(t *testing.T) {
	cases := []struct{ name, body string }{
		{"no-name", `
[[list]]
name = "x"
command = ["echo"]
id = "{a}"
label = "{a}"
`},
		{"list-missing-id", `
name = "p"
[[list]]
name = "x"
command = ["echo"]
label = "{a}"
`},
		{"action-undeclared-field", `
name = "p"
[[action]]
name = "x"
command = ["echo", "{missing}"]
`},
		{"action-bad-field-type", `
name = "p"
[[action]]
name = "x"
command = ["echo", "{a}"]
[[action.field]]
name = "a"
label = "A"
type = "color"
`},
		{"list-with-placeholder", `
name = "p"
[[list]]
name = "x"
command = ["echo", "{a}"]
id = "{a}"
label = "{a}"
`},
		{"dup-capability", `
name = "p"
[[list]]
name = "dup"
command = ["echo"]
id = "{a}"
label = "{a}"
[[action]]
name = "dup"
command = ["echo"]
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			writePluginFile(t, home, "p.toml", c.body)
			if _, err := LoadPlugins(home); err == nil {
				t.Fatalf("expected a loud error for %s, got nil", c.name)
			}
		})
	}
}

func TestLoadPluginsDuplicateNameAcrossFiles(t *testing.T) {
	home := t.TempDir()
	manifest := `
name = "same"
[[list]]
name = "x"
command = ["echo"]
id = "{a}"
label = "{a}"
`
	writePluginFile(t, home, "a.toml", manifest)
	writePluginFile(t, home, "b.toml", manifest)
	if _, err := LoadPlugins(home); err == nil {
		t.Fatal("expected a loud duplicate-plugin-name error, got nil")
	}
}
