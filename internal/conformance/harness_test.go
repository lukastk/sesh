package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
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

// Sandbox is an isolated daemon home plus the runners that drive it. For a local
// sandbox Runner and daemonRunner are the same. For a REMOTE (routed) sandbox,
// Home/Machine/TmuxSocket describe the PEER machine; daemonRunner manages the
// peer daemon directly (with the peer's full env), while Runner drives
// OPERATIONS from a separate local client via `--machine peer` routing (a real
// ssh hop) — so the test exercises the same code path that the v1 `--machine X`
// bug broke.
type Sandbox struct {
	Home         string
	Machine      string
	TmuxSocket   string
	MasterSocket string
	Runner       Runner
	daemonRunner Runner

	// APIAddr/APIToken are set when the sandbox's daemon was started with its TCP
	// API exposed (withAPI) — so a test can register it as an http-transport peer.
	APIAddr  string
	APIToken string
}

// sandboxConfig collects newSandbox options.
type sandboxConfig struct {
	apiAddr  string
	apiToken string
	tmuxConf string
}

type sandboxOpt func(*sandboxConfig)

// withAPI starts the sandbox's daemon with its TCP API exposed on addr behind
// token — i.e. reachable over the HTTP transport, not just ssh.
func withAPI(addr, token string) sandboxOpt {
	return func(c *sandboxConfig) { c.apiAddr = addr; c.apiToken = token }
}

// withTmuxConf starts the sandbox's daemon with SESH_TMUX_CONF=path, so the WORK
// tmux server it spawns sessions on sources that `-f` config.
func withTmuxConf(path string) sandboxOpt {
	return func(c *sandboxConfig) { c.tmuxConf = path }
}

// newSandbox builds a sandbox for the given locality. The home is a fresh temp
// dir cleaned up with the test; the runner is local (exec) or remote (ssh
// localhost) accordingly. Each sandbox gets its OWN tmux socket so cells never
// touch the user's real sessions and never collide with each other.
func newSandbox(t *testing.T, loc matrix.Locality, opts ...sandboxOpt) *Sandbox {
	t.Helper()
	var sc sandboxConfig
	for _, o := range opts {
		o(&sc)
	}
	bin := seshBin(t)
	home := t.TempDir()
	stamp := time.Now().UnixNano()
	machine := fmt.Sprintf("sb-%s-%d", loc, stamp)
	socket := fmt.Sprintf("sesh-test-%s-%d", loc, stamp)
	masterSocket := fmt.Sprintf("sesh-test-master-%s-%d", loc, stamp)

	env := map[string]string{
		"SESH_HOME":        home,
		"SESH_MACHINE":     machine,
		"SESH_TMUX_SOCKET": socket,
		// Isolate the master socket too: its default is the user's live
		// "mymastertmux". No non-nav test touches it, but a clash would corrupt the
		// user's real master view, so we never leave it at the default.
		"SESH_MASTER_SOCKET": masterSocket,
		// Isolated codex home so sesh's per-cwd trust writes (which suppress
		// codex's directory-trust prompt) never touch the user's real ~/.codex.
		// auth is symlinked so codex stays authenticated.
		"SESH_CODEX_HOME": setupCodexHome(t),
	}
	if sc.apiAddr != "" {
		env["SESH_API_ADDR"] = sc.apiAddr
		env["SESH_API_TOKEN"] = sc.apiToken
	}
	if sc.tmuxConf != "" {
		env["SESH_TMUX_CONF"] = sc.tmuxConf
	}
	peerDaemon := &localRunner{bin: bin, env: env}

	var sb *Sandbox
	switch loc {
	case matrix.Local:
		sb = &Sandbox{Home: home, Machine: machine, TmuxSocket: socket, MasterSocket: masterSocket, Runner: peerDaemon, daemonRunner: peerDaemon, APIAddr: sc.apiAddr, APIToken: sc.apiToken}
	case matrix.Remote:
		// A separate local client machine that reaches the peer via `--machine`.
		ensureSSHLocalhost(t)
		clientHome := t.TempDir()
		clientMachine := fmt.Sprintf("client-%d", stamp)
		clientEnv := map[string]string{
			"SESH_HOME":          clientHome,
			"SESH_MACHINE":       clientMachine,
			"SESH_MASTER_SOCKET": fmt.Sprintf("sesh-test-cmaster-%d", stamp),
		}
		// Register the peer with the client (exercises `sesh peer add`).
		client := &localRunner{bin: bin, env: clientEnv}
		if _, stderr, err := client.Run(t, "peer", "add", "--machine", machine, "--ssh", "localhost", "--home", home, "--binary", bin); err != nil {
			t.Fatalf("peer add: %v\n%s", err, stderr)
		}
		sb = &Sandbox{
			Home: home, Machine: machine, TmuxSocket: socket,
			Runner:       &routingRunner{bin: bin, env: clientEnv, peerMachine: machine},
			daemonRunner: peerDaemon, // peer daemon started directly with its full env
		}
	default:
		t.Fatalf("newSandbox: unknown locality %q", loc)
	}

	// Always tear down the daemon AND the tmux server at the end of the test,
	// regardless of where the test bailed — a leak would pollute later cells.
	t.Cleanup(func() {
		sb.daemonRunner.Run(t, "daemon", "stop")                      //nolint:errcheck — best-effort
		exec.Command("tmux", "-L", socket, "kill-server").Run()       //nolint:errcheck — best-effort
		exec.Command("tmux", "-L", masterSocket, "kill-server").Run() //nolint:errcheck — best-effort
	})
	return sb
}

// newSSHSandbox builds a remote sandbox whose operations run ON the far machine
// via a real ssh hop (sshRunner) — for daemon.lifecycle/remote, which manages the
// remote daemon directly rather than routing operations to it.
func newSSHSandbox(t *testing.T) *Sandbox {
	t.Helper()
	bin := seshBin(t)
	ensureSSHLocalhost(t)
	home := t.TempDir()
	stamp := time.Now().UnixNano()
	machine := fmt.Sprintf("sb-ssh-%d", stamp)
	socket := fmt.Sprintf("sesh-test-ssh-%d", stamp)
	env := map[string]string{
		"SESH_HOME":          home,
		"SESH_MACHINE":       machine,
		"SESH_TMUX_SOCKET":   socket,
		"SESH_MASTER_SOCKET": fmt.Sprintf("sesh-test-master-ssh-%d", stamp),
	}
	r := &sshRunner{bin: bin, env: env}
	sb := &Sandbox{Home: home, Machine: machine, TmuxSocket: socket, Runner: r, daemonRunner: r}
	t.Cleanup(func() {
		r.Run(t, "daemon", "stop")                              //nolint:errcheck
		exec.Command("tmux", "-L", socket, "kill-server").Run() //nolint:errcheck
	})
	return sb
}

// setupCodexHome makes an isolated CODEX_HOME with the user's codex auth symlinked
// in, so codex stays authenticated but sesh's trust writes are sandboxed. Skips the
// symlink if the user has no codex auth (codex cells then fail loudly rather than
// silently, which is correct).
//
// It is deliberately NOT a subdir of the sandbox's t.TempDir: codex spawns
// background children (plugin clones) that keep writing into CODEX_HOME/.tmp as the
// test ends, which races t.TempDir's automatic RemoveAll ("directory not empty").
// Instead it is its own dir with a RETRYING cleanup, registered before the sandbox
// teardown so it runs AFTER the daemon/tmux (and thus codex) are killed.
func setupCodexHome(t *testing.T) string {
	t.Helper()
	ch, err := os.MkdirTemp("", "sesh-codex-home-")
	if err != nil {
		t.Fatalf("setup codex home: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if os.RemoveAll(ch) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond) // a codex child is still writing; retry
		}
	})
	if uh, err := os.UserHomeDir(); err == nil {
		realAuth := filepath.Join(uh, ".codex", "auth.json")
		if _, err := os.Stat(realAuth); err == nil {
			os.Symlink(realAuth, filepath.Join(ch, "auth.json")) //nolint:errcheck
		}
	}
	return ch
}

// startDaemon starts the sandbox's (target machine's) daemon directly and fails
// the test if it does not come up. Operations issued later via sb.Runner reach
// this daemon (locally, or via `--machine` routing for a remote sandbox).
func (sb *Sandbox) startDaemon(t *testing.T) {
	t.Helper()
	if _, stderr, err := sb.daemonRunner.Run(t, "daemon", "start"); err != nil {
		t.Fatalf("daemon start: %v\n%s", err, stderr)
	}
}

// newThread spawns a thread via sesh and returns the parsed record.
func (sb *Sandbox) newThread(t *testing.T, agent, name, cwd string) api.Thread {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "new", "--agent", agent, "--name", name, "--cwd", cwd, "--json")
	if err != nil {
		t.Fatalf("thread new (%s): %v\n%s", agent, err, stderr)
	}
	var th api.Thread
	if err := json.Unmarshal([]byte(stdout), &th); err != nil {
		t.Fatalf("decode thread json: %v\nraw: %s", err, stdout)
	}
	return th
}

// waitThreadReady waits until an agent thread is genuinely ready for input:
// its agent process is running, its TUI has actually RENDERED (a pre-init pane
// is blank/static and would falsely read as idle), and activity has settled to
// waiting. Returns the marked pane id. Sending before this is met loses
// keystrokes — the agent is not yet listening.
func (sb *Sandbox) waitThreadReady(t *testing.T, threadID, agent string) string {
	t.Helper()
	var pane string
	ok := waitUntil(agentStartTimeout, func() bool {
		p, pid, ok := sb.markedPane(t, threadID)
		if !ok || !agentRunningUnder(pid, agent) {
			return false
		}
		pane = p
		cap, err := sb.rawTmux(t, "capture-pane", "-t", p, "-p")
		if err != nil || nonBlankLines(cap) < 3 {
			return false // TUI has not rendered yet
		}
		return sb.threadStatus(t, threadID).Activity == api.ActivityWaiting
	})
	if !ok {
		t.Fatalf("%s thread never became ready for input", agent)
	}
	return pane
}

func nonBlankLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// threadStatus fetches a thread's live runtime status via sesh.
func (sb *Sandbox) threadStatus(t *testing.T, id string) api.ThreadStatusResponse {
	t.Helper()
	stdout, stderr, err := sb.Runner.Run(t, "thread", "status", "--id", id, "--json")
	if err != nil {
		t.Fatalf("thread status: %v\n%s", err, stderr)
	}
	var st api.ThreadStatusResponse
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		t.Fatalf("decode thread status: %v\nraw: %s", err, stdout)
	}
	return st
}

// sendKeys types text into a pane and presses Enter, directly via tmux (the
// "world" action of a user typing — independent of sesh's own send feature).
func (sb *Sandbox) sendKeys(t *testing.T, pane, text string) {
	t.Helper()
	if out, err := sb.rawTmux(t, "send-keys", "-t", pane, "-l", text); err != nil {
		t.Fatalf("send-keys text: %v\n%s", err, out)
	}
	time.Sleep(250 * time.Millisecond) // let the TUI register the text before Enter (codex drops it otherwise)
	if out, err := sb.rawTmux(t, "send-keys", "-t", pane, "Enter"); err != nil {
		t.Fatalf("send-keys enter: %v\n%s", err, out)
	}
}

// attachViewer attaches a real tmux client to session (so its attachment axis
// flips to attached). The viewer is a second session whose command attaches to
// the target with $TMUX cleared (nested attach). Torn down with the test.
func (sb *Sandbox) attachViewer(t *testing.T, session string) {
	t.Helper()
	viewer := "viewer_" + session
	cmd := "env -u TMUX tmux -L " + sb.TmuxSocket + " attach -t " + session
	if out, err := sb.rawTmux(t, "new-session", "-d", "-s", viewer, cmd); err != nil {
		t.Fatalf("attach viewer: %v\n%s", err, out)
	}
	t.Cleanup(func() { sb.rawTmux(t, "kill-session", "-t", "="+viewer) }) //nolint:errcheck
}

// markedPane returns the (pane id, pane pid) of the pane bearing threadID's
// @sesh-thread-id marker, read directly from tmux (independent of sesh). ok is
// false if no such pane exists.
func (sb *Sandbox) markedPane(t *testing.T, threadID string) (pane string, pid int, ok bool) {
	t.Helper()
	out, err := sb.rawTmux(t, "list-panes", "-a", "-F", "#{pane_id}\t#{pane_pid}\t#{@sesh-thread-id}")
	if err != nil {
		return "", 0, false
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 3 && f[2] == threadID {
			pid, _ := strconv.Atoi(strings.TrimSpace(f[1]))
			return f[0], pid, true
		}
	}
	return "", 0, false
}

// agentRunningUnder reports whether a REAL process of the given agent kind is
// running in panePID's process subtree, determined by an INDEPENDENT `ps` walk
// (not sesh's own proctree). This is the honest "did the real agent spawn"
// check; codex runs as a child of a `node` pane process, so we must walk
// descendants, not just the pane process itself.
func agentRunningUnder(panePID int, agent string) bool {
	out, err := exec.Command("ps", "-eww", "-o", "pid=,ppid=,comm=").Output()
	if err != nil {
		return false
	}
	type row struct {
		ppid int
		comm string
	}
	procs := map[int]row{}
	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		procs[pid] = row{ppid: ppid, comm: f[2]}
		children[ppid] = append(children[ppid], pid)
	}
	// BFS from panePID (inclusive).
	queue := []int{panePID}
	seen := map[int]bool{}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if procs[pid].comm == agent {
			return true
		}
		queue = append(queue, children[pid]...)
	}
	return false
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

// sandboxEnv builds a child env from the current process's, but with every
// inherited SESH_* variable STRIPPED before the sandbox's own values are applied.
// The user may be running the test from a shell that has its real sesh env set
// (they run the live old sesh); nothing of theirs must leak into a test process.
func sandboxEnv(extra map[string]string) []string {
	var out []string
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(name, "SESH_") {
			continue // never inherit the user's sesh config/sockets
		}
		out = append(out, e)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

type localRunner struct {
	bin string
	env map[string]string
}

func (l *localRunner) Locality() matrix.Locality { return matrix.Local }

func (l *localRunner) Run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(l.bin, args...)
	cmd.Env = sandboxEnv(l.env)
	return runCmd(cmd)
}

// routingRunner drives OPERATIONS against a remote peer by running the local
// sesh client (as a separate client machine) with `--machine peer` appended, so
// main's global router forwards the command to the peer over a real ssh hop.
// This is the honest remote operational path — it exercises the exact `--machine`
// routing the v1 bug broke.
type routingRunner struct {
	bin         string
	env         map[string]string // the local CLIENT machine's env (holds peers.json)
	peerMachine string
}

func (r *routingRunner) Locality() matrix.Locality { return matrix.Remote }

func (r *routingRunner) Run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(r.bin, append(args, "--machine", r.peerMachine)...)
	cmd.Env = sandboxEnv(r.env)
	return runCmd(cmd)
}

// sshRunner runs sesh ON the far machine via a real ssh hop into localhost — used
// for daemon.lifecycle/remote, where the honest test is to MANAGE the remote
// daemon directly (you reach a machine to manage its daemon; there is no
// routing).
type sshRunner struct {
	bin string
	env map[string]string
}

func (r *sshRunner) Locality() matrix.Locality { return matrix.Remote }

func (r *sshRunner) Run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	parts := []string{"env"}
	for _, k := range sortedEnvKeys(r.env) {
		parts = append(parts, k+"="+shellQuote(r.env[k]))
	}
	parts = append(parts, shellQuote(r.bin))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	cmd := exec.Command("ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"localhost",
		strings.Join(parts, " "),
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

// runSSH runs a command on localhost via a real ssh hop (shell-quoted), for
// tests that need to exercise the remote side directly (e.g. curl a peer socket).
func runSSH(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	cmd := exec.Command("ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"localhost",
		strings.Join(parts, " "),
	)
	return runCmd(cmd)
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
