# Thread environment variables

A thread can carry environment variables of its own. They belong to the *thread*: every
launch and headless turn starts with them, wherever the thread's agent runs from.

```sh
sesh env set API_URL=https://staging.example DEBUG=1 [--id <id>]
sesh env set --file staging.env                     # KEY=VALUE lines; - reads stdin
sesh env unset DEBUG
sesh env show [--id <id>]                           # values masked
sesh env show --reveal [--json]
sesh env edit [--id <id>]                           # KEY=VALUE lines in $VISUAL/$EDITOR
sesh env clear

sesh thread new --env API_URL=https://… --env-file staging.env [--no-inherit-env]
sesh thread restart --id <id> [--force]
```

Flags go before the `KEY=VALUE` arguments. `--id` is optional (the current thread is
inferred, like every per-thread verb) except on `thread restart`, and `--machine <m>`
routes to the thread's owner.

`env edit` and `--file` read one `KEY=value` per line. Blank lines, `#` comments and a
leading `export ` are allowed. A value is literal unless quoted: `"…"` is unescaped like a
Go string (`\n`), `'…'` is taken as it stands.

Refused names: anything that is not a shell variable name, names sesh sets (`SESH_*`,
`TMUX`, `TMUX_PANE`, `CLAUDE_ENV_FILE`), and zsh's special parameters (`LINES`, `COLUMNS`,
`path`, `UID`, …), because assigning one can abort the sourced file and silently drop the
variables after it. Total size is limited to 64 KiB. Shell, virtual and divider threads run
no agent, so they cannot have variables.

## When a change takes effect

`env set` says which of these happened (`outcome` in `--json`):

| outcome | meaning |
|---|---|
| `live` | the running agent's shell commands have it: **claude** from the next command, **pi / mypi** from the next turn |
| `restart-required` | **codex**, or a claude not launched by sesh's current daemon: nothing changes until `sesh thread restart` |
| `next-launch` | nothing is running; the next launch or headless turn has it |

`thread restart` replaces the agent process in the same pane, on the same conversation. It
kills a turn in flight, so a thread that is mid-turn is refused without `--force`.

`live` covers shell commands only. On claude and codex the agent process and the MCP
servers it already started keep the environment they launched with, and pi does not re-read
what it read at startup. So a variable that configures the *agent*, such as a provider base
URL, or an MCP server, needs `thread restart`.

A pi that started before this feature, or was adopted rather than launched by sesh, has no
env file to follow and needs one `thread restart`.

## Variables your shell startup also sets

pi and codex run each command in a fresh `zsh`, which reads `~/.zshenv`. If that file exports
a name, its value replaces the thread's in those shells, and `env set` cannot tell. On this
rig that means the names myrig exports, such as `GITHUB_TOKEN` and `HF_TOKEN`. Claude is not
affected: it applies the thread's file after the shell starts. The variable is correct in
the agent process itself on all three.

## Inheritance

A new thread starts with a copy of its parent's variables, a fork with its source's, and a
`delegate` worker with its calling thread's. `--env` wins over an inherited value, and
`--no-inherit-env` (on `thread new` and `delegate`) starts it empty. After that the two
change independently. A thread copied to another machine does not take its variables along.

## Where the values live

On the machine that owns the thread, in `<SESH_HOME>/thread-env/<id>/` (0600 files in a 0700
directory), removed with the thread. They are **not** in `sesh.db`, on peers or in thread
rows: a row carries only `env_count`, so `sesh thread grid --json` is safe to share.

The agent can read them, and whatever it prints goes in its transcript. **Codex also writes
its whole environment in plaintext to `~/.codex/shell_snapshots/`, readable by other users of
the machine.**

## Where it shows

- `sesh info` prints an `env:` line; every thread record carries `env_count`.
- The TUI's **ENV** column shows the count. It is not in the default column set; add it with
  `--columns …,env,…` or a `[[tui.column]]` entry.
- The palette command **env-edit** opens the manager: `a` add, `e` edit, `d` `d` delete,
  `v` reveal, `r` `r` restart the agent. See [TUI](tui.md).
