# Mesh, peers, cockpit, daemon operations, and environment

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
## Mesh, peers, mycockpit, daemon

**mycockpit** — also "the cockpit" / "my cockpit" — is Lukas's cross-machine tmux cockpit:
one tmux server (socket `sesh-master`, prefix `C-a`) with one window per machine, each an
auto-reconnecting attach into that machine's work server. sesh builds and drives it
(`sesh master …`); myrig wraps it in the `mmt-*` commands. ("The master tmux setup" is the
retired old name.)

It has two **levels**, and Lukas uses these words: the **master** level is the cockpit's own
server (`sesh-master`, prefix `C-a`, cross-machine — pick a machine, then act), the **base**
level is one machine's work server (`sesh`, prefix `C-b`, this machine). myrig's `mmt-*`
commands act at the master level, `mt-*` at the base level.

```bash
sesh peer list                                             # registered machines + transport
sesh peer add --machine macbook --ssh lukas@macbook --home /Users/lukas/.sesh \
  --api-addr 100.x.y.z:7070 --api-token-file ~/.sesh/api-token   # http peer (ssh otherwise)
sesh master up --tmux-conf ~/.sesh/myrig/tmux.master.conf  # build the per-machine window cockpit
sesh master attach        # attach to it      sesh master watchers   # who's watching this machine
sesh daemon status        # machine, pid, version, uptime, db, socket, schema, mesh_cadence
supervisorctl restart sesh-daemon  # authorized production restart; NEVER shell-start it
sesh doctor               # diagnose the install (binary, config, SESH_MACHINE, daemon checks)
```

**codex's shared app-server daemon is kept OFF.** codex ≥ 0.157 starts one detached
app-server per codex home from every interactive `codex`, and it keeps a conversation's
writer lock after its pane is killed — so `sesh thread stop` then a headless turn / resume
fails "already has an active writer", and `thread adopt` cannot identify the pane. The sesh
daemon therefore writes `[features] daemon_auto_start = false` into the codex home's
`config.toml` at startup and before every codex launch (a surgical edit: every other key and
comment is kept; a config it cannot parse is refused, never rewritten). It overrides an
explicit `true` — loudly, in the log and in `sesh doctor`. `sesh doctor` shows three rows:
`codex daemon` (the setting), `codex daemon pin` (an override or edit failure, if any), and
`codex app-server` — a daemon ALREADY running for that home (started before the pin) still
holds locks until stopped: `CODEX_HOME=<home> codex app-server daemon stop` (when no codex
pane is mid-turn), then kill its leftover `… daemon pid-update-loop` updater (pid in
`<home>/app-server-daemon/daemon-updater.pid`), which `daemon stop` does not stop.

The target machine's supervised daemon is the sole creator of its work tmux server. If a
master window finds no sessions, it asks the target daemon to create `scratch`; it does not
run `tmux new-session` in the local or SSH attach shell. This matters on macOS because tmux
retains its creator's audit session: a server born under SSH cannot read Claude Code's login
Keychain even when a local cockpit later attaches to it. A daemon-born work server keeps the
Aqua service context. Raw interactive SSH is still Keychain-isolated and may require Claude
`/login`; the cockpit works because its panes run inside the Aqua daemon-born work server.

**Status options (for tmux status lines and scripts).** The owning daemon stamps every pane
that carries a `@sesh-thread-id` marker with its thread's record fields as tmux PANE user
options, kept current within a tick: `@sesh-name`, `@sesh-agent`, `@sesh-tags` (comma-joined,
empty when none), `@sesh-archived`, `@sesh-flagged`, `@sesh-flag-disabled` (each `1` or empty).
A status line renders the thread row with format lookups alone — no `#()` job, no shell per
redraw — e.g. `#{?@sesh-name,sesh: #{@sesh-name} [#{=8:@sesh-thread-id}] · #{@sesh-agent},}`;
an unmarked pane renders nothing. Read them with `tmux -L sesh show-options -p -t <pane> -v
@sesh-name`. They are pane-scoped only (tmux inherits user options during format expansion, so
never read them at window/session scope), and they are data published by the daemon, not
something to set by hand. This is the portable status-line contract; a copied skill
does not need the source repository's design documents.

**"A machine's threads vanished from my TUI."** Almost always that machine is
*unreachable*, not thread-less: offline machines' threads are hidden by default
(the TUI's toggle-offline command reveals them, and the footer names the machine). Check `sesh mesh`
from another machine — the affected box's own view stays green because outbound sync
keeps working, so diagnose from the OUTSIDE. Then run `sesh doctor` on it and read the
`api` line: `ok listening on <ip>:<port>` is healthy; `fail … NOT BOUND` means the bind
keeps failing (DNS/interface — the error is quoted); `warn SESH_API_ADDR not set` means
the daemon has no TCP API at all, so peers cannot reach it — normal only for an
inbound-less leaf like termux, and otherwise a daemon started by hand without its
service environment (fix: `supervisorctl restart sesh-daemon`; the same warning is
logged at daemon startup).

**"The cockpit froze after my laptop slept — I can select threads but nothing opens."**
A master window is an ssh attach into that machine's work server, and sleep can leave that
connection dead with no FIN and no RST. ssh notices only when it next has bytes to send,
which an idle attach never does, so the window keeps painting its last pre-sleep frame.
Nav still *reports success* — the far side's sshd still holds the pty, so the remote tmux
still lists that client, the master-client marker still matches it, and `switch-client`
returns 0 against a client nobody can see. sesh now passes `ServerAliveInterval`
keepalives on every ssh it opens, so a dead path is dropped within ~45s and the window's
supervisor re-establishes it by itself. **A running cockpit keeps the binary it was
launched with**, so after updating sesh you need `mmt-kill && mmt-start` (or
`sesh master down` + `up`) once before the keepalives are actually in force. If a cockpit
is wedged right now, that same restart is the recovery — rebuilding the sidebar
(`prefix+r`) will not help, because the rot is in the window attaches, not the sidebar.

**Mesh sync cadence (demand-driven).** The background peer sync runs at full pace (~1s)
only while something is consuming the mesh view — a `sesh tui`/sesh-ui poll or an
`--all-machines` read — or when `[[hooks]]` are configured (hooks observe remote threads
through the cache, so they pin full pace). Otherwise it idles to `[mesh] idle_interval`
(default 60s; `"0s"` = never idle) and snaps back instantly on the next read, so opening
the TUI after an idle stretch is fresh within ~a round trip. `sesh peer list` showing
"synced 45s ago" on a quiet daemon is therefore deliberate idling, not degraded sync —
`sesh daemon status` reports the pace as `mesh_cadence` (active / idle / hooks-pinned /
always). Between schema-41 daemons each sync round transfers only the rows that CHANGED
since the last one (delta sync; an unchanged round is ~100 bytes), and against older
daemons an unchanged snapshot is a bodyless 304 (ETag). What the views SHOW is unchanged
by any of this — every machine's full thread set, archived included, still replicates
across the mesh.

## Common flags & environment

- `--json` — machine-readable output (use it when scripting).
- `--machine <m>` — route to a peer (real ssh hop or its HTTP API; not for peer/matrix/master).
  An **http** peer is reached THROUGH your local daemon (it reuses the daemon's warm
  connection to that peer), so routing to an http peer needs the local daemon running —
  if it is down you get "routing to <m> goes through the LOCAL sesh daemon, which did not
  answer"; and right after a sesh upgrade, "the LOCAL sesh daemon has no /v1/route" means
  the daemon was not restarted yet. ssh peers need no local daemon.
- `--all-machines` — fan a read out across the mesh.
- `SESH_HOME` (default `~/.sesh`), `SESH_MACHINE` (this machine's identity — the daemon
  refuses to run without it), `SESH_THREAD_ID` (the current thread, for inference),
  `SESH_REMOTE`/`SESH_API_TOKEN` (target a remote daemon's TCP API directly),
  `SESH_ROUTE_MACHINE` (target an http peer through the local daemon — what `--machine`
  sets for an http peer; rarely set by hand).

Errors are loud by design (an unimplemented or impossible request fails explicitly rather
than degrading to a plausible-but-wrong result) — read the error; it usually tells you the
exact precondition that failed (e.g. a 409 "thread has no live pane" on `send` to a dead
thread).

## Production service and Termux safety

The `sesh daemon start|stop|restart` mechanism exists, but it is **not** the fleet's
production restart recipe. On every machine except Termux, the service manager
owns `SESH_API_ADDR`, `SESH_API_TOKEN_FILE`, PATH, SHELL, and `SESH_TMUX_CONF`.
Hand-starting a daemon can remove inbound mesh access and hold the socket that
supervisor needs, leaving supervisor FATAL. Use its service manager when authorized.

Termux alone is an inbound-less leaf with no supervisor/API. Build/install with
plain native `go build` (CGO=1 for Android DNS) **before** stopping the old process.
Read the daemon's own reported PID (`sesh daemon status`), verify ownership and
executable, and terminate only that PID. Prefer a fresh login's myrig zshenv guard
for relaunch; verify the new PID and `/proc/<pid>/exe` inode (not `(deleted)`). A
command line containing the guard's `pgrep -f` pattern can fool that guard.

Only if the normal Termux guard cannot relaunch, its established manual exception
is `setsid nohup ~/.local/bin/sesh daemon run`, detached with logs under `$HOME`
(not unwritable `/tmp`), and explicit `SESH_HOME=~/.sesh`, `SESH_MACHINE=termux`,
`SESH_TMUX_SOCKET=sesh`, `SESH_MASTER_SOCKET=sesh-master`. Verify environment and
health afterwards. Never apply this exception to supervised machines. Never use
broad process-pattern kills, including when cleaning Codex updater processes.

For sandbox experiments, strip inherited `SESH_*`, set isolated `SESH_HOME`,
`SESH_TMUX_SOCKET`, `SESH_MASTER_SOCKET`, and `SESH_CODEX_HOME`, and invoke the test
binary by absolute path. Never default to live state or attach a test viewer to
user panes. A test's teardown owns only its own PIDs/sockets; process death can
leave tmux/agents alive, so verify cleanup explicitly.
