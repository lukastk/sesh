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
		ID:          "tmux.kill-session",
		Description: "kill one session by name on the work server (routed); non-existent session is loud — the mechanism behind myrig's kill-empty-sessions cleanup",
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
	// H13: a tmux session may host MANY threads (runtime identity is the pane's
	// marker). Placement/sharing cells are agent-agnostic tmux topology and
	// LOCAL-only by design (per-server pane ids — the thread.adopt precedent).
	Register(Feature{
		ID:          "thread.placement",
		Description: "spawn a thread INTO an existing session (--into-session = own window) or window (--into-window = split): many threads per session",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "thread.placement-pane",
		Description: "--into-pane register-then-exec: bind a thread to an EXISTING shell pane (record + marker + returned launch command; daemon does NOT spawn), then the agent runs in place",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "thread.stop-shared",
		Description: "stop kills only the thread's PANE, not its session — a sibling thread sharing the session survives (KillSession would take both)",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "thread.adopt-shared",
		Description: "adopt a 2nd agent into a session that already hosts a managed thread — both coexist (the dropped UNIQUE(session_name) constraint); same-pane re-adopt is a clean 409",
		Localities:  []Locality{Local},
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
		ID:          "mesh.delta-sync.http",
		Description: "delta sync over the TCP API (schema 41): steady-state sync rounds transfer ~empty deltas, a one-row change transfers ~one row (measured at the wire through a counting proxy between two REAL daemons), and the replicated set stays COMPLETE throughout — the transfer-layer replacement for the rejected archived-slim. HTTP-only by design: the ssh transport always full-fetches",
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
		ID:          "tmux.nav-window",
		Description: "`tmux nav --thread <id>` lands on the WINDOW holding the thread's @sesh-thread-id pane (resolved on the owner's work server), not the session's last-active window — so entering a thread whose session has extra windows returns to the thread's own window; a session-level nav (no --thread) stays put",
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
		ID:          "tmux.master-current",
		Description: "`tmux master-current --origin X` resolves the thread a master window is CURRENTLY showing (the origin master's marker client's active-pane @sesh-thread-id on that machine's work server) — the data behind the TUI's async master prefix+s preselect; routed over the mesh and TRACKS the client across nav (not a stale snapshot)",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "tmux.nav-master-http",
		Description: "the master-path nav's INNER switch-client FOLLOWS the peer's transport: for an http peer (api_addr set) it is carried over the daemon's TCP API (POST /v1/tmux/nav) with NO ssh hop — proved by a deliberately broken ssh dest (http-only.invalid) + the switch still landing; ssh peers keep the (now connection-multiplexed) ssh inner switch",
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
		ID:          "thread.capture",
		Description: "capture the live text of a thread's tmux pane (v1 pane-capture); dead thread = loud 409; routed cross-machine (pane resolved on the owner)",
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
		ID:          "thread.send-wait",
		Description: "send --wait / thread wait --until: server-owned bounded waits released by REAL turn boundaries, with a fail-fast stall guard when delivered input produces no state change (schema 43)",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.done-seen",
		Description: "the done/seen marker: a HEADFUL turn ending while unattended (detached, or attached with stale input) reads done until seen (fresh input or an attachment flip onto it); agent-independent derivation over the busy edge + attachment axes (schema 43)",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.blocked",
		Description: "the blocked overlay: mid-turn stalled on the human (approval prompt), reported by the in-agent hook; blocked always implies busy; cleared by approval (unblocked), turn boundaries, and pane death (schema 43)",
		Agents:      agentic,
		Localities:  bothLoc,
		NA: []NAEntry{
			{Agent: Pi, Locality: Local, Reason: "pi natively never blocks: tools run unapproved by design (defaultProjectTrust/yolo-equivalent) and extension-raised UI asks expose no public event — revisit if pi grows an ask/approval event"},
			{Agent: Pi, Locality: Remote, Reason: "pi natively never blocks: tools run unapproved by design (defaultProjectTrust/yolo-equivalent) and extension-raised UI asks expose no public event — revisit if pi grows an ask/approval event"},
			{Agent: Codex, Locality: Local, Reason: "codex has no in-agent hook surface at all (see thread.state-authority) — blocked requires reported authority"},
			{Agent: Codex, Locality: Remote, Reason: "codex has no in-agent hook surface at all (see thread.state-authority) — blocked requires reported authority"},
		},
	})
	Register(Feature{
		ID:          "thread.state-authority",
		Description: "in-agent reporters (pi extension, claude hooks) override the content-diff busy heuristic; state_authority=reported|heuristic always visible; authority bounded by pane liveness (schema 43, _dev/STATE_AUTHORITY.md)",
		Agents:      agentic,
		Localities:  bothLoc,
		NA: []NAEntry{
			{Agent: Codex, Locality: Local, Reason: "codex has no in-agent hook surface for turn-start (its notify config reports only turn-end); one-directional authority is worse than the heuristic, so codex stays heuristic-only by design — Lukas 2026-07-23"},
			{Agent: Codex, Locality: Remote, Reason: "codex has no in-agent hook surface for turn-start (its notify config reports only turn-end); one-directional authority is worse than the heuristic, so codex stays heuristic-only by design — Lukas 2026-07-23"},
		},
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
		Description: "drop a record without touching the runtime (unlike kill); children are promoted to the deleted thread's parent (a parent id never dangles)",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.virtual",
		Description: "virtual threads: `thread new --virtual` records a pure grouping node (agent_kind virtual — no agent/pane/transcript) to parent threads under; grouping machinery (parent, reparent, hold inheritance) applies unchanged; every agent verb refuses loudly until realized",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.realize",
		Description: "thread realize: convert a virtual grouping thread IN PLACE into a real never-started headless thread (id/children/tags survive) — proven by a REAL first turn (+continuity); a non-virtual thread refuses loudly",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.pin",
		Description: "manual ordering: `thread pin` gives a top-level thread a fractional pin_order key so it renders ABOVE the auto-sorted block (--top/--bottom/--before/--after/--order); `thread unpin` clears it; pinning a child is refused; the key is cleared on archive AND reparent-under-another; routed to the owner",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.divider",
		Description: "dividers: `thread new --divider` records a visual node (agent_kind divider — no agent/pane/transcript, always pinned) rendered as a horizontal rule; agent verbs refuse loudly; it can't be archived or un-pinned (delete it); agent-shaped creation flags refused",
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
	Register(Feature{
		ID:          "thread.model",
		Description: "agent model selection: `thread new --model <m>` pins an opaque model on the thread (applied on spawn/resume/every headless turn), `send-headless --model` overrides it for one turn. Asserted on the OBSERVABLE model the agent actually ran (pi/claude: the model recorded in their transcript; codex: the exact model string reaching codex, proved by its loud rejection echoing it). LOCAL-only: the model is stored on the record + injected by the agent command builder, locality-independent; `--machine` routing of `thread new`/`send-headless` is proven by route.parity",
		Agents:      agentic,
		Localities:  []Locality{Local},
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
	Register(Feature{
		ID:          "ticket.get",
		Description: "fetch one ticket (ticket get --id); --field prints a single field raw (prompt for clipboard/agents); the description field is gone",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "ticket.set",
		Description: "partial text-field update (ticket set --name/--prompt); only passed flags apply (status/thread go through set-status)",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "ticket.list-current",
		Description: "ticket list --current auto-detects the calling pane's thread ($SESH_THREAD_ID / @sesh-thread-id marker) and lists its tickets — for agents checking their assignment; resolved client-side before owner-routing (Local: a local-pane concept; routing covered by route.parity)",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "ticket.unbind",
		Description: "ticket unbind --id detaches a ticket from its thread (clears thread_id; an active ticket downgrades to ready, unattached); 'remove from thread'. Unknown id is loud",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "ticket.find",
		Description: "mesh-wide ticket lookup by id (`sesh ticket find --id`): the invoked daemon resolves its own store, else fans out to every peer (each answering local-only, over real ssh) and returns the first hit — the ticket record + its owning machine + bound-thread {id,name,parent}. The mechanism behind the Obsidian ticket note (an API client locating a ticket without knowing its machine). A ticket found nowhere is found=false (not an error); also surfaces closed_at_unix",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "ticket.list-all",
		Description: "mesh-wide ticket LISTING (`sesh ticket list --all-machines`): the invoked daemon merges its own tickets with every reachable peer's (each answering local-only, over real ssh), each entry stamped with its owning machine + bound-thread name — the data behind the Obsidian ticket browser. Unreachable peers are reported, not dropped",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "ticket.move",
		Description: "daemon-coordinated cross-machine ticket relocation (`sesh ticket move --from --to`): the INVOKED daemon pulls the record + every @blob() the prompt references from SRC and pushes them to DST, then deletes the source — so a ticket binds to a thread on any machine with its blobs carried along (co-located live join preserved). Real ssh hops; id-collision on the target is loud",
		Localities:  []Locality{Remote},
	})
	Register(Feature{
		ID:          "blob.store",
		Description: "content-addressed file store (`blob add/ls/get/rm`): add→ls→get round-trips the bytes, identical bytes dedup to one hash, prefix resolution, rm removes, a missing/ambiguous ref is loud. Routes per --machine",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "blob.expand",
		Description: "prompt blob-token expansion (`blob expand`): @blob(<hex>) → the blob's absolute path on the serving daemon; @@blob() escapes to a literal; a token referencing no blob is a LOUD error (never a silent passthrough). Routes per --machine",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "fs.list",
		Description: "generic filesystem listing (`fs list --path`): the immediate SUBDIRECTORIES of an allow-listed home-rooted path on the serving daemon, ~-relative, dirs only; a path outside home (incl. ../ traversal) is a LOUD 403. Routes per --machine. Powers the Obsidian new-thread cwd pickers on mobile",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "plugins.run",
		Description: "plugin command-provider substrate (`plugins list`/`plugins run`): manifests at <SESH_HOME>/plugins/*.toml declare commands the daemon runs ON ITS OWN HOST — a LIST capability (JSON output mapped to {id,label,groups,path} via templates) and ACTION capabilities (form fields substituted as ARGV, never a shell string → no injection). Commands come from the manifest only, never the client; bad requests (unknown capability, missing required field, nonzero exit) fail LOUDLY. Routes per --machine. Powers box groups in the cwd picker + create-box from the app",
		Localities:  bothLoc,
	})

	// ---- daemon / API ----
	Register(Feature{
		ID:          "daemon.lifecycle",
		Description: "daemon start/stop/status",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.spawn-mode",
		Description: "[spawn] launch policy: yolo/default/sandbox per agent (+args passthrough, --yolo/--sandbox overrides, --msg ready-send); observable on the real process argv; pi sandbox loud; LOCAL-only (the owner's config; routing exercises the same path)",
		Agents:      agentic,
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "thread.meta",
		Description: "arbitrary per-thread KV (meta set/get/unset/list); feeds [[tui.views]] meta.<key> predicates; missing keys loud",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.adopt",
		Description: "adopt a manually-launched agent (work-server panes only): true identity via argv/RPC socket/rollout; every ambiguity loud; LOCAL-only by design (pane ids are per-server)",
		Agents:      agentic,
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "thread.adopt-headless",
		Description: "adopt (no pane): register an EXISTING, not-running conversation (--session-id + --agent) as a durable headless thread; a send-headless turn RESUMES that real conversation (continuity); missing id/agent loud",
		Agents:      agentic,
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "thread.fork",
		Description: "new --fork-from [--message-id N]: branch a conversation's prefix under a fresh session id (headless-born, resumes from the branch point); the source is untouched",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.backup",
		Description: "backup/restore/copy: sha256-idempotent transcript backups into portable SQLite; restore --to-dir (all agents) / --native (claude; others reported Unsupported); copy composes them; remote = routed",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.subscribe",
		Description: "subscriptions: a subscribee's completed turns deliver into subscriber threads (owner-side engine on the eventer; dedup on the monotone reply count; cycle guard + --allow-cycle breaker); remote = cross-machine delivery via the routed send",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.transcript",
		Description: "the transcript-resolution layer (D0): owner-side locate+read of the agent's own conversation file + last-reply extraction with a monotone count; remote = routed (content never replicated)",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.delegate",
		Description: "sesh delegate: ephemeral one-shot headless worker — spawn, ask, await, print, ARCHIVE (retained-not-deleted; the contract holds on failure too); --keep leaves it active; --machine routes the whole verb",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.await",
		Description: "sesh await: block until a real turn completes (busy→idle) by polling the LOCAL mesh view — a peer's thread awaits with no forwarding; --timeout loud on expiry",
		Agents:      agentic,
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.notify",
		Description: "per-thread notification gate: [defaults] notifications at creation, thread notify --on/--off, hooks receive SESH_NOTIFY (policy stays in the hook)",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.hold",
		Description: "park a thread until a future instant (thread hold --until/--until-unix/--clear); the owning daemon derives `on_hold` against its clock so it auto-expires; the TUI's default view hides on-hold threads, the `on hold` view shows them",
		Localities:  bothLoc,
	})
	Register(Feature{
		ID:          "thread.import",
		Description: "sesh import --from-v1: bring v1 records into v2 (uuid→id+session_id so resume works; per-machine; tags/parent/archived/created mapped; idempotent; --dry-run); transcripts untouched",
		Localities:  []Locality{Local},
	})
	Register(Feature{
		ID:          "daemon.doctor",
		Description: "sesh doctor: client checks (binary, config parse, SESH_MACHINE) + DAEMON-side checks (agents on the daemon's $SHELL PATH — the deploy-env failure class — tmux, peers); fail = non-zero exit",
		Localities:  []Locality{Local},
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
