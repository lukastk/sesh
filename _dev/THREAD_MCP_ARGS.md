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
- **Mixed fleet.** A pre-63 daemon ignores `mcp_args` in a new-thread request, which
  would spawn a worker without its identity. `thread new` compares the returned thread
  with what it sent and fails, naming the thread. `mcp-arg set` reports the 404.

## A server that cannot start

Measured with a nonexistent command: `claude --print` and `codex exec` both answered and
exited 0, with no sign in their output. So a claude or codex worker can run without its
tools, silently; sesh checks that the global entry exists, not that it runs. The pi
extension is stricter because it owns the start: a headless turn exits 1.

## Not measured

- mypi end to end in a sandbox. Its launcher chooses the agent dir, so conformance runs
  pi; mypi shares the extension and the flag, and the headless argv has a unit test.
  Its launcher sets `MYPI_CODING_AGENT_DIR`, which the extension reads first.
