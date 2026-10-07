# Deployment and live-rig operations

Read for deployment or machine/service work, not every coding task. Current hard
rules live in [AGENTS.md](../AGENTS.md); detailed dated evidence is indexed in
[ENGINEERING_INDEX.md](ENGINEERING_INDEX.md). Old deploy recipes are not evidence
that a machine is still at that revision, offline, or awaiting that fix.

## Service boundary (H75)

**Never restart a production daemon by hand, except Termux's documented path.**
Use `supervisorctl restart sesh-daemon` on the other five machines. The supervisor
ini supplies `SESH_API_ADDR`, `SESH_API_TOKEN_FILE`, PATH (including agent shims),
SHELL, and `SESH_TMUX_CONF`; a shell's environment is not a substitute.

A hand-started `sesh daemon run` without API configuration silently drops inbound
mesh access. It also holds the Unix socket, so supervisor retries fail and can go
FATAL. Outbound sync may still look healthy in local `mesh`/`doctor`; it does not
prove peers can reach this machine. H75 recorded nine hours of such an outage.
Heed the daemon/doctor no-API warning. Termux is the intentional inbound-less leaf.

## Deployment checklist

1. Inspect machine identity via `$MYRIG_MACHINE`, git branch/status and revision;
   use `ssh-target` for machine access. Preserve concurrent/uncommitted work.
   Do not pull into or build a release from someone else's dirty checkout.
2. Distinguish **binary-only** client changes from **daemon-side** changes requiring
   rebuild AND supervised restart. Explain schema/API mixed-version compatibility.
   For store migrations: append only, rehearse on a consistent copy; take pre/post
   `VACUUM INTO` backups and verify row counts. Never experiment on the live DB.
3. Build per-machine with its native toolchain; install via `.new` + rename rather
   than overwriting a running binary in place. Verify `vcs.revision` and
   `vcs.modified=false`; historically linked worktrees lacked Go's VCS stamp—verify
   rather than assuming one. A clean clone was the measured alternative.
4. Install a binary supporting new config **before** rendering that config (H115).
   Restart only when needed, respecting in-flight work. Re-source tmux confs when
   bindings change. A running sidebar retains its old binary AND config until
   `prefix+r`; a deployed binary is not a refreshed sidebar.
5. Verify actual artifacts and daemon health, inbound API, mesh, schema and
   observable behavior. Daemon-exec paths need a supervised live smoke: tests
   inherit dev-shell PATH/keys and cannot prove the production environment.
   Do not resize the user's panes with an attached test viewer.
6. Report every machine's deployed/pending status explicitly. Never infer deployment
   from a commit or pull alone. Installed external skills may be GitHub **copies**,
   not symlinks: check them when skill sources change (H112). Skill refreshes and
   pushes require the task's authorization; they are not part of a doc-only pass.

## Termux exception

Build with **plain native `go build`** (CGO=1 / GOOS=android). A CGO-disabled Go
resolver cannot resolve tailnet MagicDNS there; a working binary can still drop
its machine off the mesh (H22). No supervisor, no inbound API/token.

Build/install BEFORE stopping the old daemon. Read its **own reported PID** with
`sesh daemon status`, verify ownership/executable, and terminate only that PID;
never pattern-kill. The normal recovery is a fresh login's myrig zshenv guard.
Verify the new reported PID and `/proc/<pid>/exe` inode, not an old `(deleted)`
executable. A command line containing the guard's own `pgrep -f` pattern can fool
it into thinking a daemon still exists (H93).

If a manual relaunch is required on Termux, the established exception is
`setsid nohup ~/.local/bin/sesh daemon run` with `SESH_HOME=~/.sesh`,
`SESH_MACHINE=termux`, `SESH_TMUX_SOCKET=sesh`, `SESH_MASTER_SOCKET=sesh-master`.
Redirect logs into `$HOME` (not unwritable `/tmp`), detach, then verify health and
those four environment values. Do not apply this recipe to supervised machines.
See H121 for the newer explicit-PID/fresh-login deploy evidence and the archived
trap digest for the original recipe.

## Shell / renderer / phone traps

- Never trust the status of `cmd | tail` as the status of `cmd`; inspect the actual
  build/render result. Zsh word splitting/globs differ from bash; `status` is a
  read-only special variable. Pass external paths as argv, never interpolated code.
- myrig's `scripts/install-home.py` needs the **full** `$MYRIG_TARGETS` list: a lone
  machine name can delete `all`-gated symlinks (H33). Rendering uncommitted templates
  ships them too. A piped SSH script's `npx` needs `</dev/null` or it consumes the
  remaining script (H115).
- Mesh reachability is not SSH reachability (H101); stale ControlMaster sockets
  can refuse a session while the HTTP API is healthy (H94).
- Android can hide non-dumpable `ssh-agent` processes even from same-UID `/proc`/
  `ps`. Termux-only memory counts missed a ~1.9 GB leak (H108, corrected follow-up
  3). Read corrections before attributing phone pressure to another app or sesh.
