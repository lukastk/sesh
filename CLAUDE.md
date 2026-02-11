# CLAUDE.md — sesh

## Overview

**sesh** is a Python CLI tool for managing working sessions. It tracks sessions with parent/child relationships, tmux integration, archiving, and fzf-based switching. Built with Typer.

## Commands

```bash
# Dev install
pip install -e .

# Run
sesh new [NAME] [--dir PATH] [--tmux] [--parent SESSION] [--tag TAG]
sesh list [--all] [--archived] [--tree] [--json]
sesh switch [NAME]              # prints JSON to stdout for shell wrapper
sesh archive [NAME] [--kill-tmux]
sesh delete NAME [--force]
sesh info [NAME] [--json]
sesh restore [NAME]
```

## Architecture

```
sesh/
├── pyproject.toml
├── CLAUDE.md
├── PLAN.md
└── sesh/
    ├── __init__.py
    ├── cli.py          # Typer app, all commands
    ├── store.py         # Sessions JSON read/write (~/.sesh/sessions.json)
    └── tmux.py          # Tmux subprocess helpers
```

### Key paths

- **Data dir**: `~/.sesh/`
- **Sessions file**: `~/.sesh/sessions.json`
- **Per-session dirs**: `~/.sesh/sessions/<name>/`
- **Config** (future): `~/.config/sesh.json`

### Data model (`sessions.json`)

```json
{
  "version": 1,
  "sessions": {
    "<name>": {
      "name": "string",
      "dir": "/absolute/path",
      "tmux_session": "string | null",
      "status": "active | archived",
      "created": "ISO 8601 timestamp",
      "parent": "string | null",
      "children": ["string"],
      "repoyard_index_name": "string | null",
      "tags": ["string"]
    }
  }
}
```

### Shell integration

sesh can't `cd` or `tmux switch-client` in the calling shell. The `sesh switch` command outputs JSON to stdout, and a thin shell wrapper (`s()` in `coding.sh`) reads that JSON and performs the shell-level action.

### Conventions

- All file I/O goes through `store.py` — never read/write `sessions.json` from `cli.py` directly
- Tmux operations go through `tmux.py` — never call `subprocess` for tmux from `cli.py` directly
- `sesh switch` must output **only** JSON to stdout; all messages go to stderr
- Use `rich` (bundled with Typer) for table/tree formatting in `sesh list`
- Errors use `typer.echo(..., err=True)` and `raise typer.Exit(code=1)`

### Testing

```bash
# Quick smoke test
mkdir -p /tmp/sesh-test && sesh new test --dir /tmp/sesh-test && sesh info test && sesh list && sesh delete test --force
```

## Dependencies

- Python 3.11+
- typer (includes rich)
- No other runtime dependencies — keep it minimal
