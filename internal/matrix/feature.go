package matrix

// NAEntry is a justified non-applicable cell within a feature's axes. The
// Reason must be a real justification Lukas has signed off (e.g. "pi has no
// headless mode — by design"). Silently dropping a cell from a feature's axes
// to avoid testing it is a violation; an N/A is the *only* honest way to
// exclude one, and it stays visible in the grid.
type NAEntry struct {
	Agent    Agent
	Locality Locality
	Reason   string
}

// Feature is a registered, conformant capability. It declares the axes it
// applies to; the matrix derives the cells it must cover.
//
// Agents nil/empty => agent-agnostic (a single AgentAgnostic column).
// Localities must be a non-empty subset of {Local, Remote}.
type Feature struct {
	ID          string
	Description string
	Agents      []Agent
	Localities  []Locality
	NA          []NAEntry
}

// agentAxis returns the agent values this feature ranges over, substituting the
// agnostic sentinel when there is no agent axis.
func (f Feature) agentAxis() []Agent {
	if len(f.Agents) == 0 {
		return []Agent{AgentAgnostic}
	}
	return f.Agents
}

// NAReason returns the justification for a cell being N/A, and whether it is.
func (f Feature) NAReason(a Agent, l Locality) (string, bool) {
	for _, e := range f.NA {
		if e.Agent == a && e.Locality == l {
			return e.Reason, true
		}
	}
	return "", false
}

// ExpectedCells is the set of cells this feature must cover: the cartesian
// product of its axes, minus justified N/A cells. These are the cells that
// TestMatrixComplete demands a bound test for.
func (f Feature) ExpectedCells() []Cell {
	var out []Cell
	for _, l := range f.Localities {
		for _, a := range f.agentAxis() {
			if _, na := f.NAReason(a, l); na {
				continue
			}
			out = append(out, Cell{Feature: f.ID, Agent: a, Locality: l})
		}
	}
	return out
}

// NACells is the set of justified-N/A cells (for grid rendering).
func (f Feature) NACells() []Cell {
	var out []Cell
	for _, l := range f.Localities {
		for _, a := range f.agentAxis() {
			if _, na := f.NAReason(a, l); na {
				out = append(out, Cell{Feature: f.ID, Agent: a, Locality: l})
			}
		}
	}
	return out
}
