package daemon

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/store"
	"github.com/lukastk/sesh/internal/tmux"
)

// TestClassifySession pins the session classification rule, which is mechanism
// (every client — the TUI, sesh-ui, a script — must get the same answer).
func TestClassifySession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := st.InsertThread(api.Thread{ID: "sh-live", Machine: "test",
		SessionName: "boxsess", AgentKind: api.ShellAgentKind, Cwd: "/tmp"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	d := &Daemon{store: st}

	win := func(panes ...api.TmuxPane) api.TmuxWindow { return api.TmuxWindow{Panes: panes} }
	pane := func(id, threadID string) api.TmuxPane { return api.TmuxPane{Pane: id, ThreadID: threadID} }

	t.Run("no marker and no agent pane is a GHOST", func(t *testing.T) {
		got := d.classifySession(api.TmuxSession{Name: "scratch", Path: "/tmp",
			Windows: []api.TmuxWindow{win(pane("%0", ""), pane("%1", ""))}})
		if got.Class != api.ShellClassGhost {
			t.Fatalf("class = %q, want ghost", got.Class)
		}
		if got.Panes != 2 || got.Windows != 1 || got.Path != "/tmp" {
			t.Fatalf("row wrong: %+v", got)
		}
	})

	t.Run("an agent-marked pane makes it an AGENT session", func(t *testing.T) {
		got := d.classifySession(api.TmuxSession{Name: "work",
			Windows: []api.TmuxWindow{win(pane("%0", ""), pane("%1", "t-aaa"))}})
		if got.Class != api.ShellClassAgent {
			t.Fatalf("class = %q, want agent", got.Class)
		}
		if len(got.AgentThreads) != 1 || got.AgentThreads[0] != "t-aaa" {
			t.Fatalf("agent threads = %v", got.AgentThreads)
		}
	})

	t.Run("a marker resolving to a record is a SHELL", func(t *testing.T) {
		got := d.classifySession(api.TmuxSession{Name: "boxsess", ShellID: "sh-live",
			Windows: []api.TmuxWindow{win(pane("%0", ""))}})
		if got.Class != api.ShellClassShell || got.ThreadID != "sh-live" {
			t.Fatalf("class = %q id = %q, want shell/sh-live", got.Class, got.ThreadID)
		}
	})

	t.Run("the shell marker DOMINATES hosted agent panes", func(t *testing.T) {
		// This is the coexistence rule: a shell thread's session may legitimately
		// host agent threads (that is what starting an agent inside a box's shell
		// session does), and it is STILL that shell thread's session.
		got := d.classifySession(api.TmuxSession{Name: "boxsess", ShellID: "sh-live",
			Windows: []api.TmuxWindow{win(pane("%0", "t-aaa"))}})
		if got.Class != api.ShellClassShell {
			t.Fatalf("class = %q, want shell — a shell session hosting agents is still a shell session", got.Class)
		}
		if len(got.AgentThreads) != 1 {
			t.Fatalf("the hosted agent thread must still be reported: %v", got.AgentThreads)
		}
	})

	t.Run("a marker with no record is STALE, not shell", func(t *testing.T) {
		got := d.classifySession(api.TmuxSession{Name: "orphan", ShellID: "no-such-record",
			Windows: []api.TmuxWindow{win(pane("%0", ""))}})
		if got.Class != api.ShellClassStale {
			t.Fatalf("class = %q, want stale — a marker whose record is gone is a bug state, not a ghost", got.Class)
		}
		if got.ThreadID != "no-such-record" {
			t.Fatalf("stale row must carry the dangling id, got %q", got.ThreadID)
		}
	})
}

func TestShellNameFor(t *testing.T) {
	for in, want := range map[string]string{
		"/home/l/dev/20260214_0opf84__appgarden": "20260214_0opf84__appgarden",
		"/home/l/src/":                           "src",
		"/":                                      "shell",
		"":                                       "shell",
	} {
		if got := shellNameFor(in); got != want {
			t.Errorf("shellNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShellSendTarget(t *testing.T) {
	w0 := api.TmuxWindow{Index: 0, Panes: []api.TmuxPane{
		{Pane: "%0", Active: false}, {Pane: "%1", Active: true}}}
	w1 := api.TmuxWindow{Index: 1, Panes: []api.TmuxPane{{Pane: "%2", Active: true}}}
	sess := api.TmuxSession{Name: "boxsess", Windows: []api.TmuxWindow{w0, w1}}
	idx := func(i int) *int { return &i }

	if got, err := shellSendTarget(sess, "", nil); err != nil || got != "%1" {
		t.Fatalf("default = (%q, %v), want the session's active pane %%1", got, err)
	}
	if got, err := shellSendTarget(sess, "%2", nil); err != nil || got != "%2" {
		t.Fatalf("explicit pane = (%q, %v), want %%2", got, err)
	}
	if got, err := shellSendTarget(sess, "", idx(1)); err != nil || got != "%2" {
		t.Fatalf("window 1 = (%q, %v), want its active pane %%2", got, err)
	}
	// An address that is not in THIS session is LOUD — never a fallback to some
	// other pane, which would put the text where the caller did not ask.
	if _, err := shellSendTarget(sess, "%99", nil); err == nil {
		t.Fatal("a pane not in the session must be a loud error")
	}
	if _, err := shellSendTarget(sess, "", idx(9)); err == nil {
		t.Fatal("a window not in the session must be a loud error")
	}
	if _, err := shellSendTarget(sess, "%0", idx(0)); err == nil {
		t.Fatal("--pane and --window together must be refused")
	}
}

// TestHostedAgentThreads: what a shell thread's kill would take down with it.
func TestHostedAgentThreads(t *testing.T) {
	sess := api.TmuxSession{Windows: []api.TmuxWindow{
		{Panes: []api.TmuxPane{{ThreadID: "a"}, {ThreadID: ""}, {ThreadID: "a"}}},
		{Panes: []api.TmuxPane{{ThreadID: "b"}}},
	}}
	got := hostedAgentThreads(sess)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("hosted = %v, want [a b] deduped", got)
	}
	if len(hostedAgentThreads(api.TmuxSession{})) != 0 {
		t.Fatal("an empty session hosts nothing")
	}
}

// TestShellRuntimeIsTheSession drives a REAL tmux server: a shell thread's head
// comes from its SESSION marker, never a pane, and the maintainer must resolve
// it that way (headful while the session lives, headless once it is gone).
func TestShellRuntimeIsTheSession(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	sock := "seshshell-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "boxsess", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	const tid = "tid-shell-runtime"
	if err := st.InsertThread(api.Thread{ID: tid, Machine: "test", SessionName: "boxsess",
		AgentKind: api.ShellAgentKind, Cwd: "/tmp"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	srv := tmux.NewServer(sock)
	if err := srv.StampSessionShellID("boxsess", tid); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	d := &Daemon{store: st, tmux: srv}
	m := newMaintainer(d)

	m.tick()
	snap, ok := m.stateOf(tid)
	if !ok {
		t.Fatal("maintainer published no snapshot for the shell thread")
	}
	if snap.Head != api.Headful {
		t.Fatalf("head = %q, want headful — the marked SESSION is alive", snap.Head)
	}
	// A shell thread has no busy axis: it must read idle, never busy, and never
	// the unknown "?" that an unset value would render as.
	if snap.Busy != api.BusyIdle {
		t.Fatalf("busy = %q, want idle — a shell thread has no turn to execute", snap.Busy)
	}
	// It has no MARKED PANE, so a pane-based resolution must find nothing: this
	// is what proves head came from the session index, not the pane index.
	if _, found, err := srv.FindPaneByThreadID(tid); err != nil || found {
		t.Fatalf("a shell thread must have no marked pane (found=%v, err=%v)", found, err)
	}

	// Kill the session: the thread falls to headless — a remembered place.
	if _, err := raw("kill-session", "-t", "boxsess"); err != nil {
		t.Fatalf("kill-session: %v", err)
	}
	m.tick()
	snap, _ = m.stateOf(tid)
	if snap.Head != api.Headless {
		t.Fatalf("head = %q after the session died, want headless", snap.Head)
	}
}

// TestHostingShellThread pins the auto-parent resolution rules: only a placement
// into a SHELL thread's session yields a parent, and a stale marker never becomes
// a dangling parent id.
func TestHostingShellThread(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "sesh.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	sock := "seshhost-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", "boxsess", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck
	if _, err := raw("new-session", "-d", "-s", "plain", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session plain: %v", err)
	}
	if _, err := raw("new-session", "-d", "-s", "orphan", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session orphan: %v", err)
	}

	const shellID = "sh-host"
	if err := st.InsertThread(api.Thread{ID: shellID, Machine: "test", SessionName: "boxsess",
		AgentKind: api.ShellAgentKind, Cwd: "/tmp"}); err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	srv := tmux.NewServer(sock)
	if err := srv.StampSessionShellID("boxsess", shellID); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if err := srv.StampSessionShellID("orphan", "no-such-record"); err != nil {
		t.Fatalf("stamp orphan: %v", err)
	}
	d := &Daemon{store: st, tmux: srv, cfg: config.Config{Machine: "test"}}

	if got, err := d.hostingShellThread(api.NewThreadRequest{IntoSession: "boxsess"}); err != nil || got != shellID {
		t.Fatalf("into a shell thread's session = (%q, %v), want %s", got, err, shellID)
	}
	// A session that is NOT a shell thread's yields no parent — the common case,
	// and it must stay a root rather than erroring.
	if got, err := d.hostingShellThread(api.NewThreadRequest{IntoSession: "plain"}); err != nil || got != "" {
		t.Fatalf("into a plain session = (%q, %v), want no parent", got, err)
	}
	// No placement at all: the spawn opens its own session, so nothing hosts it.
	if got, err := d.hostingShellThread(api.NewThreadRequest{}); err != nil || got != "" {
		t.Fatalf("no placement = (%q, %v), want no parent", got, err)
	}
	// A STALE marker must NEVER become a dangling parent id.
	if got, err := d.hostingShellThread(api.NewThreadRequest{IntoSession: "orphan"}); err != nil || got != "" {
		t.Fatalf("into a session with a stale marker = (%q, %v), want no parent", got, err)
	}
	// Placement by PANE resolves through to the owning session.
	pane, err := raw("list-panes", "-t", "boxsess", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	if got, err := d.hostingShellThread(api.NewThreadRequest{IntoPane: strings.TrimSpace(pane)}); err != nil || got != shellID {
		t.Fatalf("into a pane of a shell thread's session = (%q, %v), want %s", got, err, shellID)
	}
	// An unresolvable pane is LOUD — never a silent "no parent", which would hide
	// a bad target behind a plausible result.
	if _, err := d.hostingShellThread(api.NewThreadRequest{IntoPane: "%999"}); err == nil {
		t.Fatal("an unresolvable placement target must be a loud error")
	}
}
