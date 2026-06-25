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
func effectiveHolds(threads []api.Thread) map[string]int64 {
	own := make(map[string]int64, len(threads))
	parent := make(map[string]string, len(threads))
	for _, t := range threads {
		own[t.ID] = t.OnHoldUntilUnix
		parent[t.ID] = t.Parent
	}
	eff := make(map[string]int64, len(threads))
	for id := range own {
		best := own[id]
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
			cur = parent[cur]
		}
		eff[id] = best
	}
	return eff
}
