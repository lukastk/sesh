package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTrust(t *testing.T, path, key string) (json.RawMessage, bool) {
	t.Helper()
	var top map[string]json.RawMessage
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("%s is not valid JSON after EnsureTrust: %v\n%s", path, err, raw)
	}
	var projects map[string]map[string]json.RawMessage
	if err := json.Unmarshal(top["projects"], &projects); err != nil {
		t.Fatalf("projects is not an object: %v", err)
	}
	v, ok := projects[key]["hasTrustDialogAccepted"]
	return v, ok
}

func TestGlobalConfigPath(t *testing.T) {
	t.Setenv("HOME", "/h/user")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got, err := GlobalConfigPath(); err != nil || got != "/h/user/.claude.json" {
		t.Fatalf("default: got %q, %v — want /h/user/.claude.json (a sibling of ~/.claude, not inside it)", got, err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/cfg/dir")
	if got, err := GlobalConfigPath(); err != nil || got != "/cfg/dir/.claude.json" {
		t.Fatalf("CLAUDE_CONFIG_DIR: got %q, %v", got, err)
	}
}

func TestTrustKeys(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	realKeys, err := TrustKeys(real + "/")
	if err != nil {
		t.Fatal(err)
	}
	// The base itself may live behind a symlink (macOS /var -> /private/var), so
	// compare against the resolved form rather than assuming a single key.
	want, _ := filepath.EvalSymlinks(real)
	if realKeys[len(realKeys)-1] != want || realKeys[0] != filepath.Clean(real) {
		t.Fatalf("real dir keys = %v, want [%s ... %s]", realKeys, filepath.Clean(real), want)
	}
	linkKeys, err := TrustKeys(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(linkKeys) != 2 || linkKeys[0] != link || linkKeys[1] != want {
		t.Fatalf("symlinked cwd must seed BOTH the as-given path and its realpath, got %v", linkKeys)
	}
	if _, err := TrustKeys("relative/dir"); err == nil {
		t.Fatal("a relative cwd must be refused")
	}
	if _, err := TrustKeys(filepath.Join(base, "missing")); err == nil {
		t.Fatal("a missing cwd must be refused loudly (a spawn into it would fail later anyway)")
	}
}

func TestEnsureTrustCreatesAndSeeds(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfgdir", ".claude.json") // parent dir absent: created
	cwd := t.TempDir()
	if err := EnsureTrust(cfg, cwd); err != nil {
		t.Fatal(err)
	}
	if v, ok := readTrust(t, cfg, filepath.Clean(cwd)); !ok || string(v) != "true" {
		t.Fatalf("fresh config: projects[%q].hasTrustDialogAccepted = %s (present=%v), want true", cwd, v, ok)
	}
	st, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %o, want 0600 (claude's own mode for this file)", st.Mode().Perm())
	}
}

// The document is claude's live state: everything sesh does not own must
// survive byte-for-byte, and an entry that already exists keeps its other
// fields and their exact values (big ints, nested arrays, unknown keys).
func TestEnsureTrustPreservesEverythingElse(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	cwd := t.TempDir()
	seed := `{
  "numStartups": 1893,
  "oauthAccount": {"accountUuid": "abc", "emailAddress": "x@y.z"},
  "bigInt": 9007199254740993,
  "projects": {
    "/other/dir": {"hasTrustDialogAccepted": false, "allowedTools": ["Bash(ls *)"], "lastCost": 14.080910500000003},
    ` + strconv(cwd) + `: {"hasTrustDialogAccepted": false, "exampleFilesGeneratedAt": 1789562142628, "allowedTools": [], "custom": {"deep": [1, 2, {"k": null}]}}
  },
  "tipsHistory": {"a": 1}
}`
	if err := os.WriteFile(cfg, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTrust(cfg, cwd); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cfg)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("invalid JSON after write: %v", err)
	}
	// Untouched top-level members: verbatim (compacted, since we re-indent).
	for k, want := range map[string]string{
		"numStartups":  "1893",
		"oauthAccount": `{"accountUuid":"abc","emailAddress":"x@y.z"}`,
		"bigInt":       "9007199254740993",
		"tipsHistory":  `{"a":1}`,
	} {
		if compact(t, got[k]) != want {
			t.Fatalf("top-level %q changed: %s (want %s)", k, got[k], want)
		}
	}
	var projects map[string]json.RawMessage
	if err := json.Unmarshal(got["projects"], &projects); err != nil {
		t.Fatal(err)
	}
	if compact(t, projects["/other/dir"]) != `{"hasTrustDialogAccepted":false,"allowedTools":["Bash(ls *)"],"lastCost":14.080910500000003}` {
		t.Fatalf("another project's record changed: %s", projects["/other/dir"])
	}
	var mine map[string]json.RawMessage
	if err := json.Unmarshal(projects[filepath.Clean(cwd)], &mine); err != nil {
		t.Fatal(err)
	}
	if string(mine["hasTrustDialogAccepted"]) != "true" {
		t.Fatalf("hasTrustDialogAccepted = %s, want true", mine["hasTrustDialogAccepted"])
	}
	if string(mine["exampleFilesGeneratedAt"]) != "1789562142628" {
		t.Fatalf("a big integer in the edited record was mangled: %s", mine["exampleFilesGeneratedAt"])
	}
	if compact(t, mine["custom"]) != `{"deep":[1,2,{"k":null}]}` {
		t.Fatalf("an unknown nested field in the edited record changed: %s", mine["custom"])
	}
}

func TestEnsureTrustIsANoOpWhenAlreadyTrusted(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	cwd := t.TempDir()
	keys, err := TrustKeys(cwd)
	if err != nil {
		t.Fatal(err)
	}
	projects := map[string]any{}
	for _, k := range keys {
		projects[k] = map[string]any{"hasTrustDialogAccepted": true}
	}
	seedBytes, _ := json.Marshal(map[string]any{"projects": projects, "numStartups": 7})
	seed := "  " + string(seedBytes) + "\n\n" // odd formatting: must survive untouched
	if err := os.WriteFile(cfg, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(cfg)
	if err := EnsureTrust(cfg, cwd); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(cfg)
	if string(after) != seed {
		t.Fatalf("already-trusted must not rewrite the file; got:\n%s\nwant:\n%s", after, seed)
	}
	if st, _ := os.Stat(cfg); !st.ModTime().Equal(before.ModTime()) {
		t.Fatalf("already-trusted must not touch the file at all (mtime moved)")
	}
}

func TestEnsureTrustFlipsFalseAndFillsMissingProjects(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	cwd := t.TempDir()
	// "projects": null is a shape claude can leave behind; a missing key too.
	for _, seed := range []string{`{"a":1}`, `{"a":1,"projects":null}`, `{"a":1,"projects":{` + strconv(filepath.Clean(cwd)) + `:{"hasTrustDialogAccepted":false}}}`} {
		if err := os.WriteFile(cfg, []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := EnsureTrust(cfg, cwd); err != nil {
			t.Fatalf("seed %s: %v", seed, err)
		}
		if v, ok := readTrust(t, cfg, filepath.Clean(cwd)); !ok || string(v) != "true" {
			t.Fatalf("seed %s: trust = %s (present=%v), want true", seed, v, ok)
		}
	}
}

// A document that does not parse is claude's problem to surface, not sesh's to
// erase: refuse loudly and leave the bytes exactly as found.
func TestEnsureTrustRefusesCorruptConfig(t *testing.T) {
	cwd := t.TempDir()
	for name, seed := range map[string]string{
		"truncated":          `{"projects": {"/x": {"hasTrustDialogAccepted": tr`,
		"array top-level":    `[1,2,3]`,
		"projects not obj":   `{"projects": "nope"}`,
		"entry not obj":      `{"projects": {` + strconv(filepath.Clean(cwd)) + `: 42}}`,
		"projects is a list": `{"projects": []}`,
	} {
		cfg := filepath.Join(t.TempDir(), ".claude.json")
		if err := os.WriteFile(cfg, []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
		err := EnsureTrust(cfg, cwd)
		if err == nil {
			t.Fatalf("%s: EnsureTrust succeeded on a corrupt config", name)
		}
		if !strings.Contains(err.Error(), "refusing to overwrite") {
			t.Fatalf("%s: error should say it refused to overwrite, got: %v", name, err)
		}
		after, _ := os.ReadFile(cfg)
		if string(after) != seed {
			t.Fatalf("%s: corrupt config was modified:\n%s", name, after)
		}
		if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg), ".claude.json.sesh-*")); len(leftovers) != 0 {
			t.Fatalf("%s: temp file leaked: %v", name, leftovers)
		}
	}
}

func TestEnsureTrustSeedsSymlinkedCwdUnderBothNames(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(base, ".claude.json")
	if err := EnsureTrust(cfg, link); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(real)
	for _, key := range []string{link, want} {
		if v, ok := readTrust(t, cfg, key); !ok || string(v) != "true" {
			t.Fatalf("projects[%q] = %s (present=%v), want true — claude may key the workspace by either spelling", key, v, ok)
		}
	}
}

func strconv(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// compact strips insignificant whitespace WITHOUT decoding (json.Compact keeps
// number literals as text — a float64 round-trip here would mangle the very big
// ints the test exists to protect).
func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return buf.String()
}
