---
name: do-tickets
description: Use when you (an agent running inside a sesh thread) need to handle your assigned work tickets — check what tickets you've been given, read a ticket's prompt/instructions, or update a ticket's status (e.g. mark it done when finished). Explains the sesh ticket system and the exact `sesh ticket` commands for the find → read → do → report loop.
---

# do-tickets

A **ticket** is a unit of work in `sesh` (the multi-machine coding-agent session
manager). You are a **thread** — one agent conversation in a tmux pane. A ticket can be
**bound to a thread**, and when it is, that ticket is *assigned to that thread's agent*:
you. The ticket's **prompt** is your instructions. This skill is the loop:

> **find** my tickets → **read** the prompt → do the work → **report** (set status).

You drive this with the `sesh ticket` CLI. Everything is machine-readable (`--json`), and
the commands auto-route to wherever the ticket lives — you normally just operate on your
own thread's tickets and don't think about routing.

## The status lifecycle

A ticket moves through five statuses:

| status | meaning |
|---|---|
| `triage` | created; the prompt isn't final yet; not attached to a thread |
| `ready` | the prompt is final and deployable; still unattached |
| `active` | **attached to a thread** — i.e. assigned to an agent (you) and being worked |
| `done` | finished (terminal) — set this when you complete the work |
| `dropped` | abandoned / won't-do (terminal) |

So a ticket that is `active` and bound to *your* thread is **work assigned to you**. When
you finish it, set it to `done`.

`needs-input` is a derived signal (not a stored status): a ticket is "needs input" when
it is `active` and its thread is **headful and idle** — i.e. a live agent is waiting on
the human. It's "needs restart" when the bound thread is `headless and idle` (no runtime).

## 1. Find your tickets

The key command — it auto-detects the thread you're running in (from `$SESH_THREAD_ID`,
falling back to your tmux pane's `@sesh-thread-id` marker) and lists that thread's tickets:

```bash
sesh ticket list --current            # id  status  name  thread  (tab-separated)
sesh ticket list --current --json     # one JSON object per line (full records)
```

This is your self-check: *"what am I assigned?"* Use `--json` when you want to parse the
prompt or ids programmatically:

```bash
sesh ticket list --current --json | jq -r 'select(.status=="active") | "\(.id)\t\(.name)"'
```

To list a **specific** thread's tickets (not your own), pass its id instead:

```bash
sesh ticket list --thread <thread-id> [--json]
sesh ticket list                       # ALL tickets (no filter)
```

## 2. Read a ticket's prompt

The prompt is the actual instructions for the work. Get just the prompt, raw (no JSON, no
trailing newline — ideal to pipe or read directly):

```bash
sesh ticket get --id <ticket-id> --field prompt
```

`--field` accepts `id | name | prompt | status | thread | created`. For the whole record:

```bash
sesh ticket get --id <ticket-id> --json
```

(`--id` needs the full ticket UUID — get it from `ticket list`.)

## 3. Report your progress (change status)

When you finish the work, mark the ticket done:

```bash
sesh ticket set-status --id <ticket-id> --status done
```

Other transitions use the same command. **One rule:** moving a ticket to `active` requires
binding it to a thread, so pass `--thread`:

```bash
sesh ticket set-status --id <ticket-id> --status active --thread <thread-id>
sesh ticket set-status --id <ticket-id> --status ready      # no thread needed
sesh ticket set-status --id <ticket-id> --status dropped    # abandon it
```

## Other operations

```bash
sesh ticket create --name <name> [--prompt <text>]     # new ticket (starts in triage)
sesh ticket set --id <id> [--name <t>] [--prompt <t>]  # edit text fields (only the flags you pass)
sesh ticket send-prompt --id <id>                      # type the prompt into the bound thread's live pane
sesh ticket needs-input --id <id>                      # the derived needs-input / needs-restart view
sesh ticket delete --id <id>                           # remove the record (no thread/pane touched)
```

## A typical run

```bash
# What am I assigned?
sesh ticket list --current
#   bbfa98e0-…   active   fix-the-oauth-flow   7e108848-…

# Read the instructions
sesh ticket get --id bbfa98e0-6d38-4672-b655-941623481e4f --field prompt
#   the redirect drops the state param — fix it and add a test

# … do the work …

# Report done
sesh ticket set-status --id bbfa98e0-6d38-4672-b655-941623481e4f --status done
```

## How binding & routing work (you usually don't need this)

There is no global ticket store — a ticket lives on the **same machine as the thread it's
bound to** (or on the configured `SESH_TICKET_OWNER` if one is set). Every `sesh ticket`
command auto-routes there, so acting on your own thread's tickets "just works" locally.
You only hit a wall if you try to bind a ticket to a thread on a *different* machine than
the ticket — that's not supported (the thread is validated on its own daemon).

## The human side (for reference)

The person supervising you manages tickets visually, not via these commands: the `sesh tui`
**tickets view** (press `K` on a thread to list/edit/create its tickets) and the myrig
cockpit commands (`mt-/mmt-ticket-edit`, `mmt-ticket-browse`, etc.). As the agent, you use
the `sesh ticket` CLI above — that's your interface to the same tickets.
