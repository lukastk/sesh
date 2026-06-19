# 07 — the REAL daemon `GET /v1/threads/terminal` WebSocket endpoint: FINDINGS

The sibling of exp 06 — the **xterm pty** endpoint. Code lives in the real source tree (branch
`rpc-ws-endpoint`, **not committed**). Gives any GUI a **real interactive terminal** onto a headful
thread's tmux pane over the daemon API — **agent-agnostic** (claude/codex/pi), detach-safe.

## What was built (real source)

- **`internal/daemon/terminal.go`** — `GET /v1/threads/terminal?id=<threadID>&cols=&rows=` (WebSocket):
  resolves the thread → refuses loudly (`404` unknown, `409` no live pane / headless / dead), then
  bridges a **pty running `tmux attach`** ↔ the WebSocket (binary frames = terminal bytes both ways; a
  `{"type":"resize",cols,rows}` text frame calls `pty.Setsize`). Same router → inherits the bearer-token
  auth on TCP by construction; origin check disabled (token is the trust signal).
- **Detach-safety baked in** (from exp 02, verified): before attaching it sets
  `window-size largest` (a smaller UI viewer can't shrink the user's real view) and attaches a
  **grouped viewer session** (`new-session -t <session> -s uiterm-…`), pointed at the thread's window
  and **killed on disconnect**, so selecting the window never moves a user watching that session.
- **`server.go`** one line (`d.routesTerminal`). **`go.mod`** added `github.com/creack/pty v1.1.24`.
- **`internal/conformance/terminalws_test.go`** — `TestThreadTerminalWebSocket`: honest e2e.

## Proof

- **`TestThreadTerminalWebSocket` PASSES (7.1s)** — real headful pi: the live pane **streams** as
  terminal bytes (asserts ANSI present = a real terminal), a **typed marker reaches the real pane**
  (write bridge, observed via `capture-pane`), and the loud paths (404 unknown, 409 headless) are correct.
- **No regressions:** `go build ./...` + `go vet ./...` clean; api/router/auth cells still pass; gofmt clean.
- **End-to-end in the real app** (`shots/`): the shell's Terminal tab now hits `/v1/threads/terminal`
  **straight at the daemon** (the exp-01 node bridge is gone). The live pi TUI renders; I typed a prompt
  and the agent replied `DAEMON-TERM-OK` — **fully interactive**, in the browser. The tmux status bar
  shows the **grouped viewer session** `uiterm-…` (detach-safety visibly in effect).

## What this settles

- **Both recommended streaming endpoints now exist, are tested, and proven in the app:** pi RPC chat
  (exp 06) and the universal xterm terminal (this). Together they cover every chat surface in the UI map
  with real push/streaming — no polling, no node middleware.
- **Detach-safety is solved**, not hand-waved — the one risk the feature map flagged for live attach.

## Productionization checklist (NOT done — experiment branch)

- **`set-option -g window-size largest`** is a global mutation of sesh's work tmux server. It's idempotent
  and a sensible default (sesh's server *should* be detach-safe), but a ship should decide whether the
  daemon sets it or it lives in myrig's tmux conf. Flagged, not silently assumed.
- **Cross-machine:** same as exp 06 — the GUI dials the thread's **owning** daemon's API (mesh model,
  works today); a hub-proxy variant is a follow-up.
- **Shared-session input interleaving** (user typing while the UI drives the same pane) is a UX question
  to settle (exp 02 caveat). · `SchemaVersion` bump decision (left at 20). · No CLI surface → SKILL/help
  correctly untouched.

Artifacts (real source): `internal/daemon/terminal.go`, `internal/daemon/server.go`,
`internal/conformance/terminalws_test.go`, `go.mod`/`go.sum`. Integration: `03_svelte_shell` Terminal tab;
`shots/native-terminal*.png`. Detach-safety basis: `../02_detach_safety/`.
