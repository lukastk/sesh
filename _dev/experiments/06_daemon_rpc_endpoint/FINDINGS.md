# 06 — the REAL daemon `GET /v1/threads/rpc` WebSocket endpoint: FINDINGS

**This is the only experiment whose code lives in the real source tree, not `_dev`** — it's the
production endpoint the prototypes (exp 04/05) were de-risking. Built on branch **`rpc-ws-endpoint`**,
**not committed** (left for review). It makes the pi streaming bubble-chat a first-class sesh API
feature: works through the real daemon, over the network, with no node middleman.

## What was built (real source)

- **`internal/daemon/rpc.go`** — `GET /v1/threads/rpc?id=<threadID>` (WebSocket, via `coder/websocket`):
  resolves the thread → refuses loudly (`400` non-pi, `404` unknown, `409` no live pi socket — never a
  silent empty stream), then upgrades and **bridges the pi `rpc-socket` ↔ the WebSocket** (auto
  `subscribe`; each JSONL line ↔ one text frame). `piRPCSocketPath` resolves the socket the same way the
  pi extension creates it (`$TMPDIR`→`/tmp`→`/var/tmp` / `pi-rpc-sockets/<agent_session_id>.sock`).
- **`internal/daemon/server.go`** — one line: `d.routesRPC(mux)`. The route is on the **shared router**,
  so it's served on the unix socket (local trust) AND the TCP API **behind the existing bearer-token
  auth — by construction, no second auth surface**. Origin checking is disabled (the token is the trust
  signal; a browser app connects from a different origin via a trusted proxy/main process).
- **`go.mod`/`go.sum`** — added `github.com/coder/websocket v1.8.15` (pure Go, dependency-free).
- **`internal/conformance/rpcws_test.go`** — `TestThreadRPCWebSocket`: an **honest** end-to-end test
  (spawns a REAL pi agent, opens a REAL WebSocket over the daemon's TCP API, sends a prompt, asserts the
  reply **streams** as `text_delta` events and contains a unique marker) **plus the loud-error paths**
  (no token → 401, unknown id → 404, non-pi record → 400). Outside the matrix (pi-specific + WS — not a
  `(locality)×(agent)` cell), like the other focused regression tests. Gated by `-short`.

## Proof

- **`TestThreadRPCWebSocket` PASSES (8.1s)** — real pi, real WS, streamed marker `WS-PI-OK-4242`, all
  three loud-error statuses correct.
- **No regressions:** `go build ./...` + `go vet ./...` clean; the **API/router/auth cells pass**
  (`api.tcp-auth ✓`, `api.tcp-parity ✓`, `api.http-json ✓` local+remote — they drive the full router my
  route plugs into); changed-package unit tests green.
- **End-to-end in the real app** (`shots/native-daemon-rpc.png`): the Svelte shell now points `/v1/threads/rpc`
  **straight at the daemon's TCP API** (no node relay) — selected a headful pi thread → streamed reply
  ending in `NATIVE-DAEMON-WS-OK`. The node relay from exp 05 is now obsolete; the daemon does it.

## What this settles

- **The recommended first streaming endpoint is done and proven.** Porting cost was small (~110 lines +
  one dep). The sibling `GET /v1/threads/terminal` (xterm pty, exp 01) is the same shape — a clear next.
- **Cross-machine "for free" is *almost* true and needs one decision** (flagged, not silently skipped):
  the endpoint serves the pi socket **on its own host**. The mesh model says the GUI connects to the
  thread's **owning** daemon's API (it knows `row.machine`), which Just Works. If instead the GUI should
  always hit one hub daemon and have it **proxy** the WS to the owner (like the HTTP verb fan-out), that's
  a follow-up (proxy the upgrade over the peer transport). Recommend: GUI dials the owner directly first.

## Productionization checklist (NOT done — this is an experiment branch)

- **SchemaVersion left at 20.** A new WS route adds no JSON schema, but a ship may want to bump it so
  clients can feature-detect the endpoint (verified nothing hardcodes `20`, so a bump is low-risk). Lukas's call.
- **No CLI surface added** → the sesh-cli SKILL + help registry + their meta-tests are unaffected
  (correctly — the AGENTS.md sync rule triggers on CLI changes, and there are none).
- Consider a one-line mention in `SPEC.md §5` (the daemon/API section) that streaming endpoints exist.
- Cross-machine WS proxying decision (above). Reconnect/backoff is a client concern (the shell is a
  prototype). `abort`/`compact`/image-send are already supported by the protocol — just wire the composer.

Artifacts (real source): `internal/daemon/rpc.go`, `internal/daemon/server.go`, `internal/conformance/rpcws_test.go`,
`go.mod`/`go.sum`. Integration + proof: `03_svelte_shell` (now relay-free), `shots/native-daemon-rpc.png`.
