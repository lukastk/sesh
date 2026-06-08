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
- **Commit messages are prompts.** Write each commit message so another agent could recreate the work from it.

---

## Workflow

1. Read `_dev/SPEC.md` and `_dev/PLAN.md` in full before writing code.
2. Build the feature registry + matrix harness *first* (it is the spine — see `_dev/PLAN.md` Phase 0), so every subsequent feature lands as a visible burndown from yellow to green.
3. Implement features per the PLAN's sequencing. Each feature: register it → write its matrix tests (they start as `Skip`/red) → implement until green honestly.
4. Keep the rendered matrix current; when you stop, report the grid state (greens / reds / skips) truthfully in your summary.
