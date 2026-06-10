package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	matrix.RegisterTest("tmux.nav-in-client-multi", matrix.AgentAgnostic, matrix.Local, testNavInClientMulti)
}

// testNavInClientMulti: when MULTIPLE clients are attached to the same work-socket
// session (the real case: a master window AND a direct attach), `nav --in-client` must
// switch the client the caller IDENTIFIES — via --client or the $SESH_NAV_CLIENT the
// popup keybindings bake in — and never an ambient pick: tmux cannot map a subprocess,
// popup pty, or pane pty back to a client, so any ambient "current client" is
// arbitrary (observed live moving a master window's attach instead of the invoker).
// Directions: a carrier-less call in the SHARED session fails loudly moving NOBODY;
// --client and $SESH_NAV_CLIENT each move exactly client B while A stays put.
func testNavInClientMulti(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)
	target := sb.createSession(t, "mctgt")
	if _, err := sb.rawTmux(t, "new-session", "-d", "-s", "mcscr", "-c", "/tmp"); err != nil {
		t.Fatalf("scratch session: %v", err)
	}

	dir := t.TempDir()
	octl := func(sock string, args ...string) string {
		c := exec.Command("tmux", append([]string{"-S", sock}, args...)...)
		c.Env = sandboxEnv(nil) // no inherited SESH_* into the outer client
		out, _ := c.Output()    //nolint:errcheck
		return strings.TrimSpace(string(out))
	}
	startClient := func(sock string) {
		octl(sock, "-f", "/dev/null", "new-session", "-d", "-s", "o", "-x", "120", "-y", "40")
		octl(sock, "send-keys", "-t", "o:0", "-l", "env -u TMUX tmux -L "+sb.TmuxSocket+" attach -t mcscr")
		octl(sock, "send-keys", "-t", "o:0", "Enter")
	}
	aSock, bSock := filepath.Join(dir, "a.sock"), filepath.Join(dir, "b.sock")
	startClient(aSock)
	startClient(bSock)
	t.Cleanup(func() { octl(aSock, "kill-server"); octl(bSock, "kill-server") })

	if !waitUntil(10*time.Second, func() bool { return workClientTotal(t, sb) == 2 }) {
		t.Fatalf("expected 2 work-socket clients, got %v", workClientSessions(t, sb))
	}

	// B's client name, DETERMINISTICALLY: the inner tmux client runs on B's outer
	// pane's pty, so that pane_tty IS the inner #{client_name}.
	bClient := octl(bSock, "display-message", "-p", "-t", "o:0", "#{pane_tty}")
	if bClient == "" {
		t.Fatalf("could not capture client B's tty")
	}
	if _, ok := clientSessionsByName(t, sb)[bClient]; !ok {
		t.Fatalf("captured B tty %q is not an attached client: %v", bClient, clientSessionsByName(t, sb))
	}

	// (1) Carrier-less nav typed INSIDE the shared session: two clients watch this
	// pane — "which one pressed Enter" is unknowable there, so nav must FAIL LOUDLY
	// and move nobody (any guess could move the other user's view).
	before := clientSessionsByName(t, sb)
	env := fmt.Sprintf("SESH_HOME=%s SESH_MACHINE=%s SESH_TMUX_SOCKET=%s SESH_MASTER_SOCKET=%s SESH_NAV_CLIENT=",
		sb.Home, sb.Machine, sb.TmuxSocket, sb.MasterSocket)
	rcFile := filepath.Join(dir, "rc")
	octl(bSock, "send-keys", "-t", "o:0", "-l",
		fmt.Sprintf("%s %s tmux nav --to %s:%s --in-client; echo rc=$? > %s", env, seshBin(t), sb.Machine, target, rcFile))
	octl(bSock, "send-keys", "-t", "o:0", "Enter")
	if !waitUntil(10*time.Second, func() bool {
		b, _ := os.ReadFile(rcFile)
		return strings.TrimSpace(string(b)) != ""
	}) {
		t.Fatalf("carrier-less typed nav never finished")
	}
	if rc, _ := os.ReadFile(rcFile); strings.TrimSpace(string(rc)) == "rc=0" {
		t.Errorf("carrier-less nav in a SHARED session succeeded — must be a loud error")
	}
	if got := clientSessionsByName(t, sb); !mapsEqualStr(got, before) {
		t.Errorf("carrier-less nav in a shared session MOVED a client: before=%v after=%v", before, got)
	}

	// CARRIER-LESS directions (how a misconfigured caller would invoke nav — no
	// --client, no $SESH_NAV_CLIENT, no usable $TMUX_PANE; with MULTIPLE clients
	// this is ambiguous):
	// (1) it must FAIL LOUDLY and move nobody — any ambient pick is arbitrary
	//     (observed live switching a master window's attach instead of the invoker);
	// (2) with an explicit --client it switches exactly that client.
	navEnv := func(extra ...string) []string {
		base := sandboxEnv(map[string]string{
			"SESH_HOME": sb.Home, "SESH_MACHINE": sb.Machine,
			"SESH_TMUX_SOCKET": sb.TmuxSocket, "SESH_MASTER_SOCKET": sb.MasterSocket,
		})
		clean := base[:0:0]
		for _, kv := range base {
			if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") || strings.HasPrefix(kv, "SESH_NAV_CLIENT=") {
				continue
			}
			clean = append(clean, kv)
		}
		clean = append(clean, "TMUX=/tmp/tmux-fake/"+sb.TmuxSocket+",1,1")
		return append(clean, extra...)
	}
	target2 := sb.createSession(t, "mctgt2")
	before = clientSessionsByName(t, sb)
	navArgs := []string{"tmux", "nav", "--to", sb.Machine + ":" + target2, "--in-client"}
	cmd := exec.Command(seshBin(t), navArgs...)
	cmd.Env = navEnv()
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("carrier-less nav --in-client did not fail; output: %s", out)
	} else if !strings.Contains(string(out), "--client") {
		t.Errorf("carrier-less nav error does not point at --client: %s", out)
	}
	if got := clientSessionsByName(t, sb); !mapsEqualStr(got, before) {
		t.Errorf("carrier-less nav MOVED a client: before=%v after=%v", before, got)
	}

	cmd = exec.Command(seshBin(t), append(navArgs, "--client", bClient)...)
	cmd.Env = navEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nav --in-client --client %s: %v: %s", bClient, err, out)
	}
	if !waitUntil(10*time.Second, func() bool {
		cs := clientSessionsByName(t, sb)
		other := ""
		for name, sess := range cs {
			if name != bClient {
				other = sess
			}
		}
		return cs[bClient] == target2 && other == "mcscr"
	}) {
		t.Errorf("ttyless nav --client: want B(%s)->%q + other->mcscr, got %v", bClient, target2, clientSessionsByName(t, sb))
	}

	// (3) the $SESH_NAV_CLIENT carrier (what the popup keybindings bake in via
	//     run-shell's #{client_name} expansion) — switches exactly that client.
	target3 := sb.createSession(t, "mctgt3")
	cmd = exec.Command(seshBin(t), "tmux", "nav", "--to", sb.Machine+":"+target3, "--in-client")
	cmd.Env = navEnv("SESH_NAV_CLIENT=" + bClient)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nav --in-client with SESH_NAV_CLIENT=%s: %v: %s", bClient, err, out)
	}
	if !waitUntil(10*time.Second, func() bool {
		cs := clientSessionsByName(t, sb)
		other := ""
		for name, sess := range cs {
			if name != bClient {
				other = sess
			}
		}
		return cs[bClient] == target3 && other == "mcscr"
	}) {
		t.Errorf("SESH_NAV_CLIENT nav: want B(%s)->%q + other->mcscr, got %v", bClient, target3, clientSessionsByName(t, sb))
	}
}

func mapsEqualStr(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func workClientTotal(t *testing.T, sb *Sandbox) int {
	n := 0
	for _, c := range workClientSessions(t, sb) {
		n += c
	}
	return n
}

func workClientSessions(t *testing.T, sb *Sandbox) map[string]int {
	out, _ := sb.rawTmux(t, "list-clients", "-F", "#{client_session}")
	m := map[string]int{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m[l]++
		}
	}
	return m
}

func clientSessionsByName(t *testing.T, sb *Sandbox) map[string]string {
	out, _ := sb.rawTmux(t, "list-clients", "-F", "#{client_name}\t#{client_session}")
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.SplitN(strings.TrimSpace(l), "\t", 2); len(f) == 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}
