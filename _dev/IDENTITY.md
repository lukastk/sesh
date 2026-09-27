# IDENTITY — how a process finds out which thread it is

*Design record. Written 2026-09-27 alongside schema 50 (`thread.turn-identity`), and
retro-documenting the model H82/H92/H95/H112 built incrementally. Read this before
touching current-thread inference, `sesh whoami`, or anything that injects an
identity into a spawned process.*

## 1. The question, and why it is hard

Almost every sesh verb accepts an optional `--id`. Omitted, it infers "the current
thread" — the convenience that lets an agent run `sesh ticket list --current` or
attach itself to a vault note without looking its own uuid up.

The hard part is not resolving an id. It is knowing whether the id you resolved is
**yours**. Three real incidents, all the same shape:

| when | what happened |
|---|---|
| 2026-07-10 | `thread new` silently parented a thread to a stranger; invisible for ten hours (ticket 6ea1f6eb) |
| 2026-08-25 | an agent asked `sesh info` who it was, was told it was an unrelated thread, and its self-compact runner **compacted that thread** and injected a foreign handover prompt |
| 2026-08-27 | a supervisor's three `sesh subscribe` calls were refused; the loop discarded stderr and printed "subscribed"; nothing was delivered for an hour |
| 2026-09-25 | a claude background job in a mosaic-v3 course box reported that its inherited id named `adi-requests`, a live thread in a different box — and that the standard advice ("sign with `$SESH_THREAD_ID`") produces that misattribution *silently* |
| 2026-09-27 | **the same reporter, correcting itself:** it was not identity-less at all. It WAS thread `1a26989d` (pane `%1745`, marker and cwd both registered) while its `$SESH_THREAD_ID` named `adi-requests`. It found out by accident — a decision record assigned work to `1a26989d`, it captured that "other agent's" pane to avoid a collision, and read its own prose. §6 |

Every one is `$SESH_THREAD_ID`. The variable is injected at launch, frozen into
the process environment, and inherited by every descendant forever — including
descendants that are no longer, or never were, that thread. A claude background
job is hosted by a machine-global `claude daemon run` which froze whichever pane's
environment happened to start it, so its shells carry a **valid** uuid naming
**real, live, unrelated** work. From sesh's side it looks perfect.

The 2026-09-25 report put the principle best: **the resolvable-but-wrong case is
worse than the unresolvable one.** That job had signed a message with its Claude
Code job id, which failed loudly and was caught within the hour. Had it signed
with `$SESH_THREAD_ID` nothing would have questioned it.

## 2. The rule

> An identity is VERIFIED when the asking process can be shown to be INSIDE
> something an authority created, checked with that authority at the moment of
> asking. Anything read out of the process's own environment is a hint.

That is the whole model. "Inside something an authority created" is what a frozen
environment can never be: you cannot inherit your way into a container you are not
in. Everything below is an application of it.

## 3. The sources

Precedence, highest first (`cmd/sesh/current.go`):

| source | verified | what it is |
|---|---|---|
| `explicit` | yes | the caller passed an id or prefix. Nothing was guessed. |
| `pane` | yes | the `@sesh-thread-id` marker on the tmux pane this process runs in, read live from tmux. Re-stamped on adopt/reparent, so it tracks live ownership. |
| `turn` | yes | the local daemon confirming this process sits inside the tree it created for a thread's headless turn (schema 50, §5). |
| `harness` | yes, conditionally | the agent harness's OWN session id (`$CLAUDE_CODE_SESSION_ID`) matched against the `agent_session_id` the daemon recorded for exactly one thread — accepted only with cwd corroboration AND a live pane (§6). |
| `env` | **no** | `$SESH_THREAD_ID`, with none of the above to check it against. |

`pane` is checked before `turn` for two reasons: a turn's environment has
`$TMUX`/`$TMUX_PANE` stripped (§5), so the two can never both answer; and asking
the daemon second means the pane agent — the overwhelmingly common caller — pays
nothing for a question that is not about it.

## 4. What is done with an unverified answer

Two mechanisms, and they are deliberately different because the verbs that reach
them want opposite things.

**Corroboration (H92).** An env-derived id is compared against the caller's
working directory, and a POSITIVE contradiction — the named thread's cwd and the
caller's cwd are unrelated trees — is a refusal. It follows the
**one-directional evidence rule**: missing cwd, an unreadable path, a relative
path, and containment in *either* direction all read as "no contradiction" and
still resolve. A false positive costs one loud, actionable error; a false negative
costs someone else's session.

**The gate vs the diagnostic (H112).** `sesh info` is a DIAGNOSTIC: on an
uncontradicted env id it reports `source: env`, `verified: false`, warns on
stderr, and **exits 0** — because refusing to describe a thread is exactly wrong
when you are diagnosing. That leaves the safe reading optional
(`jq 'select(.source == "pane")'`), and *an optional ritual is one nobody
performs*: the skills that were supposed to carry it did not, three times.

So `sesh whoami` is a second verb answering a different question — "may I act as
this thread?" — with the **exit code**. stdout is the bare uuid iff the identity
is verified, else stdout is empty and stderr says which refusal it is.
`TID=$(sesh whoami) || exit 1` is safe by construction, which the jq form never
was. `--allow-unverified` downgrades the gate for callers that genuinely want
`info`'s tolerance.

The residual `info` exits 0 on, and `whoami` does not: an env id the cwd neither
contradicts nor confirms. **Absence of contradiction is not evidence.**

## 5. `turn` — the daemon-launched worker (schema 50)

### The gap

A headless turn — `sesh thread send-headless`, and therefore every
`sesh schedule spawn --headless` run — is a process the daemon starts itself:
`$SHELL -c '<agent> --print …'`, with `$SESH_THREAD_ID` injected. It has no pane.
So the only thing naming its thread was the variable the gate refuses, and the one
process on the machine whose identity the daemon knew **for certain** was the only
one that could not prove it. Measured from a real pi probe on mymain, 2026-09-26:
`whoami --json` exit 1, `info --json` `source=env verified=false`.

That blocked real work: a scheduled worker that must publish under its own
authoritative uuid had no supported way to learn it.

### The mechanism

The daemon records the ROOT PID of every in-flight turn (`Daemon.turnPID`, same
lock and the same lifetime as `hlInFlight`) and serves
`GET /v1/threads/turn-identity?pid=N`: it walks UP the process tree from `N` and
answers with the thread of the turn whose root is an ancestor
(`internal/procs`, `turnOwnerOf`).

### Why ancestry and NOT a token

The obvious alternative is a per-turn secret in the turn's environment, checked
against the daemon. It was rejected, and the reason is the point of the whole
document: **a token in the environment is inherited by exactly the detached and
background processes this model distrusts.** Every incident in §1 is a frozen
environment outliving the context that produced it; shipping a new frozen
environment variable as the fix would reproduce the bug class with a fresh name,
and a token that leaked into a log or a note would be worse still.

Ancestry has nothing to inherit. The only input is a pid, the answer is recomputed
from the live tree, and a reparented process — ancestry reaching pid 1, which is
the exact shape of the 2026-09-25 report — is refused by construction. It is also
the precise analogue of the pane marker: *you are demonstrably inside the thing the
authority made.*

### The rules that make it honest

- **Only turns in flight right now.** The pid is dropped in the same critical
  section that clears the in-flight flag. An identity that outlived its turn would
  be the stale-id bug in a new costume.
- **An unreadable tree is a REFUSAL carrying the read error**, never a quiet "not a
  descendant". The difference between "provably not yours" and "could not tell" is
  the whole value of the gate.
- **Two in-flight turns claiming one pid is refused, not guessed**, naming both.
  It is impossible by construction (the daemon starts each turn as its own child,
  so one turn's tree cannot contain another's root) — which is why it must be loud
  if it ever happens: the assumption, not the caller, would be wrong.
- **Asked on the LOCAL socket only**, explicitly, not through `daemonClient` —
  which honours `$SESH_REMOTE` and would have a *peer* answer about our pid from
  its own process table. That is the confident-but-wrong shape itself, so the one
  question that is meaningless remotely is asked locally by construction rather
  than by convention. (`whoami` is likewise excluded from `--machine` routing and
  refuses the flag with that reason.)
- **A daemon that cannot answer produces a NOTE, not a downgrade.** A pre-50
  daemon 404s the route; the worker is still refused (fails CLOSED), but stderr
  says the identity could not be *checked* rather than implying it was checked and
  rejected.

### Two env changes that came with it

`agents.TurnEnv` is now the single builder of a headless turn's environment, for
all three agents, above the per-agent switch — a carrier added to pi's branch and
forgotten in codex's would give a worker an identity on two agents and none on the
third.

- **`$TMUX`/`$TMUX_PANE` are stripped.** A headless turn has no pane, but it
  inherits the DAEMON's environment, and inference reads exactly those two
  variables to find the marker it treats as ground truth. A daemon started from
  inside a pane would hand every worker a live reference to a stranger's pane —
  a mis-identification through the *most* trusted source in the model. Every
  supervised daemon on the fleet was checked and carries neither, so this removes
  nothing in production; the property should not depend on how the daemon happened
  to be started.
- **`$SESH_BIN` is added** (panes have had it since schema 43). A worker that wants
  to call sesh should not have to hope PATH resolves something compatible: from a
  login shell `sesh` may be myrig's wrapper FUNCTION, which re-pins `SESH_HOME`,
  or an older install from a profile directory. A worker asking who it is should
  not be guessing which sesh it is asking.

### The contract for a worker

```bash
TID=$("$SESH_BIN" whoami) || exit 1
```

Works in a headless run (source `turn`) and a `--headed` one (source `pane`).
Refused, correctly, for a process the worker DETACHED from its own turn, and for
anything asking after the turn ended. Both want an explicit id captured while the
turn was live.

## 6. `harness` — the reparented agent (2026-09-27)

### The correction that produced it

The 2026-09-25 reporter came back and withdrew its own conclusion, and the
withdrawal is more useful than the original report. It had written that an agent
in its position "has no valid sesh identity at all". False: it was thread
`1a26989d`, *mosaic-finnish*, headful on mymain, pane `%1745`, cwd
`…/mosaic-v3/courses/finnish` — all verified here against the live daemon. Its
`$SESH_THREAD_ID` said `c194478c` (*adi-requests*, a different project).

So the variable is not merely absent-or-stale for a detached process: **it can be
WRONG while a correct answer sits in the daemon the whole time.** Nothing about
that situation was unknowable. The tooling just had no way to be asked.

That also inverts the original report's suggestion (2), "unset the variable for
detached jobs": doing so would have left it with nothing while the daemon knew.

### Why the pane marker and turn ancestry both miss it

Its process ancestry was `zsh <- claude <- claude <- systemd` — reparented. So:

- the pane marker is unreachable: the marker is found via `$TMUX_PANE`, and this
  process's environment is the frozen one from another pane;
- §5's turn ancestry is unreachable *by construction*: the pane records pid
  1838169 and this process does not descend from it. **A whoami that walks parents
  cannot serve a reparented agent, and that is a real limit of §5, not a bug in
  it** — §5 answers for a process the daemon started, and this one it did not.

### Why NOT cwd matching, which is what the report asked for

The report proposed matching cwd (plus pid) against the thread table. Measured on
the real fleet, 2026-09-27, across 2,311 threads:

| | |
|---|---|
| threads whose cwd is shared with ≥1 other thread | **937 (41 %)** |
| distinct cwds / cwds shared by >1 thread | 1,480 / 113 |
| worst directory | **174 threads** (`…__mosaic-v2`) |
| `~/mysetup/sesh` | 38 threads |
| the reporter's own directory | 3 (2 archived, 1 live) |

So a cwd match is a coin flip in the common case and would have worked for the
reporter only by luck of that particular directory. **It is a LEAD, never an
identity** — see §7.

### What is used instead

`$CLAUDE_CODE_SESSION_ID`, which the running harness writes for the conversation
it is actually serving, matched against the `agent_session_id` the daemon already
recorded — sesh spawns claude with `--session-id`, so the mapping is the daemon's
own (verified live: the reporter's pane runs `claude --session-id 07998bab…` and
the thread record holds exactly that). No API change, no new endpoint: the id is
already in the thread list.

**The bar is higher than for a pane marker, and deliberately so.** The marker is
read FROM the container the process is in; this is a CLAIM the process presents.
If a harness ever froze its session var the way `$SESH_THREAD_ID` gets frozen, the
claim would name a real thread belonging to unrelated work and look perfect. So it
is accepted only when two independent things agree with it:

1. **the calling directory does not contradict the thread's cwd** — mandatory
   here, optional for `$SESH_THREAD_ID`, because here it is the entire warrant.
   This is what makes the mechanism **fail closed** on the frozen case: a claim
   from another project is refused with its own message (`harnessMismatchError`);
2. **the thread still has a live pane** — a claim naming a conversation that is
   no longer running certifies nothing. An unreachable liveness probe reads as
   NOT live; it must never read as a pass.

Two or more threads on one session id is impossible (the daemon claims a session
per thread) and is therefore **refused naming both**, not resolved. An archived
thread is not an identity.

Claude only, and not dressed up as generic: no equivalent variable is known for
codex or pi, and their tool calls are not detached from their panes the way a
claude background job is, so `$TMUX_PANE` already answers for them. Adding a
harness is one entry in `harnessSessionEnvVars`.

**Not built, recorded as the available strengthening:** `$CLAUDE_PID` equals the
pane's `pane_pid` for a pane agent (verified), so a claim could additionally be
checked against the live pane table. It is not required because a reparented
agent's `CLAUDE_PID` may be a child claude rather than the pane's, and requiring
it would refuse the very case this exists for.

## 7. Refusals carry a LEAD

The 2026-09-27 reporter's real complaint was not that the gate refused it — the
gate was right — but that the refusal was a dead end: *"nothing about my situation
was unknowable, I just could not get it from the environment and did not think to
ask the daemon."*

So every whoami refusal now appends what the daemon DOES know about the caller:
when exactly ONE live, non-archived thread is registered in the caller's **exact**
directory, it is named, with `sesh info <id>` and "capture its pane" as the way to
confirm. More than one and the count is given and **none** is named, because
several candidates is not a lead. Exact directory only, never containment, or a
174-thread box arrives as "candidates".

The wording is load-bearing: `LEAD, NOT your identity`. A lead that reads like an
answer is worse than no lead.

Two related wordings were corrected at the same time, because they caused the
wrong conclusion rather than merely failing to prevent it:

- whoami's no-identity refusal said "this process has **NO sesh thread
  identity**". It now says resolution FAILED — `could not establish a verified
  identity` — and states explicitly that this means UNRESOLVED, not "you are not a
  thread", naming the reparented case where a real thread looks exactly like this.
  **sesh can know that it could not resolve an identity; it cannot know that the
  caller has none, and it must not say so.**
- the sesh-cli skill's "if you have no sesh identity, say so" paragraph now leads
  with the same distinction.

## 8. What is NOT claimed

- **Not a security boundary.** The pid in the request is self-reported, and anyone
  who can reach the daemon socket can already do anything (the API is
  RCE-equivalent behind one token — H73). This defends against *accidental*
  misattribution, which never lies about its pid.
- **A process genuinely inside a live turn's tree can claim that thread.** That is
  correct rather than a residual: it *is* that thread's work.
- **Corroboration is evidence, not proof.** An inherited id naming a thread rooted
  in the same tree still resolves as `env`. Outside a pane, a turn and a
  corroborated harness session, an explicit `--id` remains the only certainty.
- **`harness` is the weakest of the verified sources** and is the one to re-read
  first if an incident ever comes from this area: it rests on a claim plus two
  corroborations, not on a container the caller cannot leave. `--json` reports the
  source, so a caller with a higher bar can require `pane`.
- **A cwd match is never an identity**, at any count. §6 has the numbers.
- **The registry's removal at turn end is not asserted end-to-end.** Every vantage
  point that survives the turn is outside the turn's tree, and such a process is
  refused before and after — so an assertion from there would pass without the
  removal (the vacuous-negative trap). It rests on the shared critical section
  (whose other half `thread.send.headless` proves via the busy→idle edge) and on
  the unit test that no live entry means refusal.

## 9. Where the code is

| file | what |
|---|---|
| `cmd/sesh/current.go` | the precedence, the provenance model, corroboration, the injectable truth table |
| `cmd/sesh/whoami.go` | the gate: `whoamiGate` (pure), the refusal texts, and `whoamiLead` |
| `internal/procs` | `IsAncestor` — /proc on Linux, `ps` elsewhere |
| `internal/daemon/headless.go` | the turn registry, `turnOwnerOf`, the endpoint |
| `internal/agents/headless.go` | `TurnSpec`, `TurnEnv`, the pid hook |
| `internal/conformance/whoami_test.go` | `thread.whoami` — the gate against a real pane |
| `internal/conformance/turnidentity_test.go` | `thread.turn-identity` — a real scheduled headless worker, per agent |
