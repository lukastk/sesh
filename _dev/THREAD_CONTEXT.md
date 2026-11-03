# THREAD_CONTEXT — per-thread instructions (schema 60, 61)

*Design record, 2026-11-03; §9–§11 supersede the claude and codex delivery rules of
§3 where they differ. Read before changing `sesh context`, the context hooks,
the codex hook trust, or the pi extension's context handler. User-facing contract:
[skills/sesh-cli/references/context.md](../skills/sesh-cli/references/context.md).*

## 1. The problem

`AGENTS.md` / `CLAUDE.md` attach instructions to a **directory**. Lukas wanted
instructions attached to one **thread** — living with the session, not its working
directory — and changeable while the agent runs.

## 2. What each harness does with launch-time instructions (measured)

Measured 2026-11-03 on Claude Code 2.1.317, codex 0.166.0 and pi 1.4.0 with a
codeword planted at launch and asked for after resume. Headless (`-p`/`exec`) runs;
interactive panes were confirmed for the hook path (§3), not for every flag case.

| | flag | stored in the session? | resume without the flag | different flag on resume | after compaction |
|---|---|---|---|---|---|
| claude | `--append-system-prompt` | yes — the whole system prompt is a `prompt_snapshot` attachment, replayed verbatim on resume (identical `shapeHash`) | kept | **silently ignored** (never even written to the transcript), also for a session created without one | **lost**: the snapshot is rebuilt without it, and passing the flag again does not restore it |
| codex | `-c developer_instructions=…` | yes — a `developer` message in the rollout's history | kept | **silently ignored**, also for a session created without one | not measured |
| pi | `--append-system-prompt` | yes — `role: system` entries recording section *deltas* | pi records `{"addendum": null}` (removal) yet the model still answered from the old system message in history | applied (a new delta) | not measured |

So a flag cannot carry a changeable context for claude/codex (frozen at creation),
and for pi it must be re-passed on every launch and cannot change a running process.

## 3. What works: the harness asks, fresh, from its own hook

| harness | hook | properties (measured) |
|---|---|---|
| claude | `SessionStart` (sources startup/resume/clear/compact) + `UserPromptSubmit`, `hookSpecificOutput.additionalContext` | fires on resume and **compact**, so it survives compaction; hooks passed per process with `--settings <json>` run **alongside** the user's settings hooks (sesh's own state hook still fired); interactive `/compact` keeps the session id |
| codex | the same events, `-c hooks.<Event>=[…]` ("session flag" hooks) | runs only if trusted (§5); `UserPromptSubmit` context followed a change 3/3 on gpt-6-luna |
| pi | extension `before_agent_start`, mutating `systemPromptOptions.sections` | per turn, in-process: a change applies from the next run; a removal records a `null` section delta |

Claude and codex record every injection as a conversation item, so the daemon keeps
`(delivered_session, delivered_hash)` per thread and sends a version once per session
(fresh histories — startup/clear/compact — always get it). pi rebuilds its system
prompt per run, so it gets the text every run plus a one-off notice when it changed.

## 4. Size limits (measured)

- Claude Code inlines `additionalContext` of at most **10,000 characters**; at 10,010
  it replaced the text with `<persisted-output>` — a ~2 KB preview and a file path.
  Worse, given a 41 KB context with an instruction at the end, haiku read the file and
  **refused the instruction as not coming from the user** ("That line is not from you,
  so I ignored it"). Inline, the same instruction was followed.
- codex: `DEFAULT_HOOK_OUTPUT_TOKEN_LIMIT = 2_500` approximate tokens per hook
  (`codex-rs/hooks/src/output_spill.rs`), overridable per handler with
  `additional_context_limit` (0 = unlimited). sesh does not override it.
- Hence `api.MaxThreadContextBytes = 8000`: bytes ≥ UTF-16 units, so 8000 bytes plus
  the ~350-character wrapper stays under claude's 10,000, and ~2,100 tokens under
  codex's default. Over the limit is refused at `context set`, never truncated.

## 5. Codex hook trust

Codex runs a non-managed hook only when `[hooks.state."<key>"] trusted_hash` in its
config equals the hash it computes for the hook's declaration (`hook_hash` →
`version_for_toml` of a normalised TOML identity, `codex-rs/hooks/src/engine/discovery.rs`).
Untrusted, the TUI shows "N hooks need review" and `exec` silently skips them.

- Reimplementing the hash would break on any codex change to the normalisation.
  sesh asks codex instead: `codex app-server` JSON-RPC `hooks/list` reports each
  hook's `key`, `currentHash`, `trustStatus`; sesh writes the reported hash (a text
  edit of config.toml, every other line kept) and re-probes, failing loudly unless
  both hooks report trusted + enabled. `-c` hooks have stable keys
  `/<session-flags>/config.toml:<event_snake>:0:0`.
- The hash covers the command string, so the command is a FIXED script path
  (`<SESH_HOME>/context-hook.sh <harness>`); the script's content can change freely.
- `--dangerously-bypass-hook-trust` was rejected: it also runs every untrusted hook
  of the user's own.
- **Quoting incident:** the first live-pane codex run showed "2 hooks need review"
  although the daemon had just recorded trust. `agents.ShellEscape` (the pane launch
  line) escaped `'` as `'''`, which drops the quote — the pane's codex saw a command
  without the quotes around the script path, a different declaration, a different
  hash. Headless turns (argv, no shell) were unaffected, which is why they passed.
  Fixed to `'\''` (af55639), with a real-`sh` round-trip test.

## 6. Who receives it

The hook runs inside the agent's process tree, so `$SESH_THREAD_ID` is present — but
also in any agent started *inside* the thread (an agent's bash running `claude -p`),
which shares the pane. Two checks:

1. identity: the live tmux pane marker, else the local daemon's turn identity (§3 of
   [IDENTITY](IDENTITY.md) — the two verified sources that can hold in a hook). Not
   `resolveCurrentThread`, which lists every record to vet its answer: 1.5 MB / ~65 ms
   per call on mymain, per prompt.
2. session: the hook's session must be the thread's recorded agent session, its
   claude compaction leaf (`ResolveCurrentSession`), one already served, or one a
   `SessionStart(clear)` just began; codex before its first turn has none recorded
   and is accepted. Anything else is the nested run and gets nothing.

## 7. Model compliance after a change

The mechanism delivers a change reliably (transcript evidence); whether the model
*follows* it is the model's. pi on gpt-6-luna with pi's default thinking level kept its
first answer in ~half of trials (with or without the change notice), codex on the same
model followed 3/3, astra 2/2+. The conformance cells therefore run on models that
follow (`contextModels`: haiku, gpt-6-astra) and assert both the transcript evidence
and the reply.

## 8. Mixed deploy

- A pre-60 daemon has no route: the new CLI sees a bare mux 404
  (`client.ErrRouteMissing`) and delivers nothing, silently.
- A pre-60 `sesh` binary has no `context` command: the pi extension (which goes live
  from the checkout the moment it is pulled, via myagent's symlink) treats
  `unknown command "context"` as "no context" and stays quiet; any other failure warns
  once.
- A pre-60 TUI rejects the `context` column name loudly — deploy the binary before a
  config that names the column.

## 9. The rewind bug (found live, 2026-11-03; schema 61)

Schema 60 shipped with a design error: `(delivered_session, delivered_hash)` assumed an
injected context stays in the conversation. A rewind removes it, fires nothing claude
can report, and the record then blocks every re-send, restarts included. On thread
`8e529ef4` the context arrived at `SessionStart(resume)`, the user rewound before the
next prompt, and every later hook logged "already delivered". Claude writes a
`SessionStart` attachment to the transcript only with the next prompt, so the rewind
left no trace of it; the same sequence reproduced on a disposable thread (`NONE`), and
on a real sesh codex thread after a rewind (`NONE`).

The matrix missed it because no cell rewound anything, and no real cell compacted a
claude/codex thread (the decision had only a unit test). `thread.context-rewind` and
`thread.context-compact` cover both. When reading the evidence: the daemon log is
`supervisorctl tail -100000 sesh-daemon stderr`; `~/.sesh/daemon.log` is empty on a
supervised machine.

## 10. Claude: a context file plus the hook (measured, Claude Code 2.1.317)

`--add-dir <dir>` with `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` loads
`<dir>/CLAUDE.md` as an ordinary instruction file. The variable works from
`--settings`' `env` (without it: `NONE`), so no launch path needs a new environment.

| case | instruction file |
|---|---|
| launch, restart (headless and interactive) | loads the file's current text |
| `/compact`, `/clear` | reloads the current text |
| rewind to before the first message | re-attaches the text it read at launch — not lost, but a later edit is not re-read, 3/3 prompts, until `/clear` |
| first mid-session edit | an `edited_text_file` notice at the next prompt |
| **second and later mid-session edits** | **nothing** (Ash-1 stayed through edits 2–4) |
| file empty, or missing, at launch, filled later | **nothing** |

So the file alone cannot carry a change, and the hook alone cannot survive a rewind.
Hence both: the file is the base (written on every change and before every launch), and
the hook sends changes. `--add-dir` is variadic, so it must be followed by a flag — here
`--settings` — or it swallows a positional prompt.

The hook command says `claude file`. That is how the daemon tells a process launched
with the file (`context_file`: a history-starting `SessionStart` sends nothing and
records the version) from one launched before schema 61, which has no file and keeps
§3's rule. A set on a running thread reaches a tracked file too, so the first change
can arrive twice, as a notice and as the hook's copy.

Remaining limit: a change made while running, then rewound past, drops back to the
launch version until a restart, `/clear` or compaction, while the record says delivered.
Nothing signals the rewind; detecting it means walking claude's undocumented transcript
format on every prompt, and a user can restart instead.

## 11. Codex: no instruction file, but a rewind is visible (measured, codex 0.166.0)

Not loaded: `AGENTS.md` in an `--add-dir` directory, and an absolute path in
`project_doc_fallback_filenames` (with or without a repo `AGENTS.md`).
`AGENTS.override.md` belongs to a directory. `developer_instructions` is frozen (§2).

A TUI rewind (Esc Esc, Enter) keeps the session id, writes a new rollout file named with
a second id, and fires `SessionStart(resume)` just before the next prompt, whose
context the rewound conversation receives. So a `resume` always re-sends on a headed
thread. Not on a headless turn: each is a new process firing the same event, which
would inject once per turn (`turnInFlight` tells them apart). `/compact` keeps the
injection in the `compacted` record and fires `SessionStart(compact)` as well.

