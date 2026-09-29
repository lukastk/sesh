package conformance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukastk/sesh/internal/matrix"
	"github.com/lukastk/sesh/internal/tmux"
)

func init() {
	matrix.RegisterTest("tmux.nav-last", matrix.AgentAgnostic, matrix.Remote, testNavLast)
}

// testNavLast proves prefix+L's history (#14): a cockpit-carried `sesh tmux nav --to`
// records the location the CARRIER's master window was showing, and `nav --last` returns
// there — even when ANOTHER client on the same master server moved last.
//
// Why the decoy has to be where it is: every client in the one `master` session shares its
// current window, so a decoy there cannot make an ambient read wrong. The bug (`display-
// message -c` expanding against tmux's ambient client, H98 follow-up 2) needs a client in a
// SECOND session on the master server, switch-client'd LAST. Its window is named after the
// SELF machine, so an ambient read answers with a real machine and records a plausible,
// wrong location rather than failing harmlessly.
func testNavLast(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	self := newSandbox(t, matrix.Local)
	self.startDaemon(t)
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)
	if _, stderr, err := self.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost",
		"--home", peer.Home, "--binary", seshBin(t), "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}

	self.newThread(t, "pi", "last-self", "/tmp")
	a := peer.newThread(t, "pi", "last-a", "/tmp")
	b := peer.newThread(t, "pi", "last-b", "/tmp")
	for _, id := range []string{a.ID, b.ID} {
		peer.waitThreadReady(t, id, "pi")
	}

	if _, stderr, err := self.Runner.Run(t, "master", "up", "--machines", self.Machine+","+peer.Machine); err != nil {
		t.Fatalf("master up: %v\n%s", err, stderr)
	}
	t.Cleanup(func() { self.Runner.Run(t, "master", "down") }) //nolint:errcheck
	marker := tmux.MasterClientMarker(peer.Home, self.Machine)
	if !waitUntil(20*time.Second, func() bool {
		b, err := os.ReadFile(marker)
		return err == nil && len(strings.Fields(string(b))) == 2
	}) {
		t.Fatalf("the peer window's attach never recorded a marker at %s", marker)
	}

	mtx := func(args ...string) (string, error) {
		c := exec.Command("tmux", append([]string{"-L", self.MasterSocket}, args...)...)
		c.Env = sandboxEnv(nil)
		out, err := c.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	// The decoy's session on the MASTER server, its window named after the self machine.
	if out, err := mtx("new-session", "-d", "-s", "decoy", "-n", self.Machine, "exec sleep 100000"); err != nil {
		t.Fatalf("decoy session: %v\n%s", err, out)
	}

	// Two REAL clients from an outer tmux: the CARRIER on `master`, the DECOY on `decoy`.
	dir := t.TempDir()
	outerSock := filepath.Join(dir, "outer.sock")
	octl := func(args ...string) {
		c := exec.Command("tmux", append([]string{"-S", outerSock}, args...)...)
		c.Env = sandboxEnv(nil)
		c.Run() //nolint:errcheck
	}
	octl("-f", "/dev/null", "new-session", "-d", "-s", "o1", "-x", "160", "-y", "45")
	octl("send-keys", "-t", "o1:0", "-l", "env -u TMUX tmux -L "+self.MasterSocket+" attach -t "+masterSessionName)
	octl("send-keys", "-t", "o1:0", "Enter")
	octl("new-session", "-d", "-s", "o2", "-x", "160", "-y", "45")
	octl("send-keys", "-t", "o2:0", "-l", "env -u TMUX tmux -L "+self.MasterSocket+" attach -t decoy")
	octl("send-keys", "-t", "o2:0", "Enter")
	t.Cleanup(func() { octl("kill-server") })
	var carrier, decoy string
	if !waitUntil(15*time.Second, func() bool {
		out, err := mtx("list-clients", "-F", "#{client_name}\t#{client_session}")
		if err != nil {
			return false
		}
		carrier, decoy = "", ""
		for _, ln := range strings.Split(out, "\n") {
			name, sess, _ := strings.Cut(ln, "\t")
			switch sess {
			case masterSessionName:
				carrier = name
			case "decoy":
				decoy = name
			}
		}
		return carrier != "" && decoy != ""
	}) {
		t.Fatalf("expected one real client on %q and one on \"decoy\"", masterSessionName)
	}
	withCarrier := map[string]string{"SESH_NAV_CLIENT": carrier}
	bumpDecoy := func() {
		if out, err := mtx("switch-client", "-c", decoy, "-t", "decoy"); err != nil {
			t.Fatalf("bump decoy: %v\n%s", err, out)
		}
	}
	shows := func() string {
		out, _, _ := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		return strings.TrimSpace(out)
	}
	navPrev := func() []string {
		b, err := os.ReadFile(filepath.Join(self.Home, "nav-prev"))
		if err != nil {
			t.Fatalf("nav-prev not written: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\t")
	}

	// Put the carrier's window on the peer, showing A.
	if _, stderr, err := runWithEnv(t, self, withCarrier, "tmux", "nav", "--to", peer.Machine+":"+a.SessionName, "--thread", a.ID); err != nil {
		t.Fatalf("nav to A: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool { return shows() == a.ID }) {
		t.Fatalf("setup nav never landed on A; shows %q", shows())
	}

	// A → B with the decoy the last mover: the recorded from-location must be peer:A.
	if err := os.Remove(filepath.Join(self.Home, "nav-prev")); err != nil {
		t.Fatalf("clear nav-prev: %v", err)
	}
	bumpDecoy()
	if _, stderr, err := runWithEnv(t, self, withCarrier, "tmux", "nav", "--to", peer.Machine+":"+b.SessionName, "--thread", b.ID); err != nil {
		t.Fatalf("nav to B: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool { return shows() == b.ID }) {
		t.Fatalf("nav never landed on B; shows %q", shows())
	}
	if f := navPrev(); len(f) != 3 || f[0] != peer.Machine || f[1] != a.SessionName {
		if len(f) > 0 && f[0] == self.Machine {
			t.Fatalf("prefix+L from-location = %q: it recorded the DECOY client's window (%s), not the carrier's — the carrier's master window was read ambiently", strings.Join(f, "\t"), self.Machine)
		}
		t.Fatalf("prefix+L from-location = %q, want %s\\t%s\\t<window>", strings.Join(f, "\t"), peer.Machine, a.SessionName)
	}

	// `nav --last` (decoy moving last again) must land back on A and record B.
	bumpDecoy()
	if _, stderr, err := runWithEnv(t, self, withCarrier, "tmux", "nav", "--last"); err != nil {
		t.Fatalf("nav --last: %v\n%s", err, stderr)
	}
	if !waitUntil(10*time.Second, func() bool { return shows() == a.ID }) {
		t.Fatalf("nav --last did not return to A; shows %q", shows())
	}
	if f := navPrev(); len(f) != 3 || f[0] != peer.Machine || f[1] != b.SessionName {
		t.Fatalf("after --last the from-location = %q, want %s\\t%s\\t<window>", strings.Join(f, "\t"), peer.Machine, b.SessionName)
	}
}
