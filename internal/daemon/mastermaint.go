package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/peers"
)

// The master self-heal loop converges the cockpit to "one window per CONNECTED
// machine" (Lukas's invariant): while a master server is up, self plus every
// mesh-reachable registered peer gets a window — a window lost to prefix+K (or a
// crash) comes back within a tick, and a machine that becomes reachable gains its
// window automatically. The loop only ADDS missing windows: it never removes one
// (an existing window for a now-unreachable machine keeps its supervisor's retry
// loop), never touches existing windows, and does nothing at all when no master
// session exists — `master down` stays down, sesh never builds a cockpit on its
// own. Manual convergence stays available as `sesh master ensure` (prefix+R).
const masterMaintTick = 5 * time.Second

type masterMaint struct {
	d       *Daemon
	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
}

func newMasterMaint(d *Daemon) *masterMaint {
	return &masterMaint{d: d, stop: make(chan struct{}), done: make(chan struct{})}
}

func (mm *masterMaint) start() {
	mm.mu.Lock()
	mm.started = true
	mm.mu.Unlock()
	go mm.run()
}

func (mm *masterMaint) stopAndWait() {
	mm.mu.Lock()
	started := mm.started
	mm.mu.Unlock()
	if !started {
		return
	}
	close(mm.stop)
	<-mm.done
}

func (mm *masterMaint) run() {
	defer close(mm.done)
	t := time.NewTicker(masterMaintTick)
	defer t.Stop()
	for {
		select {
		case <-mm.stop:
			return
		case <-t.C:
			mm.tick()
		}
	}
}

// mtmux runs a tmux command on the master socket with $TMUX scrubbed.
func (mm *masterMaint) mtmux(args ...string) (string, error) {
	full := append([]string{"-L", mm.d.cfg.MasterSocket}, args...)
	cmd := exec.Command("tmux", full...)
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TMUX=") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (mm *masterMaint) tick() {
	// No master session => nothing to converge (and never resurrect a downed one).
	if _, err := mm.mtmux("has-session", "-t", "=master"); err != nil {
		return
	}

	// Desired = self + every REGISTERED peer the mesh currently sees as reachable.
	// A peer with no snapshot row yet (just added, first sync pending) is not
	// desired until it proves reachable; a row with reachable=0 (offline, or a
	// dead-end registration) never forces a window.
	desired := []string{mm.d.cfg.Machine}
	reg, err := peers.Load(mm.d.cfg.PeersPath())
	if err != nil {
		log.Printf("master selfheal: load peers: %v", err)
		return
	}
	registered := map[string]bool{}
	for _, p := range reg.List() {
		registered[p.Machine] = true
	}
	rows, err := mm.d.store.LoadPeerSnapshots()
	if err != nil {
		log.Printf("master selfheal: load peer snapshots: %v", err)
		return
	}
	for _, r := range rows {
		if r.Reachable && registered[r.Machine] && r.Machine != mm.d.cfg.Machine {
			desired = append(desired, r.Machine)
		}
	}

	out, err := mm.mtmux("list-windows", "-t", "master", "-F", "#{window_name}")
	if err != nil {
		return // master vanished between has-session and here — next tick re-checks
	}
	existing := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			existing[l] = true
		}
	}

	self, err := os.Executable()
	if err != nil {
		log.Printf("master selfheal: executable: %v", err)
		return
	}
	for _, m := range desired {
		if existing[m] {
			continue
		}
		winCmd := fmt.Sprintf("%s master window %s", shellQuoteD(self), shellQuoteD(m))
		if out, err := mm.mtmux("new-window", "-d", "-t", "master:", "-n", m, winCmd); err != nil {
			log.Printf("master selfheal: new-window %s: %v: %s", m, err, out)
			continue
		}
		log.Printf("master selfheal: recreated window %q", m)
	}
}

// shellQuoteD single-quotes a string for /bin/sh (daemon-side copy of cmd/sesh's
// shellQuote — the window command runs through tmux's default-shell).
func shellQuoteD(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
