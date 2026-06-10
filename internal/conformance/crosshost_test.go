package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/client"
)

// TestRealCrossHost is the one thing the ssh-localhost matrix cells cannot stand
// in for: a GENUINELY separate physical host. It runs the agent-spawn path against
// the partner machine over a real network ssh hop — from mymain it targets macbook
// and vice versa (the pairing in $MYRIG_MACHINES). It is environment-gated and
// SKIPS WITH A WARNING when it can't run (not on mymain/macbook, $MYRIG_MACHINES
// unset, partner unreachable, or v2 sesh not installed on the partner).
//
// PREREQUISITE (manual, not automated — see AGENTS.md): the v2 sesh binary must be
// installed on the partner at ~/.local/bin/sesh-v2 (built for the partner's
// GOOS/GOARCH). The test never ships the binary; it assumes it is present.
//
// Isolation: it runs an isolated daemon on the partner (own SESH_HOME, tmux socket,
// master socket, CODEX_HOME) under /tmp, and tears it all down — it never touches
// the partner's live old sesh.
func TestRealCrossHost(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// Self-detection uses $MYRIG_TARGETS, which IS exported (unlike the
	// $MYRIG_MACHINES zsh assoc array): this machine's name is its own active target.
	self := crossHostSelf()
	if self == "" {
		t.Skip("NOT RUN: cross-host test runs on mymain/macbook only (neither is an active myrig target here)")
	}
	partnerName := crossHostPartner[self]

	// The partner's ssh dest comes from $MYRIG_MACHINES, which is a zsh assoc array
	// and NOT exported by default — run with it exported (see AGENTS.md).
	machines := myrigMachines()
	if len(machines) == 0 {
		t.Skipf("NOT RUN: $MYRIG_MACHINES not visible to the test (it's a zsh assoc array, not exported). Run:\n" +
			"  MYRIG_MACHINES=\"$MYRIG_MACHINES\" go test ./internal/conformance -run TestRealCrossHost -v")
	}
	partner, found := machineByName(machines, partnerName)
	if !found {
		t.Skipf("WARNING NOT RUN: partner %q not in $MYRIG_MACHINES", partnerName)
	}

	bin := seshBin(t)

	// Preflight: reachable, v2 sesh present, and discover the partner's $HOME.
	rhomeBase, rbin, err := crossHostPreflight(partner.dest, partner.port)
	if err != nil {
		t.Skipf("WARNING NOT RUN: partner %q unusable for cross-host test: %v\n  (install v2 sesh at ~/.local/bin/sesh-v2 on %s — see AGENTS.md)", partnerName, err, partnerName)
	}

	stamp := time.Now().UnixNano()
	rhome := fmt.Sprintf("%s/sesh-xhost-%d", rhomeBase, stamp)
	rcodex := fmt.Sprintf("%s/sesh-xhost-codex-%d", rhomeBase, stamp)
	rsock := fmt.Sprintf("sesh-xhost-%d", stamp)
	rmaster := fmt.Sprintf("sesh-xhost-master-%d", stamp)
	remoteEnv := strings.Join([]string{
		"env",
		"SESH_HOME=" + rhome,
		"SESH_MACHINE=" + partnerName,
		"SESH_TMUX_SOCKET=" + rsock,
		"SESH_MASTER_SOCKET=" + rmaster,
		"SESH_CODEX_HOME=" + rcodex,
		rbin,
	}, " ")

	// Start an isolated daemon on the partner (codex auth symlinked so codex stays
	// authenticated while its trust writes stay sandboxed).
	setup := fmt.Sprintf("mkdir -p %s && ln -sf $HOME/.codex/auth.json %s/auth.json; %s daemon start", rcodex, rcodex, remoteEnv)
	if out, err := crossHostSSH(partner.dest, partner.port, setup); err != nil {
		t.Fatalf("start remote daemon on %s: %v\n%s", partnerName, err, out)
	}
	t.Cleanup(func() {
		crossHostSSH(partner.dest, partner.port, fmt.Sprintf("%s daemon stop >/dev/null 2>&1; tmux -L %s kill-server 2>/dev/null; rm -rf %s %s", remoteEnv, rsock, rhome, rcodex)) //nolint:errcheck
	})

	// Local client: its own isolated home (holds peers.json); identity = self.
	clientHome := t.TempDir()
	clientEnv := map[string]string{"SESH_HOME": clientHome, "SESH_MACHINE": self}
	runLocal := func(args ...string) (string, string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = sandboxEnv(clientEnv)
		return runCmd(cmd)
	}

	if _, stderr, err := runLocal("peer", "add", "--machine", partnerName, "--ssh", partner.dest, "--port", partner.port, "--home", rhome, "--binary", rbin, "--tmux-socket", rsock); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}

	routedActivity := func(id string) api.Activity {
		stdout, _, err := runLocal("thread", "status", "--id", id, "--machine", partnerName, "--json")
		if err != nil {
			return "ERR"
		}
		var st api.ThreadStatusResponse
		if json.Unmarshal([]byte(strings.TrimSpace(stdout)), &st) != nil {
			return "ERR"
		}
		return st.Activity
	}

	// For each agent: spawn on the partner, prove it's genuinely alive THERE (a
	// routed status that resolves a live pane can only come from the partner's
	// daemon — there is no local daemon in this test), then stop + delete.
	for _, agent := range []string{"claude", "codex", "pi"} {
		agent := agent
		t.Run(agent, func(t *testing.T) {
			name := "xh-" + agent
			stdout, stderr, err := runLocal("thread", "new", "--machine", partnerName, "--agent", agent, "--name", name, "--cwd", "/tmp")
			if err != nil {
				t.Fatalf("remote spawn %s: %v\n%s", agent, err, stderr)
			}
			id := strings.TrimSpace(stdout)
			t.Cleanup(func() {
				runLocal("thread", "stop", "--id", id, "--machine", partnerName)   //nolint:errcheck
				runLocal("thread", "delete", "--id", id, "--machine", partnerName) //nolint:errcheck
			})

			// Alive ON the partner.
			if !waitUntil(45*time.Second, func() bool { return routedActivity(id) == api.ActivityWaiting }) {
				t.Fatalf("%s thread never became alive on %s (activity=%s)", agent, partnerName, routedActivity(id))
			}

			// Stop ends the runtime on the partner; the routed status flips to dead.
			if _, stderr, err := runLocal("thread", "stop", "--id", id, "--machine", partnerName); err != nil {
				t.Fatalf("remote stop: %v\n%s", err, stderr)
			}
			if !waitUntil(15*time.Second, func() bool { return routedActivity(id) == api.ActivityIdle }) {
				t.Errorf("%s thread still alive on %s after stop", agent, partnerName)
			}
		})
	}
}

// TestRealCrossHostHTTP is the real-network proof for the HTTP transport — the one
// thing the 127.0.0.1 `.http` matrix cells cannot stand in for. Same pairing/self-
// gating as TestRealCrossHost (from mymain it targets macbook and vice versa), but
// the partner's daemon is reached over its TCP API across the REAL network (tailscale)
// instead of ssh-exec.
//
// It registers the partner as an http peer with a deliberately BROKEN ssh dest, so a
// green run PROVES every cross-machine path crossed the real network over HTTP — a
// silent ssh attempt would fail. It exercises all three http paths: ROUTING
// (`thread new/delete --machine`), live FAN-OUT (`thread list --all-machines`), and
// the background SYNC (`/v1/mesh`).
//
// PREREQUISITE (manual, see AGENTS.md): a CURRENT v2 sesh at ~/.local/bin/sesh-v2 on
// the partner — re-install after any schema change (this session added the http
// transport, so a pre-mesh binary is stale). A stale binary => loud skip, not a
// confusing failure.
func TestRealCrossHostHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	self := crossHostSelf()
	if self == "" {
		t.Skip("NOT RUN: cross-host test runs on mymain/macbook only (neither is an active myrig target here)")
	}
	partnerName := crossHostPartner[self]
	machines := myrigMachines()
	if len(machines) == 0 {
		t.Skipf("NOT RUN: $MYRIG_MACHINES not visible (zsh assoc array, not exported). Run:\n" +
			"  MYRIG_MACHINES=\"$MYRIG_MACHINES\" go test ./internal/conformance -run TestRealCrossHostHTTP -v")
	}
	partner, found := machineByName(machines, partnerName)
	if !found {
		t.Skipf("WARNING NOT RUN: partner %q not in $MYRIG_MACHINES", partnerName)
	}
	bin := seshBin(t)
	rhomeBase, rbin, err := crossHostPreflight(partner.dest, partner.port)
	if err != nil {
		t.Skipf("WARNING NOT RUN: partner %q unusable: %v\n  (install v2 sesh at ~/.local/bin/sesh-v2 on %s — see AGENTS.md)", partnerName, err, partnerName)
	}
	// Stale-binary guard: the partner sesh-v2 must know the http transport flags.
	if help, _ := crossHostSSH(partner.dest, partner.port, rbin+" peer add --help 2>&1"); !strings.Contains(help, "api-addr") {
		t.Skipf("WARNING NOT RUN: partner sesh-v2 is STALE (no --api-addr) — rebuild & reinstall ~/.local/bin/sesh-v2 on %s (schema changed this session; see AGENTS.md)", partnerName)
	}

	// The partner's API binds its own hostname (resolves to its tailscale IP on the
	// partner, and is reachable from self over tailscale) on a stamp-derived port.
	apiHost := partner.dest
	if _, h, ok := strings.Cut(partner.dest, "@"); ok && h != "" {
		apiHost = h
	}
	stamp := time.Now().UnixNano()
	apiAddr := fmt.Sprintf("%s:%d", apiHost, 20000+stamp%10000)
	token := fmt.Sprintf("xhost-http-token-%d", stamp)

	rhome := fmt.Sprintf("%s/sesh-xhttp-%d", rhomeBase, stamp)
	rcodex := fmt.Sprintf("%s/sesh-xhttp-codex-%d", rhomeBase, stamp)
	rsock := fmt.Sprintf("sesh-xhttp-%d", stamp)
	rmaster := fmt.Sprintf("sesh-xhttp-master-%d", stamp)
	remoteEnv := strings.Join([]string{
		"env",
		"SESH_HOME=" + rhome,
		"SESH_MACHINE=" + partnerName,
		"SESH_TMUX_SOCKET=" + rsock,
		"SESH_MASTER_SOCKET=" + rmaster,
		"SESH_CODEX_HOME=" + rcodex,
		"SESH_API_ADDR=" + apiAddr,
		"SESH_API_TOKEN=" + token,
		rbin,
	}, " ")

	// Start the partner's daemon with its TCP API exposed over the real network.
	setup := fmt.Sprintf("mkdir -p %s && ln -sf $HOME/.codex/auth.json %s/auth.json; %s daemon start", rcodex, rcodex, remoteEnv)
	if out, err := crossHostSSH(partner.dest, partner.port, setup); err != nil {
		t.Fatalf("start remote API daemon on %s: %v\n%s", partnerName, err, out)
	}
	t.Cleanup(func() {
		crossHostSSH(partner.dest, partner.port, fmt.Sprintf("%s daemon stop >/dev/null 2>&1; tmux -L %s kill-server 2>/dev/null; rm -rf %s %s", remoteEnv, rsock, rhome, rcodex)) //nolint:errcheck
	})

	// A local daemon on self (isolated) — it runs the mesh sync + fan-out we assert.
	localHome := t.TempDir()
	localEnv := map[string]string{
		"SESH_HOME":          localHome,
		"SESH_MACHINE":       self,
		"SESH_TMUX_SOCKET":   fmt.Sprintf("sesh-xhttp-local-%d", stamp),
		"SESH_MASTER_SOCKET": fmt.Sprintf("sesh-xhttp-localmaster-%d", stamp),
		"SESH_CODEX_HOME":    setupCodexHome(t),
	}
	runLocal := func(args ...string) (string, string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = sandboxEnv(localEnv)
		return runCmd(cmd)
	}
	if _, stderr, err := runLocal("daemon", "start"); err != nil {
		t.Fatalf("start local daemon: %v\n%s", err, stderr)
	}
	t.Cleanup(func() {
		runLocal("daemon", "stop")                                                      //nolint:errcheck
		exec.Command("tmux", "-L", localEnv["SESH_TMUX_SOCKET"], "kill-server").Run()   //nolint:errcheck
		exec.Command("tmux", "-L", localEnv["SESH_MASTER_SOCKET"], "kill-server").Run() //nolint:errcheck
	})

	// Register the partner as an HTTP peer with a BROKEN ssh dest — so a silent ssh
	// attempt on ANY cross-machine path would fail. A green run proves HTTP carried it.
	if _, stderr, err := runLocal("peer", "add", "--machine", partnerName, "--ssh", "http-only.invalid", "--home", rhome, "--binary", rbin, "--tmux-socket", rsock, "--api-addr", apiAddr, "--api-token", token); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}

	// 1. ROUTING over real-network http: spawn a headless thread ON the partner.
	stdout, stderr, err := runLocal("thread", "new", "--machine", partnerName, "--agent", "pi", "--name", "xh-http", "--cwd", "/tmp", "--headless")
	if err != nil {
		t.Fatalf("routed http spawn on %s: %v\n%s", partnerName, err, stderr)
	}
	id := strings.TrimSpace(stdout)
	t.Cleanup(func() {
		runLocal("thread", "delete", "--id", id, "--machine", partnerName, "--force") //nolint:errcheck
	})

	// 2. routed status over http resolves it ALIVE on the partner.
	routedActivity := func() api.Activity {
		out, _, err := runLocal("thread", "status", "--id", id, "--machine", partnerName, "--json")
		if err != nil {
			return "ERR"
		}
		var st api.ThreadStatusResponse
		if json.Unmarshal([]byte(strings.TrimSpace(out)), &st) != nil {
			return "ERR"
		}
		return st.Activity
	}
	if !waitUntil(45*time.Second, func() bool { return routedActivity() == api.ActivityWaiting }) {
		t.Fatalf("routed http status never saw the thread alive on %s (activity=%s)", partnerName, routedActivity())
	}

	c := client.New(localHome + "/daemon.sock")

	// 3. live FAN-OUT over real-network http: the local daemon's `?all-machines`
	//    aggregation includes the partner's thread.
	if !waitUntil(20*time.Second, func() bool {
		resp, err := c.ThreadList(context.Background(), false, true)
		return err == nil && threadInResp(resp.Threads, id)
	}) {
		t.Errorf("fan-out over real-network http never included the partner's thread")
	}

	// 4. background SYNC over real-network http: the merged /v1/mesh replicates it,
	//    attributed to the partner and marked reachable.
	if !waitUntil(20*time.Second, func() bool {
		mesh, err := c.Mesh(context.Background())
		if err != nil {
			return false
		}
		for _, mv := range mesh.Machines {
			if mv.Machine == partnerName && mv.Reachable {
				for _, th := range mv.Threads {
					if th.ID == id {
						return true
					}
				}
			}
		}
		return false
	}) {
		t.Errorf("mesh sync over real-network http never replicated the partner's thread (reachable)")
	}

	// 5. mutation round-trip over http: routed delete removes it from the fan-out.
	if _, stderr, err := runLocal("thread", "delete", "--id", id, "--machine", partnerName, "--force"); err != nil {
		t.Fatalf("routed http delete: %v\n%s", err, stderr)
	}
	if !waitUntil(15*time.Second, func() bool {
		resp, err := c.ThreadList(context.Background(), false, true)
		return err == nil && !threadInResp(resp.Threads, id)
	}) {
		t.Errorf("partner thread still in the fan-out after routed http delete")
	}
}

// crossHostPartner is the fixed pairing: each machine's cross-host target.
var crossHostPartner = map[string]string{"mymain": "macbook", "macbook": "mymain"}

type myrigMachineSpec struct {
	name string // the machine name (host part of user@host:port)
	dest string // ssh destination user@host
	port string
}

// myrigMachines parses $MYRIG_MACHINES ("user@host:port user@host:port ...").
func myrigMachines() []myrigMachineSpec {
	var out []myrigMachineSpec
	for _, tok := range strings.Fields(os.Getenv("MYRIG_MACHINES")) {
		userHost, port, _ := strings.Cut(tok, ":")
		_, host, ok := strings.Cut(userHost, "@")
		if !ok || host == "" {
			continue
		}
		if port == "" {
			port = "22"
		}
		out = append(out, myrigMachineSpec{name: host, dest: userHost, port: port})
	}
	return out
}

// crossHostSelf returns this machine's name if it is one of the cross-host pair,
// detected via myrig's "the local machine's name is its own active target"
// convention ($MYRIG_TARGETS — which is exported, unlike $MYRIG_MACHINES).
func crossHostSelf() string {
	targets := map[string]bool{}
	for _, t := range strings.Split(os.Getenv("MYRIG_TARGETS"), ",") {
		targets[strings.TrimSpace(t)] = true
	}
	for name := range crossHostPartner {
		if targets[name] {
			return name
		}
	}
	return ""
}

func machineByName(machines []myrigMachineSpec, name string) (myrigMachineSpec, bool) {
	for _, m := range machines {
		if m.name == name {
			return m, true
		}
	}
	return myrigMachineSpec{}, false
}

// crossHostPreflight checks the partner is reachable and has v2 sesh, returning
// (partner $HOME, absolute sesh-v2 path).
func crossHostPreflight(dest, port string) (home, bin string, err error) {
	out, err := crossHostSSH(dest, port, `printf '%s\n' "$HOME"; test -x "$HOME/.local/bin/sesh-v2" && echo HAVE_V2 || echo NO_V2`)
	if err != nil {
		return "", "", fmt.Errorf("ssh failed: %v (%s)", err, strings.TrimSpace(out))
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 || lines[len(lines)-1] != "HAVE_V2" {
		return "", "", fmt.Errorf("sesh-v2 not found at ~/.local/bin/sesh-v2")
	}
	home = strings.TrimSpace(lines[0])
	return home, home + "/.local/bin/sesh-v2", nil
}

func crossHostSSH(dest, port, remoteCmd string) (string, error) {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=no"}
	if port != "" && port != "22" {
		args = append(args, "-p", port)
	}
	args = append(args, dest, remoteCmd)
	out, err := exec.Command("ssh", args...).CombinedOutput()
	return string(out), err
}
