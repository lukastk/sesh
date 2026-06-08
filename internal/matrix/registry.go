package matrix

import (
	"fmt"
	"sort"
	"sync"
	"testing"
)

var (
	mu sync.Mutex

	// featureOrder preserves registration order for stable grid rendering.
	featureOrder []string
	features     = map[string]Feature{}

	// cellTests binds a cell ID to its test function. A cell with no entry here
	// is StatusMissing (a red build) unless it is N/A.
	cellTests = map[string]boundTest{}
)

type boundTest struct {
	cell Cell
	fn   func(t *testing.T)
}

// Register adds a feature to the registry. It panics (loud, by design) on a
// duplicate id, an empty locality axis, or a malformed N/A entry — these are
// programmer errors in the spine itself and must never degrade silently.
func Register(f Feature) {
	mu.Lock()
	defer mu.Unlock()
	if f.ID == "" {
		panic("matrix.Register: empty feature id")
	}
	if _, dup := features[f.ID]; dup {
		panic(fmt.Sprintf("matrix.Register: duplicate feature id %q", f.ID))
	}
	if len(f.Localities) == 0 {
		panic(fmt.Sprintf("matrix.Register: feature %q has no localities", f.ID))
	}
	for _, l := range f.Localities {
		if l != Local && l != Remote {
			panic(fmt.Sprintf("matrix.Register: feature %q has bad locality %q", f.ID, l))
		}
	}
	axis := map[Agent]bool{}
	for _, a := range f.agentAxis() {
		axis[a] = true
	}
	for _, e := range f.NA {
		if e.Reason == "" {
			panic(fmt.Sprintf("matrix.Register: feature %q N/A cell (%s,%s) lacks a justification", f.ID, e.Agent, e.Locality))
		}
		if !axis[e.Agent] {
			panic(fmt.Sprintf("matrix.Register: feature %q N/A references agent %q outside its axis", f.ID, e.Agent))
		}
	}
	features[f.ID] = f
	featureOrder = append(featureOrder, f.ID)
}

// Features returns all registered features in registration order.
func Features() []Feature {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Feature, 0, len(featureOrder))
	for _, id := range featureOrder {
		out = append(out, features[id])
	}
	return out
}

// FeatureByID returns a registered feature.
func FeatureByID(id string) (Feature, bool) {
	mu.Lock()
	defer mu.Unlock()
	f, ok := features[id]
	return f, ok
}

// RegisterTest binds a test function to a cell. It panics if the cell is not an
// expected cell of a registered feature (unknown feature, off-axis agent/
// locality, or a cell the feature declared N/A) or if a test is already bound —
// this is what stops a test from quietly drifting off the declared axes.
func RegisterTest(feature string, a Agent, l Locality, fn func(t *testing.T)) {
	mu.Lock()
	defer mu.Unlock()
	f, ok := features[feature]
	if !ok {
		panic(fmt.Sprintf("matrix.RegisterTest: unknown feature %q", feature))
	}
	cell := Cell{Feature: feature, Agent: a, Locality: l}
	if !expected(f, cell) {
		panic(fmt.Sprintf("matrix.RegisterTest: cell %s is not an expected cell of feature %q (off-axis or N/A)", cell.ID(), feature))
	}
	if _, dup := cellTests[cell.ID()]; dup {
		panic(fmt.Sprintf("matrix.RegisterTest: duplicate test bound to cell %s", cell.ID()))
	}
	cellTests[cell.ID()] = boundTest{cell: cell, fn: fn}
}

func expected(f Feature, c Cell) bool {
	for _, e := range f.ExpectedCells() {
		if e == c {
			return true
		}
	}
	return false
}

// boundCells returns all bound cell tests, sorted by cell ID for deterministic
// run order.
func boundCells() []boundTest {
	mu.Lock()
	defer mu.Unlock()
	out := make([]boundTest, 0, len(cellTests))
	for _, b := range cellTests {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].cell.ID() < out[j].cell.ID() })
	return out
}

// BoundCell pairs a cell with its bound test function, for the conformance
// runner to execute.
type BoundCell struct {
	Cell Cell
	Fn   func(t *testing.T)
}

// BoundCells returns every bound cell test in deterministic (cell ID) order.
func BoundCells() []BoundCell {
	out := make([]BoundCell, 0)
	for _, b := range boundCells() {
		out = append(out, BoundCell{Cell: b.cell, Fn: b.fn})
	}
	return out
}

func hasTest(c Cell) bool {
	mu.Lock()
	defer mu.Unlock()
	_, ok := cellTests[c.ID()]
	return ok
}
