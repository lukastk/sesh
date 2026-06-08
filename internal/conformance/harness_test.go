package conformance

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
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
	Home       string
	Machine    string
	TmuxSocket string
	Runner     Runner
}

// newSandbox builds a sandbox for the given locality. The home is a fresh temp
// dir cleaned up with the test; the runner is local (exec) or remote (ssh
// localhost) accordingly. Each sandbox gets its OWN tmux socket so cells never
// touch the user's real sessions and never collide with each other.
func newSandbox(t *testing.T, loc matrix.Locality) *Sandbox {
	t.Helper()
	bin := seshBin(t)
	home := t.TempDir()
	stamp := time.Now().UnixNano()
	machine := fmt.Sprintf("sb-%s-%d", loc, stamp)
	socket := fmt.Sprintf("sesh-test-%s-%d", loc, stamp)

	env := map[string]string{
		"SESH_HOME":        home,
		"SESH_MACHINE":     machine,
		"SESH_TMUX_SOCKET": socket,
	}

	var r Runner
	switch loc {
	case matrix.Local:
		r = &localRunner{bin: bin, env: env}
	case matrix.Remote:
		ensureSSHLocalhost(t)
		r = &remoteRunner{bin: bin, env: env}
	default:
		t.Fatalf("newSandbox: unknown locality %q", loc)
	}
	sb := &Sandbox{Home: home, Machine: machine, TmuxSocket: socket, Runner: r}

	// Always tear down the daemon AND the tmux server at the end of the test,
	// regardless of where the test bailed — a leak would pollute later cells.
	t.Cleanup(func() {
		r.Run(t, "daemon", "stop")                              //nolint:errcheck — best-effort
		exec.Command("tmux", "-L", socket, "kill-server").Run() //nolint:errcheck — best-effort
	})
	return sb
}

// startDaemon starts the sandbox's daemon and fails the test if it does not
// come up.
func (sb *Sandbox) startDaemon(t *testing.T) {
	t.Helper()
	if _, stderr, err := sb.Runner.Run(t, "daemon", "start"); err != nil {
		t.Fatalf("daemon start: %v\n%s", err, stderr)
	}
}

// rawTmux runs `tmux -L <sandbox socket> <args...>` directly (bypassing sesh) so
// a cell can verify the OBSERVABLE tmux effect independently of sesh's own
// output. The tmux server is always on this box (remote = ssh localhost), so raw
// verification is local in both localities.
func (sb *Sandbox) rawTmux(t *testing.T, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"-L", sb.TmuxSocket}, args...)
	out, err := exec.Command("tmux", full...).CombinedOutput()
	return string(out), err
}

type localRunner struct {
	bin string
	env map[string]string
}

func (l *localRunner) Locality() matrix.Locality { return matrix.Local }

func (l *localRunner) Run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(l.bin, args...)
	cmd.Env = os.Environ()
	for k, v := range l.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return runCmd(cmd)
}

type remoteRunner struct {
	bin string
	env map[string]string
}

func (r *remoteRunner) Locality() matrix.Locality { return matrix.Remote }

// Run executes the command on the far side of a real ssh hop into localhost.
// This is the honest remote path: a second daemon, reached only via ssh.
func (r *remoteRunner) Run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	// Build: env K=V ... <bin> <args...>, shell-quoted.
	parts := []string{"env"}
	for _, k := range sortedEnvKeys(r.env) {
		parts = append(parts, k+"="+shellQuote(r.env[k]))
	}
	parts = append(parts, shellQuote(r.bin))
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

func sortedEnvKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
