# AGENTS.local.md — sesh v2 working notes

## Build status (as of the Phase 0–5 build)

Matrix: **57 / 76 cells green**, 0 fail, 0 missing. `go test ./...` green.
Run `go run ./cmd/sesh matrix grid` (after `go test ./internal/conformance`) to see it.

Done (honest, real-agent / real-ssh): the matrix spine; daemon + SQLite/WAL; the
tmux layer (local + remote via `--machine` routing); the thread layer local+remote
(new.headed, kill, list, resolve-pane for all agents; runtime-state + send.headful
for claude+pi); the ticket layer (create, list-by-thread, set-status, needs-input,
send-prompt, ownership); api.http-json; daemon.mesh-read.

### Remaining 19 skips and WHY (all honest, none faked)

- **codex × 6** (runtime-state L/R, send.headful L/R, send-prompt L/R): codex shows a
  **"Do you trust the contents of this directory?"** prompt at spawn that eats the
  first keystrokes. Trust is persisted per-directory in `~/.codex/config.toml`
  (`[projects."<dir>"] trust_level="trusted"`). `--dangerously-bypass-approvals-and-sandbox`
  does NOT skip it; a `-c projects."<dir>".trust_level=...` override didn't take. Pre-
  trusting an ephemeral test dir would mean writing into the user's global codex
  config — a hack. **Needs a real codex-trust integration decision** (e.g. sesh manages
  a per-thread CODEX_HOME, or trusts the cwd at spawn). The detection mechanism itself
  is agent-agnostic and works for codex once it's past the prompt.
- **headless × 12** (thread.new.headless, thread.send.headless): the headless mechanism
  (persistent child agent, no window, send-as-a-turn) is not built yet. Also pending:
  the **pi-headless N/A** question (PLAN flags it as a candidate N/A — confirm with Lukas).
- **tmux.nav × 1** (remote): the nav primitive (outer mymastertmux switch + inner
  switch-client + detached-pane kick) — not built.

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
