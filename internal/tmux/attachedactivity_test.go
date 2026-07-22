package tmux

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestAttachedSessionsActivity proves the activity capture behind the notify
// hook's "is the user driving this session?" gate, against a REAL tmux server
// with a REAL nested client (`env -u TMUX tmux attach` in a driver pane — the
// established viewer trick):
//   - an attached session is present in the map with a sane client_activity;
//   - INPUT typed through the client bumps the activity;
//   - a detached session is absent from the map.
func TestAttachedSessionsActivity(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sock := "seshact-" + strings.ReplaceAll(t.Name(), "/", "_")
	raw := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	defer exec.Command("tmux", "-L", sock, "kill-server").Run() //nolint:errcheck

	// Two target sessions plus a driver session hosting the nested client.
	for _, s := range []string{"watched", "parked", "drv"} {
		if _, err := raw("-f", "/dev/null", "new-session", "-d", "-s", s, "-x", "80", "-y", "24"); err != nil {
			t.Fatalf("new-session %s: %v", s, err)
		}
	}
	if _, err := raw("send-keys", "-t", "drv", "-l",
		fmt.Sprintf("env -u TMUX tmux -L %s attach -t watched", sock)); err != nil {
		t.Fatalf("type attach: %v", err)
	}
	if _, err := raw("send-keys", "-t", "drv", "Enter"); err != nil {
		t.Fatalf("submit attach: %v", err)
	}

	srv := NewServer(sock)
	var acts map[string]int64
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		acts, err = srv.AttachedSessions()
		if err != nil {
			t.Fatalf("AttachedSessions: %v", err)
		}
		if _, ok := acts["watched"]; ok || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	first, ok := acts["watched"]
	if !ok {
		t.Fatalf("attached session missing from map: %v", acts)
	}
	now := time.Now().Unix()
	if first < now-30 || first > now+2 {
		t.Fatalf("activity %d implausible (now %d)", first, now)
	}
	if _, ok := acts["parked"]; ok {
		t.Fatalf("detached session wrongly reported attached: %v", acts)
	}

	// Input THROUGH the nested client (keys into the driver pane flow through
	// the client into the watched session) must bump the activity. tmux stamps
	// whole seconds, so cross a second boundary first.
	time.Sleep(1500 * time.Millisecond)
	if _, err := raw("send-keys", "-t", "drv", "-l", "x"); err != nil {
		t.Fatalf("type through client: %v", err)
	}
	if !eventually(5*time.Second, func() bool {
		acts, err := srv.AttachedSessions()
		return err == nil && acts["watched"] > first
	}) {
		acts, _ := srv.AttachedSessions()
		t.Fatalf("activity never bumped past %d after input: %v", first, acts)
	}
}

func eventually(d time.Duration, f func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
