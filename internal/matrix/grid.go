package matrix

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
)

// statusFn resolves a cell's status (and any reason text). Two implementations
// exist: liveStatus, used in-process during a test run (consults the recorder +
// the test-binding registry), and snapshotStatus, used by the `sesh matrix` CLI
// (reads a persisted run artifact, since the CLI process has no test bindings).
type statusFn func(f Feature, c Cell) (Status, string)

func liveStatus(f Feature, c Cell) (Status, string) { return resolveStatus(f, c) }

func snapshotStatus(snap Snapshot) statusFn {
	m := map[string]CellResult{}
	for _, cr := range snap.Cells {
		m[Cell{Feature: cr.Feature, Agent: cr.Agent, Locality: cr.Locality}.ID()] = cr
	}
	return func(f Feature, c Cell) (Status, string) {
		cr, ok := m[c.ID()]
		if !ok {
			return StatusNotRun, ""
		}
		return ParseStatus(cr.Status), cr.Reason
	}
}

// ParseStatus is the inverse of Status.String.
func ParseStatus(s string) Status {
	switch s {
	case "pass":
		return StatusPass
	case "fail":
		return StatusFail
	case "skip":
		return StatusSkip
	case "n/a":
		return StatusNA
	case "missing":
		return StatusMissing
	case "not-run":
		return StatusNotRun
	default:
		return StatusNotRun
	}
}

// Counts is a tally of cell statuses across the whole matrix.
type Counts struct {
	Pass, Fail, Skip, NA, Missing, NotRun, Total int
}

// AllGreen is the single computed "done" gate: every expected cell passes,
// nothing is skipped, failing, missing, or unrun. N/A cells don't count against
// it (they're justified non-cells).
func (c Counts) AllGreen() bool {
	return c.Fail == 0 && c.Skip == 0 && c.Missing == 0 && c.NotRun == 0 && c.Pass > 0
}

func tally(status statusFn) Counts {
	var c Counts
	for _, f := range Features() {
		for _, cell := range f.ExpectedCells() {
			s, _ := status(f, cell)
			c.Total++
			switch s {
			case StatusPass:
				c.Pass++
			case StatusFail:
				c.Fail++
			case StatusSkip:
				c.Skip++
			case StatusMissing:
				c.Missing++
			case StatusNotRun:
				c.NotRun++
			}
		}
		c.NA += len(f.NACells())
	}
	return c
}

// Tally counts statuses using the live (in-test) status source.
func Tally() Counts { return tally(liveStatus) }

// TallySnapshot counts statuses from a persisted run artifact.
func TallySnapshot(snap Snapshot) Counts { return tally(snapshotStatus(snap)) }

// RenderGrid prints the live matrix (during a test run) to w.
func RenderGrid(w io.Writer) { renderGrid(w, liveStatus) }

// RenderSnapshotGrid prints a persisted run artifact's matrix to w.
func RenderSnapshotGrid(w io.Writer, snap Snapshot) { renderGrid(w, snapshotStatus(snap)) }

func renderGrid(w io.Writer, status statusFn) {
	cols := gridColumns()

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "sesh v2 — feature matrix")
	fmt.Fprintln(w, "  legend: ✓ pass  ✗ fail  ⚠ skip  · n/a  ∅ missing-test  ? not-run")
	fmt.Fprintln(w)

	header := "FEATURE\t"
	for _, col := range cols {
		header += col.label() + "\t"
	}
	fmt.Fprintln(tw, header)

	for _, f := range Features() {
		row := f.ID + "\t"
		for _, col := range cols {
			if !inAxes(f, col.agent, col.locality) {
				row += "\t"
				continue
			}
			cell := Cell{Feature: f.ID, Agent: col.agent, Locality: col.locality}
			s, _ := status(f, cell)
			row += s.Glyph() + "\t"
		}
		fmt.Fprintln(tw, row)
	}
	tw.Flush()

	c := tally(status)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "summary: %d cells — %d pass, %d fail, %d skip, %d missing, %d not-run, %d n/a\n",
		c.Total, c.Pass, c.Fail, c.Skip, c.Missing, c.NotRun, c.NA)

	if skips := skipsWith(status); len(skips) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "⚠ %d SKIPPED (NOT IMPLEMENTED) — these do NOT count as done:\n", len(skips))
		for _, sk := range skips {
			fmt.Fprintf(w, "    %s — %s\n", sk.Cell.ID(), sk.Reason)
		}
	}
	if c.Missing > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "∅ %d MISSING TESTS — expected cells with no bound test (red build):\n", c.Missing)
		for _, f := range Features() {
			for _, cell := range f.ExpectedCells() {
				if s, _ := status(f, cell); s == StatusMissing {
					fmt.Fprintf(w, "    %s\n", cell.ID())
				}
			}
		}
	}

	fmt.Fprintln(w)
	if c.AllGreen() {
		fmt.Fprintln(w, "ALL GREEN ✓ — matrix complete, zero skips, zero missing.")
	} else {
		fmt.Fprintln(w, "NOT DONE — matrix is not all-green.")
	}
}

// column identifies a grid column.
type column struct {
	agent    Agent
	locality Locality
}

func (c column) label() string {
	if c.agent == AgentAgnostic {
		return string(c.locality[:1]) // l / r
	}
	return fmt.Sprintf("%s.%s", agentAbbrev(c.agent), c.locality[:1])
}

func agentAbbrev(a Agent) string {
	switch a {
	case Claude:
		return "cl"
	case Codex:
		return "co"
	case Pi:
		return "pi"
	default:
		return string(a)
	}
}

// gridColumns is the ordered union of (agent,locality) columns across all
// features: agnostic columns first, then per-agent, each in (local,remote).
func gridColumns() []column {
	seen := map[column]bool{}
	for _, f := range Features() {
		for _, l := range f.Localities {
			for _, a := range f.agentAxis() {
				seen[column{a, l}] = true
			}
		}
	}
	var cols []column
	for c := range seen {
		cols = append(cols, c)
	}
	sort.Slice(cols, func(i, j int) bool {
		oi, oj := agentSortKey(cols[i].agent), agentSortKey(cols[j].agent)
		if oi != oj {
			return oi < oj
		}
		return cols[i].locality < cols[j].locality
	})
	return cols
}

func agentSortKey(a Agent) int {
	switch a {
	case AgentAgnostic:
		return 0
	case Claude:
		return 1
	case Codex:
		return 2
	case Pi:
		return 3
	default:
		return 9
	}
}

func inAxes(f Feature, a Agent, l Locality) bool {
	axisOK := false
	for _, fa := range f.agentAxis() {
		if fa == a {
			axisOK = true
		}
	}
	if !axisOK {
		return false
	}
	for _, fl := range f.Localities {
		if fl == l {
			return true
		}
	}
	return false
}

// SkipInfo is a skipped cell and its reason.
type SkipInfo struct {
	Cell   Cell
	Reason string
}

func skipsWith(status statusFn) []SkipInfo {
	var out []SkipInfo
	for _, f := range Features() {
		for _, cell := range f.ExpectedCells() {
			if s, reason := status(f, cell); s == StatusSkip {
				out = append(out, SkipInfo{Cell: cell, Reason: reason})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cell.ID() < out[j].Cell.ID() })
	return out
}

// Skips returns all live-skipped cells (during a test run).
func Skips() []SkipInfo { return skipsWith(liveStatus) }

// SnapshotSkips returns all skipped cells from a persisted run artifact.
func SnapshotSkips(snap Snapshot) []SkipInfo { return skipsWith(snapshotStatus(snap)) }
