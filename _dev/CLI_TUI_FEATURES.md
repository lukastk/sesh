# CLI & TUI feature batch — 2026-06-11

Six features requested by Lukas (2026-06-11). Same contract as `PARITY_ROADMAP.md`:
each built **full-featured, not minimal** (no-scarcity mindset), researched against the
current codebase (and v1 where relevant) *before* implementation, and landed honestly —
loud errors, no defensive fallbacks, real matrix cells / TUI claims where the feature is
conformant, and the rendered grid kept truthful.

Status legend: `[ ]` todo · `[~]` in progress · `[x]` shipped (gate green + deployed).

**Decisions confirmed by Lukas (2026-06-11):** G5 — fold/unfold moves to the **left/right
arrow keys**, `h`/`l` become horizontal column scroll. G2 key = `T`, G3 key = `P`. All other
"Open decisions" default to the first option listed.

## Checklist (suggested dependency / size order — small & self-contained first)

- [ ] **G1** `thread capture` — live tmux pane capture (v1 `pane-capture` port)
- [ ] **G2** TUI remove-tag action (popup tag picker → `thread tag --remove`)
- [ ] **G3** TUI set-parent action (key → paste parent UUID → `thread reparent`)
- [ ] **G4** Per-column colours (`[tui]` config; default green CWD, blue NAME)
- [ ] **G5** TUI scrolling: `ctrl+j/k` vertical viewport scroll + `h/l` horizontal column scroll
- [ ] **G6** CLI help: flesh out every command/subcommand description + real `--help`

Each section below is self-contained: **Want → Research (current state) → Design →
Files → Tests → Open decisions**. The "Open decisions" are the forks I want Lukas to
confirm before I build that feature (I'll default to the first option if he doesn't object).

---

## G1. `thread capture` — live tmux pane capture

**Want:** v1 had a pane-capture command that was "extremely useful" — read the live tmux
pane content of a thread. Key use-case: a parent thread supervising a child agent (e.g.
claude) needs to see that the child has stalled on a multiple-choice prompt.

**Research — v1 (`github.com/lukastk/sesh`):** `internal/cli/pane_capture.go`.
- Verb: `sesh pane-capture <uuid> [-n N] [--json]`. `-n/--lines` default 50; `0` = visible
  area only.
- tmux call: `tmux -S <socket> capture-pane -t <pane-or-session> -p` (+ `-S -<N>` for the
  last N lines of scrollback). Plain text, no `-e` colour escapes.
- Thread→pane resolution: live `tmux.Walk()` scan, falling back to the stored daemon
  record. Local-only — refuses a remote-owned record loudly (pane ids are per-tmux-server).

**Research — v2 current state:** the primitives already exist.
- `internal/tmux/threads.go:82` `Server.CapturePane(pane string)` = `capture-pane -t <pane> -p`
  (already used by the activity probe).
- `internal/tmux/threads.go:39` `Server.FindPaneByThreadID(id)` → `(api.PaneLocator, found, err)`
  (marker-based: the `@sesh-thread-id` pane option). This replaces v1's Walk+fallback.
- No production capture command/endpoint exists yet (the conformance harness uses
  `capture-pane` for testing only).
- Endpoint pattern to follow (request → client → daemon handler → route): exactly how
  `thread pane` / `tmux info` are wired (`internal/api/thread.go`, `internal/client/thread.go`,
  `internal/daemon/thread.go`, `internal/daemon/server.go`).

**Design:**
- New verb **`sesh thread capture --id <id> [--lines N] [--json]`** (`--lines` default 50,
  `0` = visible only; matches v1's `-n`). `--id` accepts the usual id-prefix resolution.
- Endpoint `GET /v1/threads/capture?id=&lines=` → `ThreadCaptureResponse{schema,id,content}`.
- Daemon handler: resolve thread (404 if unknown) → `FindPaneByThreadID` → **409 loud if no
  live pane** (dead/headless thread has nothing to capture — per house rules, no empty-string
  fallback) → `CapturePane` → trim to last N lines when `lines>0`.
- **Cross-machine: HTTP-routable.** It's a read-only probe with no socket-path dependency,
  so it routes via the normal `--machine` path (http peers over their API, ssh peers over
  the hop). This is strictly better than v1's local-only restriction — `--machine X` resolves
  the pane *on X's daemon*, where the pane id is meaningful. No machine-guard needed because
  resolution happens daemon-local by construction.
- Trimming happens daemon-side (so we don't ship a huge scrollback across the wire when the
  caller only wants 50 lines).

**Files:** `internal/api/thread.go` (+req/resp types, schema bump only if a stored shape
changes — it doesn't, so no migration), `internal/client/thread.go` (+`ThreadCapture`),
`internal/daemon/server.go` (+route), `internal/daemon/thread.go` (+`handleThreadCapture`),
`cmd/sesh/thread.go` (+`threadCapture` + dispatch case + `usage`).

**Tests:** matrix cell **`thread.capture`** — honest e2e: spawn a real agent in a real tmux
pane (all three agents), let it render, `thread capture` and assert the returned content
contains the agent's actual on-screen text. Both `local` and `remote` (ssh-localhost peer)
so the routed path is exercised. Plus a unit/claim for the **409-on-dead** loud error (stop
the thread, capture, assert the error — not an empty string).

**Open decisions:**
1. **Verb placement** — `thread capture` (thread-centric, my default) vs `tmux capture`
   (tmux-layer, closer to v1's `pane-capture`). I lean `thread capture`: callers think in
   threads, and id-resolution + routing already live on the thread verbs.
2. **TUI affordance** — optionally a `P`-ish key (lower `v`/"peek"?) that pops the captured
   pane in a scrollable popup, for the supervising-from-the-grid case. The user asked for the
   *command*; I'll ship the command first and offer the popup as a fast follow if wanted.
3. **Default line count** — keep v1's 50? (My default: yes.)

---

## G2. TUI remove-tag action

**Want:** no way to remove tags. Add a TUI action: select a thread, press a key, a popup
lets you pick which tag to remove.

**Research — current state:**
- `thread tag --id <id> --remove <tag>` **already exists** (`cmd/sesh/thread.go`,
  `client.ThreadTag` → `POST /v1/threads/tag`, repeatable `--remove`). So this is a
  **TUI-only** feature — no CLI/daemon work.
- TUI tag-add flow: `t` → `promptTag` line-prompt → `tagRow()` → `routedVerb("tag",
  patch, "--add", tag)` with an optimistic `addTags` patch (`internal/tui/model.go`).
- Popups today: the UUID popup (`y`, `handleUUIDKey`) is a single-screen popup with `c`=copy,
  any-other-key=close. There is **no selectable-list popup primitive** yet — this feature
  introduces one (reusable by G3 if needed).
- `rowPatch` (model.go) has `addTags` but **no `removeTags`** — needs adding for the
  optimistic clear.

**Design:**
- New key **`T`** (capital — `t` stays add) opens a **tag-picker popup** listing the selected
  row's current tags, cursor-navigable (`j/k`/arrows), `enter` removes the highlighted tag,
  `esc`/`q` closes. If the row has **no tags**, show a brief "no tags" note instead of an
  empty popup (this is a legitimate empty state, not a masked bug, so a note is correct —
  not a silent no-op).
- Removal → `routedVerb("tag", patch, "--remove", tag)`; optimistic patch gets a new
  `removeTags []string` field; `rowPatch.apply` filters those tags out of the displayed set;
  `merge`/`satisfied` updated symmetrically.
- New popup mode `popupTagRemove` with its own state (`tagPickRow api.ThreadRow`,
  `tagPickCursor int`). Modeled on `handleUUIDKey` but with a cursor + a list render.

**Files:** `internal/tui/model.go` (popup state, `handleKey` `T` branch, `handleTagRemoveKey`,
`removeTagRow`, `rowPatch.removeTags` + apply/merge/satisfied, `View` popup render).

**Tests:** TUI claim **`action-untag`** — seed a thread with two tags, open the popup, remove
one, assert (a) the optimistic display drops it immediately and (b) the daemon's stored tags
no longer contain it; assert the *other* tag survives. Plus the empty-state note (no-tags row →
popup shows the note, removes nothing).

**Open decisions:**
1. **Key** — `T` (my default, mnemonic pair with `t`). Alternatives if `T` feels off: `D`
   already?-no (`d`=delete, `D` free). I'll use `T`.
2. **Multi-remove** — allow selecting several tags before closing, or one-removal-then-stay-
   open? My default: removal stays in the popup (so you can strip several), `esc` closes —
   matches the "select which tag to remove" phrasing while not being annoying for multiples.

---

## G3. TUI set-parent action

**Want:** a TUI action to set a thread's parent: select a thread, press a key, paste the full
UUID of the parent.

**Research — current state:**
- `thread reparent --id <id> --parent <new-parent>` (and `--root` to detach) **already
  exists** (`cmd/sesh/thread.go`, `client.ThreadReparent`). So again **TUI-only**.
- The line-prompt machinery (`promptRename`/`promptTag`, `handlePromptKey`) is exactly the
  shape needed for "paste a UUID". `promptKind` is an enum — add `promptReparent`.
- The tree already renders parent/child (`row.Parent`, `visibleMatches`, `expandAncestors`).
  After a reparent, the tree reshapes — optimistic patching of *structure* is more involved
  than a name/tag patch.

**Design:**
- New key **`P`** (capital; lower `p` reserved — see note) opens a `promptReparent` line-prompt
  pre-labelled "parent UUID (empty = root)". Submit → `reparentRow()`:
  - empty input → `thread reparent --id <id> --root`
  - non-empty → `thread reparent --id <id> --parent <uuid>` (routed to the owner via
    `routedVerb`).
- **Validation is daemon-side and loud** — `reparent` already rejects unknown parents and
  cycles; the TUI surfaces that error in its error line (no client-side guessing).
- **No structural optimistic patch** (v1-parity tree reshaping mid-patch is a code smell
  risk). Instead: on success, trigger an immediate reconcile fetch and `expandAncestors` so
  the moved node is visible under its new parent. The metadata-patch system stays for
  name/tag/notify; structure refetches. (If the ~1 tick of latency bothers Lukas in live use,
  revisit — flagged rather than hacked.)

**Files:** `internal/tui/model.go` (`promptReparent` kind, `handleKey` `P` branch,
`handlePromptKey` already generic, `reparentRow`, prompt label in `View`).

**Tests:** TUI claim **`action-reparent`** — two threads A,B at root; select B, `P`, paste A's
full id, submit; assert B's stored `parent == A.id` and B renders nested under A. Plus the
detach path (empty input → root) and the loud cycle rejection (parent B onto its own
descendant → error surfaced, no change).

**Open decisions:**
1. **Key** — `P` (my default). Note `p` is currently unbound; I'm reserving capital `P` to
   avoid colliding if we later want lower `p` for "peek"(G1). Confirm or swap.
2. **Paste UX** — the prompt accepts a pasted UUID as literal runes (works with bracketed
   paste through tmux). Should I also accept an **id-prefix** (resolve like the CLI does) so
   you don't need the full UUID? The user said "full uuid", but prefix-resolution would be
   strictly more ergonomic and the CLI already does it. My default: accept both (try exact,
   else prefix-resolve via the daemon), erroring loudly on an ambiguous/empty match.

---

## G4. Per-column colours

**Want:** set colours for columns; default **green CWD** and **blue NAME**.

**Research — current state:**
- `internal/tui/columns.go` renders cells with width/pad but **no per-cell colour**. Styling
  today (model.go): `styleHeader` (bold), `styleSelected` (reverse), `styleDim` (faint),
  `styleMatch` (bold magenta for filter hits). Colour is applied *after* padding so widths
  stay rune-true.
- Config (`internal/config/tui.go` `TUIConfig`): `Columns`, `ColumnMoves`, `ExpandChildren`,
  `Views`. **No colour table.**
- Interaction constraints: the **selected row** is full-line reverse video (per-cell colour
  must yield to it, or it'll fight the reverse), and **filter-match highlight** recolours
  matched runes. Precedence must be defined.

**Design:**
- Config: a `[[tui.column_color]]` array-of-tables — `{ name = "cwd", color = "green" }`.
  Colours accept lipgloss-friendly names (`green`,`blue`,`red`,…) and `#rrggbb` /
  256-palette numbers. Unknown column name or unparseable colour = **loud config error**
  (consistent with `Columns`/`ColumnMoves` validation).
- Built-in defaults applied when the user sets nothing: **CWD green, NAME blue** (overridable;
  setting `[[tui.column_color]]` for a column replaces its default, and an explicit empty
  color clears it). myrig's `config.toml` is where the *policy* default lives; sesh ships the
  green/blue defaults as Lukas asked, but document that myrig owns final say.
- Render precedence (highest wins): **selected-row reverse** > **filter-match highlight** >
  **per-column colour** > default. Implementation: in `renderCells`, wrap the *padded* cell
  in its column colour *unless* the row is selected (selected path already reverses the whole
  line) — and the match-highlight `highlight()` already overrides per-rune, so it composes on
  top. Verify the match style still reads against a coloured cell (it's bold magenta — fine on
  green/blue; note if not).

**Files:** `internal/config/tui.go` (`ColumnColor` struct + parse/validate), `internal/tui/
columns.go` (colour map plumbed into `renderCells`), `internal/tui/model.go` (build the
lipgloss style map once; pass into the column renderer), myrig `config.toml` (ship the
green/blue default there too so it's visible/tunable).

**Tests:** unit test on `renderCells` asserting the ANSI SGR for the configured colour wraps
the CWD/NAME cell and **not** the others; a config-validation unit (unknown column / bad
colour → loud error); a TUI claim that a coloured grid still renders correct *widths* (colour
must not shift columns) and that a selected row suppresses per-cell colour in favour of
reverse.

**Open decisions:**
1. **Config shape** — `[[tui.column_color]]` array-of-tables (my default, matches
   `[[tui.column]]` moves) vs a flat `[tui.colors]` map (`cwd = "green"`). The array-of-tables
   is consistent with the existing column-move config; the flat map is terser. I lean
   array-of-tables for consistency.
2. **Header colouring** — colour only the cells, or the column header too? My default: cells
   only (headers stay bold/neutral so the colour reads as data, not chrome).
3. **Scope** — full-cell colour only, or also support colour-by-predicate later (v1 had rule
   columns)? Out of scope here; static per-column colour now, predicate colouring deferred.

---

## G5. TUI scrolling (`ctrl+j/k` vertical, `h/l` horizontal)

**Want:** scroll up/down with `ctrl+j` / `ctrl+k`; `h`/`l` move left/right to see columns
that are clipped.

**Research — current state (the consequential one):**
- **No vertical viewport exists.** The TUI renders *all* visible rows into the View string and
  relies on the terminal's own scrollback; the cursor can scroll off-screen with many threads.
- **No horizontal clipping exists.** Full-width columns (NAME/CWD) size to their longest cell
  and the row can exceed terminal width with no clamp (columns.go TODO even notes "horizontal
  scroll" as future work). So today there's nothing to scroll *to* horizontally until we add
  clipping.
- **`h`/`l` are currently fold/unfold** (collapse/expand tree node) — same as `left`/`right`
  arrows. This feature **repurposes `h`/`l`** → horizontal scroll, leaving fold/unfold on the
  arrow keys. (Called out as Open decision 1 — it's a binding change.)
- `j`/`k` move the cursor (wrapping). `ctrl+j`/`ctrl+k` are currently unbound.

**Design (two parts):**

*Vertical viewport.* Introduce a real scroll offset.
- Add `m.vOffset int`; compute `bodyHeight` from `m.height` minus header/footer chrome.
- Render only `rows[vOffset : vOffset+bodyHeight]`.
- `j`/`k` move the cursor and the viewport **follows** (clamp cursor into view; scroll when it
  would leave). `ctrl+j`/`ctrl+k` scroll the viewport by a **half-page** without forcing the
  cursor (cursor re-clamped into the visible window). A scroll indicator (e.g. `▲N more` /
  `▼N more`) in the footer when content is clipped — silent truncation reads as "you've seen
  everything", which we explicitly avoid.

*Horizontal column scroll.* Add clipping + an offset.
- Add `m.hOffset int` (in columns, not chars — cleanest: offset = number of *leading data
  columns* scrolled past; the glyph/attachment gutter + the NAME tree column can be treated as
  "frozen" so the tree stays readable — decision 2).
- When total rendered width > `m.width`, clip the row to the window and render only columns
  from `hOffset` onward that fit; `h`/`l` decrement/increment `hOffset` (clamped so you can't
  scroll past the last column). Footer shows `‹more` / `more›` markers when clipped.
- Header row scrolls in lockstep with the body so labels stay aligned.

**Files:** `internal/tui/model.go` (offsets, `ctrl+j/k` + `h/l` handlers, viewport math,
`View` windowing, footer indicators), `internal/tui/columns.go` (horizontal clip/window of the
rendered cell list + matching header window).

**Tests:** unit tests on the viewport math (cursor-follow, half-page scroll, clamp at both
ends) and the horizontal window (clip at width, clamp, frozen columns). TUI claims
**`scroll-vertical`** (seed >screenful of threads in a fixed-size pane, `ctrl+j`, assert the
top row changes and the "more" indicator appears/clears at the ends) and **`scroll-horizontal`**
(narrow pane so columns clip, `l`, assert a previously-clipped right-hand column becomes
visible and a left one scrolls off; `h` reverses).

**Open decisions:**
1. **`h`/`l` repurpose** — move fold/unfold to **arrow keys only** and give `h`/`l` to
   horizontal scroll (my default, matches the request literally). The alternative is keeping
   `h`/`l` as fold and binding horizontal scroll to something else (e.g. `H`/`L` or
   `ctrl+h`/`ctrl+l`) — but that contradicts "h and l should work for moving left and right".
   I'll go with the repurpose unless Lukas wants fold kept on `h/l`.
2. **Frozen columns** — keep the state-glyph gutter + NAME (with its tree rails) pinned while
   the rest scroll horizontally (my default — scrolling the tree column away makes the
   hierarchy unreadable), vs scroll everything uniformly. Confirm.
3. **`ctrl+j/k` semantics** — half-page viewport scroll (my default) vs single-line scroll vs
   full-page. Half-page is the common pager feel.

---

## G6. CLI help — descriptions + `--help`

**Want:** the `seshv2` CLI descriptions are bare-bones and there's no `--help`. Flesh it out so
an agent can learn the tool purely from `--help` output.

**Research — current state:**
- Hand-rolled dispatch (`cmd/sesh/main.go`), **no flag library**. `-h`/`--help`/`help` only
  hit a single hardcoded top-level `usage()` (a flat command list, no flag/subcommand detail).
- ~23 top-level commands, many with subcommands (`thread` alone has ~20). Every subcommand
  builds a `flag.NewFlagSet` but **all flag help strings are empty (`""`)** and there's no
  per-command `--help` — errors are terse one-liners (`"thread new: --agent, --name and --cwd
  are required"`).
- `--machine` is a pseudo-global; meta-commands (`peer`/`matrix`/`master`/`help`) are excluded
  from routing (`route.go`).

**Design (architecturally sound, no big-bang rewrite):**
- Introduce a small **help registry** — a structured description tree (`cmd/sesh/help.go`):
  for each command and subcommand, a `{ summary, usage, long, examples }`; flags described
  via real help strings on each `flag` definition (fill in the empty `""`s).
- Intercept `-h`/`--help` at **every** dispatch level (top-level *and* each subcommand
  dispatcher), printing that node's help: summary + usage line + flag table (from the
  FlagSet's `PrintDefaults`, which now has populated descriptions) + examples + the list of
  subcommands with their one-line summaries. This keeps the existing hand-rolled dispatch
  (no Cobra) but makes help complete and uniform.
- **Agent-legibility is the bar:** every command's help must state what it does, its required
  vs optional flags, the cross-machine `--machine` behaviour where relevant, the `--json`
  shape where it emits JSON, and a worked example. A top-level `sesh help <command>` mirrors
  `sesh <command> --help`.
- Keep it **mechanism-honest**: help describes the explicit flags as they are; no implying
  magic defaults that don't exist.

**Files:** `cmd/sesh/help.go` (new registry + renderer), `cmd/sesh/main.go` (richer top-level
`usage`, route `-h/--help/help <cmd>`), and a `-h/--help` check + populated flag descriptions
in **every** `cmd/sesh/*.go` subcommand dispatcher.

**Tests:** golden/unit tests in `cmd/sesh` — for **every** registered command and subcommand,
assert `--help` exits 0 and its output contains the command name, a non-empty summary, and a
`Usage:` line; a meta-test that **every** dispatch case has a help entry (mirrors the matrix's
"no silent gaps" ethos — a command with no help is a failing test, not a blank). Assert flag
descriptions are non-empty for each FlagSet.

**Open decisions:**
1. **Scope of "fleshed out"** — full per-command help with examples for all ~23 commands
   (my default, since the stated bar is "an agent can use it from `--help` alone") vs a lighter
   summary-only pass. I'll do the full version; it's mechanical and high-value.
2. **`help` registry vs inline `FlagSet.Usage`** — a central registry (my default: one place to
   audit completeness, powers the meta-test) vs each command owning its own `Usage` closure.
   Central registry wins for the honesty meta-test.
3. **Man-page / markdown export** — optionally a hidden `sesh help --markdown` that dumps the
   whole tree (handy for docs/agents). Nice-to-have; flag it, build only if wanted.

---

## Cross-cutting notes

- **Honesty:** G1 and G5 get real conformance cells/claims; G2/G3 get TUI claims; G4/G6 are
  primarily unit/golden + a claim where observable. Nothing here mocks the thing under test.
- **Deploy:** G1 touches the daemon (new endpoint) → daemon restart on both machines after the
  binary build. G2–G6 are TUI/CLI-binary only (the TUI/CLI run the binary directly) → just
  `~/.local/bin/sesh-v2` update, no daemon restart. G4 also ships a myrig `config.toml` default.
- **Schema:** none of these change a stored shape, so **no migration** (G1's response is a new
  read-only endpoint; G4's colour config is TUI-local). Confirm during build that G3's reparent
  needs none (it reuses the existing verb).
- **Binding changes to call out to Lukas:** G2 adds `T`, G3 adds `P`, G5 **repurposes `h`/`l`**
  (fold→arrows) and adds `ctrl+j`/`ctrl+k`. These are the only user-visible muscle-memory
  changes.
