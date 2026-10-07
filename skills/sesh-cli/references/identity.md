# Identity, selectors, and mutation safety

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
## Thread ids and id-prefixes

Threads are identified by a UUID. Every `--id` accepts an **unambiguous prefix**
(`sesh thread stop --id 1a2b3c4d`; an unknown/ambiguous prefix is a loud error — a FULL
well-formed uuid skips the prefix lookup entirely, so an unknown full uuid errors at the
verb itself via the daemon's 404 instead), and most
verbs infer the **current** thread when you omit `--id` (from the calling pane's live
`@sesh-thread-id` marker first, then — for a pane-less process — a headless **turn** the
local daemon launched, then the agent **harness's own session id**, then `$SESH_THREAD_ID`,
or a loud error if nothing resolves). The pane marker wins because it is re-stamped on adopt/reparent while
`$SESH_THREAD_ID` is frozen at launch and can drift stale; on disagreement the pane is
used and a drift note is printed to stderr. **See "Am I really this thread?" below —
outside a pane the answer is UNVERIFIED and may be refused.** Inference happens **only when `--id` is
omitted entirely**: passing an *explicitly empty* `--id ""` (or an empty positional id,
e.g. from an unset shell variable) is a **loud error**, never silently treated as the
current thread — so a stray empty `$VAR` can't make a verb act on the wrong thread. The
same holds for the other selectors that default to "everything"/"the current thread"
(`backup`/`restore --id`, `hooks test --thread`). `delete` and `stop` go further still —
being destructive, they **never** infer at all (an omitted `--id` is also an error), so
they always need an explicit `--id`. The TUI shows the short 8-char form (`i` toggles the
ID column; `y` shows the full UUID, `c` copies it). The copy goes to the clipboard of the
machine the TUI is **running on** — `pbcopy` on macOS, `wl-copy`/`xclip`/`xsel` on Linux,
`termux-clipboard-set` on termux (needs `pkg install termux-api` *and* the Termux:API
Android app). So a TUI opened in a cockpit window for a REMOTE machine copies to that
machine's clipboard, not yours. A TUI with no display in its env (a work-server popup)
takes the graphical session env from the systemd user manager; a failed copy is a loud
`✗` line naming the tool's own error.

## Am I really this thread? (provenance)

Inference has four sources and they are **not** equally trustworthy:

- **pane** — read from the `@sesh-thread-id` marker on the tmux pane the command
  actually runs in. **Verified**: a process elsewhere cannot inherit it.
- **turn** — the local daemon confirming that this process sits inside the process tree it
  created for a thread's **headless turn**. **Verified** for the same reason: the daemon
  started that turn, remembers its root process, and checks the live parent links — so a
  process outside the tree (anything detached, anything reparented) cannot claim it. This
  is how a `schedule spawn --headless` worker identifies itself.
- **harness** — your agent harness's OWN session id (`$CLAUDE_CODE_SESSION_ID`), matched
  against the `agent_session_id` the daemon recorded for exactly one thread. **Verified, but
  conditionally**: this is a claim you present rather than a fact read from your container, so
  it is accepted only when your cwd corroborates it AND that thread still has a live pane.
  This is what identifies a **reparented** claude agent — one whose tool calls run detached,
  so neither `$TMUX_PANE` nor process ancestry reaches its own pane.
- **env** — `$SESH_THREAD_ID` alone, when there is none of the above. **Unverified**: that variable
  is frozen at launch and inherited by every descendant, so a detached or background
  process (a claude bg job/agent, hosted by a machine-global `claude daemon run` that
  froze whichever pane started it) carries a perfectly *valid* id belonging to an
  *unrelated* thread.

**`sesh whoami` is the command to reach for.** It runs the same resolution with the safe
default: it prints the full uuid **only** when the identity is *verified*, and exits
non-zero otherwise. So this is safe by construction —

```bash
TID=$(sesh whoami) || { echo "identity unverified or unresolved; refusing"; exit 1; }
```

— and it has no `--id`, because naming a thread would make the answer trivially
"explicit". It is not routable either: a peer would read its **own** pane and environment
and answer confidently about a different machine, which is the failure mode, not a
limitation. `--json` adds `source`/`verified`/name/cwd; `--allow-unverified` downgrades it
to `info`'s tolerance. Refusals, each distinct:

| what happened | what it means |
|---|---|
| the env id is **contradicted** by your cwd | very likely another thread's id — do not sign or attach as it |
| the env id is **uncontradicted but unverified** | your cwd neither confirms nor denies it; absence of contradiction is not evidence |
| your **harness session** names a thread in an unrelated directory | either the harness is reporting other work's conversation, or you are not the agent you appear to be — refuse to act as it |
| **nothing resolved** | UNRESOLVED, *not* "you are not a thread" — read the lead the refusal prints (see below) |

`sesh info` is the **diagnostic** twin and deliberately keeps the opposite default: it
reports which source it used — a `source:` line, or `"source"` / `"verified"` in `--json`
— announces an env-derived answer on stderr, and still **exits 0** for it, because
refusing to describe a thread is the wrong move when you are diagnosing. It does refuse
one case: an env-derived id whose thread cwd is **unrelated** to where you are standing.
Pass `--id`, or `--allow-unverified` to proceed anyway (a pseudo-global — every verb that
infers accepts it). The refusal names the flag **that command** takes, which is not always
`--id`: `subscribe`/`unsubscribe` take `--from`, `ticket list --current` and `hooks test`
take `--thread`.

**Do not read an id out of `sesh info` to act as yourself.** That is what `whoami` is for;
`info` will hand you an unverified id and a zero exit, and the warning is on stderr where
a `2>/dev/null` swallows it.

> **Agents: a claude Bash call often has NO pane.** A tool call hosted by claude's
> machine-global daemon (any session showing background agents) runs with no `$TMUX_PANE`
> and a `$SESH_THREAD_ID` frozen from whichever thread started that daemon — so
> current-thread inference there is refused, correctly. **Name the thread explicitly in
> scripted work** (`sesh subscribe <child> --from <me>`), and **never discard stderr**:
> `sesh subscribe $ID >/dev/null 2>&1` in a loop is how three subscriptions silently
> failed to exist for an hour on 2026-08-27 while the loop printed "subscribed".

> ⚠️ **Before you do anything destructive to "yourself"** — compacting, sending, stopping,
> archiving — **require verified provenance.** This is not hypothetical: an agent with no
> pane asked `sesh info` who it was, was confidently told it was an unrelated thread, and
> its self-compact runner compacted that thread and injected a foreign handover prompt
> into it.
>
>
> ```bash
> TID=$(sesh whoami) || { echo "not verified — refusing to act on myself"; exit 1; }
> ```

**"I could not resolve my identity" is NOT "I have no identity" — do not conclude the
second from the first.** This distinction cost a real misattribution. On 2026-09-25 an agent
reported that it was not a sesh thread and should sign as a Claude Code job; on 2026-09-27
it corrected itself — it *was* thread `1a26989d`, with its pane, marker and cwd all
registered, while its `$SESH_THREAD_ID` named `adi-requests` in a different project. It was
reparented, so nothing in its environment could say so, and it found out only by capturing a
pane and recognising its own prose. The answer was in the daemon the whole time.

So when `whoami` refuses: **read the lead it prints.** A refusal now ends with what the
daemon knows about your directory — the one live thread registered there, if there is
exactly one. That is a lead, not an answer: confirm it with `sesh info <id>` and by capturing
its pane to see whether the conversation on screen is the one you are having. If it is you,
name that thread explicitly in what you were about to run.

**If you genuinely are not a thread, say so — never borrow an id.** A cron task or a
detached job with no live thread behind it should sign as what it actually is: the tool or
job and its working directory, e.g. `Claude Code job ef96bf74, cwd ~/dev/…/courses/finnish`.
Signing with an inherited `$SESH_THREAD_ID` instead produces a *resolvable* id pointing at
live, unrelated work — a reply addressed to a thread that never asked, and bookkeeping
attached to the wrong project.

Corroboration is evidence, not proof: an inherited id that happens to name a thread in the
*same* directory tree still resolves, which is precisely why `whoami` refuses it and `info`
does not.

### If you are a scheduled or headless worker

A thread created by `sesh schedule spawn` does not exist when the schedule is written, so
its prompt cannot name it. Ask instead — this works in a **headless** run (the daemon
verifies you are inside the turn it launched) and in a `--headed` one (the pane marker):

```bash
TID=$("$SESH_BIN" whoami) || exit 1
```

`$SESH_BIN` is the launching daemon's own binary, injected into the turn's environment
alongside `$SESH_THREAD_ID`. Prefer it to a bare `sesh`: from a login shell that may be a
wrapper function or an older install on the PATH, i.e. a different sesh than the one that
started you. And `$SESH_THREAD_ID` is still **not** the contract — it is inherited by
anything you detach, so `whoami` refuses it alone.

Two things that will still (correctly) refuse: a process the worker **detached** from its
own turn, and anything asking **after** the turn has ended — the identity does not outlive
the work it describes. Both cases want an explicit id, captured while the turn was live.

Outside a pane and outside a daemon-launched turn, an explicit `--id` is the only
certainty.

## Before running commands

**Read-only** (safe to run freely): `list`, `grid`, `info`, `whoami`, `status`, `pane`, `capture`,
`mesh`, `tail`, `transcript`, `subscriptions`, `peer list`, `daemon status`, `master
watchers`, `matrix`, `doctor`, `tmux current|info`, `cwd-label`, `meta get|list`, `hooks list`.

**Mutating** (think first): `new`, `stop`, `delete`, `resume`, `headful`, `send`,
`send-headless`, `rename`, `tag`, `reparent`, `archive`, `notify`, `meta set|unset`,
`adopt`, `subscribe`/`unsubscribe`, `delegate`, `backup`/`restore`/`copy`, `import`,
`ticket *`, `blob add|rm`, `tmux nav|send-text|stage-file|create-*|kill-session`,
`master up|down|ensure`, `peer add|remove`, `daemon start|stop|restart`,
`hooks enable|disable|test`.

`sesh tmux kill-session --target <name> [--machine <m>]` kills one work-server session by
exact name (routes cross-machine; a non-existent session is a loud error) — the mechanism
behind myrig's "kill empty sessions" cleanup.

Extra care: `delete` (drops a record; refuses a live thread unless `--force`, which
orphans the agent — `stop` first), `send`/`send-headless` (injects into a real agent's
conversation), `master down` (tears mycockpit down), `peer remove`, `import`.
