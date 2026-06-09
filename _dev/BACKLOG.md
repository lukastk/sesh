# sesh v2 — backlog (designed, not yet built)

Features we've decided to build but deferred. Each must land as honest matrix cells per
`../AGENTS.md` (real agent / real tmux / real ssh; no mocks; loud errors). Build order
suggestion: #1 (small) then #2 (medium).

---

> **STATUS:** #1, #2, #3 are DONE (built + matrix-green). #4 (myrig integration) remains.

## 1. ✅ DONE — `nav --in-client`: enter a thread in the CURRENT tmux client (same-socket, no master)

**Want:** in `sesh tui`, pressing Enter on a thread that is on the LOCAL machine and on the
tmux socket you're already attached to should just switch your current client to that
thread's session — no master-tmux required. (Cross-machine still uses the full master path.)

**Why it's easy:** it's exactly the *inner half* of `nav`, which already exists.
- `internal/tmux/nav.go` `InnerSwitchScript(socket, session)` = `tmux -L <socket>
  switch-client -t =<session>` (+ a "kick" fallback that creates a client when none exists).
- `cmd/sesh/tmux.go` `tmuxNav` currently ALWAYS does the outer step first
  (`master.SelectWindow(machine)` on `cfg.MasterSocket`) — that's the part needing master-tmux.
- `internal/tui/model.go` `navSelected()` execs `sesh tmux nav --to <machine>:<session>`.

**Design:**
- Add `tmux nav --in-client`: do the **inner switch only**, and error LOUDLY if `$TMUX`
  isn't a client on the target socket (no silent no-op). Reject if target machine != self.
- TUI on Enter: if `row.Machine == cfg.Machine` AND the TUI's own `$TMUX` socket path matches
  the thread's socket → exec `nav --to <m>:<s> --in-client`; else fall back to the master
  path (or, until master-tmux exists, surface "master-tmux not set up" rather than a cryptic
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

## 3. `sesh master`: master-tmux infrastructure in Go (full design in `_dev/MASTER.md`)

Move ALL master-tmux infrastructure into sesh — building the master server, the per-window
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

**4a. Deploy sesh-v2 durably (make the manual bring-up reproducible/persistent).** Currently
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

**4b. Collapse myrig's master-tmux to config + aliases (and DELETE the old orchestration).**
Per `_dev/MASTER.md §4–5`, once `sesh master` (#3) exists: keep only the `.tmux` conf
(prefix/keybindings/look) + thin `mms-*` aliases → `sesh master …` + fzf pickers
(`fzf | sesh tmux nav`) + clipboard wrappers. **Delete**: the SSH poller, all `~/.cache/mms/*`
caches, `_mms_machine_reachable`, nav history, flagged-cycle, `_mms_run_on_machine`, and the
`mms-machine-session-loop` reconnect loop (superseded by `sesh master window`). `master-tmux.sh`
goes ~1,205 → ~150–250 lines. (Parallel to v1 for now; converge onto the canonical socket +
single master once v2 retires v1.)

### Compose
TUI Enter becomes: headed thread on current socket → #1 switch in place; headless thread →
#2 promote then #1 enter; anything cross-machine → the master-tmux nav path.
