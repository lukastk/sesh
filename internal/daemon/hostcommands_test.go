package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
)

// hostCmdFixture is a bare daemon whose SESH_HOME holds one provider, "t".
func hostCmdFixture(t *testing.T, manifest string) (*Daemon, *http.ServeMux) {
	t.Helper()
	home := t.TempDir()
	dir := config.HostCommandsDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: config.Config{Home: home, Machine: "test"}}
	mux := http.NewServeMux()
	d.routesHostCommands(mux)
	return d, mux
}

const hostCmdManifest = `
[[command]]
name = "echo"
argv = ["sh", "-c", "printf '%s' \"$1\"; printf 'err' >&2; exit \"$2\"", "sh", "{text}", "{code}"]
[[command.arg]]
name = "text"
required = true
[[command.arg]]
name = "code"
[[command]]
name = "uses-dashy"
argv = ["printf", "%s", "{dashy}"]
[[command.arg]]
name = "dashy"
allow_dash = true
[[command]]
name = "slow"
argv = ["sleep", "30"]
timeout = "300ms"
[[command]]
name = "flood"
argv = ["head", "-c", "2000000", "/dev/zero"]
[[command]]
name = "missing"
argv = ["/nonexistent/program"]
`

func hostStart(t *testing.T, mux *http.ServeMux, req api.HostCommandRunRequest) (int, api.HostCommandRun, string) {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/host-commands/run", bytes.NewReader(body)))
	var run api.HostCommandRun
	_ = json.Unmarshal(rec.Body.Bytes(), &run)
	return rec.Code, run, rec.Body.String()
}

func hostFinish(t *testing.T, mux *http.ServeMux, id string) api.HostCommandRun {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/host-commands/run?id="+id+"&wait_ms=2000", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("read run: %d %s", rec.Code, rec.Body.String())
		}
		var run api.HostCommandRun
		if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
			t.Fatal(err)
		}
		if run.State == api.HostCommandDone {
			return run
		}
	}
	t.Fatalf("run %s never finished", id)
	return api.HostCommandRun{}
}

func TestHostCommandRunsArgvWithoutAShell(t *testing.T) {
	_, mux := hostCmdFixture(t, hostCmdManifest)
	// Shell metacharacters reach the program as one literal argv element.
	text := "a b; $(touch /tmp/sesh-pwned) `x`"
	code, run, raw := hostStart(t, mux, api.HostCommandRunRequest{Provider: "t", Command: "echo", Args: map[string]string{"text": text, "code": "3"}, RequestID: uuid.NewString()})
	if code != http.StatusOK || run.RunID == "" {
		t.Fatalf("start: %d %s", code, raw)
	}
	done := hostFinish(t, mux, run.RunID)
	if done.Stdout != text || done.Stderr != "err" || done.ExitCode != 3 || done.Error != "" {
		t.Fatalf("finished run = %+v", done)
	}
}

func TestHostCommandRefusals(t *testing.T) {
	_, mux := hostCmdFixture(t, hostCmdManifest)
	cases := map[string]struct {
		req  api.HostCommandRunRequest
		code int
		want string
	}{
		"no request id":    {api.HostCommandRunRequest{Provider: "t", Command: "echo", Args: map[string]string{"text": "x"}}, 400, "request_id"},
		"unknown provider": {api.HostCommandRunRequest{Provider: "nope", Command: "echo"}, 404, "no provider"},
		"unknown command":  {api.HostCommandRunRequest{Provider: "t", Command: "nope"}, 404, "no command"},
		"unknown arg":      {api.HostCommandRunRequest{Provider: "t", Command: "echo", Args: map[string]string{"text": "x", "other": "y"}}, 400, "unknown arg"},
		"missing required": {api.HostCommandRunRequest{Provider: "t", Command: "echo"}, 400, "required arg"},
		"leading dash":     {api.HostCommandRunRequest{Provider: "t", Command: "echo", Args: map[string]string{"text": "--force"}}, 400, "starts with"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.req.RequestID == "" && name != "no request id" {
				c.req.RequestID = uuid.NewString()
			}
			code, _, raw := hostStart(t, mux, c.req)
			if code != c.code || !strings.Contains(raw, c.want) {
				t.Fatalf("want %d containing %q, got %d %s", c.code, c.want, code, raw)
			}
		})
	}
	// allow_dash admits it.
	code, run, raw := hostStart(t, mux, api.HostCommandRunRequest{Provider: "t", Command: "uses-dashy", Args: map[string]string{"dashy": "-x"}, RequestID: uuid.NewString()})
	if code != http.StatusOK {
		t.Fatalf("allow_dash: %d %s", code, raw)
	}
	if done := hostFinish(t, mux, run.RunID); done.Stdout != "-x" {
		t.Fatalf("allow_dash run = %+v", done)
	}
}

func TestHostCommandRequestIDIsIdempotent(t *testing.T) {
	_, mux := hostCmdFixture(t, hostCmdManifest)
	rid := uuid.NewString()
	req := api.HostCommandRunRequest{Provider: "t", Command: "echo", Args: map[string]string{"text": "once", "code": "0"}, RequestID: rid}
	_, first, _ := hostStart(t, mux, req)
	_, again, raw := hostStart(t, mux, req)
	if again.RunID != first.RunID || !again.Replayed {
		t.Fatalf("a retried start ran again: first %s, again %s", first.RunID, raw)
	}
	req.Args = map[string]string{"text": "different", "code": "0"}
	if code, _, raw := hostStart(t, mux, req); code != http.StatusConflict {
		t.Fatalf("same request_id, different request: want 409, got %d %s", code, raw)
	}
}

func TestHostCommandFailuresAreReported(t *testing.T) {
	_, mux := hostCmdFixture(t, hostCmdManifest)
	_, slow, _ := hostStart(t, mux, api.HostCommandRunRequest{Provider: "t", Command: "slow", RequestID: uuid.NewString()})
	if done := hostFinish(t, mux, slow.RunID); done.ExitCode != -1 || !strings.Contains(done.Error, "timeout") || done.DurationMs > 8000 {
		t.Fatalf("timed-out run = %+v", done)
	}
	_, missing, _ := hostStart(t, mux, api.HostCommandRunRequest{Provider: "t", Command: "missing", RequestID: uuid.NewString()})
	if done := hostFinish(t, mux, missing.RunID); done.ExitCode != -1 || !strings.Contains(done.Error, "could not run") {
		t.Fatalf("unstartable run = %+v", done)
	}
	_, flood, _ := hostStart(t, mux, api.HostCommandRunRequest{Provider: "t", Command: "flood", RequestID: uuid.NewString()})
	if done := hostFinish(t, mux, flood.RunID); len(done.Stdout) != hostRunOutputCap || !done.StdoutTruncated || done.ExitCode != 0 {
		t.Fatalf("flood: %d bytes, truncated %v, exit %d", len(done.Stdout), done.StdoutTruncated, done.ExitCode)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/host-commands/run?id=nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown run: want 404, got %d", rec.Code)
	}
}

func TestHostCommandsList(t *testing.T) {
	_, mux := hostCmdFixture(t, hostCmdManifest)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/host-commands", nil))
	var resp api.HostCommandsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if resp.Machine != "test" || len(resp.Providers) != 1 || resp.Providers[0].Name != "t" || len(resp.Providers[0].Commands) != 5 {
		t.Fatalf("list = %+v", resp)
	}
	if c := resp.Providers[0].Commands[2]; c.Name != "slow" || c.TimeoutMs != 300 {
		t.Fatalf("slow = %+v", c)
	}
}
