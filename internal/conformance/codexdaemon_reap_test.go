package conformance

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestParsePsEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:05":       5 * time.Second,
		"17:38":       17*time.Minute + 38*time.Second,
		"01:02:03":    time.Hour + 2*time.Minute + 3*time.Second,
		"2-01:02:03":  49*time.Hour + 2*time.Minute + 3*time.Second,
		"12-00:00:00": 12 * 24 * time.Hour,
	} {
		got, err := parsePsEtime(in)
		if err != nil || got != want {
			t.Errorf("parsePsEtime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "5", "a:b", "1-2"} {
		if _, err := parsePsEtime(bad); err == nil {
			t.Errorf("parsePsEtime(%q) accepted garbage", bad)
		}
	}
}

// A real process whose executable lives INSIDE a codex home must be killed; a real
// process of the same binary OUTSIDE it must not; and the age guard must spare a
// process younger than the cutoff (a concurrent run's daemon).
func TestKillCodexHomeProcs(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("NOT IMPLEMENTED: no sleep binary to stand in for the daemon")
	}
	home := t.TempDir() + string(os.PathSeparator)
	daemonBin := filepath.Join(home, "packages", "app-server-daemon", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(daemonBin), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sleepBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(daemonBin, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(bin string) *exec.Cmd {
		c := exec.Command(bin, "300")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		go c.Wait() //nolint:errcheck — reaps the zombie so liveness checks are honest
		t.Cleanup(func() { c.Process.Kill() }) //nolint:errcheck
		return c
	}
	daemon := start(daemonBin)
	decoy := start(sleepBin)
	alive := func(c *exec.Cmd) bool { return syscall.Kill(c.Process.Pid, 0) == nil }

	// Age guard: nothing here is an hour old, so nothing may be touched.
	if n, err := killCodexHomeProcs(home, time.Hour); err != nil || n != 0 {
		t.Fatalf("young process reaped: n=%d err=%v", n, err)
	}
	if !alive(daemon) {
		t.Fatal("the age guard did not protect a young daemon")
	}

	n, err := killCodexHomeProcs(home, 0)
	if err != nil || n != 1 {
		t.Fatalf("killCodexHomeProcs = %d, %v; want exactly the one in-home process", n, err)
	}
	for i := 0; i < 40 && alive(daemon); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(daemon) {
		t.Fatal("the in-home daemon survived")
	}
	if !alive(decoy) {
		t.Fatal("a process OUTSIDE the codex home was killed")
	}
}
