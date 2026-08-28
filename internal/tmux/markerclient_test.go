package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMarkerClientCurrentPicksTheRecordedClient is the regression for the bug that made
// `sesh tmux master-current` report ANOTHER master's thread.
//
// MarkerClientCurrent used to resolve with `display-message -p -c <client>`, but -c says
// where to PRINT, not what to expand the format against — so the format resolved against
// whatever tmux ambiently considered current, and every master watching one work server
// got the same answer. It was invisible while a machine had a single master attached
// (the ambient pick was then the right client) and wrong the moment a second one showed
// up, which is the normal state of a busy box.
//
// So the test needs TWO real clients on TWO sessions carrying DIFFERENT thread ids: with
// one client the old code passes by luck. It asserts BOTH markers resolve to their own
// client, which is what makes it discriminating in both directions.
func TestMarkerClientCurrentPicksTheRecordedClient(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := "seshmarker-test-" + strings.ReplaceAll(t.Name(), "/", "_")
	drv := sock + "-drv"
	tx := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	dx := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", drv}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck
		exec.Command("tmux", "-L", drv, "kill-server").Run()  //nolint:errcheck
	})

	for _, s := range []string{"alpha", "beta"} {
		if out, err := tx("-f", "/dev/null", "new-session", "-d", "-s", s, "-x", "80", "-y", "20"); err != nil {
			t.Fatalf("new-session %s: %v\n%s", s, err, out)
		}
	}
	// beta gets a SECOND window and sits on it, so the window index is discriminating
	// too (alpha stays on 0, beta on 1).
	if out, err := tx("new-window", "-t", "beta"); err != nil {
		t.Fatalf("new-window: %v\n%s", err, out)
	}
	mark := func(target, id string) {
		pane, err := tx("display-message", "-p", "-t", target, "-F", "#{pane_id}")
		if err != nil {
			t.Fatalf("resolve pane %s: %v", target, err)
		}
		if out, err := tx("set-option", "-p", "-t", pane, ThreadIDOption, id); err != nil {
			t.Fatalf("stamp %s: %v\n%s", target, err, out)
		}
	}
	mark("alpha", "THREAD-ALPHA")
	mark("beta", "THREAD-BETA")

	// Two REAL clients, one per session, each a nested attach from a driver server.
	if out, err := dx("-f", "/dev/null", "new-session", "-d", "-s", "d1", "-x", "80", "-y", "20",
		"env -u TMUX tmux -L "+sock+" attach -t alpha"); err != nil {
		t.Fatalf("driver 1: %v\n%s", err, out)
	}
	if out, err := dx("new-session", "-d", "-s", "d2", "-x", "80", "-y", "20",
		"env -u TMUX tmux -L "+sock+" attach -t beta"); err != nil {
		t.Fatalf("driver 2: %v\n%s", err, out)
	}
	// Wait for BOTH clients — with only one attached the old code passes vacuously.
	var clients string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		clients, _ = tx("list-clients", "-F", "#{client_name}\t#{client_pid}\t#{client_session}")
		if len(strings.Split(strings.TrimSpace(clients), "\n")) >= 2 && strings.Contains(clients, "alpha") && strings.Contains(clients, "beta") {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	bySession := map[string][2]string{} // session -> {name, pid}
	for _, ln := range strings.Split(strings.TrimSpace(clients), "\n") {
		f := strings.Split(ln, "\t")
		if len(f) == 3 {
			bySession[f[2]] = [2]string{f[0], f[1]}
		}
	}
	if len(bySession) < 2 {
		t.Fatalf("expected a client on each session, got %q", clients)
	}

	srv := NewServer(sock)
	home := t.TempDir()
	for _, c := range []struct {
		session, wantThread string
		wantWindow          int
	}{
		{"alpha", "THREAD-ALPHA", 0},
		{"beta", "THREAD-BETA", 1},
	} {
		cl := bySession[c.session]
		marker := filepath.Join(home, "master-client."+c.session)
		if err := os.WriteFile(marker, []byte(cl[0]+" "+cl[1]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gotSession, gotThread, gotWindow, err := srv.MarkerClientCurrent(marker)
		if err != nil {
			t.Fatalf("%s: MarkerClientCurrent: %v", c.session, err)
		}
		if gotThread != c.wantThread {
			t.Errorf("%s: thread = %q, want %q — the marker resolved ANOTHER client's thread", c.session, gotThread, c.wantThread)
		}
		if gotSession != c.session {
			t.Errorf("%s: session = %q, want %q", c.session, gotSession, c.session)
		}
		if gotWindow != c.wantWindow {
			t.Errorf("%s: window = %d, want %d", c.session, gotWindow, c.wantWindow)
		}
	}

	// A STALE marker (right shape, no such client) must resolve to nothing, not to
	// whichever client tmux would otherwise pick.
	stale := filepath.Join(home, "master-client.stale")
	if err := os.WriteFile(stale, []byte("/dev/pts/9999 999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, tid, w, err := srv.MarkerClientCurrent(stale)
	if err != nil {
		t.Fatalf("stale marker: %v", err)
	}
	if s != "" || tid != "" || w != -1 {
		t.Errorf("stale marker resolved to (%q,%q,%d), want empty — a dead client must never resolve", s, tid, w)
	}
}
