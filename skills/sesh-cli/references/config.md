# Daemon, TUI, hook, and app configuration

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
## Config (`~/.sesh/config.toml`)

```toml
[[session_name]]                 # name the tmux session from the cwd (first match wins)
match = '^~/dev/.+$'
name  = '{path} ({tid8})'

[[cwd_label]]                    # the TUI CWD column's display transform
match = '^~/mysetup/(?P<rel>.+)$'
label = 'mysetup/{rel}'

[tui]
columns = ["machine","agent","name","cwd","tags","notify"]   # opt-in extras incl. sched (next scheduled fire), hold, created, id
all_machines = true              # default `sesh tui` to the cross-machine view (= --all-machines)
show_offline = true              # show OFFLINE machines' threads by default (else hidden; toggle-offline toggles)
expand_children = true           # tree nodes start EXPANDED (default false: children collapsed; = --expand)
view_ring = ["active", "flagged"]  # ONE key flips between these views (the `view-ring` command;
                                 #   bind it with [[tui.key]] — no default key). Names are built-ins
                                 #   or [[tui.views]] names; unknown/ambiguous/repeated = loud at
                                 #   startup. Keep it in [tui] itself, ABOVE the [[tui.*]] tables.
[[tui.column]]                   # MOVE one column relative to an anchor, on top of the base set —
name   = "notify"                #   so you can reposition a column without enumerating them all
before = "machine"               #   (or `after = "..."`)
[[tui.column_color]]             # NAME blue / CWD green / ticket_input red by default; override here
name = "cwd"
color = "green"
[[tui.glyph_color]]              # gutter attention glyphs: busy ▶ / descendant ↓ (bright green by default)
name = "busy"
color = "2"                      # a name, a 0-255 number, or #rrggbb; empty clears the tint
[[tui.key]]                      # REBIND a command's key (ids come from `?` / the palette)
command = "fork"                 #   first entry for a command REPLACES its defaults (a MOVE);
key     = "F"                    #   further entries ADD keys; key = "" unbinds (palette-only).
                                 #   Unknown id / unusable key / two entries on one key = loud error.

[[tui.views]]                    # custom Tab-cycle views over the predicate language
name = "ticketed"
filter = "ticketed and not archived"   # keywords incl. headful/headless/busy/idle/archived/onhold/flagged/flagdisabled/ticketed
position = 2                     # optional: 1-based slot in the Tab/picker order among the built-ins (active/on hold/archived/all); omit/0 = appended after the built-ins
[[tui.views]]                    # supplies the custom view named by view_ring above
name = "flagged"
filter = "flagged"

[defaults]
agent = "pi"                    # thread new may omit --agent; explicit --agent wins; unset = loud error
notifications = true

[mesh]
idle_interval = "60s"            # peer-sync pace while nothing reads the mesh view ("0s" = never idle)

[spawn]                          # default launch policy (yolo bypasses permission prompts)
mode = "yolo"

[send]                           # the typing guard on every paste into a live pane
respect_typing = "60s"           # hold a delivery until the pane has seen no viewer input this long ("0s" = off)
respect_typing_deadline = "10m"  # a delivery still held this long fails loudly + flags the thread

[schedules]                      # `sesh schedule` policy (the schedules themselves are records, not config)
enabled = true                   # false = this daemon fires nothing (run-now still works)
disable_after_failures = 10      # a schedule failing this many runs in a row is disabled, loudly (0 = never)

[[hooks]]                        # event hooks: fire a command on an observed state edge
name = "notify-idle"
event = "busy_changed"
from = "busy"
to = "idle"
command = "~/.mybin/sesh-notify"
```

The hook command is a **site-specific example**, not a file distributed with this
skill. Supply an existing trusted command on the owning daemon's host; do not paste
this whole policy example over a user's configuration. `view_ring` belongs in
`[tui]`, not inside a `[[tui.key]]` entry. The custom `flagged` view above makes the
example ring resolvable without relying on the user's other config.

`[defaults] agent` is resolved by the **owning daemon**, so routed creation with
`thread new --machine <m>` uses machine `<m>`'s policy. Valid values are exactly
`pi`, `claude`, and `codex`; an invalid value prevents the daemon from starting
rather than silently choosing another harness. The daemon reads `[defaults]` at
startup, so changing it requires a daemon restart through the machine's service
manager. This is separate from `ui_config.toml`'s `default_agent`, which only
preselects a value in sesh-ui's New-thread modal.

A hook command runs through `$SHELL -c` with the event described in env vars:
`SESH_EVENT` (+`SESH_EVENT_FROM`/`SESH_EVENT_TO` on edges), `SESH_THREAD_ID`,
`SESH_THREAD_NAME`, `SESH_AGENT`, `SESH_MACHINE`, `SESH_CWD`, `SESH_SESSION`,
`SESH_TAGS` (comma-joined), `SESH_HEAD`, `SESH_BUSY`, `SESH_ATTACHMENT`
(`attached`/`detached`), `SESH_ATTACHED_ACTIVITY_AGO` (seconds since the last
INPUT on a client attached to the thread's session; absent when detached or
unknown), `SESH_ATTACHMENT_CHANGED_AGO` (seconds since the observing daemon saw
the attachment axis flip — the "just navigated onto it" signal; absent if no
flip observed since daemon start), `SESH_NOTIFY` (the per-thread gate as
`1`/`0` — the hook fires regardless; honoring the gate is the hook's job),
`SESH_FLAGGED` (`1`/`0` — the needs-attention flag), `SESH_FLAG_REASON`
(present only when an auto-flag carries one, e.g. the question the agent
asked), and `SESH_STATE_AUTHORITY` (`reported`/`heuristic` — which mechanism
decided busy; absent when unknown), and on `schedule_fired`/`schedule_failed`
`SESH_SCHEDULE_ID`, `SESH_SCHEDULE_NAME`, `SESH_SCHEDULE_OUTCOME`. The event vocabulary includes
`flag_changed` (from/to `flagged`/`unflagged`) — to=flagged is THE toast
edge: the daemon flags exactly when a turn ends or the agent stalls on a
question/approval (attended or not), and on manual flags. The activity/flip ages exist because a HEURISTIC busy→idle edge alone
can't tell a finished turn from the user pausing: typing into a pane or
navigating onto it latches the content-diff busy probe like agent output
would, while raw attachment over-suppresses (cockpit clients park on
sessions) — a notify hook should skip only when attached AND (recent input OR
a recent attachment flip), failing open when the vars are absent. Under
`SESH_STATE_AUTHORITY=reported` the edge is exact (a real turn boundary).

### `ui_config.toml` — the app's preferences (a SECOND file)

`<SESH_HOME>/ui_config.toml` is separate from `config.toml`: it holds preferences for the
**sesh-ui app**, which the daemon stores and serves over `GET`/`POST /v1/ui-config`. sesh
does not otherwise interpret them, and the CLI/TUI ignores the file entirely. It lives in
`SESH_HOME` so it follows whichever daemon a client connects to (per-machine).

```toml
collapse_parents = true          # parent threads start COLLAPSED in the app's tree (default true)
cwd_roots = ["~/mysetup", "~/dev"]   # "default parent folders" the new-thread modal quick-picks
                                     #   from (listed per target machine via GET /v1/fs/list)
transcript_prefetch_secs = 10    # background transcript prefetch cadence; 0 disables
master_command = "mmt-start"     # what the app's Master mode runs in a pty ($SHELL -lc); empty = unconfigured
default_agent = "claude"         # new-thread modal preselections
default_machine = "macbook"
default_chat_view = "terminal"   # terminal | transcript | rpc
[[cwd_label]]                    # display transform for the cwd quick-pick (same rule language as config.toml)
```
