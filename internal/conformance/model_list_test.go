package conformance

// model.list cells (schema 58): `model list --agent <a>` reports what the REAL
// harness on the owning machine says it can use — claude's SDK initialize
// handshake, `codex debug models`, `pi --list-models` — never a list sesh
// keeps. Evidence: the models the other model cells run on are present, and for
// codex/pi the count equals what the harness CLI prints when run directly. The
// daemon reuses an answer (same fetched_at) until --refresh asks again. Remote =
// routed with --machine.

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		for _, a := range matrix.AllAgents {
			a := a
			matrix.RegisterTest("model.list", a, loc,
				func(t *testing.T) { testModelList(t, string(a), loc) })
		}
	}
}

func listModels(t *testing.T, sb *Sandbox, agent string, extra ...string) api.ModelListResponse {
	t.Helper()
	args := append([]string{"model", "list", "--agent", agent, "--json"}, extra...)
	out, stderr, err := sb.Runner.Run(t, args...)
	if err != nil {
		t.Fatalf("model list: %v\n%s", err, stderr)
	}
	var resp api.ModelListResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	return resp
}

// harnessModelCount runs the harness's own listing directly (no sesh) and counts
// the models it offers. claude has no listing CLI to compare against.
func harnessModelCount(t *testing.T, sb *Sandbox, agent string) (int, bool) {
	t.Helper()
	switch agent {
	case "codex":
		cmd := exec.Command("codex", "debug", "models")
		cmd.Env = append(os.Environ(), "CODEX_HOME="+sb.codexHome(t))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("codex debug models: %v", err)
		}
		var cat struct {
			Models []struct {
				Visibility string `json:"visibility"`
			} `json:"models"`
		}
		if err := json.Unmarshal(out, &cat); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, m := range cat.Models {
			if m.Visibility == "list" {
				n++
			}
		}
		return n, true
	case "pi":
		out, err := exec.Command("pi", "--list-models").Output()
		if err != nil {
			t.Fatalf("pi --list-models: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return len(lines) - 1, true // minus the header
	}
	return 0, false
}

func testModelList(t *testing.T, agent string, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	first := listModels(t, sb, agent)
	if first.Agent != agent || first.Machine != sb.Machine {
		t.Errorf("answered for %s on %s, want %s on %s", first.Agent, first.Machine, agent, sb.Machine)
	}
	ids := map[string]bool{}
	for _, m := range first.Models {
		ids[m.ID] = true
	}
	for _, want := range testModels[agent] {
		if !ids[want] {
			t.Errorf("%s's list lacks %q, a model it runs (got %d models)", agent, want, len(first.Models))
		}
	}
	if n, ok := harnessModelCount(t, sb, agent); ok && n != len(first.Models) {
		t.Errorf("sesh lists %d %s models; the harness itself offers %d", len(first.Models), agent, n)
	}
	if agent == "claude" {
		if first.HarnessDefault == "" {
			t.Errorf("claude reported no harness default")
		}
		for _, m := range first.Models {
			if m.ID == "default" {
				t.Errorf("claude's \"default\" pseudo-entry was offered as a model")
			}
		}
	}

	// Reused until --refresh.
	if again := listModels(t, sb, agent); again.FetchedAtUnix != first.FetchedAtUnix {
		t.Errorf("a second list re-asked the harness (fetched %d then %d)", first.FetchedAtUnix, again.FetchedAtUnix)
	}
	time.Sleep(1100 * time.Millisecond)
	if fresh := listModels(t, sb, agent, "--refresh"); fresh.FetchedAtUnix <= first.FetchedAtUnix {
		t.Errorf("--refresh reused the old answer (fetched %d then %d)", first.FetchedAtUnix, fresh.FetchedAtUnix)
	}

	if _, stderr, err := sb.Runner.Run(t, "model", "list", "--agent", "gemini"); err == nil || !strings.Contains(stderr, "gemini") {
		t.Errorf("an unknown agent was not refused loudly: err=%v stderr=%s", err, stderr)
	}
}

// codexHome is the sandbox daemon's isolated SESH_CODEX_HOME.
func (sb *Sandbox) codexHome(t *testing.T) string {
	t.Helper()
	lr, ok := sb.daemonRunner.(*localRunner)
	if !ok || lr.env["SESH_CODEX_HOME"] == "" {
		t.Fatalf("sandbox daemon has no SESH_CODEX_HOME")
	}
	return lr.env["SESH_CODEX_HOME"]
}
