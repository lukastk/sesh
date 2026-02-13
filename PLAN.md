# PLAN.md — sesh Implementation Plan

## Phase 1: Project Scaffold

### 1.1 Create `pyproject.toml`

```toml
[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[project]
name = "sesh"
version = "0.1.0"
description = "Session manager with tmux integration"
requires-python = ">=3.11"
dependencies = ["typer>=0.15"]

[project.scripts]
sesh = "sesh.cli:app"
```

### 1.2 Create package files

- `sesh/__init__.py` — empty
- `sesh/cli.py` — Typer app stub with `app = typer.Typer()`
- `sesh/store.py` — empty class stub
- `sesh/tmux.py` — empty module stub

### 1.3 Verify

```bash
pip install -e .
sesh --help
```

---

## Phase 2: Core — `store.py`

The store manages all reads/writes to `~/.sesh/sessions.json`.

### 2.1 Data classes

```python
@dataclass
class Session:
    name: str
    dir: str
    tmux_session: str | None
    status: str  # "active" | "archived"
    created: str  # ISO 8601
    parent: str | None
    children: list[str]
    boxyard_index_name: str | None
    tags: list[str]
```

### 2.2 `SessionStore` class

```python
class SessionStore:
    DATA_DIR = Path.home() / ".sesh"
    SESSIONS_FILE = DATA_DIR / "sessions.json"

    def load(self) -> dict[str, Session]
    def save(self, sessions: dict[str, Session]) -> None
    def get(self, name: str) -> Session              # raises if not found
    def add(self, session: Session) -> None           # raises if exists
    def update(self, session: Session) -> None        # raises if not found
    def remove(self, name: str) -> None               # raises if not found
    def list(self, status: str | None = None) -> list[Session]
    def session_dir(self, name: str) -> Path          # ~/.sesh/sessions/<name>/
```

Key behaviors:
- `load()` creates `~/.sesh/` and returns empty dict if file doesn't exist
- `save()` writes atomically (write to `.tmp` then rename)
- `session_dir()` creates the directory on demand
- All methods that modify state call `load()` → mutate → `save()` (no in-memory caching across calls)

### 2.3 Verify

```python
# Quick REPL test
from sesh.store import SessionStore, Session
store = SessionStore()
store.load()  # should return {}
```

---

## Phase 3: Core — `tmux.py`

Thin wrappers around `tmux` subprocesses.

### 3.1 Functions

```python
def session_exists(name: str) -> bool
    # tmux has-session -t name; return code 0 = exists

def create_session(name: str, dir: str, window_name: str = "CC") -> None
    # tmux new-session -d -s name -n window_name -c dir

def kill_session(name: str) -> None
    # tmux kill-session -t name

def rename_session(old: str, new: str) -> None
    # tmux rename-session -t old new

def list_sessions() -> list[str]
    # tmux list-sessions -F "#{session_name}"; return [] if server not running
```

All functions use `subprocess.run` with `check=False` where appropriate, returning booleans or raising on unexpected errors.

### 3.2 Verify

```bash
python -c "from sesh.tmux import list_sessions; print(list_sessions())"
```

---

## Phase 4: Commands — `sesh new`

### 4.1 Signature

```python
@app.command()
def new(
    name: Annotated[Optional[str], typer.Argument()] = None,
    dir: Annotated[Optional[Path], typer.Option("--dir")] = None,
    tmux: Annotated[bool, typer.Option("--tmux")] = False,
    parent: Annotated[Optional[str], typer.Option("--parent")] = None,
    tag: Annotated[Optional[list[str]], typer.Option("--tag")] = None,
):
```

### 4.2 Logic

1. `dir` defaults to `Path.cwd()`
2. If `name` not given:
   - Try `boxyard which --json` on `dir` → extract `.name`
   - Fall back to `dir.name` (basename)
3. Check store for existing session with same name:
   - If active → error
   - If archived → print message suggesting `sesh restore`; exit 1
4. Detect boxyard: run `boxyard which --json --path <dir>`, capture `index_name` field (ignore errors)
5. If `--parent`: validate parent exists in store; error if not found
6. Create `Session` object with `status="active"`, `created=now()`
7. `store.add(session)`
8. If `--parent`: update parent's `children` list
9. If `--tmux`: call `tmux.create_session(name, dir)`; set `session.tmux_session = name`; `store.update(session)`
10. Create `store.session_dir(name)`
11. Print confirmation to stderr

### 4.3 Verify

```bash
mkdir -p /tmp/sesh-test
sesh new test-session --dir /tmp/sesh-test
cat ~/.sesh/sessions.json
```

---

## Phase 5: Commands — `sesh info`

### 5.1 Signature

```python
@app.command()
def info(
    name: Annotated[Optional[str], typer.Argument()] = None,
    json_output: Annotated[bool, typer.Option("--json")] = False,
):
```

### 5.2 Logic

1. If `name` not given:
   - Check `$TMUX` env → get current tmux session name → look up in store
   - Fall back to `boxyard which --json` on `$PWD` → match by boxyard_index_name
   - Fall back to matching session by `dir == $PWD`
   - Error if none found
2. `session = store.get(name)`
3. Live-check tmux: if `session.tmux_session` is set, verify with `tmux.session_exists()`
4. If `--json`: print session as JSON to stdout
5. Else: print formatted key-value pairs to stdout

### 5.3 Verify

```bash
sesh info test-session
sesh info test-session --json
```

---

## Phase 6: Commands — `sesh list`

### 6.1 Signature

```python
@app.command("list")
def list_sessions(
    all: Annotated[bool, typer.Option("--all")] = False,
    archived: Annotated[bool, typer.Option("--archived")] = False,
    tree: Annotated[bool, typer.Option("--tree")] = False,
    json_output: Annotated[bool, typer.Option("--json")] = False,
):
```

### 6.2 Logic

1. Load sessions filtered by status:
   - Default: `status="active"`
   - `--archived`: `status="archived"`
   - `--all`: no filter
2. Detect current session (same heuristic as `info`)
3. If `--json`: dump as JSON array
4. If `--tree`:
   - Group sessions: roots (no parent) and children under parents
   - Use `rich.tree.Tree` for display
   - Mark current with `*`
5. Default table:
   - Use `rich.table.Table`
   - Columns: NAME, DIR, TMUX, STATUS
   - Live-check tmux for each session
   - Mark current session with `*` prefix on name

### 6.3 Verify

```bash
sesh list
sesh new child --dir /tmp/sesh-child --parent test-session
sesh list --tree
```

---

## Phase 7: Commands — `sesh switch`

### 7.1 Signature

```python
@app.command()
def switch(
    name: Annotated[Optional[str], typer.Argument()] = None,
):
```

### 7.2 Logic

1. If `name` not given:
   - Get active sessions from store
   - Format as lines: `name\tdir\ttmux_status`
   - Pipe to `fzf` via `subprocess.run`
   - Parse selected line to get name
   - If fzf exits non-zero (cancelled) → exit 1
2. `session = store.get(name)`
3. If `session.tmux_session`:
   - If `tmux.session_exists(session.tmux_session)`: output JSON with tmux_session set
   - Else: recreate tmux session, update store, output JSON with tmux_session set
4. Else: output JSON with `tmux_session: null` and `dir` set
5. **Stdout output format** (JSON, one line):
   ```json
   {"name": "x", "dir": "/path", "tmux_session": "x"}
   ```
6. All human-readable messages go to stderr only

### 7.3 Verify

```bash
# Direct switch
result=$(sesh switch test-session)
echo "$result" | jq .

# fzf picker (interactive)
sesh switch
```

---

## Phase 8: Commands — `sesh archive`

### 8.1 Signature

```python
@app.command()
def archive(
    name: Annotated[Optional[str], typer.Argument()] = None,
    kill_tmux: Annotated[bool, typer.Option("--kill-tmux")] = False,
):
```

### 8.2 Logic

1. If `name` not given: detect current session (same as `info`)
2. `session = store.get(name)`; error if not active
3. Warn (to stderr) if session has active children
4. Set `session.status = "archived"`
5. If `session.tmux_session`:
   - If `--kill-tmux`: `tmux.kill_session(session.tmux_session)`; set `session.tmux_session = None`
   - Else: `tmux.rename_session(session.tmux_session, f"archived/{name}")`; update `session.tmux_session = f"archived/{name}"`
6. `store.update(session)`
7. Print confirmation to stderr

### 8.3 Verify

```bash
sesh archive test-session
sesh list --all
```

---

## Phase 9: Commands — `sesh restore`

### 9.1 Signature

```python
@app.command()
def restore(
    name: Annotated[str, typer.Argument()],
):
```

### 9.2 Logic

1. `session = store.get(name)`; error if not archived
2. Set `session.status = "active"`
3. If tmux session `archived/{name}` exists:
   - `tmux.rename_session(f"archived/{name}", name)`
   - `session.tmux_session = name`
4. `store.update(session)`
5. Print confirmation to stderr

### 9.3 Verify

```bash
sesh restore test-session
sesh list
```

---

## Phase 10: Commands — `sesh delete`

### 10.1 Signature

```python
@app.command()
def delete(
    name: Annotated[str, typer.Argument()],
    force: Annotated[bool, typer.Option("--force")] = False,
):
```

### 10.2 Logic

1. `session = store.get(name)`
2. If `session.status == "active"` or `session.children`: require `--force`
3. If `session.tmux_session` and `tmux.session_exists(session.tmux_session)`:
   - `tmux.kill_session(session.tmux_session)`
4. If `session.parent`:
   - `parent = store.get(session.parent)`
   - Remove `name` from `parent.children`
   - `store.update(parent)`
5. For each child in `session.children`:
   - `child = store.get(child_name)`
   - Set `child.parent = None`
   - `store.update(child)`
6. `store.remove(name)`
7. Remove `~/.sesh/sessions/<name>/` directory if it exists
8. Print confirmation to stderr

### 10.3 Verify

```bash
sesh delete test-session --force
sesh list
```

---

## Phase 11: Shell Integration in `mysetup`

### 11.1 Update `home/.mysetup/zshenv/coding.sh`

**Replace the empty `s()` function** with:

```bash
function s() {
    local result
    result=$(sesh switch "$@" 2>/dev/null)
    if [ -z "$result" ]; then return 1; fi
    local tmux_session=$(echo "$result" | jq -r '.tmux_session // empty')
    local dir=$(echo "$result" | jq -r '.dir')
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

**Update `setup-worktree`**: Replace the `tmux new-session` line (line 40) with:

```bash
sesh new "$worktree_name" --dir "$worktree_path" --tmux --parent "$repo_name" 2>/dev/null
```

Also need to register the main repo as a session if it isn't already. Add before the sesh new line:

```bash
# Ensure parent session exists
sesh new "$repo_name" --dir "$(git rev-parse --show-toplevel)" 2>/dev/null || true
```

**Update `unlink-worktree`**: Replace the `tmux kill-session` line (line 97) with:

```bash
sesh archive "$(basename "$worktree_path")" --kill-tmux 2>/dev/null
```

### 11.2 Update `home/.mysetup/zshenv/^all^all.sh`

**Update `new-repo`**: Add after the template copy block (before the function's closing `}`):

```bash
sesh new "$1" --dir "$repo_path" 2>/dev/null || true
```

### 11.3 Verify

```bash
zsh -n ~/mysetup/home/.mysetup/zshenv/coding.sh
zsh -n ~/mysetup/home/.mysetup/zshenv/^all^all.sh
```

---

## Implementation Order Summary

| Phase | What | Files |
|-------|------|-------|
| 1 | Project scaffold | `pyproject.toml`, `sesh/__init__.py`, `sesh/cli.py`, `sesh/store.py`, `sesh/tmux.py` |
| 2 | Store (JSON I/O) | `sesh/store.py` |
| 3 | Tmux helpers | `sesh/tmux.py` |
| 4 | `sesh new` | `sesh/cli.py` |
| 5 | `sesh info` | `sesh/cli.py` |
| 6 | `sesh list` | `sesh/cli.py` |
| 7 | `sesh switch` | `sesh/cli.py` |
| 8 | `sesh archive` | `sesh/cli.py` |
| 9 | `sesh restore` | `sesh/cli.py` |
| 10 | `sesh delete` | `sesh/cli.py` |
| 11 | Shell integration | `coding.sh`, `^all^all.sh` |

## Final Verification

```bash
# Full smoke test
pip install -e .
mkdir -p /tmp/sesh-test /tmp/sesh-child

sesh new test-session --dir /tmp/sesh-test
sesh info test-session
sesh info test-session --json
sesh list
sesh new child --dir /tmp/sesh-child --parent test-session
sesh list --tree
sesh archive child
sesh list --all
sesh restore child
sesh delete child --force
sesh delete test-session --force
sesh list  # should be empty

# Shell syntax check
zsh -n ~/mysetup/home/.mysetup/zshenv/coding.sh
zsh -n ~/mysetup/home/.mysetup/zshenv/^all^all.sh
```
