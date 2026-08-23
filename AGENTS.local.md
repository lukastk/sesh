# AGENTS.local.md — sesh v2 working notes

> **Naming (2026-08-23).** The cross-machine tmux cockpit is now called **mycockpit** —
> or just **the cockpit** / **my cockpit**. Entries below predate that and call it "the
> master tmux setup", "the master", or "the master cockpit"; they are a dated record and
> are left as written. Use the new name in anything new. Code identifiers (`sesh master`,
> `SESH_MASTER_SOCKET`, `mmt-*`, `tmux.master.conf`) are unchanged either way.

## H85 — LOCAL MAC COCKPIT CLAUDE LOOKED LOGGED OUT: the local master was NOT SSHing, but its long-lived WORK tmux server had been CREATED by a remote SSH cockpit and retained that audit session; fix = target daemon is sole work-server creator (2026-08-18, sesh c550644 + myrig 119ae59; CLI/config change, no schema change; 4/6 deployed, Mac work-server replacement still pending)
Lukas: Claude Code works in a normal macbook terminal but says `Login expired · Please run /login`
inside his local master cockpit; self-SSH reproduces it. His correction was exactly right: the local
master window does NOT SSH to its own machine. The missing distinction was ATTACHER versus CREATOR.
The local window attaches directly to `tmux -L sesh`, but that server survives every client and every
later attach in the security context of the process that originally created it.

MEASURED on both Macs with `sudo launchctl procinfo`, not inferred. macbook: Dock and supervised
`sesh-daemon` shared Aqua audit session 100022; master tmux also had 100022 (Ghostty), but WORK tmux
PID 1155 had SSH audit session 100071 / responsible `sshd-keygen-wrapper`. It was born at 11:55:29,
SEVEN SECONDS before the Aqua daemon at 11:55:36: an always-on remote cockpit won the empty-socket
race. macstudio had the same split: Dock/daemon/master 100003, WORK tmux PID 1497 in SSH audit session
100065. A direct SSH Keychain payload read and a command inside either SSH-born work server both fail;
normal Aqua terminals work. Therefore self-SSH Claude remaining logged out is EXPECTED macOS Keychain
isolation and is not fixed here. The local cockpit will work once its work server is daemon-born.

ROOT CODE BUG: `sesh master window` used to run raw `tmux new-session -s scratch` whenever an attach
found no sessions. For a remote machine that command runs inside SSH; `tmux` then permanently carries
the wrong audit session even after a local client attaches. It also duplicated `SESH_TMUX_CONF` into
`peers.Peer.TmuxConf`, making the attaching client a second server owner.

FIX (sesh c550644): `master window` no longer creates raw tmux. On an empty socket it invokes the
TARGET machine's `sesh tmux create-session --name scratch` with explicit target `SESH_HOME`, machine,
and socket; that CLI calls the already-supervised target daemon, and only the daemon creates the work
server. Failure is loud and the existing window supervisor retries. Removed `Peer.TmuxConf`,
`peer add --tmux-conf`, help/docs, and the myrig peers field. myrig 119ae59 renders that smaller peer
record and documents the Aqua/creator boundary. The sesh-cli skill now says explicitly: raw SSH Claude
may remain Keychain-isolated; a local cockpit works because its panes live in the Aqua daemon-born
server.

HONEST TESTS. New `master.remote-work-context` uses real `ssh localhost`, starts an empty peer whose
daemon alone has `SESH_TEST_DAEMON_CONTEXT=peer-daemon`, and asserts the real work tmux global env has
that sentinel. It was RED against old production (`unknown variable`) and GREEN after the change.
Focused matrix: `master.holding`, `master.remote-work-context`, `master.up`, `tmux.work-conf` = 4/4
green; the 71-second `master.reconnect` cell is green. New command-string unit guards are green; every
non-conformance package is green plain and `-race`; `go vet ./...` is green. The FULL 247-cell matrix
did NOT complete and must not be called green: the default run timed out at 10m in unrelated
`thread.flagged/claude/remote`; a second unchanged run with `-timeout 30m` timed out while waiting for
unrelated `thread.send.headless/pi/remote`. Neither emitted a failed assertion or final grid.

DEPLOY STATE at log time: mymain, macstudio, ideapad, and termux have myrig 119ae59 + clean native
sesh c550644 binaries (`vcs.modified=false`), current daemons, rendered peers without `tmux_conf`, and
master cockpits rebuilt so their long-lived window supervisors run the new code. Supervised machines
were restarted only via supervisor; termux's exact old daemon PID 16971 was validated, killed, and
relaunched with the documented setsid environment as PID 16841. macbook and pocket4 did not answer
Tailscale/SSH and are explicitly PENDING. Also intentionally PENDING: replacing the ALREADY-RUNNING
SSH-born Mac work servers. Macstudio PID 1497 still holds four sessions and multiple managed/unmanaged
live panes; killing it would interrupt user work, so deployment did not pretend to mutate its immutable
audit session. Once quiescent, stop/resume managed threads, replace the work server, and let the Aqua
daemon create it; do the same on macbook after it wakes and receives the binaries. Future empty-server
creation is then protected by this change.

## H84 — THE termux cockpit killer IDENTIFIED AND FIXED: Android's PHANTOM PROCESS KILLER (cap 32); cause = pocket4 made the fleet SIX machines (2 procs each) so the idle cockpit crossed 32; fix = adb `device_config` bump max_phantom_processes → 2^31-1, all six machines KEPT (2026-08-11, NO code change — device setting; H83's "NOT DETERMINED" is now RESOLVED)
Follow-up to H83, which shipped the wake-lock fix but stated (correctly) that it was PARTIAL and that
the phantom process killer "could not be confirmed or excluded" without adb. Lukas rebooted, restarted
the master, and it was culled AGAIN — so I did the full diagnosis with adb this time. Lukas's constraint:
"I don't really want to limit the amount of machines my master is connected to" — so `mmt-start
--machines a,b` (H83's lever) was off the table; the fix had to keep all six windows.
WHAT H83 GOT RIGHT AND WHAT IT COULDN'T SEE: the cohort-reaping observation (39→20 in one 10s sample,
only setsid-detached survivors) was correct, but I had FOUR wrong theories before adb, each killed by
its own evidence — memory pressure (the cockpit died with **4.8GB free / swap 65%**, a healthy phone),
sleep/network (Tailscale **direct, pong 185ms AT the moment of death**), the wake lock (held since boot,
didn't save it — exactly as H83 predicted), and H81's ssh keepalives burning CPU (**62→64 CPU ticks in
8 minutes** — nothing; and mymain's OWN cockpit shows the identical offline-peer ssh churn for 5h with
no harm, so it is normal fleet-wide behaviour, not a phone regression). All four are recorded here so
nobody re-runs them.
THE DISCRIMINATOR I couldn't get until adb: `oom_score_adj` of the dying processes stayed at **50**
(PERCEPTIBLE_RECENT_FOREGROUND — a PROTECTED class) right through the kill. A process at adj=50 is NOT
an LMK target. That is the tell: the phantom process killer does NOT consult app adj — it culls an
app's CHILD processes by COUNT against a fixed cap, independent of memory, priority, sleep, or wake
locks. Every negative result falls out of that at once.
DIRECT CONFIRMATION (adb paired over Tailscale — the android-control skill; pairing had lapsed, needed
a fresh pair + a wireless-debugging OFF/ON toggle before the device left `offline`):
  `dumpsys activity settings | grep phantom`  →  **max_phantom_processes=32**
  `dumpsys activity processes | grep PhantomProcessRecord`  →  the cockpit itemised, e.g.
     `PhantomProcessRecord {…:6980:sesh/u0a440}` — 18 of ~28 phantoms were the cockpit, parent 6980 =
     the Termux app, uid u0a440. So Android tracks EVERY termux-forked process as a phantom under the
     app, caps the set at 32, and trims the excess. No longer inferred — read from AM itself.
THE ARITHMETIC OF "WHY NOW" (this is the part H83 half-had): each cockpit machine costs **2 phantom
processes** — a `sesh master window` supervisor + its `ssh -tt`/attach. Fleet history: 4 machines →
ideapad → 5 → **pocket4 (2026-07-28) → 6**. That took the IDLE cockpit from ~23 to ~27 against the cap
of 32; ordinary activity on top (widget shells, a menu popup, boxyard's 15-min rclone sync, an inbound
ssh) then crosses 32 ROUTINELY instead of never. Nothing on the phone changed — Android build Mar 2026,
Termux 0.118.3 unchanged, no sesh/myrig regression. The fleet outgrew the 32-child budget and the
failure surfaced ~2 weeks later looking like it came from nowhere. Termux binary got H81's keepalives on
2026-08-08 (H82 deploy), which is a red herring that coincidentally lines up.
THE TRIGGER IS ACTIVITY, which explains the phenomenology ("dies ~5min after I OPEN it"): crossing 32
ARMS the killer; the trim is scheduled lazily on process-start / app-state events. So opening the app
(the widget shell, mmt-start, the attach client, each a phantom) is what pushes it over, and the trim
lands minutes later. LIVE-PROVEN: a master that had survived 49 minutes idle died within 90s of a
6-process diagnostic census I ran. The cascade to "everything dies at once" is the tmux SERVER being a
victim → SIGHUP to every pane's process group → all supervisors+ssh+attach fall together (33→16 in one
sample); when sshd is the victim instead, the phone drops off ssh entirely (hit that twice, cost ~5min
of access each time).
FIX (device setting, NO code change, keeps all six windows — the ONLY option that removes the cliff
rather than stepping back from it):
  adb shell settings put global settings_enable_monitor_phantom_procs false   # belt+braces; AM reads
      the effective toggle from DeviceConfig, so on THIS build the global flag alone did NOT clear the
      list — the max bump below is what AM actually reflected.
  adb shell device_config set_sync_disabled_for_tests persistent              # so phenotype re-sync
      can't revert the next line (MUST precede it)
  adb shell device_config put activity_manager max_phantom_processes 2147483647
VERIFIED LIVE in AM's own constants: `dumpsys activity settings` →
`max_phantom_processes=2147483647` (was 32). Nothing can exceed 2^31-1, so the killer never trims.
NB `device_config put/override activity_manager settings_enable_monitor_phantom_procs` is
ALLOWLIST-BLOCKED (SecurityException "must add flag to the allowlist") — the max_phantom_processes flag
is the writable, older one and is what worked.
PROOF (adb monitor, run as the `shell` user uid 2000 — deliberately NOT the ssh-into-Termux polling of
H83, which kept Termux warm and confounded every earlier run; adb shell does not touch Termux's phantom
accounting): full SIX-machine cockpit, master pid 3809, sampled every 60s, HANDS OFF for 26 minutes.
  age 01:15 … 07:23 (phantoms=33) … 12:29 (phantoms=33) … 26:46 — SAME pid throughout, termux_procs
  peaked at 43, phantoms sustained at 32-33 (ABOVE the old cap), **killed_records=0 the whole time**.
Before the fix an identical 6-machine cockpit died by ~7min (1m46s under memory pressure). It sailed
straight through every prior death point. FIXED.
NOT VERIFIED, and it's the one open item: REBOOT DURABILITY. `set_sync_disabled_for_tests persistent`
is designed to survive reboot and device_config overrides persist, but I could not confirm it on this
Pixel 9 / Android 16 build without a reboot (which would kill the live cockpit under test). If it
reverts after a reboot, re-applying is the 3 adb lines above; a durable path is to grant Termux
`WRITE_SECURE_SETTINGS` (`adb shell pm grant com.termux android.permission.WRITE_SECURE_SETTINGS`) and
re-assert at boot — but note the max bump is device_config, not `settings global`, so the grant only
covers the (weaker, this-build-ineffective) global toggle; the robust re-apply still needs adb. Lukas
to reboot when convenient so this can be checked.
SESH FOLLOW-UP (defense-in-depth, NOT the fix, only worth it if he ever wants the cap back on): the
supervisor layer is 6 long-lived Go processes whose only job is respawn-with-backoff. tmux can own that
via `remain-on-exit on` + a `pane-died` hook → a short-lived `sesh master respawn-pane`, halving the
per-machine cost 2→1 (idle cockpit ~27→~21). Must preserve H81's exit-driven reconnect (the
blackhole-wedge cell guards it) and the marker contract (marker write already rides the remote command
string, survives). Real design + conformance work — a ticket, not something to do unprompted.
ADB/ANDROID TRAPS (for next time): pairing lapses (Android revokes unused debug auth) → re-pair AND
toggle wireless-debugging OFF/ON or the device sticks at `offline` after a successful pair; the connect
port rotates but pairing persists; `dumpsys`/`settings get`/`device_config get` for phantom flags all
return null/denied to an untrusted app over ssh — you MUST have adb (shell uid 2000) to read them;
`tmux -L … list-windows` via adb shows 0 windows because uid 2000 can't read Termux's socket (use ssh
as the termux user for that). This is a device-config change, so there is nothing to commit but this
log entry.

## H83 — termux master cockpit dies ~2min after every `mmt-start`: ANDROID REAPS THE WHOLE COHORT; myrig held NO wake lock (boot script released it 2s in); fix shipped is PARTIAL and known not to be sufficient on its own (2026-08-10, myrig 35e309b; NO sesh change; render-only deploy, ALL SIX)
Lukas: "My master tmux setup is constantly dying on my termux. Every time I open it, it seems to die
after around 5 minutes or so."
MEASURED, not inferred (the method is the reusable part): a 10s sampler on the phone recording
`ps -e -o pid=,etime=,args=` plus mem/swap. One sample caught the whole thing:
  19:12:26 total=35  — cockpit healthy, every process aged 01:35
  19:12:36 total=20  — EVERY cockpit process gone, inside ONE 10s window
Killed as one cohort: the master tmux server, ALL SIX `sesh master window` supervisors, ALL FIVE peer
`ssh -tt`, the work server AND its attach. Survivors were exclusively the **setsid-detached** ones
(sesh daemon, sshd, crond) plus shells started later. Cockpit lifetime: **1m46s**. This is Android
reaping a process cohort — nothing in tmux or sesh failed, and no sesh code is implicated.
THREE CONTRIBUTING FACTORS, only one of which is a bug we own:
(a) **The phone is out of memory.** 11.5GB RAM ~9GB used, **swap 100% EXHAUSTED (5785/5785 MB)**,
    MemAvailable ~1.3-1.8GB. Under that pressure Termux's forked children are the softest target on
    the device. This looks like the DOMINANT factor.
(b) **The cockpit is by far Termux's biggest cohort.** Baseline termux = **12** processes; with the
    master up = **39-45**. The cockpit alone costs ~14: 1 master server + 1 window supervisor PER
    MACHINE (six) + 1 `ssh -tt` per peer (five) + work server + attach. `mmt-start --machines a,b`
    ALREADY EXISTS (mmt-start forwards "$@" to `sesh master up`) — trimming needs NO code change.
(c) **termux held no wake lock at all** — the only myrig bug here. `home/^termux^.termux/boot/
    start-sshd` did `termux-wake-lock; sshd; sleep 2; termux-wake-unlock`, and the lock is a single
    **GLOBAL, NON-REFCOUNTED** flag, so that one unlock disarmed it device-wide — including for the
    sesh daemon, whose zshenv block acquires a lock *specifically* to stop Android reaping it. The
    zshenv's own acquisition sits INSIDE `if ! pgrep -f 'sesh daemon run'`, which in steady state
    never runs because the daemon is already up. Net: no lock, ever.
FIX SHIPPED (myrig 35e309b): boot script no longer unlocks (with a comment saying why it must not come
back — removing an unlock reads as a leak unless you know the flag is global); `mmt-start` acquires,
guarded to termux with sidebar-toggle.sh's detection (`TERMUX_VERSION` or `SESH_MACHINE == termux`)
and `command -v`-checked. Deliberately NOT put in the zshenv unconditionally: that is a termux-api
round trip (~100ms+ and extra child processes) on EVERY shell incl. every ssh command, on a system
already being reaped for having too many processes.
**HONESTY — THE FIX IS PARTIAL AND I PROVED IT IS NOT SUFFICIENT:** I had accidentally acquired a wake
lock at 19:02 with a diagnostic, and `termux-battery-status` confirms termux-api is healthy on this
box, so a lock was almost certainly **held during the 19:12:36 cull**. So (c) removes a real cause and
is required for the daemon, but on its own it will NOT stop the reaping. Do not report this as fixed.
Next lever is (a)/(b), which are Lukas's calls: free memory (a reboot; swap is pinned at 100%), and/or
`mmt-start --machines mymain,macbook` on the phone.
NOT DETERMINED, and why: Android 12+'s **phantom process killer** (default cap 32 children) could not
be confirmed or excluded — `settings get global settings_enable_monitor_phantom_procs` throws
SecurityException (INTERACT_ACROSS_USERS) on Android 16 from termux, `cmd device_config` reports "Can't
find service", and `logcat` shows ONLY Termux's own app logs without READ_LOGS, so there are no
am_kill/lmkd records to read. One data point argues against a strict 32-cap being the trigger: a
40-child test took the total to 53 and they survived well past the cap. Settling it needs adb
(`adb shell settings put global settings_enable_monitor_phantom_procs false`).
`oom_score_adj` of the termux daemon reads **0** (foreground class) throughout, with oom_score 668.
TRAPS HIT (all previously documented, all bit me again):
- **`pkill -f <pattern>` matched my own ssh shell** and killed it mid-command (H22/H74). The shell's
  cmdline contains the pattern. Kill by explicit pid.
- **A backgrounded `&` inside an `ssh-target <m> '...'` command swallows ALL output** — even the
  foreground echoes before it. Ship the script with `scp` and run it as a file instead.
- **A heredoc passed through `ssh-target '...'` gets mangled** by the intervening shell layers. Same
  fix: scp the script.
- `/proc/loadavg` is **permission denied** on Android (silently emptied my first sampler).
- My own 40-child phantom test is what reaped **sshd**, costing ~5 minutes of access to the phone;
  nothing restarts sshd but a Termux session (its zshenv runs it) or a reboot.
DEPLOY: render-only, NO daemon restart, NO sesh binary change. The boot script is SYMLINKED into the
repo so on termux a `git pull` is its whole deploy — but it only runs at boot, so the lock was also
acquired by hand once. shell.sh is RENDERED jinja → install-home on each machine. All six at 35e309b
(mymain, termux, macbook, macstudio, ideapad, pocket4), `zsh -n` clean, guard verified to fire for
both termux signals and skip elsewhere. NB install-home takes the FULL comma `$MYRIG_TARGETS` (a lone
machine name deletes symlinks — H33), and must not be piped to `tail` (H30).

## H82 — A CLAUDE BACKGROUND AGENT STAMPED ITS CONVERSATION ONTO A DIFFERENT THREAD: bg processes INHERIT SESH_THREAD_ID from whoever started claude's daemon; fix = hook skips bg sessions + daemon refuses a foreign-cwd session stamp (2026-08-06, sesh <this commit>; NO schema/CLI change; binary + supervised daemon restart, and the HOOK is picked up live)
Lukas: "can you help me find where the sesh bd2d0b3c went? the agent said it had somehow had its sesh
id changed... I close the session and try to reattach but I'm now getting error messages."
SYMPTOM: `sesh thread resume` on bd2d0b3c (`ituc-evaluate-board-contract`) failed with sesh's
confirmAgentLaunched (H34) surfacing claude's own refusal: `Session 9112de36... is currently running
as a background agent (bg)`. The thread record was intact the whole time — not archived, 10 children.
ROOT CAUSE, and it is an ENV-INHERITANCE bug, not a sesh-logic bug: claude 2.1.222 runs one
machine-global `claude daemon run` plus a pool of pre-forked `bg-spare` processes. That daemon is
started by whichever claude first needs it and inherits THAT process's environment. On mymain it had
been started by the **soogun-doc** thread's claude (pid 1017217 — visible verbatim in the daemon's own
`--spawned-by` field), so the daemon AND every spare it forks carry `SESH_THREAD_ID=86304b66`
(soogun-doc). Unix env is frozen at exec, so it can never be corrected. When the ITUC agent
self-compacted at 09:43, claude moved the conversation into a bg agent = a claimed spare — which then
reported to sesh under soogun-doc's id, and H62's stamp rule ("a DIFFERING stored id is CORRECTED")
wrote the ITUC session `9112de36` onto **soogun-doc's** record. Net: bd2d0b3c kept the stale
`61ec0ba2` (frozen 19:11) while the live conversation ran to 21:02 in `9112de36`; soogun-doc pointed
at a stranger's conversation and would have resumed into it.
DISCRIMINATING ORDER (reusable): grid-JSON'd the id FIRST — it EXISTED, so not H63/H40 view filtering
and not H35 offline-hiding; then transcript lineage in the project dir showed 61ec0ba2 and 9112de36
carrying IDENTICAL hourly message counts from 09:43 to 19:11 and only 9112de36 continuing after (two
files, one conversation = a migration, not a fork); then `claude agents --json` named 9112de36
`kind: background`; then `/proc/<pid>/environ` of the hosting process gave the whole answer in one
line. **TRAP:** the hosting pid's `cmdline` still reads `claude bg-spare --bg-spare …` (a spare is
claimed AFTER exec), so grepping ps for the session id finds NOTHING and the agent looks dead — it is
not. `claude agents --json` is the only honest inventory; the `rv/<id>.sock` under
`/tmp/cc-daemon-<uid>/<x>/` is the registration and it ACCEPTS connections even with no pty socket.
NB `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` was still set in that daemon's env — H65 removed it from
settings.json, but a daemon started BEFORE that keeps it forever, and 2.1.222 has bg agents as a
first-class feature regardless. **So H65's fix does not cover this and the SKILL's claim that a bg
session "is never stamped" was exactly backwards — it is stamped, onto the wrong thread.**
FIX, two layers because the precise signal is a claude-internal env var:
(a) HOOK (`integrations/claude/sesh-agent-state.sh`): report NOTHING when `CLAUDE_CODE_SESSION_KIND=bg`
— same reasoning as the existing subagent guard. A bg session's thread id is foreign, so its busy/idle
is as wrong as its session stamp; there is nothing it may legitimately say.
(b) DAEMON BACKSTOP (`reportstate.go` + `claude.ForeignProjectDir`), which survives that env var being
renamed: claude's project dir is a pure function of cwd, so a reported session whose transcript sits
under a DIFFERENT project dir than the thread's cwd is HARD PROOF the reporter is not this thread's
agent. Refuse the stamp, loudly, keep the stored id.
TWO DESIGN CHOICES THAT ARE THE WHOLE POINT:
- **One-directional evidence.** Only a positive "foreign" result may refuse. Not-on-disk-yet (a race),
  under-cwd, and unreadable-home all read as "no contradiction" and still stamp. A false positive
  costs one un-corrected id (pre-46 behaviour, self-healing); a false negative is corruption.
- **Refuse the STAMP ONLY, never the lifecycle event.** A thread whose recorded cwd drifts from where
  its agent actually runs (`thread new --into-pane` on a pane that has since cd'd) would otherwise
  lose busy/idle tracking too. The evidence is about session ownership; that is all it may veto.
COVERAGE IS CLAUDE-ONLY, deliberately and documented in the code, not silently narrowed: codex
rollouts are indexed by session id ALONE (no cwd signal to check against) and pi has no background
mode and a stable `--session-id`. If either grows one it needs its own arm.
TESTS: `TestForeignProjectDir` (evidence truth table incl. every must-read-as-no-contradiction case);
`TestReportStateRefusesForeignSessionStamp` (the incident in miniature — foreign stamp refused, the
lifecycle half still applied, a SAME-cwd compaction drift still corrected, an unwritten transcript
still stamped); `TestClaudeHookSkipsBackgroundSession` (every mapped event silent under the bg marker,
each with a CONTROL run first so an empty log cannot pass vacuously — the H80 lesson).
ANTI-GAMING (all reverse-edited, never git-checkout — H44; all `-count=1` — H75): hook guard neutered
=> RED printing the exact bug (`--id tid-inherited … --agent-session bg-sess`); daemon guard `&& false`
=> RED `stored id = "sess-foreign" — a foreign conversation's session id overwrote the record`;
evidence scan `&& false` => RED. GREEN: every non-conformance package plain AND `-race`; `go vet`;
real-agent cells `thread.state-authority` 4/4 (claude+pi x local+remote — proves the hook still
reports in-pane and the backstop refuses nothing legitimate) and `thread.flagged/claude` 2/2.
`thread.codex-session-capture` FAILED ONCE then passed 3x — a real-codex flake; codex is logically
exempt (`kind != Claude` returns before any check), so its stamp path is byte-identical.
RECOVERY PERFORMED for Lukas before the fix (the runbook if it recurs): the bg agent still holds the
conversation, so `--fork-session` it. Here, done sesh-natively with no claude run at all: stamp the
thread with the bg session id, `thread new --fork-from <thread>` (H79's `rewriteClaudeBranch` gives the
copy a DISTINCT uuid root, so it cannot resolve back into the lock), re-stamp the thread with the
fork's id, delete the temp fork RECORD (the transcript stays). Verified byte-complete: same 2295 lines,
1888 messages, same final timestamp. Then `resume` worked and soogun-doc was re-stamped to its real
session `a14eb8d1`. Cross-checked every thread on the mesh afterwards: none pinned to a live bg session.
NOT DONE, deliberately: the offending `claude daemon run` (pid 2653752) still carries the stale
SESH_THREAD_ID and still hosts FOUR live bg agents across three boxes, so killing it is Lukas's call —
these guards make a future bg agent harmless without it, which is the point.
DEPLOY: no schema/API/CLI change. The **hook is re-read by claude per event**, so a `git pull` of the
sesh checkout makes layer (a) live in already-running claude sessions with NO restart (the H65 property).
Layer (b) is in the daemon => rebuild + supervised restart per machine.
DEPLOY RESULT (2026-08-08, commit e7fd83b): live on ALL SIX machines, every installed binary
`vcs.revision=e7fd83b` + `vcs.modified=false`. mymain/macbook/macstudio/ideapad/pocket4 rebuilt natively
(macs via /opt/homebrew/bin/go) and restarted ONLY through supervisor; each `sesh doctor` shows `api
listening on <tailnet>:7878`. termux rebuilt with PLAIN `go build` (verified CGO_ENABLED=1 / GOOS=android
per H22), old daemon killed by EXPLICIT pid 2157, relaunched setsid-nohup as pid 28399, schema 46 — logs
to $HOME since /tmp is unwritable. Mesh healthy after all six restarts (all five peers reachable).
LIVE-SMOKED against the real supervised mymain daemon: a disposable headless thread with the soogun box
as cwd, reported with the ITUC session id, was REFUSED with the record byte-unchanged and the loud line
in the supervisor stderr log; scratch thread deleted. The recovered thread bd2d0b3c stayed headful with
its agent running across the daemon restart (the daemon never touches the work tmux server).

## H81 — POST-SLEEP COCKPIT WEDGE (macbook-only): the master window's ssh had NO keepalive, so a silently-dead path never made the attach EXIT — and the supervisor reconnects only on exit; fix = ServerAliveInterval on every ssh sesh opens (2026-08-04, sesh <this commit> + myrig <this commit>; NO schema change; BINARY-ONLY but REQUIRES a master restart to take effect)
Lukas: "If my computer goes to sleep, my master tmux setup with sesh freezes and a lot of the time it
never recovers. The sidebar is there and I can try to select different threads but it doesn't open
anything... The main way I get it to recover is by just restarting it and running `mmt-start`."
**This is H71's still-open item #1, finally explained.** H71's decisive discriminator (prefix+r does
NOT clear it, mmt-start does ⇒ the rot is in the MASTER's structures, not the sidebar) was right and
pointed exactly here: the wedged thing is the per-window **attach process**, which only mmt-kill/
mmt-start destroys.
ROOT CAUSE, two halves that combine into a silent failure:
(a) `masterWindow` (cmd/sesh/master.go) re-establishes ONLY when `cmd.Run()` returns — its comment
claims it covers "laptop sleep". It doesn't. The ssh was spawned with `-tt -o BatchMode=yes -o
StrictHostKeyChecking=no` and NOTHING else; there was no `ServerAliveInterval` in sesh, in
`~/.ssh/config`, or in `/etc/ssh/ssh_config` — verified on macbook AND mymain, both ends. ssh learns
a peer is gone only when it next has bytes to SEND, and a master attach into an idle work server has
none for hours. So a path that dies with no FIN and no RST (sleep; network changing underneath)
leaves ssh blocked forever ⇒ the supervisor never re-runs ⇒ the window paints its last pre-sleep
frame indefinitely. OS TCP keepalive is not a backstop: ~2h idle on macOS and Linux both.
(b) NAV STILL REPORTS SUCCESS, which is why there was no error to go on. The far side's sshd keeps
the pty open, so the remote tmux still lists that client, so `master-client.<origin>` still matches
it by name AND pid — and that `list-clients` membership is the ENTIRE liveness contract in
`InnerSwitchScript`/`MarkerClientCurrent`. So nav resolves the dead client, `switch-client -c`
returns 0, the server-side session really does change, and the bytes go into a dead socket. Exactly
"I select threads and it doesn't open anything", with a green result.
WHY MACBOOK ONLY: it is the machine that sleeps (mymain/ideapad/macstudio are always-on). NOT every
sleep — macOS holds TCP across short sleeps (`TCPKeepAlive=active` in the pmset log; all three of
that day's inbound connections from mymain/macstudio/termux survived intact), which is precisely
Lukas's "a lot of the time". DISCRIMINATING EVIDENCE, in the order that cracked it: no keepalive
anywhere on either end (config, not inference) → `pmset -g log` showed sleep/wake at 17:06→17:08,
17:35→17:44, 17:59→17:59 with the live master session created 18:00:00, i.e. the mmt-start he ran
because of this → live `list-clients` + markers + `ps` pairing of every ssh against its far-side
sshd session. **TIMEZONE TRAP that nearly produced a false positive:** mymain logs `lstart` in UTC
and macbook in BST, so a genuinely-paired connection looked like an unpaired 1-hour-old orphan. Pair
ssh processes to sshd sessions by CONVERTING first, or you will "find" wedges that aren't there.
FIX: `peers.SSHKeepaliveArgs()` (ServerAliveInterval=15, CountMax=3 ⇒ ~45s) as the single source of
truth, wired into (1) the master-window attach — the load-bearing one, since its EXIT is the
supervisor's only reconnect trigger; (2) `nav --attach` (same long-lived-interactive hazard);
(3) `SSHMultiplexArgs` — `ConnectTimeout` bounds only the HANDSHAKE, and a ControlMaster established
before the sleep is long past that, so a routed command riding a dead mux socket hung with NO
timeout at all (the nav inner switch over ssh has no context deadline). Deliberately errs toward
disconnecting: a false positive costs one sub-second reconnect via the existing backoff, a missed
detection costs a cockpit wedged for hours. myrig adds the same two options to `home/.ssh/config`'s
`Host *` for every OTHER outbound ssh (interactive `ssh-target` sessions wedge identically);
command-line `-o` beats the file so sesh and the config never fight (verified with `ssh -G`).
TEST — the point of the whole entry: `master.reconnect` was GREEN throughout and could never have
caught this, because it drops the attach with `tmux detach-client` = a CLEAN exit, the one shape the
supervisor already handled. Its green is what made MASTER.md's "laptop sleep" claim look tested. The
cell now also does a WEDGED drop: a `blackholeRelay` (in-test loopback TCP relay) that on `Freeze()`
stops forwarding both directions on the connections open at that moment and **never closes them** —
no FIN, no RST — while still relaying connections accepted AFTER the freeze (the network came back
on wake; only the pre-sleep socket is dead — without that the supervisor could never reconnect and
the test would prove nothing). Two things that would have made it vacuous: (i) it asserts the MARKER
file now names a DIFFERENT, LIVE client, NOT a client count — the zombie stays listed on the work
server (only the far machine's sshd reaps that), so a count assertion passes trivially; asserting
the marker is also asserting exactly what nav resolves, i.e. whether the next thread selection
lands. (ii) the relay must keep every frozen `net.Conn` REFERENCED — a GC'd net.Conn is finalized
CLOSED, which would send the very FIN the relay exists to withhold. ANTI-GAMING (reverse-edited,
never git-checkout — H44; `-count=1` — H75): `SSHKeepaliveArgs` → nil turns the cell RED naming the
exact user report ("peer window did not re-establish after a BLACKHOLED connection: marker … still
"/dev/ttys028 39094" after 2m0s — the attach never exited"). Green in 71s, red at the full 131s.
GREEN: master.up/reconnect/holding, tmux.nav, tmux.nav-in-client, tmux.nav-window, mesh.snapshot
(+.http), route.parity (+.http); `go vet ./...`; all non-conformance packages plain and `-race`.
PRE-EXISTING REDS ON MACBOOK (verified NOT mine on a clean detached worktree at 03c2fc7, byte-
identical messages): `TestMaintainerDropsStaleReportedBusy` "baseline: busy=idle authority=" — the
H75 red, still unfixed; and THREE nav cells that all need a nested DIRECT `tmux attach` viewer which
will not materialise on this box — `tmux.nav-in-client-multi/-/local` ("expected 2 work-socket
clients, got map[]"), `tmux.nav-master-http/-/remote` ("direct client never parked"),
`tmux.nav-master-multi/-/remote` ("direct client never attached"). H79 got 246 green on another
machine, so treat these as macbook-environment reds (same class as H80's TUI-claim reds) — but they
are REAL reds here and nobody has looked at the viewer-attach helper on macOS.
NOT DONE, deliberately: the far side never reaps the zombie client (no `ClientAliveInterval` in any
machine's sshd_config), so a stale `master-client.*` marker can outlive its origin — visible as a
false `sesh master watchers` entry (which feeds mmt-copy-to-master's auto-detect) and as an extra
client with a stale size on the work server (the H69 sizing class). Harmless for the reported bug,
because once the origin reconnects it rewrites the marker to the live client. Left alone because it
means sudo-editing sshd_config + reloading sshd on five machines, which risks locking Lukas out of
one — his call, not a thing to do unprompted.
DEPLOY: binary-only, NO daemon restart, no schema/API/CLI change. **But a running master keeps the
binary its supervisors were launched with, so the fix is inert until `mmt-kill && mmt-start` (the
H70 lesson, now for the master rather than the sidebar).**

## H80 — TUI SEARCH silently missed every CHILD thread (uuid AND name): the filter's children-exclusion DEFAULT; fix = default to INCLUDING children, ^y now opts INTO the exclusion (2026-08-02, sesh <this commit>; NO schema change; BINARY-ONLY, no daemon restart; NOT YET DEPLOYED)
Lukas: "check that the uuid search in sesh tui actually works? I tried searching for
ef79e834-cffd-49d9-b9e7-8683d9916eae which does exist, but it doesn't come up." It did not work, and
the cause was NOT the uuid machinery.
ROOT CAUSE: `visibleMatches()` dropped every row with `Parent != ""` whenever a query was active,
unless ^y (`filterChildren`) was toggled on. `ef79e834` is an ITUC worker nested TWO deep
(`53c1a2e3 corkboard` → `219068e9 ituc-supervisor` → the target), so it was filtered out before it
could ever rank. The exclusion applied to the **uuid target too** — and a uuid is an exact,
unambiguous identifier, so a uuid search returning nothing is simply wrong. The comment justifying it
("children of a tree are usually noise when searching by name") is defensible for NAME search and
indefensible for uuid; with supervisor/worker trees, most interesting threads are children, so NAME
search was equally blind (live: `rededup` → 0 matches before, 1/36 after).
DISCRIMINATING ORDER (worth reusing): grid-JSON'd the id FIRST — it existed, `archived`/`on_hold`
both null (so not H63/H40 view filtering) and `parent` non-empty = the answer in one command; then
`sesh mesh` showed mymain reachable (so NOT the H35 offline-hide); then `fuzzyScore(uuid,uuid)`
matched in a unit test (so NOT the matcher). Only the children default was left.
FIX: the field is RENAMED `filterChildren` → **`filterExcludeChildren`** with inverted sense, so the
**ZERO VALUE is the new default**. This is the load-bearing design choice: setting it in `New()`
(the `hideOffline`/`maxColWidth` pattern) would leave a struct-literal `Model` — which is how nearly
every tui unit test builds one — searching DIFFERENTLY from the shipped TUI. H39 already recorded
that divergence as a footgun; this avoids adding another. `^y` now toggles the exclusion ON (prompt
renders `children:on` by default). Lukas chose this over my narrower "exempt the uuid target" —
correctly: it is simpler and it fixes the name-search case too.
WHY THE CLAIM NEVER CAUGHT IT: `filter-target-uuid` built only FLAT threads (`threeRowModel`), so a
green cell said nothing about nesting. The claim now reparents beta→alpha and gamma→beta on a REAL
daemon and searches gamma's FULL uuid. **VACUOUS-PASS TRAP I walked into and had to fix:** my first
version settled only on `len(Rows())==3`, which was already true from the pre-reparent snapshot — the
uuid then matched because the row was still top-level, proving nothing (caught because the paired ^y
assertion failed while the search "passed"). It now settles until the MODEL's own row carries
`Parent == betaID` before asserting, and asserts ^y makes it disappear again so the match cannot pass
vacuously. Re-learns the H40 rule: settle on the SPECIFIC state you are testing, not a proxy count.
TESTS: unit `TestFilterIncludesChildThreadsByDefault` (rewritten contract) + new
`TestUUIDSearchFindsNestedThread` (the reported shape, 2 deep, plus "an unrelated uuid still matches
0" so the fix can't degrade into a wildcard). ANTI-GAMING (reverse-edited, never git-checkout — H44;
`-count=1` — H75): flipping the predicate back turns the units RED naming `uuid search for a 2-deep
nested thread returned 0 rows` and the claim RED naming `full uuid of a 2-deep NESTED thread did not
match it (the reported bug)` — the exact user report. GREEN: tui units + `-race`, `go vet ./...`,
full TUI claims suite serially (157s).
PRE-EXISTING REDS (verified NOT mine on a clean detached worktree at 9b6da0a, identical messages,
and STILL red after rebasing onto H79's 02b1e50): `action-fork` — `no transcript on disk for this pi
conversation`, the pi-transcript class H77 recorded for `thread.model/pi/local`; `uuid-popup-copy` —
the claim stubs `wl-copy`, a WAYLAND tool, and I am on macOS, so the clipboard tool is never invoked.
**NB these are TUI CLAIMS, a SEPARATE suite from the 246 matrix cells** — so H79's all-green matrix
does not cover them, and its Claude-fork fix does not touch `action-fork`, which forks a PI thread.
Both are macbook-environment reds; neither was repaired here. All 7 filter claims pass on the rebase.
CONCURRENT SESSION (the recurring lesson, hit again): H79 landed on origin/main while this was in
flight and ALSO numbered itself H79 — renumbered mine to H80, rebased, kept both entries. Always
fetch before pushing; the first `git fetch` here failed transiently (exit 128) and a second succeeded,
so do not read one clean fetch as proof there is nothing upstream — diff HEAD..origin/main.
LIVE-PROVEN against the real daemon (isolated tmux, read-only, throwaway binary since deleted; never
pressed Enter — that would revive a live thread): the exact reported uuid renders the worker row at
default settings, and ^y hides it again; `rededup` name search 1/36 where it was 0.
DEPLOY: binary-only, NO daemon restart, no schema/API change (pure TUI-client filter). Committed and
pushed to origin/main; **NOT YET DEPLOYED — the fleet still has the old default**, so search there
still hides children until each machine rebuilds. A running sidebar keeps the binary it launched with
(H70), so a deployed machine also needs `prefix+r` (or mmt-kill/mmt-start).

## H79 — H77's eight baseline reds were two product defects plus stale/racy conformance setup; all 246 cells green (2026-08-02, sesh <this commit>; NO CLI/API/schema change; binary rebuild + supervised daemon restart required; NOT DEPLOYED)
H77's delivery fix was independently cleared by the parent, committed, deployed on the approved quiescent machines recorded in H77, and then followed by an explicit triage of the eight matrix reds that had reproduced on untouched `8022ed3`. None was caused by bracketed-paste delivery. The eight split into two product defects covering five cells and three fixture defects covering three cells; the honest fixes below restore the original assertions rather than narrowing any matrix axis.

CLAUDE FORK (2 cells, real defect): sesh's deliberate fork changed `sessionId` but copied Claude's top-level message UUID graph unchanged. `ResolveLeafSession` deliberately follows shared-root UUID lineage for Claude's native resume/rewind copies, so it could not distinguish the intentional branch: the branch retained a post-fork `FELDSPAR` turn and continuing it mutated the source. `rewriteClaudeBranch` now gives `uuid`, `parentUuid`, and `logicalParentUuid` a deterministic per-destination UUID-v5 graph while setting `sessionId`; nested content and tool IDs stay untouched. Thus native same-root resolution still works, while two sesh branches have distinct roots. A unit test pins graph consistency, source immutability, unchanged content containing the old session string, and distinct roots across two branches. The real Claude local+SSH-remote cells passed 3/3 each.

REMOTE AWAIT (3 cells, product + fixture race): H56's deliberate full-UUID fast path in ordinary `resolveIDPrefix` skips a list read because the eventual owner verb returns a 404. `await` is mesh-read-only and has no later owner request, so applying that fast path there let an as-yet-unreplicated exact UUID through; its first poll then falsely said the thread had “vanished.” `resolveMeshThreadID` now validates an exact UUID against the local list or replicated mesh, while ordinary routed verbs retain H56's fast path. Unit tests cover exact local, exact remote, and loud unknown UUIDs. The e2e row now observes the thread in the awaiter's actual mesh, then observes its real busy transition there, before invoking `await`; this replaces producer-side sleeps with the state the command consumes. All three remote cells passed 3/3, and the full six-cell row passed together. A first broad run exposed the same missing precondition in the Pi/local one-second timeout edge (`headlessReply.Working` preceded the mesh busy publish); waiting for mesh `busy` fixed it, and that exact real Pi case passed 3/3.

THREE FIXTURE REDS: Pi 0.83.0 no longer offered the two Anthropic aliases hard-coded by `thread.model/pi/local`; the cell now uses the real, disjoint catalog entries `google/gemini-flash-lite-latest` and `google/gemini-flash-latest`, and fails directly on an agent error before transcript inspection. It passed 3/3. `tmux.nav/-/remote` put the real OpenSSH `%C` control socket below `t.TempDir()`, whose generated path plus OpenSSH's temporary suffix exceeded Linux `sun_path`; it now uses the harness's existing short isolated home and the real SSH path passed 3/3. `master.selfheal/-/remote` created its initial peer window explicitly before the healer's replicated `Reachable=true` precondition existed, then killed it; the test now observes that exact mesh bit before testing recreation, and passed 3/3. Product behavior and assertions for those three cells are unchanged.

BROAD-GATE FIXTURE FOUND AFTER THE ORIGINAL EIGHT: one full run reached 245/246 because real Claude asked “Which color do you prefer?” instead of preserving the test prompt's phrase “red or blue”; sesh correctly stored `flagged=true` and that real question as the reason. The cell now asks Claude to preserve a unique nonce in the actual `AskUserQuestion` field and asserts that nonce traverses the real hook/daemon/store path. This is stricter provenance without depending on model paraphrasing; the exact cell passed 3/3.

VERIFICATION: `go test ./internal/fork ./internal/agents/claude ./cmd/sesh -count=1`; every non-conformance package sequentially with and without `-race`; `go vet ./...`; repeated focused real-agent/tmux/SSH cells above; and `git diff --check` all passed. The final uncached `go test ./internal/conformance -count=1 -timeout 60m` passed in 2389.879s. Its persisted honest grid is **246 pass, 0 fail, 0 skip, 0 missing, 0 not-run, plus 2 justified N/A**.

LIVE/DEPLOY: no machine runs H79 yet. Read-only reconfirmation found the supervised mymain daemon RUNNING as PID 2287373 from clean installed revision `6cb3762474ce` (which includes H77/f02 but not H78/H79); no live pane was focused, typed into, captured, or interrupted, and no daemon was restarted. H79 changes no API/wire/schema, so mixed versions are compatible. Safe rollout after active workers are quiescent: build/install natively per machine, restart daemon-backed machines only through their service manager (termux uses its documented explicit-PID leaf recipe), then smoke-test disposable Claude fork divergence and local/remote await. Offline or busy machines must remain explicitly pending.


## H78 — GUTTER GLYPH VOCABULARY: ⌀ flag-disabled was indistinguishable from ⊘ archived (adjacent cells); fix = ⌁ + drop the pin • + a confusable-FAMILY drift guard (2026-08-02, sesh <this commit>; NO schema change; BINARY-ONLY, no daemon restart)
Lukas: "I think there are some duplicate icons... the icon disabling flagging and the icon for archiving is
the same." CORRECT, and it was the worst pair in the gutter. `⌀` (U+2300 DIAMETER SIGN, flag-disabled) and
`⊘` (U+2298 CIRCLED DIVISION SLASH, archived) are DIFFERENT CODEPOINTS but render as the same slashed circle
in a terminal font — and they occupy DIRECTLY ADJACENT gutter cells, so an archived flag-disabled row read
as one doubled symbol (`⊘⌀`). Full gutter inventory (cells: `> ` mark head busy desc att arch flag): ↕/• ·
●◌◇─? · ▶·? · ↓ · * · ⊘ · ⚑⌀. Nothing reused an identical rune for two meanings (`?` is head-unknown AND
busy-unknown, but that is ONE semantic on two axes — deliberately left).
CONFERRED (AskUserQuestion, both answered): flag-disabled → **⌁** (U+2301 ELECTRIC ARROW — his pick over my
recommended ⚐ white flag); **pin marker • REMOVED entirely** ("we don't need an icon for pinned, it's clear
visually if they are" — pinned rows already render as a block ABOVE the auto block, so POSITION is the
signal); fold markers ▸/▾ vs busy ▶ left ALONE (his call — different region, different role).
CODE: FlagGlyph ⌀→⌁ (+ a comment naming the ⊘ adjacency so nobody "tidies" it back); pinMark reduced to the
move-mode ↕ only (signature now `pinMark(_ api.ThreadRow, moving bool)`); the stale View comment that
claimed the pin marker was `▏` (it was `•`) fixed; help binding text + colors.go GlyphFlag doc + SKILL
(glyph list, keymap, manual-ordering paragraph) synced.
THE DURABLE PART — **TestGutterGlyphsDistinct**, a drift guard, because DISTINCT-RUNE CHECKING WOULD NEVER
HAVE CAUGHT THIS BUG (⊘ and ⌀ *are* distinct). It collects every gutter glyph and refuses (a) two states
sharing a rune, (b) two states drawing from the same **confusable FAMILY** — a blocklist of shapes a
terminal renders alike at one cell: slashed circle `⊘⌀⊗∅Ø⦸`, dot `·•∙‧⋅`, right triangle `▶▸►▹`, vertical
arrow `↕↑↓⇕`. NB ● is deliberately NOT in the dot family (`●·` is the documented headful-idle signature and
reads fine). SCOPING DECISIONS written INTO the test rather than silently narrowing it: the NAME-column fold
markers are out of scope (Lukas signed the ▶/▸ overlap off), and `mark/moving` ↕ has an explicit
`familyExempt` entry vs "vertical arrow" (transient one-row mode marker, three cells from the persistent ↓).
**THE EXEMPTION BUG WORTH REMEMBERING:** my first exemption was keyed by STATE alone, so exempting
`mark/moving` waved it past EVERY family — a neuter setting the move marker to `•` PASSED. Re-keyed to
`state|family`; the same neuter now fails on the dot family. A per-item exemption must name the ONE rule it
skips, never "all rules".
CONFORMANCE: claimActionPin could no longer assert the `•` marker, so it now asserts the honest observable
the removal leaves — POSITION. It seeds a SECOND thread `aaa-top` that sorts above `pinme` while unpinned,
asserts that baseline FIRST (a rise proves nothing without it), then that `p` lifts pinme above it and `u`
drops it back. Strictly stronger than the marker check. action-flag claim's render assertion ⌀→⌁.
ANTI-GAMING (all reverse-edited, never git-checkout — H44; all with -count=1 — H75): ⌁→⌀ ⇒ guard RED naming
`[arch/archived ⊘ flag/disabled ⌀]` = the exact reported bug; move-marker→• ⇒ RED on the dot family; a stale
`familyExempt` key ⇒ RED. GREEN: tui units + -race, cmd/sesh (help meta-tests), FULL TUI claims suite
serially (142s), thread.pin + thread.divider cells local+remote.
LIVE-PROVEN in an isolated tmux (own SESH_HOME/sockets, every inherited SESH_* stripped, sandbox daemon
killed by EXPLICIT pid + tempdir removed; live daemon pid 1223 verified untouched): five threads covering
every state rendered `⊘ ` / `⊘⌁` / ` ⌁` / ` ⚑` / blank — the archived+flag-disabled row is now two visibly
different symbols — and a pinned row rose to the top of the `all` view carrying NO marker.
DEPLOY: **binary-only, NO daemon restart, no schema change** (pure TUI-client render). NOT YET DEPLOYED —
the fleet still renders ⌀ and •. A running sidebar keeps the binary it launched with (H70), so a deployed
machine also needs `prefix+r` (or mmt-kill/mmt-start) before the sidebar shows the new glyphs.

## H77 — long single-line headed sends could fill Codex's composer while their Enter was suppressed (2026-08-01, sesh f02e4d9; NO CLI/API/schema change; binary rebuild + supervised daemon restart required; DEPLOYMENT AUTHORIZED 2026-08-02)
Lukas reported that several child/supervisor reports during the live ITUC cycle appeared in the parent Codex composer in full but did not start a turn until he focused the pane and pressed Enter. This was NOT H76's concurrent shared-buffer cross-delivery bug: the worktree began clean at `8022ed3`, the installed binary and supervised mymain daemon were exact `dbbc9ce` (`vcs.modified=false`, PID 4136119), and H76's unique buffers were active. No live ITUC pane was captured, focused, typed into, or otherwise used as a reproducer.

LIVE ROUTE/SHAPE: the reported parent and child had NO persisted subscription edge; their parent field is hierarchy/grouping, not delivery. The production route was the collaboration layer's direct headed `thread send`. Read-only transcript metadata showed the three report turns immediately before Lukas's complaint were 1279, 1226, and 1090 bytes, each with ZERO newlines. That distinction is decisive: `SendText` bracket-pasted only multiline text, but sent a single line through `tmux send-keys -l` and slept 250ms before a separate Enter. The formatted persisted-subscription route is multiline and already used bracketed paste, which is why it did not reproduce the same failure under the old code.

DETERMINISTIC REPRO: a new isolated test starts the real Codex 0.146.0 in a real temporary tmux/daemon fixture, delivers a production-shaped 1299-byte single-line report via the real `thread send` route, and requires both a busy transition and the complete message in Codex's transcript. Against the OLD `dbbc9ce` transport it failed 3/3: the isolated pane visibly contained `[Pasted Content 1024 chars]` plus the literal report tail and end sentinel, while Codex remained idle and the transcript lacked the turn. That is the exact user-visible state; a later human Enter submits it.

ROOT CAUSE: `tmux send-keys -l` emits the long rapid stream as raw key input, and successful return only means tmux enqueued it — it does not acknowledge that Codex consumed the tail. Codex 0.146's raw paste-burst detector can collapse the first 1024 characters, then treat the remaining raw tail as a new active burst; Enter arriving in its 120ms paste-Enter suppression window is consumed as paste/newline handling rather than submission. The fixed 250ms delay was producer-side timing, so load between tmux's PTY queue and Codex could make it expire before Codex processed the tail. Codex's explicit bracketed-paste handler, by contrast, clears the transient paste-burst state before the next key event. This explains all observations: complete text visible, no turn, and a later manual Enter working.

FIX: `tmux.Server.SendText` now sends EVERY nonempty prompt through its H76 process/call-unique buffer and `paste-buffer -p -d`, not literal `send-keys`, then sends Enter immediately after successful paste. Tmux preserves paste-end→Enter byte ordering; an agent that requested bracketed-paste mode receives one explicit paste event, so there is no guessed sleep. The explicit pane target is carried on both commands, buffers remain per-call isolated, a failed paste returns loudly and does NOT send Enter, and there is no retry that could submit stale/unintended text. Empty-text+Enter behavior is unchanged. Direct thread sends, subscription delivery, ticket prompts, and spawn `--msg` all share this one transport seam. No CLI/help/skill surface changed.

TESTS: a real-tmux wire regression makes the pane request bracketed paste and asserts a single-line send arrives exactly as `ESC[200~ + text + ESC[201~ + Enter`; it and the existing multiline/concurrent-buffer regressions passed 10/10 repetitions. The real-Codex direct-route regression sends THREE sequential long reports through one session; two final repetitions accepted all six reports (the old code's anti-test was 3/3 red). A second real-Codex test covers the actual completion/subscription engine twice per session — first into idle Codex, then while the parent is busy — and proves each formatted child result enters the transcript; two final repetitions accepted all four deliveries. The `thread.subscribe` matrix claim now requires a SUBMITTED real-agent turn rather than pane visibility; local and remote cells pass, with the remote test first observing the subscriber in the owner's real replicated mesh cache so completion cannot race initial sync. `thread.spawn-mode/pi/local`, the only broad-run miss sharing startup `SendText`, passed 3/3 isolated repetitions. All non-conformance packages passed sequentially with and without `-race`; `go vet ./...` passed. Do NOT run the normal and race package commands concurrently: their deterministic test tmux socket names collide cross-process and create false `new-session` failures.

MATRIX (honest, final exact-candidate run, `-timeout 60m`): the renderer reported **246 cells — 238 pass, 8 fail, 0 skip, 0 missing, 0 not-run, 2 justified N/A**. Every changed/adjacent delivery row is green: `thread.send.headful` 6/6, `ticket.send-prompt` 6/6, `thread.send-wait` 6/6, `thread.spawn-mode` 3/3, `tmux.send-text` 2/2, and strengthened `thread.subscribe` 2/2. The eight reds are outside `SendText` and repeated broad-suite baseline failures: `master.selfheal/-/remote`; `thread.await/{claude,codex,pi}/remote` (mesh view says the thread vanished); `thread.fork/claude/{local,remote}` (current Claude rewind/source mutation behavior); `thread.model/pi/local` (no transcript on disk); and `tmux.nav/-/remote` (the test's generated SSH control-socket pathname exceeds Unix socket length). The matrix is therefore explicitly NOT all-green. Per the requested gate, this commit is NOT pushed; none of those unrelated surfaces was weakened or repaired in this delivery fix.

INDEPENDENT PREDEPLOY A/B AUDIT (2026-08-02): after the ITUC workers became quiescent, the parent reproduced all eight red cells from the candidate matrix on a separate clean detached checkout of the untouched parent `8022ed3`. The errors matched exactly: overlong generated SSH control-socket path for remote nav; missing peer-window recreation for remote master self-heal; vanished mesh thread for all three remote await cells; Claude source mutation/no rewind for both fork cells; and missing Pi transcript for the local model cell. Therefore none was introduced by H77. On the candidate itself, the parent independently reran the three real-tmux delivery regressions and the real-Codex three-report regression; all passed. Lukas then explicitly authorized the controlled sesh deployment and ITUC rebaseline sequence. The eight reds remain real, tracked defects rather than being reclassified as green.

PREDEPLOY STATE: the supervised live mymain daemon remains PID 4136119 on exact `dbbc9ce`; no daemon was restarted before authorization and no fleet machine ran H77. Build/install per machine, restart only via each machine's service manager (termux uses its documented exception), and smoke-test a disposable headed Codex thread with repeated >1KiB single-line sends. No cross-host binary refresh is required for compatibility because this changes no API/wire/schema. Record the exact reachable-machine deployment result below after verification rather than implying offline machines were updated.

DEPLOY (2026-08-02): H77 is live on mymain, macstudio, and termux from clean `6cb3762` source containing code commit `f02e4d9`; every installed binary reports `vcs.modified=false`. Mymain was rebuilt from a separate clean clone because its ordinary source checkout had unrelated concurrent edits, then restarted only through supervisor (`PID 1248243`). A disposable real headed Codex accepted three consecutive 1,359-, 1,527-, and 1,341-byte single-line reports as actual transcript turns and returned all three requested acknowledgements; the scratch thread was stopped and archived. Macstudio's clean clone fast-forwarded, rebuilt natively, and restarted through supervisor (`PID 84914`). Termux had no threads, rebuilt natively as Android/arm64, and its exact old PID 30422 was validated and stopped before the documented inbound-less leaf relaunch (`PID 2157`). Pocket4 was reachable but had an active worker and unrelated dirty sesh changes, so it was deliberately not touched. Macbook and ideapad timed out over SSH and remain offline. Therefore pocket4, macbook, and ideapad are explicitly PENDING rather than implied deployed.

## H76 — concurrent `ticket send-prompt` calls could paste one ticket into the wrong agent pane (2026-08-01, sesh <this commit>; NO API/schema change; daemon rebuild + supervised restart required)
The first real ITUC production cycle exposed an integrity bug while its supervisor launched two independent Claude workers at the same time. One ticket's prompt appeared in the other ticket's pane, while the second send returned HTTP 500 with `no buffer sesh-send`. The project's mandatory task preflight rejected the wrong prompt before source access or any research/database write. The supervisor preserved a reproducer, paused both launches, then safely resumed them by sending one prompt at a time and capturing the intended pane after each send.

ROOT CAUSE: multiline `tmux.Server.SendText` used the fixed tmux buffer name `sesh-send` for every request. tmux buffers belong to the whole tmux server, not to one pane or HTTP request. Concurrent request A could set the buffer to prompt A, request B could overwrite it with prompt B, and then A would paste B into A's target and delete the shared buffer. B would subsequently fail because the buffer no longer existed. This exactly explains both observed symptoms; the ticket router and thread binding were correct.

FIX: every multiline `SendText` call now gets a process-and-call-unique buffer name (`sesh-send-<pid>-<atomic sequence>`). Each request sets, pastes, and deletes only its own buffer. If paste fails, it makes a best-effort deletion of that request's buffer while returning the original delivery error. Single-line delivery, bracketed-paste behavior, Enter timing, ticket routing, and the public CLI/API are unchanged.

TESTS: new unit coverage creates 512 buffer names concurrently and proves they are unique. A new real-tmux regression starts two isolated target panes, releases 96 multiline sends concurrently, and proves every marker reaches only its intended pane with no error or missing prompt. Against the OLD fixed-buffer implementation this test failed deterministically in under a second with `no buffer sesh-send`; with the fix, the unit and real-tmux regressions passed 10/10 stress repetitions. `go test ./internal/tmux`, `go test -race ./internal/tmux`, every non-conformance package with and without `-race`, and `go vet ./...` passed. The existing `ticket.send-prompt` conformance row passed all six real-agent cells (Claude/Codex/Pi x local/remote). A repository-wide `go test ./... -count=1` reached the package's default 10-minute timeout while `thread.flagged/claude/local` was running; that unrelated exact cell passed alone in 23 seconds. This is recorded as a timed-out broad invocation, not presented as a green full-matrix run.

LIVE-CYCLE RULE: the sequential one-at-a-time delivery workaround is safe and remains mandatory for the frozen ITUC cycle until the fixed mymain daemon is deployed and verified. Deploying sesh does not alter or rebaseline the frozen project checkout, its Board, or its task/ingestion databases. Even after deployment, the supervisor may keep the sequential capture check as an additional operational safeguard.

DEPLOY: commit `dbbc9ce` is live on every reachable machine: mymain, macstudio, and ideapad were natively rebuilt and restarted through supervisor; termux was natively rebuilt with the required `CGO_ENABLED=1` / `GOOS=android`, then its old daemon was stopped by validated explicit PID and relaunched with its documented leaf environment. Every installed binary reports `vcs.revision=dbbc9ce` and `vcs.modified=false`; daemon/API/peer checks passed, with termux's no-API warning expected for an inbound-less leaf. A live mymain-daemon smoke test sent 96 concurrent multiline API requests to two disposable panes: all 192 markers arrived, none crossed targets, and no request errored. The scratch sessions/files were removed. Macbook and pocket4 were offline, so their rebuild/restart is explicitly PENDING. The ITUC supervisor was paused before the mymain restart, then released after verification; its frozen checkout and runtime databases were untouched, and it retains sequential target-capture delivery as extra containment.

## H75 — mymain SILENTLY OFF THE MESH for 9h: a HAND-STARTED daemon (no `SESH_API_ADDR`) + supervisor FATAL; fix = loud startup warning + a doctor `warn` line where there was NO line at all (2026-08-01, sesh <this commit>; NO schema change; binary + daemon restart)
Lukas: "I can't see any of my remote threads on mymain in sesh tui — but `ssh-target mymain` works."
DIAGNOSIS (the discriminating order, worth reusing): `sesh mesh --json` from macbook → mymain
`reachable=false`, synced **33018s (9.2h) ago** (pocket4 too, but that one was genuinely off — tailscale
"last seen 9h ago"). `curl mymain:7878` → **connection REFUSED in 45ms** = network path fine, NOTHING
LISTENING (a timeout would have meant the host/tailnet; the refusal localizes it to the daemon in one
command). On mymain: daemon RUNNING (uptime 5.7h) but `ss -ltnp | grep 7878` empty, and
`/proc/<pid>/environ` had **no `SESH_API_ADDR`** while it DID have `SESH_THREAD_ID=53c1a2e3…` (the codex
`corkboard` thread) ⇒ the daemon was hand-started from an agent pane at 03:13, inheriting that pane's
env instead of supervisor's ini (which is where `SESH_API_ADDR=tailnet:7878` lives). It held
`~/.sesh/daemon.sock`, so supervisor's own restarts died 4× with "a live daemon already listens" →
`startretries=3` exhausted → **`sesh-daemon FATAL`**, nothing self-healing. THE INVISIBILITY IS THE
POINT (H73's asymmetry again, different cause): outbound sync kept working, so mymain's own `sesh mesh`
and `sesh doctor` were ALL GREEN — `doctor` emitted **no `api` line at all**, because the check was
inside `if d.cfg.APIAddr != ""`. Absence of a check read as health. Meanwhile every other machine marked
it unreachable and the H35 offline-hiding default HID its 293 threads — the user-visible symptom.
FIX (mymain, live): `kill <explicit pid>` (never `pkill -f` — H22/H74) then `supervisorctl start
sesh-daemon` → "api listening on 100.106.17.33:7878", all five peers synced ~2s. Also killed a LEAKED
conformance sandbox daemon (`/tmp/seshsb-bin.2lH/sesh daemon run`) that had been ticking a maintainer
loop since **Jul 23** — 9 days; isolated SESH_HOME so harmless, but check `pgrep -af 'sesh daemon'` for
sandbox leftovers after suite runs.
CODE (Lukas: "make it loud, and caution in AGENTS.md"): `noAPIWarning(apiAddr, threadID)` in
apiserver.go — PURE (env passed in) like `offTailnetBind`, unit-tested — logged by `startAPI` on the
previously-silent `APIAddr == ""` early return; it names the consequence peers see, the
invisible-from-here asymmetry, the termux exception, and the fix, and when `SESH_THREAD_ID` is set it
says outright that the daemon was hand-started from that thread's pane (in the real incident it would
have printed the corkboard thread id). doctor.go grows the missing `else`: `api` = **warn** "SESH_API_ADDR
not set …" instead of no line. NOT fatal and NOT inferred from peers.json: an absent API is LEGITIMATE
for an inbound-less leaf, and termux has http peers too, so "has http peers ⇒ should be reachable" would
false-positive — the honest signal is the warning, not a refusal. AGENTS.md gained a hard rule (never
hand-restart a supervised daemon) + sesh-cli SKILL a "a machine's threads vanished from my TUI"
troubleshooting paragraph (diagnose from the OUTSIDE; read doctor's api line ok/fail/warn).
TESTS: unit `TestNoAPIWarning` (silent when configured, names consequence+fix when not, thread id only
when present). `daemon.doctor` cell EXTENDED to assert the unconfigured daemon (its first sandbox was
already exactly that) reports `warn`. ANTI-GAMING: neutered the `add("api","warn",…)` → cell RED with
"no api check in doctor output: [...]" — which is also a verbatim print of the OLD silent behavior —
then reverse-edited back (never `git checkout`, H44). **GOTCHA that nearly fooled me: the first neutered
run PASSED because go test replayed a CACHED result; use `-count=1` for every anti-gaming run.**
PRE-EXISTING RED (not mine, verified on a clean-HEAD worktree): `TestMaintainerDropsStaleReportedBusy`
fails at "baseline: busy=idle authority=, want idle/heuristic" — the H58 test's argv0-"claude"
symlink-to-sleep pane is no longer being resolved as an agent on this macbook. Unrelated to this change;
needs its own look.

## H74 — GitHub issue #10: thread panes spawned by the boot-started daemon have NO graphical session env; fix = spawnEnv injects the systemd user-manager session env per spawn (2026-07-30, sesh 52c58a9 + myrig 6ad1e88 + myrig f7ceabf; NO schema change; sesh binary deployed ALL SIX machines — macbook came last, a few hours after the rest)
Root cause chain (found via Lukas's "ii only shows a handful of apps, some in wrong places" on pocket4): the work tmux server starts with sesh-daemon under supervisord at BOOT, before any graphical login, so it holds ZERO session vars (WAYLAND_DISPLAY, DISPLAY, XDG_SESSION_TYPE, XDG_CURRENT_DESKTOP, XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS, HYPRLAND_INSTANCE_SIGNATURE); every pane inherits the void and Unix env is immutable after exec. `uwsm app` does NOT help — it's systemd-run --scope, caller-env passthrough (verified empirically: it adds nothing from the manager env). From a void pane: Chromium --ozone-platform-hint=auto only picks native Wayland when XDG_SESSION_TYPE=wayland is set; cold-launched `brave --app=URL` windows then come up XWayland with the GENERIC `Brave-browser` class (no pin windowrule matches) and FLOATING (X11 geometry restore) on the focused workspace; Slack/Signal ABORT ("Missing X server or $DISPLAY", SIGTRAP/SIGSEGV coredumps); GTK apps die or come up X11 with a binary-name class. SECOND bug (same script): the official Obsidian CLI at ~/.local/bin/obsidian (14KB) shadows the real app in PATH and no-ops ("unable to find Obsidian") when the app isn't running — fixed myrig-side (f7ceabf: hypr-load-main imports the manager env itself + launches /usr/bin/obsidian). TRAP hit twice during repro: `pkill -f "pattern"` from a compound ssh/zsh -c command matches the CALLER'S OWN cmdline and kills your own shell — use the bracket trick `[p]attern`. ALSO: /proc/PID/environ of Chromium/Electron processes shows a zeroed block (they scrub it post-exec) — env debugging there is a red herring; and the pgrep -f launch-guards in hypr-load-main match any wrapper whose cmdline mentions the app name (my own test harness got skipped launches). CANONICAL SOURCE: uwsm/Hyprland publish the session env to the systemd USER MANAGER activation env at session start (uwsm finalize; UWSM_FINALIZE_VARNAMES includes HYPRLAND_INSTANCE_SIGNATURE; refreshed per compositor start). Reachable from a boot context: `env -i XDG_RUNTIME_DIR=/run/user/<uid> systemctl --user show-environment` works (systemctl --user derives the bus path from XDG_RUNTIME_DIR, no DBUS var needed); fails only when no manager/session = nothing to export (pre-login, headless, termux). FIX A (sesh 52c58a9): spawnEnv — the single seam building the env map injected via tmux -e into EVERY pane (CreateSessionCmd/CreateWindowCmd/SplitWindowCmd) — moved from reportstate.go to new internal/daemon/spawnenv.go; on Linux it runs systemctl --user show-environment PER SPAWN (never cached → compositor restart picked up by next spawn; explicit XDG_RUNTIME_DIR for the call; 2s timeout; error logged; graceful no-op on non-Linux/no-session — legitimate absence), parseSessionEnv whitelists the 9 vars (values are simple; systemd $'...' escaping only appears on OTHER keys). Tests: spawnenv_test.go (whitelist filter, escaped lines, empty values, nil-on-empty). Verified live on pocket4: fresh thread pane carries the full 9-var session env. FIX B (myrig 6ad1e88): home/.myrig/zshenv/^hyprland^session-env.sh — zshenv self-heal for every env-less zsh (ssh, pre-fix thread panes' tool shells, non-daemon panes): imports the same whitelist via while-read (NO eval) gated on a missing var + /run/user/$UID/bus existing (~40ms, env-less shells only). Deployed pocket4+ideapad. NOT retroactive: pre-fix panes keep frozen env until restarted (fundamental). macbook was unreachable (ssh timeout) during the first deploy pass and was deployed a few hours later — all six machines now run the fix.
## H73 — GitHub issue #9: daemon API bound by DNS-resolving its OWN tailnet name (NSS shadow → silent mesh partition); fix = `tailnet` SENTINEL bind (2026-07-29, sesh 3a22dae + myrig 2698cbb; NO schema change; deployed ALL FIVE API machines)
Issue #9 (Lukas): the daemon bound its TCP API via `SESH_API_ADDR=<tailnet name>:7878`,
resolving its OWN name at startup. On Arch, NSS `myhostname` answers a machine's own
hostname with its LAN address AHEAD of MagicDNS — so if the system hostname == the tailnet
name (pocket4 hit this), the daemon binds a LAN IP and drops off the mesh SILENTLY:
`peer list` reads local config (looks fine), the affected box reaches OUTWARD fine, only
`sesh mesh` FROM ANOTHER machine shows the missing section. Machines were safe by coincidence
(mymain=Debian no myhostname; ideapad's hostname≠tailnet name). Same Go-resolver-vs-system
class as H22/H45, now on the SELF-BIND side.
SECURITY (Lukas was suspicious of the issue's option-2 `0.0.0.0` — RIGHTLY): the TCP API is
the FULL router behind ONE shared bearer token, incl. send-headless (agent turns → arbitrary
shell under [spawn] yolo = RCE) and GET /v1/threads/terminal (a live `tmux attach` pty over a
websocket). mymain has a PUBLIC IP (eth0 77.42.21.223) — `0.0.0.0` would expose that
RCE-capable API to the internet behind only the token. REJECTED option 2. The API is
"designed to live behind Tailscale" (its own code comment). Recommended + shipped option 1+3.
FIX (sesh 3a22dae, internal/daemon/apiserver.go): new `tailnet` SENTINEL host. When
SESH_API_ADDR host == "tailnet", the daemon DISCOVERS its own 100.64.0.0/10 (CGNAT) IPv4 by
scanning net.InterfaceAddrs() — NO DNS, no `tailscale` CLI, no dep, cross-platform. tailnetIPv4()
is LOUD on 0 matches (tailscaled not up → retried) and >1 (CGNAT-ISP clash → refuse to guess).
resolveAPIBindAddr runs INSIDE serveAPIWithRetry EACH iteration so a tailnet coming up after
the daemon (boot ordering, H45 class) is picked up; an explicit IP or hostname still passes
through unchanged (backward compat). doctor shows the RESOLVED bound addr (new apiBoundAddr),
not the sentinel string. offTailnetBind(): a bind to 0.0.0.0/:: or a concrete non-tailnet
non-loopback IP logs a LOUD WARNING (option 3 spirit — turns a silent off-tailnet exposure
visible; not fatal, an explicit bind is allowed). Peers still REACH by name via MagicDNS
(peers.json UNCHANGED) — only the self-bind was buggy. TESTS: apiserver_test.go with injectable
`interfaceAddrs` — tailnetIPv4 truth table (one/zero/multi/edge), resolveAPIBindAddr sentinel-vs-
passthrough, offTailnetBind classification; anti-gaming: neutering the sentinel branch fails
"sentinel -> discovered ip:port". Cells green: api.tcp-auth/tcp-parity/daemon.doctor/http-json/
mesh-read (literal-IP path untouched); daemon -race clean. LIVE-SMOKED on mymain pre-deploy
(isolated daemon, tailnet:PORT → bound 100.106.17.33:PORT, token 200 / no-token 401).
myrig 2698cbb: supervisor sesh-daemon.ini SESH_API_ADDR={{tailscale}}:7878 → `tailnet:7878`.
DEPLOY (NO schema change; daemon restart via supervisor reread+update): ORDER MATTERS — new
BINARY must land BEFORE the new config on each machine (an OLD binary would try to resolve the
literal host "tailnet" → fail). Per machine: build+install binary → install-home render → 
`supervisorctl reread && supervisorctl update sesh-daemon`. Verified BINDS on ALL FIVE API
machines: mymain 100.106.17.33, macbook 100.114.33.83 (macOS utun — scan works there),
macstudio 100.125.115.38, ideapad 100.116.77.31 (Arch — the at-risk distro), pocket4
100.85.205.118 (the issue's machine; sentinel makes it robust regardless of the
lukas-pocket4 rename workaround, which can now be reverted). termux EXEMPT (inbound-less leaf,
no SESH_API_ADDR). Mesh healthy after: all peers synced ~1s, no OFFLINE. doctor shows
"api ok listening on 100.x:7878" everywhere.

## H72 — FOCUS BOUNCE SOLVED: `select-pane -t ":.+"` is a TOGGLE, not "my sibling" (2026-07-28, sesh 2526f00; binary-only; CONFIRMED FIXED by Lukas)
Symptom (weeks-old, survived H66/H67/H68/H69/H71 and one wrong fix): entering a thread from the
sidebar moves focus to the thread pane and then it **flicks back to the sidebar**, "a bit random",
on the Enter key as well as the mouse.
ROOT CAUSE, and it is one target string: focusSiblingPane used `tmux select-pane -t ':.+'`.
**`.+` is relative to the window's ACTIVE pane, NOT to the calling pane** — so it is a TOGGLE.
Sidebar active → lands on the thread pane; thread pane ALREADY active → cycles straight BACK to
the sidebar. The master's after-select-window hook fires ASYNCHRONOUSLY on the same window switch
and also focuses the thread pane, so **whenever the hook won that race, focusSiblingPane undid
it** — hence "random", and hence keyboard too (never a mouse bug).
PROOF (isolated tmux, same command 3× from the same pane's context):
  old `select-pane -t ':.+'` : %1 → %0 → %1 → %0  (toggles)
  new explicit pane id       : %1 → %0 → %0 → %0  (idempotent)
FIX: select the thread pane EXPLICITLY BY ID — the pane in this window carrying neither
@sesh-sidebar nor @sesh-sidebar-slot (the same rule the myrig hook uses). Idempotent, so it no
longer matters whether the TUI or the hook lands first. Logs a FOCUS line under $SESH_TUI_LOG.
SUPERSEDES the intent-clearing in 0547775 (kept — it fixes a real second-order problem: a
preview's leftover "follow" being consumed by an unrelated enter) — but that could NEVER have
fixed the bounce, because with intent=enter the hook focuses the thread pane CORRECTLY and it was
this toggle that then undid it. I "fixed" the thing racing with the bug instead of the bug.
**LESSONS:** (a) when two things race, check whether one of them is WRONG ON ITS OWN before
trying to order them — the toggle was broken with no race at all, on the very first repeat call;
(b) tmux relative targets (`.+`, `.-`, `:+`) resolve against the CURRENT/ACTIVE object, never the
caller — never use one where you mean a specific pane; (c) what cracked this and H70 was Lukas's
PHENOMENOLOGY ("click BELOW a row" = off-by-one; "a bit random ... timing?" = a race), not any
amount of inspecting tmux state.

## H71 — **H66/H67/H68/H69 WERE REVERTED** (2026-07-28, sesh 81ee959 + myrig 7821128). Read this BEFORE trusting those four entries.
Lukas, after a long chase: "I think the last few changes you've made since we started trying to
fix this have just made it progressively worse." He was right. He chose "revert to baseline, keep
the proven fix".
**THE DECISIVE EVIDENCE, and the thing to start from next time:** `prefix+r` — which rebuilds the
sidebar PROCESS and EVERY slot pane — does **NOT** clear the bad state, but `mmt-start` does.
⇒ the state that rots is **NOT in the sidebar process, not in slot geometry, and not in the sesh
TUI at all**. It is in the MASTER's own structures (attach panes, marker clients, window options)
which mmt-start rebuilds. H66–H69 were all aimed at the wrong layer. **Do not re-fix the sidebar
TUI for this symptom without first showing the state survives a `prefix+r`.**
REVERTED (still in git history, but NOT live): H66 enter/follow sequencing (pendingEnter/
enterQueueGrace/enterSelected/takePendingEnter/enterGraceMsg); the unconditional enter-intent
declaration; myrig H67 (swap-time enforce sweeps, unzoom-before-travel, mkdir lock) and H69 (the
window-resized hook).
**CORRECTION (same day, sesh 0547775): H68's tea.ClearScreen on WindowSizeMsg was RESTORED and
IS live.** Reverting it was wrong: the leftover-paint artifact reappeared within the hour of the
revert and had been absent while it was deployed — Lukas's own A/B. He also clarified it is
COSMETIC ("does not affect the function of the sidebar... just left-over text that hasn't been
flushed"), which matches a paint bug exactly. Live geometry was verified consistent at the time
of his screenshot, so it is stale paint, not a mis-sized pane. TestResizeClearsScreen guards it.
NB the other tea.ClearScreen calls are the FILTER TINT ones — those predate all this and work;
don't remove them.
**KEPT — the only change with a test that fails without it (H70): `View()` returns WITHOUT its
trailing newline.** That newline made the frame one line taller than the pane whenever BOTH
scroll indicators showed; bubbletea drops the TOP line to fit, so every click landed one row off
= "I have to click BELOW a row to select it". TestViewFitsPaneHeight guards it.
KEPT — `$SESH_TUI_LOG` diagnostics (inert unless set) and myrig `prefix+r` / `sidebar-toggle.sh
refresh` (Lukas asked for it; also the way to pick up a new binary, since a long-running sidebar
keeps the one it launched with). Kept separately: a8a9256 (active view shows running threads —
an unrelated explicit request).
**METHOD LESSON (the expensive one):** each of H66/H67/H69 had a RIG THAT GENUINELY REPRODUCED
SOMETHING. A reproduction proves a mechanism EXISTS; it does not prove it is the mechanism the
user is hitting. I kept treating the former as the latter and shipped four behavioural changes to
a system Lukas depends on. What actually moved things forward: (a) asking him for exact
phenomenology — "click BELOW a row to select it" is an off-by-one signature and found H70 in
minutes; (b) his own environment hypothesis (two terminal emulators); (c) the prefix+r-vs-
mmt-start discriminator above. ASK FOR PHENOMENOLOGY AND FOR WHAT DOES/DOESN'T CLEAR IT FIRST.
STILL OPEN: (1) clicking degrades over time, cleared only by mmt-start ⇒ look at master state
(marker clients! mymain's work server carries FIVE clients and `sesh master watchers` lists four
markers — a nav switches the marker client for the origin, which may not be the one in the window
being viewed). (2) the FOCUS BOUNCE — **SOLVED, see H72.**

## H70 — THE REAL CLICK BUG: View()'s TRAILING NEWLINE made the frame 1 line too tall ⇒ bubbletea drops the TOP line ⇒ every click off by one (2026-07-28, sesh 93c18b3 + instrumentation f75c417/866003e; NO schema change; binary-only)
**This is the one.** H66 (enter/follow race), H67 (over-wide slot), H68 (stale paint), H69 (two
terminals rescaling the pane) were ALL real defects and all shipped — but NONE of them was the
thing Lukas kept hitting. What cracked it was asking him for exact phenomenology and getting:
"I have to click multiple times sometimes to get it to select the row, often I have to click
**BELOW a row** to select it (so there's a mismatch between where I click and where the row is)."
**Click-below-to-select is an exact off-by-one signature — ASK FOR THAT KIND OF DETAIL FIRST.**
ROOT CAUSE: `View()` ended with a trailing "\n". bubbletea splits the view on "\n" and renders
one terminal line per element, so the trailing newline contributes a **PHANTOM empty final
line** — the frame is one line taller than it looks. `chromeLines` reserves exactly 2 lines for
the ▲/▼ indicators, and a list scrolled to the MIDDLE shows BOTH, consuming the whole reserve.
Frame = height+1 ⇒ bubbletea keeps only the last `height` lines (`newLines[len-height:]`, it
drops from the TOP) ⇒ the title vanishes, every row renders ONE LINE HIGHER than the model
believes, and rowAtY (itself correct) maps every click one row off. Measured: "frame is 25 lines
in a 24-row pane". FIX: `strings.TrimSuffix(b.String(), "\n")`.
WHY IT SURVIVED FOUR ROUNDS (the important part):
- It needs BOTH scroll indicators ⇒ a list long enough to scroll AND scrolled into the middle.
  Lukas's list was 13 rows during the instrumented capture, so the capture looked PERFECTLY
  HEALTHY (every click mapped right, every nav fired right) and I nearly concluded there was no
  bug. A clean log is not proof of correctness — it can just mean the trigger wasn't present.
- `TestRowAtYMatchesRender` (the H41 drift guard) CANNOT see it: it compares logical lines of a
  render against each other, so an offset applied to the WHOLE frame is invisible. The new
  `TestViewFitsPaneHeight` measures the frame against the PANE — that is the axis that mattered.
FOCUS half of the same report ("when I click a thread it should focus its tmux pane, but
sometimes focus remains on the sidebar — happens on the KEYBOARD too"): the sidebar declared its
window-switch intent only when it PREDICTED a switch. The intent is ONE shared tmux option, so a
"follow" declared by a preview that did NOT switch windows stayed set and was consumed by the
next ENTER's switch → hook keeps focus on the sidebar exactly when the user asked to leave it.
"It happens on the Enter key too" was the tell that it was never a mouse bug. Now an enter
ALWAYS writes "enter" (no prediction), so the hook can only act on the latest user action.
INSTRUMENTATION THAT NOW EXISTS (`$SESH_TUI_LOG=<path>`, internal/tui/debuglog.go): logs MOUSE
(every event reaching Update — separates "tmux never forwarded it" from "we mapped it wrong"),
CLICK (every term of rowAtY: x/y, pane size, rowsTop, vOffset, viewport, row count, resolved
idx+name, or OUTSIDE), SWALLOWED (a modal ate it, naming which — move mode renders almost like
the normal grid and silently eats every click), RESIZE, NAV EXEC (full argv + output — a nav can
exit 0 having switched a client nobody is looking at), NAV/FOLLOW DONE. Enable WITHOUT touching
any script: `tmux -L sesh-master set-environment -g SESH_TUI_LOG /tmp/sesh-sidebar.log` then
refresh the sidebar. **Reach for this on turn 1 of any "the TUI is behaving oddly" report.**
ALSO SHIPPED (myrig 8bac108): `sidebar-toggle.sh refresh` + **prefix+r** — kill the sidebar and
every slot, rebuild at current geometry in one press. This is the answer to "is there a way to
completely refresh the sidebar" AND the way to pick up a newly deployed binary: **a long-running
sidebar keeps the binary it was launched with**, which is why TUI fixes repeatedly appeared not
to have landed. Also REVERTED MY OWN REGRESSION from H67: enforce was pinning slots in windows
PARKED at 80 cols; tmux's proportional scaling is SELF-INVERSE (a slot left alone at 16 in an
80-col window returns to exactly 38 at 188), so forcing 38 while parked makes it 89 on arrival —
pinning everything made the drift WORSE. Enforce now only touches windows sized to a live client.

## H69 — THE ACTUAL "clicks stop working" CAUSE: TWO ATTACHED TERMINALS + `window-size latest` silently rescale the sidebar (2026-07-27, myrig 52a4942+5da06d7; NO sesh change; conf = pull + source-file)
**Lukas found this, not me** ("I'm wondering if it's to do with the fact that I have attached to
the master tmux in two different terminal emulators? ghostty and wezterm. Although it doesn't
always end up in this state"). He was exactly right, and it is NOT H66/H67/H68 (all three were
real but none was THIS). Lesson: when the user offers a hypothesis about their own environment,
test it FIRST — I burned three rounds on races/paint before doing so.
LIVE CAPTURE mid-failure (macbook): `list-clients` = /dev/ttys015 **188x51** + /dev/ttys014
**214x55** (two clients, DIFFERENT sizes), `window-size latest`, and
`vis={25x50,0,0,11,162x50,26,0,3}` ⇒ **the 38-col sidebar pane was 25 COLS**.
MECHANISM: under `window-size latest` the window is re-sized to whichever client last received
**INPUT** — so merely TYPING in the other terminal resizes the window (188↔214) and tmux
rescales panes **PROPORTIONALLY**, dragging the pinned 38-col slot off its width. NEITHER
existing hook covers it: `client-resized` doesn't fire (no client changed size) and
`after-select-window` doesn't fire (no window switch) ⇒ it drifted and STAYED drifted.
WHY THAT BREAKS CLICKING (no corruption involved — the render was perfectly self-consistent at
25 cols, bubbletea truncating everything correctly): the sidebar's CLICKABLE REGION is no longer
where the user aims. At 25 cols a click on a thread NAME (~col 26-38, where it has always been)
lands in the neighbouring AGENT pane. Drifting the other way past 80 cols also flips sesh into
its maximized column set. And "not ALWAYS": two terminals of the SAME size resize the window to
the same geometry, so nothing rescales.
FIX: hook sidebar-enforce.sh to **`window-resized`** as well. Resizing a PANE doesn't change the
window size, so enforce can't re-trigger it — converges after one correction.
RIG (reproduced the live number EXACTLY): one master, TWO attached clients 188x51 + 214x55,
`window-size latest`, then alternate INPUT between the terminals with NO window switching.
  no hook: 38 → **25** → 38 → **25** …  (the live value, oscillating with input focus)
  hooked : 38 → 38 → 38 → 38 …
GOTCHAS (both bit me):
- `window-resized` is WINDOW-scoped: it NEVER appears in `show-hooks -g` (session table) — check
  `show-hooks -gw`. I briefly "concluded" `-g` was silently discarded and shipped a wrong
  comment; verified properly afterwards that **`-g` and `-gw` both register AND fire**.
- The H30 pipe-exit trap AGAIN: `tmux set-hook … | head -2 && echo ACCEPTED` reports success
  unconditionally (a pipeline's status is the LAST command's). Never gate a check on `cmd | head`.
DEPLOY: conf is symlinked BUT a `set-hook` lives in the running server's state ⇒ pull is NOT
enough, a running master needs `source-file ~/.sesh/myrig/tmux.master.conf`. Done mymain +
macbook + macstudio (running masters); ideapad none; termux self-guards. Also ran
sidebar-enforce.sh once on macbook's live master to pull the stuck 25-col sidebar back to 38.

## H68 — "clicking still gets caught in weird states": the CORRUPTED RENDER *IS* THE BROKEN CLICKING; fix = wipe the screen on resize (2026-07-27, sesh 878b851; NO schema change; binary-only, deployed ALL FIVE, no restarts)
Lukas after H67's myrig fix: "the sidebar still gets caught in weird states where the clicking
no longer works properly."
THE REFRAME (the insight worth keeping): **a corrupted render IS broken clicking.** rowAtY maps
a click through the MODEL's geometry, and that geometry is always correct — but when the SCREEN
no longer matches it, clicks land on rows the user can't see = "it can't see where I'm clicking".
So H66 (the enter/follow race) and H67 (landing in an over-wide slot) were both real, but the
thing that made it PERSIST until mmt-kill was stale paint. Don't hunt the click math again —
`TestRowAtYMatchesRender` now includes a sidebar-geometry case and it's honest.
ROOT CAUSE: bubbletea's renderer repaints only the lines of the NEW frame on resize
(WindowSizeMsg → `repaint()`, which just drops its line-diff cache). It TRUNCATES over-wide
lines it writes, but wrapping that ALREADY HAPPENED (a frame rendered when the pane was wider)
is invisible to it — those extra physical lines are never erased. A shrink therefore strands the
old wide output below the new narrow render = the screenshot.
FIX: on a REAL size change, Update returns `tea.ClearScreen` (= EraseEntireScreen + CursorHome +
repaint — the full wipe the renderer won't do itself). Unchanged size → nil (not a per-frame
cost). A sidebar resizes constantly (fullscreen toggle, travelling between master windows, slot
re-pinning), so this is the difference between self-healing and staying broken.
TESTS: TestResizeClearsScreen (shrink+grow clear, same-size doesn't; the msg type is UNEXPORTED
so compare against `tea.ClearScreen()`'s own message with reflect.DeepEqual). ANTI-GAMING:
return nil instead → red; reversed. TestRowAtYMatchesRender gained a 38-col SIDEBAR case
carrying a long actionErr + note + OFFLINE peer footer (all wider than the pane) — pins that
bubbletea truncates rather than wraps them, so rowsTop stays honest.
NB THE GENERAL LESSON: bubbletea will NOT clean up after a shrink. Any long-lived full-screen
bubbletea program in a resizable pane needs this.
DEPLOY: binary-only ALL FIVE (no daemon restart). Running sidebar keeps its old binary.

## H67 — sidebar CORRUPTED RENDER (stacked half-drawn copies): it LANDED in an over-wide slot (2026-07-27, myrig 356c732; NO sesh change; symlinked scripts = git pull only, no restart)
Lukas screenshotted the 38-col sidebar column containing TWO overlapping renders: a correct
narrow one on top, and below it the WIDE (maximized) column set — a cwd column present and a
line wrapping mid-word ("scuttlebug-dagster-bu" / "ild scuttlebug-healthcare-dagster <vdn3hk>").
READING THE SCREENSHOT IS THE DIAGNOSIS: wide column set + wrapping at 38 ⇒ the TUI's m.width
was ≥ sidebarWideThreshold(80) while the pane was 38. sesh is the VICTIM here (it can only
believe SIGWINCH) — the bug is in the myrig rig.
ROOT CAUSE (reproduced): tmux scales panes PROPORTIONALLY on window resize, and a master window
parked at the detached default (80 cols) has never been sized to the client ⇒ the moment the
client sizes it, its 38-col slot becomes 38/80*188 ≈ **98 cols**. The traveling sidebar swapped
into that window LANDS at 98 ⇒ renders the full grid column set; the hook's `resize-pane -x 38`
then snaps it to 38. bubbletea repaints line-by-line against its OWN model of the screen and has
no idea the terminal already WRAPPED that wide output, so the wrapped remnants are never erased
= the stacked corruption. sidebar-enforce.sh existed to pin slots but ran ONLY on
client-attached/client-resized — never on a window switch — so a drifted slot stayed drifted.
LIVE EVIDENCE of the sibling hazard: on macbook, window @3 was flagged zoomed=1 around pane %11
while %11 had ALREADY been swapped into @0, where it reported **188x50 inside a 38-col cell**
(a zoomed pane's pty is sized to the whole window; swapping it out leaves the old window flagged
zoomed around it).
FIXES (sidebar-swap.sh): (1) run the enforce sweep BEFORE picking the target slot (so the
sidebar can never land wide) and again after; (2) UN-ZOOM the sidebar before it travels when it
is the zoomed pane (dropping fullscreen on travel is the documented intent — _dev/SIDEBAR.md);
(3) SERIALIZE with an mkdir lock — the after-select-window hook (async run-shell) and
sidebar-zoom.sh (which calls this script DIRECTLY when the sidebar is parked elsewhere) can run
concurrently, and interleaved swaps move the sidebar twice. Stale lock (>1min) reclaimed so it
can never wedge the cockpit; `-mmin` gets an INTEGER (BSD/macOS find rejects fractions — the
master usually runs on a Mac). sidebar-enforce.sh: skip only the pane that IS fullscreen
(zoomed AND active) instead of every pane of a zoomed window — the blanket skip is what let a
stuck over-wide sidebar survive until mmt-kill.
RIG (the thing that finally reproduced it — worth reusing): isolated master tmux + a REAL
ATTACHED CLIENT at 200x50 via a driver tmux (`env -u TMUX tmux -L <sock> attach`), `window-size
latest`, 3 windows of which 2 are PARKED at the detached default, the swap hook installed, then
zoom+travel. A DETACHED rig (plain `new-session -x/-y`) reproduces NOTHING — tmux auto-unzooms
and never proportionally rescales — so the attached client + parked windows are the load-bearing
ingredients. Assert on `#{window_visible_layout}`: before = parked slots read 98x48, after = every
slot 38x48 in every window. ANTI-GAMING: removing the enforce sweeps brings the 98-col slots back.
DEPLOY: myrig scripts are SYMLINKED into ~/.sesh/myrig, and the hook command string is unchanged
⇒ `git pull` is the whole deploy, no install-home render, no conf re-source, no cockpit restart;
a live master picks it up on its next window switch (which also self-heals drifted slots).
Pulled mymain/macbook/macstudio/ideapad (+termux, where the scripts self-guard anyway).

## H66 — sidebar "clicks don't register": ENTER vs in-flight FOLLOW race (2026-07-27, sesh b27b538; NO schema change; binary-only, deployed ALL FIVE, no restarts)
Lukas: "sometimes the sidebar gets in a very strange state where it doesn't register my
clicks... I click a thread, the sidebar briefly shows it's focused then loses it like usual,
but it doesn't transition — it stays on the thread I was on. But then if I click on the MAIN
PANE it sometimes does transition to the thread I clicked on previously. Only mmt-kill +
mmt-start gets out of it."
DIAGNOSIS TRAIL (several plausible theories killed by measurement — worth the recipe):
- "briefly focused then loses it" PROVED the click was received AND the nav SUCCEEDED (the
  focus handoff only runs on navDoneMsg). So NOT a click→row mapping bug. Confirmed anyway
  by capturing the live 38x52 sidebar pane: title Y=0, header Y=1, first row Y=2 == rowsTop().
- KILLED: chrome-line WRAPPING desyncing rowAtY/chromeLines at 38 cols. bubbletea v1.3.10's
  standard_renderer TRUNCATES over-wide lines (`ansi.Truncate(line, r.width, "")`) — it never
  wraps, so logical lines == physical lines. (It DOES drop lines from the TOP when a frame
  exceeds r.height — `newLines[len(newLines)-r.height:]` — but chromeLines over-reserves 2 for
  the ▲/▼ indicators, so the frame can't overflow in practice.) Read the renderer, don't assume.
- KILLED: a PINNED follow resolver. `ps eww` on the live sidebar pid showed SESH_TUI_MASTER_
  MACHINE is NOT set → it uses the live sidebarWindowName() per follow. Correct.
- KILLED: stale `@sesh-sidebar-intent` / missing hooks — `show-options -g | grep sesh` was
  empty and all three hooks (after-select-window swap, client-attached/resized enforce) present.
ROOT CAUSE: the sidebar drives the cockpit's thread pane from TWO places — the ambient
selection FOLLOW (armed by EVERY selection move, incl. every wheel/trackpad notch) and a user
ENTER (click/Enter). Both shell out `sesh tmux nav`; the pane shows whichever lands LAST;
NOTHING sequenced them. Click during an in-flight follow (routine right after a trackpad
scroll; a CROSS-machine follow runs hundreds of ms — Lukas has mymain+macstudio rows) → the
stale preview lands ON TOP of the click. The existing coalesce then re-armed a follow onto the
still-selected row and corrected it a nav later = "it transitions a moment later, seemingly
when I click the main pane" (the main-pane click is COINCIDENTAL, not causal). Measured nav
sequence: [clicked, preview, clicked].
FIX (internal/tui, pure client): enterSelected() sequences an enter behind a live follow —
followInFlight ⇒ hold the clicked ROW in pendingEnter; followDoneMsg dispatches it INSTEAD of
re-arming (explicit beats ambient) ⇒ [preview, clicked]. BOUNDED by enterQueueGrace 250ms
(enterGraceMsg keyed by pendingEnterSeq) so a stalled preview can never make the sidebar
unclickable — the very bug being fixed. navSelected split into navSelected()/navRow(row) so a
queued enter navs the CLICKED row, not wherever the cursor drifted. All 4 enter call sites bind
cmd BEFORE returning m (H56 pointer-receiver gotcha; takePendingEnter too). Side effect: a
click's intent="enter" can no longer clobber an in-flight follow's intent="follow".
TEST sidebar_race_test.go (outside the matrix): real fake `sesh` ON DISK logging every --to,
arrow onto a slow row → click the other mid-flight → run the outstanding cmds GENUINELY
CONCURRENTLY through a faithful mini event loop (one goroutine per cmd, msgs fed back in true
COMPLETION order — BATCHING THEM HIDES THE INTERLEAVING, my first harness did and passed
vacuously). Asserts the observable external effect = the nav SEQUENCE: normal follow must be
exactly [peer:slowpoke peer:clicked]; a 2s stalled follow must still issue the click first.
ANTI-GAMING: `if true ||` the sequencing guard → red with the EXACT bug signature
[peer:clicked peer:slowpoke peer:clicked]; reverse-edited back.
NOT REPRODUCED / still open: the "only mmt-kill+mmt-start gets out of it" part. The model
self-heals (that's the third nav), so this fix removes the visible misbehaviour but I found no
sticky state. If it recurs, next suspects: (a) the sidebar pane is a LONG-RUNNING process — it
keeps whatever binary it was launched with, so a stale sidebar is a real "restart fixes it"
class; (b) master windows parked at the detached default (live macbook had @0/@1 at 24 rows vs
@2/@3 at 52) resize proportionally on first visit, and sidebar-enforce.sh only runs on
client-attached/resized — a window switch isn't covered, so the sidebar can end up oversized
(≥80 cols ⇒ wide columns AND follow disabled).
DEPLOY: binary-only ALL FIVE at b27b538, NO daemon restart (no daemon/api/schema change).
**A running sidebar does NOT pick this up** — prefix+b twice (or mmt-kill/mmt-start) restarts it.

## H65 — "missing turns on close+reopen" (claude AND codex): root cause = claude AGENT-TEAMS bg agents + session-id drift; fixes = disable agent-teams (myrig) + claude session-id STAMPING (2026-07-26, sesh bed6171 + myrig 03941ec; NO schema change; hook-only + config, deployed ALL FIVE)
Lukas: closing thread ab9d5a3a (dagster-netrun) with `x` and reopening lost "a lot of turns"
— often, both claude and codex. LONG diagnosis; the REAL cause only surfaced at the end.
DIAGNOSIS TRAIL:
- `x` = stop runtime (KillPane), reopen = revive → `claude --resume <record's id>`. The record
  pinned 9063203c (the spawn --session-id); the user's recent work was in a DIFFERENT file
  0207aa9c (more/newer msgs, its own aiTitle "Evaluate Netrun as Dagster wrapper"). The leaf
  resolver couldn't bridge them (different first-message uuids → different conversation-root),
  so revive resumed 9063203c = missing everything in 0207aa9c.
- The two files SHARED 693 message uuids for 4 days, then DIVERGED today 19:24. THE REVEAL:
  reviving 0207aa9c 409'd — `Session 0207aa9c is currently running as a background agent (bg)`.
  0207aa9c was a claude **agent-teams BACKGROUND AGENT** (`claude bg-pty-host …/pty/
  0207aa9c.sock`, child of `claude daemon run`), spawned by the experimental feature (env
  CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1, "← 2 agents" in the pane). **bg agents run under
  claude's machine-global daemon, OUTSIDE sesh's tmux pane** — so the work migrated into a
  session sesh can't see or resume (claude blocks two holders). This is the recurring cause.
RECOVERY (live, with Lukas): (1) killed the bg-agent daemon per the H34 recipe — SIGTERM the
`claude daemon run` supervisor (pid 2785550) FIRST so it can't resurrect, then SIGKILL the
surviving `bg-pty-host` for 0207aa9c (3711628); verified the user's INTERACTIVE/sesh claude
sessions survived (they're plain `claude`, not bg-pty-host children — a fresh daemon
auto-restarts for others, doesn't hold 0207aa9c). (2) HEADLESS-adopted 0207aa9c as a NEW
thread 1f7c4a97 (`thread adopt --agent claude --session-id 0207aa9c --cwd …`), then headful
→ resumed the exact session in a sesh pane, all turns restored. (3) archived the old
ab9d5a3a (renamed dagster-netrun-old-thread; its 9063203c branch is behind). NB the user's
`claude --resume 0207aa9c --fork-session` was a red herring — a fork that wrote nothing;
0207aa9c had all the work.
FIXES:
- **myrig 03941ec**: removed CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS from home/.claude/settings.json
  (`env` block). settings.json is a SYMLINK to the repo → the edit was instantly live on
  mymain; deployed all five via git pull (each machine's ~/.claude/settings.json symlinks its
  repo copy). Stops new bg agents from being spawned. Existing sessions keep the env until
  restarted.
- **sesh bed6171 (part b, the durable in-pane fix)**: the claude hook (integrations/claude/
  sesh-agent-state.sh) already gets `session_id` on stdin — now passes `--agent-session
  <session_id>` every turn; the daemon STAMPS it onto the record (the --agent-session flag +
  stamp already shipped for codex in H62, schema 46, so NO daemon change). So resume/reopen
  lands on the session claude is ACTUALLY in after a compaction/rewind fork, instead of the
  fragile leaf resolver. Script restructured to emit event\treason\tsession_id + build argv
  with `set --`. IMPORTANT SCOPE: this fixes IN-PANE drift (compaction/rewind); it does NOT
  fix the bg-agent case (bg agents run outside the pane, so the pane's hook never sees their
  session) — that's why disabling agent-teams is the real fix for THIS incident. Deploy =
  git pull the sesh checkout all five (claude re-reads the hook script each event → live
  hook-enabled sessions pick it up with NO restart; the daemon needs nothing).
TESTS: internal/agents/claudehook_sh_test.go drives the REAL hook with a fake SESH_BIN —
UserPromptSubmit/Stop carry --agent-session, session-less payload reports without it, subagent
(agent_id) reports nothing, AskUserQuestion → blocked+reason+session; anti-gaming: neutering
the --agent-session line fails it. FLAKE NOTE (re-confirmed): thread.flagged/state-authority
claude cells PASS in isolation (flagged/claude/local 23s; all 4 serial 88s) but TIME OUT
under concurrent real-claude load (claude rate-limiting — flagged runs ~5 turns, most
sensitive). Always confirm claude cells serially (`-parallel 1`), never trust a concurrent-
batch red. The daemon-stamp path itself is unit-tested since H62 (TestReportStateStampsAgentSession).
DEPLOY: sesh git pull all five (hook-only, no rebuild/restart); myrig git pull all five
(settings.json symlink). SKILL state-authority paragraph updated (stamping + the
agent-teams-outside-the-pane caveat).

## H64 — phantom IDLE-while-running: stale reported-IDLE now dropped on an ANIMATING pane (H58's mirror) + the root cause = a session PREDATING the reporter hooks (2026-07-26, sesh 26ca34f; NO schema change; daemon RESTART all five; thread ab9d5a3a again)
Lukas: thread ab9d5a3a (dagster-netrun, claude/mymain — the H58 thread) showed IDLE while
actively running, and didn't FLAG at turn end. This is the INVERSE of H58 (which was
busy-while-idle). DIAGNOSIS (live repro, worth the recipe):
- Snapshot said busy=idle + state_authority=**reported**, but the PANE showed
  `✽ Orbiting… still thinking with xhigh effort` (clearly busy). So a REPORTED entry was
  overriding the content-diff — and it said idle.
- Scoped a temp tap into integrations/claude/sesh-agent-state.sh (case "$SESH_THREAD_ID" in
  ab9d5a3a*) logging the hook_event_name; sent a `test`. Tap stayed **EMPTY across turns**
  incl. active tool use, and after a turn ended state_authority stayed heuristic (never
  flipped back to reported) → **claude is NOT invoking the reporter hooks for this session
  at all.** ROOT CAUSE: the claude PROCESS started **Jul 22 16:56** (ps -o lstart on the
  `claude --session-id … --dangerously-skip-permissions` pid), one day BEFORE the reporter
  hooks were added to settings.json (myrig f064857 Jul 23 15:55). **Claude Code loads hooks
  at session start — no mid-session hot-reload** — so this 4-day session has no reporter.
  (Also its env lacks SESH_BIN, added Jul 23 — the hook's PATH fallback to `sesh` would
  work, but it's moot since the hook isn't invoked.)
- So a STALE reported-idle entry (origin: some earlier report; never updated since the
  reporter is dead) sat forever: nothing sends turn_started (→busy) or release, the pane is
  alive so the pane-liveness bound never clears it, and H58's staleness bound only drops
  reported-BUSY on a FROZEN pane — there was NO mirror for reported-IDLE on an ANIMATING
  pane. The maintainer kept preferring stale-idle over the heuristic → pinned idle while
  running. No flag either: a flag needs a busy→idle EDGE; stuck-idle never has a busy phase.
- PROOF: `report-state --event release` → fell to heuristic → busy tracked perfectly (busy
  while thinking, idle when done). The heuristic was never broken; the stale authority
  shadowed it.
FIX (sesh 26ca34f, the H58 MIRROR, agreed w/ Lukas): liveState.heuristicBusySince (when the
current content-diff busy streak began; zeroed when it breaks) + staleReportedIdle predicate
symmetric to staleReportedBusy (pane animating for the whole bound AND report ≥ bound old).
refreshThread drops a reported-idle the animating pane contradicts, LOUDLY, degrading to
heuristic (state_authority reported→heuristic). Reuses m.staleBound (2m). Tests:
TestStaleReportedIdle truth table; TestMaintainerDropsStaleReportedIdle (REAL maintainer +
REAL tmux pane animated by an argv0-"claude" symlink→sh running a counter loop — a static
sleep won't animate, `yes` goes byte-stable after the screen fills, so a changing-counter
loop is the honest animator; AND the symlink keeps argv0=claude so AgentUnderPane resolves
it headful). Anti-gaming: `if false &&` the drop → maintainer test red at "busy=idle
authority=reported". state-authority conformance cells (claude+pi × local+remote, REAL
agents) stay green — a working reporter reports busy during turns so the idle bound never
fires. SKILL documents both symmetric bounds.
KEY LIMITATION (told Lukas): this keeps BUSY correct when a reporter dies, but does NOT
restore FLAGS — heuristic busy→idle edges don't flag claude (H47 false-edge avoidance is by
design), and after the drop the thread is on the heuristic. FLAGS require a LIVE reporter.
For ab9d5a3a specifically the real fix is to RESTART the claude session (stop→headful,
resumes intact) so it loads the hooks — offered, Lukas's call (it was mid-work). Every
NORMALLY-spawned thread already reports fine; this was a one-off session predating the hooks.
Also: the 2m bound means the busy self-heal is slow for short turns on a dead reporter (a
short turn never sustains 2m of animation) — a longer turn heals it once, and a dead
reporter won't recreate the entry, so it stays on heuristic after. DEPLOY (daemon change, NO
schema bump — state_authority already exists; rebuild + RESTART all five): mymain native +
supervisorctl; macbook/macstudio /opt/homebrew/bin/go + supervisorctl; ideapad native +
supervisorctl; termux plain go build + explicit-pid kill (4709) + setsid-nohup (pid 12892).
All five vcs.revision=26ca34f, schema 46. Reverted the temp hook tap (git clean).

## H63 — active view: HOLD BEATS FLAG (reverses part of H59/H60's flagged-always-in-active) (2026-07-26, sesh fd535ae; NO schema change; binary-only, deployed ALL FIVE, no restarts)
Lukas: "put threads on hold and they won't appear in my active view" — the H59 "flagged
thread ALWAYS shows in active" rule was surfacing FLAGGED on-hold threads (and H52 flags on
every turn end now, so this was frequent). ONE-LINE predicate change in
builtinViewAdmits/ViewActive (internal/tui/model.go): `flagged || ((!archived || headful)
&& !onHold)` → `(flagged || !archived || headful) && !onHold`. So the flag still overrides
ARCHIVED-hiding for non-held threads (H40 needs-attention), but !OnHold now dominates
everything — an on-hold thread never shows in active, flagged or not; its ⚑ still shows in
the `on hold` view. PURE TUI-CLIENT change (the daemon already publishes on_hold; the TUI
filters) ⇒ binary-only deploy, NO daemon restart, mixed-mesh trivially safe.
TESTS: TestActiveViewAdmitsArchivedHeadful truth table — the three flagged-on-hold cases
flip true→false (+ a flagged-HEADFUL-on-hold case). view-hold claim EXTENDED: flag the held
thread on a real daemon, settle until its ⚑ RENDERS in the `on hold` view (proves the
model's data carries the flag — the active recheck can't pass vacuously on a stale pre-flag
fetch; NB the flag lands async so I gate on the on-hold render, not a fixed sleep), then
cycle on-hold→archived→all→active and assert it's STILL hidden. ANTI-GAMING: restoring the
old predicate fails 3 truth-table cases AND the claim at "active view must keep HIDING a
flagged on-hold thread" — verified, then reversed. SKILL active-view paragraph updated
(`(flagged OR not archived OR headful) AND not on hold`; "HOLD BEATS FLAG").
DEPLOY GOTCHA (bit me): a CONCURRENT sidebar session (Lukas's, sesh 0996341/f27039f
--sidebar-filter-style) pushed WHILE I had model.go edited in the working tree; my
`git add <explicit files>` for the commit did NOT include model.go (I'd listed it, but the
concurrent push had already been rebased in and the file was mid-edit) → **model.go's
predicate change is NOT in commit fd535ae's diff** (that commit shows only view_active_test/
tui_affordances_test/SKILL). BUT `git show HEAD:internal/tui/model.go` confirms main DOES
carry the correct new predicate (line 95) — it landed via the interleave, not orphaned. All
five binaries built from HEAD verify vcs.revision=fd535ae and the deployed behavior is
correct. Lesson reconfirmed: with a concurrent pusher, `git show HEAD:<file>` the actual
content after committing, don't trust the commit's own --stat.
DEPLOY: binary-only ALL FIVE at fd535ae (mymain native; macbook/macstudio /opt/homebrew/bin/
go; ideapad native; termux plain go build CGO=1) — NO daemon restart (each `sesh tui` runs
fresh). Lukas switched me to Opus mid-deploy (rejected the first termux attempt, said
Continue) — retried termux clean.

## H62 — codex SESSION-ID CAPTURE shipped (H61's fix): notify payload thread-id → report-state stamp (2026-07-26, sesh a12b029 api 45→46, NO store migration; deployed ALL FIVE; ticket 49d4299b done)
Lukas: "okay proceed to do the fix." Both H61 bugs killed at the shared root (headed codex
threads never recorded their late-minted session id):
- WIRE: ReportStateRequest.agent_session_id (additive, owner-local — the reporter talks to
  its own daemon; a pre-46 daemon ignores the unknown field). reportState STAMPS it via
  SetThreadAgentSession BEFORE the event branches (so the early-returning
  turn_ended_no_authority codex path gets it); a DIFFERING stored id is CORRECTED with a
  loud log — the live pane's reporter IS the thread's conversation, which also self-heals
  past mis-discoveries (and covers codex /new mid-pane). CLI `thread report-state
  --agent-session` (+help usage/flagDoc; SKILL state-authority paragraph).
- SCRIPT: embedded codexnotify.sh seds "thread-id" out of the payload (no jq; payload only
  ever expanded as quoted argv, never shell-re-parsed) and passes --agent-session; id-less
  payload (older codex) still reports without the flag. Serve() now ALSO rewrites the
  materialized <SESH_HOME>/codex-notify.sh (the codexnotify.go doc CLAIMED "rewritten on
  every daemon start" but nothing called it outside spawn — a deploy would have waited for
  the next codex spawn to reach live panes; fatal on failure, home is store-writable).
- FALLBACK HARDENED: DiscoverCodexSession gains an exclude set = agent session ids claimed
  by OTHER threads (claimedAgentSessions, archived included), both call sites (revive +
  headed-born send-headless). Legacy-only path now (any thread with a post-46 turn is
  stamped); residual: a same-cwd pair where NEITHER ever reported can still mis-land — gone
  once each runs one turn. Fork of an unstamped codex source: accurate 409 "no captured
  session id … run one turn, or stop+revive" (was the misleading "no turn yet").
- MATRIX: new feature thread.codex-session-capture (codex × Local — owner-side reporter+
  disk, the thread.adopt precedent). Cell (real codex ~50s): pre-turn fork refusal names
  the real state → turn stamps id → fork of a LIVE HEADED codex works → second thread SAME
  cwd, own id → kill both, revive OLDER FIRST (the exact pre-46 mis-landing order) →
  stored ids unchanged + each recalls ITS codeword, not the other's. ANTI-GAMING: stamp
  neutered (if false &&) → cell red at "never stamped" → reverse-edited back. Blast radius
  green: thread.flagged/fork ×2 codex, thread.resume/send.headless ×2 codex, daemon+agents
  -race, cmd/sesh help meta-tests. Unit: stamp truth table; exclude-set; the REAL embedded
  script driven by sh with a fake SESH_BIN argv logger (payload w/ id, id-less, foreign type).
DEPLOY (schema 46 = rebuild + daemon RESTART all five): mymain native+supervisorctl;
macbook/macstudio /opt/homebrew/bin/go + supervisorctl; ideapad native + supervisorctl;
termux plain go build + explicit-pid kill (30082) + setsid-nohup relaunch (pid 4709).
Verified ~/.sesh/codex-notify.sh carries --agent-session on mymain+macbook (the Serve
refresh working). LIVE SMOKE mymain: scratch headed codex thread stamped
agent_session_id 019f9d49-… right after its first turn (previously impossible); deleted.
NB myrig/myagent/sesh-ui need NOTHING (script embedded in sesh; api field request-only).

## H61 — codex lifecycle EXPERIMENT (Lukas's ask): compact/fork/revive — 2 real bugs found, NO code change; ticket 49d4299b (triage)
Lukas: "any issues with sesh and codex, especially after you compact... if you fork and kill
both and reattach, do they land on the right thread?" Ran a fully ISOLATED live experiment
(codex-cli 0.145.0, GPT-5.6 Sol; sandbox SESH_HOME/CODEX_HOME [auth.json copied]/sockets
cx-work+cx-master, SESH_* stripped, deployed binary; codeword-recall as the continuity probe;
sandbox torn down after).
VERIFIED FINE: (a) codex **/compact is IN-PLACE** — same session id, same rollout file (14→19
lines), NO drift (unlike claude; ResolveCurrentSession's identity-for-codex assumption HOLDS);
(b) `codex resume` **appends in place** (no new rollout file, id stable across repeated
kill/revive); (c) compact→stop→headful recalls pre+post-compact codewords; (d) fork mechanics +
kill-both/revive-both land correctly WHEN both ids are stamped (branch codewords stayed
isolated); (e) turn-less codex revive still N/As correctly (0.145 writes the rollout only at
FIRST TURN, not launch — filename ts ≈ launch is just the minted session ts).
TWO REAL BUGS (shared root cause: a HEADED codex thread's agent_session_id is NEVER stamped —
only revive/headless-turn stamp it, so it sits "" through any number of headed turns):
1. **Fork of a live headed codex thread 409s** "no transcript on disk (no turn yet)" —
   misleading; transcript exists, TranscriptPath(kind,"") is the failure. Works only after one
   stop→revive cycle. (TUI `F` on a codex row hits this too. The green fork cells never caught
   it — they fork headless-started sources, which are always stamped.)
2. **THE BAD ONE — revive discovery mis-lands with two codex threads in ONE cwd** (both
   unstamped = the default): kill both, revive the OLDER → DiscoverCodexSession picks the
   NEWEST same-cwd rollout → resumes the WRONG conversation + permanently stamps the wrong id;
   reviving the second lands BOTH threads on the SAME codex session (two codex processes
   appending to one rollout — cross-contamination visible in the notify payload's
   input-messages), the older conversation ORPHANED on disk (recovery: thread adopt
   --agent codex --session-id <id>). Silent-wrong-behavior class (the --machine X failure).
FIX PATH (proven viable, not built): codex's notify payload CARRIES the session id —
`{"type":"agent-turn-complete","thread-id":"<rollout id>","turn-id":...,"cwd":...}` (verified
via a tap on the materialized codex-notify.sh; remember H60: any codex SPAWN re-materializes
that script and WIPES taps — tap only while the target thread is already live). codex-notify.sh
already reports turn_ended_no_authority with $SESH_THREAD_ID → extend it to pass thread-id and
stamp agent_session_id on first report; discovery becomes a legacy fallback. Ticket 49d4299b
(triage) has the full spec + matrix-cell asks.
EXPERIMENT GOTCHAS: `thread send --wait` has NO --until flag (that's `thread wait`); --timeout
wants a duration ("120s"). Sending "/compact" via thread send triggers the slash command fine.
Codex renders resumed history in the TUI, so codeword RECALL (a real turn) is the honest probe,
not the rendered backlog.

## H58 — phantom BUSY from an Esc'd claude turn: authority STALENESS BOUND (2026-07-25, sesh ff8e65f; NO schema change; deployed ALL FIVE)
Lukas: thread ab9d5a3a (dagster-netrun, claude on mymain) showed busy while "very idle".
DIAGNOSIS TRAIL (worth remembering as a recipe): grid said busy but `thread status --id` said
IDLE → the maintained snapshot disagreed with the on-demand probe → checked the row's
`state_authority` = **reported** → a stuck AUTHORITY entry (schema 43's in-agent reporter
overrides the content-diff), NOT the diff. The marked pane was byte-stable across captures.
Transcript tail (~/.claude/projects/<cwd-slug>/<agent-session-id>.jsonl) showed the last turn
ended with **"[Request interrupted by user]"** (Esc, 12:08) — and **claude's Stop hook does NOT
fire on a user interrupt**, so integrations/claude/sesh-agent-state.sh reported turn_started
with no turn_ended; authority is bounded only by PANE liveness and the idle claude's pane
lives on ⇒ busy pinned until the thread's next prompt. (The 12:12 away_summary fired no Stop
either.) IMMEDIATE UNSTICK: `sesh thread report-state --id <id> --event release --source
sesh:manual-unstick` (seq defaults to unix-nanos so it always beats the stored seq; release
deletes the entry; next tick publishes heuristic idle).
FIX (conferred; Lukas picked the daemon staleness bound over hook-side Notification mapping):
maintainer drops a reported-BUSY, NON-blocked entry when the pane content changed ZERO times
for authorityStaleBound (2min) AND the report is ≥ that old (the report-age guard keeps a fresh
turn_started on a long-frozen pane alive until its first render). Rationale: a real in-flight
claude/pi turn ANIMATES every second (spinner/elapsed timer) — a frozen pane contradicts the
report. Drop is LOUD (log names thread/source/ages) + VISIBLE (state_authority reported→
heuristic). Mechanics: liveState.lastChange = when pane bytes last differed — deliberately
separate from lastActive, which the authority path BUMPS every tick while reported-busy (using
lastActive would defeat the bound). BLOCKED entries exempt (a question/permission prompt is
genuinely mid-turn with a static pane) — RESIDUAL GAP: Esc-ing OUT of a permission/question
prompt can still pin blocked-busy until the next prompt (accepted; rarer than Esc-ing a turn).
The bound also catches the late-PostToolUse race (unblocked landing after Stop's turn_ended
re-pins busy with a later seq — PostToolUse maps to unblocked ⇒ busy=true).
TESTS: TestStaleReportedBusy predicate truth table; TestMaintainerDropsStaleReportedBusy =
real maintainer + real tmux pane occupied by an argv0-"claude" SYMLINK TO SLEEP (gotcha: a
shebang script reads as argv0 "sh" and misses agentRe — symlink keeps argv0) with injectable
m.staleBound=2s: pins fresh → drops past bound → blocked stays pinned. thread.state-authority
cells (claude+pi × local+remote, real agents) stay green — real turns animate, bound never
fires. SKILL state-authority paragraph updated.
DEPLOY (binary + daemon restart, NO schema change): all five at ff8e65f (mymain native+
supervisorctl; macbook/macstudio homebrew go + supervisorctl; ideapad native + supervisorctl;
termux plain go build + explicit-pid kill + setsid-nohup, pid 9327). Verified the netrun
thread reads idle/heuristic.

## H57 — the PERSISTENT SIDEBAR (issue #8 sesh half): `tui --sidebar` + traveling-slot cockpit rig + Tab view picker (2026-07-24/25, branch ui-sidebar merged c843a5d; NO schema change, binary-only; deployed ALL FIVE, no restarts; myrig phase PENDING)
Issue #8 design-first: conferred (AskUserQuestion) → tmux-layered, plain thread list,
desktops only; then SIX live iterations with Lukas on his macbook master. Design doc
_dev/SIDEBAR.md is the reference (kept current through every pivot). Mechanism all in
sesh; the cockpit rig on macbook is TRANSIENT (hook+toggle scripts in /tmp, tmux
bind/hook on the live server — dies with the master; myrig phase makes it real).
- MODE (`sesh tui --sidebar`): NAME-only column preset (SidebarColumns; [tui] columns +
  [[tui.column]] moves deliberately skipped — wide-grid tuning; explicit --columns wins);
  nav DOESN'T QUIT — focus hands to the sibling pane (focusSiblingPane: plain `tmux
  select-pane -t :.+` via the pane's own $TMUX); SINGLE click enters (jump list; fold
  marker still folds); esc/q NO-OPS (Lukas hit Esc, the pane died and took the traveling
  slot — ctrl+c stays the deliberate kill); NO "entered" note (the switched pane IS the
  feedback).
- LAYOUT SAGA (the awkwardness ladder): per-window sidebars (4 copies, state doesn't
  travel) → join-pane traveling REJECTED pre-build (systematic ±38-col resize churn =
  inner reflow on every switch) → **TRAVELING over FIXED SLOTS**: every master window
  keeps a permanent 38-col left slot (blank `sleep` placeholder pane, @sesh-sidebar-slot
  marker); ONE sidebar (@sesh-sidebar) swap-panes into the active window's slot via an
  after-select-window hook — same-size swap ⇒ ZERO resizes. GOTCHAS: parked windows sit
  at detached-default 80x24 until visited — PRE-resize-window them to the client size or
  the first visit rescales the slot; capture split-window's pane id via -P -F
  '#{pane_id}' (matching pane_current_command races the shell startup); macOS: never
  overwrite a running binary in place (.new+mv) — an in-place build once made the next
  spawn die silently.
- FOLLOW (the headline): moving the selection PREVIEWS the thread, focus STAYS in the
  sidebar; Enter/click commits focus. Latency reworked twice on Lukas's feel: 300ms
  debounce → FIRE-IMMEDIATELY + COALESCE (followInFlight latch; completion re-arms for
  the current cursor; a FAILED target records lastFollowedID too or error→re-arm→
  still-selected loops the broken nav forever — caught by the unit test) + LOCAL FAST
  PATH (row on the current window's machine == local machine ⇒ ONE warm client.TmuxNav
  {Session,Origin,ThreadID} on the unix socket — no subprocess, no master select; ~a
  tmux switch). Cross-machine follow WORKS (the traveling sidebar rides the window
  switch — the original same-machine guard became obsolete): sesh declares
  @sesh-sidebar-intent=follow|enter (global option on the master server,
  declareSidebarIntent, cleared on nav failure) and the swap hook consumes-and-clears:
  follow→select the sidebar (keep arrowing), enter→select the attach, absent (manual
  prefix+N)→leave focus alone. Policy: live HEADFUL only (preview never revives),
  reachable owner, dedup, sibling machine from $SESH_TUI_MASTER_MACHINE (pinned) else
  the WINDOW NAME resolved LIVE per follow (followResolver func — a same-size swap
  raises no resize event to re-cache on; window-name also feeds WithMasterCursor start
  preselect).
- TAB VIEW PICKER (all TUI modes, not just sidebar): Tab opens a popup listing every
  view, PRESELECTING THE NEXT (tab+enter ≡ old cycle); tab/↑↓ wrap, enter applies,
  esc cancels, wheel moves, CLICK applies (rows at line 2 — viewPickerRowAtY mirrors);
  filter-mode Tab same (dispatches above filter, filter survives); tickets-view tab
  untouched. Conformance nextView(t,m) helper replaced 15 KeyTab sites.
- TESTS: sidebar_test.go (nav-no-quit, esc-guard, single-click, follow truth table incl.
  the retry-loop guard + in-flight swallow + coalesce, picker, preset); claim
  sidebar-nav-stays (registered AND declared): real daemon + 2 real pi + real master —
  one arrow press drives the REAL fast-path client.TmuxNav (thread session parked on a
  2nd window lands back on the thread's window = the H8 --thread observable), then enter
  = subprocess master path (master window switch + no quit); claim scrubs $TMUX before
  running cmds so focus/intent writes can NEVER touch the developer's live server (same
  in units — followNav writes tmux options via ambient $TMUX!). view-cycle-tab claim
  rewritten for the picker incl. real click-apply. Anti-gaming neuters each round.
  REPAIRED stale claim en route: action-mutate-remote still expected the pre-H54 archive
  confirm. FULL TUI claims 64/64 green at merge.
- MACBOOK RIG (transient): slots+markers in all 4 windows, /tmp/sesh-sidebar-swap.sh
  (after-select-window hook: swap + width-enforce + intent consume),
  /tmp/sesh-sidebar-toggle.sh on prefix+b — HIDE removes sidebar AND all slots (full
  width back; leaving blank slots read as "still there" — Lukas), SHOW rebuilds slots
  everywhere + sidebar in the current window focused; revives a dead pane. Test binary
  ~/.local/bin/sesh-sidebar (now == main).
- MYRIG PHASE (pending, the follow-up): conf-owned hook + prefix+b toggle + mastermaint
  slot self-heal (new windows have no slot until a toggle cycle) + termux excluded. NB
  Lukas asked whether the myrig cockpit layer should move INTO sesh — assessment given
  (see issue #8 / the session): keep personal policy (bindings/menus/styling) in myrig,
  move the STRUCTURAL sidebar machinery (hook/toggle/slots, the intent protocol's
  consumer) into sesh as mastermaint + a `sesh master sidebar` verb so the intent
  protocol lives in one repo and gets conformance cells.
DEPLOY: merged --no-ff c843a5d, binary-only ALL FIVE (mymain native; macbook/macstudio
/opt/homebrew/bin/go; ideapad native; termux plain go build CGO=1) — no daemon
restarts, schema stays 45. The Tab picker is now LIVE in the normal TUI fleet-wide.

## H60 — "codex doesn't flag": mechanism was HEALTHY, the ATTENDED GATE was the culprit — and Lukas REMOVED the gate entirely (2026-07-25, sesh a3cb29d, myrig ae78f1b; NO schema change; daemon RESTART all five)
Lukas: codex threads (corkboard 9331d905, then 547230fa) neither flag nor toast on turn
end. DIAGNOSIS (long, with one nasty trap): env ✓ (SESH_THREAD_ID/SESH_BIN in the codex
process), config ✓ (notify= line predates process start), version 0.145.0 (updated from
0.142.5 since H50 — codex now has an app-server architecture; logs moved into
CODEX_HOME/logs_2.sqlite, no more log files). Manual fire of the notify script → flagged
✓. THE TRAP: **a sesh codex SPAWN re-materializes <SESH_HOME>/codex-notify.sh, silently
WIPING any debug tap in it** — my first scratch spawn destroyed the tap, and every
"codex never invokes notify" probe result after it was VOID (raw-tmux probes also
self-gate silently: no SESH_THREAD_ID → exit 0). A clean re-test (spawn FIRST, tap
AFTER, then drive the turn) proved the live chain fully healthy: invoked + report rc=0 +
flagged "turn ended". ⇒ his threads simply always hit the H48-style attended gate:
input in that session <60s before the fast reply landed. Bisect scaffolding that's
reusable: isolated CODEX_HOME probes with tmux + config copies (/tmp/cxbisect-*),
tapped scripts logging invocation+rc.
RULING (Lukas): "Even attended threads should trigger notifications and flagging.
That feature should be removed." DONE both sides:
- sesh a3cb29d: attendedNow/flagAttendedWindow DELETED; autoFlagTrigger loses the
  attachment/activity/now params (turn-end + stall flag unconditionally; heuristic
  edges still gated by [flags] heuristic_agents; stall latch unchanged); the codex
  turn_ended_no_authority path AutoFlags without consulting the snapshot. The H48
  attachment/activity plumbing (snapshot field + hook env) STAYS — only flag policy
  stopped reading it. Truth table rewritten; all six thread.flagged cells green;
  daemon -race clean.
- myrig ae78f1b: sesh-notify drops its belt-and-braces attended gate — the
  notify-flagged toast fires on every flag edge; SESH_NOTIFY stays the only gate.
CONSEQUENCE (told Lukas): actively conversing with a thread now flags it on every
reply; the flag just STAYS set during a conversation (no repeated edges while
flagged), so one toast per unflag-cycle, and rows stay ⚑ in active until manually
cleared. DEPLOY: rebuild + daemon RESTART all five (schema stays 45); myrig pull all
five; macbook sidebar respawned; the diagnostic tap stripped from mymain's
codex-notify.sh and /tmp probe debris removed.

## H59 — sidebar/TUI polish batch: picker-on-current, flagged-ALWAYS-in-active, maximize-adaptive sidebar columns (+prefix+z), selection glyph CHIPS (2026-07-25, sesh f55cf5b+9c23cae, myrig fc51a3b; NO schema change, binary-only; deployed ALL FIVE)
Four asks from Lukas post-#8-close. NB a CONCURRENT session took H58 + pushed sesh
ff8e65f/6661218 (authority staleness bound, daemon change, THEY deployed+restarted) —
fetch+rebase caught it mid-push; my batch is TUI-only so binary-only deploy composes
(running daemons keep their restarted ff8e65f code).
- PICKER opens ON the CURRENT view (preselect-next was disorienting). Conformance
  nextView helper = tab+tab+enter now.
- ACTIVE VIEW: `flagged OR ((!archived || headful) && !onHold)` — a FLAGGED thread
  ALWAYS shows (attention beats parking; manual unflag re-hides). Optimistic hides
  compose free via leavesViewWith/builtinViewAdmits. Truth table extended.
- MAXIMIZE-ADAPTIVE sidebar columns: pane >= sidebarWideThreshold(80) → the FULL grid
  set (config [tui] columns + [[tui.column]] moves, resolved via runTUI's new shared
  gridColumnSet closure; WithSidebarWideColumns; effectiveColumnNames swaps per
  render — zoom raises a resize, zero extra wiring; explicit --columns pins/disables).
  myrig fc51a3b: prefix+z → sidebar-zoom.sh = zoom PINNED to the @sesh-sidebar pane
  from anywhere (resize-pane -Z -t; falls back to default zoom when no sidebar).
  LIVE-PROVEN macbook: zoomed=1 + sidebar active, restore clean.
- SELECTION CHIPS: gutter glyph tints (▶/↓/⚑) now SURVIVE selection — each tinted
  glyph renders its [[tui.glyph_color]] style COMPOSED WITH Reverse(true): under
  reverse the fg becomes the visible cell BACKGROUND → a coloured chip inside an
  unbroken reverse band (H49's drop-tint-on-selected reversed). Column colours (TKT!
  red) still yield to the band — chips are for the 1-cell attention glyphs only.
  TestViewTintsRunningGlyphs re-pinned to the new contract (SGR carries 7 AND colour).
- Earlier same batch (already in H57 follow-up flow): prefix+v simplified to plain
  `select-pane -t :.+` (alias of prefix+o).
DEPLOY: binary-only all five at 9c23cae (no restarts; H58's daemons untouched);
macbook sidebar respawned. myrig render+source-file all five at fc51a3b.

### H57 follow-up — sidebar polish + the myrig phase LANDED; port-to-sesh DECLINED (2026-07-25, sesh cc69e8f, myrig a05b793; deployed ALL FIVE)
Same-day iterations after the merge: filter-Enter in sidebar mode EXITS search
(nav captures the FILTERED selection FIRST — clearing reshapes the visible set
under the cursor — then query cleared + cursor re-landed on the entered thread;
popup untouched; sesh cc69e8f, fleet-uniform binary). prefix+v = plain pane
cycle (`select-pane -t :.+`, alias of prefix+o — Lukas simplified it from an
earlier focus-the-sidebar script). MYRIG PERMANENCE (a05b793): the transient
rig became home/.sesh/myrig/sidebar-{swap,toggle}.sh (symlinked) + conf
set-hook/bind b/bind v + mmt-start/mmt-ensure run `sidebar-toggle.sh ensure`;
the swap hook now LAZILY provisions a slot in windows born after the last
toggle (closes the new-window gap). Scripts no-op on termux
(SESH_MACHINE/TERMUX_VERSION guards); socket from $SESH_MASTER_SOCKET; sidebar
spawns from ~/.local/bin/sesh. macbook's transient rig (tmp scripts,
sesh-sidebar binary) removed; running masters re-sourced (mymain, macbook,
termux — termux self-guards); macstudio/ideapad have no master. PORT-TO-SESH
DECLINED (Lukas asked "worth it?"; my earlier lean reversed on honest sizing):
~60 lines of on-demand shell vs a daemon reconciler continuously acting on the
live cockpit — revisit triggers recorded in _dev/SIDEBAR.md. **THE INTENT
CONTRACT is cross-repo**: sesh internal/tui declareSidebarIntent writes
@sesh-sidebar-intent=follow|enter; myrig sidebar-swap.sh consumes-and-clears —
renaming it breaks both repos (documented both ends). Issue #8 CLOSED.

## H56 — remote-action lag KILLED (H55's A+D+C shipped): keypress optimism w/ per-action revert + full-uuid resolve fast path + post-write mesh nudge (2026-07-24, sesh 436dd31 api 44→45, NO store migration; deployed ALL FIVE)
Lukas approved H55's recommendation verbatim ("Okay do that (A+D+C)"); E/F not built.
- A — KEYPRESS OPTIMISM, PER-ACTION REVERT (internal/tui/model.go, the H36-item-3 debt):
  Model.pending REBUILT from a per-thread merged map[string]*rowPatch into an ORDERED
  per-action list []*rowPatch (rowPatch gains id/seq/confirmed; Model.patchSeq mints).
  recordPatch() stamps+applies at KEYPRESS (provisional deadline; reanchors the cursor
  over a hide) and SUPERSEDES older same-thread entries' replaced fields (supersededBy:
  two quick notify toggles must not leave entry-1 demanding a value the server never
  shows → bogus loud expiry; an entry left hide-only after its fields are superseded is
  SPENT and dropped — which also fixed a real latent bug: U-during-pending-archive kept
  the row hidden 15s then warned "sync degraded"). actionMsg carries seq NOT a patch:
  success = confirmPatch (deadline re-stamped from confirmation so TTL bounds only the
  read path), failure = dropPatch (revert EXACTLY that entry + loud actionErr + refetch)
  — per-action identity is what makes revert not clobber merged siblings (the old H36
  blocker). flag/^f FINALLY have patches (rowPatch flagged/flagDisabled; flagPatch
  mirrors the daemon one-rule semantics + leavesViewWith hide for flag-filtered custom
  views) — flag was the worst offender (NO patch at all, 1-3s perceived). Pin path
  converted (applyPinPatch returns seq; persistPin/pinDoneMsg carry it). GOTCHA baked
  into the code comments: mutating helpers are now POINTER receivers, so call sites MUST
  bind the cmd BEFORE returning m (`cmd := m.x(); return m, cmd`) — `return m, m.x()`
  has UNSPECIFIED operand-evaluation order and can return the pre-mutation copy. Deleted
  dead archiveSelected/deleteSelected (uncalled since H54's rewiring). stampPatch gone.
- D — FULL-UUID RESOLVE FAST PATH (cmd/sesh/current.go): resolveIDPrefix skips
  listAllThreads when --id is a canonical 36-char lowercase-hex uuid — on a ROUTED verb
  that list pull was the peer's ENTIRE thread set (incl. archived) fetched only to
  expand a prefix, and the TUI always passes full row.IDs. Unknown full uuid still loud
  via the daemon's 404 at the verb; uppercase/short forms fall through unchanged.
  resolveMeshThreadID inherits it (await w/ a bogus full uuid errors "vanished while
  waiting" on the first poll — still immediate + loud).
- C — POST-WRITE MESH NUDGE (api 44→45, additive; only the CALLING machine needs 45).
  POST /v1/mesh/nudge {machine} on the LOCAL daemon syncs that ONE peer now
  (meshSync.nudgePeer via a launchSync refactor sharing tick()'s inflight guard; bumps
  mesh demand so active cadence covers the race with an in-flight pre-write fetch;
  handler returns BEFORE the fetch — the caller is an exiting CLI). Unknown machine 404,
  self/empty 400 — loud. cmd/sesh nudges after EVERY successfully routed --machine
  command (ssh-handled inline; http-routed via a postRouteNudge closure after the
  dispatch switch — cfg captured BEFORE routeMachine sets SESH_REMOTE so the nudge hits
  the LOCAL daemon). BEST-EFFORT by design (errors dropped, commented why): the routed
  command already succeeded; a pre-45/down local daemon just degrades freshness to the
  old cadence — nothing masked, observable in `sesh mesh` synced_at.
- TESTS (anti-gaming: neutered nudgePeer's launchSync → nudge test red; neutered
  recordPatch's apply → 3 keypress tests red; both reverse-edited): TestMeshNudgeSyncsPeerNow
  (REAL handler+store+httptest peer, NO run loop — the nudge alone fetches);
  TestKeypressOptimismAndRevert / PerActionRevertKeepsSiblings / SupersedeSameField /
  UnarchiveSupersedesArchiveHide / FlagKeypressOptimism; pending_hide/columns/pin suites
  ported to the list (pendingFor = merged read-only view for tests); TestIsFullUUID +
  TestResolveIDPrefixFullUUIDSkipsList (nil client proves no list call). REPAIRED
  pre-existing red: claim action-mutate-remote still expected the PRE-H54 archive
  confirm popup (H54 updated claimActionArchive but missed this one) — now asserts
  instant archive + keypress hide. Green: tui/daemon/cmd -race, FULL TUI claims, mesh.*
  cells both transports, thread.delete + thread.flagged 6/6.
- LIVE-MEASURED (mymain→ideapad, scratch thread, vs H55 baselines): routed flag verb
  190-240ms → 118-120ms (D's saved round trip + list transfer); flag visible in the
  LOCAL mesh cache 600-1400ms → 96-454ms after verb return. The residual spread is the
  PEER's ~300ms maintainer publish tick racing the nudge fetch — when the nudge arrives
  pre-publish, the demand-bumped active cadence catches it next round (~450ms); no
  sleep-hack added, and perceived TUI latency is ~0ms anyway via A's keypress patch.
- DEPLOY (schema 45 = rebuild + daemon RESTART): mymain (native + supervisorctl),
  macstudio (/opt/homebrew/bin/go + supervisorctl), ideapad (native + supervisorctl),
  termux (plain go build CGO=1 per H22, explicit-pid kill 5973 + setsid-nohup relaunch
  → pid 8868, schema 45 verified on-box); macbook came back online later the same day
  and was deployed the standard way (pull + /opt/homebrew/bin/go + supervisorctl) →
  schema 45, mesh synced — ALL FIVE current. SKILL sync: id-prefix section notes the
  full-uuid fast path. sesh-ui: NO change (its API surface unchanged; it could adopt
  the nudge pattern after writes if wanted — follow-up only).

## H49 — attention-glyph COLOURS: ▶/↓ bright green, TKT! red (2026-07-23, sesh 8bc529e + 9afdb10; NO schema change; deployed ALL FIVE)
Lukas: colour the attention glyphs — ▶ (busy) green, the TKT! `!` red, ↓ (descendant running)
green; then mid-turn "make it a brighter green" → palette "2"→"10" (9afdb10). PURE TUI-client
render change ⇒ deploy = binary only, NO daemon restart. TWO mechanisms, matching each glyph's
home: (a) ▶/↓ are GUTTER glyphs (not columns) → new hardcoded styleRunning
(lipgloss Foreground "10", model.go) applied in View()'s NON-selected branch only — the
selected row stays untinted (reverse video is the dominant cue, the exact rule renderCells
follows for column colours); idle `·`/`?` untinted. (b) the `!` lives in the ticket_input
COLUMN → added {ticket_input, red} to DefaultColumnColors (colors.go) so the EXISTING
[[tui.column_color]] machinery renders it and config can still override/clear. Lukas's live
config already has ticket_input at position 1 ⇒ no settings change needed.
TESTS: TestResolveColumnColorsDefaults extended (ticket_input present);
TestViewTintsRunningGlyphs (descendant_test.go) forces lipgloss.SetColorProfile(termenv.ANSI)
— DETERMINISTIC off-tty, where the profile is Ascii and styles render as no-ops — and asserts
an SGR immediately precedes ▶ (busy row) / ↓ (parent of a running child), and that selecting
the busy row DROPS the tint (the reverse-video wrap's SGR is at line start, not glyph-adjacent,
so the regex `\x1b\[[0-9;]*m▶` discriminates). Anti-gaming: neutered the tint (`if false &&`)
→ test red, reversed the edit (never git-checkout, the H44 lesson). Existing tests were
ANSI-safe by audit: all glyph assertions are single-rune Contains (survive ANSI wrapping) or
already strip ANSI; no test asserts contiguous "●▶". termenv was ALREADY a direct go.mod dep.
Conformance descendant-running-glyph claim green post-change (real daemon + real pi turn).
LIVE-PROVEN (isolated tmux `sesh tui` vs the live daemon, read-only, capture-pane -e): 4 busy
rows rendered `ESC[32m▶` at 8bc529e, then `ESC[92m▶` after the brighten — my own thread's
in-flight turn guarantees a busy row during a smoke.
DEPLOY GOTCHA (new): mymain's ~/mysetup/sesh checkout was on ANOTHER AGENT'S WIP branch
(herdr-steals, uncommitted edits — the issue #6 done/seen feature) — do NOT pull/switch it;
deployed via a THROWAWAY `git clone --depth 1` in /tmp, built from there, rm'd the clone
(vcs.revision stamps fine from a shallow clone). Other four: normal pull+build (.new+mv;
termux plain go build CGO=1 per H22). ALSO: hit the H30-class zsh `=word` gotcha AGAIN — a
bare `echo ===` in a compound aborts the WHOLE line ("== not found") before later commands
run; a `git stash` in that line never executed (verified stash list empty before proceeding).
SKILL sync: glyph-list prose (bright green, reverse-video-wins) + column_color defaults in
sesh-cli SKILL. No help.go change (no flag/key/column surface change). sesh-ui unaffected
(GUI with its own rendering — terminal-TUI-only).

### H49 follow-up — glyph colours now CONFIG-CONTROLLABLE ([[tui.glyph_color]]) + Lukas's pick = "2" (sesh 1d20908 + myrig c1d649a; deployed — myrig render 4/5, mymain PENDING)
Lukas (after a swatch comparison — printing `\e[38;5;Nm▶` candidates in his terminal worked
well for choosing): "make all of them settings controllable instead, and set them to '2' in
my config". styleRunning DELETED; new [[tui.glyph_color]] mirrors [[tui.column_color]]
exactly: GlyphColorSpec + DefaultGlyphColors (busy/descendant both "10" = the previous
hardcoded bright green) + ResolveGlyphColors (empty colour clears; unknown glyph/bad colour
LOUD) in colors.go; Model.glyphColors + WithGlyphColors; config.TUIConfig.GlyphColors
(toml "glyph_color"); cmd/sesh/tui.go wires it beside the column colours. The `!` was
ALREADY config-controllable (column_color ticket_input). NB BurntSushi toml.Unmarshal
IGNORES unknown keys — verified before shipping, so the new config key is harmless to old
binaries AND to the daemon's section loaders (deploy order irrelevant; no restart, TUI
reads [tui] at launch). POLICY in myrig c1d649a: config.toml.jinja gets active
[[tui.glyph_color]] busy=2 + descendant=2 (after the [tui] scalars, TOML
subtables-after-scalars). Tests: ResolveGlyphColors defaults/override+clear/loud;
TestViewTintsRunningGlyphs wires ResolveGlyphColors(nil) + a cleared→untinted case;
TestLoadTUIGlyphColors (config parse). LIVE-PROVEN macbook: isolated-tmux sesh tui vs the
live daemon captured `ESC[32m▶` = palette 2 from the RENDERED config (was [92m = 10).
DEPLOY: sesh binary 1d20908 ALL FIVE (mymain again via throwaway shallow clone — its
checkout still on herdr-steals). myrig render: macbook (uv --with jinja2 — its BARE python3
LACKS jinja2 now, H46 was right / H43 stale; and DON'T >/dev/null the first render attempt,
grep the rendered file), macstudio + ideapad (uv), termux (python3) all show 3
glyph_color hits. **mymain myrig PENDING**: its checkout has ANOTHER AGENT'S unpushed local
commits (8df129b "claude turn-state hooks + exact-edge notify policy for sesh schema 43" +
e2e02a4 ghostty) — 8df129b likely touches config.toml.jinja ([[hooks]] lives there), so a
pull --rebase risks a conflict inside their in-flight work; left untouched, self-heals when
they sync (harmless meanwhile: mymain TUI shows default bright green; NEVER install-home
from a temp clone — it would re-point the home symlinks INTO the clone).

## H48 — H47's attached-gate KILLED all macbook toasts (parked cockpit clients): activity+flip ages (2026-07-22, sesh 1165589 api 41→42, myrig 7e3ce6e; deployed ALL FIVE)
Lukas: "notifications don't work anymore on macbook" — the day after H47. DIAGNOSIS: his ONE
opted-in thread (corkboard-codex on mymain, id a2f69b62 = the codex agent from the corkboard
collab) read attached ~ALWAYS: its session held TWO clients, one with input 14s fresh (him)
and one PARKED since the previous day (activity ~6h stale — cockpit clients park on sessions).
H47's blanket attached-gate therefore suppressed every toast for exactly the thread he watches.
ATTACHMENT IS THE WRONG PROXY for "the user is watching". MEASURED FACTS (isolated tmux):
tmux `client_activity` = last INPUT from a client — typing bumps it, agent output does NOT,
and **switch-client does NOT** (so nav needs a different signal: the attachment FLIP itself).
CONFERRED (AskUserQuestion): Lukas picked activity+nav-window over drop-the-gate / keep-and-
detach.
- MECHANISM (sesh 1165589, api 41→42 additive/omitempty): ThreadSnapshot.attached_activity_unix
  = MAX client_activity among clients attached to the thread's session, stamped by the owning
  maintainer's EXISTING per-tick probe (tmux.AttachedSessions now returns map[session]→activity;
  format `#{client_activity}\t#{client_session}` — activity FIRST since session names may hold
  spaces but never the TAB tmux passes verbatim; malformed lines error loudly). Eventer keeps an
  observer-LOCAL attachFlip map (id → when THIS observer saw the attachment axis change; no wire
  change needed — flips recorded BEFORE emitting the same tick's events so a nav-caused busy edge
  reads flip-age ~0; deleted with the thread). decorate() stamps Event.AttachedActivityAgo +
  AttachmentChangedAgo (-1 = unknown; Env() OMITS unknowns — a hook's numeric test on an empty
  var fails OPEN; exporting 0/-1 would wrongly suppress). Env vars SESH_ATTACHED_ACTIVITY_AGO /
  SESH_ATTACHMENT_CHANGED_AGO. hooksapi builds test Events with explicit -1 (zero-value Event =
  "input 0s ago" footgun) + computes real activity age from the thread's snapshot.
- POLICY (myrig 7e3ce6e, sesh-notify): suppress iff attached AND (ACTIVITY_AGO<60 OR
  CHANGED_AGO<30). Parked (stale both) → TOAST. Absent vars (old daemon) → toast (fail open).
  Known accepted gap: a turn finishing <60s after your last keystroke in that thread is silent.
- TESTS: TestEventEnv contract (+2 vars; absent-when-unknown; the ENTRY-COUNT check works — it
  caught my own missing SESH_SESSION in H47); TestEventerDecorate (unknown/-1, real ages,
  future-clock clamps to 0 not negative); tmux TestAttachedSessionsActivity = REAL nested client
  (`env -u TMUX tmux attach` in a driver pane): attached present w/ sane activity, input THROUGH
  the client bumps it (sleep 1.5s first — tmux stamps whole seconds), detached absent.
  Conformance green: thread.notify local+remote, thread.runtime-state/pi/local, mesh.snapshot
  (+.http). NB ~20 files fail `gofmt -l` on clean HEAD (toolchain drift) — format ONLY touched
  files, don't sweep.
- GOTCHA: `sesh hooks test --thread` resolves LOCAL-only (stateOf/GetThread on that daemon) — it
  CANNOT exercise a remote thread's event even though the eventer observes remote threads fine.
  Verified the remote case by parts instead: macbook's CACHED mesh row of the mymain thread
  carries attached_activity_unix (same stale value), + env-injected sesh-notify runs on macbook
  proved all four gate directions (fresh-input gated / fresh-flip gated / parked TOASTS /
  no-vars TOASTS).
- sesh-ui: NO change (doesn't render activity; hooks are daemon-side).
DEPLOY (schema 42 = rebuild + daemon RESTART all five + myrig pull): mymain (native), macbook +
macstudio (/opt/homebrew/bin/go), ideapad (native) via supervisorctl; termux plain go build +
explicit-pid kill + setsid-nohup relaunch. All five verified api schema 42.

## H47 — spurious toasts on nav/typing: SESH_ATTACHMENT in hook env + attached-gate in sesh-notify (2026-07-21, sesh 2d7441a + myrig 298d2b3; NO schema change; deployed ALL FIVE)
Lukas: notifications fired just from NAVIGATING onto a thread in sesh tui, or from TYPING into
it. ROOT CAUSE (mechanism, not a bug per se): busy is a pane CONTENT-DIFF (≥2 changes in 2s,
maintainer.go) and cannot attribute changes — keystroke ECHOES and the resize/redraw of
switching a client onto a session (window-size latest ⇒ reflow+repaint) latch busy exactly
like agent output; when the user stops, the settle emits busy_changed busy→idle = the edge
notify-idle subscribes to ⇒ toast for your own interaction.
FIX (mechanism/policy split per the events.go doctrine — the daemon never decides what a
notification is): sesh 2d7441a adds SESH_ATTACHMENT ("attached"/"detached") to Event.Env()
from the snapshot's attachment axis; myrig 298d2b3 gates in sesh-notify — exit 0 when
EXACTLY "attached" (empty/absent = pre-2d7441a daemon ⇒ still notify; fail OPEN). Attached =
a tmux client is on the session = you're looking at it — covers BOTH symptoms (nav + typing
imply attachment). KEY PRECONDITION verified: attachment RIDES THE MESH snapshot to the
observing daemon — H44's peer-facing slim was REJECTED (api.go:190; Lukas), snapshots are
full, so the gate works for remote threads too. KNOWN EDGE (accepted): interact → nav AWAY
within the idle-confirm window → idle fires while detached ⇒ still toasts.
TESTS: TestEventEnv (internal/daemon/events_test.go) pins the FULL hook-env contract with an
entry-count check so a new env var FAILS the test until added (bit me immediately — I'd
missed SESH_SESSION from my own want-map); thread.notify local cell's hook echoes
att=$SESH_ATTACHMENT and both gate assertions require att=detached (headless thread = no
pane = detached), proving wire delivery through a REAL daemon + real pi turns; local+remote
cells green. SKILL sync: sesh-cli SKILL now lists ALL hook env vars (there was no list
anywhere before — config/hooks.go only says "SESH_EVENT_* variables").
LIVE-PROVEN on macbook through the real chain (hooks test --thread): the ATTACHED thread
(chanu-dashboards — Lukas's live session) → "ran ok" with NO hs.notify line = gated; a
DETACHED thread → hs.notify toast fired. Direct env-injected sesh-notify runs confirmed both
gate directions too.
DEPLOY (binary + daemon RESTART all five — only hook machines macbook/ideapad strictly need
it, fleet kept consistent; myrig = symlinked script, git pull): mymain (native, supervisorctl),
macbook + macstudio (/opt/homebrew/bin/go + supervisorctl), ideapad (native + supervisorctl),
termux (plain go build = CGO=1/android per H22, kill by explicit pid + setsid-nohup relaunch,
pid 19399). All on 2d7441a.

## H46 — notifications now OPT-IN fleet-wide + macbook notify was a MUTED HOOK (2026-07-21, myrig e254b11; NO sesh change; deployed ALL FIVE)
Lukas: "threads should have notifications off by default" + "notifications don't work on my
macbook anyway". TWO separate things, one policy change:
- DEFAULT OFF (myrig e254b11): `[defaults] notifications` true→false in home/.sesh/
  config.toml.jinja — new threads start with the notify gate OFF; opt in per thread with
  `sesh thread notify --on`. This is a RECORD-CREATION knob read by the daemon at start ⇒
  deploy = re-render config.toml (install-home) + daemon RESTART, all five (mymain native
  python3; macbook/macstudio/ideapad `uv run --with jinja2` — ideapad's bare python3 LACKS
  jinja2, first render silently produced nothing and the restart ran on the OLD config, caught
  by grepping the rendered file; termux python3 has jinja2, daemon relaunched the termux way —
  explicit pid kill + setsid-nohup, pid 21742). ALSO bulk-flipped EVERY existing thread
  (incl. --archived — an archived-but-headful thread still fires busy→idle per H40) to
  notify=off via `thread notify --off` loops: all were true = the old default, ZERO explicit
  opt-ins existed, so nothing deliberate was lost. 0 notify-on remaining mesh-wide.
- MACBOOK ROOT CAUSE: the notify-idle [[hooks]] entry (rendered only on macbook/ideapad —
  the machines Lukas sits at; observer-bound, covers the whole mesh) was `muted: true` on
  macbook's daemon — hook mute is PERSISTED in the store (store.SetHookMuted, survives
  restarts), so someone ran `sesh hooks disable` there at some point (no timestamp — unknown
  who/when). NOT a PATH/env problem: `hs` resolves fine in a non-login zsh (hooks run via
  `$SHELL -c`). Fixed with `sesh hooks enable --name notify-idle`; ideapad was already enabled.
- VERIFY GOTCHAS: (a) `sesh hooks test --name notify-idle` with NO --thread builds a SYNTHETIC
  snapshot whose Notify is the Go zero value FALSE → SESH_NOTIFY=0 → sesh-notify gates itself
  off and the test "ran ok" with NO toast — a vacuous pass. For a VISIBLE end-to-end proof:
  `thread notify --on` a real thread on that machine, `hooks test --thread <id>` (hs output
  names the thread = toast actually sent), then --off again. Did exactly that on macbook:
  `hs.notify: chanu-dashboards` + "hook notify-idle ran ok". (b) `hooks` commands are NOT
  --machine-routable — run them on the target machine over ssh.
- FOLLOW-UP (myrig 4debbf5): toasts should PERSIST until clicked away. TWO layers make a mac
  notification auto-hide: (1) our script's `withdrawAfter=15` — now 0 in BOTH hs.notify.new
  branches (0 = never expire; hs DEFAULTS to 5s when omitted, so explicit 0 is required;
  autoWithdraw=true kept = click dismisses); (2) macOS's per-app style — "Banners" auto-hide
  regardless, only "Alerts" sit on screen. On macOS 26 (macbook, 26.5.2) that setting has NO
  scriptable store: com.apple.ncprefs is GONE (domain doesn't exist — the classic flags-
  bitfield trick is dead), and it's not in the usernoted db2 (delivered-notifications only)
  or any Group Containers plist I could find ⇒ flipping Banners→Alerts is a MANUAL System
  Settings → Notifications → Hammerspoon step. Deploy: notify is a SYMLINKED script — git
  pull on macbook only (the sole machine with hs); no render/restart. Test toast fired
  through the new code.
CONTEXT (same session, earlier): `thread send --text` has a de-facto ~16.3 KB cap — NOT sesh's:
tmux's client→server imsg protocol caps one command at MAX_IMSGSIZE=16384, and SendText passes
the whole text as ONE argv to `set-buffer` (send-keys -l same cap). Fails atomically + loudly
("command too long"; at the exact boundary "failed to send command"). Lukas declined a fix
(would be: stream via `tmux load-buffer -b <buf> -` on stdin, which has no such cap) — big
content goes via a file path or @blob token (expands to a PATH, stays short) by design.

## H44 — MESH-SYNC DATA RATIONING (GitHub issue #1): ETag/304 + demand-driven cadence + peer-facing slim + DNS-check retry (2026-07-19, sesh PR #2 merge e31ec3c impl bb51843; api 39→40, NO store migration; deployed 3/5 — macbook + termux pending; tickets e9e48b31→4f0ab408 done, side ticket aeaca0d0 triage)
GitHub issue #1: meshsync fetched EVERY peer's FULL /v1/snapshot at 1 Hz forever — on termux
~128 KB/s ≈ 11 GB/day of mobile data (mymain's 203-thread snapshot alone 124 KB). Conferred
with Lukas (AskUserQuestion): ETag+demand-driven (over metered-detection — unnecessary after
these and it'd put Android policy in sesh), slim YES, default-on-everywhere YES. He then
interrupted once: "make sure to assess the usage-experience impact incl. going into/out of
idle" — the assessment FOUND THE ONE REAL REGRESSION and it became a design input:
- HOOKS PIN (the load-bearing UX finding): macbook/ideapad run [[hooks]] notify-idle, and the
  EVENTER observes REMOTE busy→idle THROUGH the peer-snapshot cache. At a 60s sample a remote
  turn shorter than the window produces NO busy edge ⇒ the toast would SILENTLY NEVER FIRE
  exactly when Lukas walked away. ⇒ a daemon with [[hooks]] configured NEVER idles
  (mesh_cadence "hooks-pinned"). Subscriptions/turn-delivery are UNAFFECTED by cadence
  (deliverSubscriptions is owner-side only; a local thread's edges come from the local
  maintainer, always 1s-fresh).
- DEMAND = GET /v1/mesh reads AND the all-machines fan-outs (fanOutThreads/fanOutGrid):
  sesh-ui's ThreadsScreen polls the LIVE fan-out (NOT the cache) yet leans on the cache's
  knownOfflinePeers gate — fan-out reads must bump demand or sesh-ui-open would let
  reachability go stale. TUI polls /v1/mesh (~3s) so it self-sustains active cadence.
- MECHANICS: [mesh] idle_interval (config.LoadMesh; default 60s, "0s"=never idle, broken =
  daemon refuses loudly). meshSync ZERO VALUE never idles (hand-built test daemons unchanged).
  Demand while idle KICKS an immediate round (buffered chan, 2s debounce) ⇒ first TUI frame
  after idle corrects in ~an RTT. ETag = sha256 over SORTED-by-id peer-facing threads JSON
  (map iteration is nondeterministic — sorting is load-bearing); syncer remembers per-peer
  ETags in memory, set ONLY after a successful upsert (etag↔stored-payload coherence); 304 →
  store.TouchPeerSnapshot (synced_at+reachable only; touched=false ⇒ row deleted under us
  [peer remove+re-add] ⇒ drop etag + refetch full SAME round). ssh transport unconditional.
  SLIM: handleSnapshot serves peerFacingThreads() = drop Archived && Head!=Headful
  (archived+HEADFUL stays — H40 contract); /v1/mesh self entry + eventer use the UNFILTERED
  maintainer, so only REMOTE archived-dead vanish from cached views (still via --machine +
  live fan-out, which lists archived as before). StatusResponse.MeshCadence
  (active/idle/hooks-pinned/always) + `daemon status` line. DNS self-check now RETRIES
  5s/15s/30s/60s/120s (runPeerDNSCheck, injectable), loud FAILED only after exhaustion, logs
  recovery — kills the boot false alarm racing tailscale MagicDNS.
- HARNESS FIX (separate pre-existing breakage, proven via a main-worktree run): the
  ssh-transport cells mesh.snapshot / mesh.offline-listing / daemon.mesh-read / route.parity
  (+ stage-file's client) were RED ON CLEAN MAIN since 4716d2d rooted the ssh ControlPath in
  SESH_HOME: a t.TempDir() home embeds the FULL TEST NAME, so <home>/ssh-cm/<40-char %C> +
  ssh's ".<16 rand>" master-creation suffix overran sun_path 108 for long-named cells. NEW
  shortSandboxHome(t) (mktemp /tmp/sesh-sb-XXX) replaces EVERY test SESH_HOME (harness + the
  ad-hoc routing clients in route/ticket/tmux tests). Cells green again and ~15x faster.
  LESSON RE-LEARNED: anything that puts a unix socket under SESH_HOME makes EVERY test home
  length-critical. ALSO BIT ME: used `git checkout <file>` to restore after an anti-gaming
  neuter — it wiped my UNCOMMITTED edits in that file (re-applied); reverse the edit itself,
  never git-checkout a file with uncommitted work.
- TESTS (anti-gaming: slim + 304 each neutered → suites RED → restored): unit
  TestSnapshotSlimAndConditional (REAL handleSnapshot ↔ REAL client.SnapshotConditional; incl.
  archived-dead churn NOT changing the ETag), TestMeshSyncConditionalFetch/304MissingRow (real
  store+handler), TestShouldSyncAndCadence, TestMeshSyncIdleAndKick (real run loop),
  TestDNSCheckRetry, TestTouchPeerSnapshot, TestLoadMesh. mesh.snapshot(+.http) cells EXTENDED:
  archive a headless peer thread → drops from the cached mesh view over BOTH real transports
  (presence-first, second live thread proves sync flows) + steady-state synced_at keeps
  advancing (on http that IS the 304/touch path). Blast radius green: mesh/*, route.parity,
  list-all, hold, stage-file, ticket.get/unbind, lifecycle, TUI claims mesh-render-offline /
  view-hold / view-active-archived-live; -race clean.
- LIVE-PROVEN on the real mesh (mymain): idle daemon = peers "synced 53s ago" → ONE /v1/mesh
  read → 1s-fresh in 2.5s + cadence idle→active; curl If-None-Match → 304 size=0; ideapad
  reports hooks-pinned, macstudio/mymain idle.
DEPLOY (api 40 = rebuild + daemon RESTART): ALL FIVE at schema 40. mymain (native,
supervisorctl) + macstudio (cij@) + ideapad (native) at e31ec3c; macbook came back on its own
already at 40/hooks-pinned (upgraded outside this session ~31min before checked — its sshd was
down but daemon reachable on :7878 the whole time); termux deployed later the same day once
android-main:8022 returned (git pull + PLAIN go build CGO=1/android per H22 + .new+mv + kill
by explicit pid + setsid relaunch → pid 5826, schema 40, cadence idle, rev ac8e157).
MEASURED (tailscale per-peer counters on mymain — /proc/net/dev + sysfs are PERMISSION-DENIED
on termux and it has no `ip`; `tailscale status` tx/rx per peer is the workable phone-traffic
source): mymain→phone 20.5 KB/s (~1.8 GB/day) with the OLD termux client against slim-serving
mymain → 2.4 KB/s (~0.2 GB/day) after the deploy — and the residual is almost all the ATTACHED
phone master COCKPIT (3 persistent ssh -tt links streaming remote tmux status bars ~1/s, the
H29 residual; mmt-kill drops it), NOT the mesh (phone cadence idle).
SIDE-FINDING (ticket aeaca0d0, triage): ideapad's TCP API has NEVER BOUND (~12 days) —
`api listen ideapad:7878 ... lookup ideapad: no such host` every 5s: Arch systemd-resolved
refuses SINGLE-LABEL lookups over the 127.0.0.53 stub and Go's resolver uses the stub (getent
resolves via NSS fine) — H22's class, bind-side. So mymain's mesh shows ideapad unreachable/
12d-stale and cross-machine views never list ideapad threads. Fix needs Lukas's call (myrig
FQDN coords vs IP bind vs resolver knob) — do NOT silently "fix" it per-machine.

### H44 follow-up — the archived-SLIM was REVERTED same day (sesh 0d12441; deployed ALL FIVE): optimizations must NEVER change what sesh shows
Lukas hit the slim live within hours: the PHONE's archived TUI view showed ONE row — 9c1cc4a5
= mymain's jf-seed-elevated-unlock, the sole archived-but-HEADFUL thread (H40 keep) — while
mymain's 183 / macbook's 67 / macstudio's 9 archived-dead threads had stopped replicating
(termux itself has ZERO local threads; the 2 stale ideapad archived rows were hidden by the
H35 offline-hide since ideapad reads unreachable — see ticket aeaca0d0). macbook "looked
fine" only because its 67 archived are LOCAL (self view unfiltered). His verdict (verbatim
class): "I don't want hacks like these. We shouldn't deteriorate the usage experience of
sesh because of this optimisation. Let me be extremely clear about that." ⇒ STANDING RULE
(also saved to persistent memory no-ux-tradeoffs-for-optimization): a design-Q&A sign-off on
an abstract trade is NOT consent to a degraded experience in practice — only
TRANSFER-INVISIBLE levers are acceptable; never hide rows. REVERT 0d12441: handleSnapshot
serves the FULL set again (peerFacingThreads → sortedSnapshotThreads — the by-id sort stays,
the ETag needs a deterministic payload); ETag/304 + demand-driven cadence UNTOUCHED (those
are the levers that took the phone 20.5 KB/s → ~300 B/s idle). The mesh.snapshot(+.http)
cells now carry a FULL-REPLICATION GUARD (archive a headless peer thread → row STAYS in the
cached mesh view with archived=true, both transports; unit
TestSnapshotFullAndConditional) — re-introducing the filter turns both RED (verified by
temporary neuter; reverse the edit, NEVER git-checkout a file with uncommitted work — that
wiped my edits once this session). Deployed ALL FIVE at 0d12441 (mymain/macstudio/ideapad/
macbook [sshd came back] supervisorctl; termux kill-by-pid + setsid relaunch → pid 17525);
LIVE-PROVEN from the phone's cache: mymain threads=212 archived=183, macbook 73/67,
macstudio 9/9 — the archived view is whole again. The UX-invisible replacement for the
payload-size problem (whole 124 KB re-sends whenever ANY row changes) = DELTA SYNC
(version-cursor, only changed rows transfer, archived rows cost one transfer ever) — ticket
953ac79d, triage, design to confer first. ALSO: phone traffic monitoring — /proc/net/dev +
sysfs are PERMISSION-DENIED in termux and it has no `ip`; measure from the OTHER END via
`tailscale status` per-peer tx/rx counters on mymain.

### H44 follow-up 2 — DELTA SYNC shipped (sesh PR #3 merge 2e6ff15 impl e308c77; api 40→41, NO store migration; deployed ALL FIVE; ticket 953ac79d done)
The UX-invisible replacement for the reverted slim, built same-day on Lukas's go-ahead.
MECHANICS: maintainer assigns a monotonic per-thread CHANGE GENERATION — publish() bumps it
only when the published snapshot actually changed (reflect.DeepEqual vs previous; archived/
idle rows keep their gen forever); deletions tombstoned in BOTH delete paths (tick cleanup +
zero-threads early-out; cap 4096 → clear + raise minReliableGen). Cursor = opaque
"<boot-epoch>:<gen>" (epoch = start unixnano base36 — cross-boot cursors can NEVER alias).
GET /v1/snapshot?since=<cursor> → {delta:true, threads:changed, removed:[ids],
generation:next}; empty delta ≈ 100 B = the steady state; ANY unservable cursor (other
epoch, pruned, garbage, future) → FULL payload — degrade toward full, never toward wrong.
snapshotWithGen() reads payload+gen under ONE lock (separate reads could mint a cursor
ahead of its payload and skip a concurrent change). ETag/304 kept for cursor-less (pre-41)
clients; ssh transport stays full-fetch. meshsync: per-peer cursor + IN-MEMORY working map
(id→row) coherent with the STORED blob (both updated only after successful upsert; removals
applied BEFORE upserts — a re-created id appears in both lists); empty delta → TouchPeer
Snapshot; missing row/base or failed write → clearCondState + syncPeerFull SAME round.
peer_snapshots blob format unchanged ⇒ no migration, no reader changes.
TESTS: TestSnapshotDelta / TestMeshSyncDeltaFetch / TestMeshSyncMissingRowRefetches (all
against the REAL handler + REAL client; test seeding now goes through pubThread/dropThread =
the real publish path, else generations never exist — direct st writes bypass them, bit me).
NEW matrix feature mesh.delta-sync.http (http-only BY DESIGN, own feature id like
mesh.snapshot.http — no N/A needed): two real daemons with a byte-COUNTING PROXY between
them (a measurement tap is honest — nothing mocked), 24 virtual-thread seed, asserts every
steady round < 1 KB and < full/10, a rename crosses < full/4, set stays COMPLETE with the
renamed row. ANTI-GAMING: neutering the since-branch → cell red with "transferred 8937
bytes (full=8937)". GOTCHA: after CLI-seeding threads, WAIT for the maintainer to publish
them (~300ms tick) before measuring the reference full snapshot — the first direct fetch saw
10/24 rows.
DEPLOY (schema 41 = rebuild + restart, all five: mymain/macbook/macstudio/ideapad
supervisorctl; termux kill-by-pid + setsid relaunch). LIVE-MEASURED (tailscale per-peer
counters on mymain): phone mesh ACTIVE at 1 Hz vs churning mymain = 3.6 KB/s total link
(~1 KB/s mesh share after the attached-cockpit ssh streams) vs 34 KB/s post-revert full
re-sends vs 124 KB/s original — full thread set incl. archived replicating everywhere.
Issue #1 got a follow-up comment; ticket 953ac79d done.

## H45 — ideapad was a MESH ISLAND for 12 days: NM clobbered tailscale DNS; resolved-stack fix + doctor bind-state check (2026-07-19/20, myrig e9ef43c + sesh 4063a93; deployed ALL FIVE; ticket aeaca0d0 done)
Ticket aeaca0d0 (found during H44 deploys): ideapad's daemon retried `api listen ideapad:7878
... lookup ideapad: no such host` every 5s. ROOT CAUSE (bigger than the bind): on 2026-07-07
NetworkManager overwrote /etc/resolv.conf with LAN-only DNS (router 192.168.1.1, search
mynet) — tailscaled's direct-mode file lost the fight (accept-dns WAS on; no systemd-resolved
on the box). Go's PURE resolver reads resolv.conf directly ⇒ EVERY tailnet name went dead for
Go: no API bind (inbound dead) AND meshsync couldn't dial mymain/macbook/macstudio (outbound
dead — ideapad's own mesh view was 12.3d stale). libc/NSS (ssh, curl, getent) kept working,
which MASKED it. H22's lesson generalized: Go-resolver vs system-resolver divergence, now on
the BIND side + NM-fight flavor.
FIX (live, then encoded): the RESOLVED STACK so nothing fights over resolv.conf — enable
systemd-resolved; /etc/NetworkManager/conf.d/dns.conf `[main] dns=systemd-resolved` (NM feeds
per-network DNS into resolved; apply with `nmcli general reload` — a FULL NM restart drops
the WiFi and kills your ssh session mid-sequence, bit me; run multi-step network surgery as a
DETACHED script via setsid nohup); /etc/resolv.conf → resolved STUB symlink — the stub's
`search tail27f06c.ts.net` line is what makes bare tailnet names resolve for Go; tailscaled
registers 100.100.100.100 + ts.net with resolved over D-Bus (it had ALREADY done so — only
the file was wrong; note tailscaled REWRITES resolv.conf in direct mode if it doesn't see the
stub there at its restart, so order matters: stub link LAST, no tailscaled restart needed).
PROVEN: daemon bound within 5s of DNS returning ("api listening on ideapad:7878", LISTEN on
100.116.77.31:7878); mymain's mesh flipped ideapad reachable; ideapad's peers 0-1s fresh;
`getent hosts ideapad` resolves on-box via search completion. myrig e9ef43c encodes it in
setup/installs/arch/tailscale.py (3 idempotent steps, re-run against the fixed box =
byte-identical). 
RECURRENCE-VISIBILITY (sesh 4063a93, no schema change, deployed all five + restarts): doctor
said "api: ok, exposed on <addr>" from CONFIG alone the whole 12 days. Daemon now tracks
apiBound/apiBindErr in serveAPIWithRetry; doctor reports ok/"listening on" vs
fail/"configured … but NOT BOUND — cross-machine http access to this daemon is down" + last
bind error. daemon.doctor cell extended (unresolvable SESH_API_ADDR sandbox → fail/NOT
BOUND; bound → ok/listening).

## H43 — mmt-copy-clipboard-to-master: push the BASE machine's clipboard into the master's clipboard (2026-07-12, myrig efc8cad; NO sesh change; deployed 4/5 — termux OFFLINE, pending; ticket 1d978651)
Ticket 1d978651 "mmt command similar to mmt-copy-to-master but instead transfers the current
clipboard content of the base to the master". MYRIG-ONLY (shell.sh.jinja): new zsh function
`mmt-copy-clipboard-to-master [--to <machine>]` right after mmt-copy-to-master — reads THIS
(base) machine's clipboard with the EXISTING helpers, image preferred over text exactly like
mmt-send-clipboard (_mmt_clip_get_image → clip_<ts>.png else _mmt_clip_get_text → .txt in a
mktemp -d cleaned in an always block; neither → loud "Clipboard is empty or has no supported
content." rc=1), then DELEGATES the tempfile to mmt-copy-to-master, which owns target
detection (`sesh master watchers` → 1=direct, several/none=fzf), machine validation, the
self short-circuit, and the ssh-target transport — zero duplication. Only flags: --to X /
--to=X passthrough (bare --to loud), -h/--help; any other arg loud, pointing at
mmt-copy-to-master for the file form. `my_alias -g mmt` next to its siblings; added to
MT_QUICK_CMDS (the WORK prefix+m popup runs ON the base = the right context to read the
base's clipboard) and deliberately NOT to MMT_QUICK_CMDS (the master popup runs on the
master host and would read the master's OWN clipboard — wrong direction). Docs: myrig
AGENTS.md + skills/mysetup-navigator/SKILL.md clipboard-relay lists.
KEY MECHANICS FACT: the extensionless remote tempfile is fine for images — sesh-set-clipboard
dispatches by `file --mime-type` (CONTENT-sniffed), so piped PNG bytes land as an image
(mac: osascript «class PNGf»; termux target refuses images loudly, Android clipboard is
text-only). mymain has a REAL X display for clipboard work: DISPLAY=:0 (Xorg on the dummy
config) — xclip works from any shell with it set.
LIVE-PROVEN (real network, clipboards SAVED + RESTORED around the test): text sentinel
mymain-clipboard → macstudio-clipboard byte-exact; a 1x1 PNG in mymain's clipboard PREFERRED
over text and read back from macstudio as «class PNGf» via `osascript -e 'clipboard info'`;
DISPLAY-less run fails loud rc=1; --to=/bare---to/unknown-arg/help all correct; zsh -n clean.
DEPLOY (render-only — shell.sh is rendered jinja via install-home; menus.sh/confs are
symlinks so pull suffices; NO daemon restart, NO conf re-source — no binding changed):
mymain (local python3 install-home), macbook + macstudio + ideapad (git pull + python3
install-home — all three had jinja2 in system python3, no uv needed). **termux OFFLINE
(android-main:8022 timed out) → PENDING, harmless; when back: cd ~/mysetup/myrig && git pull
&& python3 scripts/install-home.py "$MYRIG_TARGETS" (its python3 HAS jinja2 per H30 — never
pipe install-home to tail).** Also OBSERVED: macbook's PENDING H42 sesh-binary deploy is
ALREADY CLEARED (its ~/.local/bin/sesh reads vcs.revision=4716d2d = HEAD — a later session
deployed it); all five machines owe nothing on the sesh side from H42. Ticket 1d978651
marked done (closed by myrig efc8cad).

### H43 follow-up — DISPLAY inference + loud clipboard errors + xclip-over-ssh unhang (2026-07-17, myrig b05baaa; deployed ALL FIVE — termux back online, caught up on efc8cad too)
Lukas: "having to write DISPLAY=:0 seems a bit unergonomic" (his DISPLAY-less shell made the
new command report the misleading "Clipboard is empty..."). Three fixes in shell.sh.jinja:
(1) `_mmt_x_display` — $DISPLAY when set, else iff EXACTLY ONE /tmp/.X11-unix/XN socket
exists → ":N" (mymain: Xorg pinned :0 by xorg-dummy; login/ssh/popup shells never export
DISPLAY). Wired as `local -x DISPLAY="$(_mmt_x_display)"` into the linux branches of
_mmt_clip_get_image/_mmt_clip_get_text/sesh-set-clipboard (function-scoped, no env leak,
explicit DISPLAY wins). The prefix+m popup path on mymain works now too. (2)
`_mmt_clip_read_err` replaces the generic message in mmt-send-clipboard +
mmt-copy-clipboard-to-master: names the real cause (tool missing / DISPLAY unset+not
inferrable / DISPLAY's socket absent "is X running?") before falling back to genuinely-empty.
(3) THE TRAP the inference EXPOSED: `xclip -i` FORKS a child that keeps serving the X
selection and inherits stdio — over ssh (mmt-copy-to-master's inbound hop into a Linux
target) the held-open pipes stop the session from EVER closing (measured: hang killed at
2min; the transfer itself had SUCCEEDED — only session-close hung). Latent before: the
DISPLAY-less remote xclip used to fail fast, so the hang was unreachable. Fix: run xclip -i
with stdin/stdout detached + stderr to a TEMPFILE (a pipe/`$()` capture hangs identically —
the forked child holds fd 2); non-zero rc prints the tempfile loudly ("xclip failed (Error:
Can't open display: :9)") + propagates. The lingering xclip process after a set is NORMAL
(it's the selection owner; exits when the selection is taken). Other repo xclip sites
(copy-last, repo pickers, remote_desktop pbcopy aliases) are interactive-only — untouched.
LIVE-PROVEN: no-prefix round trip rc=0 on mymain; DISPLAY=:9 loud on both read+write sides;
macstudio→mymain mmt-copy-to-master (the previously-infinite hang) completes instantly with
the content landing; clipboards restored after. DEPLOY render-only ALL FIVE at b05baaa
(termux BACK online — also cleared its pending efc8cad; NB `whence` in the SAME login shell
that pulled shows "none" — functions were sourced at startup from the pre-pull shell.sh,
check with a FRESH shell). SECOND FOLLOW-UP (myrig 0018a88): mymain's pbcopy/pbpaste
(^remote_desktop^remote_desktop.sh, mymain-only symlink — deploy = pull on mymain, done)
converted alias→function with the SAME two fixes (_mmt_x_display inference + pbcopy stdio
detach for the ssh held-pipe trap); ideapad's Wayland twin (hyprland.sh wl-copy/wl-paste)
untouched. Proven: DISPLAY-less round trip, DISPLAY=:9 loud both sides, bounded
ssh-target-mymain pbcopy closes instantly.

## H42 — TUI selection ANCHORED to the thread, not the row index (2026-07-10, sesh 4e5c76d; NO schema change; deployed 4/5 — macbook OFFLINE, pending; ticket f262e0a8)
Ticket f262e0a8 "Ensure that the selected row does not change if the state of the view changes in
sesh tui": with a row selected, a NEW row appearing above/below shifted the selection onto a
DIFFERENT thread → the archive/delete-the-WRONG-row footgun. ROOT CAUSE: `m.cursor` is a POSITIONAL
index into `visibleMatches()`; the `meshMsg` poll handler (every ~3s + after actions) only CLAMPED
cursor to range — it never re-anchored to the selected THREAD. So a row sorting in above the cursor
slid a different thread under it silently. PURE TUI-CLIENT change — NO daemon/api/schema ⇒ deploy =
binary only, NO restart, mixed-mesh trivially safe.
- internal/tui/model.go: new `selectedID()` + `reanchorCursor(anchorID)`. The meshMsg handler
  captures `anchorID := m.selectedID()` BEFORE `m.rows = msg.rows`, then reanchorCursor moves the
  cursor back onto that id in the NEW row set (found → its new index; NOT found / "" → hold the
  positional slot, clamped into range = the neighbour). The preselect chain became an else-if so an
  EXPLICIT jump (--cursor / master-cursor / moved-node follow) still WINS over anchoring; a
  still-PENDING preselect (not yet published) also anchors to the current selection while it waits.
  Replaced the old two clamp sites (meshMsg + the actionMsg optimistic-patch block) with
  reanchorCursor — actionMsg anchors to `msg.id` (the acted-on thread): a non-hiding reorder (rename
  re-sorts by name) keeps the cursor on it; a HIDING change (archive/delete/hold leaves the view →
  id not found) falls back to the neighbour = the ticket's stated EXCEPTION (an action that removes
  the selected thread should not follow it).
- KEY MECHANICS FACT (load-bearing): `visibleMatches()` for the no-filter tree keeps SIBLING/ROOT
  order = `m.rows` order (only pins float roots to the top via a stable sort) — it does NOT re-sort
  by name. The name/(machine,name,id) sort happens in `flattenMeshRows` on the FETCH path, so
  msg.rows arrive PRE-SORTED. ⇒ a rename doesn't reorder IN-PLACE within actionMsg (applyPending
  only changes the name field); the reorder shows on the NEXT poll's sorted msg.rows, where the
  meshMsg anchor follows the renamed thread to its new slot. So the meshMsg change is the real fix;
  the actionMsg reanchor is a safe, more-explicit equivalent (identical behaviour for hide + no
  in-place reorder). Pin `p`/`u` go through pinDoneMsg (not actionMsg) and apply the patch at
  keypress with no cursor touch → unaffected; move-mode `m` tracks reorderID separately → unaffected.
- TESTS: internal/tui/anchor_test.go — row-appears-above keeps the thread; appears-below / removed-
  above keep it; the EXCEPTION (selected row leaves the view → neighbour); rename re-sorts across a
  poll → cursor FOLLOWS the renamed thread; preselect still wins; reanchorCursor unit truth table.
  Conformance TUI claim **selection-anchored** (registered AND in declaredTUIClaims — the H25 gotcha)
  drives a REAL daemon: cursor on `delta`, a new top-sorting `alpha` appears on the next real poll,
  selection MUST stay on delta (a positional cursor lands on beta). ANTI-GAMING: temporarily neutered
  the anchor → both the unit tests AND the live claim FAIL with the cursor on the wrong thread (proven
  discriminating), then restored. LIVE SMOKE (isolated tmux `sesh tui`, own daemon/home/short sockets
  /tmp/sk.XXX per the 108-char unix-sockaddr limit, SESH_* stripped — never touched the live mesh):
  cursor on delta, spawned `aaa-top` via CLI → it appeared at the top and the `>` stayed on delta;
  then archived delta → `>` fell to the neighbour beta (didn't chase the vanished row).
- skills/sesh-cli/SKILL.md: paragraph documenting the anchoring semantics + the action exception. NO
  new key/column/flag/env var (behaviour-only), so no keymap/help.go change. sesh-ui needs NO change —
  it's a GUI with its own selection model; this is terminal-TUI-only.
DEPLOY (binary-only, NO daemon restart — each `sesh tui` runs fresh from the binary): pushed
origin/main, rebuilt at 4e5c76d on mymain (native go), macstudio (/opt/homebrew/bin/go), ideapad
(native go), termux (PLAIN go build = CGO=1/arm64 per H22). **macbook OFFLINE (ssh :22 timed out) →
PENDING, harmless (binary-only + mixed-mesh safe); when back: cd ~/mysetup/sesh && git pull &&
/opt/homebrew/bin/go build -o ~/.local/bin/sesh.new ./cmd/sesh && mv -f (no restart).** Ticket
f262e0a8 marked done (closed by 4e5c76d).

## H41 — TUI MOUSE clicks: click=select, double-click=enter, click ▸/▾=fold (2026-07-09, sesh 646eb46; NO schema change; deployed ALL FIVE — macstudio BACK online; ticket 68f53afb)
Ticket 68f53afb "Mouse support in sesh tui": click a row to SELECT it, double-click to ENTER
it, click the ▸/▾ marker to collapse/expand. Builds on H9 (mouse WHEEL already enabled via
`tea.WithMouseCellMotion()`; H9 added no cell — "wheel is just another driver of existing
offsets"). This adds the LEFT-CLICK path. PURE TUI-CLIENT change — NO daemon/api/schema ⇒
deploy = binary only, NO restart, mixed-mesh trivially safe.
- internal/tui/model.go: new `tea.MouseButtonLeft` case in Update's MouseMsg switch, acting
  ONLY on `Action==Press` (release/motion ignored so a drag fires nothing — bubbletea v1.3.10
  SGR release keeps Button==Left but Action==Release, so the press-check is the correct gate).
  New Model fields lastClickID/lastClickAt (double-click tracking) + nowFn (test clock; nil=time.Now).
- internal/tui/mouse.go (NEW): handleLeftClick maps a press → select / double-click-enter /
  fold-toggle. Guards modal states (ticket/details/confirm/prompt/tag/uuid popups + reorder).
  rowAtY maps terminal Y → visible-row index by MIRRORING View's top chrome (rowsTop: title +
  lastErr/actionErr/note + column header + `▲ N more`) and the viewport window (viewportRange).
  clickOnFoldMarker finds the ▸/▾ glyph's terminal column from activeColumns + horizontalView
  (so it works with the CONFIGURED column order — NAME is NOT column 0 by default, it's 3rd:
  machine,agent,NAME,cwd,tags,notify — AND with horizontal panning), accepting the 2-cell "▾ "
  region. doubleClickWindow=500ms. Double-click enter reuses navSelected + the offline-owner gate
  (machineReachable) so entering an OFFLINE machine's thread refuses INSTANTLY (loud actionErr,
  no shell-out) instead of hanging on the routing timeout — same as the `enter` key. Fold-marker
  click resets the double-click timer so a 2nd fold-click never reads as an enter.
- KEY LAYOUT FACT: the row gutter is exactly gutterWidth=9 cols (`"> "`/`"  "` 2 + mark+head+busy
  +desc+att+arch+" " 7); renderCells (columns) begins at X=9. The fold marker is the penultimate
  rune of the tree prefix ("▾ "/"▸ " → glyph then space) at the START of the NAME cell. All gutter/
  tree glyphs are 1-cell so a rune index into an ANSI-stripped render line == its terminal column
  (load-bearing for both the click math and the live-smoke marker lookup).
- TESTS: internal/tui/mouse_test.go — select / outside-rows-ignored / double-click-enters(within
  window) vs re-selects(outside) / different-rows-never-enter / offline-refused / fold-toggle both
  directions / release+motion ignored / modal ignored, PLUS TestRowAtYMatchesRender: a RENDER-DRIFT
  GUARD (the H40 gutter-misalign bug class) that finds each rendered row's REAL screen Y (clean /
  actionErr / scrolled-viewport cases) and round-trips rowAtY — so any change to View's top chrome
  not mirrored in rowsTop fails loudly. Conformance TUI claim **mouse-click** (registered AND in
  declaredTUIClaims — the H25 gotcha) drives a REAL daemon + a real parent/child tree (P-reparent,
  poll until nested like claimActionReparent): a left click selects the pointed row + a marker click
  collapses/expands the subtree, asserted on the LIVE render (double-click ENTER is left to the unit
  test + the existing action-nav claim — invoking nav in the claim would revive a real thread).
- LIVE SMOKE (the H9 gold standard): injected REAL SGR mouse bytes (`ESC[<0;C;RM`/`m` via `tmux
  send-keys -l $'\033[<0;C;RM'`) into `sesh tui` in a FULLY ISOLATED tmux (own daemon/home/short
  socket path [unix-sockaddr 108-char limit — scratchpad path was too long, used mktemp /tmp/sk.XXX],
  sockets smoke-work/smoke-ui, SESH_* stripped — never touched the live mesh): single click moved the
  `>` cursor to the clicked row; click on ▸ expanded (child `└` appeared) + again collapsed it;
  double-click on a headless thread promoted it `◌·`→`●·` (it entered/revived). NB display-popups
  can't be driven by send-keys but a plain pane CAN — the smoke ran the TUI in a plain session.
- help.go tui long + sesh-cli SKILL (keymap `mouse click` line + a click/double-click/fold prose
  paragraph) updated. sesh-ui needs NO change — it's a GUI with native mouse; this is terminal-TUI-only.
DEPLOY (binary-only, NO daemon restart — each `sesh tui` runs fresh from the binary): pushed
origin/main, then rebuilt on ALL FIVE at 646eb46 — mymain (native go1.25), macbook + macstudio
(/opt/homebrew/bin/go), ideapad (native), termux (PLAIN go build = CGO_ENABLED=1/arm64 per H22).
**macstudio is BACK online** (was ssh-unreachable through much of H36–H40) — so ALL FIVE are current
on 646eb46. VERIFIED macstudio's daemon is already api schema 39 / store 21 (same as mymain), i.e. it
had ALSO caught up to the H38 daemon at some point — NOT stale, no restart owed. So this binary-only
mouse deploy needed no daemon action anywhere. Ticket 68f53afb marked done (closed by 646eb46).

## H40 — DEFAULT VIEW keeps ARCHIVED-but-HEADFUL threads + `⊘` archived gutter glyph (2026-07-06, sesh e8eaae6; NO schema change; deployed 4/5 — macstudio OFFLINE, pending; ticket f23b8ea9)
Ticket f23b8ea9 "default view = all non-archived + archived-but-live, not on hold" = `(non-archived OR
(archived AND live)) AND not on hold`. Lukas CLARIFIED (AskUserQuestion) that "live"/"attached" means
**HEADFUL** (a live pane, `Head==Headful`), not tmux-attached — and wanted a **symbol** for archived
(chose a gutter glyph). PURE TUI-CLIENT change: the maintainer already publishes archived threads in its
snapshot (`ListThreads(true)`); the TUI merely filtered them client-side in `builtinViewAdmits`. NO
daemon/api/schema change ⇒ deploy = binary only, no restart, mixed-mesh trivially safe.
- internal/tui/model.go `builtinViewAdmits` ViewActive: `!Archived && !OnHold` → `(!Archived ||
  Head==Headful) && !OnHold`. So an archived thread stays shown while its agent runs and drops out once
  it goes headless. The optimistic-archive hide (`archiveRow`→`leavesViewWith`→`builtinViewAdmits`) does
  the right thing FOR FREE: archiving a HEADFUL thread no longer hides it (stays); a HEADLESS one still
  hides instantly. Other views unchanged (`on hold`/`archived`/`all`).
- ARCHIVED GLYPH: new `ArchivedGlyph` → `⊘` in the state gutter for any archived row (dedicated cell after
  the attachment `*`), complementing the existing opt-in ARCHIVED date column.
- GUTTER FIX (found along the way): H38's manual-order `mark` cell had widened the per-row gutter WITHOUT
  updating `gutterWidth` (7) or the `"  HBD  "` header → a latent 1-col header/row MISALIGNMENT + a 1-col
  horizontal-pan budget error. Now `gutterWidth=9` (2 prefix + mark+head+busy+desc+att+archived+sep) + a
  named `gutterHeader="   HBD   "` (HBD sits exactly over head/busy/desc), guarded by TestGutterHeaderWidth.
- TESTS: internal/tui/view_active_test.go (predicate truth table: archived+headful shown / archived+headless
  hidden / on-hold excluded regardless of head; ArchivedGlyph; rendered glyph+HBD; gutter-width drift guard);
  pending_hide_test `TestLeavesCurrentView` now parametrised by head (archiving a HEADFUL thread in active =
  STAYS, headless = leaves); conformance claim **view-active-archived-live** (registered AND declared — the
  H25 gotcha) drives a REAL headed pi (headful) + a real headless record, both archived on the daemon, and
  asserts the default view renders the archived-headful row WITH `⊘` and HIDES the archived-headless one.
  KEY: wait on the `⊘` (not just the row) — a headful thread renders in the active view even BEFORE the
  archive propagates to the maintainer snapshot, so requiring the glyph proves "archived AND headful" together
  (this bit me — first pass settled on row presence and read the pre-archive render). Live visual smoke
  (ANSI-stripped `View()`): HBD aligns over `●·`, the archived-live row shows `●· *⊘`, columns line up.
- help.go tui long + sesh-cli SKILL (glyph list, tab-view description, archived paragraph) updated.
DEPLOY (binary-only, NO daemon restart — each `sesh tui` runs fresh from the binary): mymain (native build
.new+mv), macbook (lukas@, git pull + /opt/homebrew/bin/go), ideapad (lukastk@ ssh-target, native go),
termux (lukas@android-main:8022, git pull + PLAIN go build = CGO=1/android per H22, .new+mv) — all four on
e8eaae6. **macstudio (cij@macstudio) OFFLINE (ssh :22 timed out — down since before H37; the `| tail` in my
deploy line masked the ssh failure as exit 0, the H30 pipe-exit lesson) → PENDING, harmless (binary-only +
mixed-mesh safe); when back: git pull + /opt/homebrew/bin/go build .new+mv (no restart).** This deploy ALSO
carried **H39** (bbe0764, binary-only) to those 4 machines — its `[DEPLOY PENDING]` is cleared for them.
Ticket f23b8ea9 marked done (closed by e8eaae6).

## H39 — TUI column MAX-WIDTH cap (default on, `w` toggles, config) + `I` thread-DETAILS popup (2026-07-06, sesh bbe0764; NO schema change; ticket 6c99ee39)
Ticket "Max column length": cap each TUI column by default so a long name/cwd can't blow out the grid; a key to toggle the cap off (read clipped text in full); a key to popup ALL of a thread's fields. Pure TUI-client + config — NO daemon/schema change (deploy = binary only, no restart, mixed-mesh trivially safe). (Numbered H39 — H38 was taken by the concurrent manual-ordering feature below, which landed on main first; this landed via a merge commit.)
- WIDTH CAP (internal/tui/columns.go): colSpec.maxW for full-width cols (NAME/CWD/TKT-NAME default 40/40/30; fixed cols cap at their fixedW). Model.maxColWidth (default true via New(); a struct-literal Model defaults FALSE — unit tests set it explicitly) + colMax overrides. colWidths: cap ON → fixed cols reserve fixedW (UNCHANGED from before), full-width cols size-to-content-then-clamp-to-max; cap OFF → EVERY col sizes to content (the "see full text" behavior, incl. fixed cols). `w` key toggles + re-clamps hOffset. Config: `[tui] max_column_widths` (*bool, unset=on) + `[[tui.column_width]]` name/max (ResolveColumnWidths: loud on unknown col / max<1).
- DETAILS `I` (internal/tui/model.go): detailsPopup full-screen takeover (like ticketView — early-return in View(), handleDetailsKey) listing every ThreadRow field aligned (id/agent/model/state axes/session/cwd/parent/tags/created/hold/tickets/agent-session/meta); esc/q/enter close. Read-only, NOT in requiresReachableOwner.
- Tests: unit (cap/toggle/override/details/config parse) + 2 conformance claims column-max-width + thread-details (real daemon render; note the default cap applies even with width unset since colWidths caps independent of horizontalView). FIXED a pre-existing-RED claim found along the way: scroll-horizontal drove plain l/h to pan, but H25 rebound pan to ^l/^h — updated to ctrl keys (+ the stale scroll.go "h/l pan" comment). Also shortened claimColumnsConfig's 41-char longName (now truncated by the 40 cap) + gave cwd-label-column's raw-path assertion WithMaxColumnWidths(false). Full TUI-claims suite GREEN, -race clean. Live-smoked isolated tmux: 51-char name truncates at 40 with …, `w` shows full + tightens cols, `I` aligned details, esc back. NB the `w`/`I` keys do NOT collide with H38's p/u/m/D.
- help.go tui keymap (^h/^l fix + I/w + cap prose) + sesh-cli SKILL keymap/columns-config updated. DEPLOY: binary-only, no restart. Deployed 4/5 (mymain/macbook/ideapad/termux) — rode along with the H40 e8eaae6 binary deploy (bbe0764 is an ancestor); macstudio OFFLINE, pending with H40.

## H38 — MANUAL THREAD ORDERING: pin top-level threads above the auto block + dividers (2026-07-06, sesh 9897aa0 api 38→39 store 20→21; deployed 4/5 — macstudio OFFLINE ~3.7d, pending; ticket aefa7f64)
Ticket aefa7f64 "Manually ordering threads". Lukas's ask (paraphrased): select a thread → enter an
ordering mode → up/down to position it; manually-ordered threads form a block above/below the auto-sorted
ones; spawnable DIVIDERS (horizontal lines); a command to remove ordering; CLI too; ordering lost on
archive. He asked FIRST "is this a good idea / better version?" — so I conferred before coding.
CONFERRED (Lukas locked 5 decisions): (1) DROP the two-zone model + the teleport-past-block — ONE pinned
zone at the TOP only; (2) ordering applies to PARENTLESS (top-level) threads only; (3) dividers = option
(b): `agent_kind="divider"` NODES (the H37 non-agent-node pattern), NOT free-floating lines and NOT
leaning on virtual-parent groups; (4) SYNC everywhere (owner-side thread metadata, like everything else —
NOT cockpit-local); (5) NO bottom zone (hold already parks threads). Also kept: `pin` semantics; pin-to-top
gesture; a `•` glyph marker (no config). KEY UX critique that drove the redesign: the original spec's
"teleport a row across the whole 80-thread block on one arrow-press" is disorienting and exists only to
keep the auto block contiguous; and it was written flat but the TUI is a TREE + already has virtual-parent
groups (H37) — so manual order must reorder ROOTS (each carrying its subtree), not arbitrary flat rows.
DESIGN: pinned top-level threads render as a block ABOVE the auto-sorted roots, ordered by a FRACTIONAL
float `pin_order` computed CLIENT-SIDE from the merged cross-machine view; the daemon is a PURE SETTER
(like hold). Fractional keys ⇒ every pin/reorder is a SINGLE write to ONE owner, never renumbering siblings
(which may be on OFFLINE machines — the load-bearing reason vs integer positions). Dividers are inert
childless always-pinned nodes rendered as a full-width rule.
IMPLEMENTATION:
- DATA (api 38→39, store 20→21, additive/mixed-mesh-safe): api.Thread.PinOrder *float64 (nil=unpinned;
  rides ThreadRow+ThreadSnapshot), api.DividerAgentKind="divider" + api.NonAgentKind(kind),
  NewThreadRequest.Divider+PinOrder, PinThreadRequest{ID,PinOrder}. Store migration 20 (VERSION 21):
  `ALTER TABLE threads ADD COLUMN pin_order REAL` — NULLABLE (NULL=unpinned so 0/negative are valid keys,
  distinct from unpinned; scan via sql.NullFloat64→*float64). SetThreadPin; SetThreadArchived clears pin
  on the archive transition; SetThreadParent clears pin on reparent-to-NON-root (both in-SQL, atomic).
- DAEMON: POST /v1/threads/pin (pure setter; refuses pinning a CHILD [409 "top-level"] + un-pinning a
  DIVIDER [409 "delete it"]). handleThreadNew divider branch (mirrors --virtual). GENERALIZED virtualGate
  → nonAgentGate covering virtual AND divider (send/capture/fork/revive/send-headless/transcript, tailored
  message per kind); ~6 call sites renamed. Refuse archiving a divider. agents.ParseKind still rejects
  "divider" so any unguarded agent path fails closed. client.ThreadPin.
- CLI: `thread pin --id [--top|--bottom|--before|--after|--order]` (client-side fractional math via
  `thread grid --all-machines`: blockEnd/neighborMidpoint/findPinnedNode in cmd/sesh/pin.go), `thread
  unpin`, `thread new --divider [--name]` (placed at top; refuses agent-shaped flags LOUDLY — the CLI
  branch guards them, not just the daemon). help.go/help_flags.go/help_test.go (pin/unpin added to
  subcommandSets; --divider flagDoc).
- TUI (internal/tui): filter.go visibleMatches sort.SliceStable partitions ROOTS pinned-first (by
  pin_order, then machine, id) as a block above the auto roots (each pinned root keeps its subtree; the
  filter-mode score list is untouched). Keys: p=pin-to-top, u=unpin, m=MOVE MODE (new reordering/reorderID
  state; ↑/↓ reposition via reorderTarget midpoint, enter/esc exit, auto-pins an unpinned top-level row
  first), D=new-divider prompt (empty label = bare rule, Esc cancels — unlike v's empty=cancel). rowPatch
  gained pinSet/pinOrder overlay (optimism re-sorts instantly); pinDoneMsg drops the optimism + refetches
  on a FAILED write. All four keys → requiresReachableOwner + BOTH offline_test lists (H35 gate). `•`
  marks pinned rows, `↕` the row being moved; renderDividerLine draws the rule; HeadGlyph divider fallback
  "─"; Enter on a divider refuses loudly; move-mode legend. move mode dispatched BEFORE the offline gate
  (entry key `m` is already gated).
- TESTS: store TestThreadPin (set/clear/archive-clears/reparent-clears, incl. a 0-key reading back pinned).
  TUI pin_test.go (root ordering, reorderTarget bounds+leapfrog, pinTopOrder, p/u/m/D handlers,
  child-refusal, divider render, pinMark, samePinOrder). Conformance features thread.pin + thread.divider
  (AgentAgnostic × local+remote, real ssh routing; pin sets/repositions/before/after, child-pin refused,
  archive+reparent clear it; divider record+no-session+nonAgentGate refusals+archive/unpin refused+agent-
  flag refusals+delete) + TUI claims action-pin/action-reorder/action-new-divider (+declaredTUIClaims, the
  H25 gotcha). FIXED the store migration test (TestMigrationClearsDanglingParents): it rolled version back
  to len(migrations)-1 assuming the dangling-parent sweep was the LAST (idempotent) migration — my ADD
  COLUMN broke that (duplicate column on re-run); now it locates + re-execs the sweep SQL directly.
- SKILL: skills/sesh-cli/SKILL.md keymap (p/u/m/D) + "Manual ordering" paragraph + CLI verbs.
GOTCHAS (bit me): (a) termux /tmp is UNWRITABLE — my relaunch redirect `>/tmp/seshd.log` failed AFTER I'd
killed the old daemon, briefly leaving termux daemon-less; relaunch with `>/dev/null`. (b) /proc/<pid>/
environ WAS readable on termux this time (H21 said unreadable — situational); read the daemon's exact env
(SESH_HOME=~/.sesh SESH_MACHINE=termux SESH_TMUX_SOCKET=sesh SESH_MASTER_SOCKET=sesh-master +
SESH_API_TOKEN ambient) from it. termux is an OUTBOUND leaf (not in mymain's peer set) — verify its schema
on the box itself, not via mymain's mesh.
DEPLOY (api 39 = daemon REBUILD + RESTART): mymain (native build .new+mv + supervisorctl restart;
LIVE-SMOKED: pin order 0, divider order -1 above it, unpin-divider + archive-divider both loud 409, archive
clears pin_order→None, scratch threads deleted clean), macbook (lukas@, git pull + /opt/homebrew/bin/go +
supervisorctl restart), ideapad (lukastk@ via ssh-target, native go + supervisorctl restart), termux
(lukas@android-main:8022, git pull + PLAIN go build = CGO=1/android per H22, .new+mv, kill daemon by
EXPLICIT pid 7861 [NOT pkill -f], setsid-nohup relaunch → pid 24302). All four api schema 39, mesh synced
0-1s. **macstudio (cij@macstudio) OFFLINE ~3.7d (ssh :22 timed out — down since before H37) → PENDING; it
now owes H35+H36+H37+H38, additive so harmless. When back: ssh-target macstudio → cd ~/mysetup/sesh &&
git pull && /opt/homebrew/bin/go build -o ~/.local/bin/sesh.new ./cmd/sesh && mv -f && supervisorctl
restart sesh-daemon.** Ticket aefa7f64 marked done (closed by 9897aa0).
FOLLOW-UP: sesh-ui manual-ordering support = ticket ed5c0e0c (triage; render pinned-above-auto + dividers,
refuse divider chat surfaces, emit pin/unpin/reorder/new-divider verbs — the fractional math is client-side,
reference cmd/sesh/pin.go + internal/tui/model.go). Analogous to H37's sesh-ui ticket 7d09e0f5.

## H37 — VIRTUAL parent threads + realize; delete promotes children; codex lost-since-wipe restored (2026-07-05, sesh 7e806ee api 37→38 store 20, myrig 1d44d2f; deployed 3/5 — termux + macstudio unreachable, pending)
Ticket 181d3ca6 "Virtual parents" (explore) → ticket 149339a1 (implement). Lukas's ask: parent threads
under something that isn't a thread; later convert the parent into a real thread. DESIGN (explored first,
Lukas locked 4 decisions): a virtual parent = a THREAD RECORD with agent_kind="virtual" — NOT a new
entity, NO store migration for the record (agent_kind is a TEXT column). All grouping machinery (tree,
reparent, H26 hold inheritance, tags, archive, mesh sync) applies unchanged; maintainer resolves it
headless·idle for free (no pane). Decisions: (1) kind string "virtual" (not "none"/empty — empty must
stay "bug"); (2) TUI Enter = LOUD WARNING, not fold-toggle; (3) cross-machine parenting OUT of scope
(parent existence is validated against the OWNER's local store — virtual groups are per-machine like
hold inheritance); (4) dangling parent ids must be fixed for ALL deletes.
IMPLEMENTATION:
- api 37→38 (additive/mixed-mesh safe): api.VirtualAgentKind, NewThreadRequest.Virtual,
  RealizeThreadRequest + POST /v1/threads/realize. Only the OWNER needs 38 (virtual threads can only be
  created there); pre-38 viewers render the kind string, their routed verbs hit the owner's loud refusal.
- KEY INSIGHT that made realize trivial: a converted virtual thread == the EXISTING never-started
  headless state (newHeadlessThread). handleThreadRealize sets kind, pre-mints AgentSessionID (pi/claude;
  codex mints on first turn), renames session to headless-<id> (so headful revival mints via
  [[session_name]]), requires a cwd by then (--cwd else the one stored at creation; virtual cwd is
  OPTIONAL at create). store.RealizeThread guards `WHERE agent_kind='virtual'` → concurrent realizes
  can't double-convert. Id/children/tags/holds/ticket bindings all survive (in-place).
- FAIL-CLOSED gates: virtualGate (409 naming the realize command) on send, capture, send-headless,
  revive (=resume+headful), fork source, transcript. agents.ParseKind never accepts "virtual", so any
  UNGUARDED agent path still fails loudly. CLI `thread new --virtual` refuses --agent; daemon refuses
  every other agent-shaped field. TUI: HeadGlyph ◇ for virtual; Enter + f warn via actionMsg (instant,
  nothing shells out); fork gated client-side too (daemon's would be the opaque "unknown agent" parse).
- DELETE PROMOTION (all threads, not just virtual): store.DeleteThread promotes children to the deleted
  thread's parent in the same tx; store migration 19 (VERSION 20 — comment numbering is offset from
  element index, the "4" comment spans 2 elements; the migration test rolls back meta.version to
  len(migrations)-1 rather than a literal) clears HISTORICAL danglers to root. Live store verified: 81
  threads, zero dangling parents post-restart.
- conformance: features thread.virtual (agnostic × both loc: create/no-session/refusals/grouping + hold
  inheritance THROUGH a virtual parent + delete-promotes) + thread.realize (3 agents × both loc: REAL
  first turn + continuity locally; stored-cwd default + one routed real turn remotely); thread.delete
  cells extended w/ promotion; TUI claim action-virtual-enter (added to declaredTUIClaims — the H25
  register-only-never-runs gotcha). All 16 blast-radius cells + claims green.
- REPAIRED STALE CLAIM: mesh-render-offline still asserted the PRE-H35 "offline threads stay listed"
  contract → failing on clean HEAD since H35 made hide-offline the default (verified via a HEAD
  worktree). Now asserts OFFLINE + "hidden" footer + `o` reveals last-known rows. Green.
CODEX SIDE-QUEST (Lukas: "codex should be installed; check myrig for what went wrong"): my codex realize
cells failed "command not found: codex" — and so did the long-green thread.send.headless/codex cell.
ROOT CAUSE: the 2026-06-29 `rm -rf ~` wipe on mymain. Recovery re-provisioned node via mise (14:27 that
day) but myrig has NO step installing the agent npm globals — pi was manually reinstalled 2026-07-03,
codex NEVER was, and ~/.codex/auth.json was also lost (only the myrig hooks.json symlink came back).
FIX: `mise x node@lts -- npm install -g @openai/codex@latest` + `mise reshim` on mymain AND ideapad
(also missing there); auth.json scp'd from macbook (0600). myrig 1d44d2f adds an idempotent post step
(scripts/post/all.sh): ensure @earendil-works/pi-coding-agent + @openai/codex under mise npm (skips
termux; claude has its own installer; auth is a credential — copy manually after a wipe). codex cells
then green.
LIVE SMOKE GOTCHAS (bit me, worth remembering):
- `thread new` PARENT INFERENCE strikes again: creating the smoke virtual group from inside this sesh
  thread silently childed it to MY session thread → the TUI filter (children:off by default) showed
  0 matches and I chased a phantom bug. Pass --no-parent for standalone smoke rows.
- tmux send-keys with a whole string ("/query") arrives as ONE bubbletea KeyRunes burst → normal-mode
  switch matches nothing → the `/` never opened the filter, and my Esc QUIT the TUI. Type the `/` and
  the query as separate send-keys (with small sleeps).
- In filter mode the SELECTED row is the top MATCH — I pressed Enter while the cursor sat on the pi
  CHILD row and revived it into a real pane on the LIVE work server (nav success quit the TUI = the
  "exited 0" mystery). Stopped it immediately; no user client was disturbed (verified list-clients
  before/after). ALWAYS capture and confirm 1/N matches + the `>` cursor before driving Enter.
LIVE-VERIFIED on mymain (real daemon): create virtual (kind/no-cwd/virtual-<id> session), all refusals
loud + actionable, TUI ◇ + AGENT=virtual + Enter ✗ warning persists w/o quitting, realize→pi + real
headless turn ("VIRTUAL-REALIZED-OK"), delete → child promoted to the group's parent, migration clean.
DEPLOY (schema 38 = binary + daemon RESTART): mymain / macbook / ideapad deployed (api 38, mesh synced);
additive schema so any lagging machine is harmless.
FOLLOW-UPS: sesh-ui virtual-thread support = ticket 7d09e0f5 (triage; render ◇/virtual, refuse chat
surfaces, realize affordance). Cross-machine parenting = future feature (TUI tree already joins by id
across the merged mesh set; the blockers are owner-side parent validation + H35 offline-hiding
promoting children to root + H26 same-machine hold walk). Tickets 181d3ca6 + 149339a1 marked done.

### H37 follow-up — TUI `v` key creates a virtual group (2026-07-05, sesh a04b6bc; binary-only; ticket 0b09f7aa)
Lukas chose "option 1": `v` in normal mode opens the line prompt for a NAME → exec `thread new
--virtual --name X --no-parent --json` → actionMsg{preselect} lands the cursor on the new root group;
you then `P` children under it. DECISIONS baked in: (a) created on the SELECTED row's MACHINE (a
virtual parent only groups same-machine threads — the prompt header shows the target machine, e.g.
`new virtual group (empty=cancel) "macbook">`; no selection = local); (b) EMPTY submit CANCELS (no
accidental nameless groups — unlike the CLI, which allows nameless); (c) `--no-parent` is LOAD-BEARING:
the TUI's subprocess inherits its launcher's env, so parent inference would silently child the group to
whatever sesh thread the TUI runs inside (this exact footgun bit the H37 smoke); (d) `v` added to
requiresReachableOwner + BOTH offline_test key lists (the H35 gate refuses instantly on an offline
owner's row). New TUI claim action-new-virtual (registered AND declared, the H25 gotcha) + units
(prompt opens/names target machine, empty-cancel no-cmd, no-selection→local, offline refusal). Legend
`v group`, help.go tui keymap, SKILL keymap + virtual section. Binary-only (no daemon/schema change) —
deployed a04b6bc (no restart). LIVE-PROVEN on the real mesh: `v` from mymain's TUI
with the cursor on a macbook row created the group ON macbook + preselected the ◇ row; routed delete
cleaned it up. Ticket 0b09f7aa marked done.

## H36 — TUI property-set lag + archive disappear→REAPPEAR→disappear flicker: meshsync stall + fetch-count patch TTL (2026-07-04, sesh 021b316; NO schema change; deployed 4/5 — macstudio OFFLINE, pending)
Ticket 0b3d2774 "Large lag when setting properties in sesh tui": a/h/stop laggy; archiving hid the row,
then it RESURFACED ~a second later, then vanished for good. Lukas asked diagnose→plan→confer; plan agreed
as items 1+2+4 (item 3 = optimism-at-keypress DEFERRED: needs action-scoped patches for revert-on-failure,
reassess after living with the rest; item 5 = post-write sync nudge dropped as invisible once patches hold).
THREE DEFECTS, one measured live:
1. MESHSYNC STALL (the amplifier). meshsync.tick() fetched all peers concurrently but wrote ALL results
   only after wg.Wait(), and run() calls tick() serially — so ONE asleep peer (blackholed TCP dial hanging
   the full 8s meshFetchTimeout; macstudio was down ~41h) gated EVERY peer's cache write. MEASURED on
   mymain: peer synced_at saw-toothed 1s→9s (≈8s period = the timeout) instead of ≤2s. So remote-thread
   truth lagged up to ~12s (owner republish ≤0.3s + pull ≤9s + TUI poll ≤3s). FIX: tick() launches a
   goroutine per peer UNLESS one is in flight (inflight map); each fetch writes its own result on
   completion (store writes serialize via SetMaxOpenConns(1)); meshSync has ctx/cancel so stopAndWait
   aborts hangs promptly, and a shutdown-canceled fetch does NOT MarkPeerUnreachable (cancellation ≠ peer
   health); fetchTimeout injectable. Tests (real store + real httptest peer + a listener that ACCEPTS AND
   NEVER RESPONDS, broken ssh dests so only http can serve): healthy peer lands per-tick mid-hang; dead
   peer flips unreachable after timeout with last-known payload retained; prompt shutdown. LIVE-PROVEN
   post-deploy: staleness steady 1-2s with macstudio still dead.
2. FETCH-COUNT PATCH TTL (the flicker). rowPatch.ttl = 4 reconcile fetches, but EVERY fetch decrements
   EVERY pending patch and each action fires 2 fetches — park a few threads back-to-back and the first
   patch died in ~1-2s while the cache was still stale ⇒ archived row RESURFACED until the next
   post-catch-up poll. FIX: wall-clock deadline (optimisticPatchTTL=15s, stamped at CONFIRMATION in
   stampPatch ← routedVerb/renameRow/tagRow, carrying machine+desc). applyPending GC: satisfied-by-field
   on a present row; row ABSENT counts as landed ONLY if its owning machine reports reachable in
   m.machines (absence with machine offline/missing proves nothing — closes the reachability-flap
   resurface too); deadline expiry drops the patch AND sets actionErr LOUDLY ("confirmed by X but still
   not reflected... sync may be degraded"). rowPatch gained archived/head/busy overrides; archiveRow
   patches the archived FIELD (not just hide) so ViewAll reconciles by field instead of wrongly hiding.
   satisfied(): PURE hide (delete) never satisfied by presence.
3. NO OPTIMISM on stop/hold-clear. stopSelected now patches {head:headless, busy:idle} (● flips ◌
   instantly, reconciles when the owner's maintainer publishes the pane death). holdRow --clear uses
   holdClearPatch: optimistic {onHold:false,+hide} ONLY when OnHoldEffectiveUnix <= OnHoldUntilUnix — a
   DOMINATING INHERITED hold stays non-optimistic (only the owner derives the H26 max).
Lukas's cache-optimism question answered during diagnosis: his eventual-consistency reasoning was RIGHT,
but daemon-cache-level optimism would be clobbered by the next stale pull (same flicker one layer down);
the TUI rowPatch layer was the right place and just had the two bugs above.
TESTS: meshsync_test.go (2, above); pending_hide_test.go rewritten for deadlines + NEW absence-keeps-
patch-when-machine-not-reporting, survives-20-stale-fetches (the regression), loud-expiry (names
desc+machine), archived-field-satisfies-in-ViewAll, head/busy overrides, inherited-hold-not-optimistic;
columns_test deadline expiry. GATE: build+vet; daemon/store/tui -race; conformance mesh.snapshot(.http),
mesh.offline-listing(.http), thread.hold local+remote, TUI claims action-{archive,hold,stop,delete},
view-hold — all green. LIVE SMOKE (isolated tmux ttest-flicker, real sesh tui --all-machines --cursor,
scratch headless pi thread on ideapad): archive gone <1s, ZERO reappearance over 13 frames/13s, no ✗,
record archived=True on ideapad; scratch deleted after.
NB (bit me): `thread new --json` prints a FLAT object (id at top level, no .thread); `thread list --json`
is JSONL (one object per line, not an array).
NEW MACHINE: **ideapad** (5th mesh member, http peer ideapad:7878, lukastk@, /home/lukastk/.sesh, native
`go build`, supervisorctl sesh-daemon — same recipe as mymain; reached via ssh-target ideapad).
DEPLOY (NO schema change; daemon RESTART for item 1, TUI binary-only): deployed all machines at
021b316 (termux gotcha noted: the zshenv login-guard relaunches the daemon itself within the sleep
window with the full env — a manual setsid relaunch loses the race, harmless). Ticket 0b3d2774 done.
DEFERRED (Lukas may ask later): item 3 optimism-at-keypress (~200ms→0ms; needs per-action patch identity
so a failed action reverts without disturbing merged sibling patches); item 5 post-write per-peer sync
nudge (sub-second server truth — invisible while patches hold).

## H35 — DISCONNECTED threads: hide OFFLINE machines' threads by default + refuse owner-routed TUI actions instantly (no freeze) (2026-07-02, sesh 074d191; NO schema change; deployed 3/4 — macstudio OFFLINE, pending)
Ticket 23b0ecf2 "Disconnected threads": macstudio was offline but its threads still showed in `sesh tui`; entering one FROZE the TUI for seconds, and archive/hold "didn't work". Lukas: diagnose + confer first.
DIAGNOSIS (measured, not guessed): macstudio is an **http peer** (macstudio:7878). Its last-known threads keep showing because the mesh cache RETAINS an offline peer's threads (deliberate offline-browsing feature — meshsync `MarkPeerUnreachable`). Every TUI action ROUTES to the owning daemon by shelling out `sesh <verb> --machine macstudio` (navSelected/routedVerb/holdRow/archiveRow/rename/tag/…), which for a dead http peer hangs on `client.Client` http.Timeout = **15s** (measured: `time sesh thread list --machine macstudio` → "context deadline exceeded" 15.02s; ~6s for the ssh `tmux nav` carve-out). Actions run in a bubbletea goroutine so the key-loop isn't hard-deadlocked, but you get zero feedback for 6–15s + (via the master cockpit) navving a headed offline thread switches the master window to macstudio's dead ssh-reconnecting pane = looks frozen. archive/hold "don't work" because `archived`/`on_hold` are OWNER-authoritative (owner's store + maintainer-derived), so a viewer can't mutate them without reaching the owner. KEY LEVER: the TUI ALREADY KNEW macstudio was offline — `m.machines[].Reachable` (rendered in the OFFLINE footer) — it just didn't use it to gate actions.
CONFERRED (AskUserQuestion): Lukas chose (1) DISPLAY = **hide offline by default, `o` toggles to show**; (2) ARCHIVE/HOLD on offline = **block cleanly now, revisit later** (a viewer-local override that syncs to the owner on reconnect is a separate bigger feature — deferred).
FIX (internal/tui/model.go + cmd/sesh + config — PURE TUI/CLIENT change, NO daemon/api/schema change ⇒ deploy = rebuild binary, NO daemon restart):
1. FREEZE FIX. `machineReachable(machine)` (self/own-machine always reachable; a machine ABSENT from the mesh view is NOT blocked — never gate on missing data, the routed call surfaces its own loud error; every real row's machine is self-or-peer so always present). `requiresReachableOwner(key)` = the gated key set {enter,a,d,x,f,r,t,T,P,h,H,n,K}. handleKey checks it BEFORE the normal-mode switch (before any confirm/prompt popup opens): if the selected row's machine is unreachable → instant loud `actionErr` "<machine> is offline — can't reach <thread> until it reconnects", returns NO cmd (nothing shells out). Read-only/nav keys (movement/scroll/fold/filter/tab/i/o/y/R/q) stay exempt so offline browsing works.
2. HIDE OFFLINE + `o` TOGGLE. Extracted the mesh→rows flatten from fetch() into a PURE `flattenMeshRows(machines,view,pred,all,hideOffline,preselect)` (unit-testable without a daemon). It drops an unreachable peer's threads unless hideOffline is off; SELF is never hidden. New model field `hideOffline` (default TRUE via New()); `o` key toggles per-session (+refetch); `[tui] show_offline` / `--show-offline` set the default (WithShowOffline: show→hideOffline=false). Footer OFFLINE line reports the hidden count + toggle ("! macstudio OFFLINE · 4 threads hidden · last seen Ns ago · o to show" / "o to hide" when shown); legend gains `o offline`.
Archive/hold are in the gated set ⇒ they give the instant message instead of hanging (the "block cleanly" choice).
TESTS (internal/tui/offline_test.go + config, all green, -race clean): TestMachineReachable; TestRequiresReachableOwnerCoversActions (DRIFT GUARD — every owner-routed key gated, no read-only key gated; a new routed key must be added or this fails, so the freeze can't silently come back); TestOfflineActionRefusedInstantly (every gated key on an offline row → actionErr set, cmd==nil, NO popup opened); TestReachableActionNotBlocked; TestFlattenHidesOfflineMachines (hide default / reveal / self-never-hidden); TestOfflineToggleKey; config TestLoadTUIShowOffline. help.go usage+long (new --show-offline, `o` keymap) + help_flags.go + sesh-cli SKILL (keymap `o`, "Offline machines" paragraph, show_offline config) updated; help meta-tests pass.
LIVE-VERIFIED against the genuinely-offline macstudio (isolated tmux `-L ttest`, real `sesh tui --all-machines` vs the LIVE daemon; read-only + nav is gated so it can't disturb Lukas's live threads): default view HID macstudio's 4 threads (footer "4 threads hidden"); `o` revealed both macstudio threads (local-llm, sesh-pi-fail); Enter on an offline thread refused in **<0.1s** (poll broke on the first 0.1s tick; was 15s); `a`/`h` refused instantly with NO y/n popup. NB `sesh` has no `version` subcommand — read the built revision via `go version -m <bin> | grep vcs.revision`.
SCOPE CUT (told Lukas): if a machine goes offline WHILE you're already inside the K tickets view, ops there can still hang — the gate is on the main-grid entry keys only. Main grid (the complaint) fully fixed; can extend to the ticket sub-view later.
DEPLOY (binary-only; no daemon restart — each `sesh tui` runs fresh from the binary): deployed all machines at 074d191 (pure client change, no schema/mixed-mesh concern). Ticket 23b0ecf2 marked done.

## H34 — can't enter a headless thread: silent-revive fixed (Fix B, KEPT); a bg-agent resolver change (Fix A) was WRONG + REVERTED (2026-07-02, sesh 265e1ae then revert 8e9fef7; NO schema change)
Lukas: "why can't I enter thread 60a56f17?" (a claude headless·idle thread). `thread headful` printed
"promoted to headed" but silently did nothing. TWO issues, one symptom; only Fix B shipped.
**Fix B (KEPT, the real fix) — internal/daemon/spawnverify.go**: reviveThread + handleThreadNew returned
200 the INSTANT they created the pane, never confirming the agent stayed up. `claude --resume` of a
session LOCKED by another running process exited a beat later → the lone-pane session self-destructed →
thread reads headless again → caller told "promoted" (the silent-success class AGENTS.md forbids). Fix:
`confirmAgentLaunched` polls the marked pane for a 3s settle window after spawn; if it vanishes → teardown
+ LOUD error carrying the pane's last output (claude's own reason, e.g. "session held by another agent").
Wired into reviveThread + handleThreadNew's spawn branches (headless/fork/into-pane skip it — no pane).
3s must outlast claude cold-start(<0.5s)+lock-refusal(~1-2s); injectable window for a fast test. Healthy
path proven unbroken (resume + new.headed pi/claude + placement cells green). Daemon-internal, no schema.
**Fix A (REVERTED, was WRONG) — the resolver**: I made the claude leaf resolver skip sessions carrying
the `ai-title`/`agent-name` header (claude's `/agents` bg-agent transcripts open with those). PREMISE
FALSE: that header does NOT mean "not the thread's session" — the USER can be actively driving such a
session, so it can be the thread's real latest work. Fix A wrongly excluded it → resolved to a STALE
anchor. The ORIGINAL resolver (follow the anchor forward to the most-recently-extended fork) was CORRECT;
the only bug was the LOCK (→ Fix B). Reverted session.go + its test to pre-265e1ae.
GOTCHA — killing a claude bg agent: killing the agent process alone doesn't work — a `claude daemon run`
supervisor RESURRECTS it within 1s (even swaps model). You must kill the SUPERVISING claude daemon
(SIGTERM), then SIGKILL surviving pty-hosts (they ignore SIGTERM). Verify the daemon isn't shared with
the user's live interactive claude sessions first (those are plain claude under his shell, not
`--bg-pty-host` children).
LESSONS: (1) the ai-title/agent-name header does NOT mean "not the thread's session" — the user may drive
it. (2) NEVER run a verification-`headful` on a thread whose leaf you're unsure of — a resume APPENDS to
that branch and can flip which fork the "newest wins" heuristic picks, corrupting resolution (this bit me:
my Fix-A verification appended to the wrong branch; it later self-healed when Lukas resumed the right one).
(3) The "newest fork among a shared conversation-root" heuristic is fragile with two concurrently-extended
branches and can't be overridden by an explicit anchor — a durable fix would PIN the thread's session id
instead of re-deriving by newest-tip (a design change to discuss, not to hack).
DEPLOY: 8e9fef7 (Fix B only) to all machines, schema 37 (daemon rebuild + restart). (Note: `sesh daemon
status` text `schema_version` is the STORE migration version; the API schema is `--json | .schema`.)

## Trap digest — durable gotchas from the archived June 2026 build-out

The June 2026 build-out entries (H1-H33, plus the WHICH-CLIENT LAW / session-naming /
env-leak notes) were moved to `_dev/AGENTS.local.archive.md` (NOT imported into agent
context) on 2026-08-22 to keep this file lean — the live-log guidance is "read the last
few entries", so the two-month-old build log was pure per-session token cost. Nothing was
lost: those entries stay in full in the archive and in git history, the durable design
lessons are distilled in the "Reference" section just below, and the traps still
cross-referenced by shorthand in the entries above are kept here so those references
still resolve.

- **the H22 lesson** — on termux, build sesh with PLAIN `go build` (CGO=1 / GOOS=android).
  A `CGO_ENABLED=0` binary runs but its pure-Go resolver can't resolve tailnet MagicDNS
  names (termux has no /etc/resolv.conf) -> the box silently drops off the mesh. Same
  Go-resolver-vs-system class as H45 (NM clobbered resolv.conf) and H73 (self-bind DNS).
- **the H30 pipe-exit trap** — never gate a check/deploy on `cmd | tail` (or any pipe): a
  pipeline's exit status is the LAST command's, so a failed build / `install-home` piped
  to `tail` reports success and leaves a stale half-deploy. Check the real command's
  status. Related zsh trap: a bare `===` / `=word` token in a compound line aborts the
  WHOLE line silently before later commands run.
- **the H33 install-home rule** — `python3 scripts/install-home.py "$MYRIG_TARGETS"` takes
  the FULL comma-separated list. A lone machine name makes `all`-gated files "not match
  targets" and DELETES their symlinks. shell.sh is a rendered jinja (needs a render); the
  confs are symlinks (a `git pull` suffices, but a running tmux server needs source-file).
- **the H25 gotcha** — a new TUI conformance claim must be added to the HARDCODED
  `declaredTUIClaims` list (tui_test.go) AND registered; `registerTUIClaim` only BINDS, so
  a claim missing from `declaredTUIClaims` silently never runs (TestTUIClaimsComplete only
  checks declared->bound, not the reverse).
- **termux daemon relaunch** (H21/H38, repeated in every later termux deploy) — no
  supervisor: kill the old daemon by EXPLICIT pid (never `pkill -f` — it matches your own
  ssh shell, H22/H74), `mv` the new binary in first, then `setsid nohup
  ~/.local/bin/sesh daemon run` with SESH_HOME=~/.sesh SESH_MACHINE=termux sockets
  sesh/sesh-master. /tmp is unwritable (log to $HOME); termux is an inbound-less leaf (no
  SESH_API_ADDR / token).
- **THE WHICH-CLIENT LAW** — tmux cannot map a popup/pane/subprocess pty back to the
  client that triggered it; `display-message -p '#{client_name}'` there is an AMBIENT
  guess. Resolve the client via a BINDING's own `#{client_name}` carrier (baked into
  $SESH_NAV_CLIENT by the myrig popup bindings), never ambiently.

## Reference: foundational decisions & gotchas (from the original 76-cell build)

Run `go run ./cmd/sesh matrix grid` (after `go test ./internal/conformance`) to see the
rendered grid.

Every feature is honest (real agent in a real tmux pane; remote = real `ssh
localhost` hop). All 24 feature rows green across their axes:
matrix spine; daemon + SQLite/WAL; tmux layer incl. **nav** (local + remote via
`--machine` routing); thread layer local+remote — new.headed/kill/list/resolve-pane,
runtime-state, send.headful, **new.headless + send.headless** — for all 3 agents;
ticket layer (create/list-by-thread/set-status/needs-input/send-prompt/ownership);
api.http-json; daemon.mesh-read.

### Things resolved that were once blockers

- **codex trust**: codex shows a per-directory "trust this dir?" prompt that ate
  input. Fixed by `agents.EnsureCodexTrust` — sesh writes `[projects."<dir>"]
  trust_level="trusted"` into codex's `config.toml` at spawn; `CODEX_HOME`
  (SESH_CODEX_HOME) lets tests isolate it with auth.json symlinked.
- **activity probe**: codex's thinking-phase animates only a ~1s timer; probe now
  EARLY-EXITS on working (fast) with a ~3s idle-confirm window.
- **send timing**: codex drops input when text+Enter are back-to-back → 250ms settle
  before Enter (tmux.SendText + test sendKeys).
- **headless**: stateless-per-turn (Lukas's choice). A headless thread is a durable
  conversation, no tmux window; a turn = `<agent> --print/exec --resume`; "working"
  = a turn process is in flight (daemon in-memory registry). pi is NOT N/A — it has
  `--print --session-id`. codex's session id is parsed from `codex exec --json`
  (thread.started) on the first turn.

## Key decisions baked in

- **runtime-state = two orthogonal axes** (activity from pane content-diff, attachment
  from `tmux list-clients`); needs-input = activity==waiting regardless of attachment.
  Lukas signed off (provisionally) in Phase 3b. SPEC §3/§4 updated. See the memory
  `sesh-v2-runtime-state-design`.
- **content-diff probe**: samples a pane 4× over ~1.14s, working iff a MAJORITY of
  intervals change (rejects one-off idle blips like claude's rotating hints / MCP
  startup; catches a real turn's animated spinner). All three TUIs animate while
  working AND are byte-stable when idle.
- **`--machine X` routing**: pseudo-global flag in cmd/sesh; main forwards the command
  (minus --machine, plus the peer's SESH_HOME/SESH_MACHINE) over a real ssh hop.
  Excludes meta commands (peer/matrix/help). This is the honest "remote" path.
- **ticket ownership**: SESH_TICKET_OWNER; ticket commands auto-route to the owner.

## Test gotchas learned the hard way

- A freshly spawned agent's pane is blank → byte-STABLE → content-diff misreads it as
  waiting. Always `waitThreadReady` (TUI rendered ≥3 non-blank lines AND activity
  waiting) before sending, or keystrokes are lost.
- `tmux display-message -t =session` silently returns empty for the `=` exact-match
  prefix → use `list-sessions`/`list-clients` and match the name in Go.
- tmux escapes control bytes in `-F` output (0x1f → literal `\037`); use a TAB field
  separator (passed through verbatim) and treat a wrong field count as a loud error.
- Nested `tmux attach` works headlessly via `env -u TMUX tmux attach` in a viewer
  session (used to test the attached state).
- **Store migrations are APPEND-ONLY** — a mid-list insert desyncs already-deployed DBs
  (their `meta.version` skips the inserted element). Always append a new migration last.
- **Never settle a conformance claim on a row's ABSENCE alone** — it is vacuously true
  before the maintainer first publishes the row. Settle on PRESENCE first, then assert
  the negative.
- **The conformance suite CANNOT catch deploy-env gaps** — test daemons inherit the dev
  shell (PATH, mise shims, API keys), but the supervised production daemon has a bare env.
  So live-smoke after deploy is MANDATORY for daemon-exec paths (headless turns run via
  `$SHELL -c`, like tmux runs pane commands); the supervisor ini pins PATH/shims/SHELL.

## H50 — herdr-vs-sesh migration assessment: DON'T migrate; steal the integration idea (2026-07-23, NO code change; ticket 3aa7a590 done)
Lukas found https://herdr.dev/ (Rust agent multiplexer, ogulcancelik/herdr, v0.7.5, solo
full-time maintainer, Apache-2.0) and asked whether to migrate off sesh. Cloned + three deep
code passes. FINDINGS: herdr = the tmux+`sesh tui` cockpit layer only, done very well
(mouse-first, per-agent manifests for 19 agents incl. claude/codex/pi, AUTHORITATIVE
lifecycle integrations w/ real `blocked` + seen/unseen `done` states, rich newline-JSON unix
socket API, plugin [[events]] hooks). It has NONE of the four load-bearing pillars: (1) no
persistent identity — no UUIDs anywhere (workspaces w1/w2, panes w1:p3, forgotten at close),
(2) no archiving/history — close is destructive, code-verified, (3) no tickets/task concept
at all + API is LOCAL-ONLY (zero TCP in codebase → mysystem can't reach it), (4) no
multi-machine — --remote is a thin ssh stdio bridge of the RENDER client to ONE host; no
mesh/fleet/merged view. Also no cwd→label regex (sesh's boxyard-feel cwd_label; herdr
sidebar has custom $name metadata tokens but they're wiped on server restart). herdr DOES
auto-resume still-open panes across server restart via stored native session refs
(claude --resume/codex resume/pi --session) — restart-continuity only, not revival of
closed conversations. Assessment in myvault pad "Herdr vs sesh - migration assessment".
VERDICT (delivered): don't migrate; bridging = rebuilding sesh's daemon/store/mesh on a
preview-channel API. IDEAS WORTH STEALING for sesh: (a) integration-style authoritative
turn-state reporting from inside pi/claude → replace the content-diff busy heuristic (the
H24/H47/H48 bug class); (b) a `blocked`/approval-prompt state + seen/unseen done for notify
gating. BRIDGE THAT EXISTS TODAY: a conversation started in a herdr pane can be registered
into sesh afterwards via headless adopt (`thread adopt --agent X --session-id Y`).

## H51 — the herdr-steals batch SHIPPED: state authority + blocked + done/seen + send --wait (2026-07-23, sesh cf0092f..649bb60 api 42→43, myrig 8df129b, myagent 7eb8033; deployed ALL FIVE; issues #4-#7 closed, #8 open)
The four features from the H50 assessment, implemented + deployed in one session. 14 new
matrix cells green + blast radius (runtime-state ×6, send.headful ×6, notify, mesh.snapshot
both transports) + full TUI claims + -race clean.
- STATE AUTHORITY (#4): in-agent reporters override the content-diff busy heuristic.
  POST /v1/threads/report-state {thread_id, source, event turn_started|turn_ended|blocked|
  unblocked|release, strictly-monotonic seq, reason?} → in-memory authority map on Daemon
  (runtime state; restart = heuristic floor until next report); maintainer's headful path
  prefers a live entry (content-diff still runs — keeps lastActive + the window warm for
  degradation); EVERY no-runtime path clears authority (pane-liveness bound — a dead
  reporter can't pin busy). snapshot/row gain state_authority reported|heuristic (omitempty;
  unset on headless — the turn registry needs no label; the grid's on-demand fallback also
  unset by design). REPORTERS exec `$SESH_BIN thread report-state` — SESH_BIN
  (agents.EnvSeshBin, injected by Daemon.spawnEnv into every spawn/revive/into-pane env) is
  the daemon's own os.Executable(); a pane's PATH `sesh` is UNRELIABLE (login shells
  re-prepend profile dirs — the smoke pi called the old schema-42 binary until this). pi:
  integrations/pi/sesh-agent-state (extension in THIS repo; myagent 7eb8033 registers it as
  extensions/sesh-agent-state → relative relay symlink; herdr-derived mechanics:
  agent_settled + ctx.isIdle() double-check, session_start re-derives after mid-turn
  /reload, release ONLY on session_shutdown reason=quit, serialized latest-wins send queue).
  claude: integrations/claude/sesh-agent-state.sh via Claude Code hooks (myrig settings.json,
  now UserPromptSubmit/Stop/Notification/PostToolUse — skips subagent invocations via
  agent_id, never maps SubagentStop, ALWAYS exit 0 or Stop hooks would block claude). codex:
  justified N/A both localities (notify config = turn-end only; one-directional authority is
  worse than the heuristic — Lukas's call).
- BLOCKED (#5): reported-only overlay (busy axis stays two-valued; blocked ALWAYS implies
  busy incl. no-prior-entry). claude Notification → blocked iff message contains
  "permission" (the idle reminder is NOT blocked); PostToolUse → unblocked; both turn
  boundaries clear. blocked_changed event + SESH_BLOCKED(1/0 always)/SESH_BLOCKED_REASON
  (presence-gated)/SESH_STATE_AUTHORITY(presence-gated) env. TUI ‼ + `blocked` keyword.
  Cell drives a REAL permission prompt — sandbox forces [spawn.claude] args
  --permission-mode default because the user-level default is an AUTO mode that
  self-approves safe Bash (live-smoke finding). KNOWN POLICY GAP (flagged): the fleet's
  [spawn] mode=yolo bypasses permissions → blocked rarely fires for spawned threads until a
  thread opts out of yolo.
- DONE/SEEN (#6): liveState.doneSince via PURE nextDoneSince (doneseen.go truth table):
  headful busy→idle edge while unattended (detached OR attached-with-input-staler-than-60s —
  parked cockpit clients don't count, H48) sets; fresh input OR an attachment FLIP onto the
  session clears (switch-client bumps no client_activity — the flip IS the nav signal);
  retained across stop/headless; publish() stamps done/done_since_unix + prevAttachment
  (single choke point). done_changed event + SESH_DONE. TUI ✔ (idle-only; ‼ wins) + `done`.
  Cell: real pi turn detached → done; REAL nested attach (attachViewer) clears with zero
  keystrokes; freshly-attended turn end never sets. Headless turn completion deliberately
  excluded (subscriptions/await own that).
- SEND --WAIT (#7): GET /v1/threads/wait?id&until&timeout_ms — ONE bounded server-owned
  wait (100ms polls, 10s/request cap; reached=false@bound is a 200 — the CLIENT loop
  decides; keeps the client's hard 15s http timeout safe; remote = the --machine router
  re-execs the CLI on the owner so the loop is local there, ONE hop total). until: busy|
  idle|blocked|settled (settled = idle-or-blocked — bare idle would sit out an approval
  prompt). CLI `thread wait --until --timeout` + `thread send --wait --timeout` with the 5s
  stall guard (busy latch OR LastActiveUnix advancing = progress; already-busy sends skip
  it). GOTCHA: a wedged pane CANNOT be honestly staged in a cell — tmux SIGCONTs any
  stopped pane child instantly (measured; SIGTTIN is caught by node agents) — so the stall
  composition is cmd/sesh UNIT tests vs a scripted daemon (legit outside the matrix); the
  cells prove real-turn release (computed GREENLIGHT sentinel present on return) + loud
  timeout, all 3 agents × both loc.
- HOOKS/NOTIFY: blocked_changed + done_changed added to config.ValidHookEvents (they were
  emitted but UNSUBSCRIBABLE — the daemon refuses unknown hook events at start; deploy
  ordering: new binary must land with/before the rendered config). myrig 8df129b: notify-idle
  (busy_changed + H47/H48 age-gating) REPLACED by notify-done (done_changed seen→done — the
  daemon's derivation makes the edge exact) + notify-blocked (blocked_changed → "‼ <name>
  needs you" + reason); sesh-notify keeps the attended-gate as belt-and-braces. Hook mute
  state is per-NAME → renamed hooks start enabled.
- REPAIRED pre-existing red: TUI claim custom-views hardcoded 3 Tabs to reach the custom
  view — red on CLEAN main since H25 added the `on hold` built-in (verified via worktree).
  Now tabs by TITLE (bounded 8).
- CONCURRENT-SESSION rebase: another agent landed tui glyph-colors + took H49 (sesh
  4f60d10, myrig c1d649a) while this ran — rebased sesh (clean) + myrig (clean), renumbered
  my assessment entry H49→H50. Check `git log origin/main` before pushing.
- DEPLOY (api 43 = rebuild + daemon RESTART all five + myrig RENDER + myagent pull/symlink):
  mymain native; macbook + macstudio /opt/homebrew/bin/go + supervisorctl (macbook's system
  python3 LOST jinja2 — render needed `uv run --with jinja2`, the H46 class; macstudio's
  still has it); ideapad uv-render + supervisorctl; termux plain go build + explicit-pid
  kill + setsid-nohup relaunch (pid 8412). pi-extension symlink created on all five
  (~/.pi/agent/extensions/sesh-agent-state → myagent relay). All five verified schema 43;
  macbook lists notify-done + notify-blocked enabled. LIVE-PROVEN on the real fleet: spawn
  pi → send --wait settled with the turn → state_authority=reported + done=True (detached
  finish); macbook's fan-out shows a mymain thread auth=reported (mesh replication of the
  new fields); even THIS pre-43-spawned claude session reports (hook's PATH fallback now
  resolves the deployed binary).
- REMAINING: #8 (persistent sidebar cockpit UI revamp) — design-discussion-first, NOT
  started, by Lukas's instruction. sesh-ui: no changes made; the new snapshot fields
  (state_authority/blocked/done) are available to it — surfacing them there is follow-up.

## H52 — the FLAGGED system replaces done/blocked (2026-07-23, sesh eeae368+12dcfae api 43→44 store 21→22, myrig b51b136; deployed 4/5 — termux OFFLINE, pending; ticket df4fb07a done)
Lukas, hours after H51 shipped: "not much of a distinction between done and blocked — any
agent that stops their turn should be looked at" → both 43-era overlays REMOVED, replaced
by his ticket-df4fb07a flag design. CONFERRED: manual-only clearing (flags NEVER
auto-clear); flag-on RE-ENABLES a flag-disabled thread and flags it (one rule, no
auto-vs-manual provenance bit — his refinement).
- MODEL: STORED flagged/flag_reason/flag_disabled on the record (migration 22) — persists
  across restarts + replicates like any record field (fixes done's runtime-only wart).
  AutoFlag guards (flag_disabled + already-flagged) in SQL, atomic. Auto-set (autoflag.go,
  pure truth table): UNATTENDED (H48 60s-input window; parked clients don't count) turn-end
  edge — REPORTED edges always, HEURISTIC edges only via [flags] heuristic_agents (default
  none, per ticket) — or an unanswered STALL (question/approval), checked per tick so
  nav-away-from-a-prompt still flags; one flag per stall episode (stallFlagged latch) so a
  manual unflag isn't fought while the same prompt sits open. Headless turn ends do NOT
  flag (delegate/await/subscriptions own that delivery).
- WIRE REMOVALS (44): done/done_since/blocked/blocked_reason snapshot fields,
  done_changed/blocked_changed events (ValidHookEvents!), SESH_DONE/SESH_BLOCKED* env.
  blocked lives on DAEMON-INTERNALLY (authorityState) feeding the flag trigger + the wait
  endpoint's blocked/settled conditions (waitConditionMet now takes busy+blocked; the wait
  runs on the owner and reads its own authority map). flag_changed event +
  SESH_FLAGGED/SESH_FLAG_REASON (presence-gated).
- ALL THREE HARNESSES hook-driven (the ticket's preference): claude Stop; pi agent_settled;
  CODEX = sesh now wires its notify config AT SPAWN (embedded internal/agents/codexnotify.sh
  → materialized <SESH_HOME>/codex-notify.sh; notify= PREPENDED into codex config.toml — a
  TOP-LEVEL toml key, appending would land inside the last [projects.*] table; an existing
  user notify is never clobbered). codex reports the NEW turn_ended_no_authority event =
  flag evaluation WITHOUT busy authority (one-directional authority would pin idle through
  real turns — the reason codex is N/A on thread.state-authority).
- ASKUSERQUESTION (his explicit contingency): PreToolUse fires with tool_name=AskUserQuestion
  + the FULL question JSON exactly when the prompt shows (verified empirically via a
  project-scoped hook logger BEFORE designing) → blocked report with the question as reason
  → flag reason = the question. TWO gotchas bit me: (1) the script mapping alone did
  NOTHING — PreToolUse also had to be REGISTERED in myrig settings.json (cell caught it);
  (2) claude ALSO fires a generic "needs your permission" Notification for the SAME prompt
  (identical message for questions and permission prompts!) and hooks run concurrently →
  daemon rule: within one stall episode the FIRST reason wins (a later 409'd/generic report
  can't overwrite the specific question).
- TUI: new FLAG gutter cell (gutterWidth 9→10 + header + mouse-test coords — the H40
  drill): ⚑ red-tinted (new [[tui.glyph_color]] name="flag", default "9") / ⌀ disabled;
  F toggle + ^f gate toggle (requiresReachableOwner + offline lists); flagged/flagdisabled
  predicates; FOLD-PIERCING: flagged descendants (any depth) render as direct rails under a
  collapsed parent (▸ kept), unflagging re-hides (TestFoldPiercing + view-flag-pierce claim).
- MATRIX: thread.done-seen + thread.blocked features REMOVED (superseded, not gamed);
  thread.flagged 6/6 GREEN (all agents × both loc; claude cells drive a REAL
  AskUserQuestion; codex cells prove the notify wiring end to end). TUI claims action-flag +
  view-flag-pierce (registered AND declared). ANTI-GAMING: neutered autoFlagTrigger →
  pi/local red "turn end never flagged" → reverse-edited back. Blast radius green:
  state-authority ×4, send-wait ×6, runtime-state/pi ×2, FULL TUI claims, -race.
  filter-target-uuid FLAKED once under batch load (passes ×3 isolated — the H8 flake class).
- CONCURRENT SESSIONS twice more: sesh push rejected (glyph-colors session), myrig too
  (ghostty/spacework) — fetch+rebase before every push this week.
- DEPLOY (44 = rebuild + RENDER + restart TOGETHER — the 43-era hook events REFUSE a 44
  daemon): mymain + macbook (uv-render; hooks list shows notify-flagged enabled) +
  macstudio + ideapad ✓; termux came back later the same day and was deployed the standard
  termux way (pull + plain go build + explicit-pid kill 3298 + setsid-nohup relaunch → pid
  5973, schema 44 verified ON THE BOX — it is an outbound leaf, per H38) → ALL FIVE on 44. LIVE-PROVEN on mymain:
  detached pi turn → flagged=True reason="turn ended" (+ send --wait settled); flag --off/
  stop/delete clean. Cross-machine cached-view spot-check SKIPPED (grid fan-out hung on the
  offline termux peer — the H35/H36 slow-peer class); replication rides the same
  record-serialization path the mesh full-replication guards pin.
- NOTE: with [spawn] mode=yolo fleet-wide, permission-prompt stalls rarely occur, but
  AskUserQuestion stalls + turn-end flags fire under yolo too — the flag triggers that
  matter are live. sesh-ui: flags not yet surfaced there (follow-up if wanted).

## H53 — TUI: f=flag / F=fork swap + keymap → `?` popup (2026-07-24, sesh f296c45; NO schema change; binary-only, deployed ALL FIVE, no restarts)
Lukas: flag deserves the lowercase key; the always-on wrapped legend ate 3-4 rows. f =
toggle flag, F = fork (^f unchanged; both stay gated — offline lists + requiresReachable
comments swapped; action-fork/action-flag/virtual-refusal claims re-pressed + green). `?` =
full-screen keymap popup (helpView, width-WRAPPED per the H1 lesson; esc/q/?/enter close;
in the mouse modal guard); bottom line = dim "? keys" hint; MOVE MODE keeps its ambient
reorder legend (mode feedback must stay visible); legendLines() budget self-adjusts (it
measures the rendered line). TestLegendOverflowsNotClips → TestHelpPopupAndLegendHint.
help.go tui long + SKILL synced. Deploy: binary-only on all five (each `sesh tui` runs
fresh from the binary; ideapad has no `hostname` cmd — verify via `go version -m` vcs.revision).

## H54 — TUI: per-line scrollable `?` keymap + INSTANT archive with `U` undo (2026-07-24, sesh 94a8bd4; NO schema change; binary-only, deployed ALL FIVE, no restarts)
Two asks. (1) The `?` popup lists ONE BINDING PER LINE (helpBindings entries incl. mouse
gestures) and SCROLLS on height overflow (↑/↓, j/k, ^j/^k, wheel — the MouseMsg wheel path
diverts to helpOffset while the popup is up); ▲/▼ more-indicator lines ALWAYS rendered
(blank when unneeded) for stable layout; width-truncation with …. (2) `a` archives
INSTANTLY — confirmArchive REMOVED (`d` keeps its y/n) — act-then-undo: a CONFIRMED
archive attaches archiveUndoEntry to its actionMsg (success only, never a bogus target),
pushed to a session LIFO stack (cap 20) + note `archived "x" · U to undo`; `U` pops,
un-archives routed, preselects the restored row; repeated U walks back. KEY DESIGN: U's
target comes from the STACK not the selection → NOT in requiresReachableOwner (that gate
checks the SELECTED row's machine — the wrong target); undoLastArchive checks the entry's
own machine and refuses loudly KEEPING the entry when that owner is offline. Tests:
TestArchiveInstantAndUndo, TestHelpPopupAndLegendHint (scroll window/paging), claim
action-archive rewritten (no confirm + U restores on the REAL daemon; action-delete's
confirm untouched). help.go + SKILL synced. Deployed all five at 94a8bd4 (binary-only).

## H55 — EXPLORATION (no code): why remote TUI actions lag + the options (2026-07-24; measurements on the live mesh)
Lukas: flagging/archiving a REMOTE thread from the TUI lags surprisingly. MEASURED (live,
scratch headless threads, cleaned up): tailnet RTT ~40ms both macbook+ideapad (first
tailscale ping reads high — path setup; use -c 4). LOCAL flag verb ~33-52ms (mostly fork).
ROUTED flag verb ~190-240ms = fork(~12) + peers.json + COLD TCP connect + **the id-prefix
resolve fetching the peer's ENTIRE thread list incl. archived** (resolveIDFlag→
resolveIDPrefix→listAllThreads runs against the REMOTE daemon once route.go points the
process at the peer's API) + the actual POST ≈ 3 sequential round trips where 1 would do.
Change visible in the LOCAL mesh cache (what the TUI renders for remote rows): 0.6-1.4s
(peer publish ≤300ms + delta pull at the 1s active cadence). TUI adds its own fetch
alignment (3s poll + the 2 post-action reconciles) ⇒ perceived ⚑ latency ~1-3s (worse
from idle cadence). KEY UI FACTS: optimistic patches apply at CONFIRMATION (after the
round trip), and FLAG has NO rowPatch at all (H52 chose none) — archive hides at ~200ms
remote, flags wait the full cache path. GOTCHA that burned 15 bogus seconds: `sesh mesh
--json` is ONE pretty-printed document (NOT JSONL like thread list/grid) — a JSONL parser
returns nothing and a sloppy poll loop reports its own timeout as the latency.
OPTIONS EVALUATED (recommended A+D+C; E/F stop mattering once A lands):
- A. KEYPRESS optimism with per-action revert (H36's deferred item 3): patch at keypress,
  revert exactly that patch + loud actionErr on failure. Needs per-action patch identity
  (the H36 blocker). 0ms perceived for EVERY action on every machine. The main fix.
- B. (subsumed by A) at minimum give flag/flagDisabled a rowPatch at confirmation → ~200ms.
- C. Post-write mesh NUDGE (H36 item 5): after a routed write, kick the local syncer to
  pull that peer now (H44 kick machinery exists) → cache truth in ~RTT; shrinks A's lie
  window + lets patches reconcile fast instead of living their TTL.
- D. Skip the remote LIST when --id is a full well-formed UUID (the TUI always passes
  row.ID): resolve is a no-op then; saves a round trip + the big-list transfer; daemon
  still 404s unknown ids loudly. Tiny, zero-risk.
- E. Daemon-side WARM routing for verbs (generalize H32's master-current fix): local
  daemon forwards over its warm mesh conn; kills fork+cold-connect (~200→~85ms measured
  for master-current). Real architecture change; value drops to faster-error-feedback
  once A exists.
- F. In-process client for LOCAL rows (routedVerb forks even locally, ~40ms). Same note.
NOT implemented — Lukas to pick; next session should re-read H36 items 3+5 first.
