# Inspecting, creating, reviving, and navigating threads

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
## Entering, listing, inspecting

```bash
sesh tui --all-machines             # the live grid (enter to jump to a thread)
sesh thread list --all-machines      # flat list across the mesh (--json for scripts)
sesh thread grid --all-machines      # list + live head/busy/attachment per thread
sesh mesh                            # merged cross-machine view + per-peer freshness
sesh info <id>                       # one thread: record + both axes + tmux locator + tickets
sesh thread status --id <id> --json  # just the live runtime axes
sesh thread pane --id <id>           # the live pane locator (errors if dead)
sesh thread capture --id <id> --lines 80   # the live PANE TEXT — peek at what an agent
                                            # is showing (e.g. a child stuck on a prompt)
sesh tail <id> -n 50                 # last N transcript lines
sesh transcript <id>                 # whole transcript dump
```

`sesh thread capture` is the supervising-from-afar tool: a parent thread can read a
child's screen to see if it stalled on a multiple-choice prompt. It routes cross-machine
(`--machine`), resolving the pane on the owner.

## Creating, lifecycle, navigating

> ⚠️ **PARENT INFERENCE — read this before you create a thread.** `sesh thread new`
> defaults to making the new thread a **CHILD** of the thread you are running inside. With
> no `--parent` and no `--no-parent`, it infers a parent using the ordinary current-thread
> precedence (pane marker, verified daemon turn, corroborated harness id, then env hint).
> **So an agent that spawns a thread will, by default, create a child of itself.** This is
> correct only when you genuinely mean to delegate a sub-task.
>
> The inferred parent is **announced on stderr**, naming the thread and the provenance it
> came from (`sesh: parenting under "boxyard-go" (1777a4ac) — inferred from pane …`), so a
> mis-parent is visible immediately rather than a day later. If the id here is *unverified
> and contradicted* (see [identity](identity.md)), it refuses to infer, says so, and
> creates a **root** thread.
>
> **If the thread is meant to stand alone (a top-level/independent thread), you MUST pass
> `--no-parent`.** Otherwise it will be a child. Be explicit:
> - `--parent <id>` — child of a specific thread.
> - *(neither flag)* — child of the **current** thread (inferred). Standalone only when run
>   from outside any thread.
> - `--no-parent` — force a **root** thread regardless of context.

> **AGENT HARNESS POLICY:** When an agent creates an ordinary thread with `sesh thread new`,
> it MUST omit `--agent` unless the user explicitly requested a particular harness. Let the
> owning daemon apply its configured `[defaults] agent`; do not hard-code the current agent's
> harness or choose one on the user's behalf. (`--fork-from` inherits the source thread's
> harness before consulting the configured default.)

`--cwd` accepts a **relative path** (expanded against the directory where you run the
command) or **`~` / `~/…`** and **defaults to the current dir (`.`)** when omitted. A
leading `~` is **resolved by the OWNING daemon against THAT machine's home**, not the
caller's — so a `~`-relative cwd is **portable across a `--machine` spawn** (e.g.
`--cwd '~/proj' --machine macbook` lands in macbook's `~/proj`). **Quote the tilde**:
unquoted `~` is expanded by the caller's shell before sesh sees it. A bare relative
path is only meaningful locally; outside `~`, pass the destination's absolute path.

```bash
sesh thread new --name defaulted --cwd ~/proj                       # uses [defaults] agent; loud if unset
sesh thread new --agent claude --name fix-bug --cwd ~/proj          # explicit agent overrides the configured default
sesh thread new --agent pi --cwd ~/proj                              # --name is OPTIONAL (a nameless thread)
sesh thread new --agent pi --name notes --cwd . --headless           # headless; cwd = $PWD
sesh thread new --agent codex --name sub --cwd ./src --parent <id>   # a child of a specific thread
sesh thread new --agent claude --name solo --cwd ~/p --no-parent     # a ROOT thread (standalone; suppress inference)
sesh thread new --agent claude --name try --cwd ~/p --fork-from <id> # branch a conversation (agent inherits from source if omitted)
sesh thread new --agent pi --name fast --cwd . --headless --model anthropic/claude-haiku-4-5  # pin an agent model

# --model pins an OPAQUE agent model on the thread (no curated list — a bad model fails
# LOUDLY at the agent), applied on spawn, resume, AND every headless turn. Empty = the
# agent's own default. Each agent takes its own spelling: claude `haiku|sonnet|opus|<id>`,
# codex `gpt-5.5|<id>`, pi `provider/id[:thinking]` (e.g. anthropic/claude-opus-4-8).

# Placement — a tmux session may host MANY threads (identity is the pane marker,
# not the session). Default = own new session; otherwise:
sesh thread new --agent pi --name win  --cwd . --into-session <name>   # a new WINDOW of an existing session
sesh thread new --agent pi --name beside --cwd . --into-window <pane>  # a SPLIT beside a pane (or session:window)
exec sesh thread new --agent claude --name here --into-pane "$TMUX_PANE" --exec  # run the agent IN the current shell pane
#   --into-pane is register-then-exec: sesh records the thread + marks the pane,
#   then (with --exec) replaces THIS process with the agent so it takes over the
#   pane. cwd defaults to the pane's. Without --exec it prints the launch command.

sesh thread new --virtual --name "project X"                         # a VIRTUAL grouping node (no agent; cwd optional)
sesh thread realize --id <id> --agent claude --cwd ~/proj            # convert a virtual thread into a real one, in place

sesh thread stop --id <id>           # end runtime (kills the thread's PANE; a session shared with siblings survives), keep the record (revivable)
sesh thread resume --id <id>         # revive a dead thread into a fresh pane (restores convo)
sesh thread headful --id <id>        # promote a live HEADLESS thread into a pane
sesh thread headful --id <id> --force   # ...and first stop a claude BACKGROUND SESSION that owns the
                                        # conversation (see "A thread that will not come back", below)
sesh thread delete --id <id>         # drop the record (refuses a live thread; stop first); children promote to the grandparent

# ── A CLAUDE THREAD THAT WILL NOT COME BACK: a held background session ──────────────
# A claude conversation can be OWNED by a BACKGROUND SESSION, and while it is, claude
# refuses to `--resume` it — the thread cannot be revived. THE CAUSE (measured, both
# production cases reproduced): pressing ← TWICE on an empty claude prompt opens claude's
# agents view, which moves the conversation into a background session (`continued-in`
# record in the old transcript; claude's daemon log tags it `(slash)` = "from the REPL").
# Nothing is typed, so nothing shows in any history. Easy to do by accident: ← is "fold"
# in the sesh TUI and "go left to the sidebar" in the cockpit.
#
# sesh now launches every claude pane with CLAUDE_CODE_DISABLE_AGENT_VIEW=1, so ← ← does
# nothing there (subagents and background shells still work). A hold can still come from
# `claude --bg` run by hand, or from before this change. sesh follows the `continued-in`
# chain, so the session a revive tries to resume is exactly the held one.
#
# The failure NAMES the holder, its state and every remedy. To see it coming instead:
sesh doctor                          # reports every thread whose conversation a background session owns
claude agents --json                 # claude's own registry: kind == "background"
claude attach <short-id>             # open the background session here, without stopping it
claude stop <short-id>               # release it; the conversation is KEPT (`--resume` works after)
sesh thread headful --id <id> --force   # do both: stop the holder, then a REAL resume
#
# --force is never implied: stopping a holder whose state is "working" interrupts a turn
# running right now, so neither the TUI's revive nor a scheduled one ever forces.
# NB measured NOT to cause it: `sesh thread stop` / the TUI's `x` (kills the pane — even
# mid-tool), Ctrl-C, Ctrl-B, /quit, /clear, a claude auto-update.
sesh thread archive --id <id>        # park it; --unarchive to restore
sesh thread hold --id <id> --until 2026-07-01          # park until a date (hidden from the default view); auto-expires
sesh thread hold --id <id> --release                   # release it (+ its subtree) from an ANCESTOR's hold, until tomorrow
sesh thread hold --id <id> --clear                     # clear both; it inherits from its ancestors again

# Manual ordering: pin a top-level thread ABOVE the auto-sorted list (default: top).
sesh thread pin --id <id>                              # pin to the top of the manual block
sesh thread pin --id <id> --after <other>             # or --before <other> / --bottom / --top / --order <f>
sesh thread unpin --id <id>                            # remove the manual ordering (rejoins the auto block)
sesh thread new --divider --name "today"              # a DIVIDER: a labeled rule in the pinned block (reposition with pin)
#   Only top-level threads can be pinned; archiving or reparenting-under-another clears it.
#   A divider takes no agent-shaped flags; delete it with `thread delete` (not archive/unpin).

sesh thread rename --id <id> --name <new>
sesh thread tag --id <id> --add wip --remove stale     # repeatable --add/--remove
sesh thread reparent --id <id> --parent <p>            # or --root to detach
sesh thread notify --id <id> --off                     # mute this thread's notification hooks

# Adopt a manually-launched agent (a pane on sesh's WORK server) into a thread.
# The conversation id is auto-detected (claude from argv, pi from its RPC socket,
# codex from its rollout) — pass --session-id when it can't be (e.g. a claude
# started with a bare `-r`, which carries no id in its argv):
sesh thread adopt --name here                                  # current pane ($TMUX_PANE)
sesh thread adopt --name here --session-id <conversation-uuid> # explicit id

# HEADLESS adopt: register an EXISTING conversation that is NOT running anywhere
# (e.g. a claude transcript on disk) as a durable headless thread. No pane is used,
# so --agent and --session-id are REQUIRED (nothing to detect them from); --cwd
# defaults to '.'. A later `send-headless` RESUMES that conversation:
sesh thread adopt --name corkboard --agent claude --session-id <conversation-uuid> --cwd ~/dev/corkboard

# Spawn on another machine (real cross-machine spawn over the mesh):
sesh thread new --agent claude --name x --cwd '~/proj' --machine macbook
```

`enter`/nav is normally done from the TUI; the underlying primitive is `sesh tmux nav --to
<machine>:<session>` (mycockpit + the inner client switch).

The cockpit's flagged ring (master `prefix+,` / `prefix+.`) is one call:

```bash
SESH_NAV_CLIENT=<master client> sesh tmux nav --cycle-flagged next   # or prev
sesh tmux nav --cycle-flagged next --dry-run   # JSON plan: outcome, ring, current, target, from
```

It steps through the **flagged** threads of the `active` view (every machine, offline
peers hidden) in the TUI's own render order, wrapping, stepping over headless rows (a
cycle key never revives). Where the cockpit is now is resolved **once** and used both as
the start point and as `prefix+L`'s from-location. An empty ring refuses loudly and
distinctly: `no flagged active threads` vs `flagged threads are all dead`. Without
`$SESH_NAV_CLIENT` there is no start point: `next` enters at the first entry, `prev` at the
last.
