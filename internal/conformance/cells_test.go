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
	"tmux.current":              "resolve calling terminal locator + owning thread",
	"tmux.info":                 "cross-machine session/window/pane walk",
	"tmux.create-session":       "create a tmux session",
	"tmux.create-pane":          "create a pane",
	"tmux.nav":                  "outer switch + inner switch-client + detached-pane kick",
	"tmux.stage-file":           "copy local file to machine, return staged path",
	"tmux.send-text":            "paste/send text into a pane",
	"thread.new.headed":         "spawn headed thread in a real tmux pane",
	"thread.new.headless":       "spawn a thread with no pane: no tmux session, unified idle state (stateless-per-turn)",
	"thread.parent":             "parent/child records: new --parent (+inference/--no-parent) + reparent (cycle guard loud)",
	"thread.info":               "describe one thread; no-arg = current-thread inference (explicit/prefix > env > pane stamp > loud)",
	"thread.stop":               "end a thread's runtime but keep the record (idle, revivable)",
	"thread.send.headful":       "send into live pane (codex: directory-trust prompt at spawn eats input; needs per-dir trust handling)",
	"thread.send.headless":      "run a turn on any IDLE thread (headless- or headed-born, conversation continuity); a live pane refuses loudly",
	"thread.list":               "mesh-replicated cross-machine list",
	"thread.list-all.http":      "live fan-out (thread list --all-machines) over the peer's TCP API — SSH↔HTTP parity twin",
	"thread.grid.http":          "live fan-out grid over the peer's TCP API — SSH↔HTTP parity twin",
	"thread.resolve-pane":       "resolve pane via @sesh-thread-id marker",
	"thread.capture":            "capture a thread's live pane text (v1 pane-capture); dead = loud 409; routed cross-machine",
	"thread.runtime-state":      "head(headful/headless) x busy(busy/idle) + attachment axes, all transitions both directions (codex: directory-trust prompt at spawn eats input)",
	"thread.rename":             "rename a thread record",
	"thread.tag":                "add/remove tags",
	"thread.archive":            "park a thread (hidden from active list, record kept)",
	"thread.delete":             "drop a record without touching the runtime",
	"thread.headful":            "revive a never-paned idle thread into a pane (== resume, unified model); codex-before-any-turn is N/A (separate test)",
	"thread.headful-busy":       "reviving a thread mid-turn (headless turn in flight) is rejected with a conflict",
	"thread.session-name":      "[[session_name]] config rules name the real tmux session from cwd (spawn + revival); default without a match; broken config = no daemon",
	"thread.resume":             "revive an idle thread into a pane (recreate session + relaunch with --resume); conversation continuity verified for all three agents (claude needs a clean top-level env, guaranteed by daemon ScrubHarnessEnv)",
	"thread.snapshot":           "GET /v1/snapshot reflects real live state from the background maintainer (O(1) read, tracks waiting<->working)",
	"mesh.snapshot":             "GET /v1/mesh: L2 sync replicates a peer's snapshot into the local cache (ssh transport); merged view read locally",
	"mesh.snapshot.http":        "L2 sync replicates a peer's snapshot over the peer's TCP API (http transport) — SSH↔HTTP parity twin",
	"mesh.offline-listing":      "offline browsing (ssh transport): a downed peer's threads stay listed (reachable=false), and recover when it returns",
	"mesh.offline-listing.http": "offline browsing over the http transport — SSH↔HTTP parity twin",
	"route.parity":              "--machine routing over ssh: thread/ticket/tmux ops land on the peer's daemon",
	"route.parity.http":         "--machine routing over the peer's TCP API (http) — SSH↔HTTP routing parity twin",
	"tmux.nav-in-client":        "nav --in-client switches the current client to a local session (no master); loud on remote target / off-socket",
	"master.up":                 "sesh master up builds a window per machine, each attached into that machine's work server (peer over ssh)",
	"master.reconnect":          "the per-window supervisor re-establishes a dropped attach (local + ssh-localhost peer)",
	"master.holding":            "an empty work server's master window falls back to a holding 'scratch' shell, not a 'no sessions' loop",
	"master.selfheal":          "the daemon converges the cockpit: killed windows self-recreate (real re-attach), unreachable machines get none, downed masters stay down",
	"master.ensure":            "master ensure recreates only missing machine windows (real re-attach), no-ops when complete, builds when down",
	"master.watchers":          "master watchers lists origins with a live window-attach (marker liveness), present while up, gone after down",
	"tmux.work-conf":            "the work tmux server starts with `tmux -f <SESH_TMUX_CONF>` (sesh's own UI, separate from ~/.tmux.conf)",
	"tmux.nav-in-client-multi":  "with multiple clients, nav --in-client switches exactly the carried client (--client/$SESH_NAV_CLIENT); ambiguous carrier-less calls fail loudly",
	"tmux.nav-master-multi":     "with multiple clients on a work server, the master-path nav switches the master window's marker-recorded client; a direct attach never moves",
	"tmux.nav-master-http":      "the master-path nav's inner switch-client follows the peer's transport: an http peer carries it over POST /v1/tmux/nav (no ssh) — proved by a broken ssh dest + the switch still landing",
	"tmux.master-current":       "`tmux master-current --origin X` resolves the thread a master window is currently showing (routed) and tracks the client across nav — the data behind the TUI's async prefix+s preselect",
	"tmux.nav-attach":           "nav --attach (Enter from a plain shell) attaches the terminal to the thread (a client lands on it)",
	"api.tcp-auth":              "TCP API bearer-token auth: 401 on missing/wrong, 200 on correct, refuses to start without a token",
	"api.tcp-parity":            "TCP API full parity: a remote client drives thread/ticket/tmux/mesh/snapshot over TCP+token",
	"ticket.create":             "create a ticket",
	"ticket.list-by-thread":     "list tickets assigned to a thread",
	"ticket.send-prompt":        "deliver prompt to bound thread (codex: directory-trust prompt at spawn eats input)",
	"ticket.set-status":         "set status incl. agent-driven done",
	"ticket.needs-input":        "derived view active && waiting",
	"thread.spawn-mode":         "config yolo on real argv per agent; --sandbox override (codex read-only argv); pi sandbox loud; args passthrough; --msg reaches the conversation",
	"thread.meta":               "KV set/get/unset round-trip + wire; meta.<key> predicates see real values; missing key loud; remote routed",
	"thread.adopt":              "a manual real agent adopted with its TRUE session id (argv/socket/rollout, or an explicit --session-id when undetectable); becomes a managed headful thread; non-agent/managed/unknown panes loud",
	"thread.fork":               "fork@turn-1 carries A not B (real divergence); the branch CONTINUES with memory; the source byte-untouched; loud out-of-range/turn-less",
	"thread.backup":             "backup→wipe→restore byte-equal per agent; claude native restore + RESUMED memory; idempotent; copy composes; loud guards; remote routed",
	"thread.subscribe":          "a real turn lands formatted in the subscriber's REAL pane exactly once; cycle refused/--allow-cycle; unsubscribe stops; remote = peer-owned delivery into a local pane",
	"thread.transcript":         "real transcript located+read after a real turn (sentinel in lines + last_reply; monotone reply_count; pre-turn loud); remote routed",
	"thread.delegate":           "real one-shot answer + worker GONE after (ephemeral both directions); --keep = usable thread w/ memory; --sandbox loud until E3",
	"thread.await":              "blocks until a real turn finishes (reply available on return); remote = NO routing, the mesh carries it; timeout/unknown loud",
	"thread.notify":             "per-thread gate: creation default from config, toggle round-trip, SESH_NOTIFY in real hook env both values",
	"thread.import":             "v1 records imported (uuid as id+session-id, tags/parent/archived mapped, per-machine scope, idempotent re-import, dry-run, missing store loud)",
	"daemon.doctor":             "agent-on-daemon-$SHELL-PATH check is real (deploy-env class), config-parse status, broken config = FAIL + non-zero exit",
	"daemon.hooks":              "[[hooks]] fire on real observed edges; remote = observer-bound (a LOCAL hook fires for a PEER's edge via the mesh cache)",
	"daemon.lifecycle":          "start/stop/status",
	"daemon.mesh-read":          "cross-machine read via peer mesh",
	"ticket.ownership":          "single canonical owner, writes route to owner",
	"api.http-json":             "client-facing HTTP+JSON surface",
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
