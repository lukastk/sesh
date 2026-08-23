# 03 — Svelte app shell (also covers 00 API-access + 02 headless-chat): FINDINGS

**Question:** how much of a real sesh app is "forms + lists over the API", and do both chat modes
work inside one Svelte app on the live daemon? **Answer: most of it is; and yes.**

## What was built (`03_svelte_shell/`)

A Svelte 5 + Vite app driven by the **real isolated daemon** (TCP API on `127.0.0.1:8979`):
- `src/api.js` — a ~30-line client; `glyph()`/`stateLabel()` mirror the TUI's head/busy state model.
- `src/App.svelte` — sidebar **thread grid** (polls `GET /v1/threads/grid` every 2.5s; live glyphs,
  daemon header, per-row agent + state) + a **thread-detail** pane with action buttons (Stop / Resume /
  Archive / Headless-Terminal toggle) wired to the real verbs.
- `src/Terminal.svelte` — embeds **xterm.js** (the exp-01 bridge) for headed threads, inline in the app.
- `src/HeadlessChat.svelte` — transcript **JSONL → chat bubbles** + a composer that does
  `send-headless` → polls `headless-reply` → reloads the transcript.
- `vite.config.js` — dev proxy: `/api → daemon` (injects the bearer token), `/term → bridge` (ws).

## Proof (live, see `shots/`)

1. `shell-01-grid.png` — the grid renders 3 real threads from the API with state glyphs (● headful /
   ◌ headless, busy/idle) and the daemon header (`local · schema 20 · pid …`).
2. `shell-02-terminal.png` — selecting the headed pi thread embeds its **live agent terminal** in the
   app (the earlier BRIDGE-OK-42 conversation visible), with Stop/Archive/Headless actions.
3. `shell-03/04-headless.png` — the headless thread shows the transcript as **chat bubbles** (user
   right, assistant left with collapsed "thinking"); typed *"what is tmux?"* in the composer → Send →
   the reply rendered as a new bubble. Full headless turn loop, end-to-end, from the UI.

## What this means for production design

- **00 (API access):** the daemon's TCP API + bearer token is a clean foundation. **CORS is the one
  catch** — verified the daemon requires the token even on the OPTIONS preflight and sends no
  `Access-Control-Allow-Origin`, so a browser-origin fetch is blocked. The dev proxy stands in for what
  the real app does: route through a **non-browser HTTP layer** (Electron main / Android native HTTP) —
  exactly the Obsidian plugin's `requestUrl` trick. No daemon CORS change needed (and adding permissive
  CORS to a token service would be a security regression — flag, don't add silently).
- **02 (headless chat):** the `send-headless → headless-reply → transcript` loop is a perfectly good
  request/reply chat and is **mobile-perfect** (no tmux/attach needed). Polling is fine at turn
  granularity; an SSE endpoint would make it crisper (see `UI_SCOPING.md`).
- **03 (shell):** the grid + detail + lifecycle verbs are genuinely "lists and buttons over the API" —
  the bulk of `FEATURE_UI_MAP.md` is this shape. The two non-trivial renderers are the **terminal**
  (solved, exp 01) and the **transcript-JSONL→bubbles parser** (prototyped for pi; production needs a
  typed parser per `agentKind` — claude/codex/pi formats differ — with a raw fallback).

## Non-obvious things learned

- **Svelte 5 + Vite 6** needs `@sveltejs/vite-plugin-svelte@^5` (the `^4` peer-pins to Vite 5). `mount()`
  from `svelte` (not `new App()`) is the v5 entry. xterm.js bundles fine as an ESM dep under Vite.
- The grid endpoint is `GET /v1/threads/grid` (returns `{rows:[…]}` with `head`/`busy`/`attachment`/
  `cwd_rel`/`tickets_open` already computed daemon-side) — the app needs almost no client-side state.
- A Vite proxy **can inject a static `Authorization` header**, which made the whole API reachable from
  the renderer for the demo without embedding the token in client code.

## Deferred / not built

- Per-`agentKind` transcript parsers (only pi's `type:"message"` shape parsed here). · Tickets board,
  blobs/attachments, automation center, machines management (all mapped in `FEATURE_UI_MAP.md`, all
  "forms over the API"). · The Electron/Android transport seam (prototype used the dev proxy). · SSE/WS
  streaming. · The detach-safety experiment (exp 02 slot, see `UI_SCOPING.md`).

Artifacts: `src/*.svelte`, `src/api.js`, `vite.config.js`, `shots/*.png`.

## Update (2026-06-19) — full multi-screen app shell

Expanded the prototype from a single threads view into a real **3-screen app** (top-nav router in
`App.svelte`), to validate how much of `FEATURE_UI_MAP.md` is "forms over the API". **Answer: nearly all
of it — fast.** Each screen is a thin Svelte component polling/posting the existing endpoints.

- **Threads** (`ThreadsScreen.svelte`) — the live grid + chat surfaces, now with **+ New thread**
  (`NewThreadModal.svelte` → `POST /v1/threads`; proven: created a headless thread from the UI and it
  appeared live), **rename/archive/delete/stop/resume** row actions, and a **ticket badge** (`1🎫`) on
  bound threads. `shots/app-threads.png`, `app-newthread.png`.
- **Tickets** (`TicketsBoard.svelte`) — a **kanban board** from `GET /v1/tickets/list-all` (5 status
  columns triage→ready→active→done→dropped, color-coded cards showing machine + bound thread), **+ New
  ticket** (`POST /v1/tickets`), and a **detail/editor** modal: editable name, status switcher
  (`POST /v1/tickets/status`), prompt edit (`/tickets/set`), **Send-to-thread** (`/tickets/send-prompt`,
  enabled only when bound), delete. `shots/app-tickets.png`, `app-ticket-detail.png`.
- **Machines** (`MachinesScreen.svelte`) — the **mesh** (`GET /v1/mesh`): a card per machine with a
  reachability dot, freshness ("live"/"12s ago"/"OFFLINE"), thread counts, and a thread preview. The
  the cockpit collapses into this. `shots/app-machines.png`.

Confirms the scoping thesis: **the whole non-chat surface is lists + forms + modals over the daemon
API** — the only hard parts were the two chat modes (terminal + RPC streaming), both now solved and
backed by real daemon endpoints (exp 06/07). Still mapped-but-not-built in the shell: blobs/attachments
UI, automation center (hooks/subscriptions), per-machine peer add/remove, fork — all the same shape.
