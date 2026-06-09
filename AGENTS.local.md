# AGENTS.local.md — sesh v2 working notes

## Build status: ALL GREEN

Feature matrix: **100 cells, all green** (on branch `mesh-replicated-state`). Separate
**TUI conformance track: 8/8 claims green** (+ `mesh-render-offline`) + structural
import rule + completeness gate. Plus **`TestRealCrossHost`** (real network ssh hop
mymain↔macbook, all 3 agents) — env-gated/skip-able. `go test ./...` green.

### Mesh / live cross-machine state (branch mesh-replicated-state) — the killer feature
Design in `_dev/MESH.md`. Three decoupled loops:
- **L1 maintainer** (`internal/daemon/maintainer.go`): per-daemon background rolling
  probe → every local thread's live state is O(1) to read (`/v1/snapshot`, `thread.snapshot`).
  Grid reads it (`d.maint.stateOf`).
- **L2 mesh sync** (`internal/daemon/meshsync.go`): pulls each peer's snapshot over
  multiplexed ssh into a SQLite cache (`peer_snapshots`, migration 6); `/v1/mesh` serves
  the merged view locally (`mesh.snapshot`, `mesh.offline-listing`). Offline peer →
  reachable=0, last-known retained.
- **L3 TUI**: `sesh tui`/`sesh mesh` render the merged view with per-machine staleness.
- **Phase C — network API** (`internal/daemon/apiserver.go`): `SESH_API_ADDR` exposes the
  SAME full router over TCP behind a bearer token (`SESH_API_TOKEN`); refuses to run
  exposed without a token. Client is transport-agnostic (`client.NewRemote`); CLI targets
  a remote daemon via `SESH_REMOTE`. Parity by construction + tested (`api.tcp-auth`,
  `api.tcp-parity`). For mobile: bind to the tailscale interface; a phone hits `/v1/mesh`.
- `peers.Peer` has a `Port` field (`peer add --port`) → non-22 machines reachable
  (`SSHArgs()` at every ssh site).

### Hybrid daemon↔daemon transport (Stages 1+2+3 DONE: SYNC + ROUTING + live FAN-OUT over http)
The transport is EXPLICIT per peer (`peers.Peer.Transport()` → "http" if it has an
`ApiAddr`, else "ssh"), NOT an automatic fallback — an http failure is loud, never a silent
ssh downgrade. `peer add --api-addr <host:port> --api-token[-file]` opts a peer into http;
`peer list` shows the transport. ssh stays the default + bootstrap/admin transport.
- **Stage 1 — sync**: `fetchPeerSnapshot` branches in `internal/daemon/meshsync.go`
  (`fetchPeerSnapshotHTTP` reuses a per-peer `client.NewRemote` for keep-alive across the
  1s ticks). Cells `mesh.snapshot`(.http), `mesh.offline-listing`(.http).
- **Stage 2 — routing**: `cmd/sesh/route.go routeMachine(cfg, machine, rest) (handled,err)`
  — http peer + `httpRoutable(rest)` ⇒ set `SESH_REMOTE`/`SESH_API_TOKEN`, return
  handled=false → `main` drops `--machine` and dispatches locally (daemonClient hits the
  peer's API). ssh peer / carve-out ⇒ `routeToMachineSSH`, handled=true. Ticket-owner
  auto-routing (`cmd/sesh/ticket.go`) uses the same path (reloads cfg after the http
  branch; does NOT re-enter the owner check → no loop). **Carve-outs stay ssh** (`httpRoutable`
  returns false): `daemon` lifecycle, `tmux nav`, `tmux stage-file`. Cells `route.parity`(.http).
- **Stage 3 — live fan-out**: `internal/daemon/{fanout,grid}.go fetchPeerThreads/
  fetchPeerGrid` branch on `Transport()` (http → `peerRemoteClient(p).ThreadList/ThreadGrid`).
  So `thread list --all-machines` / `thread grid --all-machines` reach an http peer over
  its API. An http-only peer is now first-class on EVERY cross-machine path. Cells
  `thread.list-all`(.http), `thread.grid`(.http).
- **Parity is matrix-enforced**: every `.http` twin shares the ssh body (`meshTransport`
  param) and registers the peer with a **broken ssh dest** (`http-only.invalid`), so a green
  http cell PROVES HTTP carried it (a silent ssh fallback would fail). 106 cells total.

### Lifecycle verbs (post-refactor): orthogonal primitives, no `kill`
`kill` was split into `stop` + `delete` (it was the non-atomic composite `stop && delete`;
the composite belongs in myrig). Two axes:
- **runtime**: `stop` (end agent + tmux session, keep record → dead/resumable) ↔ `resume`.
- **record**: exists until `delete`; `archive`/`unarchive` toggle visibility.
- `delete` refuses a LIVE thread unless `--force` (else it orphans the agent).
- Matrix: `thread.stop` (6 agent cells), `thread.delete` (guard + force), `thread.resume`
  (all 3 agents, continuity). TUI: `x` stop, `d` delete, `a` archive, enter nav.

### RESOLVED: the claude-resume saga was an ENV LEAK (not a claude limitation)
When the daemon is started from inside an agent session (autonomous build / the
conformance suite under Claude Code), it inherited `CLAUDECODE=1` / `IN_CLAUDE_CODE` /
`CLAUDE_CODE_SESSION_ID` / `CLAUDE_CODE_*`. Those leak into the spawned claude, which then
behaves as a NESTED session and stops persisting its transcript to `~/.claude/projects`
— so `claude --resume` reports "No conversation found". Old sesh never hit this (its
daemon runs normally, not under a claude agent). **Fix:** `agents.ScrubHarnessEnv()` at
the top of `daemon.New()` unsets those markers (verified: propagates daemon→tmux→pane;
hard-killed claude then resumes with full continuity). My earlier "claude buffers until
graceful exit" theory was WRONG — it was the env leak masquerading as that. Verified by
driving real claude in tmux via the `tui-tmux-testing` skill.

codex resume edge: a codex thread killed BEFORE its first turn has no minted id →
explicit N/A error (TestCodexResumeBeforeFirstTurnIsNA), never faked.

### mesh + cross-host
- `--machine` routing (real ssh hop), `thread.list-all` (`?all-machines`) + `thread.grid`
  (`?with-status`, concurrent) daemon-side fan-out (offline peers → `unreachable`, not
  dropped). TUI is a thin client over `internal/client` only.
- `TestRealCrossHost`: real-host spawn validation. PREREQ (manual): v2 at
  `~/.local/bin/sesh-v2` on both paired machines; run with `MYRIG_MACHINES` exported (it's
  a zsh assoc array). See AGENTS.md "Test environment notes".

### Test isolation (the user runs LIVE old sesh here)
Every sandbox isolates `SESH_HOME` / `SESH_TMUX_SOCKET` / `SESH_MASTER_SOCKET` /
`SESH_CODEX_HOME`; `sandboxEnv` strips inherited `SESH_*`. Never leave a socket/home at
its default in a test.

### Known follow-ups (not blocking)
- `peers.Peer` has no port field → non-22 machines (android-main:8022) are skipped by
  the cross-host test. Add a Port field + `ssh -p` to support them.
- Mesh list is LIVE fan-out only; SPEC hints at replicated/cached listing for offline
  browsing — not built.

## Reference: foundational decisions & gotchas (from the original 76-cell build)

Run `go run ./cmd/sesh matrix grid` (after `go test ./internal/conformance`) to see the
rendered grid.

Every feature is honest (real agent in a real tmux pane; remote = real `ssh
localhost` hop). All 24 feature rows green across their axes:
matrix spine; daemon + SQLite/WAL; tmux layer incl. **nav** (local + remote via
`--machine` routing); thread layer local+remote — new.headed/kill/list/resolve-pane,
runtime-state, send.headful, **new.headless + send.headless** — for all 3 agents;
ticket layer (create/list-by-thread/set-status/needs-input/send-prompt/ownership);
api.http-json; daemon.mesh-read.

### Things resolved that were once blockers

- **codex trust**: codex shows a per-directory "trust this dir?" prompt that ate
  input. Fixed by `agents.EnsureCodexTrust` — sesh writes `[projects."<dir>"]
  trust_level="trusted"` into codex's `config.toml` at spawn; `CODEX_HOME`
  (SESH_CODEX_HOME) lets tests isolate it with auth.json symlinked.
- **activity probe**: codex's thinking-phase animates only a ~1s timer; probe now
  EARLY-EXITS on working (fast) with a ~3s idle-confirm window.
- **send timing**: codex drops input when text+Enter are back-to-back → 250ms settle
  before Enter (tmux.SendText + test sendKeys).
- **headless**: stateless-per-turn (Lukas's choice). A headless thread is a durable
  conversation, no tmux window; a turn = `<agent> --print/exec --resume`; "working"
  = a turn process is in flight (daemon in-memory registry). pi is NOT N/A — it has
  `--print --session-id`. codex's session id is parsed from `codex exec --json`
  (thread.started) on the first turn.

## Key decisions baked in

- **runtime-state = two orthogonal axes** (activity from pane content-diff, attachment
  from `tmux list-clients`); needs-input = activity==waiting regardless of attachment.
  Lukas signed off (provisionally) in Phase 3b. SPEC §3/§4 updated. See the memory
  `sesh-v2-runtime-state-design`.
- **content-diff probe**: samples a pane 4× over ~1.14s, working iff a MAJORITY of
  intervals change (rejects one-off idle blips like claude's rotating hints / MCP
  startup; catches a real turn's animated spinner). All three TUIs animate while
  working AND are byte-stable when idle.
- **`--machine X` routing**: pseudo-global flag in cmd/sesh; main forwards the command
  (minus --machine, plus the peer's SESH_HOME/SESH_MACHINE) over a real ssh hop.
  Excludes meta commands (peer/matrix/help). This is the honest "remote" path.
- **ticket ownership**: SESH_TICKET_OWNER; ticket commands auto-route to the owner.

## Test gotchas learned the hard way

- A freshly spawned agent's pane is blank → byte-STABLE → content-diff misreads it as
  waiting. Always `waitThreadReady` (TUI rendered ≥3 non-blank lines AND activity
  waiting) before sending, or keystrokes are lost.
- `tmux display-message -t =session` silently returns empty for the `=` exact-match
  prefix → use `list-sessions`/`list-clients` and match the name in Go.
- tmux escapes control bytes in `-F` output (0x1f → literal `\037`); use a TAB field
  separator (passed through verbatim) and treat a wrong field count as a loud error.
- Nested `tmux attach` works headlessly via `env -u TMUX tmux attach` in a viewer
  session (used to test the attached state).
