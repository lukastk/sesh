---
name: do-tickets
description: Use when you (an agent in a sesh thread) need to handle assigned work tickets — find your tickets, read a ticket's prompt/instructions, or update its status (e.g. mark it done). Covers the `sesh ticket` find → read → report loop.
---

# do-tickets

A **ticket** is a unit of work in `sesh`. When one is bound to your thread it's assigned
to you; its **prompt** is your instructions. Loop: **find → read → report**.

## Do not create tickets

**Never create a ticket unless Lukas explicitly asked for one.** Creating none is the
default. A ticket is *his* tracking surface: each one becomes a note in his vault that
he consolidates by hand, so an unasked-for ticket is work you have handed him, not work
you have recorded. This holds however useful the ticket feels — "this deserves
follow-up", a plan you wrote yourself, and a finding you want remembered are **not**
requests. Instead:

- **Steering a sub-thread?** Use `sesh thread send` / `sesh thread send-headless`, or
  `sesh delegate` for a one-shot worker. Never mint a ticket per instruction.
- **Recording something durable?** The commit message, the repo's `AGENTS.local.md`, or
  a `_dev/` note — wherever the work already lives.
- **Reporting to your parent?** Report in your turn; that is what reaches them.
- **Sure one is warranted?** Say so in a sentence and let Lukas decide.

`ticket create` is for when he asked. Tickets **assigned to you** are unaffected — read
and report them exactly as below.

Statuses: `triage` (draft) → `ready` (final, unattached) → `active` (assigned to a
thread) → `done` / `dropped` (terminal). An `active` ticket on your thread = your work;
mark it `done` when finished.

```bash
# FIND — your thread's tickets (auto-detected via the pane marker / $SESH_THREAD_ID)
sesh ticket list --current                 # id  status  name  thread
sesh ticket list --current --json          # one JSON object per line

# READ — the instructions (raw, no trailing newline)
sesh ticket get --id <id> --field prompt   # --field: id|name|prompt|status|thread|created|closed|notes
sesh ticket get --id <id> --json           # whole record

# REPORT — close AND record what you did, in one call (ALWAYS leave a note on close)
sesh ticket set-status --id <id> --status done --note "what you did; closed by <commit-sha>"
sesh ticket set-status --id <id> --status active --thread <thread-id>
```

**Always leave a note when you close a ticket.** A ticket has a free-text **`notes`**
field; when you mark a ticket `done` (or `dropped`), append a short note explaining what
you did and — if there was a commit — which commit closed it. **Write notes in markdown**
(headings, lists, fenced code, links) — they are rendered as markdown in the Obsidian
ticket-note panel where Lukas reads them during consolidation. `--note` on `set-status`
appends as part of the same call (each `--note` appends, blank-line separated). You can
also append/replace notes independently:

```bash
sesh ticket set --id <id> --append-note "investigated; root cause is X"   # append
sesh ticket set --id <id> --notes "<full text>"                          # REPLACE
sesh ticket get --id <id> --field notes                                  # read them back
```

`--id` takes the full ticket UUID (from `ticket list`). Commands auto-route to wherever
the ticket lives, so acting on your own thread's tickets just works.

A prompt may reference files/images as **`@blob(<hex>)`** tokens. When the prompt is
*delivered* to you (`send-prompt`) these are already expanded to real paths you can read.
If you read the prompt **raw** with `ticket get --field prompt`, the tokens are NOT
expanded — pipe through `sesh blob expand` to resolve them to paths:

```bash
sesh ticket get --id <id> --field prompt | sesh blob expand   # @blob(..) → real file paths
```

Other ops: `ticket create --name <n> [--prompt <t>]`,
`ticket set --id <id> [--name|--prompt|--notes|--append-note]`,
`ticket send-prompt --id <id>` (deliver to the bound thread: literal Pi RPC steering, guarded terminal paste for other headed agents),
`ticket delete --id <id>`. (`sesh ticket --help` for the rest.)

Pi delivery preserves an unsent editor draft and does not interpret leading slash
commands. Missing/broken RPC is a loud failure, never a terminal-paste fallback.
Use `sesh thread command` deliberately for Pi commands such as `/compact`, not a
ticket prompt. Submission acknowledgement is not completion of the agent's turn.
