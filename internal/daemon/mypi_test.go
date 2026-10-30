package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lukastk/sesh/internal/agents"
	"github.com/lukastk/sesh/internal/agents/pi"
	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// installFakePiFamily puts BOTH executables on a private PATH, each logging the
// argv it was called with, so a per-kind selection test can tell which one ran —
// and can catch a mypi thread silently invoking pi.
func installFakePiFamily(t *testing.T) (argvLogs map[string]string) {
	t.Helper()
	dir := t.TempDir()
	logs := map[string]string{}
	for _, name := range []string{"pi", "mypi"} {
		log := filepath.Join(dir, name+".argv")
		logs[name] = log
		script := "#!/bin/sh\n: > " + log + "\nfor arg in \"$@\"; do printf '%s\\n' \"$arg\" >> " + log + "; done\n" +
			"if [ -n \"$FAKE_PI_RESPONSE\" ]; then printf '%s\\n' \"$FAKE_PI_RESPONSE\"; fi\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logs
}

// The native stored-history CLI must be the THREAD's own executable. A mypi
// thread invoking pi would read the right file with the wrong binary — exactly
// the silent cross-harness call this test exists to refuse.
func TestStoredHistorySelectsTheExecutableOfTheThreadsKind(t *testing.T) {
	logs := installFakePiFamily(t)
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")

	for _, kind := range []string{"pi", "mypi"} {
		thread, path := insertPiFamilyStoredHistoryThread(t, d, kind, piHome)
		t.Setenv("FAKE_PI_RESPONSE", nativeStoredHistorySuccess(t, thread.AgentSessionID))
		for _, log := range logs {
			_ = os.Remove(log)
		}
		rec := httptest.NewRecorder()
		d.handleBrowserStoredHistory(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/history?id="+thread.ID, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s stored history = %d %s", kind, rec.Code, rec.Body.String())
		}
		argv, err := os.ReadFile(logs[kind])
		if err != nil {
			t.Fatalf("%s did not invoke its own executable: %v", kind, err)
		}
		want := "--session-history-json\n" + path + "\n"
		if string(argv) != want {
			t.Fatalf("%s argv = %q, want %q", kind, argv, want)
		}
		other := "pi"
		if kind == "pi" {
			other = "mypi"
		}
		if _, err := os.Stat(logs[other]); err == nil {
			t.Fatalf("a %s thread also invoked %s", kind, other)
		}
	}
}

func insertPiFamilyStoredHistoryThread(t *testing.T, d *Daemon, kind, piHome string) (api.Thread, string) {
	t.Helper()
	id, session := uuid.NewString(), uuid.NewString()
	cwd := filepath.Join(t.TempDir(), kind)
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	thread := api.Thread{
		ID: id, Machine: d.cfg.Machine, SessionName: "headless-" + id, Cwd: cwd,
		AgentKind: kind, Name: kind + "-history", Tags: []string{},
		CreatedAtUnix: time.Now().Unix(), AgentSessionID: session,
	}
	if err := d.store.InsertThread(thread); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(pi.SessionsRoot(piHome), pi.CwdDirName(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-10-30T00-00-00-000Z_"+session+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return thread, path
}

// A mypi pane reports process title "pi" (the fork keeps pi's APP_NAME), so the
// runtime resolver must still see a LIVE agent for a thread recorded as mypi.
// Without family matching this thread is published as headless·idle — a live
// thread rendered dead, with resume offered for a conversation already running.
func TestLivenessAcceptsAPiProcessUnderAMyPiThread(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// argv0 "pi": precisely what the patched fork presents, because it sets
	// process.title from pi's own APP_NAME.
	bin := filepath.Join(t.TempDir(), "pi")
	if err := os.Symlink(sleepBin, bin); err != nil {
		t.Fatal(err)
	}
	sock := fmt.Sprintf("sesh-test-mypi-%d", time.Now().UnixNano())
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "live", "-x", "80", "-y", "24", bin+" infinity"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() }) //nolint:errcheck
	pane, err := raw("list-panes", "-t", "live", "-F", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}

	d := &Daemon{store: st, tmux: tmux.NewServer(sock)}
	// The pane's shell has to exec the process first: a real agent appears a
	// moment after the session does, so wait for the evidence rather than
	// asserting into the race.
	if !eventuallyD(10*time.Second, func() bool {
		agent, running := tmux.AgentUnderPane(panePID(t, raw))
		return running && agent.Kind == "pi"
	}) {
		t.Fatal("the fake pi process never appeared under the pane")
	}
	for _, kind := range []string{"mypi", "pi"} {
		id := "tid-" + kind
		if err := st.InsertThread(api.Thread{ID: id, Machine: "test", SessionName: "live", AgentKind: kind, Cwd: "/tmp"}); err != nil {
			t.Fatal(err)
		}
		if _, err := raw("set-option", "-p", "-t", pane, tmux.ThreadIDOption, id); err != nil {
			t.Fatal(err)
		}
		thread, err := st.GetThread(id)
		if err != nil {
			t.Fatal(err)
		}
		head, _, err := d.resolveState(thread)
		if err != nil {
			t.Fatalf("%s resolveState: %v", kind, err)
		}
		if head != api.Headful {
			t.Fatalf("a live pi process under a %s thread resolved as %s, want headful", kind, head)
		}
	}
	// The family is pi-only: a claude thread over the same pi pane is still dead.
	if err := st.InsertThread(api.Thread{ID: "tid-claude", Machine: "test", SessionName: "live", AgentKind: "claude", Cwd: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := raw("set-option", "-p", "-t", pane, tmux.ThreadIDOption, "tid-claude"); err != nil {
		t.Fatal(err)
	}
	claudeThread, err := st.GetThread("tid-claude")
	if err != nil {
		t.Fatal(err)
	}
	head, _, err := d.resolveState(claudeThread)
	if err != nil {
		t.Fatal(err)
	}
	if head != api.Headless {
		t.Fatalf("a pi process under a claude thread resolved as %s, want headless", head)
	}
}

// The pi-only surfaces accept mypi (one socket protocol, one native contract)
// and still refuse the harnesses that genuinely do not have them.
func TestPiFamilySurfacesAcceptMyPiAndRefuseOthers(t *testing.T) {
	piHome := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	d := newBrowserTestDaemon(t, "self", "")
	for _, tc := range []struct {
		kind       string
		wantAccept bool
	}{{"pi", true}, {"mypi", true}, {"claude", false}, {"codex", false}} {
		thread := insertDisplayThread(t, d, tc.kind, piHome, displayConversation)
		// The RPC bridge: accepted kinds get past the kind gate and fail later on
		// the ABSENT socket (409), refused kinds are rejected at the gate (400).
		rec := httptest.NewRecorder()
		d.handleThreadRPC(rec, httptest.NewRequest(http.MethodGet, "/v1/threads/rpc?id="+thread.ID+"&protocol=live-v1", nil))
		if tc.wantAccept {
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no live Pi") {
				t.Fatalf("%s rpc = %d %s", tc.kind, rec.Code, rec.Body.String())
			}
		} else if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "only pi/mypi threads") {
			t.Fatalf("%s rpc = %d %s", tc.kind, rec.Code, rec.Body.String())
		}
		// The stored-history route's agent gate, read through its stable code.
		rec = httptest.NewRecorder()
		d.handleBrowserStoredHistory(rec, httptest.NewRequest(http.MethodGet, "/v1/browser/threads/history?id="+thread.ID, nil))
		body := rec.Body.String()
		if tc.wantAccept == strings.Contains(body, "unsupported_agent") {
			t.Fatalf("%s stored history = %d %s", tc.kind, rec.Code, body)
		}
	}
}

// `[spawn.mypi]` is an independent table and the sandbox refusal covers it:
// mypi has no permission system either, so a configured sandbox is loud.
func TestSpawnModeRefusesSandboxForTheWholePiFamily(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	for _, kind := range []string{"pi", "mypi"} {
		if _, err := d.resolveSpawnMode(agents.Kind(kind), "sandbox"); err == nil {
			t.Fatalf("%s accepted a sandbox mode", kind)
		} else if !strings.Contains(err.Error(), kind) {
			t.Fatalf("%s sandbox refusal does not name the harness: %v", kind, err)
		}
		if mode, err := d.resolveSpawnMode(agents.Kind(kind), "yolo"); err != nil || mode != "yolo" {
			t.Fatalf("%s yolo = (%q, %v)", kind, mode, err)
		}
	}
}

// panePID reads the live pane's process id from the isolated server.
func panePID(t *testing.T, raw func(args ...string) (string, error)) int {
	t.Helper()
	out, err := raw("list-panes", "-t", "live", "-F", "#{pane_pid}")
	if err != nil {
		t.Fatalf("pane pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("pane pid %q: %v", out, err)
	}
	return pid
}

// Headless adopt ASSERTS the kind (there is no process to inspect), so it must
// accept mypi and record it verbatim — that record is what later makes resume
// and headless turns launch mypi rather than pi.
func TestHeadlessAdoptAcceptsMyPi(t *testing.T) {
	d := newBrowserTestDaemon(t, "self", "")
	session := uuid.NewString()
	cwd := t.TempDir()
	rec := httptest.NewRecorder()
	body := `{"name":"adopted-mypi","session_id":"` + session + `","agent_kind":"mypi","cwd":"` + cwd + `"}`
	d.handleThreadAdopt(rec, httptest.NewRequest(http.MethodPost, "/v1/threads/adopt", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("headless adopt mypi = %d %s", rec.Code, rec.Body.String())
	}
	var resp api.ThreadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Thread.AgentKind != "mypi" || resp.Thread.AgentSessionID != session || !resp.Thread.HeadlessStarted {
		t.Fatalf("adopted record = %+v", resp.Thread)
	}
	// An unknown harness is still refused, and the refusal lists mypi.
	rec = httptest.NewRecorder()
	bad := `{"name":"nope","session_id":"` + uuid.NewString() + `","agent_kind":"gemini","cwd":"` + cwd + `"}`
	d.handleThreadAdopt(rec, httptest.NewRequest(http.MethodPost, "/v1/threads/adopt", strings.NewReader(bad)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "mypi") {
		t.Fatalf("unknown adopt kind = %d %s", rec.Code, rec.Body.String())
	}
}
