package main

import (
	"errors"
	"testing"
)

func ringOf(spec ...string) []cycleEntry {
	// "x" = enterable x, "x!" = flagged but dead
	var r []cycleEntry
	for _, s := range spec {
		dead := s[len(s)-1] == '!'
		if dead {
			s = s[:len(s)-1]
		}
		r = append(r, cycleEntry{ID: s, Enterable: !dead})
	}
	return r
}

// TestPickCycleTarget: the ring step's rules — wrap both ways, dead rows stepped over
// (never revived), no/unknown start point enters at the matching END, and the two empty
// rings are distinct outcomes.
func TestPickCycleTarget(t *testing.T) {
	cases := []struct {
		name, current, dir, want, outcome string
		ring                              []cycleEntry
		err                               error
	}{
		{"next from middle", "b", "next", "c", "ok", ringOf("a", "b", "c"), nil},
		{"prev from middle", "b", "prev", "a", "ok", ringOf("a", "b", "c"), nil},
		{"next wraps", "c", "next", "a", "ok", ringOf("a", "b", "c"), nil},
		{"prev wraps", "a", "prev", "c", "ok", ringOf("a", "b", "c"), nil},
		{"dead stepped over next", "a", "next", "c", "ok", ringOf("a", "b!", "c"), nil},
		{"dead stepped over prev", "c", "prev", "a", "ok", ringOf("a", "b!", "c"), nil},
		{"no start: next = first", "", "next", "a", "ok", ringOf("a", "b", "c"), nil},
		{"no start: prev = last", "", "prev", "c", "ok", ringOf("a", "b", "c"), nil},
		{"start outside ring: next = first", "zz", "next", "a", "ok", ringOf("a", "b"), nil},
		{"start is a dead row: prev = last enterable", "b", "prev", "c", "ok", ringOf("a", "b!", "c"), nil},
		{"single entry next is itself", "a", "next", "a", "ok", ringOf("a"), nil},
		{"no flagged", "", "next", "", "no-flagged", nil, errCycleNoFlagged},
		{"all dead", "", "next", "", "all-dead", ringOf("a!", "b!"), errCycleAllDead},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := cyclePlan{Ring: c.ring, Current: c.current}
			err := pickCycleTarget(&plan, c.dir)
			if plan.Outcome != c.outcome {
				t.Fatalf("outcome = %q, want %q (err %v)", plan.Outcome, c.outcome, err)
			}
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err = %v, want %v", err, c.err)
				}
				if plan.Target != nil {
					t.Fatalf("an empty ring must not name a target, got %+v", plan.Target)
				}
				return
			}
			if err != nil || plan.Target == nil || plan.Target.ID != c.want {
				t.Fatalf("target = %+v (err %v), want %q", plan.Target, err, c.want)
			}
		})
	}
}
