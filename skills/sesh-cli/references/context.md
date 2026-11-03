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

The context stays in the owner's database. Each harness then receives it in its own
way, so setting, changing or clearing it applies to an agent that is **already
running** — no restart:

| harness | how it is held | when a change arrives |
|---|---|---|
| claude | a per-thread instruction file, `<SESH_HOME>/thread-context/<thread-id>/CLAUDE.md`, loaded with `--add-dir` like any other `CLAUDE.md`; a `UserPromptSubmit` hook carries changes to the running process | at the next prompt; the file itself is reloaded on restart, `/clear` and compaction |
| codex | `SessionStart` + `UserPromptSubmit` hooks added with `-c hooks.…` and trusted by the daemon in the codex config. Codex has no per-session instruction file | at the next prompt or session start |
| pi / mypi | the `sesh-agent-state` extension, before every turn: the context is a section of the system prompt | at the next turn |

The file is a copy that sesh rewrites on every change and before every launch: edit
the context with `sesh context`, not the file. It lives outside the working
directory, so nothing appears in a repository. `--add-dir` also lets that Claude read
and write the folder, but what it writes there is overwritten and never reaches the
database. Deleting a thread removes its folder.

Claude and codex keep each hook injection in the conversation, so sesh sends a given
version **once per agent session**. A change is sent as a new version that says it
replaces the earlier one; a clear is told as "the context was removed". The earlier
text stays visible in the conversation history until a compaction, so a model may
still recall it — the newest version is always the one marked current.

### Rewinding a conversation

A rewind removes the later part of a conversation, including anything injected there.

- **claude:** a context the thread had when Claude started is safe, because it is an
  instruction file. A context set or changed *while it runs* arrives through the hook,
  so rewinding to before that prompt returns the agent to the version it started with.
  A restart, `/clear` or compaction brings it up to date.
- **codex:** a rewind fires a session start, and sesh re-sends the context then, so the
  next prompt has the current version. Each rewind or restart adds one copy.
- **pi / mypi:** not affected: the context is applied on every turn.

Only the thread's **own** agent receives it: the hook checks that it runs in the
thread's pane or one of its headless turns (its verified identity), *and* that the
agent session is the thread's. Another agent started inside the thread — say an
agent running `claude -p` from its shell — inherits the pane but gets nothing.

Agents sesh did not launch (an adopted session, a `claude` you started yourself) have
no context hooks or file until sesh next relaunches them (`thread resume`, a headless
turn). pi and mypi are the exception: their extension is global. A Claude that was
already running when sesh gained the context file gets it at its next restart; until
then its context arrives only through the hook, so a rewind can remove it.

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
  codex; claude's context files live beside it in `thread-context/`) and **codex context hooks** (whether codex accepted their trust). Codex
  refuses to run an untrusted hook, so sesh refuses to launch codex when it cannot
  record the trust — the doctor line says why.
- A `[spawn.claude] args` entry passing `--settings` is refused: sesh needs that flag
  for the hooks and the context file. Put those settings in claude's own settings files.
- The daemon log notes each delivery it skipped for a thread that has a context
  (`context: thread … not delivered — …`), e.g. a nested agent run.
