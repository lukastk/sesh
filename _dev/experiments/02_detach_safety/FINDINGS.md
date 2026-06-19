# 02 — detach-safety: can a UI terminal viewer avoid disturbing the user? FINDINGS

**Question:** when the UI attaches a 2nd tmux client to a session the user is *also* attached to in
their real terminal, does it resize/move their view? And how do we prevent it? **Answer: yes by
default (bad), and two tmux settings fix it — both now baked into the terminal endpoint.**

## Method (`probe.mjs`)

Spawned real, differently-sized tmux clients with **node-pty** on an isolated server: a "user" client
at 200×50 and a "UI" client at 90×28, and measured the window size (`#{window_width}x#{window_height}`)
after each attached, under different `window-size` settings. tmux 3.5a.

## Result (verified)

| setting | window after user (200×50) | after UI (90×28) attaches | verdict |
|---|---|---|---|
| **default (`window-size latest`)** | 200×48 | **90×26** | ✗ **DISTURBED** — UI shrank the user's view |
| **`window-size largest`** | 200×48 | **200×48** | ✓ SAFE — UI attach left the user's view unchanged |

So a naive attach is exactly the "looks-fine-but-wrong" failure the project forbids: opening the UI
terminal would silently shrink the user's real panes. **`window-size largest`** fixes the size axis —
the larger (user) client governs the window; the smaller UI viewer just sees a cropped viewport
(xterm.js scrolls/letterboxes). A UI viewer *larger* than the user would grow the window (additive,
content-preserving — acceptable, and only when the UI is genuinely bigger).

## The second axis: window *selection*

Size isn't the only shared state — a tmux **session has one current window shared by all its clients**.
Attaching with a window target (or `select-window`) would move a user viewing another window of that
session. Fix: attach a **grouped viewer session** (`tmux new-session -t <session> -s <viewer>`) — it
shares the window *list* but has its **own** current window, so the UI can point at the thread's window
without moving the user. Killing the viewer session on disconnect is safe (the windows belong to the
group / original session).

## Applied to production

Both fixes are in the real terminal endpoint (`internal/daemon/terminal.go`, exp 07):
1. `set-option -g window-size largest` before attaching (size safety).
2. a **grouped viewer session** per connection, pointed at the thread's window, killed on disconnect
   (selection safety).

The 1:1 case (one thread = one session = one window, the default) is trivially safe with these; the
grouped session makes the **shared-session placement modes** (`--into-session`/`--into-window`) safe too.

## Caveats / not covered

- Two clients viewing the *same* window at *different* sizes is fundamental to tmux (one character
  grid) — `largest` is the least-bad resolution, not perfect isolation. True per-viewer sizing would
  need the UI to render its own pane (out of scope).
- Not tested: the user actively *typing* while the UI also drives the same pane (input interleaving) —
  a UX question, not a safety one. Noted for the product.

Artifact: `probe.mjs`.
