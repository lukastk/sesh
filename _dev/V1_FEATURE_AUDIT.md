# V1 → V2 feature audit (discussion document)

**Date:** 2026-06-10.
**Provenance:** multi-agent audit of old sesh v1 (`/home/lukastk/mysetup/sesh` — Go binary, TUI, daemon, plus the myrig shell layer in `~/.myrig/zshenv/sesh.sh` / `master-tmux.sh`), each feature compared against the v2 codebase (this repo) and the v2 myrig layer (`/home/lukastk/mysetup/myrig/home/.sesh-v2/myrig/`). Overlapping audit entries have been deduplicated. Purpose: Lukas decides feature-by-feature what to port. Statuses: **present** (v2 has it), **partial** (some of it), **missing** (nothing), **superseded** (v2 deliberately solved it differently — porting is probably regression).

---

## Master table

### TUI — filter & search

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| fzf-style fuzzy filter mode | Type-to-filter; `sesh tui` OPENS in filter mode; Esc applies the filter and drops to normal mode (manage the narrowed list); `/` re-enters; full caret editing (←/→, ^a/^e, mid-query insert); footer shows match count | default on launch; `--no-filter`; `/`; Esc | **partial** | v2 TUI has no filter at all (`internal/tui/model.go:231-255` is the whole key surface). The *select-a-thread* workflow is covered by real fzf in myrig `mt-enter-session` (shell.sh.jinja:124-207, prefix+a/A), but the apply-filter-then-manage flow has no equivalent. v1: `internal/tui/update.go:287-397` |
| Fuzzy match algorithm | fzf FuzzyMatchV1-style scoring (boundary/camel/consecutive bonuses), smart-case, matched-rune bold+underline highlights in cells | automatic | **missing** | No fuzzy code in v2; fzf supplies it in the pickers. Would need re-porting from v1 `internal/tui/fuzzy.go` if v2 grows a filter mode |
| Configurable match columns + UUID toggle | `--match name,cwd,...` picks filter columns; ctrl+t toggles cols↔full-UUID search; footer shows active target | `--match`; ctrl+t | **missing** | myrig fzf matches one fixed composed display field; id rides in a hidden tab field, unsearchable |
| Score ranking, best-match-last | While filtering: flat list sorted by score, best match at the BOTTOM next to the prompt, cursor snaps to it on every edit | automatic | **partial** | fzf gives score-rank + cursor-on-best in the pickers (best-first/top via `--reverse`, not v1's bottom-anchor) |

### TUI — views

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| Tab view cycle: active / archived / all | Tab cycles membership views; current view named in footer | Tab | **partial** | v2 `Model.archived` exists and `fetch()` honors it (model.go:35,154-156) but NOTHING sets it — archived rows are unreachable from the TUI. Archived browsing = CLI `--archived` + myrig `mt-enter-session --archived` (prefix+A) only |
| Custom views: `[[tui.filters]]` + predicate language | Named predicate filters appended to the Tab cycle (`and/or/not`, `==/!=/~/!~`, selectors over state/agent/tags/meta); compile-time loud errors | config.toml | **missing** | No predicate grammar anywhere in v2 (v1: `internal/tui/predicate.go`). v1's state selectors (turn/live) would need re-mapping to v2's head/busy/attached axes |
| Background archived load | Active rows paint instantly; archived (thousands) merge in async with a loading hint | automatic | **missing / obviated** | v2's fetch is an instant local read of the mesh cache, so the *speed* motivation is gone — but there is no archived load path at all |

### TUI — layout & rendering

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| Built-in column set | ID, NAME, CWD, AGENT, MACHINE, SOCKET, TAGS, CREATED; dynamic NAME/CWD widths | automatic | **partial** | v2: HB-glyphs+attach, MACHINE, AGENT, HEAD, BUSY, NAME, TAGS in one hardcoded Sprintf (model.go:483,497-498). Missing: ID, CWD, CREATED (data IS on the wire — api.Thread.Cwd/CreatedAtUnix); SOCKET is moot (one work socket/machine by design) |
| Column-width control | `--full` per-column full-width set; ctrl+w all-full toggle; `i` ID-column toggle; horizontal scroll (ctrl+h/l, wheel) of overflowing rows | flags + keys | **missing** | v2 hard-truncates (machine 12 / agent 7 / name 20) with no way to see the full value |
| Dynamic columns `[[tui.columns]]` | Config-defined metadata-value or predicate-glyph columns, anchored before/after built-ins | config.toml | **missing** | Needs the predicate grammar AND a per-thread metadata story (v2 threads have tags only) |
| CWD relabeling: `[[tui.cwd_rules]]` / `$SESH_CWD_FORMATTER` / `sesh cwd-label` | Regex rules (or external command) relabel the CWD column live at render; `cwd-label` exposes the transform to scripts | config.toml / env / CLI | **partial** | The mechanism survives, repurposed: `[[session_name]]` rules (`internal/config/naming.go`) bake the cwd-derived label into the SESSION NAME at spawn/revival. No TUI CWD column, no live re-render, no CLI transform verb. External-formatter fallback (silent fall-back-to-raw) conflicts with v2's loud-errors rule |
| Two-glyph status gutter | Two orthogonal per-row glyphs: live ●/○ + turn ◆/◇ | automatic | **present** | v2's two-axes model is this idea made wire-level and conformance-tested: ●/◌ head, ▶/· busy, `?` skew, `*` attached (model.go:450-498) |
| Row emphasis | NAME blue / CWD green tints, busy bold, archived gray + ▒ marker, reverse-video cursor | automatic | **partial** | v2 has bold header + reverse cursor only. Archived styling moot until archived rows are viewable |
| Truncation | Rune-counted clamp with `…` | automatic | **partial** | v2's `trunc()` is BYTE-counted (model.go:522-527) — splits multi-byte runes at the boundary. Arguably a small bug regardless of porting decisions |

### TUI — actions & input

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| Enter = open/attach | Select row → enter the session (auto goto if live / resume if detached) | Enter / double-click | **present** | v2 is richer: `navSelected` (model.go:259-334) revives headless·idle threads (routed cross-machine), then `tmux nav` — in-client / master path / plain-shell attach. Conformance-claimed |
| Archive / unarchive | `a` with y/N confirm; `u` unarchive | a / u | **partial** | v2 `a` archives with NO confirm; no `u` (archived rows invisible anyway). CLI + myrig prefix+Tab toggle exist |
| Archive+kill composite | `K`: confirm, archive, kill pane | K | **superseded** | v2 deliberately split kill into `x` stop / `d` delete / `a` archive (orthogonal lifecycle verbs; composites belong in myrig) |
| Delete with confirm | ctrl+d, y/N confirm | ctrl+d | **partial** | v2 `d` deletes on one keypress, no confirm; safety moved server-side (daemon refuses live-thread delete without --force) — but an idle thread dies on a single key |
| Rename prompt | `r` line prompt pre-filled with name | r | **partial** | Backend complete (`thread rename` → client → daemon). No TUI prompt widget; `r` is taken (refresh) |
| Tag add prompt / tag remove chooser | `t` line prompt; `x` multi-remove chooser overlay | t / x | **partial** | Backend complete (`thread tag --add/--remove`). No prompt/overlay machinery in v2; `x` is taken (stop) |
| UUID popup + clipboard copy | `y` shows full UUID, `c` copies (pbcopy/xclip/...) | y, c | **missing** | v2 TUI never shows the thread id at all; ids only via `--json` |
| Sort chooser | `s` field chooser (NAME/CREATED/CWD/AGENT/MACHINE), `S` direction flip | s / S | **missing** | v2: one fixed sort (machine, name, id) chosen for cursor stability under the 3s poll |
| Tree: parent/child nesting | Children fold under parents, →/← expand/collapse | →/← ; `--expand` | **missing** | Data-model gap first: api.Thread has no Parent field anywhere in v2 |
| Mouse | wheel cursor, click select, double-click activate, horizontal wheel scroll | automatic | **missing** | No tea.MouseMsg handling; alt-screen only |
| Cursor wrap | fzf-style wrap at list ends; ctrl+j/k in both modes | `--no-cycle` opt-out | **partial** | v2 clamps; myrig fzf pickers pass `--cycle` |
| `--cursor` preselect | Open with cursor on a given uuid/prefix (the pane the popup launched from); best-effort | `--cursor` | **missing** | v2 always opens at row 0; the pane→thread primitive (`tmux current`) exists to derive it from |
| Esc to quit / cancel exit code | q/Esc/ctrl+c quit; distinct cancel exit code for wrappers | q / Esc / ^c | **partial** | v2 binds q/ctrl+c only; Esc unbound. Exit-code contract moot without emit mode |

### TUI — plumbing

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| Dual-role picker vs manager + emit contract | `sesh pick` / `--enter`-less `tui` EMIT a structured Selection on stdout (`--format json\|nul\|kv`, `--action`, `--select`, exit codes 0/130/3); /dev/tty rendering under `sel=$(...)` | `sesh pick`, flags | **superseded** | v2 inverted to drive-don't-emit (Enter acts in-process); wrappers consume `thread grid --json` + fzf instead (myrig mt-enter-session). Re-adding a pick/emit mode would need the /dev/tty plumbing rebuilt |
| Live Watch stream | SSE record stream, O(1) in-place row patch, delete tombstones | automatic | **superseded** | v2 is poll-first by design: 3s tick over the offline-capable L2 mesh cache; user-visible outcome (live cross-machine rows) holds |
| Read-only daemonless mode | actions==nil → mutations degrade to a "read-only" note | automatic | **missing / obviated** | v2 TUI is a thin client by rule; daemon down = loud error. Offline PEERS stay browsable from the cache (different, arguably better) |

### CLI — spawn & lifecycle

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| Spawn (`new`) | Spawn pi/claude/codex, pre-assigned uuid, name/cwd/tags | `sesh new --agent ...` | **partial** | `thread new --agent --name --cwd [--headless]` — daemon-owned, pane stamped with thread id. Missing flags: --tag, --msg, --wait, --session-id, --window, --target, --dry-run, --no-launch, --model, --sandbox, `--` passthrough |
| Headless spawn + turns | Durable no-pane conversation; turns via resume | `new --headless --msg` | **present** | Improved: pi supported (v1 refused), codex id parsed from exec --json; symmetric send/send-headless gates; matrix-green ×3 agents |
| Opening message (`--msg` / `--wait`) | First message delivered at spawn; --wait blocks for the reply | flags on new | **missing** | Compose (`new` + `send`) races a blank pane (keystroke loss — the harness needs waitThreadReady for exactly this), so a real port needs daemon support |
| Fork a conversation | `--fork-from <uuid> --message-id N` — rewind-and-branch transcript under a new uuid | `new --fork...` | **missing** | v2 only mentions fork as the thing its 409 guards PREVENT accidentally. Needs a transcript read/rewrite layer that doesn't exist |
| Remote spawn | `--machine` routes spawn to the owner daemon | `new --machine [--enter]` | **present** | The founding fix: real ssh/http routing, matrix + real-network proven. `--enter` ≈ `tmux nav --attach` / picker compose |
| Register / adopt an existing agent | Record an externally-launched agent (codex path); myrig `sesh-register-here` + walker `pane-resolve` | `sesh register`, prefix+R | **missing** | Walkers superseded by birth-stamped provenance (`@sesh-thread-id`) for sesh-owned panes — but the ADOPTION of a hand-launched agent has no v2 path at all; threads are only born via `thread new` |
| archive / unarchive / delete / rename / tag-untag-retag | Record mutations | one verb each | **present** | `thread archive [--unarchive] / delete [--force] / rename / tag --add --remove`. v2 --force semantics differ (live-runtime guard, not dead-owner tombstone) |
| `meta` per-thread KV store | Arbitrary replicated key-values; fed predicates/columns | `sesh meta set/get/unset` | **missing** | Tags are v2's only annotation. Prerequisite for [[tui.columns]]/predicate ports |
| parent / children / reparent | Session tree verbs + auto-parent at spawn | 3 verbs | **missing** | No Parent field; data-model decision before any TUI tree |
| resume / enter | Switch to live pane or resume detached, cross-machine | `sesh resume [uuid]` | **present** | `sesh resume <id>` / `thread resume --machine`-routed, 409 on turn-in-flight (no --force fork override — deliberate). No --target placement (sessions minted per naming rules) |
| `--sandbox` permission restriction | claude default-deny / codex read-only for spawned/resumed/delegated turns | flag on new/send/delegate | **missing** | v2 agents always launch in their default (permissive) mode |
| Codex auto-trust | Writes trust_level=trusted for the cwd at spawn | automatic | **present** | `agents.EnsureCodexTrust` |

### CLI — inspection & reads

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| `list` filters + TSV columns | --agent/--tag filters; `--columns ...` stable TSV for fzf/awk | flags | **partial** | v2: `thread list/grid [--json --archived --all-machines]`; no --agent/--tag filters, no TSV contract (myrig uses --json+jq — arguably the superseding answer) |
| `find` + short-prefix resolution | Every uuid arg accepts a unique prefix; `find` explores prefixes | automatic + verb | **missing** | All v2 verbs demand full ids; tid8 is display-only |
| Current-session inference | Omitted uuid → $SESH_SESSION_ID / pane walker | automatic | **partial** | Primitive exists (`tmux current` → thread id, used by myrig status/archive-here) but no verb consumes it; per v2's mechanism-not-UX rule the compose lives in shell |
| `info` / `state` | Full record / runtime state incl. context % | 2 verbs | **partial / present** | `thread status --id` is richer on state (two axes + attachment + needs_input, probe-backed); no single `info`, no context % anywhere in v2 |
| `tags` mesh-wide listing | All tags in use, sorted | verb | **missing** | jq one-liner over `list --json`; nothing ships it |
| `tail` / `transcript` | Print last N lines / full transcript, mesh-wide | 2 verbs | **missing** | v2 never reads transcripts; only `thread headless-reply` (last headless turn) |
| `pane` status query | pane→sesh+status for the tmux status bar, daemon-down-safe | CLI in status-format | **present** | `tmux current --json` + myrig seshv2-current-status; caveat: display fields = a daemon roundtrip per redraw, loud line when daemon down |
| `pane-capture` / `pane-keys` | Capture pane text / send named keys to a thread's pane | 2 verbs | **missing / partial** | Probe captures internally but no verb; `tmux send-text` sends literal text+Enter to a raw target, not named keys to a thread |

### CLI — comms & orchestration

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| Reply-returning `send` | Blocks until the turn completes, prints the reply (sync agent-to-agent RPC); --no-wait; --to-parent | `reply=$(sesh send ...)` | **partial** | v2 `thread send` = fire-and-forget keystrokes into a live pane; headless turns poll via `headless-reply`. No sync reply for headed sends, no --to-parent (no parents) |
| `await` | Block until busy→idle, mesh-wide, --timeout/--poll | verb | **missing** | All polling primitives exist (`thread status --json`, mesh snapshot); the loop is shell-scriptable but unshipped |
| `delegate` | Ephemeral one-shot headless worker: spawn→ask→print→delete | verb | **missing** | Fully composable from green primitives (new --headless + send-headless + poll + delete, all routable); nothing ships it |
| `subscribe` + turn-delivery engine | Auto-push a subscribee's replies into subscriber sessions; dedup, cycle guard, rate limit | 3 verbs | **missing** | No eventing/delivery anywhere; tickets are the only inter-thread mechanism (different shape) |
| pi `abort` / `compact` | Drive pi's rpc socket | 2 verbs | **missing** | v2 has no pi rpc layer at all; `thread stop` is the blunt instrument |
| `autoname` (+toggle) | LLM-generated names from the transcript via `pi -p`; --all batch; post-turn hook mode; autoRename opt-out | verb + hooks | **missing** | No transcript reading, no hook system, no autoRename field. (v2 names need not be unique, which eases a port) |

### Transcripts & backup

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| `copy` transcript ship | Restore a transcript to a dir or ship+restore natively on another machine (claude only) | verb | **missing** | `tmux stage-file` ships bytes mesh-wide but knows nothing of transcripts |
| `backup` / `restore` | Idempotent sha256 transcript backups into portable SQLite; --to native/dir, --rewrite-cwd, --force | 2 verbs | **missing** | Whole subsystem absent; v2 never touches transcript files |

### Config & env

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| SESH_HOME single state dir | One env var relocates everything | env | **present** | Identical pattern; conformance suite depends on it |
| SESH_MACHINE — deliberately NO hostname fallback | v1 refused to start rather than mislabel records | env / flag | **partial** | ⚠ v2 DOES fall back to `os.Hostname()` (config.go:76-83) — the exact class of silent fallback both projects' rules forbid. Deployments always set it, but the fallback is reachable. Worth a decision |
| Agent state-dir resolution | CLAUDE_CONFIG_DIR / ~/.codex / ~/.pi | env | **partial** | Only codex resolved (SESH_CODEX_HOME — trust + rollout discovery). claude/pi never read by v2 (resume delegated to the agent) |
| `[models]` default model per agent | --model > [models].agent > default | config.toml | **missing** | No model concept anywhere; agents launch bare |
| `[agents]` binary path overrides | Explicit agent binary paths when daemon PATH lacks shims | config.toml | **missing** | The motivating problem was solved at the deploy layer instead (supervisor ini PATH + $SHELL -c). A knob would still have caught the deploy-env saga earlier |
| `[spawn]` remote placement | Socket + holding session for remote spawns | config.toml | **superseded** | Every thread gets its own session, named by `[[session_name]]` rules |
| Global `--json` / `--machine` / `--socket` | Uniform machine-readable + routing flags | root flags | **partial** | --machine present and stronger (real routing); --json per-subcommand; --socket replaced by env (SESH_HOME/SESH_REMOTE) |

### Hooks & notifications

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| `[[hooks]]` event hooks + `hooks list/enable/disable/test` + sesh-notify | Run user commands on daemon-observed edges (busy→idle etc.) with filters; persisted runtime mute; synchronous test; desktop toast wiring in myrig | config + CLI + toast | **missing** | The event SOURCE exists (the maintainer observes every transition) but nothing fires commands. This was the v1 notification story (notif-on/off). Biggest missing daemon-side feature cluster |

### Daemon, mesh & peers

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| Daemon lifecycle | start/stop/restart/status/ensure (supervisor entrypoint) | 5 verbs | **partial** | run/start/stop/status exist (loud start, graceful stop). Missing: restart, ensure — supervisor role filled by myrig's supervisord ini |
| `doctor` | Checks agents/tmux/ssh on PATH, dirs, daemon, schema | verb | **missing** | The deploy-env saga (mise shims/zshenv keys missing under supervisord) is exactly what a doctor would catch; today only live smoke does |
| `import` v1 migration | One-shot import of older records, idempotent, daemon-guarded | verb | **missing** | No migration path from live v1 (Go or TS) into v2's store — relevant whenever v1 retires |
| peer add / remove / list | Mesh membership | 3 verbs | **partial** | add (ssh + http transports, ports, tokens — superseding v1's gRPC tunnels) + list; `peer remove` = edit peers.json by hand |
| Mesh-replicated record store w/ offline resync | Full record replication, origin_seq, reconnect catch-up | automatic | **partial** | v2 caches peer SNAPSHOTS (offline → last-known, reachable=0) but listing is LIVE fan-out, not replicated records; SPEC hints at cached listing — not built (known follow-up) |
| Transparent owner forwarding | Any owner-bound op routes to the owner without --machine | automatic | **partial** | v2 routing is explicit `--machine` (deliberate); only tickets auto-route; TUI/picker compose handles resume/promote cross-machine |

### myrig shell / master-tmux layer

| Feature | What it does | v1 UX | v2 | Notes |
|---|---|---|---|---|
| Session picker (sesh-enter / sse) | fzf with info PREVIEW pane, Tab view cycle, --cwd/--local-only filters, cursor preselect on focused pane | shell + popups | **partial** | mt-enter-session covers core (glyphs, alignment, revive compose, routed nav). Missing niceties: preview pane, in-picker Tab cycle, --cwd filter, focused-pane preselect |
| Thread cycling (sesh-next/prev) | prefix+< / > cycle live seshes in order, cross-machine | keys | **missing** | No thread-aware next/prev anywhere in v2/myrig-v2 |
| TUI popup wrapper (sst) | prefix+s popup, selection routed | shell + key | **present** | sst2 + prefix+s with the SESH_NAV_CLIENT carrier; routing built INTO the TUI |
| Archive-toggle picker (ssa, prefix+S) | fzf where Enter TOGGLES archived in place | shell + key | **partial** | Split across archive-here (pane), TUI `a` (row), prefix+A picker (enters, doesn't toggle). No toggle-in-place picker |
| Archive-here (prefix+Tab) | Toggle archive on the focused pane's thread | shell + key | **present** | seshv2-archive-here; the display-popup no-expansion lesson re-fixed via run-shell |
| Register-here (prefix+R) | Adopt a hand-launched agent in a pane | shell + key | **missing** | Depends on the missing register/adopt verb |
| Status line | "sesh: name [id8] · agent · [tags] · 🗄" in status row 2 | automatic | **present** | seshv2-current-status via SESH_TMUX_CONF; no TTL cache (a fork per redraw — fine so far) |
| TTL caches / session poller / refresh-cache | tmpfile caches + per-machine ssh pollers fronting slow listings | automatic, prefix+u | **superseded** | maintainer (L1) + meshsync (L2) solve latency daemon-side; offline browsing retained |
| Master-tmux lifecycle | mms-start/attach/kill, window-per-machine reconnect loops | shell + keys | **present** | `sesh master up/window/attach/down/ensure/watchers` in Go + thin mt-* wrappers; v2 EXCEEDS v1 (daemon self-heal of master windows) |
| Cross-machine nav core | master-window select + inner switch-client + bare-shell kick | automatic | **present** | `sesh tmux nav` with the explicit-client carrier contract + --attach; adds loudness v1 lacked. NOT ported: nav history |
| Flagged cycling + last-session jump (prefix+,/./L) | Cycle flagged sessions; jump to previous (machine,session) | keys | **missing** | No flags, no nav history in v2 (known gap; , . unbound, L unbound) |
| mms-on-machine fan-out popups | Pick a machine, run a command there (t/e/E/m/M/g/G/b/B/r/j) | keys | **missing** | Deliberately not ported (mysystem integration; thin by design) |
| Clipboard cluster (prefix+P) | Ship clipboard/files between machines, paste path into pane | shell + key | **present** | mt-* twins; transport upgraded to `tmux stage-file`; auto-detect improved via `master watchers` |
| Box picker (mms-enter-box, prefix+c) | fzf over boxyard boxes per machine | shell + key | **missing** | Substrate (pollers/caches) gone; boxes findable indirectly via session-name rules |
| sesh-cli agent skill | Shipped SKILL.md teaching agents the CLI | repo artifact | **missing** | Live environment still loads the v1 skill; a v2 skill would need writing before v1 retires |

---

## Lukas's wishlist (v2 TUI), mapped to v1

1. **Renaming threads** ← v1 `r` rename line-prompt (`internal/tui/update.go:699-718`). The backend is done end-to-end (`thread rename` → `client.ThreadRename` → daemon); what's missing is a line-prompt input mode in `internal/tui/model.go` (the same widget unlocks tag-add). Keybinding decision needed: `r` is currently refresh.

2. **Filtering + start-in-filter flag + tmux shortcut launches it filtered** ← v1's fuzzy-filter cluster (filter mode, FuzzyMatchV1 scorer, `--no-filter` inverted to a start-in-filter default). Entails: filter state + a fuzzy scorer in `internal/tui/model.go` (port v1 `fuzzy.go` or vendor a lib), a flag in `cmd/sesh/tui.go`, and updating the popup bindings in `mysetup/myrig/home/.sesh-v2/myrig/tmux.work.conf` / `tmux.master.conf`. Main design choice: how much of v1's apparatus (Esc-applies, ctrl+t uuid target, caret editing) comes along vs. a minimal type-to-narrow.

3. **Esc closes the TUI** ← v1 quit keys (q/Esc/ctrl+c). One line in `handleKey` (model.go:231-255) today — but decide now how Esc interacts with the future filter mode, where v1 used Esc to APPLY the filter and drop to normal mode (so Esc-quits only from normal mode, presumably).

4. **Tab view cycling with named views + custom config views** ← v1 Tab cycle + `[[tui.filters]]` + the predicate mini-language. Step 1 is nearly free: `Model.archived` already exists and `fetch()` honors it — add the Tab key, a 3-way view enum, and a footer label. Custom config views need a `[tui]` section in `internal/config` and a predicate grammar (port v1 `predicate.go`, re-mapping its turn/live selectors to v2's head/busy/attached axes); deciding whether predicates are worth it vs. a few hardcoded views is the real question.

5. **Column enable/disable flags + config defaults** ← v1 `--match`/`--full`/`i`-toggle/`[[tui.columns]]` (the column system generally). Entails replacing the hardcoded `fmt.Sprintf` row (model.go:483-498) with a named-column abstraction (a small port of v1 `columns.go`), flags on `cmd/sesh/tui.go`, and a `[tui] columns = [...]` default in config.toml. This is also the prerequisite for adding the CWD/CREATED/ID columns at all.

6. **CWD display preprocessor in config.toml** ← v1 `[[tui.cwd_rules]]` (+ `cwd-label`). v2 already has the identical engine — `[[session_name]]` first-match cwd-regex → template in `internal/config/naming.go` — so this is a second rule table (e.g. `[[cwd_label]]`) reusing that machinery, applied at render time to a new CWD column (data already on the wire: `api.Thread.Cwd`). Decide: a separate rule set as you sketched, or one shared rule set that both session naming and the CWD column consume (your four rules are nearly the same in both). A `sesh cwd-label <path>` debug verb falls out almost free.

---

## Recommended discussion order

Options, not decisions — grouped by what they'd cost.

### (a) Cheap wins (mechanism exists; mostly TUI affordances)
- **Esc to quit** — one line; just fix the filter-mode interplay up front (wishlist #3).
- **Archived view Tab toggle** — `Model.archived` already plumbed, nothing sets it; the unreachable-archived state is arguably a latent bug (wishlist #4 step 1).
- **Line-prompt widget → rename (`r`) + tag-add (`t`)** — one widget, two wishlist/v1 features; backends fully green (wishlist #1).
- **Cursor wrap (fzf --cycle)** — small `moveCursor` change; matches the pickers' feel.
- **Rune-counted truncation** — v2's byte-counted `trunc()` splits multi-byte runes; a fix regardless of any port.
- **`peer remove` + `daemon restart`** — trivial verb gaps people will eventually hit.
- **ID surfacing (`i` toggle or `y`-copy)** — today there is NO way to get a thread id from the TUI.
- **`--cursor` preselect** — `tmux current` already resolves the focused pane's thread; wiring it into the popup binding + a preselect field restores a v1 nicety every picker lacked.

### (b) Design-needed (worth a real conversation each)
- **In-TUI fuzzy filter** (wishlist #2) — the big one: decide minimal type-to-narrow vs. porting v1's full apparatus, and how it coexists with the fzf pickers that currently own this workflow.
- **Column system + config defaults** (wishlist #5) — prerequisite for CWD/CREATED columns and for #6; decide flag surface + `[tui]` config shape.
- **CWD display rules** (wishlist #6) — second rule table vs. unifying with `[[session_name]]`; whether to ship `cwd-label`.
- **Custom views / predicate grammar** (wishlist #4 step 2) — port v1's predicate.go (re-mapped to head/busy) or hardcode a few views; predicates only pay off if `meta`/columns also come.
- **Hooks/notifications (`[[hooks]]`)** — the maintainer already observes every busy→idle edge; this was your v1 notification story (notif-on/off, sesh-notify toast) and the largest missing daemon feature. Also the natural host for autoname if that ever returns.
- **Agent-to-agent tier: reply-returning send / await / delegate / subscribe** — decide which of these belong in v2 vs. staying ticket-mediated; await+delegate are compositions of green primitives, subscribe is a real engine.
- **Transcript layer: tail / backup / copy / fork** — a whole subsystem v2 deliberately never built; decide if it's in scope at all before v1 retires (fork and backup were genuinely used).
- **Register/adopt foreign agents** — conflicts with v2's provenance-only model; needs a deliberate stance (walkers were superseded, adoption wasn't replaced).
- **parent/child + `meta` KV** — both are data-model additions (api.Thread, store, wire) before any UI; gate the tree view and dynamic columns.
- **SESH_MACHINE hostname fallback** — v2 silently falls back to os.Hostname(), the exact fallback v1 refused and your rules forbid; decide refuse-to-start vs. keep.
- **v1→v2 migration (`import`)** — needed the day live v1 retires; shape depends on the transcript-layer decision.
- **`doctor`** — the deploy-env saga is its poster child; cheap-ish but needs a check-list design.
- **Spawn knobs: `[models]` / `[agents]` paths / `--sandbox` / `--msg`** — per-flag decisions; --msg needs daemon support (blank-pane race), --sandbox is a safety feature delegate would want.

### (c) Probably superseded / skip (port would likely be regression — confirm and close)
- **Watch stream / tombstones** — poll-first over the mesh cache was a deliberate v2 design; outcome parity holds.
- **Emit contract (pick / --format / --select / /dev/tty)** — drive-don't-emit + `--json`+fzf replaced it; only revisit if a non-fzf wrapper ever needs structured selection.
- **K archive+kill composite** — the stop/delete/archive split was a deliberate correction; composites belong in myrig.
- **`[spawn]` holding session** — per-thread sessions + `[[session_name]]` rules replaced it outright.
- **pane-resolve walkers** — birth-stamped `@sesh-thread-id` is strictly more reliable for sesh-owned panes (the adoption gap is the (b) item, not the walkers).
- **TTL caches / session poller / mms-refresh-cache** — maintainer+meshsync solve it daemon-side with the same offline property.
- **Background archived load** — the latency motivation is gone (instant local cache); only the archived *view* matters (item (a)).
- **mms-on-machine popups / box picker** — deliberately thin mysystem integrations; revisit only if you miss them in practice.
- **pi abort/compact** — would require building a pi rpc layer for two niche verbs; `thread stop` covers the blunt case.
- **SESH_CWD_FORMATTER external command** — silent fall-back-to-raw conflicts with v2's loud-errors rule; in-process rules (wishlist #6) cover the need.
