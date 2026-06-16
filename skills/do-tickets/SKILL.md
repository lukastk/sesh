---
name: do-tickets
description: Use when you (an agent in a sesh thread) need to handle assigned work tickets — find your tickets, read a ticket's prompt/instructions, or update its status (e.g. mark it done). Covers the `sesh ticket` find → read → report loop.
---

# do-tickets

A **ticket** is a unit of work in `sesh`. When one is bound to your thread it's assigned
to you; its **prompt** is your instructions. Loop: **find → read → report**.

Statuses: `triage` (draft) → `ready` (final, unattached) → `active` (assigned to a
thread) → `done` / `dropped` (terminal). An `active` ticket on your thread = your work;
mark it `done` when finished.

```bash
# FIND — your thread's tickets (auto-detected via $SESH_THREAD_ID / pane marker)
sesh ticket list --current                 # id  status  name  thread
sesh ticket list --current --json          # one JSON object per line

# READ — the instructions (raw, no trailing newline)
sesh ticket get --id <id> --field prompt   # --field: id|name|prompt|status|thread|created
sesh ticket get --id <id> --json           # whole record

# REPORT — set status (moving to `active` needs --thread)
sesh ticket set-status --id <id> --status done
sesh ticket set-status --id <id> --status active --thread <thread-id>
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

Other ops: `ticket create --name <n> [--prompt <t>]`, `ticket set --id <id> [--name|--prompt]`,
`ticket send-prompt --id <id>` (type the prompt into the bound thread's pane),
`ticket delete --id <id>`. (`sesh ticket --help` for the rest.)
