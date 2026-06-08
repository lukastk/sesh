# internal/matrix — the tracking spine

This package is the **feature matrix**: the antidote to v1's silent feature-gaps
(see `../../AGENTS.md`). It enforces *completeness* and *visibility*, never
*honesty* — honesty is on the test author and Lukas's audit agent, by design.

## Axes

`(local, remote) × (claude, codex, pi)`. Agent-agnostic features (tmux/ticket
plumbing) have no agent axis; they render under a single `-` column.

## How it fits together

- **`features.go`** registers the canonical feature set from `_dev/PLAN.md` at
  package `init()`. Each `Feature` declares its `Agents` (or none = agnostic),
  `Localities`, and any justified `NA` cells. `ExpectedCells()` = the product of
  the axes minus N/A.
- **`../conformance`** (test-only package) binds one test per expected cell.
  - `TestMatrix` runs every bound cell as a subtest, recording pass/fail/skip.
  - `TestMatrixComplete` **fails the build** if any expected cell has no bound
    test (a missing test is red, never a blank), or if a test drifted off-axis.
  - `TestMain` renders the grid and writes the run artifact afterwards.
- **`cmd/sesh matrix`** reports the last run from the artifact (`grid` | `skips`,
  `--json`). The CLI never runs tests; the matrix is a *measurement* of the
  suite, kept separate from it.

## Rules of the road (from AGENTS.md)

- A cell goes green **only** via a real test: a remote cell does a real ssh hop;
  an agent cell spawns the real agent binary in a real tmux pane. Mocking the
  thing under test is the cardinal sin.
- Leave an unfinished cell as `matrix.Skip(t, "...")` — it renders ⚠ yellow,
  shows up in `sesh matrix skips`, and **never** counts as done.
- An `N/A` needs a justification string Lukas has signed off (`NAEntry.Reason`);
  you may not silently drop a cell from a feature's axes.
- **Done = the full matrix is green** with zero skips, zero missing, zero unrun.
  `Counts.AllGreen()` is the single computed gate.

## Run it

```sh
go test ./internal/conformance        # run the matrix, write the artifact
go run ./cmd/sesh matrix grid         # render last run
go run ./cmd/sesh matrix skips        # list NOT-IMPLEMENTED cells
go run ./cmd/sesh matrix grid --json  # machine-readable
```

## Adding a feature

1. Register it in `features.go` with its axes.
2. Add its cells to the conformance suite (start with `skipAll` → all yellow).
3. Implement until each cell is a real, green test; replace `skipAll` with
   explicit per-cell tests as they land. Delete `skipAll` once fully covered.
