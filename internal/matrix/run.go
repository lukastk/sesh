package matrix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// recorder collects per-cell outcomes during a test run. It is process-global
// so RunCell (called from the conformance subtests) and the grid renderer (in
// TestMain) share one view.
type recorder struct {
	mu      sync.Mutex
	results map[string]Status
	reasons map[string]string // skip reason, keyed by cell ID
}

var rec = &recorder{
	results: map[string]Status{},
	reasons: map[string]string{},
}

func (r *recorder) set(id string, s Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[id] = s
}

func (r *recorder) setReason(id, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons[id] = reason
}

func (r *recorder) get(id string) (Status, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.results[id]
	return s, ok
}

// Skip marks the current cell test as not-yet-implemented. It records the
// reason for the `skips` query AND calls t.Skip with the AGENTS.md-mandated
// "NOT IMPLEMENTED: " prefix so the cell renders yellow in `go test` too. This
// is the only sanctioned way to leave a cell unfinished.
func Skip(t *testing.T, reason string) {
	t.Helper()
	rec.setReason(testCellID(t), reason)
	t.Skipf("NOT IMPLEMENTED: %s", reason)
}

// RunCell executes a bound cell test under t and records its outcome via a
// cleanup hook (which fires even after Skip/FailNow do a runtime.Goexit). The
// recorded status is the single source of truth for the grid.
func RunCell(t *testing.T, c Cell, fn func(t *testing.T)) {
	t.Helper()
	t.Cleanup(func() {
		switch {
		case t.Failed():
			rec.set(c.ID(), StatusFail)
		case t.Skipped():
			rec.set(c.ID(), StatusSkip)
		default:
			rec.set(c.ID(), StatusPass)
		}
	})
	fn(t)
}

// testCellID recovers the cell ID from a subtest's name. Subtests are run as
// t.Run(cell.ID(), ...), and t.Name() is "TestMatrix/<cell.ID()>"; we want the
// trailing cell ID. Kept simple and robust: take everything after the first
// slash.
func testCellID(t *testing.T) string {
	name := t.Name()
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

// ---- result snapshot + artifact ----

// CellResult is one row of the run artifact.
type CellResult struct {
	Feature  string   `json:"feature"`
	Agent    Agent    `json:"agent"`
	Locality Locality `json:"locality"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason,omitempty"`
}

// Snapshot is the full resolved state of the matrix after a run.
type Snapshot struct {
	Cells []CellResult `json:"cells"`
}

// resolveStatus computes the authoritative status for a cell, combining static
// structure (N/A, missing) with recorded run outcomes.
func resolveStatus(f Feature, c Cell) (Status, string) {
	if reason, na := f.NAReason(c.Agent, c.Locality); na {
		return StatusNA, reason
	}
	if !hasTest(c) {
		return StatusMissing, ""
	}
	if s, ok := rec.get(c.ID()); ok {
		reason := ""
		if s == StatusSkip {
			rec.mu.Lock()
			reason = rec.reasons[c.ID()]
			rec.mu.Unlock()
		}
		return s, reason
	}
	return StatusNotRun, ""
}

// BuildSnapshot resolves every expected and N/A cell of every feature.
func BuildSnapshot() Snapshot {
	var snap Snapshot
	for _, f := range Features() {
		cells := append(f.ExpectedCells(), f.NACells()...)
		for _, c := range cells {
			s, reason := resolveStatus(f, c)
			snap.Cells = append(snap.Cells, CellResult{
				Feature:  c.Feature,
				Agent:    c.Agent,
				Locality: c.Locality,
				Status:   s.String(),
				Reason:   reason,
			})
		}
	}
	return snap
}

// ArtifactPath is where the grid renderer writes the last-run snapshot, so the
// `sesh matrix` CLI can report it. Overridable via SESH_MATRIX_ARTIFACT. The
// default resolves to the repo root (go tests run in the package dir, so a bare
// relative path would land in the wrong place).
func ArtifactPath() string {
	if p := os.Getenv("SESH_MATRIX_ARTIFACT"); p != "" {
		return p
	}
	return filepath.Join(repoRoot(), "_dev", ".matrix-state.json")
}

// repoRoot walks up from the working directory to the directory containing
// go.mod. Panics loudly if not found — there is no sane fallback for "where is
// the repo", and a wrong guess would silently scatter artifacts.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic("matrix: cannot get working directory: " + err.Error())
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("matrix: could not locate repo root (no go.mod found walking up from " + dir + ")")
		}
		dir = parent
	}
}

// WriteArtifact persists the snapshot as JSON.
func WriteArtifact(path string) error {
	data, err := json.MarshalIndent(BuildSnapshot(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadArtifact loads a previously written snapshot.
func ReadArtifact(path string) (Snapshot, error) {
	var snap Snapshot
	data, err := os.ReadFile(path)
	if err != nil {
		return snap, err
	}
	err = json.Unmarshal(data, &snap)
	return snap, err
}
