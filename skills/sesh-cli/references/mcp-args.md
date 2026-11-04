# MCP arguments: a per-thread addition to a global MCP server

A harness has one global entry for an MCP server. A thread can add arguments to that
entry for its own processes, so one installed server can run differently per thread,
for example with a different identity file. The global entry never changes.

```sh
sesh thread new --agent claude --name worker --cwd ~/proj \
  --mcp-arg soliton=--agent-file=/home/me/.soliton/worker.json

sesh thread mcp-arg set   --id <id> --mcp-arg soliton=--agent-file=/new/path
sesh thread mcp-arg unset --id <id> --server soliton
sesh thread mcp-arg list  --id <id> [--json]    # {schema, id, mcp_args}
```

- `--mcp-arg <server>=<arg>` splits at the first `=`, so the argument may contain one.
  Repeat it for several arguments; their order is kept.
- `set` replaces the list of each server it names and keeps other servers. `unset`
  removes a server, and refuses one the thread has no arguments for.
- `--machine <m>` routes all of these to the owning machine, as with any thread verb.
- The owning daemon applies the arguments every time the harness starts: spawn, resume,
  a restart in place (`switch-model`), and every headless turn. A running agent keeps
  what it started with, so a change arrives at its next start.
- **A fork inherits nothing**, unlike its model. Give it its own with `--mcp-arg` on
  `thread new --fork-from`, or `mcp-arg set` afterwards.
- `thread info` shows them, and `thread list --json` carries them as `mcp_args`.
- **Never pass a secret.** Arguments are on the thread record, which replicates to every
  peer and is copied by backups. Pass the path of a file that holds the secret.

## How each harness receives them

The thread's arguments always come **after** the global entry's own.

| harness | launch argument | where the global entry is read |
|---|---|---|
| claude | `--mcp-config=<SESH_HOME>/thread-mcp/<id>/mcp.json`, a generated file (0600, in a 0700 folder) | `mcpServers` in `~/.claude.json`, or `$CLAUDE_CONFIG_DIR/.claude.json` |
| codex | `-c mcp_servers.<server>.args=[…]` | `[mcp_servers.<server>]` in `$CODEX_HOME/config.toml` |
| pi, mypi | `--sesh-mcp-args={"<server>":["<arg>",…]}` | `mcpServers` in `mcp.json` in the agent dir (`$MYPI_CODING_AGENT_DIR` under mypi, else `$PI_CODING_AGENT_DIR`, else `~/.pi/agent`) |

On pi and mypi, the `sesh-agent-state` extension starts the server itself and registers
its tools as `mcp__<server>__<tool>`, with any character a provider refuses in a tool
name replaced by `_`. It follows `tools/list_changed` in the running process, and gives
the model the server's `instructions` on every run.

A server sees the thread's env variables on claude and pi, not on codex.

Headless turns can only call an MCP tool with permissions bypassed (`[spawn] mode =
"yolo"`). Otherwise claude reports that the tool needs permission and codex that it
requires approval.

## What the global entry must look like

- It must exist on the machine that owns the thread, or the launch fails naming the file.
- It must be a stdio server, because a URL server has no arguments.
- **claude:** no `env` block. A same-named entry in `--mcp-config` replaces the global
  entry whole, so its `env` would have to be copied into the generated file, and sesh
  does not write those values to disk. Such an entry refuses the launch.
- **pi, mypi:** `"disabled": true`. The extension reads the entry either way, and this
  keeps the MCP adapter from running its own copy without the thread's arguments. A
  thread with no arguments for the server does not start it. An optional `timeout`, in
  seconds, limits a tool call; progress notifications reset it. Without one a tool call
  has no limit. An interrupted or timed-out call sends `notifications/cancelled`, and
  the error says the call may still be running, because a server may ignore that.

## When a server cannot start

A pi or mypi headless turn exits non-zero before the model runs. So does one whose
server promised tool changes but still lists no tools after 45 seconds; tools that
arrive within that wait are used in that turn. `sesh await` then exits non-zero with the
reason (see `sesh await` in [delivery](delivery.md)).

A headed pi shows the error, tells the model the server's tools are unavailable, and
starts the server again at every run and every call to one of its tools, reading
`mcp.json` afresh. Tools that arrive later appear without a restart.

A claude or codex headless turn carries on without the server and exits 0, showing
nothing in its reply, and **sesh does not detect this**: `thread mcp-arg list` shows
what the thread asked for, not that a server is running. To check, ask the worker to
call one of the server's tools. A server that lists no tools at first recovers on
claude. Codex 0.166 does not re-list: a headed codex needs `thread restart`, and a
headless thread gets a new process at each turn.
