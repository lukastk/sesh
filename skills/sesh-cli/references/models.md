# Agent models

A thread has two models. Its **registered model** is the `model` on its record: what sesh passes as
`--model` when it spawns, resumes or runs a headless turn on that thread. Model
names are opaque and spelled the harness's own way — claude `opus` / `haiku` /
`claude-fable-5-1[1m]`, codex `gpt-6-astra`, pi `provider/id[:thinking]` (e.g.
`anthropic/claude-haiku-4-5`). Sesh keeps no list of valid names; a bad one fails
loudly at the agent.

Its **observed model** is what the thread's own transcript says it last ran on,
read by the owning daemon from the harness's records (claude assistant entries,
codex `turn_context`, pi `model_change` / assistant messages) — never stored, never
guessed. It moves when the agent is switched in-session or a turn runs with a
one-turn `send-headless --model`, and it is empty until the first turn. A settled
**archived** thread is not observed (its model cannot change, and a machine holds
thousands); it is again once it runs. Claude does
not record the context-window suffix, so `opus` and `opus[1m]` both observe as
`claude-opus-5-5`.

```sh
sesh info <id>                    # model:  opus · last ran claude-sonnet-5-5
sesh info --json <id>             # thread.model + observed_model
sesh thread grid --json           # observed_model on every row (the TUI MODEL column)
```

## Changing a thread's model

```sh
sesh thread set-model --model opus [--id <id>]        # registered model only
sesh thread set-model --clear [--id <id>]             # back to the harness default
sesh thread switch-model --id <id> --model sonnet     # registered AND live
sesh thread switch-model --id <id> --model gpt-6-astra --force   # even mid-turn
```

`set-model` changes what sesh launches the thread with from now on (spawn,
resume, headless turns). A live agent keeps running the model it has.

`switch-model` also switches the live agent. Every route is **session-only**:
no harness's own global default is written.

| live agent | what happens | outcome |
|---|---|---|
| claude | the pane is **restarted in place** (same pane, layout and marker) on `claude --resume <session> --model <m>`: the conversation continues, but the claude process — and any turn in flight — does not survive | `restarted` |
| codex | `/model <m>` is typed into the pane (through the typing guard) and must be confirmed by codex's "Model changed to …" | `switched` |
| pi / mypi | pi-rpc-socket `set_model` (a `:thinking` suffix is refused live) | `switched` |
| none (headless, idle) | the record changes; the next spawn, resume or turn uses it | `next-turn` |

Claude is restarted because there is no outside way to switch it for one
session: a typed `/model <m>` also saves `<m>` as claude's global default in
`~/.claude/settings.json`, which would change every new claude session.

A thread that is mid-turn is refused unless `--force` (for claude that kills the
turn). `switch-model` needs an explicit `--id`, like `stop`: it is never inferred
from the current thread, since restarting your own claude pane ends your process.

In the TUI the palette commands **`switch-model`**, **`set-model`** and
**`set-default-model`** open a fuzzy picker over the owning machine's model list —
see [TUI](tui.md).

## Which models a harness can use

```sh
sesh model list --agent claude                 # ids --model accepts, with labels
sesh model list --agent pi --machine macstudio --json
sesh model list --agent codex --refresh        # ask again instead of the 5-minute cache
```

The answer comes from the harness on the **owning** machine — its credentials and
config decide what it can use — never from a list sesh keeps:

| harness | source | notes |
|---|---|---|
| claude | its SDK `initialize` handshake (`claude -p --input-format stream-json`) | no model turn runs and no session is saved. Its "default" entry is reported as `harness_default` (the model claude runs given no `--model`), not offered as a model. This list is shorter than claude's interactive `/model` picker; any id claude accepts still works with `--model`. |
| codex | `codex debug models` | entries with `visibility: list` only. It is a `debug` command, so a format change is a loud error. |
| pi / mypi | `--list-models` | every model of every provider you have credentials for; ids are `provider/id`. |

## Default model per harness

Each harness may have a default model: the `model` key of its `[spawn.<agent>]`
table in the **owning** daemon's `config.toml`. A thread created without
`--model` (by `thread new`, a fork whose source has none, `thread realize`, a
schedule's spawn, `delegate`) gets the default and **records it**, so changing the
default later never moves an existing thread to another model. With no default
configured the thread records no model and the harness picks its own.

```sh
sesh model defaults                                      # every harness's default ('-' = none)
sesh model set-default --agent claude --model opus       # write [spawn.claude] model
sesh model set-default --agent pi --model anthropic/claude-haiku-4-5 --machine macstudio
sesh model unset-default --agent codex                   # remove it
```

- The daemon reads the default **fresh at every thread creation**, so a change —
  through these commands, a hand edit, or a synced copy of the file — applies to
  the next thread with no restart. (The rest of `[spawn]` is still read at start.)
- `set-default` edits **only that key**: every other line and comment is kept, a
  missing table is added next to `[spawn]`, and `unset-default` removes a table it
  leaves empty. A `[spawn.<agent>]` written as an inline table or dotted keys is
  refused (rewrite it as a header table).
- If `config.toml` is a **symlink** (a shared dotfiles copy), the write goes
  through it to the target; the link stays a link. A dangling link is refused.
- `--machine <m>` reads and writes machine `<m>`'s config, like every routed call.
- `mypi` has its own `[spawn.mypi]` default; it does not inherit pi's.
- A global `[spawn] model` is refused: one name cannot mean the same model in
  every harness.
