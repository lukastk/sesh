# Experiments plan — a UI for sesh

Throwaway prototyping to scope a graphical app for **sesh** (the multi-machine coding-agent
session manager). Goal: a UI that (a) exposes the full sesh feature set and (b) lets you
**chat with each thread inside the UI** — the hard, high-risk part Lukas flagged.

Targets in scope: **desktop** (Svelte + Electron) and **Android**. The only durable
deliverable of each experiment is its **findings** (summarised here, full writeup in the
experiment's `FINDINGS.md`). Experiment code is throwaway and gitignored.

Status legend: `todo` · `in progress` · `done` · `skipped`.

---

## The architectural starting point (established by recon, 2026-06-18)

- **sesh already has a complete HTTP+JSON API** — the daemon's full router (`internal/daemon/server.go`)
  is served on a unix socket AND optionally on TCP (`SESH_API_ADDR`) behind a bearer token
  (`SESH_API_TOKEN`), with **parity by construction** (same router, no second surface). The CLI,
  the TUI, and the **existing Obsidian plugin** are all just clients of this API. **A Svelte/Electron
  app and an Android app are simply more API clients.** This is the single biggest derisking fact:
  most of the feature surface is "call an endpoint, render JSON".
- **~30 top-level commands / ~90 subcommands** (full list captured for the feature→UI map).
- **The chat problem is the real risk.** Two distinct "talk to a thread" modes exist in sesh today:
  1. **Headless turns** — `send-headless` runs one stateless turn in the background, `headless-reply`
     polls completion, `transcript?tail=N` returns the agent's raw JSONL. Clean chat-bubble UX,
     pure HTTP, mobile-friendly — but loses the live interactive agent TUI (permission prompts,
     plan mode), one turn at a time, **no streaming** (poll only).
  2. **Live pane** — a headful thread is an agent running in a real tmux pane. `thread capture`
     (capture-pane → screen text) + `thread send` (send-keys) exist, but there is **no pty/stream
     endpoint** today. A true interactive terminal in the UI needs a new **websocket pty bridge**.
- **No streaming anywhere yet** (no SSE/websocket/flush in the codebase). Live updates today = poll.

---

## Group A — Foundations: can a UI client reach the API?

### `00_api_access` *(folded into `03_svelte_shell`)*

**Status:** done (2026-06-18)

**Questions**
- Can a browser/Svelte renderer hit the daemon API directly (CORS, auth header)? What about the
  unix socket vs TCP `SESH_API_ADDR`? What does Electron need (main-process proxy vs renderer fetch)?
- What does Android need (native HTTP to a remote always-on daemon over tailscale)?

**Findings** *(full writeup in [`03_svelte_shell/FINDINGS.md`](03_svelte_shell/FINDINGS.md))*
- The TCP API + bearer token is a clean foundation; a Svelte app lists/acts on real threads with ~30
  lines of client.
- **CORS is the catch:** the daemon requires the token even on the OPTIONS preflight and sends no
  `Access-Control-Allow-Origin` (verified), so a *browser-origin* fetch is blocked.
- **Fix = route through a non-browser HTTP layer** (Electron main proxy / Android native HTTP) — the
  Obsidian plugin's `requestUrl` trick. No daemon CORS change needed (and shouldn't be added — security
  regression for a token service). The prototype used a Vite dev-proxy (token-injecting) to stand in.

---

## Group B — The hard part: chatting with a thread

### `01_live_terminal_bridge`

**Status:** done (2026-06-18) — **the crown result**

**Questions**
- Can we put a **real interactive terminal** for a thread's tmux pane into a browser via
  **xterm.js + a websocket pty bridge** that runs `tmux attach`? Read + write fidelity, resize,
  multiple viewers, detach-safety (must not disturb the user's real attachment).
- Electron-local (node-pty spawns tmux directly) vs daemon-served websocket (web/Android).

**Findings** *(full writeup in [`01_live_terminal_bridge/FINDINGS.md`](01_live_terminal_bridge/FINDINGS.md))*
- **Yes, decisively, and it's small (~120 lines).** Drove a real **pi** agent in a browser via
  xterm.js: typed a prompt, the agent thought and replied `BRIDGE-OK-42`, the live token/cost counter
  updated — full interactive fidelity (colour, spinners, the agent's own TUI affordances). See `shots/`.
- Resolve the pane via the **`@sesh-thread-id` marker** (sesh's own truth); pty around
  `tmux attach-session -t session:window`; `TMUX=''` to allow the nested attach.
- **Production path:** one daemon endpoint `GET /v1/threads/terminal?id=` (WebSocket pty) makes this
  cross-machine + web/Android with no Node sidecar. Electron can also go bridge-less via node-pty.
- **One real risk → its own experiment:** detach-safety (a 2nd tmux client resizing the user's real
  attachment). See `02_detach_safety` below.

### `02_detach_safety` *(surfaced by 01; gates the terminal endpoint)*

**Status:** done (2026-06-19)

**Questions**
- When the UI attaches a 2nd client to a session the user is *also* attached to, does it resize/move
  their view? How do we prevent it?

**Findings** *(full writeup in [`02_detach_safety/FINDINGS.md`](02_detach_safety/FINDINGS.md))*
- **Yes by default (bad):** with tmux's default `window-size latest`, a 90×28 UI client **shrinks** the
  user's 200×48 window to 90×26 (verified with real node-pty clients) — the forbidden "looks-fine-but-
  wrong" failure.
- **Fix (verified):** `window-size largest` → the UI attach leaves the user's window **unchanged**; the
  smaller viewer just sees a cropped viewport. Plus a **grouped viewer session** per connection isolates
  the other shared state (the session's current *window*), so the UI never moves the user.
- Both are now baked into the terminal endpoint (exp 07). The 1:1 case is trivially safe; the grouped
  session covers the shared-session placement modes too.

### `02b_headless_chat_transcript` *(folded into `03_svelte_shell`)*

**Status:** done (2026-06-18)

**Questions**
- Parse each agent's transcript JSONL (claude/codex/pi) into chat bubbles. Drive a full
  send-headless → poll → render turn loop from a UI. How good is the chat UX vs the terminal?

**Findings** *(full writeup in [`03_svelte_shell/FINDINGS.md`](03_svelte_shell/FINDINGS.md))*
- The `send-headless → headless-reply → transcript` loop is a clean request/reply chat and is
  **mobile-perfect** (no tmux/attach). Drove a full turn from the UI; rendered as chat bubbles.
- pi's transcript is `type:"message"` lines with `message.role` + `message.content[]` (text/thinking).
  **Production needs a typed parser per `agentKind`** (claude/codex/pi differ) + a raw fallback.

### `04_pi_rpc_headful_bubbles`

**Status:** done (2026-06-18)

**Questions**
- pi ships an `rpc-socket` extension — can we render a **message-bubble UI for a HEADFUL pi thread**
  (agent live in a pane), instead of only the terminal mirror?

**Findings** *(full writeup in [`04_pi_rpc_headful_bubbles/FINDINGS.md`](04_pi_rpc_headful_bubbles/FINDINGS.md))*
- **Yes — and it streams.** pi's `rpc-socket` opens a unix socket inside the live TUI session at
  `<tmpdir>/pi-rpc-sockets/<agent_session_id>.sock` (path deterministic — sesh stores the id). Protocol:
  `{"subscribe":true}` → streamed `text_delta`/`tool_*`/`agent_end`; `{"message":"…"}` → injected as a
  steer into the **live** conversation.
- Proven live (see `04_*/shots/`): `probe.mjs` streamed a reply token-by-token (first delta 1.5s) from a
  real **headful** pi thread; the turn appeared **in the live pane** (same conversation); `bridge.mjs`+
  `index.html` rendered a real **streaming browser bubble UI** ("BUBBLE-RPC-OK").
- **Breaks the "headful ⇒ terminal only" rule for pi**, and closes the no-push-channel gap *for pi* with
  zero new sesh streaming endpoint. Cross-machine needs a relay endpoint `GET /v1/threads/rpc?id=` (WS).
  claude/codex stay terminal — progressive enhancement, branched on `agentKind`.

---

## Group C — The app shell & packaging

### `03_svelte_shell` (+ packaging assessment)

**Status:** done (2026-06-18; expanded 2026-06-19) — covers `00_api_access` + `02b_headless_chat_transcript`

**Questions**
- A Svelte app shell: thread grid/list (live), thread detail with both chat modes embedded,
  ticket board. How much of the feature→UI map is "just" forms over the API?
- Packaging reality check: Electron (desktop) and Capacitor/native (Android) — what works, what's hard.

**Findings** *(full writeup in [`03_svelte_shell/FINDINGS.md`](03_svelte_shell/FINDINGS.md))*
- A runnable **Svelte 5 + Vite** app on the real API: live thread **grid** (state glyphs) + thread
  **detail** embedding the live **terminal** (exp 01) for headed threads and a **headless chat** for
  idle ones. Drove a full headless turn from the UI. See `shots/`.
- The bulk of the feature surface is genuinely "lists + buttons over the API"; the two non-trivial
  renderers are the terminal (solved) and the transcript-JSONL parser (per-`agentKind`).
- **Packaging** (assessed in the 12-agent map + `UI_SCOPING.md`, not separately prototyped): Electron =
  renderer ↔ main-process proxy (unix socket local / TCP+token remote), node-pty for a local terminal;
  Android = native HTTP to a remote hub daemon over tailscale, Keystore token, last-snapshot offline
  cache. One Svelte codebase behind a `seshClient` transport seam.
- **Expanded to a full 3-screen app (2026-06-19):** Threads (+ New thread, rename/archive/delete, ticket
  badges) · **Tickets kanban** (list-all, create, status switcher, prompt editor, send-to-thread) ·
  **Machines/mesh** (reachability + freshness). Confirms the thesis: the entire non-chat surface is
  "lists + forms + modals over the API". Shots in `03_svelte_shell/shots/app-*.png`.

### `05_rpc_relay` (daemon-endpoint blueprint + in-app streaming chat)

**Status:** done (2026-06-18)

**Questions**
- Can the pi RPC streaming chat work **through a daemon-shaped endpoint** (thread-id resolution + auth)
  and **inside the real Svelte shell**, with the cross-machine wire shape proven?

**Findings** *(full writeup in [`05_rpc_relay/FINDINGS.md`](05_rpc_relay/FINDINGS.md))*
- Built `relay.mjs` = a prototype of **`GET /v1/threads/rpc?id=` (WebSocket)**: TCP, bearer-auth,
  resolves thread→`{agent_kind, agent_session_id}`, refuses loudly (400 non-pi / 404 unknown / 401 bad
  token), bridges the pi socket ↔ WS. Shaped so the Go port is mechanical (no prod daemon code written).
- **Integrated into the shell**: pi threads default to an **RPC streaming bubble chat** (`RpcChat.svelte`)
  with an RPC/Terminal/Transcript switcher; the Vite proxy injects the token via a header so the browser
  never holds it. Proven live: typed in the app → streamed reply `IN-APP-RPC-OK` (see `05_*/shots/`).
- **Cross-machine falls out for free** — the endpoint runs on the owning host and is reached over that
  daemon's TCP API (no ssh/sidecar). Token stays server-side (Electron main / Android native in prod).
- Gotchas (cost time, all UI-layer): Vite doesn't apply proxy `rewrite` to WS upgrades (inject auth via
  `headers`); Svelte 5 deep-reactivity needs immutable array updates for streaming text; guard the
  connect `$effect` to run once per thread (the parent's 2.5s poll otherwise wipes state).

### `06_daemon_rpc_endpoint` (the REAL daemon endpoint — production code)

**Status:** done (2026-06-19) — on branch `rpc-ws-endpoint`, not committed

**Questions**
- Build the actual Go daemon endpoint `GET /v1/threads/rpc` (the relay was its blueprint) and prove it
  works through the real daemon API + the real app, with an honest test.

**Findings** *(full writeup in [`06_daemon_rpc_endpoint/FINDINGS.md`](06_daemon_rpc_endpoint/FINDINGS.md);
this experiment's CODE lives in the real source tree, not `_dev`)*
- Built `internal/daemon/rpc.go` (WebSocket via `coder/websocket`): resolves the thread, refuses loudly
  (400 non-pi / 404 unknown / 409 no socket), bridges the pi `rpc-socket` ↔ WS on the **shared router**
  (so it inherits the bearer-token auth on TCP by construction). One line wires it in `server.go`.
- **Honest test** `TestThreadRPCWebSocket` (real pi + real WS) **PASSES (8.1s)**: streamed marker +
  401/404/400 loud paths. No regressions (`build`/`vet` clean; api.tcp/http cells pass).
- **The Svelte shell now streams pi chat through the REAL daemon endpoint — node relay obsolete**
  (`06_*/shots/native-daemon-rpc.png`, reply `NATIVE-DAEMON-WS-OK`).
- Productionization left for Lukas: SchemaVersion bump decision (left at 20), cross-machine WS proxying
  vs. dial-the-owner, optional SPEC note. No CLI surface → SKILL/help untouched (correctly).

### `07_daemon_terminal_endpoint` (the REAL xterm terminal endpoint — production code)

**Status:** done (2026-06-19) — on branch `rpc-ws-endpoint`, not committed

**Questions**
- Build the sibling daemon endpoint `GET /v1/threads/terminal` (xterm pty, agent-agnostic), made
  detach-safe by exp 02, and prove it in the real app.

**Findings** *(full writeup in [`07_daemon_terminal_endpoint/FINDINGS.md`](07_daemon_terminal_endpoint/FINDINGS.md);
CODE in the real source tree)*
- Built `internal/daemon/terminal.go` (WebSocket pty via `coder/websocket` + `creack/pty`): resolves the
  thread, refuses loudly (404 unknown / 409 no live pane), bridges `tmux attach` ↔ WS (binary bytes +
  `{type:resize}`). **Detach-safe**: `window-size largest` + a grouped viewer session killed on disconnect.
- **Honest test** `TestThreadTerminalWebSocket` **PASSES (7.1s)**: live pane streams (ANSI asserted), a
  typed marker reaches the real pane, 404/409 loud paths. No regressions.
- **Proven in the app:** the shell's Terminal tab hits the real daemon endpoint (node bridge gone) —
  fully interactive, drove a real pi turn to `DAEMON-TERM-OK`; the grouped viewer session is visible in
  the tmux status bar (`07_*/shots/`).

### `08_agent_coverage` (claude & codex — not just pi)

**Status:** done (2026-06-19)

**Questions**
- The chat work leaned on pi. Are **claude** and **codex** derisked for the terminal + headless chat?

**Findings** *(full writeup in [`08_agent_coverage/FINDINGS.md`](08_agent_coverage/FINDINGS.md))*
- **Terminal endpoint works for all three** — live-smoked the daemon WS on real headed claude (4333 B,
  ANSI) and codex (4325 B, ANSI); it has zero agent-specific code. (pi has the committed Go test.)
- **Headless chat now parses + renders all three** — characterized the three distinct transcript JSONL
  schemas and wrote per-agent parsers (`HeadlessChat.svelte` `PARSERS`); claude + codex render clean
  bubbles in the shell (`08_*/shots/`). codex's system-injected env/permissions blocks are filtered out.
- **RPC streaming is pi-only by nature** (claude/codex have no live RPC socket) — they use terminal +
  headless bubbles. **Honest residual:** the parsers are Q&A-grade; real coding transcripts carry tool
  calls/diffs (claude `tool_use`, codex `function_call`) the prototype skips — best solved server-side
  with a normalized turn stream. Also: codex needs CODEX_HOME+auth set up (env.sh now does it).

---

## The feature → UI map

The exhaustive map of every CLI feature to its UI surface (desktop + Android) is produced by a
multi-agent pass and lives in [`FEATURE_UI_MAP.md`](FEATURE_UI_MAP.md). The overall scoping
verdict + recommended architecture lives in [`UI_SCOPING.md`](UI_SCOPING.md).

---

## Outcome (2026-06-18)

**Both deliverables done.** The exhaustive feature→UI map is in [`FEATURE_UI_MAP.md`](FEATURE_UI_MAP.md)
(12-agent pass); the verdict + recommended architecture + build order is in [`UI_SCOPING.md`](UI_SCOPING.md).
**Headline: a Svelte desktop+Android app is very achievable, and the hard "chat with a live thread"
feature is proven working** (interactive xterm.js terminal AND headless chat, both on the real API,
zero sesh changes). The one architectural finding the UI forces: no streaming/push channel + no CORS,
fixed by routing through a non-browser HTTP layer and (optionally) adding a WS/SSE endpoint to sesh.

## Resolved decisions (2026-06-18)

- **Scope:** scoping + experimentation only, all under `_dev/`. No prod-grade code yet (Lukas).
- **Experiment code gitignored**; `EXPERIMENTS_PLAN.md`, `FEATURE_UI_MAP.md`, `UI_SCOPING.md` tracked.
- **Stack:** Svelte for the UI; Electron for desktop; Android as a second target (Lukas).
- **The app is an API client**, one daemon at a time (mesh fan-out is daemon-side). The GUI replaces
  tmux-as-client; the master-tmux cockpit becomes a non-feature.
- **Two chat modes**, branched on `head`/`busy`: headless transcript-chat (mobile-perfect) + the
  xterm.js terminal for live panes.
- **CORS:** don't add it to the daemon — route through Electron-main / Android-native HTTP instead.

## Still to decide (carried to `UI_SCOPING.md` → "Open decisions for Lukas")

- Streaming transport: poll-only v1 vs. add a WS terminal + SSE chat endpoint to the daemon now.
- xterm terminal in v1 (my recommendation, desktop) vs. headless-only v1 (the map's recommendation).
- Android shell: Capacitor vs. Tauri-mobile vs. native Kotlin (all must use native HTTP from day one).
- `/v1/peers` CRUD + backup/restore scope (the CLI/file-only gap that blocks Android management).
- Run `02_detach_safety` before any live-attach ships.
