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
agent = "pi"                    # claude | codex | pi | mypi; thread new may omit --agent; explicit --agent wins; unset = loud error
notifications = true

[mesh]
idle_interval = "60s"            # peer-sync pace while nothing reads the mesh view ("0s" = never idle)

[browser_hub]                       # opt-in cross-peer API for trusted browser clients
enabled = true                      # default false; local-thread browser operations still work
trusted_peers = ["mymain", "macbook"] # exact Sesh machine names; each must be an HTTP peer

[browser_api]
allowed_origins = ["app://obsidian.md"] # default []; exact desktop origin, no wildcard/unconfigured reflection
                                        # the fixed CORS table covers the display read (GET) and the action POST

[spawn]                          # default launch policy (yolo bypasses permission prompts)
mode = "yolo"
[spawn.mypi]                     # per-harness override table, one per kind (claude|codex|pi|mypi)
mode = "default"                 # sandbox is refused for pi AND mypi: neither has a permission system
args = ["--thinking", "high"]    # literal extra argv for this harness only

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
`pi`, `mypi`, `claude`, and `codex`; an invalid value prevents the daemon from
starting rather than silently choosing another harness.

`mypi` is the patched pi fork as its own harness kind. Everything that LAUNCHES an
agent uses the `mypi` executable from the owner daemon's `PATH` — spawn, resume,
headless turns, and the native metadata CLIs behind the stored history/artifact/
media routes — and a missing `mypi` fails loudly there rather than falling back to
`pi`. Everything that READS or STEERS a conversation treats it as pi: the same
JSONL format in pi's session store, the same `pi-rpc-sockets` protocol-2 and
`.live.sock` sockets, the same transfer inbox environment. `[spawn.mypi]` is
therefore an independent table (different extra args are plausible) with pi's
mode space (`sandbox` refused for both).

**Environment.** Sesh never sets a `PI_*` or `MYPI_*` variable for a launch; it
only *reads* `$PI_CODING_AGENT_DIR` to resolve conversations itself, and injects
only its own `SESH_*` carriers (plus `CODEX_HOME` for codex). The mypi launcher
owns its own variables: the frozen packaging uses a separate agent dir
(`~/.mypi/agent`, symlinked into `~/.pi/agent`) but pins
`MYPI_CODING_AGENT_SESSION_DIR` to `~/.pi/agent/sessions`, which is where Sesh
looks. **Precondition:** that match holds while the daemon's
`PI_CODING_AGENT_DIR` is unset or points at `~/.pi/agent`. Point it elsewhere on
a machine running mypi threads and the two stores diverge — Sesh then reports
`session_not_found` / "no transcript on disk" for mypi reads, loudly and never
against another conversation's file.

**Harness presence.** `sesh doctor` checks `mypi` on the daemon's `$SHELL` PATH
alongside the other harnesses. A missing harness is a `warn` on its own row only:
it does not fail the run (so `sesh doctor`'s exit code stays zero), does not
change any other harness's row, and says that only that harness's spawns,
resumes and headless turns are affected. `tmux` and `ssh` remain the only `fail`
rows in that group.

The daemon reads `[defaults]` at startup, so changing it requires a daemon restart
through the machine's service manager. This is separate from `ui_config.toml`'s `default_agent`, which only
preselects a value in sesh-ui's New-thread modal.

`[browser_hub]` is a security boundary, not a convenience fallback. When absent or
`enabled = false`, browser endpoints may address only this daemon's own threads. When
enabled, they may list cached rows and route a fixed allowlist of thread operations only
to `trusted_peers` that are registered with `api_addr` + API token. The caller cannot
supply a peer URL, API path, or credential. `/v1/route` remains Unix-socket-only, so an
existing TCP bearer token does not become a generic cross-peer relay token. Changes are
read at daemon startup and require an authorized service-manager restart.

`[browser_api] allowed_origins` is a separate exact-origin CORS allowlist for the fixed
browser routes documented below plus `POST /v1/ws-ticket`; route names are matched exactly,
not by `/v1/browser/*` prefix. It defaults empty and accepts at most 32 unique origins of
512 bytes each. Values use `app`, `http`, or `https` with scheme and host only; wildcard,
`null`, userinfo, path, query, and fragment values prevent startup. Only route/method-specific
unauthenticated preflights are admitted; actual operations remain bearer authenticated.
Request headers are limited to `Authorization`, body-route `Content-Type`, chunk-upload
`X-Sesh-Chunk-SHA256`, and managed-artifact `Range`. Artifact reads expose only bounded
standard file metadata plus `X-Sesh-Artifact-SHA256`. `/v1/route`, `/v1/runtime/*`, WebSocket
upgrades, and generic/legacy API routes receive no CORS grant. Responses use `Vary` and
`Cache-Control: no-store`; they
never allow credentials/cookies or private-network access. The evidenced desktop origin is
`app://obsidian.md`; Android behavior is not established. Changes require an authorized
daemon restart.

### Browser/plugin API

The browser seam is deliberately narrower than the daemon's general API:

- `GET /v1/browser/threads` provides bounded filtering and cursor pagination over local
  rows plus cached rows from trusted peers. Each row exposes both `runtime_available` and
  `terminal_available`: the latter is true only for a fresh, reachable canonical
  `headful` snapshot. In particular, a busy headless turn has runtime work but no terminal.
- `POST /v1/browser/threads` and `POST /v1/browser/threads/action` dispatch fixed schemas
  through canonical owner-side Sesh handlers; they are not arbitrary peer proxies.
- `GET /v1/browser/threads/history?id=<uuid>[&cursor=...&max_items=...&max_bytes=...]`
  is the canonical stopped-Pi history seam. The owner resolves the session file only from
  the authoritative Sesh thread association, then directly execs metadata-only
  `pi --session-history-json <absolute-path>` with separate optional cursor/item/byte argv.
  Browser paths/IDs/argv are never accepted. Limits match Pi (1–500 items, 2–786432 bytes),
  with a 10s process deadline, 8 MiB stdout and 64 KiB stderr caps. Success wraps Pi's
  unchanged `pi.session-history` v1 session/page under Sesh `schema`, `thread_id`, and
  `owner_machine`; Sesh rejects a returned session ID that differs from the thread record.
  Native error codes remain stable. Older Pi without the protocol returns explicit HTTP 501
  `unsupported_native_capability`; there is no raw-transcript or second-agent fallback.
- `GET /v1/browser/threads/artifacts?id=<uuid>[&cursor=...&max_items=...&max_bytes=...]`
  is the separate owner-routed stopped-Pi artifact metadata seam. It invokes only
  `pi --session-artifacts-json` against the server-resolved canonical session file;
  `pi.session-history` v1 is unchanged. Metadata pages are limited to 500 items/512 KiB,
  validated against the authoritative session before and after the subprocess, and may
  truthfully omit SHA-256 for legacy inline images until explicit byte inspection.
- `GET /v1/browser/threads/artifact-bytes?id=<uuid>&publication_id=<native-id>`
  performs one stopped session-inline whole-object read through `pi --session-artifact-bytes`.
  It accepts no paths or ranges, buffers at most 32 MiB, requires successful child exit,
  validates whole length and SHA-256, then rechecks the stopped thread/session/source file
  before writing HTTP success. Runtime-source files remain unavailable. This direct read
  does not publish a durable copy; the existing Unix-local managed artifact API remains
  authoritative for retained publication and `/v1/browser/artifacts` downloads.
- `GET /v1/browser/threads/transcript?id=<uuid>&tail=<n>` performs a bounded owner-side
  stored transcript read even when the thread is stopped. `tail` defaults to 1,000 native
  JSONL lines and is limited to 10,000; retained raw line content is limited to 8 MiB and
  the encoded response to 16 MiB. The response format is `native-jsonl` with raw `lines`,
  whole-resolved-file `last_reply`/`reply_count`, and explicit zero-based line coverage:
  `source=resolved-current-session-file`, `requested_tail`, `start_line`, `end_line`
  (exclusive), `total_lines`, `complete`, and `byte_limited`. This is coverage of the
  resolved native source file only, not a promise of complete logical branch history.
- `/v1/browser/attachments/*` is a separate raw-HTTP chunk transfer surface: 16 MiB
  per attachment, 256 KiB sequential chunks, exact chunk and whole-file SHA-256,
  idempotent client initialization/last-chunk retry, fixed owner routing, and a 512 MiB
  owner-wide durable transfer ceiling. Attachment names are validated UTF-8 basenames of
  at most 255 bytes; literal `.` and `..` are rejected. `prepare` binds up to 8 ready
  files/32 MiB to one immutable request plus exact Pi stream/session/epoch and returns only
  15-minute opaque leases. No paths or binary WebSocket records are accepted.
- Sesh-launched Pi receives an all-present/all-absent transfer triple:
  `SESH_PI_TRANSFER_THREAD_ID`, `SESH_PI_TRANSFER_INBOX`, and
  `SESH_PI_TRANSFER_API_SOCKET`. The dedicated identity and inbox come from the same
  authoritative thread UUID and replace hostile inherited values on headed, revived,
  into-pane, and headless launches; neither `SESH_THREAD_ID` nor the inbox basename is
  an identity source. Missing/stale consumer registration—including an adopted
  already-running or older runtime—makes transfer explicitly unsupported; Sesh never
  guesses a runtime path. Output artifacts are published only through the Unix-only
  typed `/v1/runtime/artifacts/*` seam and downloaded through fixed owner-routed
  `/v1/browser/artifacts`; arbitrary host paths are never downloadable.
  Successful GET, HEAD, and range responses always carry exact `Cache-Control: private, no-store`
  on both owner and hub paths, independently of CORS; a hub imposes this policy rather than copying
  an upstream peer cache header.
- Durable native-output mapping is separate from stopped-native reads and direct artifact
  publication. A producer with the exact current transfer target uses Unix-only
  `POST /v1/runtime/artifact-mappings/resolve`,
  `PUT /v1/runtime/artifact-mappings/content?thread_id=<uuid>&mapping_id=<uuid>`, or
  `POST /v1/runtime/artifact-mappings/fail`. Resolve creates the pending mapping before the
  deterministic mapping/client identity and its reserved managed artifact; retries repair an
  interrupted pair. Content is one complete bounded object with exact target headers; only current
  targets for the same native session may
  finish the immutable original target. Mapping states advance only from `pending` to `ready`,
  `unavailable`, or `expired`; terminal tombstones remain durable through artifact cleanup.
  Startup idempotently removes mapping/payload pairs left behind by a crash after authoritative
  thread deletion, while malformed mapping metadata refuses startup.
- `POST /v1/browser/artifact-mappings/query` accepts one canonical thread UUID, its exact native
  session UUID, and 1–64 unique native publication IDs (canonical UUIDs, or the legacy inline-image
  64-lowercase-hex identity). It returns results in request order as
  `unmapped`, `pending`, `ready`, `unavailable`, or `expired`, routes only through the configured
  owner allowlist, never exposes the internal mapping/client UUID, and is the only mapping route
  granted exact-origin CORS (`Authorization, Content-Type`). Current Sesh lifecycle operations do
  not move one thread UUID between owners: adopt/fork/import continuations create a new UUID or a
  local conflict, and reparenting changes hierarchy only.
- `POST /v1/ws-ticket` requires `Authorization: Bearer ...` in the header and returns a
  30-second, single-use ticket bound to one canonical thread UUID, owner, route and
  protocol. Ticket URLs permit exactly one `id`, `protocol`, and `ticket` parameter;
  duplicates or extras consume and refuse the ticket. Owner identity is resolved again
  at use time, so a grant cannot follow a moved/re-associated thread. Browser WebSockets
  use the returned URL instead of storing the daemon bearer token in a WebSocket URL.
- `GET /v1/threads/rpc?...&protocol=live-v1` bridges newline-delimited JSON records to
  the Pi session's `.live.sock` without injecting the legacy protocol-2 subscribe command.
  `protocol=terminal-v1` is the ticket protocol for `/v1/threads/terminal`. A ticket URL
  has no `cols`/`rows`: the terminal initially opens at 120×32, then the authenticated
  client sends the text control frame
  `{"type":"resize","cols":<positive uint16>,"rows":<positive uint16>}`. Terminal
  input/output remains binary WebSocket frames.

All browser request bodies, pages, attachment chunks/storage, artifact streams,
stored-history subprocess output, transcript tails, WebSocket records and outgoing queues
are bounded. A slow browser viewer is disconnected without stopping or replacing the
owned agent runtime.

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
