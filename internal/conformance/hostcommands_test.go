package conformance

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
		matrix.RegisterTest("host-cmd.run", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testHostCmdRun(t, loc) })
	}
}

// testHostCmdRun: a provider manifest in the SERVING daemon's SESH_HOME is listed
// and run through the CLI (routed per --machine in the remote cell): declared
// args reach the program as literal argv, a nonzero exit is reported with the
// command's output and fails the CLI, and an undeclared arg is refused.
func testHostCmdRun(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	dir := filepath.Join(sb.Home, "host-commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `description = "conformance"
[[command]]
name = "say"
argv = ["sh", "-c", "printf '%s' \"$1\"; exit \"$2\"", "sh", "{text}", "{code}"]
[[command.arg]]
name = "text"
required = true
[[command.arg]]
name = "code"
required = true
`
	if err := os.WriteFile(filepath.Join(dir, "conf.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	sb.startDaemon(t)

	stdout, stderr, err := sb.Runner.Run(t, "host-cmd", "list", "--json")
	if err != nil {
		t.Fatalf("host-cmd list: %v\n%s", err, stderr)
	}
	var list api.HostCommandsResponse
	if err := json.Unmarshal([]byte(stdout), &list); err != nil || len(list.Providers) != 1 || list.Providers[0].Name != "conf" {
		t.Fatalf("host-cmd list = %s (%v)", stdout, err)
	}

	text := "hi $(not run); `x`"
	stdout, stderr, err = sb.Runner.Run(t, "host-cmd", "run", "--provider", "conf", "--command", "say", "--arg", "text="+text, "--arg", "code=0", "--json")
	if err != nil {
		t.Fatalf("host-cmd run: %v\n%s", err, stderr)
	}
	var run api.HostCommandRun
	if err := json.Unmarshal([]byte(stdout), &run); err != nil || run.Stdout != text || run.ExitCode != 0 || run.Machine != sb.Machine {
		t.Fatalf("run = %s (%v)", stdout, err)
	}

	_, stderr, err = sb.Runner.Run(t, "host-cmd", "run", "--provider", "conf", "--command", "say", "--arg", "text=x", "--arg", "code=4")
	if err == nil || !strings.Contains(stderr, "exited 4") {
		t.Fatalf("a nonzero exit must fail the CLI loudly; err=%v stderr=%s", err, stderr)
	}

	_, stderr, err = sb.Runner.Run(t, "host-cmd", "run", "--provider", "conf", "--command", "say", "--arg", "text=x", "--arg", "code=0", "--arg", "extra=1")
	if err == nil || !strings.Contains(stderr, "unknown arg") {
		t.Fatalf("an undeclared arg must be refused; err=%v stderr=%s", err, stderr)
	}
}
