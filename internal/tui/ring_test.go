package tui

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/lukastk/sesh/internal/api"
)

// snap builds one mesh thread for the ring tests.
type snapOpt func(*api.ThreadSnapshot)

func flagged(s *api.ThreadSnapshot)  { s.Flagged = true }
func archived(s *api.ThreadSnapshot) { s.Archived = true }
func onHold(s *api.ThreadSnapshot)   { s.OnHold = true }
func headless(s *api.ThreadSnapshot) { s.Head = api.Headless }
func parent(p string) snapOpt        { return func(s *api.ThreadSnapshot) { s.Parent = p } }
func pinned(k float64) snapOpt       { return func(s *api.ThreadSnapshot) { s.PinOrder = &k } }

func snap(machine, id, name string, opts ...snapOpt) api.ThreadSnapshot {
	s := api.ThreadSnapshot{Head: api.Headful, Busy: api.BusyIdle}
	s.ID, s.Machine, s.Name, s.SessionName = id, machine, name, "sess-"+id
	for _, o := range opts {
		o(&s)
	}
	return s
}

func ringIDs(rows []api.ThreadRow) []string {
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// TestFlaggedRingOrder pins the ring to the grid's render order across every shape the
// myrig jq copy had to reproduce: pinned roots first by fractional key, children after
// their parent (any depth), a child whose parent the active view drops promoted to a
// root, archived-but-flagged kept, on-hold dropped even when flagged, unflagged rows
// shaping the order without appearing in it.
func TestFlaggedRingOrder(t *testing.T) {
	machines := []api.MachineView{
		{Machine: "alpha", Self: true, Reachable: true, Threads: []api.ThreadSnapshot{
			snap("alpha", "a1", "zeta", flagged),                   // unpinned root, sorts late by name
			snap("alpha", "a2", "beta"),                            // unflagged parent ...
			snap("alpha", "a3", "child", parent("a2"), flagged),    // ... of a flagged child
			snap("alpha", "a4", "grand", parent("a3"), flagged),    // flagged grandchild
			snap("alpha", "a5", "held", onHold, flagged),           // hold beats flag: out
			snap("alpha", "a6", "orphan", parent("a5"), flagged),   // parent dropped => promoted root
			snap("alpha", "a7", "parked", archived, flagged),       // archived but flagged: in
			snap("alpha", "a8", "quiet", archived, headless),       // archived, not flagged: not in the view
			snap("alpha", "a9", "pin-late", pinned(2.0), flagged),  // pinned roots lead ...
			snap("alpha", "a0", "pin-early", pinned(1.0), flagged), // ... by pin key, not name
		}},
		{Machine: "bravo", Reachable: true, Threads: []api.ThreadSnapshot{
			snap("bravo", "b1", "alpha", flagged, headless), // flagged but dead: still IN the ring (enterability is the caller's call)
		}},
	}
	got := ringIDs(FlaggedRing(machines))
	// roots in (machine, name, id) after the pinned block: alpha/beta(a2), alpha/orphan(a6),
	// alpha/parked(a7), alpha/zeta(a1), bravo/alpha(b1)
	want := []string{"a0", "a9", "a3", "a4", "a6", "a7", "a1", "b1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ring = %v\nwant   %v", got, want)
	}
}

// TestFlaggedRingHidesOfflinePeersNotSelf: an unreachable PEER's threads cannot be
// entered, so they are never in the ring; self is never hidden (flattenMeshRows' rule).
func TestFlaggedRingHidesOfflinePeersNotSelf(t *testing.T) {
	machines := []api.MachineView{
		{Machine: "self", Self: true, Reachable: false, Threads: []api.ThreadSnapshot{snap("self", "s1", "x", flagged)}},
		{Machine: "gone", Reachable: false, Threads: []api.ThreadSnapshot{snap("gone", "g1", "y", flagged)}},
		{Machine: "up", Reachable: true, Threads: []api.ThreadSnapshot{snap("up", "u1", "z", flagged)}},
	}
	if got, want := ringIDs(FlaggedRing(machines)), []string{"s1", "u1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ring = %v, want %v (offline peer g1 must be hidden, self kept)", got, want)
	}
}

// TestFlaggedRingEmpty: no flags at all is an empty ring, not an error here — the verb
// turns it into its own loud outcome.
func TestFlaggedRingEmpty(t *testing.T) {
	machines := []api.MachineView{{Machine: "a", Self: true, Reachable: true, Threads: []api.ThreadSnapshot{snap("a", "1", "x")}}}
	if got := FlaggedRing(machines); len(got) != 0 {
		t.Fatalf("ring = %v, want empty", ringIDs(got))
	}
}

// TestFlaggedRingMatchesRenderedGridAnyFoldState is the "shared code, same answer"
// property: over random trees, the ring equals the flagged subsequence of what the grid
// ACTUALLY renders with its default COLLAPSED folds (fold-piercing), and with every node
// expanded. If FlaggedRing ever stopped being the grid's own walk — or piercing stopped
// preserving walk order — this goes red.
func TestFlaggedRingMatchesRenderedGridAnyFoldState(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for iter := 0; iter < 500; iter++ {
		var machines []api.MachineView
		var all []string
		for mi, mname := range []string{"m1", "m2", "m3"} {
			mv := api.MachineView{Machine: mname, Self: mi == 0, Reachable: true}
			n := 1 + rng.Intn(9)
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("%s-%02d", mname, i)
				var opts []snapOpt
				if rng.Intn(2) == 0 {
					opts = append(opts, flagged)
				}
				switch rng.Intn(6) {
				case 0:
					opts = append(opts, archived)
				case 1:
					opts = append(opts, onHold)
				}
				if i > 0 && rng.Intn(3) != 0 {
					// Parent within the same machine, any earlier node (a real tree, any depth).
					opts = append(opts, parent(fmt.Sprintf("%s-%02d", mname, rng.Intn(i))))
				} else if rng.Intn(4) == 0 {
					opts = append(opts, pinned(float64(rng.Intn(5))))
				}
				mv.Threads = append(mv.Threads, snap(mname, id, fmt.Sprintf("n%d", rng.Intn(4)), opts...))
				all = append(all, id)
			}
			machines = append(machines, mv)
		}
		ring := ringIDs(FlaggedRing(machines))
		rows, _ := flattenMeshRows(machines, ViewActive, nil, nil, true, true, "")
		for _, expand := range []bool{false, true} {
			m := Model{rows: rows, defaultExpand: expand}
			var rendered []string
			for _, tr := range m.visibleMatches() {
				if tr.row.Flagged {
					rendered = append(rendered, tr.row.ID)
				}
			}
			if !reflect.DeepEqual(ring, rendered) {
				t.Fatalf("iter %d expand=%v: ring %v != rendered flagged rows %v (threads %v)", iter, expand, ring, rendered, all)
			}
		}
	}
}
