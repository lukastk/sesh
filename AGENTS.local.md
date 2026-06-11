# AGENTS.local.md — sesh v2 working notes

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
DEPLOY STATE: mymain daemon restarted onto the new binary (schema 8) to enable H6 + H5.
macbook/macstudio/termux daemons + cross-host partner binaries still on schema 7 — need
redeploy for cross-machine cwd labels (additive field, so mixed-schema mesh is safe meanwhile).


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

### Live full-feature drive 2026-06-10 (tui-tmux-testing rig, both machines) — ALL PASS
Work server: status line; TUI enter on alive/dead(resume)/headless(promote) — presser
switches, supervisors untouched, popup closes; picker a/A (+type-filter); Tab archive
toggle (was passing LITERAL #{pane_id} — display-popup no-expansion — fixed via
run-shell); t; guarded K. Master: a/A picker + s TUI (master path: window flip + ONLY
the origin's marker client moves, verified mymain→macbook AND on macbook's own master
= the original user repro); t; P (loud clipboard error stays 3s); native n/p/w; K
disconnect. Plain shell: mt-enter-session→attach, sst2 Enter→attach, mt-attach.
mt-copy-to-master --to self short-circuit (loud xclip/no-display on headless) +
no-flag auto-detect offered exactly the live watchers. NOT tested (user instruction):
actual clipboard writes on macbook. Test-harness lessons: keys racing a slow popup
stack a second popup (overlapping navs — chaos); a tmux client whose session is
killed with detach-on-destroy=on RECONNECTS via the supervisor (plain attach → most
recent session); in zsh, ALWAYS quote `-t "=name"` (unquoted =word does PATH lookup).

### Late additions (2026-06-10, per Lukas live feedback)
- Master `prefix+a` = **mt-enter-tmux-session**: fzf over ALL tmux sessions on every
  machine's v2 work server (`seshv2 tmux info` routed per machine; unreachable → warn+skip),
  nav via shared `_mt_nav_to`. `A` = archived-thread picker, `s` = thread TUI. E2E-driven.
- **mt-reload-conf [machine...]**: re-source work conf on each machine's RUNNING work
  server (ssh-target for remotes) + the LOCAL master conf. tmux lesson recorded in it:
  source-file ADDS/OVERRIDES only — removed bindings (or ones picked up from ~/.tmux.conf
  before SESH_TMUX_CONF existed) persist until a true -f restart. macbook's work server
  had 9 stale ms-* bindings from exactly that; surgically unbound live (b B e E m M g G
  j J k o O , .) to match a fresh start.

### DONE: the UNIFIED THREAD MODEL (Lukas directive 2026-06-10; schema v2; deployed both machines)
Headless/headful becomes INFERRED runtime, not a stored gate. Design (committed here as
the execution checkpoint — if compacted, resume from this list):
- **States (inferred per tick)**: pane-live (probe content-diff → working/waiting) |
  turn-in-flight (hlInFlight → working) | **idle** (neither — the UNIFIED state that
  replaces BOTH "dead" and headless-between-turns). Wire: ActivityDead "dead" RENAMED →
  ActivityIdle "idle" (api.SchemaVersion bump; reinstall both machines + crosshost note).
- **Record**: drop the `Headless` gate from api.Thread + all gating (store column kept,
  deprecated, unread). `HeadlessStarted` re-semanticized = "conversation has begun"
  (headed new sets TRUE at spawn; headless new sets it on first turn) — it feeds
  HeadlessTurn's started param so send-headless on a HEADED-BORN idle thread resumes.
- **Verbs/gates (symmetric)**: send → needs pane-live (409 else). send-headless → 409 if
  pane-live ("would fork the conversation — use thread send") or turn-in-flight; ALLOWED
  on any idle thread (headed-born too). headful == resume (merged impl; both CLI verbs
  kept); allowed on idle only (409 turn-in-flight, 409 pane exists); codex-no-id N/A stays.
  thread new --headless = "no pane now" spawn choice only.
- **TUI/grid/picker**: glyph ◌ + label "idle"; Enter on idle → resume (routed if remote);
  no promote-vs-resume distinction. myrig picker drops .headless jq + 💤→◌ idle.
- **Conformance re-semantics**: new.headless (record has no pane + idle), send.headless
  (3 directions: headless-born turn ✓, HEADED-BORN idle turn NEW, pane-live 409 NEW),
  headful (= resume on a never-paned thread, continuity), headful-busy unchanged,
  runtime-state + snapshot/mesh/TUI claims: dead→idle strings. grep -rn '"dead"\|ActivityDead\|Headless' over conformance + tui + myrig.
- Execution order: api → store → daemon (maintainer/headless/headful/resume/thread/grid)
  → cmd → tui → build → conformance pass → FULL suite → deploy both + myrig picker.
- SHIPPED. Four extra bugs the gates+live smoke caught: (1) headed-born CODEX turns
  silently started fresh conversations (no pre-assigned id) → rollout discovery like
  revive, loud N/A if no turn ever; (2) maintainer.snapshot() emitted ZERO-IDENTITY rows
  for just-created threads → excluded until first publish; (3) the nav kick silently
  no-op'd on a NONEXISTENT session (v1 silent-failure class, reachable via Enter on a
  snapshot-stale row) → has-session guard, loud error; claims settle rows to idle first;
  (4) DEPLOY-ENV PARITY: the supervised daemon couldn't run pi/codex headless turns
  (PATH lacked mise shims) and lacked zshenv API keys → ini adds the shims + sesh runs
  headless turns through $SHELL -c exactly like tmux runs pane commands (ini pins
  SHELL=/bin/zsh); the conformance suite CANNOT catch deploy-env gaps (test daemons
  inherit the dev shell) — live smoke after deploy is mandatory for daemon-exec paths.
  Supervisor env changes need reread+update (restart reuses in-memory config) and the
  daemon must restart AFTER the binary build.

### Cockpit SELF-HEAL + TUI routed compose (2026-06-10 evening, per Lukas)
Lukas's invariant: "any machine that is CONNECTED has a master window" — overrides the
earlier K=intent rationale. `masterMaint` (3rd daemon loop, 5s tick): converges an
EXISTING master to self + mesh-REACHABLE registered peers; only ADDS windows; downed
master stays down; unreachable peers (e.g. unprovisioned macstudio) never get a window
forced (and a K'd unreachable window stays gone — but mt-start/mt-ensure still create
it manually). SESH_MASTER_SELFHEAL=off = test isolation only (master.ensure cell would
race the healer). Cells: master.selfheal (kill→auto-recreate+real re-attach, ghost peer
excluded, no resurrect) + master.ensure (manual, prefix+R / mt-ensure). PROVEN LIVE:
killed windows back in 3-5s on both machines' masters. ALSO: the TUI's Enter now ROUTES
cross-machine promote/resume (`thread headful|resume --machine` + re-resolve session
from the owner) — the dead-test123-on-mymain-from-macbook case; previously only the
picker routed. DEPLOY NOTE: the healer lives in the DAEMON → daemon restart required;
window supervisors keep old binaries until their window is K'd (healer recreates with
the new one — a poor-man's rolling supervisor upgrade).

### DONE: the PARITY ROADMAP (_dev/PARITY_ROADMAP.md — COMPLETE 2026-06-11)
21 features from the (b)-list discussion, built FULL-FEATURED (Lukas's explicit
no-scarcity directive, saved in auto-memory), each: v1 research → cells/claims →
full gate → deploy both → live smoke → tick. DONE so far (2026-06-10): A1 columns
(+SESH_HOME default hazard fix ~/.sesh→~/.sesh-v2), A2 [[cwd_label]], A3 full fzf
filter, A4 predicates+[[tui.views]] (schema v4 tickets_open join), A5 parent/child
+tree (schema v5, migration 7), F1 current-thread inference + sesh info (+ id-PREFIX
resolution; SESH_THREAD_ID now also in headless turns). Matrix 129 cells, 33 TUI
COMPLETE: A1 columns, A2 [[cwd_label]], A3 fzf filter, A4 predicates+views, A5
parent/child tree, F1 inference+info, B1 hooks, B2 notifications, C1 await, C2
delegate, C3 subscribe, D0 transcript layer, D1 tail, D2 copy, D3 fork, D4
backup/restore, D5 adopt, D6 meta, E1 SESH_MACHINE-refusal, E2 doctor, E3 spawn
knobs (yolo default), E4 v1-import. Schema v7; 175 cells. Deploy E2/E4 pending
final gate (suite31). KEY late lessons: migrations are APPEND-ONLY (mid-insert
desyncs deployed DBs); subscribe delivery is now DETERMINISTIC (daemon triggers
on headless turn completion, not the polled eventer) + marker-based dedup test;
the box is Lukas's SHARED working machine (load ~5 from his live sessions + the
v1 sesh-daemon at 31%% CPU) so the heaviest cells need generous waits, not
tighter ones. Cleaned 5275 dead test tmux sockets.
D0-D6 transcript layer → E1-E4 hygiene. Testing lesson (bit twice): NEVER settle a
claim on row ABSENCE alone — vacuously true pre-publish; settle on PRESENCE first.

### DONE: v1-parity (a)-list TUI affordances (2026-06-10 night; deployed both machines)
From `_dev/V1_FEATURE_AUDIT.md` (the ultracode audit: 178 v1 features compared, doc is
the (b)/(c) discussion agenda — (b) list NOT yet discussed). Shipped + 8 new claims (21
total): Esc quits (normal mode only; prompt/popup own their Esc), Tab view-cycle
active/archived/all w/ title label ("a" now TOGGLES archive from row.Archived), line
prompt r=rename (prefilled)/t=tag (submit via CLI verbs => routed cross-machine; R is
refresh now), cursor wrap, i=tid8 column, y=full-UUID popup + c copies (v1 clipboard.go
ported verbatim; claim asserts at a PATH-stubbed wl-copy boundary), `tui --cursor` +
SESH_TUI_PANE=#{pane_id} carrier in the work-conf s binding (popup\'s own $TMUX_PANE is
the popup!), rune-safe trunc, peer remove, daemon restart (lifecycle cells assert new
pid + old dead). Conf applied live via mt-reload-conf; live smoke: real prefix+s popup
preselected the pressing pane\'s thread, Esc closed. One unreproduced one-off flake in a
TUI-claims package run (lost output; full gate + two re-runs green) — watch for it.

### DONE: the TWO-AXES state model (Lukas 2026-06-10 late; schema v3; deployed both machines)
Replaces `activity {working,waiting,idle}` ENTIRELY (the enum fuses axes: waiting ≡
headful∧quiet, idle ≡ headless∧quiet, and `working` ERASES the head axis). New wire on
status/row/snapshot: `head: "headful"|"headless"` (live pane vs not) + `busy: bool`
(pane mid-turn via content-diff, or headless turn in flight via registry). States:
headful·busy ◐, headful·idle ●, headless·busy ◉ (turn in flight — Enter = loud "turn
in flight, wait"), headless·idle ◌ (no runtime — Enter revives, send-headless turns).
needs-input = headful∧¬busy; ticket needs-restart = headless∧¬busy. Sweep: api (drop
Activity+the uncommitted Runtime draft), maintainer/thread-status/resolveActivity/
grid/ticket, TUI (glyph/STATE cols/navSelected: revive iff headless∧¬busy), CLI prints,
myrig picker jq + markers, SPEC §3, harness waitThreadReady (waiting → headful∧¬busy),
ALL cells/claims re-assert (runtime-state, send.headless, snapshot, crosshost, mesh,
grid-render claim). Then full suite + deploy both + live smoke.
SHIPPED: 125 cells + all claims green in one run; deployed; all four states traversed
live in order on the mesh. Glyphs (Lukas's choice): head ● headful / ◌ headless, busy
▶ busy / · idle, ? per-axis unknown (axes are wire STRINGS precisely so skew renders ?).
Side effect of templated session names: thread NAMES no longer need to be unique
(sessions are tid8-unique) — duplicate names in the grid are legitimate.

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

### macbook residual state (pending, not blocking)
Its WORK server predates the conf wiring (3 live user threads — q/test/mac-shell — not
mine to kill): new work conf + bindings were `source-file`d onto the RUNNING server
(status line + carrier bindings live), but a true `-f` start applies only on its next
cycle. macstudio: still no v2 (master windows exclude it via --machines).

### DONE: CLI/TUI feature batch G1–G6 (_dev/CLI_TUI_FEATURES.md, 2026-06-11; deployed both machines)
Six features, each researched→cells/claims→gate→deploy→live-smoke→commit:
- **G1 `thread capture`** (v1 pane-capture port): GET /v1/threads/capture → CapturePaneLines
  (lines>0 = `capture-pane -S -N`); dead pane = LOUD 409 (no empty-string fallback); routed
  cross-machine over the OWNER's transport (http/ssh) — strictly better than v1's local-only.
  Cell `thread.capture` (agentic × both loc, 6 green). Live-smoked both machines incl.
  mymain→macbook http-routed capture (real pi pane content + 409).
- **G2 remove-tag** (`T`): tag-picker popup → `thread tag --remove`; rowPatch.removeTags
  optimistic. Claim `action-untag`.
- **G3 set-parent** (`P`): paste parent uuid → `thread reparent` (empty=root); NO optimistic
  structural patch (refetch + preselect + actionMsg.expand the new parent to dodge the
  orphan-promote/propagation race); daemon cycle-guard surfaces loud. Claim `action-reparent`.
- **G4 per-column colours**: `[[tui.column_color]]` (name + colour name/0-255/#rrggbb) over
  built-in defaults NAME=blue/CWD=green; empty colour clears. renderCells colorize flag,
  precedence selected-reverse > match-highlight > colour. Unit tests + claim `column-colors`
  (forces termenv profile since `go test` stdout strips colour; asserts colour emitted +
  strip-ANSI == plain layout). myrig config.toml.jinja got a commented example.
- **G5 scrolling**: vOffset/hOffset; `ctrl+j/k` half-page viewport scroll (cursor-follow),
  `h/l` horizontal column pan, **fold moved to ←/→** (Lukas-confirmed). ▲/▼ + ‹/› indicators.
  Inactive until a WindowSizeMsg (height/width 0 ⇒ render-all, so non-size tests unchanged).
  Frozen-NAME DROPPED (simple whole-column pan — recorded cut). Claims scroll-vertical/
  scroll-horizontal. New scroll.go. Also hardened claimMasterCursor's pre-existing publish race.
- **G6 CLI help**: cmd/sesh/help.go registry (every command+subcommand: summary, usage with all
  flags, examples) + main.go intercepts `-h`/`--help`/`help <cmd> <sub>` BEFORE routing (stdout,
  exit 0; survives a stray --machine). help_test.go meta-test = no-silent-gap guard.
KEY LESSONS: (1) `cp` over a running binary = ETXTBSY (Linux) → build to `.new` + `mv -f`
(atomic rename, same fs, running inode untouched). macOS: scp into ~/.local/bin/ + `codesign
--force --sign -` + same-dir `mv -f` (cross-fs mv re-invalidates the signature). (2) macbook
ssh user is **lukas@macbook** (home /Users/lukas), NOT lukastk — wrong user = "too many auth
failures". (3) supervised v2 daemon restart = `supervisorctl restart sesh-v2-daemon` (NOT the v1
`sesh-daemon`). G1 needed daemon restart; G2–G6 are binary-only (TUI/CLI exec the binary).

## Build status: ALL GREEN

Feature matrix: **120 cells** (added `master.*`, `tmux.work-conf`, `tmux.nav-in-client-multi`,
`tmux.nav-attach`, http twins, etc.). Separate **TUI conformance track: 11 claims**
(+ `action-nav-headless`, `action-nav-attach`). Plus **`TestRealCrossHost`** +
**`TestRealCrossHostHTTP`** (real network mymain↔macbook) — env-gated/skip-able.
`go test ./...` green (incl. the once-flaky `thread.resume/codex/remote`).

### TUI "enter a thread doesn't work" — FIXED 2026-06-10 (03e24a9, 355de97). THREE bugs:
Debugged by driving the real TUI in nested tmux + a faithful 2-client repro.
1. **Headless/dead threads couldn't be entered.** Enter only navved to `row.SessionName`,
   but a headless thread has NO pane and a dead one's pane is gone — so it navved to a
   non-existent session and SILENTLY no-op'd. `navSelected` now COMPOSES (the backlog
   design): a headless thread is promoted (`thread headful`), a dead one resumed
   (`thread resume`) on its owning daemon, THEN entered; cross-machine fails LOUDLY.
2. **`nav --in-client` switched the WRONG client** — THE one Lukas actually hit ("works in
   a master window, not the inner tmux"). With the master up, its window holds a client on
   the work socket AND a direct inner-tmux attach is a SECOND client on the same session.
   Resolving "the client" as `list-clients | head -1` picked the master's client, switching
   a view the user wasn't looking at. FIX: resolve the CURRENT client via `tmux
   display-message -p '#{client_name}'` (the client whose keystroke is handled = the one
   that pressed Enter) and switch IT. Conformance `tmux.nav-in-client-multi` (2 real
   clients, nav from B, asserts B moved — fails on head -1).
3. The master-path inner switch had the same bare-`switch-client` unreliability →
   `InnerSwitchScript` now also targets the work server's client explicitly (kick fallback
   kept). Conformance TUI claim `action-nav-headless`.
**Deploy: just update `~/.local/bin/sesh-v2` (TUI/nav run the binary directly; no daemon
or master restart needed).**

### TUI enter from a PLAIN SHELL → attaches (feature, 2026-06-10, commit 1fc3463)
`seshv2 tui` outside tmux: Enter ATTACHES the terminal to the thread instead of trying to
switch a (non-existent) client. The TUI captures `$TMUX` at construction (`m.tmux`,
deterministic/testable via `WithTmux`); when empty, `navSelected` returns `attachMsg` →
the TUI quits → `runTUI` execs `tmux nav --attach`, which `syscall.Exec`s `tmux attach`
locally or `ssh -t … tmux attach` for a peer (on detach → back to the launching shell).
Conformance `tmux.nav-attach` (real client lands on the target) + TUI claim
`action-nav-attach` (model quits with a pending nav --attach).

### Work-server tmux config — `SESH_TMUX_CONF` + `peer --tmux-conf` (commit 9aba503, Backlog #5)
`SESH_TMUX_CONF` → the work `tmux.Server` is `NewServerWithConf`, prepending `-f <conf>`
(sourced at server start, ignored when running). So sesh's tmux carries its OWN UI (the
per-thread status bar) separate from the user's `~/.tmux.conf` — fixes the gray bar after
v1 retires. `peer.TmuxConf` (`peer add --tmux-conf`) → the master's remote window starts
the peer's work server with its conf. myrig owns the conf FILE (a `-f` conf REPLACES base,
so it must `source` the base bits it wants + add the sesh status line). NOW WIRED in
myrig — see the next section.

### myrig port (BACKLOG #4b) — COMMITTED + DEPLOYED to mymain & macbook 2026-06-10
(myrig f799024 + a76730d wrapper SESH_TMUX_CONF + 0ce88b6 mt-start exit fix; Lukas
authorized commit/push/deploy.) Deploy = install-home + daemon restart on both machines.
mymain: master + work server CYCLED onto the new confs (verified: master prefix C-a /
machine windows / tui binds; work server `status 2` + seshv2-current-status format —
started through the master self-window, proving the wrapper's SESH_TMUX_CONF path).
macbook: files + daemon deployed; its WORK server (3 live threads) and MASTER (2 attached
clients) were NOT cycled — old confs until restarted (`mt-kill && mt-start` from a fresh
shell on macbook; work conf applies when its work server next starts empty). NOTE: the
macOS /opt/homebrew/etc/supervisor.d/sesh-v2-daemon.ini is a HARDLINK to the rendered
~/.supervisor/conf.d copy — install-home updates both at once.
The master-tmux + sesh myrig layer is ported to v2 (PARALLEL to v1 — old files untouched):
- **`home/.sesh-v2/myrig/`** (NEW — the consolidated v2 folder; named `.sesh-v2` to match
  SESH_HOME). Confs are fully **SELF-CONTAINED** (c8ef32b, per Lukas: base.conf carries V1
  master-tmux keybindings that must not leak): they never read `~/.tmux.conf`/`base.conf`.
  `tmux.common.conf` = the generic subset of base (mouse/status pos/window styles/extended-
  keys block/clipboard/focus; deliberately DROPS tpm+continuum — a continuum restore would
  recreate `sesh_*` names as agent-less shells the daemon would mis-probe).
  `tmux.windows-format.conf` = own copy of the captured default windows format.
  `tmux.work.conf` (C-b; status 2 + v2 row; t shell, K guarded kill-session, s tui,
  a/A mt-enter-session, Tab archive-here) and `tmux.master.conf` (C-a; status-left #W;
  a/A picker, s tui, t, K kill-window, P paste; unbinds c/,/./$ defaults). No unbind-v1
  lists needed anymore. mms-on-machine pick-machine popups still NOT ported (mysystem
  integration, thin by design).
- **`sesh-v2.sh.jinja`** grew (naming per Lukas: **`mt-*` = master-tmux cockpit commands**,
  `seshv2-*` = sesh-side helpers): `mt-start [--no-attach]` (master up --tmux-conf + attach),
  `mt-attach`, `mt-kill`, `sst2`, `seshv2-current-status` (pane→thread via env-injected
  `TMUX=<sock>, TMUX_PANE=<pane>` + `tmux current --json`, fields via `thread list --json
  --archived` + jq), `seshv2-archive-here` (toggle), clipboard cluster
  (`_mt_clip_get_image/text` + `mt-set-clipboard` copied from v1 so v1 deletion can't break
  it; `mt-send-clipboard <machine>` → `tmux stage-file --to`; `_mt_send_clipboard_and_paste`
  → master window name = machine → send-keys staged path into the master pane;
  `mt-copy-to-master [--to <machine>] <file>` → ssh-target + remote `mt-set-clipboard` —
  v1's no-`--to` auto-detect of attached masters relied on marker files written by
  mms-remote-entrypoint, which the v2 Go supervisor doesn't write, so no-`--to` is an fzf
  picker over $MYRIG_MACHINES instead; my_alias groups `-g mt` / `-g sesh2`).
  **`mt-enter-session [--archived]`** (c8ef32b) = the V1 mms-enter-session twin: fzf over
  `thread grid --json --all-machines` (⚡/💤/◌ markers), then the TUI's Enter compose in
  shell (headful for headless / resume for dead, `--machine`-routed, session re-resolved),
  then context-aware nav (--attach / --in-client / master). Bound prefix+a/A on BOTH
  servers. E2E-tested (2 real clients; only the invoker switched). JINJA GOTCHA: zsh's
  braced length form contains brace-hash = a jinja comment-opener; use `$#var` in .jinja.
- **Wiring**: supervisor ini adds `SESH_TMUX_CONF=~/.sesh-v2/myrig/tmux.work.conf`;
  peers.json.jinja adds per-peer `"tmux_conf"` (peer's OWN home path).
VERIFIED LIVE on mymain: both confs load on isolated sockets (bindings + format asserted);
status line + tag + archive-toggle round-trip on a real claude thread (created→deleted);
`stage-file --to macbook` landed + read back over the real mesh. NOT testable here:
clipboard grab (headless box, no X) — straight port of working v1 code.
ACTIVATION (after Lukas commits + provisions): daemon restart picks up SESH_TMUX_CONF; then
`tmux -L sesh-v2 kill-server` (work conf applies at server START) + `mt-kill && mt-start`.
Known gaps (flagged, not blocking): no per-window reconnect after prefix+K (master up is
loudly non-idempotent — needs `master window add` or idempotent up, a sesh change);
`mt-copy-to-master` has no attached-master auto-detect (needs sesh support: the master
window supervisor registering its origin machine with the remote daemon — backlog
candidate); no nav history (L unbound).

### Mesh / live cross-machine state (branch mesh-replicated-state) — the killer feature
Design in `_dev/MESH.md`. Three decoupled loops:
- **L1 maintainer** (`internal/daemon/maintainer.go`): per-daemon background rolling
  probe → every local thread's live state is O(1) to read (`/v1/snapshot`, `thread.snapshot`).
  Grid reads it (`d.maint.stateOf`).
- **L2 mesh sync** (`internal/daemon/meshsync.go`): pulls each peer's snapshot over
  multiplexed ssh into a SQLite cache (`peer_snapshots`, migration 6); `/v1/mesh` serves
  the merged view locally (`mesh.snapshot`, `mesh.offline-listing`). Offline peer →
  reachable=0, last-known retained.
- **L3 TUI**: `sesh tui`/`sesh mesh` render the merged view with per-machine staleness.
- **Phase C — network API** (`internal/daemon/apiserver.go`): `SESH_API_ADDR` exposes the
  SAME full router over TCP behind a bearer token (`SESH_API_TOKEN`); refuses to run
  exposed without a token. Client is transport-agnostic (`client.NewRemote`); CLI targets
  a remote daemon via `SESH_REMOTE`. Parity by construction + tested (`api.tcp-auth`,
  `api.tcp-parity`). For mobile: bind to the tailscale interface; a phone hits `/v1/mesh`.
- `peers.Peer` has a `Port` field (`peer add --port`) → non-22 machines reachable
  (`SSHArgs()` at every ssh site).

### Hybrid daemon↔daemon transport (Stages 1+2+3 DONE: SYNC + ROUTING + live FAN-OUT over http)
The transport is EXPLICIT per peer (`peers.Peer.Transport()` → "http" if it has an
`ApiAddr`, else "ssh"), NOT an automatic fallback — an http failure is loud, never a silent
ssh downgrade. `peer add --api-addr <host:port> --api-token[-file]` opts a peer into http;
`peer list` shows the transport. ssh stays the default + bootstrap/admin transport.
- **Stage 1 — sync**: `fetchPeerSnapshot` branches in `internal/daemon/meshsync.go`
  (`fetchPeerSnapshotHTTP` reuses a per-peer `client.NewRemote` for keep-alive across the
  1s ticks). Cells `mesh.snapshot`(.http), `mesh.offline-listing`(.http).
- **Stage 2 — routing**: `cmd/sesh/route.go routeMachine(cfg, machine, rest) (handled,err)`
  — http peer + `httpRoutable(rest)` ⇒ set `SESH_REMOTE`/`SESH_API_TOKEN`, return
  handled=false → `main` drops `--machine` and dispatches locally (daemonClient hits the
  peer's API). ssh peer / carve-out ⇒ `routeToMachineSSH`, handled=true. Ticket-owner
  auto-routing (`cmd/sesh/ticket.go`) uses the same path (reloads cfg after the http
  branch; does NOT re-enter the owner check → no loop). **Carve-outs stay ssh** (`httpRoutable`
  returns false): `daemon` lifecycle, `tmux nav`, `tmux stage-file`. Cells `route.parity`(.http).
- **Stage 3 — live fan-out**: `internal/daemon/{fanout,grid}.go fetchPeerThreads/
  fetchPeerGrid` branch on `Transport()` (http → `peerRemoteClient(p).ThreadList/ThreadGrid`).
  So `thread list --all-machines` / `thread grid --all-machines` reach an http peer over
  its API. An http-only peer is now first-class on EVERY cross-machine path. Cells
  `thread.list-all`(.http), `thread.grid`(.http).
- **Parity is matrix-enforced**: every `.http` twin shares the ssh body (`meshTransport`
  param) and registers the peer with a **broken ssh dest** (`http-only.invalid`), so a green
  http cell PROVES HTTP carried it (a silent ssh fallback would fail). 106 cells total.
### Master-tmux infrastructure in sesh (`sesh master`, built 2026-06-09)
Per the corrected boundary directive (`_dev/MASTER.md`): ALL master-tmux infra is in sesh,
not myrig. `cmd/sesh/master.go`: `master up [--machines] [--tmux-conf]` builds the master
server (window per machine, name==machine, automatic-rename off); `master window <machine>`
is a Go reconnect-supervisor (attaches into the machine's WORK server — local or
`ssh -tt … tmux -L <work> attach` — and self-heals with backoff on drop); `master attach`
(syscall.Exec tmux attach); `master down`. Conventions are sesh-internal (sesh builds AND
drives the master). Cells `master.up` + `master.reconnect` (Remote, agnostic; ssh-localhost
peer; reconnect asserts client count hits 0 before healing). myrig collapse = BACKLOG #4b.
**Validated LIVE** 2026-06-09 on mymain↔macbook: `master up` built both windows, supervisors
attached, `tmux nav` jumped between machines over it. GOTCHA: drive the live deployment via
the `seshv2` wrapper or zsh PREFIX-assignments (`SESH_X=v sesh-v2 …`) — NOT `env $E sesh-v2`,
because zsh does NOT word-split `$E`, so the whole string becomes one bogus assignment and
sesh silently falls back to DEFAULT sockets (`mymastertmux`/default home → no peers).
GOTCHA 2 (macOS deploy): scp'ing a cross-compiled darwin/arm64 binary OVER an existing/running
one invalidates its code signature on Apple Silicon → kernel SIGKILLs every invocation (exit
137, NO output — looks like "nothing happens"). Fix: `codesign --force --sign - <bin>` on the
Mac after copy (or rm+recopy / native build). The running daemon keeps working (loaded in
memory pre-overwrite), masking it. This must be in the myrig deploy step (BACKLOG #4a).

- **Real-network proof**: `TestRealCrossHostHTTP` (crosshost_test.go) validates the http
  transport over the REAL mymain↔macbook tailscale link (routing + fan-out + sync), partner
  registered with a broken ssh dest + real api-addr. Verified green 2026-06-09 (mymain→
  macbook, 5.4s) after installing the current darwin/arm64 binary on macbook. Same prereq
  as `TestRealCrossHost` (re-install partner binary after schema changes).

### Lifecycle verbs (post-refactor): orthogonal primitives, no `kill`
`kill` was split into `stop` + `delete` (it was the non-atomic composite `stop && delete`;
the composite belongs in myrig). Two axes:
- **runtime**: `stop` (end agent + tmux session, keep record → dead/resumable) ↔ `resume`.
- **record**: exists until `delete`; `archive`/`unarchive` toggle visibility.
- `delete` refuses a LIVE thread unless `--force` (else it orphans the agent).
- Matrix: `thread.stop` (6 agent cells), `thread.delete` (guard + force), `thread.resume`
  (all 3 agents, continuity). TUI: `x` stop, `d` delete, `a` archive, enter nav.

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

### mesh + cross-host
- `--machine` routing (real ssh hop), `thread.list-all` (`?all-machines`) + `thread.grid`
  (`?with-status`, concurrent) daemon-side fan-out (offline peers → `unreachable`, not
  dropped). TUI is a thin client over `internal/client` only.
- `TestRealCrossHost`: real-host spawn validation. PREREQ (manual): v2 at
  `~/.local/bin/sesh-v2` on both paired machines; run with `MYRIG_MACHINES` exported (it's
  a zsh assoc array). See AGENTS.md "Test environment notes".

### Test isolation (the user runs LIVE old sesh here)
Every sandbox isolates `SESH_HOME` / `SESH_TMUX_SOCKET` / `SESH_MASTER_SOCKET` /
`SESH_CODEX_HOME`; `sandboxEnv` strips inherited `SESH_*`. Never leave a socket/home at
its default in a test.

### Known follow-ups (not blocking)
- `peers.Peer` has no port field → non-22 machines (android-main:8022) are skipped by
  the cross-host test. Add a Port field + `ssh -p` to support them.
- Mesh list is LIVE fan-out only; SPEC hints at replicated/cached listing for offline
  browsing — not built.

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
