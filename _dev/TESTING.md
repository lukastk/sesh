# Testing and conformance reference

Read when changing tests or running conformance. [AGENTS.md](../AGENTS.md)
remains the operating guide; [PLAN.md](PLAN.md) describes the registry and harness.
The spine is shipped: its initial build phases are history, not pending work.

## The prime directive: the feature matrix is honest or it is worthless

The previous version of sesh failed in a specific, insidious way: features that *looked* implemented but silently did the wrong thing. `sesh new --machine X` returned success and set the machine field but **always spawned locally** — it only pretended to be remote. Codex liveness/headless detection returned plausible-but-wrong answers. These survived for months because nothing made them visibly false.

This project defends against that with a **feature matrix**: every conformant feature is registered, and the testing framework loudly expects a real test for each cell of that feature's row across `(local, remote) × (claude, codex, pi)`. See [PLAN.md](PLAN.md) for the mechanism.

**The integrity of the matrix is enforced by these rules and by Lukas auditing it — not by clever framework code.** That means the burden is on you to be honest. The following are hard rules:

### Honesty rules (a cell may go green ONLY if all of these hold)

1. **Exercise the real thing. Never mock the thing under test.** The cardinal sin of this project is making a cell green by mocking away the behavior it is supposed to prove.
   - A **`remote`** cell must perform a *real ssh hop* into a real remote daemon/tmux. `ssh localhost` into a second daemon/socket on the same box is acceptable and honest (it drives the actual remote code path — it would have caught the `--machine X` bug). A mocked or stubbed ssh/transport is a **violation**.
   - An **agent** cell (`claude`/`codex`/`pi`) must spawn the *real agent binary* in a real tmux pane. Mocking the agent process is a **violation**.
2. **Assert the observable external effect, not internal state.** "Did a process with this thread id actually land on the remote host?" — not "did we set `machine=remote`?". For liveness, **kill the real process and assert the state flips to dead** — test both directions, because the codex bug was a one-directional check.
3. **Skips are allowed but never silent and never count as done.** An unimplemented cell is a `t.Skip("NOT IMPLEMENTED: …")` (renders yellow) — it must be queryable (`<matrix> skips`) and it never counts toward "done".
4. **`N/A` requires a justification string** that Lukas has signed off. You may not silently drop a cell from a feature's declared axes to avoid testing it. Pi supports headless turns: the old guide's hypothetical “pi has no headless mode” example is not a valid justification.
5. **Done = the full matrix is green** with zero skips and zero unjustified N/A. "Done" is not a judgement call you make — it is the matrix all-green.

### Do not game the matrix

Do **not** weaken an assertion, shrink a feature's declared axes, stub-and-forget, or mock a dependency to turn a cell green. The grid is a *measurement*, not a goal. If you cannot honestly make a cell green, leave it `Skip`/red and say so — loudly, in your summary. A red matrix that tells the truth is infinitely more valuable than a green one that lies. Lukas runs an audit agent over the grid that checks exactly this; rigging will be found.


## Test environment notes

- **Lukas's LIVE sesh — this very binary — is running on these machines, with his real
  threads in it.** The conformance suite MUST never touch it: every test isolates
  `SESH_HOME`, `SESH_TMUX_SOCKET`, `SESH_MASTER_SOCKET`, and `SESH_CODEX_HOME`, and
  strips any inherited `SESH_*` from the test process env (`sandboxEnv` in the harness).
  Never leave a socket/home at its default in a test. A test that kills panes or wipes a
  store at the default paths destroys his working state, and you are probably running
  *inside* one of those threads while you do it.

- **Teardown cannot depend on the test process surviving — `TestMain` reaps the LAST
  run's leaks.** Each sandbox kills its own tmux server in `t.Cleanup`, which covers the
  ordinary path, but `t.Cleanup` never runs when the test BINARY dies: Ctrl-C on `go
  test`, a `-timeout` abort, a SIGKILL. tmux cannot recover on its own either —
  `exit-empty` would close an idle server, but a leaked sandbox session still holds a
  live `claude`/`pi`, so the server stays up **forever** with a real agent in it.

  Measured on mymain 2026-09-23: three leaked servers aged **5-6 days**, each still
  running an agent, plus **349** dead socket files. They were invisible to `sesh thread
  list` (their stores went with the temp dirs), so nothing surfaced them — one was found
  only because its agent still held an ssh channel to macstudio and was starving
  `ssh-target`'s ControlMaster.

  So `reapStaleTestServers` (harness_test.go) runs from `TestMain` before `m.Run()` and
  kills any `sesh-test-*` server whose embedded `-<UnixNano>` stamp is older than
  `staleTestServerAge` (2h), unlinking dead sockets as it goes. The age bound is the only
  reason it is safe: it must never kill a **concurrent** run's servers. It reports each
  kill on stderr rather than sweeping silently, so a leak that keeps recurring stays
  visible. `reap_test.go` covers the age split, that it touches nothing it does not own
  (`sesh`, `sesh-master`, unparseable or lookalike names), and — end to end — that a real
  server holding a real process is actually killed.

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


## Choosing and interpreting runs

- Name unit-test packages explicitly, e.g. `go test ./internal/tui -count=1`.
  `go test ./internal/...` includes expensive real-agent conformance.
- For a deliberately requested full suite: `go test ./internal/conformance -v
  -count=1`; render results with `go run ./cmd/sesh matrix grid`. Report run scope,
  passes/failures/skips/N/A and not-run cells. Focused green is not full-grid green.
- For a focused cell, use the exact subtest levels below. Confirm actual test names
  under `-v`; exit zero with zero matching tests is no evidence.

```sh
go test ./internal/conformance -run '^TestMatrix$/^thread.info$/^-$/^local$' -v -count=1
```

- `/` separates subtest levels even inside alternation. Agent-agnostic rows still
  have a `-` agent level. A `TestMatrix/...` filter excludes all non-matrix tests.
- Register TUI claims AND declare them in `declaredTUIClaims`
  (`internal/conformance/tui_test.go`); binding alone does not schedule a claim.
- Assert a presence/baseline before absence, and seed decoys to expose vacuous
  cursor/default success. Wait for real readiness: an empty startup pane looks idle.
- A neuter must fail on the intended observable, not compilation. Restore exact
  edits and verify byte identity; never widen a live process-kill matcher.
- Cleanup only verified sandbox PIDs/socket names. Never `pkill -f` or a generic
  command-pattern sweep. Finish stale cleanup before launching a new matching run.
- Call the sandbox binary by absolute path: myrig's `sesh` shell function can pin
  SESH_HOME back to production. `tmux -L` wants a unique NAME, not a path.
- Never attach a test viewer to a user's live session: it can resize their panes.

See [engineering history](ENGINEERING_INDEX.md) for the evidence behind these traps.
