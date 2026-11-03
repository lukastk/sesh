# Thread context

A thread can carry a **context**: a block of text its agent receives as standing
instructions. It belongs to the *thread*, not to a directory — `AGENTS.md` /
`CLAUDE.md` apply to every agent working in a folder, a thread's context to that one
thread wherever it runs. Use it for a brief, a role, constraints or facts the agent
should keep in mind for the whole conversation.

```sh
sesh context set --file brief.md [--id <id>]        # set (or replace) it from a file
echo "Reply in French." | sesh context set --file -  # … or stdin
sesh context set --text "Be terse." [--id <id>]
sesh context show [--id <id>] [--json]               # print it (--json: text, hash, updated_at_unix)
sesh context edit [--id <id>]                        # $VISUAL/$EDITOR; an emptied file removes it
sesh context clear [--id <id>]
```

`--id` is optional (the current thread is inferred, like every per-thread verb) and
`--machine <m>` routes to the thread's owner, where the text is stored. At most
**8000 bytes**: that is what every harness accepts inline (Claude Code replaces hook
context over 10,000 characters with a short preview and a file path). A longer text
is refused, not truncated. Shell, virtual and divider threads run no agent, so they
cannot have one.

## How the agent receives it

The agent asks for its context from its own extension points every time, so setting,
changing or clearing it applies to an agent that is **already running** — no
restart:

| harness | how | when a change arrives |
|---|---|---|
| claude | `SessionStart` + `UserPromptSubmit` hooks that sesh adds to every claude it launches (`--settings`; your own settings hooks still run) | at the next prompt, or when the session next starts, resumes, clears or compacts |
| codex | the same two hooks, added with `-c hooks.…` and trusted by the daemon in the codex config | at the next prompt or session start |
| pi / mypi | the `sesh-agent-state` extension, before every turn: the context is a section of the system prompt | at the next turn |

Claude and codex keep each injection in the conversation, so sesh sends a given
version **once per agent session** (and again after a compaction or `/clear`, which
drop it). A change is sent as a new version that says it replaces the earlier one; a
clear is told as "the context was removed". The earlier text stays visible in the
conversation history until a compaction, so a model may still recall it — the newest
version is always the one marked current.

Only the thread's **own** agent receives it: the hook checks that it runs in the
thread's pane or one of its headless turns (its verified identity), *and* that the
agent session is the thread's. Another agent started inside the thread — say an
agent running `claude -p` from its shell — inherits the pane but gets nothing.

Agents sesh did not launch (an adopted session, a `claude` you started yourself) have
no context hooks until sesh next relaunches them (`thread resume`, a headless turn).
pi and mypi are the exception: their extension is global.

## Where it shows

- `sesh info` prints a `context:` line; every thread record carries `context_hash`
  (non-empty = has a context) — `thread grid --json`, `thread list --json`.
- The TUI's **CTX** column marks threads with a context (`◆`). It is not in the
  default column set; add it with `--columns …,context,…` or a `[[tui.column]]`
  entry. Palette commands **context-view** (scrollable read; `e` edits) and
  **context-edit** (your editor, back to the TUI on exit). See [TUI](tui.md).

## Edits and conflicts

`context edit` and the TUI's `context-edit` save **conditionally**: if someone
changed the context while you were editing, the save is refused and your text is
kept in the temp file the error names. Scripts get the same with
`context set|clear --expect-hash <hash>` (the `hash` from `context show --json`;
`none` = only if there is no context yet).

`context set --file` and `context edit` read and write files on the machine you run
them on. Routed to a peer over its HTTP API (`--machine`, any peer with an
`api_addr`) that is still your machine; an ssh-only peer would run them on the peer.

## Troubleshooting

- `sesh doctor` lists **context hooks** (the hook script sesh hands every claude and
  codex) and **codex context hooks** (whether codex accepted their trust). Codex
  refuses to run an untrusted hook, so sesh refuses to launch codex when it cannot
  record the trust — the doctor line says why.
- A `[spawn.claude] args` entry passing `--settings` is refused: sesh needs that flag
  for the hooks. Put those settings in claude's own settings files.
- The daemon log notes each delivery it skipped for a thread that has a context
  (`context: thread … not delivered — …`), e.g. a nested agent run.
