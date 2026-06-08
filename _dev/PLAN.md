# sesh v2 — development plan

*The "how we build and track it" document. Pairs with `_dev/SPEC.md` (what) and `../AGENTS.md` (rules). Read all three before coding.*

---

## Guiding approach

Build the **tracking spine first**, then fill it. The spine is a feature registry + a matrix test harness that makes every unbuilt or fake feature *visibly red/yellow*. Every feature thereafter is a burndown from yellow (`Skip: NOT IMPLEMENTED`) to green (honest passing test). This is the antidote to v1's silent feature-gaps — see `../AGENTS.md` for why this matters and the honesty rules that govern it.

The matrix axes are **`(local, remote) × (claude, codex, pi)`**. Not every feature spans every axis; each registered feature *declares* its applicable axes (and any `N/A` with a justification).

---

## Phase 0 — the tracking spine (build this first)

A small Go test harness, roughly:

- **Feature registry.** A registered feature declares: `id`, `description`, applicable `agents` (subset of `{claude,codex,pi}`), applicable `localities` (subset of `{local,remote}`), and per-cell `N/A{reason}` where a combination genuinely does not apply.
- **Expected-cells derivation.** `expected = feature × applicable agents × applicable localities` minus justified `N/A`.
- **Test binding.** Each matrix test binds itself to a cell, e.g. `matrix.Test(t, "thread.new", Codex, Remote)`.
- **`TestMatrixComplete`** — a meta-test that fails the build if any expected cell has **no** bound test. (Missing test = red build, not a blank.)
- **Skip registry + query.** A skipped cell (`t.Skip("NOT IMPLEMENTED: …")`) is recorded and queryable (a `skips` reporter / CLI subcommand) and printed as a loud warning in the grid. Skips never count as green.
- **Grid renderer.** Renders the matrix to the terminal (and optionally an artifact) after a run: per cell `pass` ✓ / `fail` ✗ / `skip` ⚠ / `N/A` ·. "All green" is a single computed gate.
- **Two run targets, honestly labeled:**
  - *code-verified* (default CI): real agent + real tmux + `ssh localhost` for remote. Fast-ish, deterministic, sufficient to gate "done".
  - *mesh-verified* (on-demand/nightly): the same matrix pointed at real machines (real macstudio/termux/etc.). Reported as its own column; catches environment drift.

Keep the harness **dumb**: it enforces *completeness* (every cell has a test) and *visibility* (the grid), not *honesty*. Honesty is enforced by `../AGENTS.md` + Lukas's audit agent. Do not build mock-policing into the harness.

Deliverable of Phase 0: `TestMatrixComplete` passing with the full feature set registered and **every cell a `Skip`** (an all-yellow grid). That yellow grid is the project's to-do list.

---

## Feature set to register (initial; extend as the spec is realized)

Axis legend: **L**ocal/**R**emote; agents **c**laude/**co**dex/**p**i. "agent-agnostic" features have no agent axis.

### tmux layer (agent-agnostic; L/R where noted)
| Feature id | Axes | Notes |
|---|---|---|
| `tmux.current` | L | resolve calling terminal's locator + owning thread |
| `tmux.info` | L, R | JSONL walk of sessions/windows/panes across machines; `--machine`/`--session` |
| `tmux.create-session` | L, R | |
| `tmux.create-pane` | L, R | |
| `tmux.nav` | R (L is trivial) | **the nav primitive**: outer switch + inner switch-client + detached-pane kick |
| `tmux.stage-file` | L, R | copy local file to machine, return staged remote path |
| `tmux.send-text` | L, R | paste/send text into a pane |

### thread layer (full `(L,R) × (c,co,pi)` unless N/A)
| Feature id | Axes | Notes |
|---|---|---|
| `thread.new.headed` | L,R × c,co,pi | codex can't pre-assign a session id — handle per spec |
| `thread.new.headless` | L,R × c,co,pi | **pi N/A if pi has no headless mode (justify)** |
| `thread.kill` | L,R × c,co,pi | |
| `thread.send.headful` | L,R × c,co,pi | into the live pane |
| `thread.send.headless` | L,R × c,co,pi | as a turn |
| `thread.list` | L,R × (agent-agnostic) | mesh-replicated cross-machine list |
| `thread.resolve-pane` | L,R × c,co,pi | runtime pane resolution via `@sesh-thread-id` marker |
| `thread.runtime-state` | L,R × c,co,pi | **working/waiting/dead/detached — the v1 codex-detection bug lived here; test all transitions, both directions** |

### ticket layer (agent-agnostic unless noted)
| Feature id | Axes | Notes |
|---|---|---|
| `ticket.create` | L, R | |
| `ticket.list-by-thread` | L, R | what an agent is assigned |
| `ticket.send-prompt` | L,R × c,co,pi | deliver prompt to bound thread (touches agent send) |
| `ticket.set-status` | L, R | incl. agent-driven `done` |
| `ticket.needs-input` | L, R | **derived view** = `active && thread waiting`; test the derivation, incl. the `dead`≠`needs-input` distinction |

### daemon / API
| Feature id | Axes | Notes |
|---|---|---|
| `daemon.lifecycle` | L, R | start/stop/status |
| `daemon.mesh-read` | R | cross-machine read via peer mesh |
| `ticket.ownership` | R | single canonical owner; writes route to owner; read-cache elsewhere |
| `api.http-json` | L, R | client-facing HTTP+JSON surface (for CLI/TUI/Obsidian plugin) |

> The `remote` cells for **`thread.*` × `codex`** and **`thread.new × remote` (any agent)** are the two regions where v1 silently broke. Treat them as the highest-risk cells; their tests must be unmistakably real.

---

## Implementation sequencing

1. **Phase 0** — tracking spine (above). All-yellow grid.
2. **Phase 1 — daemon + storage skeleton.** SQLite/WAL, single-writer, local socket, `daemon.lifecycle`. No mesh yet.
3. **Phase 2 — tmux layer.** `tmux.current`, `tmux.info`, create-session/pane, `tmux.send-text`, `tmux.stage-file`. Local cells green first.
4. **Phase 3 — thread layer, local.** new (headed/headless), kill, send, list, resolve-pane, runtime-state — for all three agents, **local** cells green. This is where the real-agent e2e tests pay off; get codex runtime-state honest here.
5. **Phase 4 — mesh + remote.** Peer mesh over ssh; turn on all `remote` cells (using `ssh localhost` in CI). `tmux.nav` (the hard primitive) lands here with `internal/testtmux`-style coverage.
6. **Phase 5 — tickets.** create/list/send-prompt/set-status/needs-input, then `ticket.ownership` (canonical node). This is what retires `vaulthost.py`.
7. **Phase 6 — HTTP+JSON API + TUI.** `api.http-json`; port/keep the TUI in Go.
8. **Throughout:** keep the grid current; never mark done until all-green honestly.

---

## Notes & open items (from the design discussion)

- **Canonical ticket owner machine is undecided** (`_dev/SPEC.md` §8.1) — likely the always-on `hetzner-box`/a `myserver`. Phase 5 needs this answered; until then, build `ticket.ownership` against a configurable owner and test via `ssh localhost`.
- `--agent codex` cannot pre-assign a UUID (true in v1: start + `register`). Preserve/clean up this path; don't fake it.
- Keep the CLI explicit and `--json`; do not grow shell glue in this repo (it belongs in myrig).
- Reference the consolidation review (`REPORT.md` in the mysetup-review box) for the rationale behind each deletion in `_dev/SPEC.md` §7.
