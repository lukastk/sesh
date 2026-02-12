# sesh

A CLI tool for managing working sessions. Tracks sessions with parent/child relationships, tmux integration, AI coding assistants, archiving, and fzf-based switching.

## Install

```bash
uv pip install -e .
```

Requires Python 3.11+ and [tmux](https://github.com/tmux/tmux). Optional: [fzf](https://github.com/junegunn/fzf) for interactive session switching, [repoyard](https://github.com/lukas/repoyard) for auto-detection.

## Quick start

```bash
# Create a session (auto-names from directory)
sesh new --dir ~/projects/myapp

# Create a named session with tmux
sesh new myapp --dir ~/projects/myapp --tmux

# List sessions
sesh list

# Switch (opens fzf picker if no name given)
sesh switch
sesh switch myapp

# Archive when done
sesh archive myapp

# Delete permanently
sesh delete myapp --force
```

## Commands

### `sesh new [NAME]`

Create a new session. If NAME is omitted, it's auto-detected from repoyard or the directory basename.

```bash
sesh new myapp --dir ~/projects/myapp
sesh new myapp --dir ~/projects/myapp --tmux          # with tmux session
sesh new myapp --dir ~/projects/myapp --parent main   # as child of "main"
sesh new myapp --dir ~/projects/myapp --tag work      # with tags
sesh new myapp --dir ~/projects/myapp --claude         # with a Claude Code AI session (implies --tmux)
sesh new myapp --dir ~/projects/myapp --opencode       # with an OpenCode AI session (implies --tmux)
sesh new myapp --dir ~/projects/myapp --claude --cmd max-yolo-claude  # custom AI binary
```

### `sesh list`

```bash
sesh list               # active sessions (default)
sesh list --archived    # archived sessions only
sesh list --all         # everything
sesh list --tree        # tree view showing parent/child relationships
sesh list --json        # JSON output
```

### `sesh switch [NAME]`

Switch to a session. Without a name, opens an fzf picker. Outputs JSON to stdout for use with a shell wrapper (see [Shell integration](#shell-integration)).

### `sesh info [NAME]`

Show details about a session. Auto-detects the current session if no name is given.

```bash
sesh info myapp
sesh info myapp --json
```

### `sesh archive [NAME]`

Archive a session. The tmux session is renamed to `archived/<name>` by default.

```bash
sesh archive myapp              # rename tmux session
sesh archive myapp --kill-tmux  # kill tmux session entirely
```

### `sesh restore NAME`

Restore an archived session back to active.

### `sesh delete NAME`

Delete a session permanently. Requires `--force` if the session is active or has children.

## AI sessions

sesh can launch and track AI coding assistants (Claude Code, OpenCode) inside your tmux sessions.

### `sesh ai new [SESH_NAME]`

Launch a new AI session in a tmux window.

```bash
sesh ai new myapp --type claude                  # new Claude Code session
sesh ai new myapp --type opencode                # new OpenCode session
sesh ai new myapp --type claude --name my-agent  # custom name (default: claude-1, claude-2, ...)
sesh ai new myapp --type claude --cmd max-yolo-claude  # custom binary
```

### `sesh ai list [SESH_NAME]`

```bash
sesh ai list myapp          # table view
sesh ai list myapp --json   # JSON output
```

### `sesh ai resume [AI_NAME] [SESH_NAME]`

Resume an existing AI session in a new tmux window. Uses the same binary that was used to create the session.

```bash
sesh ai resume                  # auto-selects if only one AI session
sesh ai resume claude-1 myapp   # specific session
```

### `sesh ai add [SESH_NAME]`

Manually register an existing AI session ID.

```bash
sesh ai add myapp --type claude --name imported --id <session-id>
```

### `sesh ai remove AI_NAME [SESH_NAME]`

Remove an AI session record.

## Configuration

sesh uses an optional JSON config file at `~/.config/sesh.json`. All keys are optional — missing keys fall back to built-in defaults.

```json
{
  "claude_command": "claude",
  "opencode_command": "opencode"
}
```

| Key | Default | Description |
|-----|---------|-------------|
| `claude_command` | `"claude"` | Binary to run for Claude Code AI sessions |
| `opencode_command` | `"opencode"` | Binary to run for OpenCode AI sessions |

### Custom config path

Use the global `--config` flag to point to a different config file:

```bash
sesh --config ~/my-sesh-config.json ai new myapp --type claude
```

### Per-invocation override

The `--cmd` flag on `sesh new` and `sesh ai new` overrides both the config file and defaults for that single invocation:

```bash
sesh new myapp --dir ./foo --claude --cmd max-yolo-claude
sesh ai new myapp --type claude --cmd max-yolo-claude
```

**Resolution order:** `--cmd` flag > config file > built-in default.

The resolved command is stored on each AI session, so `sesh ai resume` uses the same binary that created the session.

## Shell integration

`sesh switch` outputs JSON to stdout because a subprocess can't change the calling shell's directory or tmux client. Add a wrapper function to your shell config:

```bash
function s() {
    local result
    result=$(sesh switch "$@" 2>/dev/null)
    if [ -z "$result" ]; then return 1; fi

    local tmux_session dir
    tmux_session=$(echo "$result" | jq -r '.tmux_session // empty')
    dir=$(echo "$result" | jq -r '.dir')

    if [ -n "$tmux_session" ]; then
        if [ -n "$TMUX" ]; then
            tmux switch-client -t "$tmux_session"
        else
            tmux attach-session -t "$tmux_session"
        fi
    else
        cd "$dir"
    fi
}
```

Then use `s` instead of `sesh switch`:

```bash
s          # fzf picker
s myapp    # direct switch
```

## Data storage

- **Sessions file:** `~/.sesh/sessions.json`
- **Config file:** `~/.config/sesh.json` (optional)

## Session auto-detection

When you omit the session name from commands like `sesh info` or `sesh ai new`, sesh tries to detect the current session by:

1. Matching the current tmux session name
2. Matching via repoyard index name (from `$PWD`)
3. Matching by directory (`$PWD`)
