# Daemon filesystem and command-provider plugins

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

## Plugins (`sesh plugins`) — daemon command-providers

A plugin manifest at `<SESH_HOME>/plugins/*.toml` declares commands the daemon runs **on
its own host** and how the sesh-ui app surfaces them. The app (especially mobile / a remote
daemon) has no shell on the target, so machine ops go via the daemon. Two capability kinds:

- **list** — a command whose JSON output is mapped to `{id,label,groups,path}` items
  (templated `id`/`label`/`path` over each item's fields; `groups` names a string-array
  field; `items` is a dotted path to the array, empty = root). E.g. boxyard boxes → the
  new-thread cwd picker **with groups**.
- **action** — a command with form `field`s; the values are substituted into the argv as
  **ARGV** (never a shell string → no injection) and the command runs. E.g. create-a-box.

```bash
sesh plugins list --json                                    # manifests + capabilities
sesh plugins run boxyard boxes --machine macbook --json     # a list capability → items
sesh plugins run boxyard create-box --field name=my-box     # an action; values as ARGV
```

Routes per `--machine` like `fs list`, so you drive whichever machine's plugins you need.
Commands come from the manifest **only**, never the client. Bad requests (unknown plugin or
capability, missing required field, nonzero command exit) fail **loudly**. The bundled
manifest example below can be placed at `<SESH_HOME>/plugins/boxyard.toml` on a
machine with `boxyard` on the daemon's PATH. No source checkout is needed.

## Bundled Boxyard manifest

The source example is bundled here as documentation, not installed automatically.
Place only the TOML block in the destination manifest after reviewing it.

```toml
# boxyard plugin — the first command-provider, validating the substrate.
#
# Drop this at <SESH_HOME>/plugins/boxyard.toml on any machine that has `boxyard`
# on the daemon's PATH. The daemon runs these commands ON ITS OWN HOST; the sesh-ui
# app reaches them over the API (GET /v1/plugins, POST /v1/plugins/boxyard/<cap>),
# routed cross-machine like fs/list — so the app drives the boxyard on whichever
# machine you are deploying a thread to.
#
# Unlocks two things in the app:
#   - the new-thread cwd picker can list boxes WITH their groups (the `boxes` list),
#   - create-a-box from a form on a chosen machine (the `create-box` action).

name = "boxyard"
description = "Boxyard boxes on this machine"

# A LIST capability: `boxyard list --output-format json` emits a top-level JSON array
# of box objects. id/label/path are TEMPLATES over each object's fields; the box index
# (= the ~/dev/<index> checkout dir) is composed from three fields. `groups` names the
# string-array field carrying the box's groups — what the picker groups by.
[[list]]
name = "boxes"
description = "all boxes in the yard, mapped for the cwd picker"
command = ["boxyard", "list", "--output-format", "json"]
id = "{creation_timestamp_utc}_{box_subid}__{name}"
label = "{name}"
groups = "groups"
path = "~/dev/{creation_timestamp_utc}_{box_subid}__{name}"

# An ACTION capability: the app renders a form from `field`s; the daemon substitutes
# the values into the command argv (as ARGV — never a shell string), runs it, and
# returns the output. `boxyard new` defaults the storage location and timestamp.
[[action]]
name = "create-box"
description = "create a new box on this machine"
command = ["boxyard", "new", "--box-name", "{name}"]
[[action.field]]
name = "name"
label = "Box name"
type = "text"
required = true
```
