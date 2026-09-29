package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestNavPrevRoundTrip covers the prefix+L history file: write/read a location, the
// missing/malformed cases (ok=false), and that window 0 (a valid index) round-trips.
func TestNavPrevRoundTrip(t *testing.T) {
	home := t.TempDir()

	// Missing file → not ok.
	if _, _, _, ok := readNavPrev(home); ok {
		t.Errorf("readNavPrev on a fresh home should be ok=false")
	}

	writeNavPrev(home, "macbook", "sesh_proj <ab>", 3)
	m, s, w, ok := readNavPrev(home)
	if !ok || m != "macbook" || s != "sesh_proj <ab>" || w != 3 {
		t.Errorf("round-trip = (%q,%q,%d,%v), want (macbook, sesh_proj <ab>, 3, true)", m, s, w, ok)
	}

	// Window 0 is a real index (not "unset") and must round-trip.
	writeNavPrev(home, "mymain", "scratch", 0)
	if m, s, w, ok := readNavPrev(home); !ok || m != "mymain" || s != "scratch" || w != 0 {
		t.Errorf("window-0 round-trip = (%q,%q,%d,%v), want (mymain, scratch, 0, true)", m, s, w, ok)
	}

	// Overwrite (toggle semantics rely on the latest write winning).
	writeNavPrev(home, "macstudio", "box", 1)
	if m, _, _, _ := readNavPrev(home); m != "macstudio" {
		t.Errorf("overwrite did not take: machine=%q", m)
	}
}

// TestMasterClientWindowIgnoresTheAmbientClient is the unit regression for #14.
//
// resolveMasterLocation (prefix+L's from-location) used to read the carrier client's
// master window with `display-message -p -c <carrier>`. -c only says where to PRINT; the
// format expands against whichever client tmux ambiently considers current — the one that
// last ran switch-client (H98 follow-up 2). Clients in ONE session share its current
// window, so the bug needs a client in a SECOND session: the test therefore attaches a real
// carrier to "master" (sitting on its SECOND window, so the answer is not the default) and
// a real DECOY to another session, and switch-client's the decoy LAST so it is the ambient
// pick. Each client must still resolve to its own window.
//
// Socket names carry the pid + a timestamp: a fixed name is poisoned for every later run
// if a killed test binary leaks its server (H116).
func TestMasterClientWindowIgnoresTheAmbientClient(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := fmt.Sprintf("seshnavlast-test-%d-%d", os.Getpid(), time.Now().UnixNano())
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

	// The master session: two machine windows; the carrier will sit on the SECOND.
	if out, err := tx("-f", "/dev/null", "new-session", "-d", "-s", "master", "-n", "machine-a", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session master: %v\n%s", err, out)
	}
	if out, err := tx("new-window", "-t", "master", "-n", "machine-b"); err != nil {
		t.Fatalf("new-window: %v\n%s", err, out)
	}
	// A SECOND session on the same server, where the decoy lives.
	if out, err := tx("new-session", "-d", "-s", "elsewhere", "-n", "decoy-window", "-x", "80", "-y", "20"); err != nil {
		t.Fatalf("new-session elsewhere: %v\n%s", err, out)
	}

	// Two REAL clients, each a nested attach from a driver server.
	if out, err := dx("-f", "/dev/null", "new-session", "-d", "-s", "d1", "-x", "80", "-y", "20",
		"env -u TMUX tmux -L "+sock+" attach -t master"); err != nil {
		t.Fatalf("driver 1: %v\n%s", err, out)
	}
	if out, err := dx("new-session", "-d", "-s", "d2", "-x", "80", "-y", "20",
		"env -u TMUX tmux -L "+sock+" attach -t elsewhere"); err != nil {
		t.Fatalf("driver 2: %v\n%s", err, out)
	}
	bySession := map[string]string{}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := tx("list-clients", "-F", "#{client_name}\t#{client_session}")
		bySession = map[string]string{}
		for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
			if name, sess, ok := strings.Cut(ln, "\t"); ok {
				bySession[sess] = name
			}
		}
		if bySession["master"] != "" && bySession["elsewhere"] != "" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	carrier, decoy := bySession["master"], bySession["elsewhere"]
	if carrier == "" || decoy == "" {
		t.Fatalf("expected a client on each session, got %v", bySession)
	}

	// The carrier sits on machine-b; then the DECOY moves LAST, making it tmux's ambient
	// client — exactly what an ambient read would then answer with.
	if out, err := tx("select-window", "-t", "master:machine-b"); err != nil {
		t.Fatalf("select-window: %v\n%s", err, out)
	}
	if out, err := tx("switch-client", "-c", decoy, "-t", "elsewhere"); err != nil {
		t.Fatalf("bump decoy: %v\n%s", err, out)
	}

	for _, c := range []struct{ client, want string }{
		{carrier, "machine-b"},
		{decoy, "decoy-window"},
	} {
		got, err := masterClientWindow(sock, c.client)
		if err != nil {
			t.Fatalf("masterClientWindow(%s): %v", c.client, err)
		}
		if got != c.want {
			t.Errorf("masterClientWindow(%s) = %q, want %q — it resolved ANOTHER client's window (the decoy %s moved last)", c.client, got, c.want, decoy)
		}
	}

	// A carrier that is not attached is an ERROR, never a guess.
	if got, err := masterClientWindow(sock, "/dev/no-such-client"); err == nil {
		t.Fatalf("an unattached carrier resolved to %q; want an error", got)
	}
}
