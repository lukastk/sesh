# THREAD_ENV — per-thread environment variables (schema 62)

*Design record, 2026-11-04. Read before changing `sesh env`, `thread restart`, the
claude env hook, or anything that changes an agent's environment after launch.
User-facing contract: [skills/sesh-cli/references/env.md](../skills/sesh-cli/references/env.md).*

Measured on ideapad with Claude Code 2.1.318, codex-cli 0.165.0 and pi 1.4.0: real
interactive agents in an isolated tmux server (`claude --model haiku`, `gpt-6-astra`),
each writing `printenv` to a file from its own shell tool, so results are the process
environment and not a model's report.

## 1. Two different targets

- **Tool env:** what the agent's shell commands see. Can change live (§2).
- **Harness env:** the agent process itself and what it already spawned (MCP servers,
  provider config such as a base URL). Claude and codex need a restart. pi's
  `process.env` is writable, but what pi reads only at startup is not re-read; not tested.

## 2. Changing tool env in a running agent (measured)

| harness | mechanism | add/change | remove |
|---|---|---|---|
| claude | hook env files + `FileChanged` (below) | live, from the next command | live |
| pi | `process.env` mutated by an extension in `before_agent_start` | live, from the next turn | live (`delete`) |
| codex | `.zshenv` read per command, through a `ZDOTDIR` shim | live | needs an explicit `unset` |

**Claude, what failed.** `CLAUDE_ENV_FILE` set at launch is read once and then cached:
edits were ignored. An `env` block in the `--settings` file is not hot-reloaded either.

**Claude, what works.** `SessionStart`, `FileChanged` and `CwdChanged` hooks receive
`CLAUDE_ENV_FILE` (`UserPromptSubmit` and `PreToolUse` do not). A `SessionStart` hook
returns `hookSpecificOutput.watchPaths` naming a file, and a `FileChanged` hook whose
matcher is that file's basename then fires on change, which refreshes the cache.

Copying values into `$CLAUDE_ENV_FILE` is wrong: claude sources every hook env file, so
a key removed from `filechanged-hook-0.sh` reverted to its stale value in
`sessionstart-hook-0.sh`. Each hook must write one line, `. <thread env file>`. That
variant passed: atomic rename, key removal, `unset` of a launch-time variable, and a
later change after both `/compact` and `/clear` (`SessionStart` refires for each).

**Codex.** Commands run in a fresh `zsh`, so `.zshenv` is re-read; `BASH_ENV` does
nothing here because the shell is zsh. Fleet `$SHELL` is zsh on all six machines
(termux has no codex). Codex also keeps a shell snapshot (§4) whose exports survive:
a key merely dropped from the file kept its old value, while `export` and `unset` in
`.zshenv` both won. So removals must be written as `unset` until the next restart. The
ordering inside codex's wrapper was not observed. The shim must also forward `.zprofile`,
`.zshrc` and `.zlogin`, and `ZDOTDIR` is inherited by everything the agent starts.

## 3. Restarting in place (measured)

`respawn-pane -k` plus the resume command delivered the new env with the conversation
intact: a planted codeword was recalled on codex and pi. Codex showed no writer-lock
error (`daemon_auto_start=false`, sesh#15). Claude already does this in
`restartClaudeOnModel`. A restart kills a turn in flight, as `switch-model` warns.

Codex's "Update now" prompt appeared on one `codex resume` and sat in front of the
composer, swallowing the probe's input. It did not appear on the next launch.

## 4. Findings that constrain the design

- **Do not put user values in `tmux -e` at `new-session`.** They land in the SESSION
  environment: a plain `new-window` and a sibling with its own `-e` both saw the value
  (`show-environment` lists it). Source a file in the launch command instead. Launch
  lines go through a shell, so values need `agents.ShellEscape` and keys a strict
  `[A-Za-z_][A-Za-z0-9_]*` check.
- **Codex writes the whole environment, in plaintext, to
  `~/.codex/shell_snapshots/<session>.<n>.sh`, mode 0644 in a 0755 directory.** That
  includes every secret exported to a codex launched from a shell, independent of this
  feature. Files from the killed probe sessions were left behind (and were removed by
  hand). Whether a config key disables this was not tested.
- **`sesh.db` is 0644, and backups copy it.** Thread rows replicate to peers
  (`peer_threads`), so values must stay out of rows: follow `context_hash`, with values
  fetched from the owner on demand.
- Codex does not filter secret-looking names on this config: `*_API_KEY`, `*_TOKEN`,
  `*_SECRET` and `*_PASSWORD` all reached the shell.

- **Refuse zsh's special parameters.** `export LINES='a b'` is a math error that aborts
  the sourced file, so every later variable silently disappears (found by the round-trip
  test). `agents.zshSpecialNames` is checked against the local zsh by a test.
- **`.zshenv` wins on pi and codex (open).** They run commands in a fresh `zsh`, which
  re-reads it after the launch environment, so any name it exports replaces the thread's
  value. Measured with `HF_TOKEN`: claude kept the thread's value at launch and live (its
  hook file is sourced after shell startup); pi and codex got myrig's. myrig exports 126
  names, including `GITHUB_TOKEN`. A last line in `.zshenv` sourcing
  `$SESH_THREAD_ENV_FILE` would fix it, and make codex follow live (§2), and needs the
  variable added to headless turns; not done because it is a rig-wide change in another
  repo that reverses the restart-only codex decision. Lukas's call.

## 5. What was built, and why

- **Values in 0600 files, not the store.** `<SESH_HOME>/thread-env/<id>/env.json` is the
  truth and `env.sh` its rendering; the row carries `env_count` only (§4). Plain values:
  `secret:` references were considered and dropped as unneeded complexity.
- **Claude:** `FileChanged` + `SessionStart` hooks running `sesh env hook`, which writes
  the pointer line. The hooks are added to the `--settings` sesh already passes.
- **pi:** `integrations/pi/sesh-agent-state/env.ts` reads `env.json` before each run, found
  through `$SESH_THREAD_ENV_FILE`, with no CLI call or route: an agent older than the
  variable predates the extension code too.
- **Codex: restart-only** by decision, rather than the `ZDOTDIR` shim of §2, which forwards
  the user's dotfiles and is inherited by everything the agent starts.
- **`thread restart`** generalises the claude-only restart of `switch-model`. A
  conversation with nothing on disk starts fresh on the same session id, because
  `claude --resume` refuses a session with no transcript.
- **Launch line:** `. <env.sh>; exec <agent>`, only for threads with variables, so every
  other thread launches on the same line as before. `exec` keeps the agent the pane's own
  process.
- **Removals** stay in `env.json` as `removed` until the next launch: the launched
  process, and codex's snapshot, still hold the value, so `env.sh` must say `unset`.
- **Inheritance** (Lukas): children, forks and delegate workers copy the source's variables;
  `--no-inherit-env` opts out. `delegate` passes `env_from` because its worker is a root.

## 6. Mixed deploy

A pre-62 daemon 404s the routes (the CLI reports the missing route), ignores `env`,
`no_inherit_env` and `env_from` on `thread new`, and omits `env_count`. A pre-62 `sesh`
binary has no `env` command, but claude's hook runs `$SESH_BIN`, the launching daemon's own
binary, so the two cannot disagree. The pi extension is inert without
`$SESH_THREAD_ENV_FILE`, which only a schema-62 daemon sets. Agents already running have no
hook: the owner answers `restart-required` for such a claude; a running pi has neither the
extension code nor the variable until it restarts, and is reported `live` regardless (env.md).

## 7. Not measured

A claude subagent's Bash; `mypi`; a change arriving mid-turn; whether long-lived codex
unified-exec sessions keep their old env. Shell threads were left out: they run no agent.
