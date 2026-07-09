# AGENTS.local.md — sesh v2 working notes

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
