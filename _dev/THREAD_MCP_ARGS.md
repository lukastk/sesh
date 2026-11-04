# THREAD_MCP_ARGS — per-thread MCP arguments (schema 63)

*Design record, 2026-11-04. Read before changing `thread mcp-arg`, `agents/mcparg.go`,
`daemon/mcparg.go`, or the pi extension's `mcp.ts`. User-facing contract:
[skills/sesh-cli/references/mcp-args.md](../skills/sesh-cli/references/mcp-args.md).*

## Why

Soliton gives each agent an identity file and starts a shared `soliton` MCP server with
`--agent-file=<path>`. Each harness has one global entry, so the per-agent part has to
arrive per process, at every start, and the store is the only place that outlives one.

## Measured (mymain: Claude Code 2.1.318, codex-cli 0.166.0, pi 1.4.0)

A stdio server logging its argv and one env variable, configured as a global entry.

| question | result |
|---|---|
| claude: same-named `--mcp-config` entry versus user scope | the file wins **whole**: an `env` on the global entry was gone in the server. `--print --session-id`, then `--print --resume`, both honoured it |
| codex: `-c mcp_servers.<s>.args=[…]` | replaces the args array and keeps the rest of the entry |
| pi: extension flag parsing | `--flag value` takes `value` only if it does not start with `-`, so the `=` form is required; flags are a Map, so a repeated flag keeps the last. Hence one JSON value |
| pi: `pi.registerMcpServer` | not usable: every rig machine runs `-builtin:mcp` with pi-mcp-adapter, and registrations then error. The adapter's `--mcp-config` replaces the config rather than adding to one entry |
| pi: entry in `mcp.json` | with pi's built-in MCP on, a second copy started without the thread's args. With the adapter, `"lifecycle": "lazy"` and `"disabled": true` each left only the extension's copy, and a disabled entry also cannot be started by a model through the adapter, so that is the rule |

## Decisions

- **Append, not replace.** A global entry may need its own args (`run soliton-mcp`).
  claude and codex can only replace, so sesh reads the global args at each launch and
  sends both. A change to the global entry therefore applies at the next start.
- **Generated claude file holds no secrets.** It carries `type`, `command`, `args`, and
  never `env`; a global entry with `env` refuses the launch rather than losing the
  variables or writing them down. Rewritten at each launch; removed with the thread and
  when the thread no longer has arguments.
- **Stored on the row** (`threads.mcp_args`, migration 30), unlike thread env's values:
  the point of `thread mcp-arg list --machine` and of the mesh snapshot is that these
  are paths. The rule that they are never secrets is documented, not enforced.
- **One seam.** `launchArgs` for headed starts, `startHeadlessTurn` for turns; both end
  in `mcpLaunchArgs`. TurnSpec's `HookArgs` now reaches pi too.
- **Own stdio client in the extension,** on node built-ins: pi's two MCP mechanisms
  cannot add an argument to one entry for one process (above).
- **Mixed fleet.** A pre-63 daemon refuses `mcp_args` in a new-thread request with a 400
  (`unknown field`), before anything spawns; `thread new` turns that into "upgrade it".
  `mcp-arg set` reports the 404, and `mcp-arg list` refuses a daemon whose schema predates
  the field rather than reading its silence as "no arguments". `thread new` still compares
  the returned thread with what it sent, for a daemon that accepted them and lost them.
- **The client stays live** (schema 64 fold of the independent review). `list_changed`
  re-lists in the running process; pi cannot unregister a tool, so a withdrawn tool leaves
  the active set. `instructions` become a system prompt section per run. Reading happens
  one at a time per server with a rerun flag, so a burst ends on the last list.
- **A headless pi waits, then fails.** One process is all a turn has, so a server that
  declared `listChanged` and lists nothing gets 45s (above soliton-mcp's 15s poll), then
  exit 1. A server that did not declare it is taken at its word. Headed pi does not wait:
  nothing ends the process, and the tools arrive at the next model request.
- **Tool calls have no default limit.** A timeout on a call still running is how a model
  comes to repeat it, and tool length is the server's business. The entry's `timeout`
  keeps pi's meaning (seconds, reset by progress). Cancelling is best effort.
- **Turn errors are on the row** (`threads.turn_error`, migration 31): they must outlive
  the daemon and reach `await` through the mesh. Rows replicate, so the text is capped at
  2000 bytes and the thread's env values are scrubbed; the full text stays in the
  in-memory reply. `await` reads the maintained snapshot, which can be a tick old, so
  `handleMesh` overlays the in-flight registry: otherwise an `await` right after a send
  returned at once on the idle from before the turn. Across machines the snapshot lags by
  a sync round, so a caller sending then awaiting from elsewhere passes `--machine`.

## A server that cannot start

Measured with a nonexistent command: `claude --print` and `codex exec` both answered and
exited 0, with no sign in their output. So a claude or codex worker can run without its
tools, silently; sesh checks that the global entry exists, not that it runs. The pi
extension is stricter because it owns the start: a headless turn exits 1.

That gap is open, and both halves belong to the harnesses. What each one exposes:

- **claude:** `--output-format stream-json --verbose` opens with a `system/init` event
  whose `mcp_servers` says `{"name": "bad", "status": "failed"}`. Text mode, which sesh
  uses, has nothing on stdout or stderr. Reading that status would need sesh to parse
  headless turns as stream-json. It would report a start failure only, not a tool list
  that stayed empty.
- **codex:** nothing. `codex exec --json` printed only `thread.started`, `turn.started`,
  the message and `turn.completed`, stderr had no mention, and it exited 0.

So sesh can honestly say what was asked for (`thread mcp-arg list`), not that a server
runs. Asking the worker to call a tool is the only proof on those harnesses.

## What the harnesses do with a late tool list

Measured with a server listing nothing, then sending `list_changed` after 4s (Claude Code
2.1.318, codex-cli 0.166.0, pi 1.4.0 with this extension):

| harness | re-lists | tool usable | sees `instructions` |
|---|---|---|---|
| claude | yes | yes | yes |
| codex | no | no | no |
| pi + extension | yes | yes | yes |

MCP sends `instructions` only at `initialize`, so a server's text cannot change in a
running process on any harness. soliton-mcp's during an outage says that it has not
answered yet, and a worker that started then keeps that text after the tools appear.

## Not measured

- mypi end to end in a sandbox. Its launcher chooses the agent dir, so conformance runs
  pi; mypi shares the extension and the flag, and the headless argv has a unit test.
  Its launcher sets `MYPI_CODING_AGENT_DIR`, which the extension reads first.
