# 01 — live terminal bridge: FINDINGS

**Question:** can we put a *real, fully interactive* terminal for a thread's tmux pane into a
browser, so a user can chat with a live agent inside the UI? **Answer: yes, decisively — and it's small.**

## What was built

- `server.js` (~120 lines): a Node `http` + `ws` server. A websocket at `/term?thread=<id>`:
  1. resolves the thread's pane via the **`@sesh-thread-id` tmux marker** (`list-panes -a -F …`) —
     the exact truth sesh's own runtime resolution uses;
  2. spawns a **pty** (`node-pty`) running `tmux -L <socket> attach-session -t <session>:<window>`;
  3. bridges bytes both ways; JSON `{type:'resize',cols,rows}` control frames drive `pty.resize`.
- `index.html`: xterm.js (UMD build) + the fit addon. Connects, renders, sends keystrokes/resize.

## Proof (real, end-to-end — see `shots/`)

Drove a real **pi** agent in an isolated tmux server through the **isolated Brave browser**:
1. `term-01-connected.png` — the browser renders the live pi TUI pixel-faithfully, **including the
   tmux status bar** (`sesh_term-demo`, `0:pi*`, model line). Status: connected.
2. `term-02-typed.png` — typed *"Reply with exactly the text BRIDGE-OK-42…"*; it appears in pi's
   compose box (browser → ws → pty → tmux → pi).
3. `term-03-response.png` — pressed Enter; pi thought and answered **`BRIDGE-OK-42`**, and the live
   **token/cost counter** (`↑9.5k ↓29 $0.004`) updated in the mirrored status bar. Confirmed in the
   live pane within ~1s.

So: full interactive fidelity — animated spinners, colour, the agent's own TUI affordances (permission
prompts, `/` menus, plan mode) all work, because it **is** the real terminal, not a reconstruction.

## What this means for production design

- **This is the v1 chat surface for headed threads.** xterm.js + a pty bridge is the VS Code-grade
  approach; node-pty prebuilds fine on Node 24. It beats every "reconstruct a chat UI from transcript"
  idea on fidelity for an interactive agent.
- **The bridge belongs in the daemon, not a sidecar.** For the product, add **one endpoint**:
  `GET /v1/threads/attach?id=<id>` upgraded to a websocket, piping a pty of `tmux attach`. That makes
  it reachable cross-machine (mobile → a remote always-on daemon over the existing TCP API + bearer
  token) and removes the Node bridge entirely. Go has `gorilla/websocket` + `creack/pty`; the daemon
  already owns tmux. This is the single most valuable sesh-side addition the UI needs.
- **Electron can also go bridge-less locally:** the Electron main process can `node-pty.spawn('tmux
  attach')` (or `ssh -t <machine> tmux attach` for a remote) directly — no daemon change needed for the
  desktop app. The daemon websocket is what unlocks **web + Android**.

## Non-obvious things learned

- **`TMUX=''` must be unset** in the pty env or tmux refuses the nested attach ("sessions should be
  nested with care").
- **Detach-safety / sizing is the one real subtlety.** A second client attaching to a session makes
  tmux size the window to the smallest/most-recent client (`window-size latest` default), which would
  shrink the user's real attachment. Fixes for the product: attach with per-client sizing, or set
  `window-size manual`/`aggressive-resize off` on sesh's work server, or give each viewer a **grouped
  session** (`new-session -t <target>`) so its size is independent. Worth a dedicated decision; the
  prototype ran on an isolated server with a single viewer so it didn't bite.
- **Read-only viewers** are trivial (`attach -r`) — good default for "peek" vs "drive".
- **Playwright `.fill()` on the xterm textarea did drive input** — keystrokes reached the agent — but
  the product should rely on real key events; fine for the smoke test.

## Deferred

- Go-native daemon websocket endpoint (this prototype proves the shape; the daemon version is the real
  deliverable). · Multi-viewer + sizing policy. · Mobile keyboard ergonomics over a terminal (an
  on-screen terminal is usable but a headless/chat composer may be the better mobile default — see exp 02/03).

Artifacts: `server.js`, `index.html`, `shots/*.png`.
