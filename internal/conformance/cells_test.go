package conformance

import (
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

// Real cell tests register themselves via init() in their layer files
// (daemon_test.go, tmux_test.go, ...). registerRemainingSkips then binds a
// NOT-IMPLEMENTED Skip to every still-unbound expected cell — the all-yellow
// remainder that is this project's to-do list. It runs at the top of TestMain,
// after all init()s, so it is order-independent: a cell with a real test is
// never double-bound.

// skipReasons gives a human reason per feature for its not-yet-implemented cells.
var skipReasons = map[string]string{
	"tmux.current":          "resolve calling terminal locator + owning thread",
	"tmux.info":             "cross-machine session/window/pane walk",
	"tmux.create-session":   "create a tmux session",
	"tmux.create-pane":      "create a pane",
	"tmux.nav":              "outer switch + inner switch-client + detached-pane kick",
	"tmux.stage-file":       "copy local file to machine, return staged path",
	"tmux.send-text":        "paste/send text into a pane",
	"thread.new.headed":     "spawn headed thread in a real tmux pane",
	"thread.new.headless":   "spawn headless thread (stateless-per-turn)",
	"thread.kill":           "kill a thread",
	"thread.send.headful":   "send into live pane (codex: directory-trust prompt at spawn eats input; needs per-dir trust handling)",
	"thread.send.headless":  "send as a turn (stateless-per-turn)",
	"thread.list":           "mesh-replicated cross-machine list",
	"thread.resolve-pane":   "resolve pane via @sesh-thread-id marker",
	"thread.runtime-state":  "activity+attachment axes (codex: directory-trust prompt at spawn eats input; needs per-dir trust handling)",
	"thread.rename":         "rename a thread record",
	"thread.tag":            "add/remove tags",
	"thread.archive":        "park a thread (hidden from active list, record kept)",
	"thread.delete":         "drop a record without touching the runtime",
	"thread.resume":         "revive a dead headed thread; claude blocked — interactive claude buffers its transcript and flushes only on graceful exit, so a hard-killed claude session leaves only a title and is not resumable (pending Lukas decision)",
	"ticket.create":         "create a ticket",
	"ticket.list-by-thread": "list tickets assigned to a thread",
	"ticket.send-prompt":    "deliver prompt to bound thread (codex: directory-trust prompt at spawn eats input)",
	"ticket.set-status":     "set status incl. agent-driven done",
	"ticket.needs-input":    "derived view active && waiting",
	"daemon.lifecycle":      "start/stop/status",
	"daemon.mesh-read":      "cross-machine read via peer mesh",
	"ticket.ownership":      "single canonical owner, writes route to owner",
	"api.http-json":         "client-facing HTTP+JSON surface",
}

// registerRemainingSkips binds a Skip to every expected cell that no real test
// claimed. Derived from each feature's declared axes, so it can never drift.
func registerRemainingSkips() {
	for _, f := range matrix.Features() {
		reason := skipReasons[f.ID]
		if reason == "" {
			reason = f.ID
		}
		for _, c := range f.ExpectedCells() {
			if matrix.HasBoundTest(c) {
				continue
			}
			label := f.ID + ": " + reason
			matrix.RegisterTest(c.Feature, c.Agent, c.Locality, func(t *testing.T) {
				matrix.Skip(t, label)
			})
		}
	}
}
