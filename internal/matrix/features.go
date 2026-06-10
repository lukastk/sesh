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
		Description: "spawn a thread with NO pane (a durable conversation, idle until revived or sent a turn) — no tmux session exists, state is the unified idle",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.parent",
		Description: "parent/child threads: new --parent (default: the current thread via inference; --no-parent = root), reparent --parent/--root with existence + cycle guards",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.info",
		Description: "sesh info [id|prefix]: describe one thread; with no arg the CURRENT thread is inferred (explicit > $SESH_THREAD_ID > the calling pane's birth-stamp > loud)",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.stop",
		Description: "end a thread's runtime (agent + session) but KEEP the record (idle, revivable)",
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
		Description: "run a turn on an IDLE thread (headless-born or previously-headed — headless is runtime, not a mode): working while in flight, idle after, reply captured; a LIVE pane refuses the turn loudly (would fork the conversation)",
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
		ID:          "thread.headful",
		Description: "revive an idle never-paned thread into a real tmux pane (== resume under the unified model; the verb is kept) — a real agent lands in a real pane resuming the conversation",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.headful-busy",
		Description: "promoting a headless thread mid-turn (a turn in flight) is rejected with a conflict — never spawn a pane that forks the conversation",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "tmux.nav-in-client",
		Description: "`tmux nav --in-client`: inside the work socket's tmux, navigating to a LOCAL session switches THIS client in place (no master); errors loudly for a remote target or off the work socket",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "master.up",
		Description: "`sesh master up` builds the cross-machine cockpit: one window per machine (name == machine), each GENUINELY attached into that machine's work server (peer over a real ssh hop)",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "master.reconnect",
		Description: "the per-window supervisor self-heals: after the attach is dropped, it re-establishes — for both the local window and the ssh-localhost peer window (drop observed before heal)",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "master.holding",
		Description: "a master window for a machine with NO live threads falls back to a holding 'scratch' shell session (attaches + stays a work-server client) instead of looping on 'no sessions'",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "tmux.nav-attach",
		Description: "`tmux nav --attach` (Enter from a plain shell, no tmux) attaches the terminal to the thread — a real client lands on the target session",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "tmux.nav-in-client-multi",
		Description: "with MULTIPLE clients on one work-socket session, `nav --in-client` switches exactly the client the caller identifies (--client / the $SESH_NAV_CLIENT a popup keybinding bakes in); a carrier-less ambiguous call fails loudly moving NOBODY (ambient picks are arbitrary)",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "master.selfheal",
		Description: "the daemon's cockpit-convergence loop keeps one window per CONNECTED machine: a killed window comes back by itself (real ssh re-attach), an unreachable machine never gets a window forced, and a deliberately downed master stays down",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "master.ensure",
		Description: "`sesh master ensure` converges the master to one-window-per-machine: recreates ONLY missing machine windows (the prefix+K recovery) with a REAL re-attach, never touches existing windows, no-ops when complete, and builds the whole master when down",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "master.watchers",
		Description: "`sesh master watchers` lists the origin masters with a LIVE window-attach into this work server (marker liveness-checked against real clients) — present while a real ssh-attached master is up, gone after master down; powers 'send to my master' routing",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "tmux.nav-master-multi",
		Description: "with MULTIPLE clients on a machine's work server (the master's window supervisor + a direct attach), the master-path nav switches the MASTER WINDOW's client — recorded by the supervisor's attach marker — and never moves the direct attach (the old `list-clients | head -1` resolution moved the wrong client); peer = real ssh hop",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "tmux.work-conf",
		Description: "SESH_TMUX_CONF: the WORK tmux server is started with `tmux -f <conf>` (carries sesh's own tmux UI, separate from the user's default ~/.tmux.conf) — proved by a sentinel option only that conf sets being live on the work socket",
		Localities:  []Locality{Local},
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
		Description: "the two orthogonal state axes head(headful/headless) x busy(busy/idle) + attachment — all transitions, both directions (the v1 codex bug)",
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
		ID:          "thread.session-name",
		Description: "configurable session naming: [[session_name]] cwd-regex rules in <SESH_HOME>/config.toml template the REAL tmux session name (named groups + {tid8}/{name}/{cwd}); applies to headed spawn AND revival minting; no match = default sesh_<name>; a broken config refuses the daemon loudly",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "thread.resume",
		Description: "revive an IDLE thread into a pane: recreate session + relaunch agent with --resume (conversation continuity; == headful under the unified model)",
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
		ID:          "daemon.hooks",
		Description: "[[hooks]] event hooks: observed busy/head/lifecycle edges run user commands ($SHELL -c, event env); observer-bound across the mesh; list/enable/disable/test",
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
