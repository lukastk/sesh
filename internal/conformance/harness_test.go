package conformance

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

// This file is the shared e2e harness for the conformance matrix. It exercises
// the REAL sesh binary — never a mock — locally and over a REAL `ssh localhost`
// hop. Per AGENTS.md, the ssh-localhost remote path is the honest stand-in for
// a remote machine: it drives the actual remote code path (it would have caught
// the v1 `--machine X` bug), while staying deterministic in CI.

var (
	buildOnce   sync.Once
	builtBin    string
	builtBinErr error
	builtBinDir string
)

// seshBin builds the sesh binary once per test process and returns its absolute
// path. A real binary is the thing under test for every lifecycle cell.
func seshBin(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sesh-conformance-")
		if err != nil {
			builtBinErr = err
			return
		}
		builtBinDir = dir
		bin := dir + "/sesh"
		cmd := exec.Command("go", "build", "-o", bin, "github.com/lukastk/sesh/cmd/sesh")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			builtBinErr = fmt.Errorf("build sesh: %v\n%s", err, out.String())
			return
		}
		builtBin = bin
	})
	if builtBinErr != nil {
		t.Fatalf("seshBin: %v", builtBinErr)
	}
	return builtBin
}

// cleanupHarness removes the built binary dir. Called from TestMain.
func cleanupHarness() {
	if builtBinDir != "" {
		os.RemoveAll(builtBinDir)
	}
}

// Runner runs sesh commands for one locality against one isolated SESH_HOME.
type Runner interface {
	// Run executes `sesh <args...>` and returns combined stdout, stderr, error.
	Run(t *testing.T, args ...string) (stdout, stderr string, err error)
	Locality() matrix.Locality
}

// Sandbox is an isolated daemon home plus the runner that drives it.
type Sandbox struct {
	Home    string
	Machine string
	Runner  Runner
}

// newSandbox builds a sandbox for the given locality. The home is a fresh temp
// dir cleaned up with the test; the runner is local (exec) or remote (ssh
// localhost) accordingly.
func newSandbox(t *testing.T, loc matrix.Locality) *Sandbox {
	t.Helper()
	bin := seshBin(t)
	home := t.TempDir()
	machine := fmt.Sprintf("sb-%s-%d", loc, time.Now().UnixNano())

	var r Runner
	switch loc {
	case matrix.Local:
		r = &localRunner{bin: bin, home: home, machine: machine}
	case matrix.Remote:
		ensureSSHLocalhost(t)
		r = &remoteRunner{bin: bin, home: home, machine: machine}
	default:
		t.Fatalf("newSandbox: unknown locality %q", loc)
	}
	sb := &Sandbox{Home: home, Machine: machine, Runner: r}

	// Always tear the daemon down at the end of the test, regardless of where
	// the test bailed — a leaked daemon would pollute later cells.
	t.Cleanup(func() {
		r.Run(t, "daemon", "stop") //nolint:errcheck — best-effort teardown
	})
	return sb
}

type localRunner struct {
	bin, home, machine string
}

func (l *localRunner) Locality() matrix.Locality { return matrix.Local }

func (l *localRunner) Run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(l.bin, args...)
	cmd.Env = append(os.Environ(),
		"SESH_HOME="+l.home,
		"SESH_MACHINE="+l.machine,
	)
	return runCmd(cmd)
}

type remoteRunner struct {
	bin, home, machine string
}

func (r *remoteRunner) Locality() matrix.Locality { return matrix.Remote }

// Run executes the command on the far side of a real ssh hop into localhost.
// This is the honest remote path: a second daemon, reached only via ssh.
func (r *remoteRunner) Run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	// Build: env SESH_HOME=.. SESH_MACHINE=.. <bin> <args...>, shell-quoted.
	parts := []string{
		"env",
		"SESH_HOME=" + shellQuote(r.home),
		"SESH_MACHINE=" + shellQuote(r.machine),
		shellQuote(r.bin),
	}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	remoteCmd := strings.Join(parts, " ")
	cmd := exec.Command("ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"localhost",
		remoteCmd,
	)
	return runCmd(cmd)
}

func runCmd(cmd *exec.Cmd) (string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ensureSSHLocalhost skips the test loudly (not silently) if passwordless ssh
// localhost is unavailable, so a missing prerequisite never masquerades as a
// green remote cell.
func ensureSSHLocalhost(t *testing.T) {
	t.Helper()
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "localhost", "true")
	if err := cmd.Run(); err != nil {
		t.Skipf("NOT VERIFIABLE: passwordless `ssh localhost` unavailable: %v", err)
	}
}

// shellQuote single-quotes a string for safe use in a remote shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pidAlive reports whether a process with pid is alive (signal 0 probe). Used to
// assert the *observable external effect* — a real process — not internal state.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// waitUntil polls fn until it returns true or the timeout elapses.
func waitUntil(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fn()
}
