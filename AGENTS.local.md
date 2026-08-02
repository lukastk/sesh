# AGENTS.local.md — sesh v2 working notes

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

## H33 — mt-enter-new-thread-here landed the thread in $HOME (cwd from $PWD, not the pane) (2026-06-29, myrig 8a4965e; NO sesh change; deployed ALL FOUR)
Ticket 1284a9c3: ran `mt-enter-new-thread-here` inside a workspace dir to start a Claude session,
but the session opened in $HOME. DIAGNOSIS: the recorded thread (70b8f23f) had cwd=/home/lukastk +
session=scratch. The sesh side was innocent — CLI absCwd passes an absolute path through, daemon
validates absolute + CreateWindowCmd uses `-c dir` correctly. ROOT CAUSE was the MYRIG shell
function: `mt-enter-new-thread-here` passed `--cwd "$PWD"`, but it's listed in MT_QUICK_CMDS, so it
runs from the work prefix+m quick-menu DISPLAY-POPUP — and a display-popup starts in $HOME, so $PWD
was $HOME (not the pressing pane's dir). The session resolved right (scratch) because the popup's
client session matched, but $PWD didn't. FIX (shell.sh.jinja, render-only): resolve BOTH session and
cwd from the ORIGINATING pane = `${SESH_MT_PANE:-$TMUX_PANE}` ($SESH_MT_PANE is baked in by the work
prefix+m binding, else $TMUX_PANE when run directly in a pane) via `tmux display-message -t "$pane"
-p '#{session_name}'` / `'#{pane_current_path}'`. pane_current_path is the real "here" whether run in
the pane or from the popup. Mirrors the established $SESH_MT_PANE pattern in _mt_current_thread. So
the H14 "run it IN your pane, NOT in the popup" caveat is now moot — it works from both.
PROVEN: isolated tmux server, pane cd'd into a test dir, then SESH_MT_PANE=<pane> + cwd /home/lukastk
(the popup's wrong $PWD) → resolution returned the test dir + scratch, not $PWD. Full rendered
shell.sh passes `zsh -n`.
DEPLOY (render-only — shell.sh is a rendered jinja, sourced into shells; NO daemon restart, NO conf
re-source since it's a function not a binding): ALL FOUR via `python3 scripts/install-home.py
"$MYRIG_TARGETS"` (or uv --with jinja2). mymain local + macbook/macstudio/termux over ssh-target.
GOTCHA (bit me): install-home takes ONE comma-separated arg = the FULL `$MYRIG_TARGETS` string. I
first ran `install-home.py mymain` (single target) which made shell.sh + every sesh/myrig conf
"no longer match targets" and DELETED the symlinks (shell.sh is `all`-gated, not satisfiable by
`mymain` alone). Re-running with the full `"$MYRIG_TARGETS"` string fully restored them. NEVER pass a
lone machine name to install-home — always the whole comma list. Ticket marked done (closed by 8a4965e).

## H29 — maintainer idle early-out: stop fork/exec-ing tmux+ps every tick with 0 threads (2026-06-26, sesh a9529ae; NO schema change; deployed ALL FOUR)
Lukas saw "loads of copies of the sesh daemon" in termux htop + asked for a myrig review for
runaway/overload. DIAGNOSIS (ssh into android-main:8022): NOT multiple daemons — exactly ONE
`sesh daemon run` (the htop "copies" = its 16 Go runtime OS threads, all comm="sesh"; htop shows
threads as rows unless you press H). No leak/zombie/flap (12 FDs, 0 zombies, boxyard-sync a single
hourly WiFi-gated nice-19 loop, pgrep singleton guards in the termux zshenv launch block all work).
BUT the daemon genuinely sustained ~10.6% of a CPU core CONTINUOUSLY with ZERO local threads (1
tmux pane = just scratch). ROOT CAUSE: maintainer.tick() (every ~300ms) unconditionally ran TWO
per-tick global enumerations — `tmux` PaneIndexByThreadID + `ps` NewProcSnapshot — even with no
threads. On a wake-locked phone that fork/exec churn (~4-5 tmux spawns/sec under ppid=daemon) is
pure battery waste. (NB the "6 days" I first saw was SYSTEM uptime; the daemon PROCESS had only
been up ~1.7h — etime, not CPU TIME — so the 10% was real, not a startup artifact. Measure
instantaneous CPU via /proc/<pid>/stat utime+stime delta / CLK_TCK=100, getconf is absent on termux.)
FIX (internal/daemon/maintainer.go, two early-outs): (1) len(threads)==0 → clear(m.st) + return
BEFORE any tmux/ps call. (2) the `ps` snapshot is only consulted on refreshThread's found-MARKED-pane
path, so compute it only when PaneIndexByThreadID returned ≥1 marked pane; else procs stays nil
(never dereferenced) — every thread resolves headless·idle (or turn-in-flight via the registry)
without it. So a leaf holding only headless/idle threads also skips ps. POPULATED PATH UNCHANGED: a
thread with a live pane ⇒ panes>0 ⇒ procs computed exactly as before, busy-detection byte-identical.
TESTS: added observable counters maintainer.probedPanes/probedProcs (incremented when each
enumeration actually runs) + TestMaintainerIdleEarlyOut (real isolated store + real empty tmux
server): 0 threads ⇒ 0/0; a headless thread (no marked pane) ⇒ panes enumerated once, ps skipped,
resolves headless·idle. daemon+tmux unit suites green, -race clean; thread.runtime-state/pi/local
conformance cell still GREEN with a real pi agent (busy still latches). NO schema change ⇒
mixed-mesh safe, deploy = daemon restart.
DEPLOY: ALL FOUR. termux FIRST (lukas@android-main:8022 — git pull, PLAIN `go build`=CGO=1/android
per H22, .new+mv, kill daemon by EXPLICIT pid 1563 NOT pkill -f, setsid-nohup relaunch with
SESH_HOME=~/.sesh SESH_MACHINE=termux sockets sesh/sesh-master + termux-wake-lock). VERIFIED LIVE on
termux: CPU 10.6%→~5-6% steady, daemon tmux spawns 9-in-2s → 0, peers still synced. mymain (native
build + supervisorctl restart sesh-daemon), macstudio (cij@macstudio) + macbook (lukas@macbook) (git
pull + /opt/homebrew/bin/go build + supervisorctl restart). Mesh healthy post-deploy.
RESIDUAL (flagged to Lukas, NOT a bug): the ~5-6% left on termux is the normal mesh-leaf baseline
(1s meshsync to 3 http peers) PLUS a master-tmux COCKPIT currently running ON the phone (sesh-master
server + 4 window supervisors + 3 persistent outbound ssh -tt to mymain/macbook/macstudio). If Lukas
doesn't use the phone cockpit, `mmt-kill` drops that overhead (masterMaint self-heal rebuilds windows
only if a master server EXISTS, so kill removes it until next mmt-start). A future "leaf low-power
mode" (longer maintainer/mesh tick on battery machines) would cut the meshsync residual but Lukas
chose just the idle early-out for now.

## H28 — TUI `f` fork: keyboard shortcut to copy the selected thread (2026-06-26, sesh 07e7298; NO schema change; deployed ALL FOUR)
Ticket "Forking feature" (b662ec8b): fork a thread regardless of agent, via a TUI key that
"just copies the selected thread" → the copy is headless, you enter it to continue. Asked to
check if it already exists. IT MOSTLY DID: the CLI fork (`thread new --fork-from <id>
[--message-id N]`) is a complete PARITY_ROADMAP D3 feature — internal/fork/fork.go branches the
source transcript (claude/codex/pi uniformly: copies the prefix through the Nth assistant turn,
0=whole, rewrites the embedded session id), internal/daemon/forkthread.go writes it at the
agent's own transcript location under a FRESH session id + records a new HEADLESS thread
(HeadlessStarted=true so turns RESUME), source untouched. Owner-side by construction (source
transcript on that daemon's disk). thread.fork conformance cells already green for all 3 agents
× local/remote. So the ONLY gap was the TUI shortcut.
FIX (TUI/CLI-only, internal/tui/model.go): new `f` key → `forkSelected()` execs `thread new
--fork-from <row.ID> --json`, adding `--machine <owner>` when the row isn't local (same routing
as routedVerb — the transcript lives on the owner; --fork-from then resolves the source on that
daemon). Forks the WHOLE conversation (no --message-id) into a new headless copy; nothing is
started. On success returns actionMsg{preselect: newID} (no patch — it's a NEW row, not an edit
of an existing one) so the cursor lands on the copy once the reconcile fetch brings it in. Did
NOT reuse routedVerb (that builds `thread <verb> --id <row>`; fork is `thread new --fork-from`,
a different shape + needs the new id parsed from JSON). Legend gained `f fork`; sesh-cli SKILL
keymap updated.
TESTS: new TUI claim action-fork (added to declaredTUIClaims in tui_test.go AND registered —
both required, per the H25 gotcha) — drives a REAL pi source (sentinel OBSIDIAN, one headless
turn), presses `f`, asserts a new headless pi thread appears with a DIFFERENT session id whose
transcript carries OBSIDIAN (a real copy, not empty) while the source transcript is byte-
untouched. Ran live → PASS (9s, real pi). TUI unit tests + help meta-tests + neighboring
action-stop claim all still green. gofmt also realigned a pre-existing confirmKind iota comment
block (harmless).
NO api/schema change (the fork endpoint is unchanged; this is a pure TUI client key) ⇒ deploy =
update the sesh BINARY only, NO daemon restart. Deployed ALL FOUR at 07e7298: mymain (native
go build .new+mv to ~/.local/bin/sesh), macstudio (cij@macstudio) + macbook (lukas@macbook)
(git pull + native /opt/homebrew/bin/go build + .new+mv — no supervisorctl restart needed),
termux (lukas@android-main:8022 — git pull + PLAIN `go build` = CGO=1/GOOS=android per H22,
verified on the installed binary, .new+mv; no daemon relaunch needed since binary-only). All
four vcs.revision=07e7298. Ticket b662ec8b marked done.
FOLLOW-UP (sesh b7eadb7, Lukas): keep the source's name marked " (fork)" instead of a nameless
copy — forkSelected passes `--name "<row.Name> (fork)"` (a nameless source → "(fork)"). claim
asserts the copy of "trunk" is named "trunk (fork)"; SKILL keymap updated. Binary-only redeploy
ALL FOUR at b7eadb7 (same recipe, no daemon restart).

## H27 — cockpit clipping: the live-terminal bridge left `window-size largest` stuck forever (2026-06-25, sesh c44b5b9; NO schema change; deployed ALL FOUR)
Lukas: "the master tmux setup cuts off the bottom in Claude Code, esp. its multiple-choice
modal; a previous commit tried but it's still an issue." The "previous commit" = myrig
32fc93f, which set `window-size latest` in tmux.common.conf (right for the cockpit: size a
window to the client you're TYPING in, so a fullscreen Ink TUI never lays out taller than
your view + clips its bottom rows below the viewport). It never took because the conf isn't
the only writer. ROOT CAUSE (found via `tmux -L sesh show -gw window-size` = `largest` on the
LIVE work server while the conf + the master server both said `latest`): the sesh DAEMON's
live-terminal bridge (sesh-ui web terminal, internal/daemon/terminal.go) does `set -g
window-size largest` GLOBALLY on the work server for detach-safety (a smaller web viewer must
not shrink the user's real attachment) — and NEVER restored it. So ONE web-terminal connection
flips the long-lived work server to `largest` permanently, overriding the conf; `largest`
sizes a window to the TALLEST attached client → a stale/secondary taller client makes Claude
lay out bigger than the cockpit client you view through → bottom rows (the modal/input box)
clipped. PROVEN in an isolated nested-tmux repro: largest→inner window 120x48 (tall client),
latest→120x26 (active/cockpit client). Long-lived work servers never pick up the conf change
(they persist across daemon restarts — they hold the agent sessions), so the stuck `largest`
sat there for a day.
FIX (make the override TRANSIENT, internal/daemon/terminal.go): a bridge still forces
`largest` while live, but `normalizeWindowSize()` winds it back to `latest` (tmux's own default
+ the cockpit conf policy) once NO `uiterm-*` viewer session remains. The PRESENCE of a viewer
session — not handler return — is the authoritative "a bridge is live" signal, because an IDLE
pane's disconnect goes undetected (neither pty→WS nor WS→pty sees traffic, so c.Read never
errors; this is the very reason the viewer REAPER exists). normalizeWindowSize runs (a) on each
bridge exit and (b) in the reaper after its sweep — so the reaper's STARTUP sweep (tracked set
empty → every uiterm-* is an orphan and is killed → no viewer remains) SELF-HEALS any work
server a prior daemon left stuck on largest. ⇒ a routine daemon RESTART fixes it (which is the
deploy). The old `defer unregisterViewer` + `defer kill-session` became one combined exit defer
(unregister → kill viewer → normalize). NO api/schema change (internal-only) ⇒ mixed-mesh safe,
no restart-ordering hazard.
TESTS (honest/deterministic): TestUITermViewerReaper EXTENDED — seed `window-size largest` +
leaked uiterm orphans BEFORE the daemon starts, assert the startup sweep self-heals to `latest`
(the durable fix, no agent needed). TestThreadTerminalWebSocket — assert `largest` is in force
while a REAL pi bridge is live. (Did NOT assert restore-on-close in the WS test: an idle pi
pane's disconnect can go undetected so handler-return isn't a reliable trigger — the reaper test
proves the wind-back deterministically instead.) daemon -race clean.
DEPLOY (daemon RESTART, no schema change): ALL FOUR. mymain (build .new+mv + supervisorctl
restart sesh-daemon) — work server flipped largest→latest on restart, LIVE-PROVEN. macstudio
(cij@macstudio) + macbook (lukas@macbook): git pull + native build + supervisorctl restart
(both were already `latest` — no recent bridge had clobbered them — and stay latest). termux
(lukas@android-main:8022): git pull + PLAIN `go build` (CGO=1/android per H22 — verified
CGO_ENABLED=1 GOOS=android on the installed binary), .new+mv, kill daemon by EXPLICIT PID (NOT
pkill -f, which matches the ssh shell), setsid-nohup relaunch with SESH_HOME=~/.sesh
SESH_MACHINE=termux SESH_TMUX_SOCKET=sesh SESH_MASTER_SOCKET=sesh-master (the exact env from
~/.myrig/zshenv/termux.sh; no API token — inbound-less leaf). All four `tmux -L sesh show -gw
window-size` = `latest`; mesh synced (mymain↔macbook/macstudio over http :7878). NOTE: only
mymain's live server was actually stuck on `largest` (the others happened to be latest); the
real WIN is the daemon no longer leaves it stuck, and self-heals on restart.

## H26 — hold INHERITANCE: a child inherits its parent's hold, effective = max(own, ancestors) (2026-06-25, sesh 373944b; api schema 34→35; deployed ALL FOUR)
Follow-up to H25 (Lukas): "if a parent thread is on hold, the child threads are too —
you inherit the hold status; an individual thread's hold = max(parent's hold date, its
own explicitly set date)." Implemented DERIVED (not stored/propagated), so a parent's
hold change flows to the whole subtree on the next maintainer tick with no fan-out.
WHERE: the OWNING daemon (so CLI/TUI/predicates AND the sesh-ui app all get it free —
sesh-ui reads `on_hold` and inherits for nothing). Computed per machine over that
daemon's own records → a CROSS-MACHINE parent's hold is NOT inherited (the chain ends at
a parent absent from the local set; documented limitation — parent/child are co-located
in practice, e.g. a thread + its sub-threads on one machine).
- api (schema 34→35): NEW derived `on_hold_effective_unix` on ThreadRow/ThreadSnapshot =
  max(own on_hold_until, every same-machine ancestor's own). `on_hold` is now derived
  from the EFFECTIVE deadline (was the OWN deadline). `on_hold_until_unix` stays the
  thread's OWN editable value (what hold/H set/clear). Additive omitempty + a semantic
  widening of the existing bool → mixed-mesh safe (a pre-35 peer reports non-inherited
  on_hold for its threads until upgraded).
- daemon internal/daemon/hold.go: `effectiveHolds(threads) map[string]int64` — builds
  id→own + id→parent maps from ONE machine's thread list, walks each thread's ancestor
  chain taking the max (visited-set + depth cap 256 against cycles, which reparent already
  refuses). Unit TestEffectiveHolds(+MaxAndCycle): root→mid→leaf inheritance, own-later-
  wins, cross-machine-parent-absent stops the chain, a→b cycle resolves to the cycle max.
- maintainer: tick() computes effHolds ONCE per tick (full ListThreads(true)) and passes
  effHolds[id] into refreshThread → the snap carries OnHoldEffectiveUnix → publish derives
  `snap.OnHold = OnHoldEffectiveUnix > now` (single choke point; root threads unchanged
  since eff==own when no parent).
- grid.handleThreadGrid: computes effHolds over the FULL set (ListThreads(true) even when
  the view hides archived, so a child's hold resolves through an archived ancestor) and
  passes effHolds[id] into resolveRow (both the maintained fast path + the on-demand
  fallback set OnHold + OnHoldEffectiveUnix from it).
- tui: fetch() copies OnHoldEffectiveUnix; the HOLD column shows the EFFECTIVE date with a
  `↑` prefix when inherited (effective > own); the `h` toggle decides on the thread's OWN
  hold (own active → clear own; else hold-until-tomorrow) — you can't un-hold a child below
  its parent (the max), reflected on the next fetch; holdRow optimism: SET flips on_hold +
  hides; CLEAR does NOT (effective may still be held via a parent — let the ~300ms reconcile
  settle). H prompt prefill still uses the OWN date.
- conformance: thread.hold local cell EXTENDED — held parent → child with no own hold reads
  on_hold + effective==parent's + own==0; child's OWN later hold wins (max); releasing the
  parent leaves the child held by its own. (TUI claims unchanged — fold-fragile to assert a
  nested inherited child in the rendered tree; the cell proves the daemon derivation that
  drives the view filter, which is the observable effect.)
- sesh-cli SKILL: inheritance paragraph (max, ↑ marker, per-machine scope).
The sesh-ui hold ticket (8c2755d9, child thread 69096170) gets inheritance automatically
(reads on_hold); ticket prompt to be updated to also surface on_hold_effective_unix + the
own-vs-effective toggle semantics.
DEPLOY: schema 35 = daemon RESTART, all four (additive/mixed-mesh-safe).

## H25 — thread HOLD: park a thread until a date, default view hides held threads (2026-06-25, sesh c3b1c4f; api schema 33→34; deployed ALL FOUR)
Ticket "A way to put a thread on 'hold'" (5c670fdc): on a busy day, park the threads
you're NOT working on so `sesh tui`'s default view only shows the active few; tomorrow
they reappear. Design Q&A (AskUserQuestion) locked TWO decisions: (1) the relocation
scheme — Lukas corrected his own ticket ("l not j"), so the column-pan pair `h`/`l` →
`^h`/`^l` (frees h/H; j/k + ^j/^k scroll unchanged); (2) AUTO-EXPIRY semantics — a hold
is a DEADLINE, not a latch.
MODEL: `on_hold_until_unix` on the thread record (absolute instant; 0 = not held). "On
hold RIGHT NOW" is DERIVED, never stored: `on_hold_until > the OWNING daemon's clock`,
stamped as `on_hold` on ThreadRow/ThreadSnapshot. So a hold auto-expires once the instant
passes (no explicit unhold) — `h` defaults the deadline to start-of-tomorrow, so a parked
thread returns to the active view the next day on its own. The daemon is a pure SETTER
(date math / "tomorrow" is UX → lives in the TUI/CLI, computed against the VIEWER's clock
= the user's own tomorrow); a PAST instant stores fine and reads not-held.
- api (schema 33→34, additive/mixed-mesh-safe): `on_hold_until_unix` (Thread), derived
  `on_hold` (row+snapshot), `POST /v1/threads/hold` (HoldThreadRequest{id,until}),
  client.ThreadHold. A pre-34 daemon 404s the route LOUDLY + omits on_hold (read not-held).
- store migration 18 (the list's 18th ELEMENT — migration-"4" comment spans 2 ALTERs, so
  store version = 18 though the comment says "17"): `ALTER TABLE threads ADD COLUMN
  on_hold_until` (APPENDED last); SetThreadHold + all column lists. Unit TestThreadHold.
- daemon: handleThreadHold; maintainer.publish stamps OnHold = until > now() (single choke
  point, mirrors TicketNeedsInput); grid.resolveRow stamps it on BOTH the maintained +
  fallback paths (independent of the snapshot so the fallback is correct too).
- cmd: `sesh thread hold (--until YYYY-MM-DD | --until-unix <n> | --clear) [--id]
  [--machine]` — exactly one deadline flag; --until parses start-of-day local; current-
  thread inference like notify; routes cross-machine. help.go + help_flags.go.
- tui: NEW built-in view ViewHold ("on hold") between active and archived. ViewActive
  (default) now = `!archived && !onhold`; ViewHold = `!archived && onhold`. New helpers
  builtinViewAdmits + leavesViewWith REPLACE leavesCurrentView (generalize membership +
  optimistic-hide across both axes). Keys: `h` = toggle hold (park to start-of-tomorrow;
  release if already held), `H` = explicit-date line prompt (promptHold; empty clears).
  rowPatch gains an `onHold` overlay (optimistic flip + hide when the change leaves the
  view). Opt-in `hold` column (on-hold-until date). predicate language gains `onhold`
  selector + bare atom. Legend updated. KEY GOTCHA (bit me): the column-pan keys `h`/`l`
  were the ones to move — Lukas's ticket said `j` but meant `l` (clarified). `^h`=BS and
  `^j`=LF terminal codes, but bubbletea reports them distinctly so it's fine; only `^h`/
  `^l` are used (the existing `^j`/`^k` scroll is untouched, which is why moving `l` not
  `j` matters — moving `j`→`^j` would have collided with scroll-down).
- conformance: feature thread.hold (AgentAgnostic × Local+Remote). HONEST proof = the
  derived on_hold flipping BOTH directions vs the owner's clock (future→on, PAST→off =
  the auto-expiry; the bug-class a one-directional check misses, cf the codex liveness
  bug) + --clear zeroes it + routed hold lands & derives on the peer over a real ssh hop.
  TUI claims action-hold (h parks on the daemon + leaves active view, h releases, H date
  prompt) + view-hold (default hides on-hold, `on hold` view is the complement). NB the
  TUI claim list `declaredTUIClaims` (tui_test.go) is HARDCODED — registerTUIClaim only
  BINDS; a new claim must ALSO be added to declaredTUIClaims or it silently never runs
  (TestTUIClaimsComplete only checks declared→bound, not the reverse). Bit me once.
- sesh-cli SKILL: keymap (h/H, ^h/^l), the `on hold` view, the hold CLI verb, onhold kw.
DEPLOY (schema 34 = daemon RESTART): mymain (build .new+mv + supervisorctl restart) +
macstudio (cij@macstudio, git pull + native build + restart) + termux (lukas@android-main
:8022 — git pull, PLAIN `go build` CGO=1/android per H22, .new+mv, kill daemon by explicit
PID, setsid-nohup relaunch with SESH_HOME=~/.sesh SESH_MACHINE=termux sockets sesh/
sesh-master). All three verified api schema 34 / store 18; live-smoked hold set+clear on
mymain + termux (headless record needs no working agent); mymain↔macstudio mesh synced.
macbook was OFFLINE during the initial deploy (ssh :22 timed out; mesh "last seen 520s
ago") but came online shortly after and was deployed the same way (git pull + native
build + supervisorctl restart sesh-daemon) → schema 34, mesh synced (all three peers
"synced 0s ago" from mymain). So ALL FOUR are on schema 34. (Schema 34 is additive/
mixed-mesh-safe, so the brief skew while macbook lagged was harmless.)
Ticket 5c670fdc (on mymain) marked done. GOTCHA: `ticket get/find --id` match the EXACT
id, NOT a prefix (unlike most verbs) — use the full uuid.

## H24 — busy never latched at SCALE: maintainer re-ran Info+ps PER THREAD (2026-06-25, sesh 8fbaa07; NO schema change; deployed ALL FOUR)
Ticket "In sesh tui, it doesn't show threads as running when they are (especially for
remote)": claude threads stopped rendering busy. ROOT CAUSE = a SCALE regression in the
state maintainer (internal/daemon/maintainer.go), not a logic bug. busy is content-diff
derived: a thread is BusyBusy once the maintainer sees >= busyChangesNeeded (2) pane-content
changes within busyWindow (2s), sampling every maintainerTick (300ms). The maintainer sweeps
EVERY local thread each tick, and PER THREAD it called FindPaneByThreadID (re-enumerates ALL
panes via one tmux Info) AND tmux.AgentUnderPane (re-runs `ps -eww` over the whole process
table) — both are tick-GLOBAL data recomputed once per thread. At Lukas's current load (94
threads / 46 panes / 770 procs) one full sweep measured ~3.3s (46 ps@~56ms + 94 Info@~8ms), so
each pane was sampled only ~once/3.3s → the 2-changes-in-2s window could NEVER fill → busy
pinned to idle forever. "Used to work" because the sweep was fast with few threads; remote
worse because the OWNING daemon's maintainer has the same stall PLUS mesh-sync latency.
DIAGNOSIS (cold-repro): this very thread read busy=idle while its claude pane animated every
300ms; a from-HEAD Go probe of the IDENTICAL Info→FindPaneByThreadID→CapturePane path saw
changed=true every tick → proved timing, not capture. Running daemon was already at HEAD on
socket `sesh` — so not stale code, just the O(threads) sweep.
FIX:
- internal/tmux/threads.go: Server.PaneIndexByThreadID() — threadID→PaneLocator from a SINGLE
  Info() enumeration (unit test TestPaneIndexByThreadIDMatchesFindPane proves it EQUALS the
  per-thread FindPaneByThreadID it replaces, across multi-session/window marked panes).
- internal/tmux/proctree.go: ProcSnapshot (NewProcSnapshot + AgentUnderPane method) — capture
  the process table ONCE, resolve many panes against it. Package AgentUnderPane kept for the
  many single-shot callers (thread.go/adopt.go/etc) — only the maintainer hot loop changed.
- internal/daemon/maintainer.go: tick() resolves the pane index + proc snapshot ONCE per tick
  and passes them into refreshThread (replacing the per-thread tmux calls); if either fails,
  skip the tick. refreshThread now also runs CONCURRENTLY via a bounded pool
  (maintainerConcurrency=8) — each thread's liveState is independent, only m.st is mu-guarded,
  capture-pane runs without the lock. Sweep collapsed ~3.3s → well under busyWindow.
NO api schema change (internal only; api.PaneLocator already existed) ⇒ mixed-mesh safe, no
restart-ordering hazard. Tests: tmux + daemon packages green; -race clean.
DEPLOY: ALL FOUR. mymain (build .new+mv + supervisorctl restart sesh-daemon), macstudio
(cij@macstudio) + macbook (lukas@macbook) (git pull + native build + supervisorctl restart),
termux (lukas@android-main:8022 — git pull, PLAIN `go build` CGO=1/android per H22, .new+mv,
kill old daemon by explicit PID, setsid-nohup relaunch with SESH_HOME=~/.sesh
SESH_MACHINE=termux SESH_TMUX_SOCKET=sesh SESH_MASTER_SOCKET=sesh-master from the zshenv
login-guard block — termux is an inbound-less leaf, no API token/conf). LIVE-VALIDATED: a
working claude thread now reads busy=busy while 45 idle threads stay idle (no false positives);
mesh fan-out healthy (mymain sees macbook+macstudio rows). KNOWN TEST GAP (told Lukas): the
existing thread.runtime-state cell tests busy with ONE thread so it could never catch this
scale stall; the new equivalence unit test guards the refactor but there's no matrix cell that
asserts per-tick tmux/ps calls stay constant as thread count grows. Ticket
c8108833 marked done.

## H23 — HEADLESS adopt: register an existing conversation as a headless thread (2026-06-24, sesh aa06f8c; api schema 31→32; deployed 3/4)
Ticket "Can't adopt headlessly": `sesh thread adopt --name X --session-id <uuid>` (no
pane) failed. ROOT CAUSE: `thread adopt` was ALWAYS pane-based — it inspects a live
work-server pane+agent. No pane → loud "a pane is required"; with $TMUX_PANE it adopted
the caller's shell pane → 409 "no coding agent". The H6 `--session-id` only ASSERTS the id
for an agent already live in a pane. There was no path to register a not-running
conversation. `thread new --headless` was closest but mints a NEW id, can't bind an
existing one. FIX (design decided w/ Lukas via AskUserQuestion): a pane-less MODE of
`adopt`, selected by `--agent` (meaningless in pane adopt). `--agent` + `--session-id`
REQUIRED (nothing to detect from); `--cwd` defaults to '.'. `--agent` suppresses the
$TMUX_PANE default so it never hijacks a headless adopt run from inside tmux.
- api: AdoptThreadRequest gains `agent_kind`+`cwd` (omitempty); empty `pane` = headless
  adopt instead of error. Schema 31→32 (additive; a pre-32 daemon rejects a pane-less
  adopt LOUDLY with 400 — mixed-mesh safe).
- daemon adopt.go: handleThreadAdopt branches empty-pane → adoptHeadless (ParseKind,
  expandHomeCwd, headless record SessionName "headless-<id>", no pane stamp,
  AgentSessionID=asserted id, HeadlessStarted=true so send-headless RESUMES). Pane adopt
  now rejects a stray --agent loudly.
- cmd/sesh: `thread adopt` grows --agent + --cwd; missing --session-id loud.
- conformance: NEW feature thread.adopt-headless (agentic × Local). Honest CONTINUITY
  proof: plant a codeword in a real headless source conversation, DELETE the source record
  (transcript survives), headless-adopt the session id into a fresh thread, send-headless
  and assert it recalls the codeword (would fail if adopt started fresh). Green claude/
  codex/pi (32s). help registry/flags + sesh-cli SKILL updated.
DEPLOY (schema 32 = daemon RESTART): deployed all machines (native build .new+mv + restart).
Live-smoked headless adopt on each incl. REAL-NETWORK routed adopt over http (record
headless/idle; negatives loud).

## TUI/CLI batch H1–H6 (2026-06-11; api schema 7→8; mymain daemon redeployed)
Six fixes from Lukas's feature list + two live requests. Each: research → impl →
unit/claim test → live-smoke.
- **H1 legend overflow**: the TUI keymap legend now WRAPS to width (`renderLegend` =
  `styleDim.Width(m.width)`) instead of clipping at the right edge — every binding stays
  visible. Variable height, so `scroll.go chromeLines` counts the wrapped line count
  (`legendLines()`), or the row budget drifts on narrow panes. Unit `TestLegendOverflowsNotClips`.
- **H2 reparent visibility bug — the ROOT CAUSE**: action errors lived in `lastErr`, which
  `meshMsg` clears on every successful fetch → a failed reparent (bad/self/cycle/unknown
  uuid) flashed and the reconcile fetch ERASED the warning within ms = "nothing happened,
  no warning". Fix: separate persistent `actionErr` (loud red ✗ line, survives fetches,
  cleared only by the next action). ALL action errors moved lastErr→actionErr (nav/stop/
  delete/tag/reparent/notify/archive); tests use `m.ActionErr()`. Daemon already refused
  self-parent + cycle + non-existent — they just weren't visible. claimActionReparent now
  asserts the warning PERSISTS across a render, + self-parent + non-existent cases.
- **H3 destructive confirm**: `d`/`a` open a y/n popup (`confirmKind`/`handleConfirmKey`);
  `y` runs it, any other key cancels. claims action-delete/archive press d/n (cancel keeps
  record) then d/y.
- **H4 relative --cwd**: `absCwd()` in cmd/sesh expands relative/`~` cwd against the
  invocation dir (filepath.Abs) for `thread new` + `delegate` (daemon still requires
  absolute). Cross-`--machine` still needs absolute (expands locally). Unit TestAbsCwd.
- **H5 cwd_label cross-machine — the real bug**: the labeler stripped the VIEWER's home, but
  a mesh thread carries the OWNER's absolute cwd → a different-home viewer (laptop→mymain)
  saw the raw `/home/lukastk/...` path, no label. Fix: the OWNING daemon's maintainer stamps
  `CwdRel` (= `config.TildeRelative(cwd, ownerHome)`) into every ThreadSnapshot/ThreadRow
  (api schema 7→8, additive omitempty; flows over BOTH http + ssh snapshot transports). The
  TUI's `cwdDisplay(row)` applies label rules to `row.CwdRel` (home = OWNER data, rules =
  VIEWER policy). Unit TestCwdDisplayUsesOwnerRelative; live-verified the daemon emits
  `cwd_rel=~/dev/...`. Local-only (same-home) was always fine — confirmed via live render.
- **H6 adopt --session-id**: `thread adopt` couldn't adopt a claude launched with a bare
  `-r` (no id in argv). Added explicit `--session-id <uuid>` to AdoptThreadRequest/client/
  CLI/daemon (bypasses per-agent detection; pane must still hold a live agent). USED IT to
  adopt THIS very Claude Code session (id 7e108848, claude session 9b8fccb0-…) once Lukas
  moved it onto the sesh work server (socket `sesh`, pane %0) — adopt is work-server-only,
  so it was impossible while the session ran on the `mysystem` server. Conformance
  thread.adopt claude branch gained the bare-claude-then-explicit-id case.
DEPLOY STATE (2026-06-11): committed c14ef12, pushed. Schema-8 daemons LIVE on ALL FOUR
machines — mymain, macstudio, termux, and macbook (caught up later the same day once it
came back online; HEAD 72eb901, API schema 8, mesh synced). Deploy recipe per machine:
git pull → native go build (.new + mv, ETXTBSY/codesign-safe) → restart (supervisorctl
restart sesh-daemon on mac/mymain; termux relaunched from shell, pid-guarded). The
cwd_rel field is additive/omitempty so a mixed-schema mesh stays safe during a rollout.
H5 PROVEN
LIVE cross-home: a macstudio box thread (cwd /Users/cij/dev/2026…__cwdrel-demo) rendered
as the box label "cwdrel-demo <zz9xcw>" in mymain's --all-machines TUI (was the raw path
before).

## H7 — `thread delete` now resolves an id PREFIX (2026-06-11, commit 88e6214; deployed mymain/macstudio/termux)
Found while cleaning up the H5 demo: `thread delete --id <prefix>` 404'd because
threadDelete was the ONLY single-id verb that passed the raw --id to the daemon's
exact-match lookup instead of resolving it (every other verb calls resolveThreadID; the
SKILL already promised "almost every --id accepts an unambiguous prefix"). Fix: resolve
via `resolveIDPrefix` (NOT resolveThreadID) — delete must still never INFER the current
thread (destructive + ambient = footgun), so an omitted --id stays the loud required
error, an explicit prefix now resolves, an unknown prefix is loud. Client-side fix (binary
only, no daemon restart). thread.delete cell (both localities) gained prefix + unknown-
prefix assertions; live-smoked on mymain. Deployed to all four machines.

## H8 — nav lands on the thread's WINDOW, not the session's last-active window (2026-06-11, commit 26c4395)
Lukas: entering a thread via the TUI went to the right tmux SESSION but not the right
WINDOW — open a 2nd window in a thread's session, leave, TUI-enter → landed on the 2nd
window, not the thread's. Root: every switch targeted `=session`, and switch-client to a
bare session lands on its LAST-ACTIVE window. Fix: resolve the WINDOW of the
@sesh-thread-id-marked pane at the SWITCH SITE (the owner's work server — the marker is the
truth there) and switch to `=session:window`. New optional `nav --thread <id>` threaded
through ALL paths: in-client (Go `threadWindowTarget` via `list-panes -s -f
'#{==:#{@sesh-thread-id},<id>}' -F '#{window_index}'`), master local+ssh
(`InnerSwitchScript` gained a threadID param — resolves in-shell; the no-client kick branch
selects the window too), master http (`NavRequest.ThreadID` → daemon `handleTmuxNav`),
attach (select-window before attach, local + over ssh in-shell). TUI passes `--thread
row.ID` on every Enter; PendingAttach carries it. Empty --thread = plain session nav
(unchanged — mt-enter-tmux-session + all existing nav cells byte-identical). KEY tmux fact:
`switch-client -t <paneid>` AND `-t '=session:N'` BOTH select the window (verified live).
Tests: new `tmux.nav-window` cell (in-client lands on the thread's window in a multi-window
session; no-`--thread` stays put) + `internal/tmux` test running the REAL generated
master-path script against a live tmux. All 7 nav cells + action-nav TUI claims green; live
in-client test moved a client window 1→0. Full suite 182/184 (the 2 reds are the known
master-current flake + a codex headless-send flake that PASSES in isolation — neither from
this change). DEPLOY: daemon RESTART needed (http nav handler reads ThreadID). Live on
ALL FOUR machines (mymain/macstudio/termux deployed first; macbook caught up + daemon
restarted once it came back online — HEAD 11eebdb, schema 8, mesh synced).

## H9 — TUI mouse-wheel scrolling (2026-06-12, commit 7ef6767)
Lukas: the TUI should respond to mouse scrolling, vertical + horizontal. Enabled mouse
reporting on the program (`tea.WithMouseCellMotion()` in cmd/sesh/tui.go) and added a
`tea.MouseMsg` case to Model.Update: wheel up/down → `scrollRows(±mouseWheelStep=3)`
(viewport scroll, cursor follows — same as ^k/^j, smaller step); wheel left/right →
hOffset pan (same as h/l). bubbletea v1.3.10 API = `msg.Button` ∈ {MouseButtonWheelUp/
Down/Left/Right}. Trade-off (documented in code + SKILL): mouse capture means
terminal-native drag-select needs Shift while the TUI is up. Tests: the existing
scroll-vertical/scroll-horizontal claims now ALSO drive real tea.MouseMsg wheel events
through Update and assert the same VOffset/HOffset move (no new cell — the wheel is just
another driver of the same offsets). Live-verified the TUI emits the SGR mouse-enable
sequences (?1002h/?1006h) and renders fine. TUI-only (binary) change — no daemon restart.
Live on mymain/macstudio/termux. macbook OFFLINE at first.
**Follow-up (commit 7d348ee):** Lukas — "doesn't work on termux" + "vertical scroll should
scroll between the SELECTED rows." Changed wheel up/down from `scrollRows` (viewport) to
`moveCursor(±1)`+ensureCursorVisible (move the SELECTION, viewport follows). This was the
real ask AND fixes termux: viewport-only scroll is a NO-OP when the grid already fits the
(small phone) screen, so it looked dead; moving the selection always does something.
Dropped mouseWheelStep. PROVEN end-to-end by injecting real SGR wheel bytes
(`ESC[<65;col;rowM` down / `<64` up) into a live TUI in BOTH a plain pane AND a
display-popup (the prefix+s path) on tmux 3.5a — selection moved row→row. KEY GOTCHA when
testing popup mouse: the SGR coords are SCREEN coords; an event in the popup's margin
(col 5) is NOT routed to the popup program — must be INSIDE the popup (col 30) → then tmux
3.5a forwards it. So mouse-in-popup WORKS (mouse on is in tmux.common.conf, sourced by
work+master confs). Remaining termux caveat (SKILL-documented, not a sesh bug): the Termux
terminal app captures two-finger touch-scroll for its own scrollback — a hardware mouse
generates real wheel events. Deployed 7d348ee to mymain/macstudio/termux.
**Follow-up 2 (sesh 88fcf90 + myrig 372d4b0):** Lukas — "horizontal doesn't work" + make
h/v sensitivity configurable. (1) HORIZONTAL: many terminals don't emit native
horizontal-wheel (btn 66/67), so added Shift+vertical-wheel as the reliable pan (kept
native wheel-left/right). bubbletea MouseEvent has `.Shift`. PROVEN live by SGR injection
into a clipped TUI: native wheel-right (btn 67) AND Shift+wheel-down (btn 69) both pan;
Shift+wheel-up (btn 68) pans back. (2) SENSITIVITY: `[tui] mouse_scroll_v/h` (notches per
step; 1=every notch default, higher=less sensitive). `wheelTick` accumulator (wheelAccV/H)
steps every Nth notch, resets carry on direction flip. config.LoadTUI validates (negatives
loud); Model.WithMouseScroll wired in cmd/sesh/tui.go. Tests: unit TestWheelTick,
TestMouseWheelSensitivityVertical, TestMouseWheelHorizontalPan + config TestLoadTUIMouseScroll
(coexists with [[tui.views]]/[[tui.column_color]]); scroll-horizontal claim asserts the
Shift+wheel path. myrig config.toml.jinja got an active `[tui] mouse_scroll_v/h = 2` (mild
dampening, before [[tui.views]] — TOML scalar-keys-before-subtables). DEPLOY: sesh binary
(TUI-only, no daemon restart) — horizontal works from the binary alone (shift+wheel, no
config). Config VALUES need the rendered config.toml updated: did it SURGICALLY (python
insert of the [tui] block before [[tui.views]], idempotent) on mymain/macstudio/termux
rather than a full install-home (lighter, no re-symlink). **macbook OFFLINE — pending both**
(last caught up to 11eebdb; needs binary + the config insert).


## THE WHICH-CLIENT LAW (2026-06-10, the deepest tmux lesson of this project)
tmux CANNOT map a popup pty, a pane pty, or a piped subprocess back to the attached
client that triggered it. `display-message -p '#{client_name}'` from any such context
is an AMBIENT pick — arbitrary under multiple clients (observed: it moved a master
supervisor's attach instead of the presser; it matched the presser in clean
experiments only by activity-luck, which is why the old cells passed). Also (tmux
3.5a): `display-popup` does NOT format-expand its shell-command OR -e values; the ONLY
expansion carrying the pressing client is a BINDING's own format context — i.e.
`run-shell "tmux display-popup -c '#{client_name}' -E '... SESH_NAV_CLIENT=#{client_name} ...'"`.
Hence the carrier contract (sesh commit on top of caa59fd): `nav --in-client` resolves
the client as (1) --client, (2) $SESH_NAV_CLIENT (baked in by the myrig popup
bindings), (3) $TMUX_PANE's session iff it has EXACTLY ONE client, else LOUD ERROR —
never an ambient guess; the switch is a Go-side `switch-client -c`. The TUI gets the
client via runTUI←$SESH_NAV_CLIENT→WithClient→`--client`. Master-path nav targets the
marker client (master-client.<origin> = "<tty> <pid>" written by each master window's
attach, liveness-checked by name+pid). Cells: tmux.nav-in-client(-multi) test the full
contract incl. carrier-less-ambiguous = loud + nobody moves; tmux.nav-master-multi;
TUI claims action-nav-quits + action-nav-in-client. `sesh master watchers` = live
markers ("who watches me") → mt-copy-to-master auto-detect.

### Configurable SESSION NAMING (2026-06-10, per Lukas; deployed)
`[[session_name]]` rules in `<SESH_HOME>/config.toml` (NEW file — sesh mechanism, myrig
owns the policy at `home/.sesh-v2/config.toml`): first-match cwd regex (matched
~-relative, portable) → template over named groups + {tid8}/{tid}/{name}/{cwd};
output sanitized for tmux (':' '.' → '_'; spaces/slashes/<> ARE valid session names —
verified). Applied at headed spawn + revival minting; no match → default sesh_<name>.
LOUD: broken file/regex/placeholder/empty refuses the daemon or spawn. Lukas's rules:
box root `{boxname} <{boxid}> ({tid8})`, box subdir `{boxname}/{rel} <…>`, mysetup
`mysetup/{rel} ({tid8})`, else `{path} ({tid8})`. Cell `thread.session-name` (Local) +
unit tests; live-verified all four rules on mymain. Daemon restart needed after config
edits. Dep added: BurntSushi/toml.

### RESOLVED: the claude-resume saga was an ENV LEAK (not a claude limitation)
When the daemon is started from inside an agent session (autonomous build / the
conformance suite under Claude Code), it inherited `CLAUDECODE=1` / `IN_CLAUDE_CODE` /
`CLAUDE_CODE_SESSION_ID` / `CLAUDE_CODE_*`. Those leak into the spawned claude, which then
behaves as a NESTED session and stops persisting its transcript to `~/.claude/projects`
— so `claude --resume` reports "No conversation found". Old sesh never hit this (its
daemon runs normally, not under a claude agent). **Fix:** `agents.ScrubHarnessEnv()` at
the top of `daemon.New()` unsets those markers (verified: propagates daemon→tmux→pane;
hard-killed claude then resumes with full continuity). My earlier "claude buffers until
graceful exit" theory was WRONG — it was the env leak masquerading as that. Verified by
driving real claude in tmux via the `tui-tmux-testing` skill.

codex resume edge: a codex thread killed BEFORE its first turn has no minted id →
explicit N/A error (TestCodexResumeBeforeFirstTurnIsNA), never faked.

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

## H10 — prefix+L "last window" toggle + master prefix+a/s swap (2026-06-13, sesh 73f198d, myrig bc98f70)
Lukas wanted (1) master prefix+a=sesh tui / prefix+s=tmux-session picker (swap; was a/s),
(2) prefix+L = jump to the last WINDOW he was on, cross-machine, including same-session
windows (sesh tui can move you between windows in one session). Native tmux last-window/
last-session can't express this (fragmented across the 3 cockpit layers). So sesh tracks
it: tmuxNav records the from-location (machine,session,window) into <home>/nav-prev on
every MASTER-path nav — resolved via the carrier ($SESH_NAV_CLIENT → master active window
machine) + routed `master-current` (extended to return the window). `tmux nav --last`
replays nav-prev (recording current in turn → toggle). Scope CONFIRMED by Lukas:
sesh-nav-anchored (a purely-native prefix n/p switch isn't tracked). Wire: schema 8→9
(MasterCurrentResponse.Window + NavRequest.Window as *int so a pre-window client that omits
it is "unset", not window 0). InnerSwitchScript + threadWindowTarget gained an explicit
window index (overrides thread resolution) so --last lands on the EXACT recorded window.
VALIDATED LIVE on an isolated master+work cockpit: alpha<->beta toggle AND same-session
(manually on alpha:1 → nav beta → prefix+L returns to alpha WINDOW 1, not the thread's
window 0). KEY: recording needs the carrier; the bind L uses run-shell (expands
#{client_name}), display-popup would NOT. Master conf is a SYMLINK into the myrig repo, so
a `git -C ~/mysetup/myrig pull` updates ~/.sesh/myrig/tmux.master.conf; the RUNNING master
still needs mt-reload-conf (or mt-kill && mt-start) for the new bindings. DEPLOY: schema 9 =
daemon restart. Live on mymain/macstudio/termux (sesh + myrig pulled, daemons restarted).
**macbook OFFLINE (asleep) — pending sesh 73f198d + daemon restart + myrig pull + master
reload.** Conformance: all touched cells pass (nav-window/in-client/api/mesh); the lone
master-current/-/remote FAIL is the known pre-existing flake (fails on clean HEAD all
session). The full suite's other 18 fails were claude-account flakes (every claude turn cell
— "source turns missing" — under load), NOT this change.

## H11 — mt-enter-box + master prefix+c (2026-06-13, myrig 53ab727)
Lukas: prefix+c fzf over ALL boxyard boxes, start/enter a tmux session in the box on the
machine where it lives (multi-machine → 2nd fzf). boxyard's local index has remote boxes,
so no remote poll for the list. mt-enter-box (shell.sh.jinja): `boxyard list
--output-format json` → per box, machines = `ctx/<machine>` groups ∩ {self + peers}; fzf
the 436 (of 481) boxes on a known machine; 2nd fzf if several; create-or-enter a plain
session named after the box at <home>/dev/<index> (index = ts_subid__name; boxdir is
ABSOLUTE — derived per machine: $HOME locally, else peers.json home minus /.sesh — because
`sesh tmux create-session --dir` does NOT expand ~) on that machine's work server, then
_mt_nav_to. bind c = run-shell+display-popup (carries $SESH_NAV_CLIENT). VALIDATED on
mymain: 436-box list, index→~/dev/<index> (matches real dirs), field extraction, machine
pick, peer-home derivation (macbook→/Users/lukas/dev), create-in-box-dir + existence-check
reuse. Per-machine box counts: macbook 431, macstudio 3, mymain 2 (45 boxes have no
known-machine ctx group → hidden, can't be entered). DEPLOY: shell.sh is a RENDERED jinja
(not symlinked) → needs `install-home.py` render per machine (no daemon/binary). master.conf
is symlinked → myrig pull updates it, but the RUNNING master needs source-file (prefix+c).
Done: all machines (myrig pulled + shell.sh rendered; termux uv→python3 fallback). termux likely
lacks boxyard (python deps) so mt-enter-box there would no-op; the master normally runs on macbook.

### H11 fix — prefix+c was wiped by `unbind c` (myrig d210881)
prefix+c did nothing on ANY master. ROOT CAUSE: the master conf's neutralize-defaults
block still had `unbind c` (originally to kill the default c=new-window), and source-file
runs TOP-TO-BOTTOM, so it ran AFTER the new `bind c` mt-enter-box and removed it. Fix:
drop the `unbind c` (the bind c override already supersedes the default). Verified:
removing it → prefix_c_bound flips 0→1. LESSON: when adding a `bind <key>` to a conf, check
the neutralize-defaults block for a stale `unbind <key>` below it. TERMUX DNS GOTCHA seen
while deploying: termux's git pull failed with "Could not resolve hostname github.com" — NOT
auth (the "access rights" line is git's generic fallback). The Termux:Boot **sshd-spawned
shell has no DNS**: network is up (ping 8.8.8.8 ok) and $PREFIX/etc/resolv.conf has
nameservers, but Android's bionic resolver takes DNS from the foreground-app network context
(net.dns1/2 empty for the sshd child), so public-name resolution intermittently fails over
ssh while the user's INTERACTIVE termux session resolves fine. It's intermittent (came back
on a later retry) — retry the pull, or pull interactively; do NOT hack the symlinked conf
file (leaves the repo dirty). Deployed d210881 + sourced on all machines; mt-enter-box lists
223 boxes on termux.

## H12 — mmt-*/mt-* command split (2026-06-14, myrig 9af58ee)
Lukas refactored the cockpit command namespace (mirrors V1 mms-/ms-): **mmt-*** = the
mymastertmux cockpit (sesh-master server): mmt-start/attach/kill/ensure/reload-conf +
clipboard relay mmt-send-clipboard/mmt-copy-to-master (+ helpers _mmt_clear_master /
_mmt_send_clipboard_and_paste / _mmt_clip_get_*). **mt-*** = the inner mytmux work server
(sesh): mt-enter-session/mt-enter-tmux-session/mt-enter-box (+ _mt_nav_to). mt-set-clipboard
→ sesh-set-clipboard (generic, not tmux-level; both ambiguous calls — reload-conf and the
clipboard relay — Lukas put under mmt). Alias -g groups: mmt / mt / sesh. Ref sites updated:
shell.sh.jinja, tmux.master.conf (R→mmt-ensure, P→_mmt_send_clipboard_and_paste, mmt-start
comment), termux widgets 0/1-master (mmt-start), mysetup-navigator SKILL + myrig AGENTS.md.
tmux.work.conf UNCHANGED (a/A bind mt-enter-session, stays mt). Deploy = render shell.sh
(install-home, it's a rendered jinja) + re-source the master conf on running masters. Live
on all four (macbook/termux/mymain masters re-sourced; macstudio no master). LESSON (bit me):
`git add -A` swept Lukas's uncommitted voice-agent-bridge/config.json edit into the refactor
commit — reverted it out (4aaf8ec) and restored it as an uncommitted local change on mymain.
ALWAYS stage specific files in myrig (it has live uncommitted local edits), never `git add -A`.

## H13 — multiple threads per tmux session (pane = thread identity) (2026-06-14, sesh 675aca8, myrig a584056; api schema 9→10; deployed ALL FOUR)
Lukas: "session_name shouldn't be unique — should be able to have multiple threads on
the same tmux session." Audit showed runtime identity had ALREADY migrated to the per-pane
`@sesh-thread-id` marker (maintainer/nav/probe all resolve FindPaneByThreadID); only
`stop`(=KillSession) and `resume`/`new`(=CreateSession-by-name) still treated the session
as the thread's exclusive handle, plus the `UNIQUE(session_name)` constraint. So the
constraint was vestigial — dropping it + making those two ops pane-level unlocks sharing.
This also fixed the adopt bug that triggered the discussion: adopting a 2nd agent into a
session that already had a thread hit a raw `UNIQUE constraint failed: threads.session_name
(HTTP 500)` (the pane-marker pre-check passed because the pane's marker was missing —
session/pane recreated after the original adopt — so the DB caught it instead of a clean
409). Changes:
- **store migration 12**: rebuild `threads` WITHOUT UNIQUE(session_name) (SQLite can't
  ALTER DROP CONSTRAINT → CREATE threads_new/INSERT SELECT/DROP/RENAME as ONE multi-
  statement element = atomic in its tx; modernc.org/sqlite runs multi-statement Exec).
- **stop → KillPane** (FindPaneByThreadID → kill the thread's PANE), not KillSession: a
  sibling sharing the session survives; last pane gone ⇒ tmux tears the session down, so
  1:1 is unchanged. Also fixes stop nuking extra windows in a thread's session.
- **adopt**: InsertThread BEFORE StampPaneThreadID (no stamped-but-rowless pane; rollback
  on stamp fail). Session collision gone; same-pane re-adopt stays a clean 409.
- **resume**: if the session still exists (sibling alive), revive into a NEW WINDOW
  (CreateWindowCmd); teardown kills only what it created (session if it made it, else the
  pane) so siblings are untouched.
- **placement** (NewThreadRequest.into_session/into_window/into_pane; ThreadResponse.
  launch_command/launch_env; schema 9→10): default = own new session; `--into-session
  <name>` = new window of an existing session; `--into-window <target>` = split a pane;
  `--into-pane <pane>` = REGISTER-THEN-EXEC — daemon records + marks the EXISTING shell
  pane and returns the agent command (no spawn); the CLI's `--exec` syscall.Execs it in
  place so the agent takes over the pane. KEY DESIGN (Lukas asked "why not just run the
  regular agent command?"): it DOES — register-then-exec runs the identical HeadedCommand
  the daemon would, just exec'd by the client because the pane already has a shell. The
  only sesh-added bits are the pre-minted `--session-id` (claude/pi; codex mints on first
  turn = its normal late-id behavior), SESH_THREAD_ID, the pane marker, and the record —
  exactly what makes a bare agent trackable/resumable (the morning's adopt saga). tmux
  CreateWindowCmd/SplitWindowCmd added. `--into-window`=split-beside is the "which window"
  knob — forced explicit because the detached daemon can't know "current".
- **myrig mt-enter-new-thread** (`-g mt`) + work-conf `bind N`: `exec sesh thread new
  --into-pane "$TMUX_PANE" --exec ...` — turns your current shell into a managed agent
  right here (vs mt-enter-session/-tmux-session/-box which NAV). Binding send-keys's it
  into the focused pane (must run IN the pane; it execs) so $TMUX_PANE is real.
- **conformance** (LOCAL, agent-agnostic, real pi): thread.placement (into-session own-
  window + into-window split topology), thread.placement-pane (register-then-exec: daemon
  doesn't spawn, returns command, then running it brings the agent up under the marked
  pane), thread.stop-shared (sibling survives, last-thread tears the session down),
  thread.adopt-shared (2nd agent into a shared session; same-pane re-adopt 409). LOCAL-only
  per the per-server-pane-id rule (thread.adopt precedent); routing covered by route.parity
  + thread.stop/remote. All 4 green; no regressions in stop/resume/adopt/new.headed (pi);
  store/api/cmd/tmux/config units + vet clean. SPEC §3 model rewritten, sesh-cli SKILL +
  help registry updated.
DEPLOY (2026-06-14): schema 10 = daemon RESTART. LIVE on ALL FOUR (mymain/macstudio/
termux/macbook — macbook was awake this time). Each: sesh git pull → native build (.new+mv;
mac auto-signs) → restart (supervisorctl restart sesh-daemon on mac/mymain; termux kill +
relaunch with full SESH_* env, the zshenv launch block is interactive-gated so `zsh -lc`
doesn't fire it — pass the env explicitly). myrig: pull → install-home (macs/termux need
`uv run --with jinja2`; system python3 lacks jinja2 on macs) → source-file tmux.work.conf on
the running work server (symlinked conf, bind N picked up live). Schema 10 is mixed-mesh-safe
(snapshot fields unchanged; into_*/launch_* are request/response-only, omitempty). LIVE
SMOKE on the real supervised mymain daemon: host + sib --into-session share one session, stop
sib → host pane survives + session intact, stop host → session torn down. PROVEN. myrig
staged SPECIFICALLY (shell.sh.jinja + tmux.work.conf only — Lukas's voice-agent-bridge/
config.json + .claude/settings.json stayed uncommitted local edits, per the H12 lesson).

## H14 — TUI latency fix + empty thread names + the command-menu/enter/mysetup batch (2026-06-14/15)
Two SESH commits + one help commit + a big MYRIG cockpit batch. All deployed ALL FOUR.
### sesh
- **TUI optimistic hide** (edef5aa): archiving/deleting a row in the TUI lagged ~1s (the row
  stayed until the next mesh poll refetched). Fix: `a`/`d` now optimistically drop the row
  locally on success (rowPatch hide), so it disappears instantly; the next fetch reconciles.
  Audited the other actions — nav/stop/tag/reparent/notify already patch or quit, so archive
  + delete were the only laggy ones.
- **empty thread names** (79a6467; binary-only, no schema bump): `thread new --name` is now
  OPTIONAL. Dropped the `req.Name == ""` reject in daemon `handleThreadNew` + the
  `*name == ""` guard in cmd `threadNew` (the name is DISPLAY-ONLY — the session name comes
  from a `[[session_name]]` rule / cwd, and `sanitizeName` already falls back to "thread").
  New `TestEmptyThreadName` (internal/conformance/emptyname_test.go, OUTSIDE the matrix — a
  focused regression). help.go usage + sesh-cli SKILL updated (`--name` marked optional).
- **CLI help** (f5e8a7e): a bare group command (`sesh thread`, `sesh ticket`, …) now prints
  the group's full `--help` instead of erroring; added `sesh help-tree` (the whole command
  tree). Pure help-layer, no behavior change.
### myrig (commits a3dffd6→73c271d; render-only deploy + source-file the live confs)
- **command palettes** (a3dffd6, 5965889, 31bd5b4): `mmt-menu`/`mmt-quick-menu`/`mt-menu`/
  `mt-quick-menu` bound `prefix+M` (group palette = `my -g <groups>`) / `prefix+m` (curated
  = `my --only <list>`) on BOTH the master + work servers. The lists/groups live in a NEW
  editable, symlinked `home/.sesh/myrig/menus.sh` (`MMT_MY_GROUPS`/`MT_MY_GROUPS` +
  `*_QUICK_CMDS`). KEY FIX (Lukas hit it twice): a `send-keys` menu typed the command into
  his prompt — the work `prefix+m` must run the pick IN a `display-popup -E` (carrying
  `SESH_NAV_CLIENT`, exactly like the master menu), NOT send-keys it. So a popup-interactive
  pick (fzf/TUI/agent) works; a print-and-exit can't change your pane from a popup. `my`
  gained multi-group `-g a,b` (comma-split) + curated `--only` in my_alias.sh; fixed a zsh
  stdout leak (a bare re-run `local NAME` on an already-valued var prints it — declare
  `_my_fzf` locals once up front).
- **enter split + reclassification** (3ebf98d): the cross-machine pickers are `mmt-*`, with
  a THIS-MACHINE `mt-*` twin each (`*-enter-session`/`*-enter-tmux-session`/`*-enter-box`).
  WHY the split matters: a work-server (mt) nav can't move you to another HOST — only the
  master path moves the marker client — so cross-machine enters MUST be mmt. Also renamed
  `mt-reload-conf-all`→`mmt-reload-conf-all`; **master status bar → blue** (`tmux.master.conf`
  `status-style fg=black,bg=blue`).
- **mt-enter-box was showing 2 boxes** (d577cdd): it filtered to the `ctx/<machine>` boxyard
  group (only 2 tagged) — but "checked out HERE" = the `~/dev/<index>` dir EXISTS, not the
  ctx tag. Switched the this-machine box filter to dir-existence (33 real on mymain); peers
  still use `ctx/<peer>`.
- **mysetup commands** (f767b58): `*-enter-mysetup` (pick a `~/mysetup` folder → tmux
  session) + `*-enter-new-mysetup-thread` (folder + agent + name → sesh thread; blank name →
  `mysetup - <folder>`), each mmt-(machine-first)/mt-(this-machine).
- **new-thread commands** (73c271d): `mt-enter-new-thread-here` (new thread in the CURRENT
  tmux session via `--into-session`; reads the live session + `$PWD` so it's pane-only — run
  it IN your pane, NOT in the popup quick menu) + `mt-`/`mmt-enter-new-thread-in-box` (pick a
  box this/any machine, agent + name → `thread new --cwd <boxdir>`). Empty names ride the
  sesh empty-name change above.
DEPLOY: sesh = binary build + daemon restart (edef5aa/79a6467 touch the daemon); help-only
f5e8a7e is binary. myrig = render shell.sh (rendered jinja; macs/termux need `uv run --with
jinja2`) + `source-file` the live confs (symlinked) for the new bindings. LIVE on all four
(mymain/macstudio/macbook/termux). Live-smoked: empty-name thread (all 4), enter-new-thread-
here (nameless thread into current session), enter-new-thread-in-box (session templated from
the box, not the empty name), box count (mymain 33 / termux 0 — 0 is correct, no checkouts).
PROCESS LESSON (re-confirmed): stage myrig files SPECIFICALLY — never `git add -A` (live
uncommitted local edits: voice-agent-bridge/config.json, .claude/settings.json).

## H15 — the ticket EDITOR feature (TUI K view + columns + mt/mmt cockpit) (2026-06-15, sesh 08189ed, myrig d0c87f4; api schema 10→11)
Lukas's checklist: a TUI tickets view + an editor, ticket name/needs-input columns, drop
`description`, mmt commands to copy-prompt/send/edit the current thread's tickets + a global
browser, `ticket list` of a given/current thread, retrieve a ticket's prompt. Design Q&A
(AskUserQuestion): per-thread ticket cmds = BOTH mt+mmt twins; editor = mechanism-in-sesh +
glue-in-myrig (NOT a sesh interactive command — mechanism/UX rule). The TUI K view is the Go
twin of the myrig shell editor, both over the same `sesh ticket` mechanism.
- **sesh mechanism** (3c8fbf8): `ticket get --id [--field id|name|prompt|status|thread|created]`
  (raw field = clipboard/agent path), `ticket set --id [--name][--prompt]` (flag.Visit ⇒ only
  passed flags apply; `--name ""` clears), `ticket delete`, `ticket list --current` (resolves
  the caller's thread via resolveThreadID and is expanded to `--thread <id>` BEFORE
  owner-routing, so it binds the CALLER's pane not the owner's). `description` DROPPED
  (migration 13 = `ALTER TABLE tickets DROP COLUMN`; api/store/cli scrubbed). Per-thread
  `ticket_name` (newest open) + `ticket_needs_input` (any active ticket on a headful·idle
  thread) on ThreadRow/ThreadSnapshot, computed by the OWNING daemon: maintainer derives
  needs-input in `publish()` (single choke point: st.hasActiveTicket && headful·idle), grid/
  maintainer use a new `OpenTicketDigests()` (count + newest-name + has-active). Schema 10→11
  (additive/omitempty ⇒ mixed-mesh safe during rollout).
- **TUI** (a7df56f): `internal/tui/tickets.go` — full-screen takeover on `K`: list → drill into
  a ticket → name/prompt edit in $EDITOR (tea.ExecProcess suspend→save), status picker, thread
  (re)bind picker (fzf-style, search name/uuid), send-prompt, delete (y/n). ALL ops EXEC
  `sesh ticket …` (owner-routed; m.client would hit the maybe-non-owner local daemon). Two
  opt-in columns `ticket_name` + `ticket_input` (TKT!). `--editor` flag + `[tui] editor` config
  (precedence flag → config → $EDITOR → loud). KEY BUG caught by the claim: the mesh
  snapshot→row conversion in fetch() dropped the new fields → columns empty cross-machine; fixed.
- **conformance** (08189ed): ticket.get/ticket.set/ticket.list-current cells + tickets-view/
  tickets-columns TUI claims (the K view's status-change + delete land on the daemon; the
  ExecProcess editor isn't driven headlessly — its save path is `ticket set`, a green cell).
- **myrig** (d0c87f4): `_mt_current_thread` (pressing pane via $SESH_MT_PANE / `sesh tmux
  current`) + `_mmt_current_thread` (active master window's machine via $SESH_MT_MASTER_MACHINE /
  `sesh tmux master-current`); shared `_mt_ticket_editor` (fzf attribute → vim/picker →
  `sesh ticket set`/`set-status`); commands mt-/mmt-ticket-copy-prompt/-send/-edit +
  mmt-ticket-browse (global status-filtered). Work prefix+M/m now bake SESH_MT_PANE; master
  prefix+M/m became run-shell+display-popup to bake SESH_NAV_CLIENT + SESH_MT_MASTER_MACHINE
  (the carriers). menus.sh quick lists + config.toml.jinja `[tui] editor = "vim"`.
- CONCURRENT-WORK NOTE: while building, another agent pushed dbdd189 (SKILL parent-inference
  loudness) + 9f61f55 (TUI delayed post-action reconcile). REBASED my 3 commits on top; the
  only real conflict was the SKILL Tickets/Parent bullet (kept both); model.go auto-merged
  (their reconcileMsg + my ticket cases coexist). Full build/vet/tests green post-rebase.
DEPLOY (schema 11 = daemon RESTART): all machines (live-smoked create/get/set/delete — no
`description` in output, migration 13 clean). **macbook had a local uncommitted menus.sh edit
(mt-enter-new-thread-here in MT_QUICK_CMDS) — stashed → pulled → re-applied → re-rendered, so
his customization survived** (the recurring "stage myrig files specifically" lesson).

### Follow-up: create-a-ticket from the editors + termux caught up (sesh 9e96522, myrig 73d072a)
Lukas: add "create new ticket" to the various editors. (1) TUI K view list: `n` →
ticketNewPrompt sub-mode (type a name) → `createTicket` (exec `ticket create --json`, parse
id, `set-status active --thread`) so the new ticket is bound to the thread and joins the
list. tickets-view claim extended (n + typed name lands a bound active ticket). (2) myrig:
`_mt_pick_ticket <thread> [allow-new]` prepends a `＋ new ticket` row (id `__NEW__`) — present
even with zero tickets; `_mt_ticket_edit` (mt/mmt) offers it and creates BOUND to the current
thread, `mmt-ticket-browse` offers `＋ new ticket (unbound)` (global = no current thread); new
`_mt_ticket_create [thread]` helper (read name → create [+bind active]). No daemon API change
(reuses create/set-status) ⇒ for schema-11 machines this is BINARY+myrig only, NO daemon
restart. DEPLOY: mymain/macstudio/macbook (binary + render + source, no restart); **termux got
the FULL H15 (was still schema 10) + this** — pulled (DNS-retry guard), native android build,
**daemon relaunched the termux way** (pkill 'sesh daemon run' + `SESH_HOME/MACHINE/TMUX_SOCKET/
MASTER_SOCKET setsid nohup ~/.local/bin/sesh daemon run` per the zshenv launch block — NOT
supervisor), migration 13 clean, live-smoked create/get --field/no-description/delete. GOTCHA:
termux `/tmp` is UNWRITABLE — use `$TMPDIR`/`$HOME` in remote smokes. macbook's local
uncommitted menus.sh edit (his mt-enter-new-thread-here) did NOT block this pull (the create
commit only touched shell.sh.jinja, not menus.sh). ALL FOUR now on the ticket-editor feature.

### Follow-up 2: live drive of EVERY ticket feature → 3 real bugs fixed (sesh 3547922, myrig 3b1b952)
Lukas hit `bind new ticket: … bound thread not found: 7e108848 (HTTP 400)` creating a ticket
and asked me to actually exercise everything. Set up an ISOLATED real daemon (own SESH_HOME/
sockets, real pi thread, fake $EDITOR script) and drove the real `sesh tui` K view in tmux +
the myrig helpers. Found + fixed THREE real bugs the conformance claims missed:
1. **Cross-machine ticket routing (sesh 337a56d, the reported bug).** SESH_TICKET_OWNER is
   EMPTY → tickets are LOCAL to a daemon; a ticket binds to / is validated against its
   thread's daemon. The TUI ticket ops (loadTickets/ticketAction/createTicket/applyTicketEdit)
   hit the LOCAL daemon, so acting on a thread that lives on ANOTHER machine (viewing it via
   the mesh) → the bind validates in the wrong store → 400. Fix: `m.ticketArgs()` appends
   `--machine <ticketThread.Machine>` when remote (mirrors routedVerb). New `tickets-view-remote`
   claim (TUI creates+binds on an ssh-localhost peer's thread; asserts it lands on the PEER,
   local holds 0). The myrig twin: resolvers echo "tid<TAB>machine", `_mt_route` builds the
   --machine arg threaded through every helper; mmt-ticket-browse fans out across machines.
2. **Space/paste dropped in the new-ticket name prompt + thread-pick query (sesh 3547922).**
   handleTicketNewKey/handleTicketThreadPickKey matched on `msg.String()` and appended only
   when `len(runes)==1` — so a SPACE (String()=="space") and any paste (multi-rune) were
   silently ignored. The claim passed because it typed single chars. Fix: the established
   `switch msg.Type { case KeyRunes: append msg.Runes…; case KeySpace: ' ' }` pattern. Live-
   verified "fix the OAuth flow" registers verbatim.
3. **myrig `_mt_ticket_create` prompt polluted the returned id (myrig 3b1b952).** It printed
   "New ticket name: " to STDOUT, which the callers capture via `$()` for the new id → the id
   became "New ticket name: <uuid>" → 404. Fix: prompt to STDERR (`print -nu2`); only the id
   on stdout.
LIVE-VERIFIED in the K view: create (n, with spaces), edit name/prompt via the $EDITOR
SUSPEND (tea.ExecProcess — the untested path; fake editor wrote a marker, TUI suspended→
saved), status picker, thread-rebind picker + search, delete (daemon confirmed 0). myrig
(non-interactive w/ fake fzf): _mt_route (remote→`--machine x`, local→empty), _mt_pick_ticket
±＋new sentinel, _mt_ticket_create bound-active, browse fan-out formatting. KNOWN NON-ISSUE:
send-prompt on the isolated thread hit `tmux: list-panes line has N fields, want 13` — a
PRE-EXISTING internal/tmux pane-parse error (thread status/pane fail identically; untouched by
me) triggered by a DEGRADED pi pane (pi not functional in the isolated env → π-glyph/control
bytes in pane_title); healthy threads work (ticket.send-prompt cell passes). The ticket layer
surfaced it loudly (correct). DEPLOY: binary + myrig only (TUI/CLI changes, no daemon API
change → NO daemon restart) — mymain/macstudio/macbook/termux all on 3547922 + 3b1b952.

## H16 — cross-machine ticket binding via RELOCATE + ticket-cockpit UX (2026-06-15, sesh d62a64e [pre-rebase 2a77f31], myrig 3602606; api schema 11→12; deployed ALL FOUR)
Lukas hit "no threads to bind to" pressing the `thread` item on a triage ticket in
mmt-ticket-browse, + asked for: a parent-thread column in the ticket fzfs, list ALL active
threads to bind to, a `thread (by uuid)` item, and a `remove from thread (current: …)` item.
ROOT CAUSE (architectural, surfaced to Lukas not hacked): tickets are machine-LOCAL — the
ticket↔thread live join (needs-input, TKT-NAME/TKT-! cols) is computed per-daemon
(OpenTicketDigests + maintainer join LOCAL threads), and `set-status active --thread`
validates the thread in the ticket's OWN store. So a ticket can ONLY bind to a thread on its
own daemon; the empty picker happened because the ticket lived on a thread-less machine (the
master box) while the threads were elsewhere. Decided WITH Lukas (AskUserQuestion): keep
co-location, make cross-machine binds RELOCATE the ticket to the thread's machine. Detach/
move-invalidated status → `ready` (Lukas's call; both triage+ready are unattached by design,
ready = prompt-final, and a detached active ticket's prompt is presumably final).
### sesh (schema 11→12, additive endpoints ⇒ mixed-mesh safe during rollout)
- api: ImportTicketRequest + UnbindTicketRequest. store.UnbindTicket(id) = thread_id NULL +
  active→ready (CASE; other statuses preserved). InsertTicket already preserves a supplied id.
- daemon: POST /v1/tickets/import (land a full record PRESERVING id; binding dropped, active→
  ready on arrival; colliding id = loud 409, never silent overwrite) + POST /v1/tickets/unbind.
- client TicketImport/TicketUnbind; cmd `ticket import` (reads the record as JSON on STDIN, e.g.
  from `ticket get --json`) + `ticket unbind --id`. A cross-machine MOVE is the composition
  `ticket get --machine SRC --json | ticket import --machine DST` → `ticket delete --machine SRC`
  → `set-status active --thread` on DST. help.go/help_flags.go/help_test.go + sesh-cli SKILL
  (status model + co-location rule + relocate recipe). do-tickets SKILL unchanged (status model
  there still accurate; import/unbind aren't part of the agent find→read→report loop).
- conformance (honest, real ssh hops): ticket.unbind (agent-agnostic × both loc — bind active→
  unbind→thread cleared + active→ready, text untouched, unknown id loud) + ticket.move (Remote —
  two real daemons + a client peering with both: active-bound ticket on A read→imported onto B
  [same id, unbound, ready, text preserved]→deleted from A→re-bound active to B's thread; gone
  from A; colliding re-import refused). Both green. matrix now 196 cells.
### myrig (the 4 asks, all on the new mechanisms)
- mmt-ticket-browse PARENT column: thread-id→name map from `thread grid --all-machines`; each
  row shows the bound thread's name (— unbound / <id8> bound-but-not-in-grid). KEY zsh BUG fixed:
  the ticket-list jq must emit thread_id with a "-" SENTINEL for unbound — an empty field
  COLLAPSES under zsh's IFS-whitespace tab-merging in `IFS=$'\t' read` and shifts every column.
  Also dropped a `local tcol` inside the subshell while-loop (the "bare local prints the var"
  gotcha). 
- `thread` bind item now lists ALL active threads across EVERY machine (_mt_pick_thread over
  `thread grid --all-machines`), fixing the bogus "no threads to bind to"; binding a thread on
  another machine relocates the ticket first (_mt_bind_ticket → _mt_ticket_move).
- new `thread (by uuid)` item: prompt a uuid/prefix, _mt_thread_find resolves across the mesh,
  LOUD on zero or >1 match, then bind (relocating if cross-machine).
- new `remove from thread (current: <name> <id8>)` item (shown only when bound; label via
  _mt_thread_label) → `sesh ticket unbind`.
- helpers: _mt_ticket_move/_mt_bind_ticket/_mt_pick_thread/_mt_thread_find/_mt_thread_label;
  editor menu built dynamically. _mt_pick_ticket (per-thread) left alone (parent col redundant —
  same thread). 
DEPLOY (schema 12 = daemon RESTART): ALL FOUR. mymain/macstudio/macbook native build (.new+mv;
macs auto-sign) + supervisorctl restart sesh-daemon; termux build to ~/.local/bin/sesh.new (/tmp
UNWRITABLE) + pkill 'sesh daemon run' + setsid nohup relaunch with explicit SESH_HOME/MACHINE
(termux)/TMUX_SOCKET=sesh/MASTER_SOCKET=sesh-master (read from /proc/<pid>/environ). myrig render
via install-home (macs/termux `uv run --with jinja2`); macbook had local uncommitted edits
(settings.json/.env/menus.sh) → git stash → pull → stash pop (preserved). LIVE-SMOKED: bind→unbind
round-trip (mymain); REAL cross-network move mymain→macstudio (create→import→delete→bind active to
macstudio's thread; gone from mymain) + cross-machine unbind (active→ready); PARENT column shows
the bound thread's name for a real ticket. macbook grid was momentarily empty during smoke (used
macstudio as the move target).

## H17 — TUI rename cursor, `--cwd` default, `tmux kill-session`, cockpit menu/kill-empty/ticket-new (2026-06-15, sesh cc0baa6 schema 12→13, myrig e5a112b; ALL FOUR)
Six-item batch from Lukas.
### sesh (schema 12→13: one additive endpoint)
- **TUI rename in-place editing**: Model.promptCursor (insertion point 0..len). handlePromptKey
  gained ←/→ (^b/^f), Home/End (^a/^e), Delete, and INSERT-at-cursor (was append-only);
  Backspace deletes before the cursor. `r` prefills the name with cursor at end. New
  renderPromptInput draws a block cursor at its position (model.go:1534). Unit
  TestPromptInPlaceEditing + LIVE-driven in a real tmux TUI (insert mid-name, Home jumps).
  Covers tag/parent prompts too (shared handlePromptKey).
- **`thread new --cwd` defaults to '.'**: was a hard "required" error. Default applied only when
  cwd is empty AND not --into-pane (inherits pane cwd) — fork still defaults to the source's
  cwd (set earlier). flag/help/help_flags/SKILL. Live: a no-`--cwd` headless thread took the
  invocation dir.
- **`sesh tmux kill-session --target <name>`** (NEW routed verb): daemon → tmux.KillSession on
  the work server; non-existent session = loud 409. api.KillSessionRequest, client, handler +
  route, cmd dispatch, help/help_flags/help_test, SKILL. Conformance tmux.kill-session (agent-
  agnostic × both loc, real ssh remote) — create→kill→assert gone + non-existent loud. The
  mechanism behind myrig kill-empty-sessions. SchemaVersion 12→13 (additive; mixed-mesh safe).
### myrig (e5a112b)
- **Quick menus one-per-line**: my_alias.sh `my --only` now splits on newline AND comma
  (`${only//$'\n'/,}` then comma-split) and skips blank/`#`-comment lines. menus.sh
  MMT_/MT_QUICK_CMDS rewritten multi-line (+ the new commands). Backward compatible (comma
  lists still parse).
- **master prefix+A → `sesh tui` WITHOUT --filter** (prefix+a keeps --filter). Same popup/env as
  `a`. WAS `mmt-enter-session --archived` (archived-thread picker) — archived browsing still via
  the TUI's Tab. Sourced live on the macbook + mymain masters.
- **mt-/mmt-kill-empty-sessions**: kill work-server tmux sessions with NO non-archived thread
  (keep every session `thread grid` reports; kill the rest via `sesh tmux kill-session`). mt=this
  machine, mmt=every machine; prints each kill + per-machine count. GOTCHA: `sesh tmux info`
  emits JSONL and has NO `--json` flag (don't pass it). Dry-run on mymain correctly flagged 4
  real empties; did NOT bulk-kill the user's live sessions (left for the user to run).
- **mt-/mmt-ticket-new**: create a ticket for the current thread — prompt TITLE, then PROMPT,
  then STATUS picker with `active` FIRST (preselected); active attaches to the current thread
  (routed to its machine), else unattached. GOTCHA (bit me, caught in live smoke): `status` is a
  READ-ONLY special var in zsh — renamed the local to `st`. Live-driven end-to-end with a fake
  fzf + piped input: title+prompt+active → ticket created & attached.
DEPLOY (schema 13 = daemon RESTART): ALL FOUR. mymain/macstudio/macbook native build + restart
+ render; termux build to ~/.local/bin (/tmp unwritable) + pkill+setsid-nohup relaunch. macbook
had a LOCAL uncommitted menus.sh edit (his mt-enter-new-thread-here in MT_QUICK_CMDS) — my commit
REWROTE menus.sh, so: stash all 3 local edits → pull → `git checkout stash@{0} -- settings.json
.env` (restore the non-conflicting two) → drop stash → python-insert his `mt-enter-new-thread-here`
after `mt-enter-new-thread-in-box` in the NEW multi-line format. (macOS `sed -i '' 'a\'` mangles
through ssh+zsh quoting — use a python insert.) Live-smoked: --cwd default, kill-session (local +
routed mymain→macstudio + loud 409), ticket-new full flow, rename cursor in a live TUI, menus
parse with no unknown-command warnings. PROCESS: staged myrig SPECIFICALLY (his settings.json +
voice-agent-bridge/config.json stayed local), amended the myrig commit for the status→st fix
before pushing.

## H18 — the BLOB store: files/images in prompts + daemon-coordinated `ticket move` (2026-06-15, sesh 16a6bd9, myrig 6320b83; api schema 13→14)
Lukas wanted images (and any file) in prompts. Design Q&A converged on: a content-addressed
blob store + inline reference TOKENS in prompts that expand to full paths on send/copy, the
move carrying referenced blobs. "What does it mean to include an image in a prompt" → an image
can't ride a text channel; it becomes a FILE the agent reads via a path, and the model gets the
pixels (every agent reads images from a path: codex -i, pi @path, claude Read-tool).
### Token & store (internal/blobs — pure filesystem, NO db/schema)
`<SESH_HOME>/blobs/<sha256>/<name>` — hash dir = content address (dedup), file keeps its name
(real extension for the agent). Token `@blob(<hex-prefix>)` (12-hex prefix of the content hash
— STABLE across machines: same bytes→same hash→same prefix; resolved by prefix). Format chosen
to dodge Lukas's tools: NOT `[[ ]]` (Obsidian) or `{{ }}` (jinja). Escape: `@@blob(…)` → literal,
unexpanded. Expand() = loud error on a token resolving to no blob / ambiguous prefix (never a
silent passthrough). References() lists a prompt's tokens (for the move). Unit-tested.
### sesh
- daemon `/v1/blobs` add|list|get|delete|path|expand (GET get streams raw bytes + X-Blob-Name
  header). d.blobs = blobs.New(home). CLI `sesh blob add <path>|--stdin --name | ls | get | rm |
  path | expand` (routes per --machine like tickets; add prints the @blob token on stdout,
  summary on stderr). schema 13→14.
- EXPANSION wired into send: ticket send-prompt, thread send, send-headless all call
  d.expandPrompt(text) before delivery (co-located ⇒ local blobs); a missing blob = loud 400
  (never a dangling token typed at the agent). Copy = myrig pipes `sesh blob expand`.
- `sesh ticket move --id --to [--from]` (first-class, DAEMON-COORDINATED — the principled
  choice Lukas asked for: cross-daemon movement is the daemon's job, NOT a CLI script). The
  INVOKED daemon is the HUB: it pulls the record + every @blob() the prompt references from
  --from and pushes them to --to over its OWN peer transport (http client or ssh hop — same
  machinery as fanout/meshsync), then deletes the source. Only the hub must reach both ends;
  SRC and DST need not peer. NEVER deletes the source unless the push fully succeeded (a
  duplicate is content-addressed + recoverable; data loss is not). move.go has per-machine
  helpers (self=local store / http=client / ssh=shell-out) for getTicket/importTicket/
  deleteTicket/getBlob/addBlob. importTicketLocal factored out of the import handler. Replaces
  the H16 `_mt_ticket_move` get|import|delete glue.
- conformance (real ssh): blob.store + blob.expand (agnostic × both loc); ticket.move REWRITTEN
  to a 3-DAEMON HUB model (coordinator that is neither SRC nor DST, only it peers with both)
  moving a ticket whose prompt references a blob — asserts record landed (id preserved, active→
  ready), blob CARRIED (resolves + token expands on DST), gone from SRC, re-move loud.
  TestSendExpandsBlobReferences (regression, outside matrix): send-headless w/ unknown token =
  loud. Runner gained RunStdin. matrix now 202 cells. help registry/flags/meta-test + sesh-cli
  SKILL (new "Blobs & files in prompts" section) updated.
### myrig
- `_mt_ticket_move` now delegates to `sesh ticket move`. `_mt_ticket_copy_prompt` pipes through
  `sesh blob expand` (routed to the ticket's machine) so copied prompts have real paths. New
  `_mt_ticket_attach <id> <machine>` + "attach file/image" item in the ticket editor: clipboard
  image (reuse _mmt_clip_get_image) or a file path → `blob add --stdin` ON THE TICKET'S MACHINE
  (piped bytes — `blob add <path> --machine` would read the path on the WRONG host) → append the
  @blob token to the prompt.
DEPLOY (schema 14 = daemon RESTART): all machines. Schema 14 is additive/mixed-mesh-safe: a move
TO a schema-13 daemon fails LOUDLY (404 on blob add, source intact) — PROVEN live (the move
aborted before delete when a peer was briefly still on 13).
SELF-TEST (every feature, live): blob CLI full round-trip (add/dedup/ls/get/path/expand/escape/
missing-loud/rm); a REAL claude headless turn READ an image via a @blob-referenced prompt —
expanded the token to the blobs path and its VISION reported the image's text "BLOBVISION-7391"
(generated via uv+pillow); send-headless missing-blob loud; cross-network move mymain→macstudio
carried the blob (bytes + token-expands-on-DST) and removed it from mymain; myrig attach(file)+
copy-prompt-expansion composed end-to-end. Full blob+ticket conformance suite green (95s, real
ssh+agents). GOTCHAS: termux /tmp UNWRITABLE (build + logs → $HOME); termux daemon needed a hard
pkill -9 + socket rm to drop a stale schema-13 instance before the schema-14 one served blobs.

## H19 — sesh API for the ticket-note rewrite: mesh-wide `ticket find` + `closed_at_unix` (2026-06-16, sesh 32d8263; api schema 14→15; deployed ALL FOUR)
The 2 sesh-side blockers for the Obsidian ticket-note rewrite (design + ALL decisions locked
in `~/mysetup/mysystem/_dev/TICKET_NOTE_REWRITE.md`, mysystem e5a9564; the note becomes a
sesh API client, sesh knows nothing about notes). Everything else is plugin-side (NOT started).
1. **Mesh-wide ticket lookup** `GET /v1/tickets/find?id=<id>` (`internal/daemon/ticketfind.go`).
   Tickets are per-daemon; the note must resolve a ticket-id without knowing its machine. The
   invoked daemon resolves its OWN store first, else fans out to every peer in PARALLEL — each
   answering local-only (`&local=1`, no recursion) over the peer's explicit http/ssh transport —
   first hit wins. Returns the ticket record + its owning machine + bound-thread
   {id,name,parent,machine} in ONE call. **found=false is a 200, NOT a 404** (a draft note has
   no ticket, a deleted ticket resolves to nothing — a legit state the note uses for validation);
   Unreachable[] surfaces peers the fan-out couldn't reach so a not-found is never silently
   incomplete. **DECISION (proposal left it open): LIVE fan-out, NOT cache-backed snapshot
   replication** — always-correct (reads the authoritative store), far less code, avoids
   threading ticket records through the ssh-JSONL snapshot transport + a cache-format/rollout
   hazard; fine at 4 machines (if poll-load ever bites, cache-backed is a clean follow-up).
   Wired: daemon handler+fanout, client.TicketFind, CLI `sesh ticket find --id [--local] [--json]`.
   On the SHARED router → automatically exposed over the TCP API behind the bearer token (the
   plugin's transport — verified `d.routes()` wraps routesTickets, apiSrv wraps d.routes()).
2. **`closed_at_unix`** on the ticket record (migration 14: `tickets.closed_at`). SetTicketStatus
   now takes the daemon's clock and stamps closed_at on the FIRST done/dropped transition,
   PRESERVES it across an idempotent re-set, CLEARS it to 0 on reopen (one SQL CASE; store never
   calls time.Now — daemon owns the clock, mirroring created_at). New `ticket get --field closed`.
TESTS: store unit (stamp/preserve/clear); conformance **ticket.find** (Remote, real ssh fan-out
hub→peer — carried thread context + closed_at + found=false; ✓ live) + closed_at folded into
ticket.set-status. Blast-radius gate (ticket|blob|mesh|route|api|daemon.mesh-read, 1200s) GREEN
108s — incl. the http-transport mesh cells that exercise the same fan-out path. (The FULL 203-cell
suite times out at the default 10m under the box's load — known, not a regression; ran the
blast-radius subset instead.) help registry/flags + sesh-cli/do-tickets SKILLs updated.
DEPLOY (schema 15 = daemon RESTART): ALL FOUR on 32d8263. mymain (native build .new+mv +
supervisorctl restart sesh-daemon; live-smoked find local-hit + closed_at + found=false),
macstudio=cij@macstudio + macbook=lukas@macbook (pull+build+restart), termux=lukas@android-main:8022
(build to ~/.local/bin/sesh.new — /tmp unwritable — + pkill 'sesh daemon run' + setsid nohup
relaunch with the env read from /proc/<pid>/environ: SESH_HOME/MACHINE=termux/TMUX_SOCKET=sesh/
MASTER_SOCKET=sesh-master/TMUX_CONF/API_TOKEN_FILE). mymain's PEERS are macbook+macstudio over
**http** (`:7878`) — so its find fan-out uses the http path. KILLER PROOF (real network): a ticket
created on macstudio resolved by `sesh ticket find` invoked on mymain → found=True machine=macstudio
unreachable=None (BEFORE the peers were upgraded the same find showed `unreachable:[macbook
macstudio]` — their schema-14 daemons 404'd the find route; the additive/mixed-mesh-safe rollout in
action). Nothing in myrig changed (no cockpit surface touched).
PLUGIN WORK — DONE (mysystem 73dcadf, rebased→706d43b; deployed + live-smoked on macbook
2026-06-16). The whole Obsidian ticket-note rewrite landed in the mysystem repo (NOT sesh):
sesh API client (`src/sesh/client.ts`, Obsidian requestUrl, no-CORS/mobile, local-first+fallback)
→ TicketNote rewrite (managed nested `sesh-ticket-data`, datestamp validation, prompt-from-body
w/ `# Prompt`, link→blob flattening + cycle detection, decorator/consolidation from cached status)
→ shared `src/ticket/actions.ts` (submit/attach/move/send/status/update-prompt/unsubmit/sync) +
materialize + create-thread modal → `TicketPanel.svelte` + sync-service (open + interval) →
commands (ticket-actions/create-ticket/create-inline-ticket; task-to-ticket retargeted; v1
deploy/revive/auto-deploy/_ticket-cli removed). 15 ticket unit tests + full mysystem suite (99)
green. LIVE SMOKE on macbook's running Obsidian: plugin loaded + all 11 ticket commands
registered; `ticket-sync` hit the sesh API over requestUrl and wrote the nested sesh-ticket-data
into the note's YAML (live round-trip); decorator tracked status triage 📥 → done ✅; `closed_at`
flowed daemon→find→note (closedAt stamped); needsConsolidation flipped true on done. Connectivity
(proposal §7) MET: macbook+macstudio expose the TCP API on `:7878` (the tailscale hostname, NOT
127.0.0.1 — the plugin's local endpoint is `macbook:7878`), shared SESH_API_TOKEN (identical sha
on both macs). Plugin data.json on macbook configured: sesh_api_token + sesh_local_endpoint=
macbook:7878 + sesh_fallback_endpoint=macstudio:7878 (backup at data.json.bak).
FOLLOW-UP DONE (mysystem 1ca6342): the deferred cwd pickers — the deploy-to-new-thread modal now
picks cwd from a boxyard BOX (cached BoxyardService, reads boxyard_meta.json + config off disk; no
CLI; resolves `<user_boxes_path>/<index>`) or a `~/mysetup` folder (Node fs readdirSync); a picked
path is local so it sets machine=local (no silent cross-machine wrong path; mobile no-ops the
pickers). FULL LIVE SMOKE on macbook's Obsidian (driving modals via the obsidian-CLI eval +
DOM-injection technique from AGENTS.md): box picker = 133 boxes resolving to /Users/lukas/dev/<i>,
mysetup = 9 folders; submit (name modal editable default=note-name-sans-datestamp, draft→live,
ticket-id + sesh-ticket-data written); materialize (inline [[md]] + ![[image]]→@blob upload, token
expands to a real blob path, no raw [[ left); set-status picker→ready; unsubmit (sesh ticket
deleted, note→draft); sync decorator triage 📥→done ✅ + closed_at + needsConsolidation. All smoke
artifacts cleaned up. macbook source pulled to 1ca6342 (matches the deployed main.js).
STILL NOT done: the plugin is installed only on macbook (where Obsidian runs); macstudio (fallback)
+ mobile would need the plugin + settings too if used there. Heavyweight paths not live-driven
(would spawn real agents): actually spawning a thread via attach-to-new + the cross-machine ticket
move — but threadNew is a documented sesh endpoint and `ticket move` was proven in the sesh phase.

## H20 — ticket-send fixes (newlines + prepend), frontmatter-corruption fix, stop-guard (2026-06-16; sesh cbccc24 schema 16, mysystem 16a2242; deployed ALL FOUR + plugin on macbook)
Surfaced by the Obsidian ticket note sending a multi-paragraph prompt via the panel Send button.
- **NEWLINES (sesh tmux.SendText)**: `send-keys -l` sent embedded `\n` as submitting Enters →
  multi-paragraph prompts fired line-by-line, structure lost. Multi-line now delivers via
  BRACKETED PASTE (set-buffer + paste-buffer -p): the agent buffers it as ONE input, trailing
  Enter submits intact. Single-line keeps send-keys (no change). Real-tmux unit test (bracketed
  paste preserves lines + nothing executed); all ticket.send-prompt cells (claude/codex/pi × loc)
  pass; LIVE-verified on a pi thread (3 paragraphs intact).
- **PREPEND (sesh)**: send-prompt prepends `Ticket "<name>" (<id>)\n\n` so the agent knows its
  ticket. Default = `[ticket] send_prepend` in <SESH_HOME>/config.toml (built-in ON);
  `--prepend`/`--no-prepend` override per call (SendPromptRequest.Prepend tri-state; config.LoadTicket).
  api schema 15→16 (additive request field, mixed-mesh safe — pre-16 daemon ignores it). LIVE: pi
  pane showed the header. Plugin: panel "Send raw" button + `ticket-send-prompt-raw` command (no header).
- **FRONTMATTER CORRUPTION (plugin)**: on note open the panel mount-sync + the sync-service
  file-open sync both fired; their async find()s returned together and both wrote sesh-ticket-data
  via processFrontMatter CONCURRENTLY → corrupt YAML (a DUPLICATED `sesh-ticket-data:` block in the
  user's note; processFrontMatter ERRORS on the dup → the note wouldn't parse in Obsidian). Fix: a
  per-note write QUEUE (serializeWrite) — all ticket frontmatter writes to one path serialize
  (sync/submit/unsubmit); + made sameSnapshot order-insensitive (it compared JSON.stringify, key
  order differs YAML-read vs freshly-built → churned every poll). Healed the user's note (python
  dedupe of the top-level key). VERIFIED: 6 rapid concurrent syncs → still 1 block, parses OK.
- **STOP-GUARD (sesh)**: `thread stop --id ""` (or omitted) resolved via resolveThreadID → INFERRED
  the current thread ($SESH_THREAD_ID/pane marker) → a stray empty id silently stopped the wrong
  session (bit a test script that stopped my own session — survived, it was interrupted). Stop is
  destructive (ends agent+session); now requires an explicit --id (loud "id required") + resolves
  via resolveIDPrefix, NOT resolveThreadID — same guard `thread delete` already has. thread.stop
  cell gained the empty-id loud assertion (pi cells green).
DEPLOY: sesh cbccc24 (schema 16 = daemon RESTART) on mymain/macstudio/macbook/termux (stop guard +
schema 16 verified on each). Plugin 16a2242 on macbook (pull+build+install+reload; panel shows
Send + Send raw; corruption fix verified). The user's note re-synced clean (status active).

## H21 — portable ~ cwd, tui ^k/^y, thread_archived + the ticket-dashboard feature (2026-06-16; sesh 7e6a888/0109197/0ccda7a schema 18→19, mysystem 84b99df/32db53e/fb82e5a/f519f32; deployed ALL FOUR daemons + plugin on macbook)
Three sesh changes + a mysystem ticket-dashboard feature (ticket f31fc492) + ticket ff48b03e.
- **Portable ~ cwd (sesh 7e6a888, binary; no schema bump)**: the Obsidian new-thread modal's
  box/mysetup pickers baked the LOCAL home into an absolute cwd → deploying that thread on a
  remote machine pointed nowhere. Fix: the OWNER daemon resolves a leading ~ against ITS OWN
  home (`expandHomeCwd` at top of handleThreadNew, before the absolute check + every spawn
  branch). CLI `absCwd` now PASSES ~ THROUGH unchanged (was expanding locally) so ~ is portable
  cross-machine; a bare relative path still expands against the invocation dir. Plugin
  (create-thread-modal) renders picked paths ~-relative (`toHomeRelative`) + no longer pins the
  machine for a ~-path. Tests: daemon TestExpandHomeCwd, cmd TestAbsCwd (now asserts ~
  passthrough). Daemon-side change ⇒ RESTART to apply, but it's NOT a schema bump.
- **tui ^k/^y (sesh 0109197, binary, ticket ff48b03e)**: in `/` filter mode ^k toggled
  include-children, shadowing move-up. Restored ^k = move selection up (symmetric w/ ^j=down);
  moved the child toggle to ^y. filter.go footer hint + SKILL + TestFilterChildToggleKeyIsCtrlY.
- **thread_archived (sesh 0ccda7a, schema 18→19 = daemon RESTART)**: added to the list-all entry
  (`api.TicketListEntry.ThreadArchived`, populated by the owning daemon from `archivedByID`), so
  a ticket browser can find OPEN tickets stranded in ARCHIVED threads without a per-thread call.
  Additive omitempty ⇒ mixed-mesh safe. conformance ticket.list-all archives the bound peer
  thread + asserts the flip. Back-filled the 17/18 schema-history comments.
- **mysystem ticket dashboard (ticket f31fc492)**: ticket-browser gained note-less +
  archived-thread PRESET filters (toolbar toggles + BrowserOpts.noteless/archivedThread); new
  `ms.sesh.*` obako-js helpers (getNotelessTickets/getArchivedThreadTickets/
  addNotesForNotelessTickets/openTicketBrowser) in src/ticket/dashboard.ts, exposed in plugin.ts.
  The vault note `bs/High-priority consolidations.md` got two obako-js blocks (buttons +
  dynamic lists) under "# Note-less tickets" + "# Non-completed tickets in archived threads".
  Live-verified end-to-end on macbook with synthetic data (both lists, both presets, the
  add-notes button), then cleaned up.
- **TWO plugin onReady FRAGILITIES uncovered + fixed (the deep lesson)**: the plugin's obako-js
  global surface (ms.consolidation/ms.sesh/ms.openBoxyardBrowser/…) was set in onReady AFTER
  BoxyardService.start(). BoxyardService reads its config via a dependency that does
  `import("node:fs")` — a DYNAMIC import (variable specifier, so esbuild can't externalize it)
  that REJECTS in Obsidian's renderer. Depending on esbuild's bundle ORDERING (which DIFFERS BY
  BUILD HOST — a mymain-built bundle was healthy, the same source built on macbook aborted!),
  that failure threw during init and aborted onReady before the globals were set → every
  dashboard silently lost its `ms.*` helpers. Fixes: (1) lazy-import the dashboard module inside
  the ms.sesh closures (fb82e5a) so onReady doesn't eager-load the browser chain; (2) move
  startTicketSync + BoxyardService to the END of onReady, after all global exposures (f519f32) —
  a boxyard failure can no longer strand the API surface. PROVEN by building on macbook (the
  broken-bundle env) and confirming healthy. LESSON: a plugin built on machine A can work while
  the SAME source built on machine B is broken if onload depends on bundle ordering — always
  test the bundle built where it's deployed; expose stable globals BEFORE fragile I/O subsystems.
DEPLOY (2026-06-16): all four daemons rebuilt+restarted to schema 19 (mymain/macstudio/macbook
supervisorctl; termux pkill+setsid-nohup with explicit SESH_* env from shell.sh). Plugin
(macbook only) pull+build+install+reload. KILLER PROOF for ~: a headless thread with --cwd
~/mysetup stored /home/lukastk/mysetup; on termux ~/storage → /data/data/com.termux/files/home/storage.
GOTCHA (re-confirmed): a stray `sesh daemon stop` with no isolated env hits the DEFAULT daemon —
the supervised mymain daemon auto-restarted, but be careful. /proc/<pid>/environ is unreadable on
termux — read the daemon env from shell.sh (~/.sesh, SESH_MACHINE=termux, sockets sesh/sesh-master).

## H22 — termux TUI broken: CGO=0 build can't resolve tailscale MagicDNS (2026-06-18, sesh 8c833f6, myrig 62c746c)
Ticket "something is wrong with sesh tui on termux" → user clarified: entering ANY thread in the
termux TUI failed with `✗ nav <m>:<sess>: exit status 1: sesh tmux: nav inner switch on <m> (http):
Post "http://<m>:7878/..."`, AND every peer showed OFFLINE (last sync ~4.6h stale). ROOT CAUSE (one
bug, two symptoms): the DEPLOYED termux `sesh` was built `CGO_ENABLED=0 GOOS=linux`. Go's pure
resolver reads `/etc/resolv.conf` — ABSENT on termux — and falls back to `[::1]:53` (nothing there),
so tailscale MagicDNS names (mymain/macstudio/macbook) NEVER resolve. Android's bionic resolver
(used by ssh/getent/python) resolves them fine via tailscale's split-DNS, but Go's pure resolver
doesn't touch bionic. So ALL http-transport peer traffic from termux (mesh sync + routing + the
nav "inner switch on <m> (http)", which POSTs to the peer's ApiAddr) failed on hostname lookup.
Diagnosis evidence: `go version -m ~/.local/bin/sesh` → CGO_ENABLED=0/GOOS=linux; a tiny Go probe
on termux showed CGO=0 default-resolver fails for `mymain`, CGO=0 + custom resolver→100.100.100.100
resolves the FQDN `mymain.tail27f06c.ts.net` but NOT bare `mymain` (no search domain), and
**CGO=1 build (termux's DEFAULT: GOOS=android, CC=aarch64-linux-android-clang) resolves both bare +
FQDN via bionic, even under `env -i`** (the daemon's setsid/nohup ctx). 100.100.100.100 raw-UDP is
unreliable for MagicDNS A-records on Android (only forwards public names) — bionic is the only thing
that does MagicDNS there.
FIX (Lukas chose "rebuild CGO=1 + loud self-check" over an IP-config or resolver-shim): (1) rebuilt
termux's sesh with `CGO_ENABLED=1 go build` and redeployed — peers now `synced 1s ago`, TUI Enter
exits 0 (no error). (2) sesh 8c833f6 adds `internal/daemon/dnscheck.go`: `checkPeerDNS()` (run
`go d.checkPeerDNS()` from Serve()) resolves every HTTP peer's ApiAddr host once at startup and
`log.Printf`s a LOUD warning naming the likely CGO=0 cause if any fail (ssh peers + literal IPs
skipped — `httpPeerHost()`, unit-tested TestHTTPPeerHost). No schema change. (3) myrig 62c746c adds
a comment at scripts/post/mysetup.sh's `go build` line: NEVER force CGO=0/GOOS=linux on termux.
The provisioning script was ALREADY correct (plain `go build` → CGO=1 on termux) — the bug came from
a PRIOR MANUAL deploy of mine forcing CGO=0/GOOS=linux (a static-binary habit copied from other
machines). LESSON: on termux, build sesh with plain `go build` (CGO=1/android); a CGO=0 binary runs
but is DNS-blind to the tailnet. termux daemon restart gotcha (bit me): `pkill -f "sesh daemon run"`
matched MY OWN ssh shell (its argv contained that string) and killed it before the mv — kill the
daemon by explicit PID (`sesh daemon status` prints it) instead; and `mv` the new binary into place
BEFORE killing, so the zshenv login-guard can only ever relaunch the NEW binary.
DEPLOY: all four daemons on 8c833f6 (termux native CGO=1 + relaunch; macstudio/macbook/mymain native
build + supervisorctl restart). Self-check silent on mymain/macs (they have /etc/resolv.conf).

## H30 — cockpit FAST-JUMP: prefix-less C-f → fzf of active non-on-hold threads (2026-06-29, myrig 944ac3d; NO sesh change; deployed ALL FOUR)
Ticket 1742cd23: a keyboard shortcut "as fast as just pressing Ctrl+S" (NOT a prefix sequence) that
opens an fzf of ACTIVE threads across all machines and jumps into one. Lukas's original ask was
hold-to-open / release-to-select. VIABILITY (checked first, per his request): the hold/release
gesture is IMPOSSIBLE — terminals emit NO key-RELEASE events for normal keys (holding just
auto-repeats the same byte sequence); the only release-reporting mechanism is the Kitty keyboard
protocol, which tmux can't BIND to, fzf doesn't read, and termux doesn't support. Pivoted (with
Lukas) to: re-press the SAME key to select the hovered row. KEY-CHOICE saga (each ruled out live):
Cmd/⌘ can't be bound (terminal never receives it); M-/Alt fiddly on Mac Option; C-s = XOFF
flow-control; C-g = Claude Code's external-editor; C-a = the MASTER PREFIX itself (binding it
root-table would break every prefix+ binding) AND readline start-of-line. Landed on **C-f**
(shadows readline forward-char + vim/pager page-forward globally in the cockpit — accepted).
IMPLEMENTATION (myrig ONLY — no sesh daemon/binary/schema change): shell.sh.jinja `_mt_enter_session`
gained a `--jump` mode = (a) drop on-hold rows via jq `select((.on_hold // false)|not)` over `thread
grid` (default grid already excludes archived ⇒ what's left is the active set), (b) pass `--bind
'ctrl-f:accept'` to fzf so re-pressing the opening key selects the hovered row (Enter accepts, Esc
cancels = fzf defaults). New `mmt-jump` (= `_mt_enter_session all --jump`) + my_alias. tmux.master.conf
`bind -n C-f` → display-popup running mmt-jump, carrying $SESH_NAV_CLIENT + active machine like s/a.
mysetup-navigator SKILL keymap updated (+ fixed a stale a/s swap). It's an mmt-layer / master-only key
(no mt twin) because cross-machine nav physically needs the master.
WHY a ROOT-TABLE key on the MASTER works from inside an agent pane: the master is the OUTER tmux; it
processes root keys FIRST and only passes unbound keys to the focused pane (the nested ssh→work
client). PROVEN in an isolated TRIPLE-NEST (driver→master→work): a genuine C-f fired the master's
root binding with ZERO bytes leaking to the inner work pane. Also verified: rendered template zsh -n
clean; fzf ctrl-f:accept selects / Esc cancels / Enter accepts (real-pane send-keys); live filter
10 active / 1 on-hold hidden. NB display-popups CAN'T be driven by `send-keys` (it injects into the
pane stdin, bypassing BOTH the key table AND the popup overlay) — test the open path via a nested
real-client attach, the fzf binds in a plain pane.
DEPLOY (myrig: render shell.sh + source-file the master conf; symlinked confs update on git pull,
rendered shell.sh needs install-home): ALL FOUR. mymain (python3 install-home + source master, C-f
bound on running master), macstudio (cij@, pull+render, no running master — staged), macbook (lukas@,
pull+render, master RUNNING+attached → C-f live), termux (lukas@android-main:8022). TERMUX GOTCHA
(bit me): `uv run --with jinja2 install-home` FAILED (uv couldn't fetch a Python) but my
`(uv … | tail) || (python3 …)` fallback NEVER fired — a pipeline's exit status is the LAST command
(tail=0), so uv's failure was masked and shell.sh was left STALE (mmt-jump absent) while C-f was
already bound = the worst half-state (key bound, function missing → "command not found"). FIX: termux
python3 HAS jinja2 (3.1.6) — re-ran `python3 scripts/install-home.py` directly (no pipe-to-tail) and
confirmed mmt-jump landed. LESSON: never gate a fallback on a `cmd | tail` pipeline; check the real
command's status or run it bare. Ticket 1742cd23 marked done (closed by myrig 944ac3d).

### H30 follow-up — F12 (Caps Lock) + `sesh tui` + a pty key-shim (2026-06-29, myrig dea086a)
Lukas iterated the H30 jump twice. (1) KEY: C-s [flow-control] then C-g [Claude's external-editor]
were rejected; he set Caps Lock→F12 via Karabiner on the Macs and wanted F12. tmux CANNOT see Caps
Lock directly (an OS lock key emits no keycode) — the OS remap to a real key is what tmux binds; and
tmux 3.5a accepts only F1-F12 as bind names (TESTED: F13+ rejected), so F12 works, F18-style tricks
don't. (2) PICKER: switch from fzf to `sesh tui` itself — its DEFAULT view is already
`ViewActive = non-archived AND not on hold` (model.go) and `[tui] all_machines` defaults it
cross-machine, so the fzf + jq on-hold filter were deleted (the H30 `_mt_enter_session --jump`
additions reverted as dead code). (3) RE-PRESS-TO-SELECT without modifying the sesh binary: new
home/.sesh/myrig/keyshim.py — a ~15-line pty wrapper (python `pty.spawn` + a `stdin_read` filter)
that rewrites the F12 byte sequence (ESC[24~ = terminfo kf12 = 1b,5b,32,34,7e) to CR before the
child sees it; all else passes through. mmt-jump runs `sesh tui` through it. WHY it works: a tmux
display-popup BYPASSES key tables, so while the jump popup is open the keypress goes to the program
INSIDE it — the shim turns that 2nd F12 into the Enter sesh tui already treats as "enter selected
thread". install-home symlinks keyshim.py to ~/.sesh/myrig/ on every machine (any non-.jinja file
under home/ → symlink). PROVED in isolated tmux: shim translates trigger→Enter to a child; with a
popup open a real F12 reaches the shim not the binding (the re-press test, GOT:[hello]); bind -n F12
fires; keyshim+real `sesh tui` renders the [active] cross-machine grid; F12 inside the live TUI acts
as Enter (live-filtered /fix→2 rows, then F12 selected, ZERO literal ESC[24~ leaked).
GOTCHAS (bit me): (a) `source-file` only ADDS/overrides bindings — the stale `bind -n C-f` from H30
PERSISTED on the running masters; must `tmux -L sesh-master unbind -n C-f` explicitly (did so on
mymain/macbook/termux; macstudio has no master). (b) A stray `py_compile` I ran left a repo
`home/.sesh/myrig/__pycache__/` that install-home then SYMLINKED into ~/.sesh — `rm -rf` it (running
keyshim as a script doesn't create __pycache__; only my compile did). (c) Over `ssh-target <m> 'zsh
-lc "…"'`, an unquoted `===` label triggers zsh's `=cmd` expansion ("== not found") and nested
double-quotes fight the local shell — pipe a heredoc to `zsh -ls` on the remote instead (clean, login
env sets MYRIG_TARGETS). (d) termux render: `uv run` can't fetch a Python — use its `python3`
(has jinja2 3.1.6) directly, NOT via `… | tail` (the pipe masks the real exit status, H30 lesson).
DEPLOY: myrig dea086a (rebased over a concurrent install-home push). All four: install-home (render
shell.sh + symlink keyshim.py) + source master conf + unbind C-f where a master ran. macbook is the
live Caps Lock→F12 machine.

## H31 — master prefix+s / F12 preselect lands instantly (concurrent resolve + no fork for local) (2026-06-29, sesh 5be2e21; NO schema change; deployed ALL FOUR)
Lukas: launching `sesh tui` from the cockpit (prefix+s or the H30 F12/Caps-Lock jump) preselects the
thread the active master window is on, but the cursor started at the TOP and visibly JUMPED a beat
later. MEASURED the cost (date-delta timer, no /usr/bin/time on this box): `sesh` fork/exec ~12ms;
master-current LOCAL ~13-16ms (≈ all fork — the RPC itself ~1-3ms); master-current ROUTED to a peer
~128ms (the cross-machine ssh/http round trip, intrinsic); mesh fetch ~17ms. ROOT CAUSE = two things:
(1) SERIALIZED — resolveMasterCursor was kicked only from the FIRST meshMsg, so its latency stacked
ON TOP of the first fetch; (2) FORKED — it always exec'd a whole `sesh tmux master-current`
subprocess even when the active window is LOCAL (where the TUI's own daemon client can answer).
FIX (internal/tui/model.go, binary-only — TUI is a daemon client, the TmuxMasterCurrent endpoint is
unchanged ⇒ NO schema change, NO daemon restart): (a) Init now kicks the resolve CONCURRENTLY with
the first fetch via a CONDITIONAL `tea.Batch` (only when masterCursorMachine is set; a plain Init
stays a lone fetch — important because the conformance harness drives `m.Init()()` directly and
expects a single meshMsg, and `render()` is only ever called on non-master-cursor models, verified).
The resolve feeds the existing m.preselectID machinery, so whichever fetch carries rows lands the
cursor; for local the resolve finishes before/with the fast local-cache fetch ⇒ cursor correct on the
first render-with-rows (no jump), matching the instant --cursor path. Removed the now-redundant
meshMsg resolve-kick + the masterCursorDone one-shot guard (Init runs once, so the kick is inherently
one-shot — and Init's value receiver can't persist a "done" flag anyway). (b) resolveMasterCursor:
local (machine=="" || ==origin) → m.client.TmuxMasterCurrent directly (~2ms, no fork); REMOTE → still
execs `sesh tmux master-current --machine X` because the local client can't route the ssh/http hop
(only the subprocess's --machine routing reaches the peer daemon). NET: local jump effectively
instant; remote shows rows immediately + lands after the one ~120ms cross-machine read (intrinsic —
the marker-client pane lives on the peer). TESTS: TUI unit suite green incl. -race on
MasterCursor/Preselect; updated TestMasterCursorAsyncAndNestedJump to assert Init kicks fetch+resolve
together AND a plain Init stays a lone fetch. Live-smoked `sesh tui` w/ SESH_TUI_MASTER_MACHINE on the
live mymain daemon: renders cleanly, empty resolve = graceful no-op (cursor top, no crash). DEPLOY:
binary-only — rebuilt+installed on all four (mymain native; macbook/macstudio /opt/homebrew/bin/go;
termux PLAIN `go build` = CGO=1/android per H22), all vcs.revision=5be2e21. No daemon restart (each
F12/prefix+s spawns a fresh `sesh tui` from the installed binary → live on next press).
FUTURE (if the remote ~120ms still bugs him): add daemon-side routing for master-current so the TUI
calls the LOCAL daemon (fast, no fork) and the daemon resolves the peer over its WARM keep-alive
meshsync connection (~RTT, maybe ~30ms) instead of a cold subprocess+http. Bigger change (daemon
endpoint), deferred — Lukas to decide if the remote case warrants it.

## H32 — remote master-cursor preselect: daemon-side warm routing + sesh tui filter mode for F12 (2026-06-29, sesh 7f679e7 api 35→36, myrig 1b46084; deployed ALL FOUR)
Follow-up to H31. Two asks: (1) tackle the ~120ms REMOTE master-cursor preselect; (2) F12/Caps-Lock
should open `sesh tui` in FILTER mode like prefix+s; (3) "apply the same fix to prefix+s".
(1) DAEMON-SIDE WARM ROUTING (sesh, api 35→36). H31 made LOCAL preselect instant but left remote at
~120ms: resolveMasterCursor forked `sesh tmux master-current --machine X` — a cold subprocess (~12ms
fork) that opened a COLD connection to peer X (extra handshake RTTs) and round-tripped. FIX: GET
/v1/tmux/master-current gains an optional `machine` query param; machine==peer is resolved ON THAT
PEER by the daemon via fetchPeerMasterCurrent (internal/daemon/fanout.go) — peerRemoteClient over http
(the conn meshsync keeps WARM in the shared http.DefaultTransport pool, so ~RTT, no handshake) or an
ssh hop, mirroring fetchPeerThreads. client.TmuxMasterCurrent gained a `machine` arg; the TUI's
resolveMasterCursor now ALWAYS makes a single LOCAL daemon-client call (no subprocess ever) — local
sent as machine "" (so a pre-36 daemon still resolves local correctly during deploy skew), remote sent
as the machine (daemon does the warm hop). CLI unchanged (route.go still routes --machine; passes "").
MEASURED on mymain→macbook (real network): cold CLI route ~121-141ms → daemon warm-route ~80-94ms.
The remaining ~80ms is the FLOOR: macbook RTT ~47ms + macbook's marker read ~30ms = one round trip +
the work. Can't beat physics without proactive per-tick caching (adds cross-machine load — not worth
it). MIXED-MESH SAFE: only the daemon the TUI talks to (its own machine) needs 36; the routed peer is
queried with origin only (machine ""), the in-process resolve pre-36 peers already serve — so the mesh
need not be in lockstep, a binary+restart on the master machine suffices. handler returns 502 on a
peer failure → the TUI treats it as an empty no-op preselect (never a wrong cursor).
(2) FILTER MODE (myrig): mmt-jump now runs `sesh tui --filter` (was bare `sesh tui`), opening in
filter mode like prefix+s. The keyshim still rewrites F12→Enter; in the TUI's filter mode Enter
(=re-pressed F12) calls navSelected() = enters the highlighted row (filter.go:245), Esc applies the
filter + drops to normal nav, q closes. So type-to-narrow then F12-to-enter is intact.
(3) prefix+s NEEDED NOTHING: prefix+s and F12/mmt-jump both just launch `sesh tui` with
SESH_TUI_MASTER_MACHINE set → the SAME resolveMasterCursor code path → both H31 (concurrent Init) and
H32 (daemon warm-routing) apply to prefix+s automatically. It was only "still slow" because macbook
hadn't been redeployed yet; fixed once the binary+schema-36 daemon landed there.
TEST: the tmux.master-current conformance cell (real master + real ssh-localhost peer) gained a
direct-daemon-client assertion — a client to self's daemon, called with machine=peer, returns the same
thread the routed CLI does (peerw), proving self's daemon did the hop (the path the TUI uses). Cell
green (10.8s). TUI unit suite green incl -race; full build+vet clean. Live-proven: a curl of mymain's
daemon socket with machine=macbook returned a real macbook thread over the warm http conn.
DEPLOY (api 36 = daemon RESTART): ALL FOUR. mymain (native build + supervisorctl restart sesh-daemon),
macbook + macstudio (lukas@/cij@, git pull + /opt/homebrew/bin/go build + supervisorctl restart),
termux (lukas@android-main:8022 — git pull + PLAIN go build CGO=1/android, mv, kill daemon by EXPLICIT
pid + setsid-nohup relaunch with SESH_HOME=~/.sesh SESH_MACHINE=termux sockets sesh/sesh-master per
~/.myrig/zshenv/termux.sh — NOT pkill -f, NOT supervisor). All four api schema 36. myrig 1b46084
rendered on all four (shell.sh only; F12 binding unchanged so no conf re-source). NB `sesh daemon
status` text shows `schema_version: 18` = the STORE migration version, NOT the api schema — read the
api schema from `--json | jq .schema` (36).

### H32 follow-up — keyshim must SIZE the child pty (F12 sesh tui rendered garbled) (2026-06-29, myrig 4df5cce; myrig-only)
Lukas: pressing F12 (Caps Lock) opened `sesh tui` rendered GARBLED — rows wrapped/doubled + stale
terminal content bled through — but prefix+s was fine. (He noticed it on an archived thread but it
recurred unarchived — archived was a red herring.) ROOT CAUSE: prefix+s runs `sesh tui` DIRECTLY in
the popup pty (correct size); F12 runs it through keyshim.py, which used python `pty.spawn` — and
pty.spawn/forkpty creates the inner pty WITHOUT copying the window size, so bubbletea rendered for a
wrong/default size (rows wider than the perceived width → terminal wraps them → doubled rows; the
unrendered popup area shows stale content). A plain PANE test happened to look fine (it inherited a
usable size), which is exactly why my earlier smoke missed it — only a real display-popup exposed it.
FIX (rewrote home/.sesh/myrig/keyshim.py): drop pty.spawn for an explicit bridge — pty.openpty(),
copy our terminal's winsize (ioctl TIOCGWINSZ on stdout → TIOCSWINSZ on the pty) BEFORE the child
starts so the TUI never sees 0x0, os.fork + os.login_tty in the child, then a select() copy loop that
still rewrites the trigger bytes (F12=ESC[24~) → CR; a SIGWINCH handler re-copies the size (the kernel
then SIGWINCHes the child) so resizes work. Exit with the child's status. PROVEN in isolated tmux: the
child sees the right size in a pane (40 150) AND in a REAL display-popup (34 133 of a 40x150 client,
not 0 0 / 24 80 — drove the popup via a nested real-client attach since send-keys can't reach a popup);
F12 still translates to Enter; `sesh tui --filter` renders cleanly (no doubled rows). DEPLOY: keyshim.py
is SYMLINKED by install-home (non-.jinja under home/ → symlink), so deploy = `git pull` on each machine
(no render, no restart) — the F12 popup picks up the new file on the next press. All four pulled;
os.login_tty present on every python3 (linux/macOS/android); compiles clean. LESSON: a pty wrapper for
a full-screen TUI MUST set the child pty's winsize + forward SIGWINCH — pty.spawn alone doesn't, and a
plain-pane test won't catch it; test inside the actual display-popup.

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
