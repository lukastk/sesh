package conformance

import (
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

// This file binds a test to every expected cell of every registered feature.
// In Phase 0 they are ALL Skip ("NOT IMPLEMENTED") — the all-yellow grid that
// is this project's to-do list. As features land, replace a feature's skipAll
// line with explicit per-cell tests (real agent, real tmux, real ssh) that go
// green honestly; delete its skipAll once every cell is covered.

// skipCell returns a cell body that records a NOT-IMPLEMENTED skip.
func skipCell(reason string) func(t *testing.T) {
	return func(t *testing.T) { matrix.Skip(t, reason) }
}

// skipAll binds a Skip to every expected cell of a feature. Derived from the
// feature's declared axes, so it can never drift from the registration.
func skipAll(feature, reason string) {
	f, ok := matrix.FeatureByID(feature)
	if !ok {
		panic("conformance: skipAll on unknown feature " + feature)
	}
	for _, c := range f.ExpectedCells() {
		matrix.RegisterTest(c.Feature, c.Agent, c.Locality, skipCell(reason))
	}
}

func init() {
	// ---- tmux layer ----
	skipAll("tmux.current", "tmux.current: resolve calling terminal locator + owning thread")
	skipAll("tmux.info", "tmux.info: cross-machine session/window/pane walk")
	skipAll("tmux.create-session", "tmux.create-session")
	skipAll("tmux.create-pane", "tmux.create-pane")
	skipAll("tmux.nav", "tmux.nav: outer switch + inner switch-client + detached-pane kick")
	skipAll("tmux.stage-file", "tmux.stage-file: copy local file to machine, return staged path")
	skipAll("tmux.send-text", "tmux.send-text: paste/send text into a pane")

	// ---- thread layer ----
	skipAll("thread.new.headed", "thread.new.headed: spawn headed thread in a real tmux pane")
	skipAll("thread.new.headless", "thread.new.headless: spawn headless thread (pi N/A pending Lukas sign-off)")
	skipAll("thread.kill", "thread.kill")
	skipAll("thread.send.headful", "thread.send.headful: send into live pane")
	skipAll("thread.send.headless", "thread.send.headless: send as a turn (pi N/A pending Lukas sign-off)")
	skipAll("thread.list", "thread.list: mesh-replicated cross-machine list")
	skipAll("thread.resolve-pane", "thread.resolve-pane: resolve pane via @sesh-thread-id marker")
	skipAll("thread.runtime-state", "thread.runtime-state: working/waiting/dead/detached, both directions")

	// ---- ticket layer ----
	skipAll("ticket.create", "ticket.create")
	skipAll("ticket.list-by-thread", "ticket.list-by-thread")
	skipAll("ticket.send-prompt", "ticket.send-prompt: deliver prompt to bound thread")
	skipAll("ticket.set-status", "ticket.set-status incl. agent-driven done")
	skipAll("ticket.needs-input", "ticket.needs-input: derived view active && waiting")

	// ---- daemon / API ----
	// daemon.lifecycle: real tests bound in daemon_test.go (local + remote).
	skipAll("daemon.mesh-read", "daemon.mesh-read: cross-machine read via peer mesh")
	skipAll("ticket.ownership", "ticket.ownership: single canonical owner, writes route to owner")
	skipAll("api.http-json", "api.http-json: client-facing HTTP+JSON surface")
}
