// Package matrix is the tracking spine for sesh v2.
//
// It enforces *completeness* and *visibility*, not honesty: every conformant
// feature declares the cells it must cover across (local,remote) x
// (claude,codex,pi), and TestMatrixComplete fails the build if any expected
// cell has no bound test. The grid renderer makes the state loud. Honesty —
// that a green cell exercises the real thing — is enforced by AGENTS.md and by
// Lukas's audit agent, deliberately NOT by clever framework code here.
package matrix

import "fmt"

// Agent is a coding-agent kind. The empty-meaning sentinel AgentAgnostic marks
// a feature with no agent axis (e.g. tmux/ticket plumbing).
type Agent string

const (
	Claude        Agent = "claude"
	Codex         Agent = "codex"
	Pi            Agent = "pi"
	AgentAgnostic Agent = "-"
)

// AllAgents is the real agent axis (excludes the agnostic sentinel).
var AllAgents = []Agent{Claude, Codex, Pi}

// Locality is where the exercised code path runs.
type Locality string

const (
	Local  Locality = "local"
	Remote Locality = "remote"
)

// AllLocalities is the full locality axis.
var AllLocalities = []Locality{Local, Remote}

// Cell is one square of the matrix: a (feature, agent, locality) triple. For an
// agent-agnostic feature, Agent is AgentAgnostic.
type Cell struct {
	Feature  string
	Agent    Agent
	Locality Locality
}

// ID is the stable, unique key for a cell. Used as map key and subtest name.
func (c Cell) ID() string {
	return fmt.Sprintf("%s/%s/%s", c.Feature, c.Agent, c.Locality)
}

func (c Cell) String() string { return c.ID() }

// Status is the resolved outcome of a cell after a test run.
type Status int

const (
	// StatusMissing: an expected cell with no bound test. A red build.
	StatusMissing Status = iota
	// StatusNA: a justified, signed-off non-applicable cell.
	StatusNA
	// StatusSkip: a bound test that called Skip ("NOT IMPLEMENTED: ...").
	StatusSkip
	// StatusFail: a bound test that ran and failed.
	StatusFail
	// StatusPass: a bound test that ran and passed honestly.
	StatusPass
	// StatusNotRun: a bound test that exists but was not executed this run.
	StatusNotRun
)

// Glyph is the single-character grid rendering of a status.
func (s Status) Glyph() string {
	switch s {
	case StatusPass:
		return "✓"
	case StatusFail:
		return "✗"
	case StatusSkip:
		return "⚠"
	case StatusNA:
		return "·"
	case StatusMissing:
		return "∅"
	case StatusNotRun:
		return "?"
	default:
		return "?"
	}
}

func (s Status) String() string {
	switch s {
	case StatusPass:
		return "pass"
	case StatusFail:
		return "fail"
	case StatusSkip:
		return "skip"
	case StatusNA:
		return "n/a"
	case StatusMissing:
		return "missing"
	case StatusNotRun:
		return "not-run"
	default:
		return "unknown"
	}
}
