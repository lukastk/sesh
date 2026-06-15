# sesh v2 — agent instructions

You are developing **sesh v2**: one Go binary + per-machine daemon that owns multi-machine coding-agent session management, tmux orchestration, and tickets.

- **What we are building:** `_dev/SPEC.md` (the design — read it first).
- **How we build and track it:** `_dev/PLAN.md` (the roadmap + the feature matrix + the testing framework).
- This file: **the rules you must follow.** They are not optional and they are not negotiable.

---

## The prime directive: the feature matrix is honest or it is worthless

The previous version of sesh failed in a specific, insidious way: features that *looked* implemented but silently did the wrong thing. `sesh new --machine X` returned success and set the machine field but **always spawned locally** — it only pretended to be remote. Codex liveness/headless detection returned plausible-but-wrong answers. These survived for months because nothing made them visibly false.

This project defends against that with a **feature matrix**: every conformant feature is registered, and the testing framework loudly expects a real test for each cell of that feature's row across `(local, remote) × (claude, codex, pi)`. See `_dev/PLAN.md` for the mechanism.

**The integrity of the matrix is enforced by these rules and by Lukas auditing it — not by clever framework code.** That means the burden is on you to be honest. The following are hard rules:

### Honesty rules (a cell may go green ONLY if all of these hold)

1. **Exercise the real thing. Never mock the thing under test.** The cardinal sin of this project is making a cell green by mocking away the behavior it is supposed to prove.
   - A **`remote`** cell must perform a *real ssh hop* into a real remote daemon/tmux. `ssh localhost` into a second daemon/socket on the same box is acceptable and honest (it drives the actual remote code path — it would have caught the `--machine X` bug). A mocked or stubbed ssh/transport is a **violation**.
   - An **agent** cell (`claude`/`codex`/`pi`) must spawn the *real agent binary* in a real tmux pane. Mocking the agent process is a **violation**.
2. **Assert the observable external effect, not internal state.** "Did a process with this thread id actually land on the remote host?" — not "did we set `machine=remote`?". For liveness, **kill the real process and assert the state flips to dead** — test both directions, because the codex bug was a one-directional check.
3. **Skips are allowed but never silent and never count as done.** An unimplemented cell is a `t.Skip("NOT IMPLEMENTED: …")` (renders yellow) — it must be queryable (`<matrix> skips`) and it never counts toward "done".
4. **`N/A` requires a justification string** that Lukas has signed off (e.g. "pi has no headless mode — by design"). You may not silently drop a cell from a feature's declared axes to avoid testing it.
5. **Done = the full matrix is green** with zero skips and zero unjustified N/A. "Done" is not a judgement call you make — it is the matrix all-green.

### Do not game the matrix

Do **not** weaken an assertion, shrink a feature's declared axes, stub-and-forget, or mock a dependency to turn a cell green. The grid is a *measurement*, not a goal. If you cannot honestly make a cell green, leave it `Skip`/red and say so — loudly, in your summary. A red matrix that tells the truth is infinitely more valuable than a green one that lies. Lukas runs an audit agent over the grid that checks exactly this; rigging will be found.

---

## Other hard rules

- **Loud errors over silent failures. No defensive fallbacks.** (This is a standing rule across all of Lukas's projects.) Any reachable-but-unimplemented code path must `panic`/return an explicit `"NOT IMPLEMENTED: <what>"` error — it must **never** degrade to a plausible-looking wrong behavior (that is the exact `--machine X` failure). Do not add fallback values that mask bugs. If something returns an unexpected empty/nil, let it fail loudly.
- **Both unit and end-to-end tests are required.** Unit tests cover internal logic (fast, may live *outside* the matrix). The matrix cells are primarily **e2e** (real agent, real tmux, real ssh). Neither substitutes for the other.
- **Tests may live outside the matrix.** Not every test maps to a cell — unit tests, regression tests, and helper tests are free. The per-cell expectation applies *only* to features registered as conformant in the matrix.
- **`sesh` is mechanism, not UX.** Keep the CLI explicit (no magic defaults) and machine-readable (`--json`, stable/versioned schema). Ergonomics belong in `myrig` shell wrappers, not here. Do not grow a shell-glue layer inside this repo.
- **If you find yourself implementing a hack to get something to work, stop.** It usually means a bug to fix or a design decision for Lukas. Surface it in your summary rather than papering over it.
- **The CLI skill must stay in sync.** `skills/sesh-cli/SKILL.md` is the user-facing guide to the CLI/TUI. Any change to the CLI surface — a new or removed command, a renamed/added flag, changed semantics, new TUI keys or columns, new env vars — MUST be accompanied by an update to that skill file in the same change. A CLI change without a corresponding skill update is incomplete. (The skill documents *using* sesh, not developing it.) There is also an agent-facing `skills/do-tickets/SKILL.md` (the ticket find→read→report loop) — a change to the `sesh ticket` surface must keep it accurate too. The same in-repo sync applies to `--help`: `cmd/sesh/help.go` holds the registry (summary/usage/examples per command) and `cmd/sesh/help_flags.go` holds the per-flag explanations (`flagDocs`). Two meta-tests in `help_test.go` enforce both — a help entry per dispatched command, and a `flagDoc` for **every** flag in a command's usage line (no orphans/dups) — so a new or renamed flag can't land undocumented. The `tui --columns` value list is rendered programmatically from `tui.ValidColumnNames()`, so column additions need no manual help edit.
- **Commit messages are prompts.** Write each commit message so another agent could recreate the work from it.

---

## Workflow

1. Read `_dev/SPEC.md` and `_dev/PLAN.md` in full before writing code.
2. Build the feature registry + matrix harness *first* (it is the spine — see `_dev/PLAN.md` Phase 0), so every subsequent feature lands as a visible burndown from yellow to green.
3. Implement features per the PLAN's sequencing. Each feature: register it → write its matrix tests (they start as `Skip`/red) → implement until green honestly.
4. Keep the rendered matrix current; when you stop, report the grid state (greens / reds / skips) truthfully in your summary.

---

## Test environment notes

- **Lukas runs the LIVE old sesh on these machines.** The conformance suite MUST never
  touch it: every test isolates `SESH_HOME`, `SESH_TMUX_SOCKET`, `SESH_MASTER_SOCKET`,
  and `SESH_CODEX_HOME`, and strips any inherited `SESH_*` from the test process env
  (`sandboxEnv` in the harness). Never leave a socket/home at its default in a test.

- **Real cross-host test (`TestRealCrossHost`)** validates genuine multi-machine spawn
  over a real network ssh hop (the one thing the `ssh localhost` matrix cells cannot
  stand in for). Pairing: `mymain ↔ macbook` (from `$MYRIG_MACHINES`). It self-gates and
  **skips with a warning** when it can't run. **Prerequisite (manual, by design — the
  test does NOT ship the binary):**
  1. Install the v2 binary on BOTH paired machines at `~/.local/bin/sesh-v2`, built for
     that machine's GOOS/GOARCH (e.g. from mymain: `GOOS=darwin GOARCH=arm64 go build -o
     /tmp/sesh-v2 ./cmd/sesh && scp /tmp/sesh-v2 lukas@macbook:.local/bin/sesh-v2`).
     **Re-install after any wire/API-schema change** — the local side uses the freshly
     built binary and the partner uses its installed one; they must be compatible.
  2. `$MYRIG_MACHINES` is a zsh assoc array (NOT exported), so run the test with it
     exported: `MYRIG_MACHINES="$MYRIG_MACHINES" go test ./internal/conformance -run
     TestRealCrossHost -v`. (Self-detection uses `$MYRIG_TARGETS`, which IS exported.)
  `peers.Peer` now has a `Port` field, so non-22 partners are supported.

- **Real cross-host HTTP test (`TestRealCrossHostHTTP`)** is the symmetric real-network
  proof for the **http transport** (the `127.0.0.1` `.http` cells exercise the code but
  never cross a real network). Same wiring/pairing/prereq as `TestRealCrossHost`, but it
  starts the partner's daemon with its TCP API on its tailscale interface, registers it
  as an **http peer with a deliberately broken ssh dest**, and asserts routing + fan-out
  + sync all cross the real network over HTTP (a silent ssh attempt would fail). Run:
  `MYRIG_MACHINES="$MYRIG_MACHINES" go test ./internal/conformance -run
  TestRealCrossHostHTTP -v`. It skips loudly if the partner binary is stale (no
  `--api-addr`) — re-install after schema changes.
