package matrix

// This file is the canonical registration of the sesh v2 conformance feature
// set, transcribed from _dev/PLAN.md. Registering here (rather than in a test
// file) means both `go test` and the `sesh matrix` CLI see the same grid.
//
// NOTE on N/A: AGENTS.md requires every N/A to carry a justification Lukas has
// signed off. None are asserted yet. In particular, the PLAN flags pi headless
// (`thread.new.headless`, `thread.send.headless`) as a *candidate* N/A pending
// confirmation that pi has no headless mode — those cells are left as ordinary
// expected cells (they will start as Skip) until Lukas signs off the N/A. This
// is the honest default: a pending decision is a yellow Skip, not a silent drop.

func init() {
	agentic := AllAgents // {claude, codex, pi}
	bothLoc := AllLocalities

	// ---- tmux layer (agent-agnostic) ----
	Register(Feature{
		ID:          "tmux.current",
		Description: "resolve the calling terminal's locator + owning thread id",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "tmux.info",
		Description: "JSONL walk of sessions/windows/panes across machines; --machine/--session",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "tmux.create-session",
		Description: "create a tmux session on the mytmux socket",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "tmux.create-pane",
		Description: "create a pane within a session",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "tmux.nav",
		Description: "the nav primitive: outer switch + inner switch-client + detached-pane kick (Local is trivial)",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "tmux.stage-file",
		Description: "copy a local file to a machine, return the staged remote path",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "tmux.send-text",
		Description: "paste/send text into a pane at the cursor",
		Localities:  bothLoc,
	})

	// ---- thread layer (full (L,R) x (c,co,pi) unless N/A) ----
	Register(Feature{
		ID:          "thread.new.headed",
		Description: "spawn a headed thread (visible pane); codex can't pre-assign a session id",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.new.headless",
		Description: "spawn a headless thread (persistent child agent, no window)",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.stop",
		Description: "end a thread's runtime (agent + session) but KEEP the record (dead, resumable)",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.send.headful",
		Description: "send a message into a thread's live pane",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.send.headless",
		Description: "send a message to a headless thread as a turn",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.list",
		Description: "mesh-replicated cross-machine thread list (agent-agnostic)",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.list-all",
		Description: "daemon-side mesh fan-out (ssh transport): GET /v1/threads?all-machines aggregates this machine + every peer",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "thread.list-all.http",
		Description: "thread.list-all's SSH↔HTTP twin: the live fan-out reaches the peer over its TCP API (http transport) instead of ssh-exec",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "thread.grid",
		Description: "live status grid: every thread with its real activity/attachment, concurrently; remote = mesh fan-out over ssh (the TUI's data source)",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.grid.http",
		Description: "thread.grid's SSH↔HTTP twin (remote only): the live fan-out grid reaches the peer over its TCP API (http transport)",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "thread.snapshot",
		Description: "GET /v1/snapshot: each thread's live state from the background maintainer — an O(1) read (no on-demand probe), tracks waiting<->working (see _dev/MESH.md L1)",
		Agents:      agentic,
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "mesh.snapshot",
		Description: "GET /v1/mesh over the SSH transport: the background sync (L2) replicates each peer's snapshot into the local cache; the merged view is read locally with a peer's threads + live state",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "mesh.snapshot.http",
		Description: "mesh.snapshot's SSH↔HTTP parity twin: the SAME replication, but the peer is reached over its TCP API (HTTP transport) instead of ssh-exec — proves the http sync path works, not just ssh",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "mesh.offline-listing",
		Description: "offline browsing (SSH transport): a peer going down keeps its last-known threads listed (reachable=false, retained from cache), and a recovered peer refreshes to reachable",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "mesh.offline-listing.http",
		Description: "mesh.offline-listing's SSH↔HTTP parity twin: same offline-browsing guarantee with the peer reached over its TCP API — a downed HTTP peer stays listed and recovers",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "route.parity",
		Description: "`--machine` routing over the SSH transport: representative client-only ops (thread/ticket/tmux) routed to a peer land on the peer's daemon (carve-outs daemon-lifecycle/nav/stage-file stay ssh by design)",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "route.parity.http",
		Description: "route.parity's SSH↔HTTP twin: the SAME routed ops carried over the peer's TCP API (HTTP transport) instead of ssh-exec — proves `--machine` routing has http parity, not just ssh",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "api.tcp-auth",
		Description: "the optional TCP API (mobile/remote) requires a bearer token — rejects missing/wrong (401), accepts correct (200), and refuses to start exposed without a token",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "api.tcp-parity",
		Description: "the TCP API has FULL parity with the local one (same router): a remote client drives every layer — thread, ticket, tmux, mesh, snapshot — over TCP+token",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "thread.resolve-pane",
		Description: "runtime pane resolution via the @sesh-thread-id pane user-option",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.runtime-state",
		Description: "working/waiting/dead/detached — test all transitions, both directions (the v1 codex bug)",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.rename",
		Description: "rename a thread (record only)",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.tag",
		Description: "add/remove tags on a thread",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.archive",
		Description: "archive/unarchive — park a thread, hidden from the active list, record kept",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.delete",
		Description: "drop a record without touching the runtime (unlike kill)",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.resume",
		Description: "revive a dead headed thread: recreate session + relaunch agent with --resume",
		Agents:      agentic,
		Localities:  bothLoc,
	})

	// ---- ticket layer ----
	Register(Feature{
		ID:          "ticket.create",
		Description: "create a ticket",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "ticket.list-by-thread",
		Description: "list the tickets assigned to a thread",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "ticket.send-prompt",
		Description: "deliver a ticket's prompt to its bound thread (touches agent send)",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "ticket.set-status",
		Description: "set ticket status, incl. agent-driven done",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "ticket.needs-input",
		Description: "derived view = active && thread waiting; incl. dead != needs-input",
		Localities:  bothLoc,
	})

	// ---- daemon / API ----
	Register(Feature{
		ID:          "daemon.lifecycle",
		Description: "daemon start/stop/status",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "daemon.mesh-read",
		Description: "cross-machine read via the peer mesh",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "ticket.ownership",
		Description: "single canonical owner; writes route to owner; read-cache elsewhere",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "api.http-json",
		Description: "client-facing HTTP+JSON surface (CLI/TUI/Obsidian plugin)",
		Localities:  bothLoc,
	})
}
