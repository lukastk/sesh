# 04 — pi RPC: a streaming bubble UI for a HEADFUL thread: FINDINGS

**Question:** pi ships an `rpc-socket` extension — can we render a message-bubble chat UI for a
**headful** pi thread (agent live in a tmux pane), instead of being limited to the terminal mirror?
**Answer: yes, and it's better than the headless path — it streams.**

## What pi exposes

- pi has `--mode rpc` (a JSONL command/event protocol) and the **`rpc-socket` extension**
  (`lukastk/pi-rpc-socket`) opens a unix socket **inside the live interactive TUI session** at
  `<tmpdir>/pi-rpc-sockets/<sessionId>.sock` — where `<sessionId>` is the thread's `agent_session_id`,
  which **sesh already stores**, so the socket path is deterministic.
- Protocol (JSONL, one object per line):
  - **send** `{"message":"…"}` → injected into the live conversation **as a steer** (so it works whether
    the agent is idle or mid-turn); also `{"abort":true}`, `{"compact":true}`, `{"getState":true}`,
    `{"appendSystemPrompt":"…"}`.
  - **subscribe** `{"subscribe":true}` → receive **streamed events**: `{"event":"text_delta","delta":…}`,
    `tool_execution_start/end`, `agent_end`. Events are broadcast **only for socket-initiated turns**.

## Proof (live — see `shots/rpc-bubbles.png`, plus `probe.mjs`)

1. `probe.mjs` connected to a real **headful** pi thread's socket, subscribed, and sent a prompt. It
   received the answer as **streamed `text_delta` chunks** — *first delta at 1.5s, streaming in real
   time* — assembled into a full reply ending in `RPC-STREAM-OK`. Got live `state` too (idle flag,
   model `deepseek-v4-pro`, full tmux locator).
2. The injected turn **appeared in the live tmux pane** — confirming it's the *same conversation*, not a
   side channel. The pane TUI and the RPC bubble UI drive one session.
3. `bridge.mjs` + `index.html` = a browser **message-bubble UI** (socket↔websocket bridge). Drove it via
   the isolated Brave: typed a message → user bubble (right) + a **streaming assistant bubble** (left)
   that grew token-by-token to "1. tmux / 2. GNU Screen / 3. Zellij / BUBBLE-RPC-OK". Header showed
   "headful thread · idle". Clean chat, no terminal.

## What this changes in the design

**It breaks the "headful ⇒ terminal only" rule — for pi.** The reason headful threads needed a terminal
was (a) no structured send except `send-keys` and (b) no reply/stream channel. pi's RPC gives **both**,
plus **token-level streaming** — which also closes the "no push channel" gap *for pi* with **zero new
sesh streaming endpoint** (the stream already exists; sesh just has to relay the socket).

Updated chat verdict (now reflected in `UI_SCOPING.md`):
- **pi (headful or headless) → RPC streaming bubbles** as the primary chat surface, on **any platform**
  (it's just JSON — great on mobile). Terminal becomes an optional "raw view" for pi.
- **claude / codex → terminal (desktop) + transcript-history bubbles**, until/unless they expose a
  comparable live interface (claude's `stream-json` print mode is per-invocation, not a live-attach into
  a running interactive instance — not equivalent). Progressive enhancement, branched on `agentKind`.

## Caveats / to verify before productizing

- **Cross-machine:** the socket is local to pi's host. The daemon must relay it — a new endpoint
  `GET /v1/threads/rpc?id=` (WebSocket) that bridges the per-session pi socket, same shape as the
  terminal-WS idea. Single highest-value pi-specific sesh addition.
- **Enablement:** depends on the `rpc-socket` extension being loaded on sesh-spawned pi threads (it is,
  by default here). sesh resolves the socket from the stored `agent_session_id`.
- **Dual-driver coherence:** typing in the pane *and* sending via RPC both feed one conversation; pi's
  steer semantics handle "busy", but subscribers only see *socket-initiated* turns — so a bubble UI
  won't stream a turn the user typed directly in the pane (it would need a transcript re-read to catch
  up). Worth a deliberate UX decision.
- **`{"message"}` is delivered as *steer*** (not a fresh top-level prompt) — fine for chat; note it if
  exact turn semantics matter.

Artifacts: `probe.mjs` (protocol proof), `bridge.mjs` + `index.html` (bubble UI), `shots/rpc-bubbles.png`.
