package conformance

import (
	"encoding/json"
	"fmt"
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
	matrix.RegisterTest("tmux.nav-cycle-flagged", matrix.AgentAgnostic, matrix.Remote, testNavCycleFlagged)
}

// cyclePlanJSON mirrors the --dry-run output fields the cell asserts on.
type cyclePlanJSON struct {
	Outcome string `json:"outcome"`
	Ring    []struct {
		ID        string `json:"id"`
		Enterable bool   `json:"enterable"`
	} `json:"ring"`
	Current string `json:"current"`
	Target  *struct {
		ID string `json:"id"`
	} `json:"target"`
	From *struct {
		Machine string `json:"machine"`
		Session string `json:"session"`
	} `json:"from"`
}

// testNavCycleFlagged proves `sesh tmux nav --cycle-flagged next|prev` (issue #11) over a
// REAL cockpit: a self daemon + an ssh-localhost peer (a real ssh hop), a real master
// with a REAL client attached to it (the carrier the keybinding names in
// $SESH_NAV_CLIENT), and a DECOY client on the peer's work server that switches LAST
// before every resolve, so an ambiently-resolved "where am I" would be observably wrong
// (the H98 follow-up 2 discipline).
//
// It asserts the observable effect of each press — what the master window's marker
// client actually shows afterwards, read back via master-current — and that the press
// resolved "where am I" EXACTLY ONCE. The count is measured, not assumed: the peer is
// registered with a wrapper binary that logs every `tmux master-current` it serves, and
// both the old path (nav's own resolveMasterLocation) and the new one reach the peer
// through that binary. A plain `nav --to` is counted too, as the control that proves the
// counter sees the old path at all.
func testNavCycleFlagged(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ensureSSHLocalhost(t)
	self := newSandbox(t, matrix.Local)
	self.startDaemon(t)
	peer := newSandbox(t, matrix.Local)
	peer.startDaemon(t)

	// The peer's binary is a wrapper that counts master-current resolves, then execs sesh.
	dir := t.TempDir()
	countLog := filepath.Join(dir, "master-current.log")
	wrapper := filepath.Join(dir, "sesh-counting")
	script := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *\" master-current \"*) echo x >> %q;; esac\nexec %q \"$@\"\n", countLog, seshBin(t))
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := self.Runner.Run(t, "peer", "add", "--machine", peer.Machine, "--ssh", "localhost",
		"--home", peer.Home, "--binary", wrapper, "--tmux-socket", peer.TmuxSocket); err != nil {
		t.Fatalf("peer add: %v\n%s", err, stderr)
	}
	resolves := func() int {
		b, err := os.ReadFile(countLog)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "x")
	}

	// Threads. self: one UNflagged. peer: two flagged live, one flagged HEADLESS (dead —
	// in the ring, never landed on), one unflagged (the decoy's parking spot).
	self.newThread(t, "pi", "selfw", "/tmp")
	park := peer.newThread(t, "pi", "cyc-park", "/tmp")
	ringA := peer.newThread(t, "pi", "cyc-a", "/tmp")
	ringB := peer.newThread(t, "pi", "cyc-b", "/tmp")
	for _, th := range []string{park.ID, ringA.ID, ringB.ID} {
		peer.waitThreadReady(t, th, "pi")
	}
	dead := peer.newHeadlessThread(t, "pi", "cyc-dead")
	for _, id := range []string{ringA.ID, ringB.ID, dead.ID} {
		if _, stderr, err := peer.Runner.Run(t, "thread", "flag", "--on", "--id", id); err != nil {
			t.Fatalf("flag %s: %v\n%s", id, err, stderr)
		}
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

	// The CARRIER: a real client attached to the master server, from an outer tmux.
	outerSock := filepath.Join(dir, "outer.sock")
	octl := func(args ...string) {
		c := exec.Command("tmux", append([]string{"-S", outerSock}, args...)...)
		c.Env = sandboxEnv(nil)
		c.Run() //nolint:errcheck
	}
	octl("-f", "/dev/null", "new-session", "-d", "-s", "o", "-x", "160", "-y", "45")
	octl("send-keys", "-t", "o:0", "-l", "env -u TMUX tmux -L "+self.MasterSocket+" attach -t "+masterSessionName)
	octl("send-keys", "-t", "o:0", "Enter")
	t.Cleanup(func() { octl("kill-server") })
	var carrier string
	if !waitUntil(15*time.Second, func() bool {
		out, err := exec.Command("tmux", "-L", self.MasterSocket, "list-clients", "-F", "#{client_name}").Output()
		carrier = strings.TrimSpace(string(out))
		return err == nil && carrier != "" && !strings.Contains(carrier, "\n")
	}) {
		t.Fatalf("no single real client ever attached to the master server (got %q)", carrier)
	}
	withCarrier := map[string]string{"SESH_NAV_CLIENT": carrier}

	// The DECOY: a second real client on the peer's work server, parked on cyc-park.
	peer.attachViewer(t, park.SessionName)
	decoyClient, _ := peer.workClientOn(t, park.SessionName)
	bumpDecoy := func() {
		if out, err := peer.rawTmux(t, "switch-client", "-c", decoyClient, "-t", park.SessionName); err != nil {
			t.Fatalf("bump decoy client: %v\n%s", err, out)
		}
	}
	shows := func() string {
		out, _, _ := self.Runner.Run(t, "tmux", "master-current", "--machine", peer.Machine, "--origin", self.Machine)
		return strings.TrimSpace(out)
	}
	dryRun := func(dir string) cyclePlanJSON {
		out, stderr, err := runWithEnv(t, self, withCarrier, "tmux", "nav", "--cycle-flagged", dir, "--dry-run")
		if err != nil {
			t.Fatalf("dry-run %s: %v\n%s", dir, err, stderr)
		}
		var p cyclePlanJSON
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			t.Fatalf("dry-run json: %v\n%s", err, out)
		}
		return p
	}

	// Wait until self's mesh carries all three flags (the ring is read from the LOCAL
	// mesh cache, which syncs the peer asynchronously).
	if !waitUntil(30*time.Second, func() bool { return len(dryRun("next").Ring) == 3 }) {
		t.Fatalf("self's mesh never showed the 3 flagged peer threads; plan = %+v", dryRun("next"))
	}
	if p := dryRun("next"); p.Ring[0].ID != ringA.ID || p.Ring[1].ID != ringB.ID || p.Ring[2].ID != dead.ID || p.Ring[2].Enterable {
		t.Fatalf("ring order/enterability wrong: %+v (want cyc-a, cyc-b, cyc-dead[dead])", p.Ring)
	}

	// CONTROL: a plain nav with the carrier costs ONE resolve (resolveMasterLocation) —
	// proof the counter sees the old path.
	before := resolves()
	if _, stderr, err := runWithEnv(t, self, withCarrier, "tmux", "nav", "--to", peer.Machine+":"+ringA.SessionName, "--thread", ringA.ID); err != nil {
		t.Fatalf("nav to cyc-a: %v\n%s", err, stderr)
	}
	if d := resolves() - before; d != 1 {
		t.Fatalf("control: plain nav resolved the location %d times, want 1 — the counter is not measuring what this cell needs", d)
	}
	bumpDecoy()
	if !waitUntil(10*time.Second, func() bool { return shows() == ringA.ID }) {
		t.Fatalf("setup nav never landed on cyc-a; shows %q", shows())
	}

	// DRY-RUN: current is what the WINDOW shows (not the decoy's thread), target is next.
	bumpDecoy()
	p := dryRun("next")
	if p.Current != ringA.ID {
		if p.Current == park.ID {
			t.Fatalf("dry-run current = the DECOY's thread %s — 'where am I' is not scoped to the master window's client", park.ID)
		}
		t.Fatalf("dry-run current = %q, want cyc-a %s", p.Current, ringA.ID)
	}
	if p.Target == nil || p.Target.ID != ringB.ID || p.From == nil || p.From.Session != ringA.SessionName || p.From.Machine != peer.Machine {
		t.Fatalf("dry-run plan wrong: target %+v from %+v (want target cyc-b, from %s:%s)", p.Target, p.From, peer.Machine, ringA.SessionName)
	}

	// Each REAL press: exactly one resolve, lands where the ring says, records prefix+L's
	// from-location = where it was. next: a → b; next: b → a (wraps, dead stepped over);
	// prev: a → b (wraps backwards).
	steps := []struct {
		dir        string
		from, want string
		fromSess   string
	}{
		{"next", ringA.ID, ringB.ID, ringA.SessionName},
		{"next", ringB.ID, ringA.ID, ringB.SessionName},
		{"prev", ringA.ID, ringB.ID, ringA.SessionName},
	}
	for i, s := range steps {
		bumpDecoy() // another master moves last — an ambient resolve would now read the decoy
		before := resolves()
		start := time.Now()
		if _, stderr, err := runWithEnv(t, self, withCarrier, "tmux", "nav", "--cycle-flagged", s.dir); err != nil {
			t.Fatalf("step %d (%s): %v\n%s", i, s.dir, err, stderr)
		}
		elapsed := time.Since(start)
		// Read BEFORE shows() below — it is a master-current too, and the wrapper counts it.
		pressResolves := resolves() - before
		if pressResolves != 1 {
			t.Errorf("step %d (%s): resolved 'where am I' %d times, want exactly 1 (the ring start point and prefix+L's from-location must share one resolve)", i, s.dir, pressResolves)
		}
		if !waitUntil(10*time.Second, func() bool { return shows() == s.want }) {
			t.Fatalf("step %d (%s from %s): the window shows %q, want %s", i, s.dir, s.from, shows(), s.want)
		}
		b, err := os.ReadFile(filepath.Join(self.Home, "nav-prev"))
		if err != nil {
			t.Fatalf("step %d: nav-prev not written: %v", i, err)
		}
		if f := strings.Split(strings.TrimSpace(string(b)), "\t"); len(f) != 3 || f[0] != peer.Machine || f[1] != s.fromSess {
			t.Errorf("step %d: prefix+L from-location = %q, want %s\\t%s\\t<window>", i, strings.TrimSpace(string(b)), peer.Machine, s.fromSess)
		}
		t.Logf("step %d %s: %v, %d resolve(s)", i, s.dir, elapsed.Round(time.Millisecond), pressResolves)
	}

	// The two EMPTY rings are distinct, loud refusals. All flags off but the dead one:
	// "all dead". Then that one off too: "no flagged".
	for _, id := range []string{ringA.ID, ringB.ID} {
		if _, stderr, err := peer.Runner.Run(t, "thread", "flag", "--off", "--id", id); err != nil {
			t.Fatalf("unflag %s: %v\n%s", id, err, stderr)
		}
	}
	if !waitUntil(30*time.Second, func() bool { return dryRun("next").Outcome == "all-dead" }) {
		t.Fatalf("dry-run outcome never became all-dead: %+v", dryRun("next"))
	}
	if _, stderr, err := runWithEnv(t, self, withCarrier, "tmux", "nav", "--cycle-flagged", "next"); err == nil || !strings.Contains(stderr, "flagged threads are all dead") {
		t.Errorf("all-dead ring: err %v, stderr %q — want a loud 'flagged threads are all dead'", err, stderr)
	}
	if _, stderr, err := peer.Runner.Run(t, "thread", "flag", "--off", "--id", dead.ID); err != nil {
		t.Fatalf("unflag dead: %v\n%s", err, stderr)
	}
	if !waitUntil(30*time.Second, func() bool { return dryRun("next").Outcome == "no-flagged" }) {
		t.Fatalf("dry-run outcome never became no-flagged: %+v", dryRun("next"))
	}
	if _, stderr, err := runWithEnv(t, self, withCarrier, "tmux", "nav", "--cycle-flagged", "next"); err == nil || !strings.Contains(stderr, "no flagged active threads") {
		t.Errorf("empty ring: err %v, stderr %q — want a loud 'no flagged active threads'", err, stderr)
	}
	if shows() != ringB.ID {
		t.Errorf("a refused cycle MOVED the window: shows %q, want it left on cyc-b", shows())
	}
}
