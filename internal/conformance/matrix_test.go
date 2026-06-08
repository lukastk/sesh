// Package conformance is the sesh v2 feature matrix: one bound test per
// expected cell across (local,remote) x (claude,codex,pi). It contains only
// tests. TestMatrix runs every bound cell; TestMatrixComplete fails the build
// if any expected cell lacks a bound test; TestMain renders the grid and writes
// the run artifact afterwards.
//
// Honesty (AGENTS.md) is on the test author, not this harness: a cell may go
// green ONLY via a real test (real agent in a real tmux pane; remote = a real
// ssh hop). Until then it is matrix.Skip("NOT IMPLEMENTED: ...") — yellow, and
// never counted as done.
package conformance

import (
	"fmt"
	"os"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func TestMain(m *testing.M) {
	code := m.Run()
	cleanupHarness()

	// Render the grid for humans...
	matrix.RenderGrid(os.Stdout)

	// ...and persist the snapshot so `sesh matrix` can report the last run.
	if err := matrix.WriteArtifact(matrix.ArtifactPath()); err != nil {
		fmt.Fprintf(os.Stderr, "matrix: failed to write artifact: %v\n", err)
	}

	os.Exit(code)
}

// TestMatrix runs every bound cell test as a subtest, recording pass/fail/skip
// for the grid.
func TestMatrix(t *testing.T) {
	bound := matrix.BoundCells()
	if len(bound) == 0 {
		t.Fatal("no cell tests bound — the matrix is empty")
	}
	for _, b := range bound {
		b := b
		t.Run(b.Cell.ID(), func(t *testing.T) {
			matrix.RunCell(t, b.Cell, b.Fn)
		})
	}
}

// TestMatrixComplete fails the build if any expected cell has no bound test, or
// if any bound test drifted off its feature's declared axes. This is the
// completeness gate — a missing test is a red build, not a blank.
func TestMatrixComplete(t *testing.T) {
	if missing := matrix.MissingCells(); len(missing) > 0 {
		for _, c := range missing {
			t.Errorf("expected cell has NO bound test: %s", c.ID())
		}
	}
	if orphans := matrix.OrphanCells(); len(orphans) > 0 {
		for _, c := range orphans {
			t.Errorf("bound test is off-axis / not an expected cell: %s", c.ID())
		}
	}
}
