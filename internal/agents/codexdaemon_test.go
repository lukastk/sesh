package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func readCfg(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEnsureCodexNoDaemon(t *testing.T) {
	cases := []struct {
		name, in, prev string
		mustContain    []string // survives verbatim (comments, other keys)
	}{
		{name: "no file", in: "", prev: "unset"},
		{name: "top-level keys and projects, no features",
			in:          "# my codex\nmodel = \"gpt-5\" # pinned\n\n[projects.\"/tmp\"]\ntrust_level = \"trusted\"\n",
			prev:        "unset",
			mustContain: []string{"# my codex", "model = \"gpt-5\" # pinned", "[projects.\"/tmp\"]", "trust_level = \"trusted\""}},
		{name: "features table without the key",
			in:          "[features]\n# keep me\nweb_search = true\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n",
			prev:        "unset",
			mustContain: []string{"# keep me", "web_search = true"}},
		{name: "explicit true is overridden",
			in:          "[features]\ndaemon_auto_start = true # I want it\n",
			prev:        "true",
			mustContain: []string{"# I want it"}},
		{name: "dotted top-level features",
			in:          "features.web_search = true\nmodel = \"m\"\n[projects.\"/y\"]\ntrust_level = \"trusted\"\n",
			prev:        "unset",
			mustContain: []string{"features.web_search = true"}},
		{name: "dotted explicit true",
			in:   "features.daemon_auto_start = true\n",
			prev: "true"},
		{name: "no trailing newline", in: "model = \"m\"", prev: "unset", mustContain: []string{"model = \"m\""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := writeCfg(t, c.in)
			prev, err := EnsureCodexNoDaemon(home)
			if err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if prev != c.prev {
				t.Errorf("prev = %q, want %q", prev, c.prev)
			}
			got, err := CodexDaemonSetting(home)
			if err != nil || got != "false" {
				t.Fatalf("after ensure: setting = %q, %v; want false", got, err)
			}
			out := readCfg(t, home)
			for _, s := range c.mustContain {
				if !strings.Contains(out, s) {
					t.Errorf("lost %q:\n%s", s, out)
				}
			}
			if c.in != "" {
				if st, _ := os.Stat(filepath.Join(home, "config.toml")); st.Mode().Perm() != 0o640 {
					t.Errorf("mode changed to %v", st.Mode().Perm())
				}
			}
			// Idempotent: a second call must not rewrite the file.
			before, _ := os.Stat(filepath.Join(home, "config.toml"))
			if p2, err := EnsureCodexNoDaemon(home); err != nil || p2 != "false" {
				t.Fatalf("second ensure = %q, %v", p2, err)
			}
			after, _ := os.Stat(filepath.Join(home, "config.toml"))
			if !after.ModTime().Equal(before.ModTime()) || readCfg(t, home) != out {
				t.Errorf("second ensure rewrote an already-false config")
			}
		})
	}
}

func TestEnsureCodexNoDaemonRefuses(t *testing.T) {
	for name, body := range map[string]string{
		"not toml":     "this is = = not toml [",
		"inline table": "features = { web_search = true }\n",
		"non-boolean":  "[features]\ndaemon_auto_start = \"yes\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := writeCfg(t, body)
			if _, err := EnsureCodexNoDaemon(home); err == nil {
				t.Fatalf("accepted a config it should refuse:\n%s", readCfg(t, home))
			}
			if readCfg(t, home) != body {
				t.Errorf("a refused config was modified")
			}
		})
	}
}
