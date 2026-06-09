# AGENTS.local.md — sesh v2 working notes

## Build status (post-ops + TUI)

Feature matrix: **93 cells, 91 green, 2 skip** (the 2 skips = `thread.resume/claude/{local,remote}`,
pending Lukas's decision — see below). Plus a separate **TUI conformance track: 6/6
claims green** + structural import rule + completeness gate. `go test ./...` green
except the 2 honest claude-resume skips.

### Features added after the first all-green (ops phase + TUI)
- Lifecycle ops (matrix): **rename, tag, archive** (park, record kept), **delete**
  (drop record, leave runtime — distinct from kill), **resume** (revive a dead headed
  thread: recreate session + relaunch with `--resume`; pi+codex green w/ continuity).
- **thread.list-all** (`?all-machines`) + **thread.grid** (`?with-status`, concurrent)
  — daemon-side mesh fan-out; the TUI's data source.
- **TUI** (`sesh tui`, Bubble Tea): live grid (glyphs from runtime-state), poll-first,
  `--all-machines` fan-out; actions x kill / a archive / enter nav. Thin client over
  `internal/client` only (import rule enforced by a test). Tests drive the real Model
  against a real daemon and assert reality-anchored, never golden blobs.

### OPEN: thread.resume for claude (2 skip cells, needs Lukas decision)
Interactive (headed) claude buffers its transcript in memory and flushes a resumable
session only on a GRACEFUL exit. A hard-killed headed claude session leaves only an
`ai-title` (124 bytes) on disk (verified) — `claude --resume <id>` then says "No
conversation found". pi/codex persist INCREMENTALLY so they resume fine. Recommended:
mark claude-resume a justified N/A. (claude's headed turns DO run + persist-on-exit;
the other claude cells are honest.)

codex resume edge: a codex thread killed BEFORE its first turn has no minted id →
explicit N/A error (TestCodexResumeBeforeFirstTurnIsNA), never faked.

## Build status: ALL GREEN — 76 / 76 cells, 0 fail, 0 skip, 0 missing (original matrix)

`go test ./...` green. Run `go run ./cmd/sesh matrix grid` (after
`go test ./internal/conformance`) to see the grid.

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
