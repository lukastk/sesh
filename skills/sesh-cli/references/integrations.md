# Daemon filesystem listing

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
