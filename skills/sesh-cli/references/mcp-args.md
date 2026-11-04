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
its tools as `mcp__<server>__<tool>`.

## What the global entry must look like

- It must exist on the machine that owns the thread, or the launch fails naming the file.
- It must be a stdio server, because a URL server has no arguments.
- **claude:** no `env` block. A same-named entry in `--mcp-config` replaces the global
  entry whole, so its `env` would have to be copied into the generated file, and sesh
  does not write those values to disk. Such an entry refuses the launch.
- **pi, mypi:** `"disabled": true`. The extension reads the entry either way, and this
  keeps the MCP adapter from running its own copy without the thread's arguments. A
  thread with no arguments for the server does not start it.

## When a server cannot start

A pi or mypi headless turn exits non-zero before the model runs. A headed pi shows the
error, and the next call to one of the server's tools tries again. A claude or codex
headless turn carries on without the server and exits 0, showing nothing in its reply.
