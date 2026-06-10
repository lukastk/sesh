package subscribe

import (
	"strings"
	"testing"
	"time"
)

func TestFormatDelivery(t *testing.T) {
	out := FormatDelivery("uuid-1", "beta", "the answer is 42")
	if !strings.Contains(out, "uuid-1") || !strings.Contains(out, "beta") || !strings.Contains(out, "42") {
		t.Fatalf("bad delivery format: %q", out)
	}
	if !strings.Contains(FormatDelivery("u", "", "x"), "(unnamed)") {
		t.Fatal("unnamed subscribee should still get a header")
	}
}

func TestTrackerDedup(t *testing.T) {
	tr := NewTracker(0, 0) // breaker disabled
	now := time.Unix(0, 0)
	if d := tr.ShouldDeliver("S", "T", 1, now); d != Deliver {
		t.Fatalf("turn 1 = %v, want Deliver", d)
	}
	if d := tr.ShouldDeliver("S", "T", 1, now); d != Skip {
		t.Fatalf("same turn again = %v, want Skip", d)
	}
	if d := tr.ShouldDeliver("S", "T", 2, now); d != Deliver {
		t.Fatalf("turn 2 = %v, want Deliver", d)
	}
	// distinct edge is independent
	if d := tr.ShouldDeliver("S2", "T", 1, now); d != Deliver {
		t.Fatalf("other subscriber turn 1 = %v, want Deliver", d)
	}
}

func TestTrackerSeedRestart(t *testing.T) {
	tr := NewTracker(0, 0)
	tr.Seed("S", "T", 5)
	now := time.Unix(0, 0)
	if d := tr.ShouldDeliver("S", "T", 5, now); d != Skip {
		t.Fatalf("turn 5 after seed = %v, want Skip", d)
	}
	if d := tr.ShouldDeliver("S", "T", 6, now); d != Deliver {
		t.Fatalf("turn 6 after seed = %v, want Deliver", d)
	}
}

// The circuit breaker hard-stops a looping (--allow-cycle) edge: at most
// MaxPerWindow deliveries per Window, then RateLimit.
func TestTrackerCircuitBreaker(t *testing.T) {
	tr := NewTracker(3, time.Minute)
	base := time.Unix(1000, 0)
	count := 0
	deliveries := 0
	for i := 0; i < 10; i++ {
		count++
		// each "turn" is a new count, 1s apart — a tight loop within the window
		if tr.ShouldDeliver("S", "T", count, base.Add(time.Duration(i)*time.Second)) == Deliver {
			deliveries++
		}
	}
	if deliveries != 3 {
		t.Fatalf("breaker allowed %d deliveries, want 3 (MaxPerWindow)", deliveries)
	}
	// After the window passes, the edge can deliver again.
	count++
	if d := tr.ShouldDeliver("S", "T", count, base.Add(2*time.Minute)); d != Deliver {
		t.Fatalf("post-window delivery = %v, want Deliver", d)
	}
}
