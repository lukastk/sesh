package main

// `sesh tmux nav --cycle-flagged next|prev` — the cockpit's flagged-ring step (master
// prefix+, / prefix+.), issue #11.
//
// This used to be myrig shell: a jq reimplementation of the TUI's order over a 2.4 MB
// `sesh mesh --json`, a routed `master-current` to find the start point, and then
// `sesh tmux nav`, which resolved the SAME location a second time (resolveMasterLocation)
// to record prefix+L's from-location. Measured 2026-09-29 on macbook with the active
// window on mymain: ~130 ms for the ring + start point, then ~170 ms for that duplicate
// resolve, then ~200 ms for the switch. Here the location is resolved ONCE and serves both
// purposes, the ring is built in process by the TUI's own code (tui.FlaggedRing), and the
// resolve runs concurrently with the local mesh read.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lukastk/sesh/internal/api"
	"github.com/lukastk/sesh/internal/config"
	"github.com/lukastk/sesh/internal/tui"
)

// navLoc is one cockpit location: the machine whose master window is active, and the
// session + window that machine's marker client shows (prefix+L's record format).
type navLoc struct {
	Machine string `json:"machine"`
	Session string `json:"session"`
	Window  int    `json:"window"`
}

// cycleEntry is one ring row as --dry-run reports it.
type cycleEntry struct {
	ID        string `json:"id"`
	Machine   string `json:"machine"`
	Name      string `json:"name"`
	Session   string `json:"session_name"`
	Head      string `json:"head"`
	Enterable bool   `json:"enterable"`
}

// cyclePlan is the resolved step: the whole ring (for --dry-run and tests), where the
// cockpit is now, and where it will go.
type cyclePlan struct {
	Schema  int          `json:"schema"`
	Outcome string       `json:"outcome"` // "ok" | "no-flagged" | "all-dead"
	Ring    []cycleEntry `json:"ring"`
	// Current is the thread the active master window shows ("" = none/unresolved);
	// CurrentNote says why it is empty when a resolve was attempted and failed.
	Current     string      `json:"current"`
	CurrentNote string      `json:"current_note,omitempty"`
	Target      *cycleEntry `json:"target"`
	From        *navLoc     `json:"from"`
}

// The two empty rings are DIFFERENT problems needing different actions from the user —
// flag something, versus revive something — so they are distinct errors (and distinct
// dry-run outcomes), never one "nothing to do".
var (
	errCycleNoFlagged = errors.New("no flagged active threads")
	errCycleAllDead   = errors.New("flagged threads are all dead")
)

// masterClientWindow returns the name of the window the master client `client` is on
// (= the machine it shows). It iterates `list-clients -F`, which expands the format per
// CLIENT: `display-message -c` does NOT scope a format to that client (H98 follow-up 2),
// so with several clients on the master server it would read another client's window.
func masterClientWindow(masterSocket, client string) (string, error) {
	out, err := exec.Command("tmux", "-L", masterSocket, "list-clients", "-F", "#{client_name}\t#{window_name}").Output()
	if err != nil {
		return "", fmt.Errorf("list master clients on %q: %w", masterSocket, err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		name, win, ok := strings.Cut(line, "\t")
		if ok && name == client {
			return win, nil
		}
	}
	return "", fmt.Errorf("$SESH_NAV_CLIENT=%q is not a client of the master server %q", client, masterSocket)
}

// planCycleFlagged resolves one ring step. The start point is the active master window's
// current thread; with no cockpit context ($SESH_NAV_CLIENT unset) there is no start
// point, so `next` starts at the first entry and `prev` at the last — the same rule as
// when the window shows a thread outside the ring (a plain shell, a non-flagged thread).
// A failed resolve of the start point is REPORTED (CurrentNote) rather than fatal: the
// ring is still well-defined and the step is still useful, exactly as before issue #11.
func planCycleFlagged(cfg config.Config, dir string) (cyclePlan, error) {
	plan := cyclePlan{Schema: api.SchemaVersion}
	if dir != "next" && dir != "prev" {
		return plan, fmt.Errorf("--cycle-flagged must be next or prev, got %q", dir)
	}
	active := ""
	if carrier := os.Getenv("SESH_NAV_CLIENT"); carrier != "" {
		w, err := masterClientWindow(cfg.MasterSocket, carrier)
		if err != nil {
			return plan, fmt.Errorf("cycle: %w", err)
		}
		active = w
	}

	// The two reads are independent: the mesh is a local unix-socket read, the location
	// may be a routed peer round trip. Run them concurrently.
	var (
		wg      sync.WaitGroup
		mesh    api.MeshSnapshot
		meshErr error
		cur     api.MasterCurrentResponse
		curErr  error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		mesh, meshErr = daemonClient(cfg).Mesh(ctx)
	}()
	if active != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Asked of the LOCAL daemon with machine=<active>: it routes a remote resolve
			// over its own warm peer connection (handleTmuxMasterCurrent) instead of this
			// process dialling the peer cold.
			machine := active
			if machine == cfg.Machine {
				machine = ""
			}
			cur.Session, cur.ThreadID, cur.Window, curErr = daemonClient(cfg).TmuxMasterCurrent(ctx, cfg.Machine, machine)
		}()
	}
	wg.Wait()
	if meshErr != nil {
		return plan, fmt.Errorf("cycle: read the mesh view: %w", meshErr)
	}
	switch {
	case active == "":
		plan.CurrentNote = "no cockpit context ($SESH_NAV_CLIENT unset)"
	case curErr != nil:
		plan.CurrentNote = fmt.Sprintf("could not resolve what the %s window shows: %v", active, curErr)
	default:
		plan.Current = cur.ThreadID
		if cur.Session != "" {
			plan.From = &navLoc{Machine: active, Session: cur.Session, Window: cur.Window}
		}
	}

	for _, r := range tui.FlaggedRing(mesh.Machines) {
		// ENTERABLE = a live pane to land on. A flagged headless thread stays in the ring
		// (it is on screen, and counts toward "all dead") but is stepped over: a cycle key
		// must never revive anything.
		plan.Ring = append(plan.Ring, cycleEntry{ID: r.ID, Machine: r.Machine, Name: r.Name, Session: r.SessionName,
			Head: string(r.Head), Enterable: r.Head == api.Headful && r.SessionName != ""})
	}
	return plan, pickCycleTarget(&plan, dir)
}

// pickCycleTarget chooses the step's target from plan.Ring and plan.Current, setting
// Outcome and Target. Pure (no IO), so the wrap and start-point rules are unit-tested.
func pickCycleTarget(plan *cyclePlan, dir string) error {
	var enterable []cycleEntry
	for _, e := range plan.Ring {
		if e.Enterable {
			enterable = append(enterable, e)
		}
	}
	switch {
	case len(plan.Ring) == 0:
		plan.Outcome = "no-flagged"
		return errCycleNoFlagged
	case len(enterable) == 0:
		plan.Outcome = "all-dead"
		return fmt.Errorf("%w (%d flagged, none enterable)", errCycleAllDead, len(plan.Ring))
	}
	idx := -1
	for i, e := range enterable {
		if e.ID == plan.Current {
			idx = i
			break
		}
	}
	n := len(enterable)
	var t int
	switch {
	case idx < 0 && dir == "next":
		t = 0
	case idx < 0:
		t = n - 1
	case dir == "next":
		t = (idx + 1) % n
	default:
		t = (idx - 1 + n) % n
	}
	plan.Outcome = "ok"
	plan.Target = &enterable[t]
	return nil
}
