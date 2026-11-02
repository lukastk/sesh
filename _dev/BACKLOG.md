# sesh v2 — backlog (designed, not yet built)

Features we've decided to build but deferred. Each must land as honest matrix cells per
`../AGENTS.md` (real agent / real tmux / real ssh; no mocks; loud errors). Build order
suggestion: #1 (small) then #2 (medium).

---

> **STATUS:** #1, #2, #3 are DONE (built + matrix-green). #4 (myrig integration) remains.

## 1. ✅ DONE — `nav --in-client`: enter a thread in the CURRENT tmux client (same-socket, no cockpit)

**Want:** in `sesh tui`, pressing Enter on a thread that is on the LOCAL machine and on the
tmux socket you're already attached to should just switch your current client to that
thread's session — no cockpit required. (Cross-machine still uses the full `sesh master` path.)

**Why it's easy:** it's exactly the *inner half* of `nav`, which already exists.
- `internal/tmux/nav.go` `InnerSwitchScript(socket, session)` = `tmux -L <socket>
  switch-client -t =<session>` (+ a "kick" fallback that creates a client when none exists).
- `cmd/sesh/tmux.go` `tmuxNav` currently ALWAYS does the outer step first
  (`master.SelectWindow(machine)` on `cfg.MasterSocket`) — that's the part needing the cockpit.
- `internal/tui/model.go` `navSelected()` execs `sesh tmux nav --to <machine>:<session>`.

**Design:**
- Add `tmux nav --in-client`: do the **inner switch only**, and error LOUDLY if `$TMUX`
  isn't a client on the target socket (no silent no-op). Reject if target machine != self.
- TUI on Enter: if `row.Machine == cfg.Machine` AND the TUI's own `$TMUX` socket path matches
  the thread's socket → exec `nav --to <m>:<s> --in-client`; else fall back to the cockpit
  path (or, until the cockpit exists, say so plainly rather than a cryptic
  outer-select error).
- The TUI knows its socket: `$TMUX` = `<socket-path>,<pid>,<session>`; compare basename to
  `cfg.TmuxSocket`.

**Matrix:** a `tmux.nav-in-client` (Local, agnostic) cell — attach a client to the sandbox's
tmux socket, create two threads, switch-client to one via `--in-client`, assert the client's
active session flipped (and assert a loud error when run outside a client / for a remote target).

**Effort:** small.

---

## 2. ✅ DONE — `thread headful`: promote a live HEADLESS thread to HEADFUL (and enter it)

**Want:** select a headless thread in the TUI → Enter → it gets a real tmux pane (agent
resumed, conversation intact) → you're dropped into it. Combined with #1, Enter on a headless
thread = promote-then-enter.

**Status today:** NOT supported. Headless vs headful is fixed at `thread new --headless`.
Subcommands (cmd/sesh/thread.go): new/list/stop/pane/status/send/send-headless/headless-reply/
rename/tag/archive/delete/resume/grid/snapshot — no promote/headful/convert.

**Why it's buildable:** it's `resume` applied to a *live* headless thread. `resume` already
recreates a tmux session and relaunches the agent with `--resume <session id>` (continuity
verified for all 3 agents; claude needs the clean env from `agents.ScrubHarnessEnv`). The
headless thread already stores `agent_session_id`. So: spawn the agent in a pane resuming that
id, flip the record to headful (drop the headless flag / give it a SessionName window).

**Busy handling (Lukas asked):** a headless thread tracks an in-flight turn
(`internal/daemon/headless.go` `d.hlInFlight[id]`); `send-headless` against a busy thread
already returns a loud **409 Conflict** ("a turn is already in flight for this thread"). So
promote must do the same: if a turn is in flight → **reject with 409** (or optionally block
until it finishes), never spawn a pane mid-turn. Mirror the existing no-silent-race pattern.

**Open design Qs:**
- After promotion, is the thread permanently headful, or can it go back to headless? (Probably
  one-way for now; reverse = `stop` then keep as headless record — TBD.)
- codex edge: a codex headless thread killed before its first turn has no minted session id
  (existing N/A `TestCodexResumeBeforeFirstTurnIsNA`) — promotion before the first turn is the
  same N/A; surface it, never fake.

**Matrix:** `thread.headful` across (local, remote) × (claude, codex, pi) — promote a headless
thread, assert a real agent lands in a real pane with conversation continuity (resume worked),
and assert promotion of a BUSY thread is rejected with a conflict (both directions). codex
before-first-turn = justified N/A.

**Effort:** medium. Reuses the `resume` machinery.

---

## 3. `sesh master`: cockpit infrastructure in Go (full design in `_dev/MASTER.md`)

Move ALL cockpit infrastructure into sesh — building the cockpit server, the per-window
ssh-attach, and the reconnect/self-heal loop — as `sesh master up [--machines …] [--tmux-conf]`
/ `sesh master window <machine>` (a Go supervisor, not a shell loop) / `sesh master attach` /
`sesh master down`, machines sourced from the daemon peers. myrig collapses to a tmux conf +
thin `mms-*` aliases/pickers/clipboard wrappers (no orchestration, no shell-sourcing — CLI
boundary only). Conventions (window-name == machine, window attached to the work socket)
become sesh-internal. Conformance: `master.up` (windows built + genuinely attached to each
machine's work socket) + `master.reconnect` (kill an attach → supervisor re-establishes),
real processes, ssh-localhost for "remote". **See `_dev/MASTER.md` for the full spec** (incl.
the function-by-function disposition of myrig's `master-tmux.sh`). Effort: medium-large.

## 4. myrig integration (cross-repo: `~/mysetup/myrig`) — NOT in this repo's code

Tracked here for completeness; the work lands in the myrig repo, talking to sesh over the
CLI boundary only (no shell-sourcing of sesh internals).

**4a. ✅ DONE (mymain + macbook) — deploy sesh-v2 durably.** Both run `sesh-v2-daemon`
under supervisor (autostart on reboot), API on tailscale:7878 + token, managed peers.json,
native build (auto-signed). Verified: cross-machine mesh syncs over http. `home/.env` is
TRACKED + SHARED across machines (not per-machine — my initial revert was wrong); the
`SESH_V2_API_TOKEN` is committed to it (reconciled via macbook's union merge a8c84ef; both
machines now at a8c84ef, trees clean). macbook's first deploy used scp'd rendered files (its
myrig was behind); its myrig is now up to date, so a normal `myrig` run reproduces it.
macstudio not yet deployed (needs a `myrig` provision — it clones+builds+supervisors via
mysetup.sh, and home-install distributes the shared token). Original plan below.

**4a (original plan). Deploy sesh-v2 durably (make the manual bring-up reproducible/persistent).** Currently
mymain↔macbook run hand-started daemons + a hand-written `~/.myrig/zshenv/sesh-v2.sh` wrapper
+ a manually-built master. Replace with:
- `setup/installs/sesh-v2/` — build+copy the binary per-arch (separate repo `sesh-v2.git`, so
  build-and-scp, NOT `go install` — its module path resolves to the OLD repo). **macOS/Apple
  Silicon gotcha:** a cross-compiled (Linux→darwin/arm64) binary scp'd OVER an
  existing/running one invalidates its code signature → the kernel SIGKILLs every invocation
  (exit 137, no output). The install MUST, on the Mac, `codesign --force --sign - <bin>` after
  copy (or `rm` then copy for a fresh inode, or build natively). Verified the hard way
  2026-06-09.
- a supervisor program (mirror `^sesh^sesh-daemon.ini.jinja`) running `sesh-v2 daemon run`
  with `SESH_API_ADDR={{tailscale}}:7878`, token from `~/.env`, isolated `SESH_HOME`.
- `home/.sesh-v2/peers.json.jinja` templated from `config.toml` (each peer `--api-addr` +
  token + ssh fallback).
- `SESH_V2_API_TOKEN` in `~/.env` (already manual; formalize).
- `home/.myrig/zshenv/sesh-v2.sh.jinja` — the wrapper (replaces the manual file; per-machine
  `SESH_MACHINE`, sets `SESH_TMUX_SOCKET`/`SESH_MASTER_SOCKET`, tui defaults to
  `--all-machines`). **Remove the manual `~/.myrig/zshenv/sesh-v2.sh` on mymain+macbook** so
  the managed one isn't shadowed.

**4b. Collapse myrig's `master-tmux.sh` to config + aliases (and DELETE the old orchestration).**
Per `_dev/MASTER.md §4–5`, once `sesh master` (#3) exists: keep only the `.tmux` conf
(prefix/keybindings/look) + thin `mms-*` aliases → `sesh master …` + fzf pickers
(`fzf | sesh tmux nav`) + clipboard wrappers. **Delete**: the SSH poller, all `~/.cache/mms/*`
caches, `_mms_machine_reachable`, nav history, flagged-cycle, `_mms_run_on_machine`, and the
`mms-machine-session-loop` reconnect loop (superseded by `sesh master window`). `master-tmux.sh`
goes ~1,205 → ~150–250 lines. (Parallel to v1 for now; converge onto the canonical socket +
single master once v2 retires v1.)

## 5. ✅ DONE — Per-server tmux config for the WORK server (`SESH_TMUX_CONF`)

Built: `SESH_TMUX_CONF` (config) → the work `tmux.Server` is `NewServerWithConf`, which
prepends `-f <conf>` to every invocation (sourced at server start, ignored when running —
verified). The supervised daemon is the sole creator of work-server sessions. When a master
window finds an empty peer socket it invokes that peer's `sesh tmux create-session`, which
delegates creation to the peer daemon; the attach shell never starts raw tmux. This removed
the redundant `peers.Peer.TmuxConf` / `peer add --tmux-conf` surface. Conformance
`tmux.work-conf` proves the supplied config is live, and `master.remote-work-context` proves
an SSH master window still gets a daemon-born work server. myrig owns the conf FILE; sesh
loads the path. Design notes kept below.

Generalize the master's `--tmux-conf` (`-f`, already built) to the WORK server, so sesh's
tmux can carry the sesh-specific UI (the per-thread status bar) while the user's REGULAR
tmux (default `~/.tmux.conf`) does not. Motivation (Lukas): post v1→v2 migration, the
thread-status bar (`status-format[0]` → `sesh-current-status`) should live ONLY in sesh's
work tmux, not in plain tmux — separate confs is the clean seam. (Today the work server
loads the default `~/.tmux.conf`, which is why base's `status 2` second line shows up as a
gray bar on the `sesh-v2` socket.)

Design:
- `SESH_TMUX_CONF=<path>` (config): the work tmux server's config. When set, the work
  server is started with `tmux -f <path>` instead of the default `~/.tmux.conf`.
- Apply only at daemon-owned server start: `internal/tmux.Server` has an optional conf and
  prepends `-f <conf>` (harmless once the server is running — tmux ignores `-f` when
  connecting). The daemon passes `cfg.TmuxConf` when creating thread sessions. Master
  windows request `scratch` through the target daemon, so no peer-side config duplication is
  needed.
- Caveat (same as the master): `-f` REPLACES base, so the supplied work conf must `source`
  the user's base bits it wants (keybindings, etc.) + add the sesh status line, or be
  self-contained. myrig owns the conf file; sesh just loads the path it's given.
- Conformance: `tmux.work-conf` asserts the server loaded a sentinel from the supplied conf;
  `master.remote-work-context` uses real SSH and a daemon-only sentinel to assert the target
  daemon created the server.

### Compose
TUI Enter becomes: headed thread on current socket → #1 switch in place; headless thread →
#2 promote then #1 enter; anything cross-machine → the cockpit nav path.

## 6. Mesh scale pass, stage D — client-side deltas + serve-from-rows (designed in `_dev/MESH_SCALE.md`, NOT built)

Stages A–C of the scale pass shipped 2026-08-29 (per-thread peer cache rows, the
diff-fed eventer, the O(live) maintainer sweep, the hash reconcile). What remains
O(total) is the LAST hop and the in-memory copies:

- **`/v1/mesh?since=`** — the TUI/sidebar/sesh-ui poll the merged view every ~3s and
  receive + re-decode the whole mesh each time (~1.5 MB at today's ~2k threads). Give
  it the same cursor treatment `/v1/snapshot` has: full set once, per-machine deltas
  after (additive api field + schema bump; old clients keep full reads; sesh-ui can
  adopt it). The daemon side already keeps exactly the per-machine change stream this
  needs (the view's transitions). This is what makes a 50k-archived-thread TUI cheap.
- **Serve from rows, RAM O(live)** — the daemon's `meshView` and the maintainer's `m.st`
  each hold every replicated / local snapshot decoded in RAM (~2–4 MB now, ~15–20 MB
  at 10k, more at 100k). Once `/v1/mesh` is delta-served, the full set only needs to
  be marshaled on a client's first fetch: serve it from `peer_threads` rows with a
  per-machine marshal cache keyed by the view epoch, and evict settled local
  snapshots from `m.st` to the store.
- **Daemon-side archived search** — with rows in SQLite, `search through 50k archived
  threads` becomes a query (generated columns / FTS over `peer_threads.snapshot`)
  instead of a client-side filter over a downloaded world.

Trigger for building it: the TUI on the phone measurably paying for the full-set poll
(the A/B left the daemon at 3.2 % of a core with a TUI-shaped poll; the CLIENT's own
decode is now the larger half), or the mesh passing ~10k threads. Phone MEMORY is not a
trigger for THIS item: the phone's Termux-side memory problem turned out to be leaked
ssh-agents from the status line (item 7 below), not the daemon's RAM, which is ~27 MB PSS
(H108 follow-up 3, MESH_SCALE.md §8). Matrix: a
`mesh.client-delta` cell over the counting proxy (the `mesh.delta-sync.http` pattern)
+ a scale-guard cell seeding thousands of archived virtual records and asserting the
work counters (`sweptThreads`, `rowsWritten`) stay O(live/Δ).

## 7. ✅ DONE — Work-server status line without a login shell per redraw (designed 2026-09-15 from the termux ssh-agent leak; BUILT 2026-09-16 as the daemon-maintained pane options — `_dev/STATUS_OPTIONS.md`, matrix row `tmux.status-options`)

Today the work server's top status row is `#(zsh -lc 'sesh-current-status #{pane_id}
#{socket_path}')` (myrig `tmux.work.conf`). Measured 2026-09-15 with 0.1 s sampling and
`status-interval` at its default 15: tmux spawns that job **about once per second per
attached client** (phone, 1 client: 22 in 20 s; mymain, 4 clients: 12 in 20 s), not once
per interval — each spawn a login zsh sourcing all of myrig's shell.sh (~0.9 s wall on the
phone, H99). On termux that fork is what leaked ~2,000 ssh-agents (H108 follow-up 3);
everywhere it is constant CPU, page-table and battery churn for a line that changes on
human timescales. Why tmux re-runs the job that often is not established — measured only.

Two shapes, both sesh mechanism with myrig keeping the rendering policy:

- **Daemon-maintained pane option (preferred).** The maintainer already resolves every
  marked pane's thread and state per tick (`refreshThread`); on change it sets user
  options on the pane — the raw fields (`@sesh_thread_name`, `@sesh_flagged`, …) for
  myrig to format, or one rendered `@sesh_status` — and the conf reads `#{@sesh_status}`:
  a format lookup, zero forks. Unmarked panes carry nothing (the row goes blank, as it
  does today for a non-thread pane). Cost: one `set-option -p` per state change per marked
  pane, sent on the batched command list the maintainer already issues (H102's
  `CapturePanes`). A stale option after a daemon restart is a real edge: clear or rewrite
  every marked pane's options on the first full sweep.
- **A Go subcommand instead of a shell (fallback).** `#(sesh tmux status #{pane_id}
  #{socket_path})`: one exec of the binary, no shell startup, no zshenv side effects — the
  smaller change, still ~1 fork/s per client.

Either way the myrig status conf changes in the same deploy. Matrix: a `tmux.status-line`
claim asserting the rendered row for a real marked pane over a real work server (local and
via the real ssh hop), plus a counter proving zero status-job spawns across N forced
redraws. Trigger: as soon as the termux agent fix has landed — the leak is the urgent half,
this is the durable half.

## 8. Scheduled work — timed messages into a thread + timed spawns (`sesh schedule`; designed 2026-09-16 in `_dev/SCHEDULING.md`, NOT built; ticket 28f6e77b)

sesh has no clock-driven input: every existing mechanism fires on a user action or an observed
state edge. The design is ONE record with two actions — `message` (deliver text into an existing
thread, on a cron/interval/one-shot) and `spawn` (create a thread in a cwd with a prompt) — sharing
the clock, catch-up policy, bounds, outcome history, CLI family and, load-bearingly, the guard
vocabulary over the existing state axes: `--when-headless {turn|revive|skip}`, `--when-busy
{skip|send}` (the heartbeat rule), `--if idle,detached,…`, `--idle-for`, hold/archived skipped by
default, a typing-collision guard on pane pastes (H48's `attached_activity_unix`). Spawn adds the
overlap guard (`--if-previous skip`), `--on-turn-end stop+archive` via the eventer's busy→idle
edge with a persisted `schedule_runs` row, auto-flag disabled on scheduled runs, and `--parent` onto
a virtual thread to group runs. Owned by the machine that executes it (the target thread's owner /
the spawn machine), owner-local table like tickets/subscriptions, NOT in the mesh snapshot. Build
order: phase 0 = the `respect-typing` wait-not-drop guard on EVERY pane delivery (`thread send`,
`ticket send-prompt`, subscriptions — a live collision today, no schema, ships first), then engine
+ `message` (the half only sesh can do), `spawn` second (cron + `delegate` already covers ~80 %),
TUI/doctor/hook-events third. ~24 matrix cells across six new rows. Decided 2026-09-16: both
actions, message first; `--idle-for 60s` implied by `--if idle`; `respect-typing` on by default;
hand-rolled cron parser; spawn defaults headless + `--on-turn-end keep`; closed guard keyword
list; the two TUI snapshot fields in phase 3; spawn-mode disclosure only; no broadcast. **Fully
decided — ready to build** in the `SCHEDULING.md` §14 order.

## 9. ✅ DONE — Thread models — show, set, apply, and per-harness defaults (designed and BUILT 2026-11-02, wire schemas 58–59; config.local.toml and the myrig split landed the same day)

**Goal.** The TUI shows each thread's model by default; one command sets a thread's
**registered** model, another sets it **and** switches the live agent; the TUI does
both through an fzf-style picker over the models the harness can actually use; and
each harness gets a default model, settable from the CLI and the TUI.

**Two values, never conflated.**
- *Registered* = `threads.model` (store migration 16). Sesh must store it: it is what
  spawn, resume and every headless turn pass as `--model`. Today it is set only by
  `thread new --model`; on mymain 80 of 106 threads have it empty.
- *Actual* = what the agent last ran, read from the harness's own session file —
  derived, never stored, never guessed:
  - claude: `message.model` on assistant entries (resolved id; the `[1m]` suffix is
    lost, so `opus` and `opus[1m]` look alike). A typed `/model` also leaves a
    `<command-name>/model</command-name>` entry.
  - codex: `turn_context.model` per turn. The rollout is written lazily, so a switch
    shows only after the next turn and a turnless thread has no file.
  - pi: a `model_change` entry at the moment of the switch, plus `provider`/`model`
    on assistant messages.
  A thread with no turns has no actual model: show nothing, not a default. The
  column shows the actual model and marks a mismatch with the registered one.

**Listing usable models (the picker's source).**
- claude: no CLI listing. Pipe one
  `{"type":"control_request","request_id":"r1","request":{"subtype":"initialize"}}`
  into `claude -p --input-format stream-json --output-format stream-json --verbose
  --no-session-persistence`; the `control_response` carries `models[]` with
  `value` (what `--model` takes), `resolvedModel`, `displayName`, `description`,
  `supportedEffortLevels`. No model turn runs. Caveat: this headless list had 5
  entries (default = Sonnet 5.5) while the interactive `/model` picker showed 13
  (default = Fable 5.1) on the same account, so the picker must also accept a typed id.
- codex: `codex debug models` → `{"models":[{slug, visibility, …}]}`; offer
  `visibility == "list"`. It refreshes from the server (`--bundled` = offline
  catalog). It sits under `debug`, so treat its shape as unstable and fail loudly.
- pi: `pi --list-models [search]` (text table, only authenticated providers; ids are
  `provider/id`), or RPC `get_available_models` on a live session.

**Commands.**
- `thread set-model --id <id> --model <m>`: record only; takes effect at the next
  spawn, resume or headless turn.
- `thread set-model --id <id> --model <m> --apply`: record, then switch the live agent:
  - headless (all harnesses): nothing further.
  - codex pane: type `/model <m>`. Verified session-only on codex-cli 0.165.0
    ("Model changed to … for this session only"; config.toml gained no model).
  - pi pane: RPC `set_model {provider, modelId}` over sesh's existing per-session
    socket bridge (`internal/daemon/rpc.go`). Pi 1.4.0's RPC path calls
    `session.setModel(model)` without `persist`, so no global default is written
    (read in code, not yet run live).
  - claude pane: **restart the pane** on the new `--model`, resuming the transcript.
    It kills the claude process and any in-flight turn (the conversation survives),
    so refuse a busy thread without `--force` and say so plainly. Do NOT type
    `/model <m>`: on Claude Code 2.1.316 it always writes `"model"` into the global
    `~/.claude/settings.json` ("…and saved as your default for new sessions"). Only
    the interactive picker's `s` key is session-only, and driving it is keystroke
    automation that breaks as the list changes.
- TUI: both actions behind one key, model chosen from the picker above.

**Per-harness defaults.** A `model` key under `[spawn.<agent>]` in
`~/.sesh/config.toml` (per machine today; fleet-wide once myrig symlinks the
file, below), applied at thread
creation when no `--model` is given and recorded as the thread's registered model, so
new threads never have an empty one. Settable by a CLI command and from the TUI
picker. Not via `[spawn.<agent>] args`: that would pass `--model` twice next to a pin.
Sesh never edits the harnesses' own config files. The writer must edit that one key
in place: config.toml is heavily commented, and re-encoding it with the TOML library
(as `ui_config.toml`'s writer does) would strip the comments. It must also write
THROUGH a symlink, never replace it: resolve `~/.sesh/config.toml` to its target and
do the temp-file + rename in the target's directory. A rename onto the link path
would turn the link into a plain file and silently detach this machine (see below).

**Follow-up in myrig — DONE 2026-11-02** (sesh `06c1e18`, myrig split): ~/.sesh/config.toml is now a symlink to myrig's plain home/.sesh/config.toml and the per-machine parts render into ~/.sesh/config.local.toml. Arrays REPLACE (the rule chosen), so mymain's volume [[session_name]]/[[cwd_label]] rules moved into the shared file, where they are inert off mymain. The original note follows. `~/.sesh/config.toml` is currently rendered
from `~/mysetup/myrig/home/.sesh/config.toml.jinja` by `scripts/install-home.py`, so a
provisioning run overwrites a default set from sesh. The plan is to make myrig manage
the file by symlink instead of rendering it, so a default set on one machine reaches
the others when myrig syncs. Today's template has machine-specific parts (e.g.
`[browser_api]` is rendered on mymain only), which the symlinked layout has to
accommodate.

Proposed shape for those machine-specific parts (a separate feature, after this one):
a `config.local.toml` that overrides `config.toml`. myrig RENDERS the local file per
machine and SYMLINKS the shared one. To decide when building it: the merge rule
(key-by-key within tables is the natural default; arrays of tables such as
`[[cwd_label]]` need an explicit replace-or-append rule), and what the default-model
writer does when `config.local.toml` sets the same key — it writes to the shared file,
so it must refuse or say loudly that the local file shadows it on this machine, never
report a setting that does not take effect.
