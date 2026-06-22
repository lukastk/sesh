package conformance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("plugins.run", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testPluginsRun(t, loc) })
	}
}

// testPluginsRun exercises the plugin command-provider substrate end-to-end against a
// REAL daemon over a real (possibly ssh-localhost) hop: a manifest at
// <SESH_HOME>/plugins/test.toml whose capabilities run REAL commands (printf/echo) on
// the daemon's host. It asserts the list mapping (templated id/label/path + the groups
// array), the action's ARGV substitution, that a field value is NOT shell-interpreted
// (the cardinal injection guard), and that bad requests fail LOUDLY (unknown
// capability, missing required field). No mocking — the daemon really execs the commands.
func testPluginsRun(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)

	// A manifest with one list capability (printf a deterministic JSON array) and one
	// action capability (echo a field value back). The list templates compose the id +
	// path and pull the groups array; the action proves ARGV substitution + no-shell.
	const boxesJSON = `[{"k":"a1","n":"Alpha","g":["grp/one","grp/two"]},{"k":"b2","n":"Beta","g":[]}]`
	manifest := "" +
		"name = \"test\"\n" +
		"description = \"conformance test plugin\"\n\n" +
		"[[list]]\n" +
		"name = \"boxes\"\n" +
		"command = [\"printf\", \"%s\", " + jsonQuote(boxesJSON) + "]\n" +
		"id = \"{k}\"\n" +
		"label = \"{n}\"\n" +
		"groups = \"g\"\n" +
		"path = \"~/dev/{k}\"\n\n" +
		"[[action]]\n" +
		"name = \"echo-val\"\n" +
		"command = [\"echo\", \"{val}\"]\n" +
		"[[action.field]]\n" +
		"name = \"val\"\n" +
		"label = \"Value\"\n" +
		"type = \"text\"\n" +
		"required = true\n"
	writeManifest(t, sb.Home, "test.toml", manifest)

	sb.startDaemon(t)

	// GET /v1/plugins → the manifest + its capabilities, served from the daemon's host.
	{
		stdout, stderr, err := sb.Runner.Run(t, "plugins", "list", "--json")
		if err != nil {
			t.Fatalf("plugins list: %v\n%s", err, stderr)
		}
		var resp api.PluginsListResponse
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("decode plugins list: %v\nraw: %s", err, stdout)
		}
		if len(resp.Plugins) != 1 || resp.Plugins[0].Name != "test" {
			t.Fatalf("plugins list = %+v, want one plugin 'test'", resp.Plugins)
		}
		if n := len(resp.Plugins[0].Capabilities); n != 2 {
			t.Fatalf("want 2 capabilities, got %d: %+v", n, resp.Plugins[0].Capabilities)
		}
	}

	// LIST capability: run the real printf command and map its JSON output.
	{
		stdout, stderr, err := sb.Runner.Run(t, "plugins", "run", "test", "boxes", "--json")
		if err != nil {
			t.Fatalf("plugins run test boxes: %v\n%s", err, stderr)
		}
		var resp api.PluginRunResponse
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("decode run: %v\nraw: %s", err, stdout)
		}
		if resp.Kind != "list" || len(resp.Items) != 2 {
			t.Fatalf("list run = %+v, want kind=list with 2 items", resp)
		}
		a := resp.Items[0]
		if a.ID != "a1" || a.Label != "Alpha" || a.Path != "~/dev/a1" {
			t.Errorf("item0 = %+v, want id=a1 label=Alpha path=~/dev/a1", a)
		}
		if strings.Join(a.Groups, ",") != "grp/one,grp/two" {
			t.Errorf("item0 groups = %v, want [grp/one grp/two]", a.Groups)
		}
		if len(resp.Items[1].Groups) != 0 {
			t.Errorf("item1 groups = %v, want empty", resp.Items[1].Groups)
		}
	}

	// ACTION capability + the INJECTION GUARD: a field value containing shell
	// metacharacters must come back VERBATIM (it was passed as ARGV, never a shell
	// string). If it were shell-interpreted, the `;` / `$(...)` would not survive intact.
	{
		const evil = "hello; echo PWNED $(whoami) > /tmp/x && rm -rf ~"
		stdout, stderr, err := sb.Runner.Run(t, "plugins", "run", "test", "echo-val", "--field", "val="+evil, "--json")
		if err != nil {
			t.Fatalf("plugins run test echo-val: %v\n%s", err, stderr)
		}
		var resp api.PluginRunResponse
		if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
			t.Fatalf("decode action run: %v\nraw: %s", err, stdout)
		}
		if resp.Kind != "action" {
			t.Fatalf("action run kind = %q, want action", resp.Kind)
		}
		if got := strings.TrimRight(resp.Output, "\n"); got != evil {
			t.Errorf("action output = %q, want the literal field value %q (shell injection!)", got, evil)
		}
	}

	// LOUD failures: unknown capability, and a missing required field.
	if _, _, err := sb.Runner.Run(t, "plugins", "run", "test", "nope", "--json"); err == nil {
		t.Error("plugins run test nope (unknown capability) was accepted")
	}
	if _, _, err := sb.Runner.Run(t, "plugins", "run", "test", "echo-val", "--json"); err == nil {
		t.Error("plugins run echo-val with no required field was accepted")
	}
}

// TestBoxyardPluginIfAvailable is a non-matrix regression test for the SHIPPED boxyard
// manifest: it skips LOUDLY when boxyard is not installed (so it never silently passes
// nor pollutes the matrix), and otherwise drives the real `boxyard list` through the
// daemon and asserts the example mapping produces non-empty box items with groups.
func TestBoxyardPluginIfAvailable(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("boxyard"); err != nil {
		t.Skip("NOT RUN: boxyard not on PATH (the shipped example needs it; covered by live smoke on machines that have it)")
	}
	sb := newSandbox(t, matrix.Local)
	body, err := os.ReadFile(repoExampleManifest(t))
	if err != nil {
		t.Fatalf("read example manifest: %v", err)
	}
	writeManifest(t, sb.Home, "boxyard.toml", string(body))
	sb.startDaemon(t)

	stdout, stderr, err := sb.Runner.Run(t, "plugins", "run", "boxyard", "boxes", "--json")
	if err != nil {
		t.Fatalf("plugins run boxyard boxes: %v\n%s", err, stderr)
	}
	var resp api.PluginRunResponse
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, stdout)
	}
	if len(resp.Items) == 0 {
		t.Fatal("boxyard boxes returned no items (expected the real yard)")
	}
	for _, it := range resp.Items {
		if it.ID == "" || it.Label == "" || !strings.HasPrefix(it.Path, "~/dev/") {
			t.Fatalf("malformed boxyard item: %+v", it)
		}
	}
}

func repoExampleManifest(t *testing.T) string {
	t.Helper()
	// internal/conformance → repo root → examples/plugins/boxyard.toml
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..", "examples", "plugins", "boxyard.toml")
}

// writeManifest drops a plugin manifest into <home>/plugins/<name>.
func writeManifest(t *testing.T, home, name, body string) {
	t.Helper()
	dir := config.PluginsDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// jsonQuote renders s as a TOML/JSON double-quoted string literal (they coincide for
// our content — no embedded newlines), for embedding in the manifest's command array.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
