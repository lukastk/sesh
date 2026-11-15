# Daemon filesystem listing and host commands

Bundled sesh-cli reference; load only for this task. See [the operational core](../SKILL.md)
for mandatory identity and safety rules. Dated incidents/version notes describe
the measured builds, not current fleet deployment.

<!-- BEGIN PRESERVED TOPIC -->
## Listing directories on a daemon (`sesh fs list`)

A generic, policy-free filesystem primitive the daemon serves over its API: the immediate
**subdirectories** of an allow-listed, **home-rooted** path on the daemon's host. Routes per
`--machine` like tickets, so you enumerate the machine you're targeting (works where the
caller has no local filesystem access — e.g. the Obsidian app on mobile filling its
box/mysetup cwd pickers).

```bash
sesh fs list --path ~/dev                       # box checkout dirs (name<TAB>~-relative path)
sesh fs list --path '~/mysetup' --machine macbook --json
```

Dirs only (symlinks not followed). A path **outside the home dir** — or one escaping via
`../` — is refused **loudly** (403), never a silent empty listing.

## Running named commands on a machine (`sesh host-cmd`)

A machine's owner declares commands in `<SESH_HOME>/host-commands/<provider>.toml`; that
machine's daemon runs one when a client names it. Routes per `--machine`, so a client with no
shell on the machine (a phone, a browser through the hub) can still run it. Sesh knows nothing
about what the commands do: which commands exist is the manifest's choice, typically
installed by the rig (for example a boxyard provider on machines that have boxyard).

```toml
# ~/.sesh/host-commands/boxyard.toml — the provider name is the file stem
description = "Boxyard boxes on this machine"

[[command]]
name = "create-box"
description = "create a new box here"
argv = ["boxyard", "new", "--box-name", "{name}"]
timeout = "120s"            # default 60s, at most 10m

[[command.arg]]
name = "name"
label = "Box name"
required = true
# allow_dash = true         # admit values starting with "-" (refused by default)
```

```bash
sesh host-cmd list --machine macbook
sesh host-cmd run --provider boxyard --command create-box --arg name=scratch --machine macbook
```

- **argv comes only from the manifest.** A caller supplies values for declared args, which
  replace `{arg}` placeholders inside argv elements. There is no shell, so no quoting or
  `~` expansion happens: write absolute paths, or call a program that resolves them. An
  absent optional arg substitutes the empty string.
- **Refusals are loud (400/404):** an unknown provider, command or arg, a missing required
  arg, a value containing NUL, or a value starting with `-` unless the arg sets
  `allow_dash`. A malformed manifest (unknown keys, an undeclared or unused placeholder, a
  bad timeout) fails every host-command request on that machine and names the file.
- **Runs are asynchronous**, because every hop to a daemon has a 15 s HTTP timeout:
  `POST /v1/host-commands/run {provider, command, args, request_id}` starts one and
  returns `{run_id, state: "running"}`; `GET /v1/host-commands/run?id=&wait_ms=` long-polls
  it (capped at 8 s) until `state` is `done`. `request_id` is a caller-minted UUID: a start
  retried with the same id returns the original run (`replayed: true`) instead of running
  the command twice, and the same id with a different request is a 409. `sesh host-cmd run`
  does the polling.
- **The result** carries `exit_code`, `stdout`, `stderr` (each capped at 1 MiB, with
  `stdout_truncated`/`stderr_truncated`) and `duration_ms`. A nonzero exit is the command's
  own result, not an HTTP error. `error` is set, with `exit_code` -1, when the program could
  not start, was killed at its timeout (with its whole process group), or the daemon shut
  down. The command runs in the daemon's home directory with the daemon's environment and
  PATH. At most 8 run at once per machine (more is a 429).
- **Runs live in daemon memory**: finished runs are kept 15 minutes, and a daemon restart
  loses them (reading one is a 404) and kills running ones.
- **Browser hub twins** (capability `browser.host-commands-v1`): `GET
  /v1/browser/host-commands?machine=`, `POST /v1/browser/host-commands/run {machine,
  request}` and `GET /v1/browser/host-commands/run?machine=&id=&wait_ms=`, under the same
  hub policy as every other machine-routed browser route.
