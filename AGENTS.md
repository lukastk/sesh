# sesh v2 — operating guide

One Go binary + per-machine daemon owns multi-machine coding-agent threads, tmux
orchestration, tickets, and the TUI. **v2 is shipped on six machines; do not regress
live work.** `sesh` owns mechanism/contracts; `myrig` owns policy, keybindings and
shell UX. Keep the CLI explicit, machine-readable (`--json`), and schema-versioned;
no magic defaults or new shell-glue layer. Explicit user-configured policy is allowed.

The cross-machine whole is **mycockpit / the cockpit**, not “the master tmux setup”
or “the master cockpit”. Its **master** level is `SESH_MASTER_SOCKET` / `C-a`;
**base** is one machine's `SESH_TMUX_SOCKET` / `C-b`. Keep these level names in APIs,
files and myrig's `mmt-*` / `mt-*` vocabulary.

## Map and task routing (read only what the task needs)

- `cmd/sesh/`: CLI, routing, help; `internal/client/` + `internal/api/`: contracts.
- `internal/daemon/`: owner-side runtime, mesh, delivery; `internal/store/` +
  `internal/migrate/`: persistence; `internal/agents/`: harness integrations.
- `internal/tmux/`, `internal/tui/`: orchestration and UI;
  `internal/matrix/`, `internal/conformance/`: feature registry and real e2e tests.
- `skills/`: user-facing CLI and ticket guides; `_dev/`: on-demand designs/history.

| Task | Read before changing that area |
|---|---|
| Architecture / feature work | [SPEC](_dev/SPEC.md) in full; [PLAN](_dev/PLAN.md) for registry/harness; [BACKLOG](_dev/BACKLOG.md) before designing something new |
| Tests / conformance | [TESTING](_dev/TESTING.md), including teardown and real-cross-host prerequisites |
| Deploy / services / machine operations | [OPERATIONS](_dev/OPERATIONS.md) |
| Mesh / performance | [MESH](_dev/MESH.md), [MESH_SCALE](_dev/MESH_SCALE.md) |
| Cockpit / shells / sidebar / status | [MASTER](_dev/MASTER.md), [SHELL](_dev/SHELL.md), [SIDEBAR](_dev/SIDEBAR.md), [STATUS_OPTIONS](_dev/STATUS_OPTIONS.md), as applicable |
| Current-thread inference | [IDENTITY](_dev/IDENTITY.md): env is a hint, not verified identity |
| Delivery / agent state | [PI_DELIVERY](_dev/PI_DELIVERY.md), [STATE_AUTHORITY](_dev/STATE_AUTHORITY.md) |
| Scheduling | [SCHEDULING](_dev/SCHEDULING.md), including §16 implementation departures (built, not merely scoped) |
| CLI/TUI feature contracts / v1 archaeology | [CLI_TUI_FEATURES](_dev/CLI_TUI_FEATURES.md), [PARITY_ROADMAP](_dev/PARITY_ROADMAP.md), [V1_FEATURE_AUDIT](_dev/V1_FEATURE_AUDIT.md) |
| Prior diagnoses, reversions, deployment evidence | [ENGINEERING_INDEX](_dev/ENGINEERING_INDEX.md); search topic/H-number, then read relevant entries **and corrections** |

Do not load the entire design corpus or chronological archive at startup. PLAN's
“build the tracking spine first” is history: the spine already exists.

## Non-negotiable engineering rules

- **Loud errors, no defensive fallbacks.** Reachable unimplemented paths must
  panic/return `NOT IMPLEMENTED: <what>`, never plausible wrong success. Unexpected
  empty/nil is a bug, not a cue to invent a default. If a hack/workaround seems
  necessary, stop and ask Lukas rather than papering over it.
- **The feature matrix measures truth, not effort.** Register each conformant
  feature → bind initially Skip/red tests → implement. Axes are
  `(local, remote) × (claude, codex, pi)` as declared by the feature.
  - Never mock the thing under test: remote cells require a **real SSH hop** into
    a real daemon/tmux (`ssh localhost` with isolated sockets is acceptable), not
    stub transport. Agent cells use the **real agent binary in a real tmux pane**.
  - Assert **observable external effects**, not fields set internally. For
    liveness, kill the real process and assert dead; prove both directions.
  - Unimplemented cells use loud, queryable `t.Skip("NOT IMPLEMENTED: …")`, never
    count as done. N/A needs a justification **signed off by Lukas**.
  - Never weaken assertions, shrink declared axes, stub-and-forget, or mock a
    dependency to make green. Leave honest red/Skip and report it.
  - **Feature done = full matrix green, zero skips and zero unjustified N/A.**
    Unit and e2e tests are both required; neither substitutes for the other.
    Units/regressions may live outside the matrix; per-cell obligations apply to
    registered conformant features. A focused pass is not an all-green claim.
- **Surface changes require matching docs in the same change:** commands, flags,
  semantics, TUI keys/columns and env vars → `skills/sesh-cli/SKILL.md`; ticket
  surface → `skills/do-tickets/SKILL.md` too. Keep `cmd/sesh/help.go` and
  `cmd/sesh/help_flags.go` accurate. Help meta-tests require an entry per dispatched
  command and a `flagDoc` per usage flag (no duplicates/orphans). `tui --columns`
  values are generated from `tui.ValidColumnNames()`, not manually listed in help.
- Store migrations are **append-only**. Never auto-delete durable thread records
  because runtime vanished. Preserve single-authoritative-writer ownership.
- Commit messages must be **prompts another agent could use to recreate the work**.

## Safety: this is the user's live rig

- Tests must isolate **SESH_HOME, SESH_TMUX_SOCKET, SESH_MASTER_SOCKET,
  SESH_CODEX_HOME** and strip inherited **SESH_*** (`sandboxEnv`). Never default a
  test home/socket to production. Invoke sandbox binaries by absolute path, not
  myrig's `sesh` function. Never attach test viewers to live sessions (resize risk).
- Teardown cannot rely only on `t.Cleanup`: aborting the test binary leaves agents
  running. Keep TestMain's stale-test-server reaper and its **2h age/ownership
  boundary**, which protects concurrent runs; see TESTING before changing cleanup.
  Kill only verified owned PIDs/socket names, never broad process patterns.
- **Never hand-restart a production daemon.** Except Termux, use
  `supervisorctl restart sesh-daemon`. The supervisor ini owns `SESH_API_ADDR`,
  `SESH_API_TOKEN_FILE`, PATH, SHELL, and `SESH_TMUX_CONF`. A shell-started daemon
  can lose inbound API/mesh visibility and block supervisor recovery by holding
  its socket. No-API warnings are actionable, not permission for a fallback.
  Termux alone has no supervisor/API; use OPERATIONS' explicit-PID relaunch recipe.
- Check git status before edits/deploys; preserve concurrent work. Never ship a
  dirty checkout by accident. Do not widen live kill matchers for test experiments.

## Work and verification

Use `go build ./...`, `go vet ./...`, and explicitly named unit packages (e.g.
`go test ./internal/tui -count=1`) as appropriate. **`go test ./internal/...` also
runs real-agent conformance**; choose that expense deliberately. Full conformance:
`go test ./internal/conformance -v -count=1`; results:
`go run ./cmd/sesh matrix grid`. Keep the rendered matrix current for feature work;
report greens/reds/skips and what was **not run**, never imply a fresh full run.
Documentation-only work uses link/diff checks, not unrelated integration suites.

Deploy is part of implementation delivery unless the task explicitly defers it.
Merged is not live: myrig builds the binary per machine. Report exactly which
machines run the change and which do not; distinguish binary-only changes from
changes requiring daemon rebuild + supervised restart. Live-smoke daemon-exec
paths: test environments do not prove supervisor environments. Running sidebars
keep their old binary/config until restarted (`prefix+r`).

Keep [AGENTS.local.md](AGENTS.local.md) a short current trap/index file, not a
chronological log. Record substantial new findings (especially failed approaches)
in a dated topic/history doc under `_dev/` and link it from ENGINEERING_INDEX;
retain only active summaries locally. Preserve tracking/privacy when moving notes.
