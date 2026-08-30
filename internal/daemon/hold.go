package daemon

import "github.com/lukastk/sesh/internal/api"

// holdChainCap bounds the ancestor walk — a backstop against a cyclic parent graph
// (reparent already refuses cycles) and pathological depth.
const holdChainCap = 256

// effectiveHolds maps each thread id to its EFFECTIVE on-hold deadline: the MAX of its
// own on_hold_until and every SAME-MACHINE ancestor's own, so a child inherits a parent's
// hold (max(parent, own); a held parent parks its whole subtree). The walk uses only the
// records in `threads` (one machine's store), so a cross-machine parent — absent from the
// set — ends the chain: cross-machine ancestry is NOT inherited (the owner resolves only
// its own records). A cycle or a missing parent stops the walk (visited set + depth cap).
//
// RELEASE (schema 48) is the escape hatch from that max. A thread whose
// hold_release_until is still in the future (against nowUnix, the owning daemon's
// clock) is DETACHED from its ancestors: it contributes only its own deadline, and
// the walk from any DESCENDANT stops at it — so releasing a thread frees its whole
// subtree with it, rather than freeing a supervisor while its workers stay parked.
// A released ancestor's OWN hold still parks its subtree; what a release cuts is
// only what flows through that node from ABOVE. The clock argument is why this is
// no longer a pure function of the record set: a release lapses with no record
// write, exactly like a hold, so nextHoldFlip must schedule a sweep for it.
func effectiveHolds(threads []api.Thread, nowUnix int64) map[string]int64 {
	own := make(map[string]int64, len(threads))
	parent := make(map[string]string, len(threads))
	released := make(map[string]bool, len(threads))
	for _, t := range threads {
		own[t.ID] = t.OnHoldUntilUnix
		parent[t.ID] = t.Parent
		released[t.ID] = t.HoldReleaseUntilUnix > nowUnix
	}
	eff := make(map[string]int64, len(threads))
	for id := range own {
		best := own[id]
		if released[id] {
			// Detached from above: only this thread's own deadline applies. (The
			// store keeps hold and release mutually exclusive, so `best` is 0 here
			// in practice; taking own anyway keeps a hand-written record honest —
			// an explicit hold on the thread itself is never silently dropped.)
			eff[id] = best
			continue
		}
		seen := map[string]bool{id: true}
		cur := parent[id]
		for depth := 0; cur != "" && depth < holdChainCap; depth++ {
			if seen[cur] {
				break // cycle (reparent guards against this; belt-and-braces)
			}
			seen[cur] = true
			h, ok := own[cur]
			if !ok {
				break // cross-machine / unknown parent: chain ends, no further inheritance
			}
			if h > best {
				best = h
			}
			if released[cur] {
				break // this ancestor is detached from ITS ancestors, so we are too
			}
			cur = parent[cur]
		}
		eff[id] = best
	}
	return eff
}

// nextHoldFlip is the earliest FUTURE instant at which some thread's on-hold flag
// changes with NO record write, so the maintainer must force a full sweep then.
// Two kinds of deadline do that: an effective hold LAPSING (held → free), and a
// release lapsing (free → held again, because an ancestor's hold snaps back on).
// Missing the second would leave a released thread reading un-held indefinitely
// after its release expired — a stale view that no write would ever correct.
func nextHoldFlip(effHolds map[string]int64, threads []api.Thread, nowUnix int64) int64 {
	var next int64
	consider := func(at int64) {
		if at > nowUnix && (next == 0 || at < next) {
			next = at
		}
	}
	for _, until := range effHolds {
		consider(until)
	}
	for _, t := range threads {
		consider(t.HoldReleaseUntilUnix)
	}
	return next
}

// holdDominator returns the id of the ANCESTOR whose own hold decides `id`'s
// effective deadline — i.e. the thread that is really keeping it parked when it is
// not its own doing. Empty when the thread is not held, is held by its own
// deadline, or is released. It exists so the hold endpoint can tell a caller WHY a clear did
// not un-hold a thread: inheritance is resolved over the owner's whole record set,
// so no client can work this out for itself.
func holdDominator(threads []api.Thread, id string, nowUnix int64) string {
	byID := make(map[string]api.Thread, len(threads))
	for _, t := range threads {
		byID[t.ID] = t
	}
	self, ok := byID[id]
	if !ok || self.HoldReleaseUntilUnix > nowUnix {
		return ""
	}
	best, bestID := self.OnHoldUntilUnix, ""
	seen := map[string]bool{id: true}
	cur := self.Parent
	for depth := 0; cur != "" && depth < holdChainCap; depth++ {
		if seen[cur] {
			break
		}
		seen[cur] = true
		a, ok := byID[cur]
		if !ok {
			break
		}
		if a.OnHoldUntilUnix > best {
			best, bestID = a.OnHoldUntilUnix, cur
		}
		if a.HoldReleaseUntilUnix > nowUnix {
			break
		}
		cur = a.Parent
	}
	if best <= nowUnix { // not actually held right now
		return ""
	}
	return bestID
}
