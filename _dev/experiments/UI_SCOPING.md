# Scoping a UI for sesh — verdict & recommended architecture

*2026-06-18. Scoping + experimentation only (ticket 598f0b04). Everything here lives under `_dev/`;
nothing prod-grade was built. Companion docs: the exhaustive [`FEATURE_UI_MAP.md`](FEATURE_UI_MAP.md)
(every CLI/daemon feature → its UI surface, from a 12-agent pass) and the per-experiment findings
under `_dev/experiments/NN_*/FINDINGS.md`.*

## TL;DR

**A graphical sesh app — desktop (Svelte + Electron) and Android — is very achievable, and the hard
part (chatting with a live agent inside the UI) is already proven working.** Two reasons it's easier
than it looks:

1. **sesh is already an API.** The daemon serves its *entire* feature set over HTTP+JSON — a unix
   socket locally and an opt-in bearer-token TCP API (`SESH_API_ADDR`) with parity by construction.
   The CLI, the TUI, and the existing Obsidian plugin are all just clients. **The app is one more API
   client; ~80% of the feature surface is "call an endpoint, render JSON, emit a verb".**
2. **The chat works.** I built and drove — in a real browser — a fully interactive coding agent
   (pi) running in a tmux pane, mirrored into the page via **xterm.js + a ~120-line websocket/pty
   bridge**, AND a clean headless chat-bubble view, both against the real daemon. Screenshots in the
   experiment dirs. This was the "I can imagine this being quite difficult" feature; it is not blocked.

## What was actually proven (live, with screenshots)

| Experiment | Result |
|---|---|
| **01 — live terminal bridge** (`01_live_terminal_bridge/`) | A real pi agent in a tmux pane, rendered in the **browser** via xterm.js. Typed a prompt → agent thought → replied `BRIDGE-OK-42` → live token/cost counter updated — all in the page. **Full interactive fidelity** (colour, spinners, the agent's own TUI affordances). ~120 lines of Node. |
| **00/02/03 — Svelte app shell** (`03_svelte_shell/`) | A runnable Svelte 5 + Vite app on the **real API**: live thread **grid** (state glyphs, daemon header), a **thread-detail** view that embeds the live **terminal** for headed threads and a **headless chat** (transcript JSONL → bubbles; send → poll → reply) for headless ones. Drove a full headless turn ("what is tmux?") from the UI. |
| **04 — pi RPC headful bubbles** (`04_pi_rpc_headful_bubbles/`) | A streaming **message-bubble chat for a *headful* pi thread** over pi's `rpc-socket`: subscribed to the live session, injected a prompt, watched the reply **stream token-by-token** into a browser bubble — and the turn appeared in the live pane (same conversation). Breaks the "headful ⇒ terminal only" rule for pi. |
| **05 — RPC relay + in-app chat** (`05_rpc_relay/`) | A prototype of the daemon endpoint **`GET /v1/threads/rpc?id=`** (TCP, bearer-auth, thread-id → pi-socket resolution, loud refusals) **wired into the real Svelte shell**: pi threads default to a streaming RPC bubble chat (RPC/Terminal/Transcript switcher), token injected server-side. Typed in the app → streamed reply `IN-APP-RPC-OK`. The daemon endpoint's blueprint. |
| **06 — the REAL daemon RPC endpoint** (`06_daemon_rpc_endpoint/`, code in `internal/`) | Built the actual Go **`GET /v1/threads/rpc`** WebSocket in the daemon (`coder/websocket`), on the shared router (inherits bearer auth), loud refusals (400/404/409). **Honest test passes** (real pi + real WS, streamed marker + 401/404/400); no regressions (api cells green). The Svelte shell now streams pi chat **through the real daemon, node relay obsolete** (`NATIVE-DAEMON-WS-OK`). Branch `rpc-ws-endpoint`, uncommitted. |
| **02 — detach-safety** (`02_detach_safety/`) | Characterized the one risk for any live terminal attach: default tmux `window-size latest` makes a UI viewer **shrink the user's real view** (verified). Fix: `window-size largest` (no shrink) + a **grouped viewer session** (independent window selection). Both baked into exp 07. |
| **07 — the REAL daemon terminal endpoint** (`07_daemon_terminal_endpoint/`, code in `internal/`) | Built the actual Go **`GET /v1/threads/terminal`** WebSocket (xterm pty via `creack/pty`), **agent-agnostic**, **detach-safe** (window-size largest + grouped viewer session). **Honest test passes** (real headful pi: live pane streams w/ ANSI, typed marker reaches the pane, 404/409). The shell's Terminal tab now hits the real daemon (node bridge gone) — fully interactive, drove a real turn to `DAEMON-TERM-OK`. Branch `rpc-ws-endpoint`, uncommitted. |
| **08 — claude & codex coverage** (`08_agent_coverage/`) | Closed the pi-only gap: the **terminal works for all three** (live-smoked claude 4333 B + codex 4325 B, ANSI), and the **headless chat now parses + renders all three transcript formats** (per-agent `PARSERS`; claude + codex bubbles verified in the shell). RPC streaming stays pi-only by nature. Residual: parsers are Q&A-grade (skip tool calls/diffs) — best fixed server-side with a normalized turn stream. |

So both "talk to a thread" modes — the interactive terminal *and* the request/reply chat — render and
round-trip inside a Svelte UI today, with **zero changes to sesh**.

## The one real architectural finding the UI forces

**There is no streaming/push channel in the daemon, and the browser can't hit the API directly.**

- **No SSE/WebSocket/CORS today.** Every live view is poll-based, `headless-reply` is in-memory
  (volatile across daemon restart), and the daemon sends no `Access-Control-Allow-Origin` and requires
  the token even on the CORS preflight — so a *browser-origin* `fetch` is blocked (verified).
- **This is not a blocker, it's a routing decision.** The fix is the same one the Obsidian plugin
  already uses: **never fetch from a browser origin** — go through a non-browser HTTP layer:
  - **Electron** → renderer calls the **main process** (unix socket locally, no token; or TCP+token for
    a remote daemon, token held only in main). The prototype's Vite dev-proxy stands in for this.
  - **Android** → a **native HTTP** layer (Capacitor/Tauri/Kotlin) to a remote always-on hub daemon
    over tailscale; token in the Keystore. Reads/routing fan out daemon-side, so the phone pointed at
    one hub sees and acts on the whole mesh.
- **For a materially better experience, add ONE thing to sesh:** a streaming endpoint. Two flavours,
  both small and worth it:
  - `GET /v1/threads/terminal?id=` (**WebSocket**, bearer-auth) — the daemon-native version of my
    bridge: pty around `tmux attach`, bidirectional, `{resize}` control. Unlocks the interactive
    terminal **cross-machine and on web/Android** with no Node sidecar. *Highest-value sesh-side add.*
  - `GET /v1/threads/chat/stream?id=` (**SSE**) — pushes transcript turns / busy→idle edges, replacing
    the `headless-reply`/transcript poll. Simpler than WS; works in Electron and Android native HTTP.

## Recommended architecture

```
            ┌─────────────── Svelte UI (one codebase) ───────────────┐
            │  stores ← seshClient (transport seam) → verbs/poll/stream│
            └───────┬───────────────────────────────────────┬─────────┘
   Electron desktop │                                         │ Android
   main-process proxy│                                        │ native HTTP + Keystore token
   (unix socket / TCP)│                                       │ → remote HUB daemon (tailscale)
            ┌────────▼────────┐                      ┌────────▼────────┐
            │ local sesh daemon│  ← mesh fan-out →    │  hub sesh daemon │
            └─────────────────┘   (already exists)    └─────────────────┘
```

- **One Svelte codebase**, a `seshClient` module with a transport seam (Electron IPC / Android native
  HTTP / dev proxy). Svelte stores never know which.
- **Mesh-as-model:** the app polls `GET /v1/mesh` from a single daemon; that daemon already aggregates
  and routes the whole mesh. The GUI **replaces tmux as the client** — selecting a thread *is* nav;
  the entire cockpit (`master up/window/attach/…`) becomes a non-feature (optional desktop
  power panel at most).
- **Chat, branched on `agentKind` × the `head`/`busy` axes every row already carries:**
  - **pi (headful *or* headless) → RPC streaming bubbles** — proven in exp 04: pi's `rpc-socket`
    exposes the live conversation as a structured, **token-streaming** protocol over a per-session unix
    socket (deterministic from the stored `agent_session_id`). This is the *best* chat surface — clean
    bubbles, real streaming, works on **any platform** (it's just JSON), and it works even when the
    agent is **headful** (messages inject as a steer into the live pane conversation). Closes the
    no-push-channel gap for pi with **no new sesh streaming endpoint** (the daemon just relays the
    socket; cross-machine wants a `GET /v1/threads/rpc?id=` WS relay).
  - **claude / codex, headless / idle → headless chat** (transcript bubbles + `send-headless` +
    poll/`await`). The mobile-perfect path for the agents without a live RPC. Ships against existing
    endpoints.
  - **claude / codex, headful (live pane) → the xterm.js terminal** (my exp-01 bridge → the future
    daemon WS endpoint), with a `capture-pane` read-only "peek" and a "Stop & chat" that flips a pane
    thread into the headless model (the unified runtime model resumes the same conversation seamlessly).
  - So: **pi gets a first-class streaming chat everywhere; claude/codex get terminal (desktop) +
    transcript-history bubbles** — progressive enhancement, not a hard wall.

### Where I differ from the feature-map's chat verdict
The 12-agent map (sensibly cautious) parked the **xterm.js terminal bridge at "end-state"** behind the
headless+capture approach, citing daemon work and detach-safety. My experiment changes that weighting:
the bridge is **small, works now, and is the only option with true interactive fidelity** — so I'd put
it **in v1 for desktop** (Electron can even run it locally via node-pty, *zero* daemon change), with the
daemon WS endpoint as the fast-follow that brings it to web+Android. Headless chat (A) remains the
right **mobile-first** default and the right surface for idle threads everywhere.

## The one risk that needs its own experiment before shipping a live terminal

**Detach-safety.** The user is frequently attached to the same tmux session in their real terminal. A
second client (the UI's attach) must **never resize/steal/kill** their attachment — tmux's default
`window-size latest` would shrink the user's panes to the browser's size. This is exactly the
"looks-fine-but-wrong" failure the project forbids. The fix is known (per-client sizing / `window-size
manual` / a **grouped session** per viewer, and WS-close kills only our client), but it deserves a
dedicated proving experiment (`02_detach_safety`, not yet run — the prototype used an isolated server
with a single viewer to sidestep it).

## Suggested next steps (if Lukas wants to proceed past scoping)

1. **Streaming endpoints** — **two of the three are now BUILT, TESTED & proven in the app** (branch
   `rpc-ws-endpoint`, uncommitted): `GET /v1/threads/rpc` (pi streaming chat — exp 06) and
   `GET /v1/threads/terminal` (universal xterm pty, detach-safe — exp 07). Together they give every chat
   surface real push/streaming with no polling/middleware. Optional third: an SSE chat-stream for
   claude/codex *headless* (poll works fine for v1). **Decisions:** review/merge the branch; bump
   `SchemaVersion` for feature-detection? (left at 20); does the daemon own `window-size largest` or
   does myrig's tmux conf? cross-machine = dial the thread's owning daemon directly (mesh model, works
   today) vs. hub-proxy the WS (follow-up). Also `/v1/peers` CRUD (mobile can't edit `peers.json`) and
   whether backup/restore/copy are in v1 (CLI/local-file-only today).
2. **Run the `02_detach_safety` experiment** before any live-attach ships.
3. **Build order** is fully laid out in `FEATURE_UI_MAP.md §5` (P0 spine → P1 cockpit → P2 power). P0 =
   transport seam + mesh grid + thread chat (both modes) + lifecycle verbs + tickets core + delegate.

## Open decisions for Lukas (carried into the plan)

- Streaming transport: poll-only v1 vs. add WS/SSE endpoints to the daemon now.
- xterm terminal in v1 (my recommendation, desktop) vs. headless-only v1 (the map's recommendation).
- Android shell: Capacitor vs. Tauri-mobile vs. native Kotlin (all must use native HTTP from day one).
- `/v1/peers` CRUD + backup/restore scope (the CLI/file-only gap that blocks Android management).
