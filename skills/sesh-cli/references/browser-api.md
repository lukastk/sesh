# Browser API

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules, and [config](config.md) for the `[browser_hub]`
and `[browser_api]` settings that decide which peers and origins may use these routes.

Graphical clients (Sesh UI, Sesh in Obsidian) talk to the daemon only through this
seam. Browser callers supply thread, publication and request IDs, never host paths,
session files or argv; a native protocol the owner cannot serve fails explicitly
rather than falling back to raw files, another protocol version or a live source.
Browser send and the display projection (`browser.send-v1`, `browser.display-v1`)
are documented in [delivery](delivery.md).

## Routes

The browser seam is deliberately narrower than the daemon's general API:

- `GET /v1/browser/threads` provides bounded filtering and cursor pagination over local
  rows plus cached rows from trusted peers. Each row exposes both `runtime_available` and
  `terminal_available`: the latter is true only for a fresh, reachable canonical
  `headful` snapshot. In particular, a busy headless turn has runtime work but no terminal.
- `POST /v1/browser/threads` and `POST /v1/browser/threads/action` dispatch fixed schemas
  through canonical owner-side Sesh handlers; they are not arbitrary peer proxies.
- Tickets, directory listing, blobs and machines (schema 68; capabilities
  `browser.tickets-v1`, `browser.fs-list-v1`, `browser.blobs-v1`, `browser.machines-v1`)
  use the same hub policy. `POST /v1/browser/tickets{,/status,/set,/delete,/send-prompt}`
  and `POST /v1/browser/blobs` take `{machine, request}` and forward `request` unchanged to
  that fixed canonical route on the owner (empty `machine` = the daemon answering);
  `POST /v1/browser/blobs/expand` expands `@blob()` tokens on the owner (browser sends
  deliver text verbatim and never expand them). `GET /v1/browser/tickets/get` and
  `GET /v1/browser/fs/list` take `?machine=`; host commands (schema 74, capability
  `browser.host-commands-v1`) use `GET /v1/browser/host-commands{,/run}?machine=` and
  `POST /v1/browser/host-commands/run` (see integrations); ticket
  `find`, `list-all` and `move` run on the hub; `GET /v1/browser/machines` lists machines
  with `routable` (whether the hub forwards to them). The blob route accepts bodies up to
  48 MiB (a 32 MiB blob as base64); every other browser body is limited to 1 MiB. Thread
  rows carry `cwd_rel` and
  `created_at_unix`.
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
- Canonical stopped input media is separate from history and artifacts. Browser thread
  pages advertise `browser.stored-input-media-v1`; presentation and metadata preserve
  Pi's own protocols and cursors, and stopped GET/HEAD bytes use only the native
  session-file CLI. The distinct `browser.stored-media-presentation-v2` token gates
  `GET /v1/browser/threads/media-presentation`, which invokes only
  `pi.session-presentation` version 2 with the fixed native version flag and returns
  canonical input plus direct tool-result output references. The input-only presentation
  route remains version 1. Unsupported or malformed v2 owners fail explicitly; Sesh never
  retries v1, revives the agent, or switches to a live source. Output bytes stay on the
  verified artifact metadata/byte and managed-download routes.
- The additive `browser.live-input-media-v1` token means the input-image byte URL can
  route an active Pi runtime only through its native-owned `.input.sock` bridge. Sesh
  resolves the exact atomic marker target plus `input_image_reader` listener generation,
  sends neither to the browser, buffers and independently verifies the whole private
  response, and revalidates owner/session/runtime/target/generation before success.
  Active reads never fall back to stopped files; stopped reads never use the bridge. All
  media responses are private/no-store; input bytes reject ranges and preserve legacy
  computed-only digest semantics. Socket image submission stays disabled until joined
  activation.
- `/v1/browser/attachments/*` is a separate raw-HTTP chunk transfer surface: 16 MiB
  per attachment, 256 KiB sequential chunks, exact chunk and whole-file SHA-256,
  idempotent client initialization/last-chunk retry, fixed owner routing, and a 512 MiB
  owner-wide durable transfer ceiling. Attachment names are validated UTF-8 basenames of
  at most 255 bytes; literal `.` and `..` are rejected. `prepare` binds up to 8 ready
  files/32 MiB to one immutable request plus exact Pi stream/session/epoch and returns only
  15-minute opaque leases. No paths or binary WebSocket records are accepted, and
  attachments never reuse legacy blob/stage semantics.
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
  `{"type":"resize","cols":<positive uint16>,"rows":<positive uint16>}` immediately
  after connecting. Ticket URLs are exact: never append dimensions. Terminal
  input/output remains binary WebSocket frames.

All browser request bodies, pages, attachment chunks/storage, artifact streams,
stored-history subprocess output, transcript tails, WebSocket records and outgoing queues
are bounded. A slow browser viewer is disconnected without stopping or replacing the
owned agent runtime.
