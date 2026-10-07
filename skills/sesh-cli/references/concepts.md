# Thread kinds, state, ownership, and discovery

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
# sesh

Use this skill when the user wants to **use** `sesh` — the multi-machine coding-agent
session manager — not develop it. `sesh` is one Go binary plus a per-machine daemon.

Mental model: each machine runs a **daemon** that owns a local SQLite store, drives a
tmux "work" server, and maintains a background probe of every local thread's live state.
A thread's runtime identity is its **pane** (a `@sesh-thread-id` marker), so a tmux
session may host many threads (their own windows, or splits) — by default a new thread
gets its own session, but see `--into-session`/`--into-window`/`--into-pane`. Daemons are linked into a **mesh** (peers, over ssh or an
HTTP API) so any machine can see and route to threads on any other. The CLI/TUI is a
thin client over the local daemon's HTTP+JSON surface; `--machine <m>` routes a command
to machine `m`. **`sesh` is mechanism, not UX** — it is explicit and machine-readable
(`--json` everywhere); ergonomic shell glue lives in the user's dotfiles.

Run `sesh help` for the command list and `sesh <command> --help` (or `sesh help <command>
<sub>`) for any command — every command and flag is documented there. Prefer reading
`--help` over guessing. `sesh help-tree` prints the entire command surface (every command
and subcommand, each with a one-line summary) as one indented tree — the fastest way to see
everything at a glance. Invoking a command group with no subcommand (e.g. `sesh thread`)
prints that group's full `--help` (not a partial usage line).

## Core concepts

- **Thread** = one coding-agent conversation. A *headed* thread runs the agent live in a
  tmux pane; a *headless* thread is a durable conversation with no pane (turns run
  stateless via `--resume`). The two are not a stored mode — they're inferred at runtime.
- **Two orthogonal state axes** (what the glyphs mean):
  - **head**: `●` headful (a live pane) / `◌` headless (no pane) / `≡` **virtual**
    (a pure grouping node — no agent at all; see *Virtual threads* below) /
    `❯` **shell thread** with a live tmux session, `›` one without (see *Shell
    threads* below). Each KIND draws from a different stroke class — round for an
    agent, a prompt chevron for a shell, stacked lines for a group — so the kind
    reads at a glance rather than by comparing outlines.
  - **busy**: `▶` busy (mid-turn) / `·` idle. **Blank for a shell thread** — it has
    no turn that could be executing, so the axis does not apply.
  - **flag** (last gutter cell): `⚑` **flagged** — this thread needs your
    attention. Auto-set when a turn ends or the agent stalls on a
    question/approval — attended or not (the unattended-only gate was removed
    2026-07-25); NEVER auto-cleared (unflag
    with `f` or `thread flag --off`). `⌁` = auto-flagging **disabled** for
    this thread (e.g. children a parent thread monitors) — deliberately not a
    slashed circle, so it can't be mistaken for the archived `⊘` in the cell
    immediately to its left. A flagged child
    stays VISIBLE under a collapsed parent (fold-piercing) — a flag never
    hides inside a fold.
  So `●·` = headful & idle = **needs input** (waiting for you); `●▶` = working in a pane;
  `◌▶` = a headless turn in flight (wait); `◌·` = idle headless (revivable). A third
  marker shows **descendant activity** (`↓` = a descendant thread — child, grandchild,
  … — is running a turn; blank = none). The running-state glyphs (`▶` and `↓`) render
  **bright green** by default so live activity pops out — on the SELECTED row the tint
  composes with the reverse-video band (the glyph shows as a coloured chip, so ▶/↓/⚑
  keep their colour when selected); tune or clear per glyph via `[[tui.glyph_color]]`
  (names `hold`, `busy`, `descendant`, `flag`). A fourth marker shows attachment (`*` = a tmux
  client is attached), and a fifth shows **archived** (`⊘` = the thread is archived —
  it appears in the default view only while still headful). The TUI's gutter header for
  the core three is `HBD` (head, busy, descendant).
  - **hold** (the LEADING cell, before the head): `⧗` the thread is parked by its
    OWN deadline / `⧖` parked by an ANCESTOR's / blank not parked. The pair is
    the same shape in two states, like `●`/`◌`, and the split is actionable
    rather than decorative: an own hold is removed by clearing it, an inherited
    one **cannot be** — the effective hold is `max(own, ancestors')`, so it needs
    `thread hold --release`. A RELEASED thread is not parked and so carries no
    sigil; the `~<date>` in the HOLD column is what reports a release in force.
    The cell is shared with move mode's `↕`, which wins while a row is being
    moved — that cell was otherwise blank on every row, which is what lets the
    sigil cost no width. NB on-hold threads are hidden from the default `active`
    view, so the sigil is something you see in `all`, `on hold`, and any custom
    view that admits parked threads.
- **Machine = origin + owner.** A thread lives on the machine that spawned it; mutations
  route to that owner (`--machine`, or auto for tickets). Cross-machine reads come from
  the mesh.
- **Archived** is orthogonal to liveness — a parked record, hidden from the active list,
  still resumable.
- **Agents**: `claude`, `codex`, `pi`. Spawn policy (yolo/default/sandbox) comes from
  `[spawn]` config or `--yolo`/`--sandbox`.
  - **Workspace-trust prompts are pre-answered.** Every headed claude/codex launch
    (new, revive, `--into-pane`) first marks the thread's cwd trusted in that agent's own
    config — claude: `projects[<cwd>].hasTrustDialogAccepted` in `~/.claude.json` (or
    `$CLAUDE_CONFIG_DIR/.claude.json`); codex: `[projects."<cwd>"] trust_level` in
    `config.toml` — so the agent comes up at its input prompt and a `thread send` fired
    right after spawn lands in the agent, not in a "Quick safety check … trust this
    folder?" dialog (which would otherwise eat it: Enter there picks "No, exit").
    Claude's dialog fires for every fresh git-repo box even under
    `--dangerously-skip-permissions`, since its trust lookup stops at the repo root.
    sesh writes exactly that one key (atomically, never a corrupt or truncated file
    — an unparseable config is refused loudly instead) and nothing when the cwd is
    already trusted. It does NOT pre-approve CLAUDE.md external imports; that dialog
    is separate and only appears for a project CLAUDE.md that imports outside the tree.
- **Parent/child** threads form a tree (a supervisor thread and its sub-agents); the TUI
  renders it collapsibly. **`thread new` defaults to childing the new thread to the current
  one** (see [creating threads](threads.md) — pass `--no-parent` for a standalone/root thread).
  Deleting a thread **promotes its children** to the deleted thread's own parent
  (grandparent; root if it had none) — parent ids never dangle.
- **Shell threads** (`sesh shell …`, glyph `❯`/`›`, `agent_kind` reads `shell`) are
  **tracked tmux SESSIONS**. Where an agent thread's durable content is its conversation,
  a shell thread's is its **working directory**: headful means a live session exists,
  headless means it is a remembered place, and `thread resume` re-creates the session in
  the recorded cwd. Runtime identity is a session-scoped `@sesh-shell-id` marker, so a
  session rename does not lose it and the session name is descriptive only.
  - **They have a runtime but no conversation.** `enter`/nav, `send`, `capture`, `stop`,
    `resume` all work; `fork`, `transcript`, `send-headless` and `--model` refuse loudly.
  - Everything else is the ordinary `thread` surface: list, rename, tag, pin, hold,
    archive, delete, reparent, meta, notify, flag.
  - `shell new --cwd <dir> [--name X]` records and starts one (`--no-start` records the
    place only). `shell enter --cwd <dir>` is **idempotent** on `(cwd, name)` — it enters
    the existing one (restarting a session that went away) or creates it. Several shells
    per cwd are legal but need **distinct names**; `shell new` refuses a duplicate.
  - `shell here` promotes the session you are sitting in; `shell promote --session <name>`
    promotes a named one. `shell sessions` lists every live session on the work server,
    classified `shell` (tracked) / `agent` (hosts agent panes) / `ghost` (untracked — the
    promote target) / `stale` (a marker whose record is gone).
  - `thread send --id <shell> [--pane %12 | --window N]` addresses ONE pane of the
    session (default: its active pane). `shell panes --id X` lists them.
  - `shell info --id X --json` returns the socket path and a ready-to-paste `tmux_prefix`
    — the deliberate **raw-tmux escape hatch**, since sesh does not reimplement tmux.
  - **Stopping a shell kills its whole session**, including any agent-thread panes inside
    it, so it refuses without `--force` when it hosts them. `delete` never kills: it
    refuses while the session lives, and `delete --force` drops the record, clears the
    marker and leaves the session running as a ghost you can re-promote. To get it out of
    the active view while still working in it, **archive** it.
  - In the TUI: **`S`** opens the **shells view** — every live session on every reachable
    machine, classified. It is a list surface like the grid and behaves like one: ↑/↓ (or
    j/k) move and the viewport FOLLOWS the selection, ^j/^k scroll a half-page, `/` filters
    (fuzzy, over session name + machine + path + the agent threads inside — enter applies,
    esc clears), the wheel moves the selection, a click selects a session and a double
    click enters it. `enter` jumps to one, **`P`** promotes it to a tracked shell thread,
    `x` kills it (confirmed; the confirmation names any agent threads that would die with
    it), `R` refreshes, `esc` closes (clearing an active filter first). The cursor is
    ANCHORED to its session across a refresh, so a promote/kill never slides it onto a
    different one.
- **Virtual threads** (`thread new --virtual --name X`, or the `new-virtual` command in the TUI) are
  grouping nodes WITHOUT an
  agent: no pane, no conversation, `agent_kind` reads `virtual`, glyph `≡`. Use one to
  group threads under a parent that isn't (yet) real work: parent/reparent threads under
  it, tag/archive/hold it (a hold on the group parks the whole subtree via inheritance).
  Every agent verb (`send`, `send-headless`, `headful`/`resume`, `capture`, `transcript`,
  fork) refuses loudly; in the TUI, Enter shows a warning instead of entering. Convert it
  into a REAL thread in place with `thread realize --id <id> --agent claude|codex|pi
  [--cwd <dir>]` — the id (and children, tags, holds, ticket bindings) survive, and the
  result is a fresh never-started headless thread: enter it or `send-headless` to start
  the conversation. `--cwd` at realize defaults to the cwd stored at creation (creation
  cwd is optional; one is required by realize time).
