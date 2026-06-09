# sesh v2 — master-tmux (design)

The cross-machine "cockpit": a single tmux server with one window per machine, each an
auto-reconnecting attach into that machine's work server. `sesh tmux nav` jumps you across
machines by driving it. **All of this infrastructure lives in sesh** — building the master,
the per-window ssh-attach, and the reconnect loop. myrig only *configures* (a tmux conf) and
*aliases* (`mms-*` → `sesh master …`); it never sources sesh's internals. The seam is the
CLI/process boundary.

> This supersedes the earlier "keep the master frame + reconnect loop in myrig" framing. The
> reconnect loop and frame construction were mechanism in disguise.

---

## 1. The two tmux servers

```
MASTER server   tmux -L $SESH_MASTER_SOCKET (e.g. sesh-v2-master)   ── sesh builds AND drives
  session "master":
    window "mymain"    → attach into mymain's WORK server        (local, no ssh)
    window "macbook"   → ssh -t macbook  → attach macbook's WORK server
    window "macstudio" → ssh -t macstudio → attach macstudio's WORK server
  one session, one window PER MACHINE, window-name == machine-name,
  each window = an auto-reconnecting attach (a Go supervisor) into that machine's work socket
                              │  nav OUTER: select-window -t <machine>   (internal/tmux/nav.go)
                              ▼
WORK server   tmux -L $SESH_TMUX_SOCKET (e.g. sesh-v2), one per machine   ── sesh owns (built)
  sessions sesh_<thread>, each a pane running the real agent, tagged @sesh-thread-id
                              │  nav INNER: switch-client -t =<session> (+ kick fallback)
```

Both servers are sesh's. The work server is already built/green; the master server is the
new piece.

---

## 2. sesh surface — new subcommands (`sesh master …`)

All infrastructure is a Go subcommand (so a tmux window runs *the binary*, never a sourced
script). Machine list is sourced from the daemon's peers (`internal/peers` + the mesh).

- **`sesh master up [--machines <list>] [--tmux-conf <path>]`**
  Create the master server: one session `master`, one window per machine, `window-name ==
  machine-name`, each window launched running `sesh master window <machine>`. Sets
  `automatic-rename off` / `allow-rename off` so names stay == machine (structural minimum
  only). Machines default to **self + the daemon's known/reachable peers**; `--machines` is an
  optional policy filter myrig may pass. `--tmux-conf` is the look/keybindings file myrig owns
  (applied via `-f`/`source-file`; sesh never bakes in styling).

- **`sesh master window <machine>`** — the per-window supervisor each master window runs.
  A long-lived **Go** process (NOT a shell `while` loop) that:
  - resolves `<machine>` from the peer registry → ssh dest (`Peer.SSH`+`Peer.SSHArgs()`),
    remote work socket (`Peer.TmuxSocket`); for self → local `cfg.TmuxSocket`, no ssh;
  - runs the attach: `env -u TMUX tmux -L <work-socket> attach` (local) or
    `ssh -t <dest> 'env -u TMUX tmux -L <remote-work-socket> attach'` (remote);
  - **reconnect/self-heal**: when the attach exits (laptop sleep, ssh blip, remote tmux
    restart, or no session yet to attach to), re-establish with backoff — indefinitely. The
    window thus never dies; only its inner attach churns.

- **`sesh master attach`** — attach the human to the master server (`tmux -L <master> attach
  -t master`).

- **`sesh master down`** — tear the master server down.

Naming: top-level `sesh master` (could alternatively nest under `sesh tmux`); pick one and
keep it stable — myrig aliases bind to it.

---

## 3. Conventions are now sesh-internal (no cross-repo contract)

Because sesh both builds and drives the master, the old 3-point "contract" (window-name ==
machine; each window attached to the work socket; human attached to the master) collapses into
implementation details of `master up` + `nav`. Nothing for a separate builder to satisfy or
drift from.

---

## 4. myrig surface — config + aliases ONLY (no orchestration, no sourcing)

- **tmux conf** `.tmux.master-tmux.conf`: prefix `C-a`, keybindings that dispatch to `sesh
  tmux nav` / `sesh master …`, status-bar/tab-strip **styling**. Passed to sesh via
  `master up --tmux-conf`.
- **Aliases**: `mms-attach`→`sesh master attach`, `mms-start`→`sesh master up`,
  `mms-kill`→`sesh master down`.
- **fzf pickers**: fzf over `sesh list` / `sesh tmux info`, choice piped to `sesh tmux nav`.
  Presentation only.
- **Clipboard/image-paste** (`prefix+P`): rebuilt on `sesh tmux current` + `tmux stage-file`
  + `tmux send-text`.

**No shell-sourcing channel.** myrig calls `sesh …` and reads its JSON. The "command a window
runs" is `sesh master window <machine>`, not a sourced script. (sesh may ship an *example*
keybindings snippet as docs — never runtime infrastructure.)

### Disposition of the current `master-tmux.sh` (~1,205 lines)
- **Delete** (sesh mesh/state replaces): `mms-session-poller`, `mms-refresh-cache`,
  `mms-list-sessions`, all `~/.cache/mms/*` caches, `_mms_machine_reachable`, nav history
  (`_mms_history_*`), flagged-cycle (`_mms_cycle_flagged`, `mms-prev/next-flagged`,
  `mms-last-session`).
- **Moved into sesh**: nav/kick cluster (`_mms_navigate`, `_mms_goto_machine`,
  `_mms_kick_window`, …) → `sesh tmux nav` (done); frame build + `_mms_run_on_machine`
  + `mms-machine-session-loop` reconnect → `sesh master up` / `sesh master window`.
- **Kept (thin)**: the aliases, the `.tmux` conf, the fzf pickers, the clipboard cluster,
  `_mms_current_machine` → `sesh tmux current`.
- Net: ~1,205 → ~150–250 lines, zero tmux/ssh orchestration.

---

## 5. Honesty / conformance (these are infrastructure → tested like everything else)

- **`master.up`** — `sesh master up --machines self,<ssh-localhost-peer>` builds the windows;
  assert via real `tmux list-windows` (names == machines) and that each window is genuinely
  attached into *that machine's work socket* (`list-clients` / the window's attached session),
  not internal state. The peer window is a real `ssh localhost` attach.
- **`master.reconnect`** — kill a window's attach (and, separately, the remote tmux server)
  and assert the supervisor **re-establishes** it; both the local and the ssh-localhost-remote
  window, real processes. Test the drop→heal in both directions.
- Register as matrix / TUI-conformance-style cells. A `Skip` stays loud and never counts as
  done.

Open detail: when a machine's work server has no sessions yet, `attach` fails — the supervisor
retries with backoff until one exists (or optionally holds a placeholder shell). Decide during
build; cover with a cell either way.
```
