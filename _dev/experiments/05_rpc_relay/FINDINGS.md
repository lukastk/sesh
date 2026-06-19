# 05 — the RPC relay (daemon-endpoint blueprint) + in-app streaming chat: FINDINGS

**Goal (from "Proceed"):** make the pi RPC streaming bubble-chat work **through a daemon-shaped
endpoint** and **inside the real Svelte shell** (not a standalone page), and prove the cross-machine
wire shape. Done — without touching production daemon code (a prototype relay + a precise spec).

## What was built

- `relay.mjs` — a **prototype of the daemon endpoint `GET /v1/threads/rpc?id=<threadID>`** (WebSocket),
  shaped so the Go port is mechanical:
  - listens on **TCP** (the daemon's API surface), **bearer-token auth** (Authorization header *or*
    `?token=` query, since browsers can't set WS headers — the constraint the feature map flagged);
  - **resolves the thread id → `{agent_kind, agent_session_id}`** (here via the sesh API; in the daemon
    it's a direct store read — the daemon already stores both);
  - **refuses loudly**: non-pi → 400, unknown thread → 404, bad token → 401, no live socket → error (no
    silent fallback — verified: 401/404 on the bad paths);
  - bridges the per-session pi rpc unix socket ↔ the WebSocket (`subscribe` + relay both ways).
- **Integrated into the Svelte shell (`03_svelte_shell`)**: new `src/RpcChat.svelte` (streaming bubbles),
  `App.svelte` routes **pi threads → RPC chat by default** with an RPC / Terminal / Transcript switcher,
  and `vite.config.js` proxies `/rpc` → the relay, **injecting the bearer token via a proxy header** so
  the browser never holds it.

## Proof (live — see `shots/shell-rpc-chat.png`)

- A non-browser WS client hit the relay with **thread-id + token** and streamed back `RELAY-OK-7` /
  `ALIAS-OK` (the daemon-endpoint shape: resolution + auth + streaming).
- **In the real app**: selected the headful pi thread `rpc-chat` → it defaulted to the **RPC streaming
  bubble chat**; typed a message → user bubble (right) + the assistant reply **streamed in token-by-token**
  (left) ending in `IN-APP-RPC-OK`; the same turn appeared in the live pane. Header switcher
  RPC / Terminal / Transcript, ribbon "⚡ pi RPC · streaming · idle".

## What this establishes for production

- **The daemon endpoint is small and well-defined.** Port `relay.mjs` to Go: a WS upgrade handler on the
  existing router (`GET /v1/threads/rpc`), `store.GetThread(id)` for `agent_kind`+`agent_session_id`,
  `net.Dial("unix", piSocketPath)`, pump both ways. Reuses the daemon's bearer auth. ~the same size as
  the exp-01 terminal WS endpoint — build them together.
- **Cross-machine falls out for free.** The endpoint runs **on the owning host** (co-located with the pi
  socket) and is reached over that daemon's **TCP API** — exactly how a remote/mobile client reaches any
  sesh feature. No ssh, no sidecar. (The prototype proves the wire by being reached over TCP+token, the
  honest stand-in for a remote daemon API.)
- **Token stays server-side.** The browser connects to `/rpc`; the Vite proxy (≙ the Electron main
  process / Android native layer in production) injects the token. The renderer never holds it.

## Non-obvious things learned (Svelte 5 + Vite gotchas — real, cost time)

- **Vite does NOT apply a proxy `rewrite` to WebSocket upgrades** — only to HTTP. So inject auth via the
  proxy `headers` option and have the endpoint accept the alias path; don't rely on rewrite for WS.
- **Svelte 5 deep reactivity:** pushing an object into a `$state` array proxies it; mutating a *separately
  held reference* to that object (`current.text += …`) does **not** update the proxy in the array → the
  bubble never grew. Fix: immutable updates (`msgs = msgs.map((x,i)=> i===idx ? {...x, text:x.text+d} : x)`).
- **A parent that re-polls (every 2.5s here) reassigns the selected row**, which re-ran the child's
  connect `$effect`, wiping state + leaking sockets. Guard the effect to run **once per thread id**.
- These are pure UI-layer bugs, not architecture limits — the message round-trip (app → proxy → relay →
  pi) worked even while the bubbles weren't rendering (pi received every message).

## Deferred

- The real Go `GET /v1/threads/rpc` daemon endpoint (this is its blueprint + spec). · Same-conversation
  dual-driver UX (pane typing vs RPC). · `abort`/`compact`/image-send wired into the composer (the
  protocol supports them). · Reconnect/backoff; the relay is a happy-path prototype.

Artifacts: `relay.mjs` (endpoint blueprint), `03_svelte_shell/src/RpcChat.svelte` + `App.svelte` +
`vite.config.js` (integration), `shots/shell-rpc-chat.png`.
