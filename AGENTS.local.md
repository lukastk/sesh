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
Done: mymain/macstudio/termux (myrig pulled + shell.sh rendered; termux uv→python3
fallback). **macbook (the master machine) PENDING — kept sleeping; needs: git -C
~/mysetup/myrig pull && (uv run|python3) scripts/install-home.py $MYRIG_TARGETS && tmux -L
sesh-master source-file ~/.sesh/myrig/tmux.master.conf.** termux likely lacks boxyard
(python deps) so mt-enter-box there would no-op; the master normally runs on macbook.

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
file (leaves the repo dirty). Deployed d210881 + sourced on termux (clean pull) + mymain +
macstudio; mt-enter-box lists 223 boxes on termux. **macbook (primary master) still PENDING
— asleep; pull + source there when up.**

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
DEPLOY (schema 11 = daemon RESTART): LIVE on mymain (live-smoked create/get/set/delete — no
`description` in output, migration 13 clean), macstudio, macbook. **macbook had a local
uncommitted menus.sh edit (he'd added mt-enter-new-thread-here to MT_QUICK_CMDS) — stashed →
pulled → re-applied his -here precisely → re-rendered, so his customization survived.**
**termux** then PENDING (not in mymain's peers; reached as `lukas@android-main:8022`).
Native build per machine (.new+mv; mac auto-signs); supervisorctl restart sesh-daemon
(mymain/macs); install-home render (macs/termux need `uv run --with jinja2`); source-file
work+master confs on the running servers.

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
DEPLOY (schema 14 = daemon RESTART): mymain/macstudio/termux done; **macbook ASLEEP (still
schema 13) — PENDING: git pull both, build+restart sesh-daemon, render myrig.** Schema 14 is
additive/mixed-mesh-safe: a move TO a schema-13 macbook fails LOUDLY (404 on blob add, source
intact) — PROVEN live (the move aborted before delete when macstudio was briefly still on 13).
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
macbook:7878 + sesh_fallback_endpoint=macstudio:7878 (backup at data.json.bak). DEFERRED (flagged):
the deploy-to-new-thread modal takes cwd as a path; box/mysetup-folder cwd pickers are a later
convenience. NOT done: the plugin is installed only on macbook (where Obsidian runs); macstudio
(fallback) + mobile would need the plugin + settings too if used there.
