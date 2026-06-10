// Package subscribe holds the delivery-side logic for session subscriptions
// (exp19): formatting the message a subscriber receives, and a Tracker that
// decides — per (subscriber, subscribee) edge — whether a completed turn should
// be delivered. The subscription EDGES live on records (replicated, mutated via
// the OP_SUBSCRIBE owner op); this package is only the delivery decision the
// subscribee's owning daemon runs on a busy→idle transition.
package subscribe

import (
	"fmt"
	"sync"
	"time"
)

// FormatDelivery is the message a subscriber receives when a subscribee
// completes a turn: a header naming the subscribee (name + uuid), then its
// latest reply verbatim.
func FormatDelivery(subscribeeUUID, subscribeeName, reply string) string {
	name := subscribeeName
	if name == "" {
		name = "(unnamed)"
	}
	return fmt.Sprintf("[sesh] %s (%s) completed a turn:\n\n%s", name, subscribeeUUID, reply)
}

// Tracker makes the per-edge delivery decision with two guards:
//
//   - DEDUP: a turn (identified by the monotone reply count) is delivered at
//     most once per edge — so a redundant idle event or a daemon restart never
//     re-delivers, and an unchanged count is silently skipped.
//   - CIRCUIT BREAKER: at most MaxPerWindow deliveries per edge per Window. The
//     subscription graph is acyclic by default (Subscribe refuses cycles), but
//     --allow-cycle re-opens runaway potential; the breaker hard-stops a looping
//     edge (and the caller logs it once) without throttling normal use, since
//     real turns rarely exceed the cap.
type Tracker struct {
	mu           sync.Mutex
	lastCount    map[string]int
	recent       map[string][]time.Time
	MaxPerWindow int
	Window       time.Duration
}

func NewTracker(maxPerWindow int, window time.Duration) *Tracker {
	return &Tracker{
		lastCount:    map[string]int{},
		recent:       map[string][]time.Time{},
		MaxPerWindow: maxPerWindow,
		Window:       window,
	}
}

func edgeKey(subscriber, subscribee string) string { return subscriber + "\x00" + subscribee }

// Seed restores an edge's last-delivered count (from persisted state) so the
// engine doesn't re-deliver already-sent turns after a restart.
func (t *Tracker) Seed(subscriber, subscribee string, count int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastCount[edgeKey(subscriber, subscribee)] = count
}

// Decision is the outcome of ShouldDeliver.
type Decision int

const (
	Skip      Decision = iota // nothing new (dedup) — silent
	Deliver                   // send it
	RateLimit                 // circuit breaker tripped — caller should warn once
)

// ShouldDeliver reports whether to deliver subscribee's turn (identified by
// reply count) to subscriber, recording the decision. now is injected for
// testability.
func (t *Tracker) ShouldDeliver(subscriber, subscribee string, count int, now time.Time) Decision {
	key := edgeKey(subscriber, subscribee)
	t.mu.Lock()
	defer t.mu.Unlock()
	if count <= t.lastCount[key] {
		return Skip // already delivered this turn (or no new reply)
	}
	if t.MaxPerWindow > 0 {
		cutoff := now.Add(-t.Window)
		kept := t.recent[key][:0]
		for _, ts := range t.recent[key] {
			if ts.After(cutoff) {
				kept = append(kept, ts)
			}
		}
		t.recent[key] = kept
		if len(kept) >= t.MaxPerWindow {
			return RateLimit // looping edge — stop delivering this window
		}
		t.recent[key] = append(t.recent[key], now)
	}
	t.lastCount[key] = count
	return Deliver
}
