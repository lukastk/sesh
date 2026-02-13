from __future__ import annotations

import json
import os
import subprocess
import sys
import uuid
from pathlib import Path
from typing import Annotated, Optional

import typer
from rich.table import Table
from rich.tree import Tree

from sesh import tmux
from sesh.store import AiSession, Session, SessionStore, load_config

app = typer.Typer(add_completion=False)
ai_app = typer.Typer(add_completion=False, help="Manage AI coding sessions.")
app.add_typer(ai_app, name="ai")
store = SessionStore()
_config_path: Path | None = None


@app.callback()
def main(
    config: Annotated[Optional[Path], typer.Option("--config", help="Path to config file")] = None,
    data_dir: Annotated[Optional[Path], typer.Option("--data-dir", help="Path to sesh data directory")] = None,
) -> None:
    """Session manager with tmux integration."""
    global store, _config_path
    if data_dir is not None:
        store = SessionStore(data_dir=data_dir)
    if config is not None:
        _config_path = config


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _detect_current_session() -> Session | None:
    """Try to detect the current session from environment context."""
    sessions = store.load()

    # 1. Check $TMUX → get current tmux session name → look up in store
    if os.environ.get("TMUX"):
        result = subprocess.run(
            ["tmux", "display-message", "-p", "#{session_name}"],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            tmux_name = result.stdout.strip()
            for s in sessions.values():
                if s.tmux_session == tmux_name:
                    return s

    # 2. Try boxyard which --json on $PWD → match by boxyard_index_name
    try:
        cwd = os.getcwd()
    except (FileNotFoundError, OSError):
        return None

    try:
        result = subprocess.run(
            ["boxyard", "which", "--json", "--path", cwd],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            by_data = json.loads(result.stdout)
            index_name = by_data.get("index_name")
            if index_name:
                for s in sessions.values():
                    if s.boxyard_index_name == index_name:
                        return s
    except (FileNotFoundError, json.JSONDecodeError):
        pass

    # 3. Fall back to matching by dir == $PWD
    for s in sessions.values():
        if s.dir == cwd:
            return s

    return None


def _boxyard_detect(dir_path: str) -> tuple[str | None, str | None]:
    """Run boxyard which --json on a path. Returns (name, index_name) or (None, None)."""
    try:
        result = subprocess.run(
            ["boxyard", "which", "--json", "--path", dir_path],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            data = json.loads(result.stdout)
            return data.get("name"), data.get("index_name")
    except (FileNotFoundError, json.JSONDecodeError):
        pass
    return None, None


# ---------------------------------------------------------------------------
# sesh new
# ---------------------------------------------------------------------------


@app.command()
def new(
    name: Annotated[Optional[str], typer.Argument()] = None,
    dir: Annotated[Optional[Path], typer.Option("--dir")] = None,
    tmux_flag: Annotated[bool, typer.Option("--tmux")] = False,
    parent: Annotated[Optional[str], typer.Option("--parent")] = None,
    tag: Annotated[Optional[list[str]], typer.Option("--tag")] = None,
    claude: Annotated[bool, typer.Option("--claude", help="Also create a Claude Code AI session (implies --tmux)")] = False,
    opencode: Annotated[bool, typer.Option("--opencode", help="Also create an OpenCode AI session (implies --tmux)")] = False,
    cmd: Annotated[Optional[str], typer.Option("--cmd", help="Override the AI command binary")] = None,
    pin: Annotated[bool, typer.Option("--pin", help="Pin the session")] = False,
    flag: Annotated[bool, typer.Option("--flag", help="Flag the session")] = False,
) -> None:
    """Create a new session."""
    if claude or opencode:
        tmux_flag = True
    dir_path = str((dir or Path.cwd()).resolve())

    # Create directory if it doesn't exist
    Path(dir_path).mkdir(parents=True, exist_ok=True)

    # Auto-detect name if not provided
    if name is None:
        by_name, _ = _boxyard_detect(dir_path)
        name = by_name or Path(dir_path).name

    # Check for existing session
    sessions = store.load()
    if name in sessions:
        existing = sessions[name]
        if existing.status == "archived":
            typer.echo(f"Session '{name}' exists but is archived. Use `sesh restore {name}` to restore it.", err=True)
            raise typer.Exit(code=1)
        else:
            typer.echo(f"Session '{name}' already exists and is active.", err=True)
            raise typer.Exit(code=1)

    # Validate parent
    if parent:
        if parent not in sessions:
            typer.echo(f"Parent session '{parent}' not found.", err=True)
            raise typer.Exit(code=1)

    # Detect boxyard index name
    _, index_name = _boxyard_detect(dir_path)

    session = Session(
        name=name,
        dir=dir_path,
        parent=parent,
        boxyard_index_name=index_name,
        tags=tag or [],
        pinned=pin,
        flagged=flag,
    )
    store.add(session)

    # Update parent's children list
    if parent:
        parent_session = store.get(parent)
        if name not in parent_session.children:
            parent_session.children.append(name)
            store.update(parent_session)

    # Create tmux session if requested
    if tmux_flag:
        tmux.create_session(name, dir_path)
        session.tmux_session = name
        store.update(session)

    typer.echo(f"Created session '{name}' → {dir_path}", err=True)

    if claude:
        _create_ai_session(session, ai_type="claude", cmd=cmd)
    if opencode:
        _create_ai_session(session, ai_type="opencode", cmd=cmd)


# ---------------------------------------------------------------------------
# sesh info
# ---------------------------------------------------------------------------


@app.command()
def info(
    name: Annotated[Optional[str], typer.Argument()] = None,
    json_output: Annotated[bool, typer.Option("--json")] = False,
) -> None:
    """Show info about a session."""
    if name is None:
        current = _detect_current_session()
        if current is None:
            typer.echo("Could not detect current session.", err=True)
            raise typer.Exit(code=1)
        name = current.name

    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    # Live-check tmux
    tmux_live = False
    if session.tmux_session:
        tmux_live = tmux.session_exists(session.tmux_session)

    if json_output:
        data = {
            "name": session.name,
            "dir": session.dir,
            "tmux_session": session.tmux_session,
            "tmux_live": tmux_live,
            "status": session.status,
            "pinned": session.pinned,
            "flagged": session.flagged,
            "created": session.created,
            "parent": session.parent,
            "children": session.children,
            "boxyard_index_name": session.boxyard_index_name,
            "tags": session.tags,
        }
        typer.echo(json.dumps(data, indent=2))
    else:
        typer.echo(f"Name:      {session.name}")
        typer.echo(f"Dir:       {session.dir}")
        typer.echo(f"Status:    {session.status}")
        if session.pinned:
            typer.echo(f"Pinned:    yes")
        if session.flagged:
            typer.echo(f"Flagged:   yes")
        typer.echo(f"Created:   {session.created}")
        if session.tmux_session:
            status_str = "running" if tmux_live else "not running"
            typer.echo(f"Tmux:      {session.tmux_session} ({status_str})")
        if session.parent:
            typer.echo(f"Parent:    {session.parent}")
        if session.children:
            typer.echo(f"Children:  {', '.join(session.children)}")
        if session.boxyard_index_name:
            typer.echo(f"Boxyard:   {session.boxyard_index_name}")
        if session.tags:
            typer.echo(f"Tags:      {', '.join(session.tags)}")


# ---------------------------------------------------------------------------
# sesh pin / sesh unpin
# ---------------------------------------------------------------------------


@app.command()
def pin(
    name: Annotated[Optional[str], typer.Argument()] = None,
    toggle: Annotated[bool, typer.Option("--toggle")] = False,
) -> None:
    """Pin a session."""
    if name is None:
        current = _detect_current_session()
        if current is None:
            typer.echo("Could not detect current session.", err=True)
            raise typer.Exit(code=1)
        name = current.name

    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    if toggle:
        session.pinned = not session.pinned
    else:
        if session.pinned:
            typer.echo(f"Session '{name}' is already pinned.", err=True)
            raise typer.Exit(code=1)
        session.pinned = True

    store.update(session)
    state = "pinned" if session.pinned else "unpinned"
    typer.echo(f"Session '{name}' is now {state}.", err=True)


@app.command()
def unpin(
    name: Annotated[Optional[str], typer.Argument()] = None,
) -> None:
    """Unpin a session."""
    if name is None:
        current = _detect_current_session()
        if current is None:
            typer.echo("Could not detect current session.", err=True)
            raise typer.Exit(code=1)
        name = current.name

    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    if not session.pinned:
        typer.echo(f"Session '{name}' is not pinned.", err=True)
        raise typer.Exit(code=1)

    session.pinned = False
    store.update(session)
    typer.echo(f"Session '{name}' is now unpinned.", err=True)


# ---------------------------------------------------------------------------
# sesh flag / sesh unflag
# ---------------------------------------------------------------------------


@app.command()
def flag(
    name: Annotated[Optional[str], typer.Argument()] = None,
    toggle: Annotated[bool, typer.Option("--toggle")] = False,
) -> None:
    """Flag a session."""
    if name is None:
        current = _detect_current_session()
        if current is None:
            typer.echo("Could not detect current session.", err=True)
            raise typer.Exit(code=1)
        name = current.name

    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    if toggle:
        session.flagged = not session.flagged
    else:
        if session.flagged:
            typer.echo(f"Session '{name}' is already flagged.", err=True)
            raise typer.Exit(code=1)
        session.flagged = True

    store.update(session)
    state = "flagged" if session.flagged else "unflagged"
    typer.echo(f"Session '{name}' is now {state}.", err=True)


@app.command()
def unflag(
    name: Annotated[Optional[str], typer.Argument()] = None,
) -> None:
    """Unflag a session."""
    if name is None:
        current = _detect_current_session()
        if current is None:
            typer.echo("Could not detect current session.", err=True)
            raise typer.Exit(code=1)
        name = current.name

    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    if not session.flagged:
        typer.echo(f"Session '{name}' is not flagged.", err=True)
        raise typer.Exit(code=1)

    session.flagged = False
    store.update(session)
    typer.echo(f"Session '{name}' is now unflagged.", err=True)


# ---------------------------------------------------------------------------
# sesh markers (for tmux status line)
# ---------------------------------------------------------------------------


@app.command(hidden=True)
def markers() -> None:
    """Output marker string for the current session. Designed for tmux #()."""
    current = _detect_current_session()
    if current is None:
        return
    m = _session_markers(current)
    if m:
        sys.stdout.write(m)


# ---------------------------------------------------------------------------
# sesh list
# ---------------------------------------------------------------------------


def _resolve_show_markers(markers: bool | None) -> bool:
    """Resolve show_markers: CLI flag overrides config default."""
    if markers is not None:
        return markers
    config = load_config(_config_path)
    return config.get("show_markers", True)


def _session_markers(s: Session, show: bool = True) -> str:
    if not show:
        return ""
    markers = ""
    if s.pinned:
        markers += "★"
    if s.flagged:
        markers += "⚑"
    if s.status == "archived":
        markers += "▪"
    return f" {markers}" if markers else ""


@app.command("list")
def list_sessions(
    all: Annotated[bool, typer.Option("--all")] = False,
    archived: Annotated[bool, typer.Option("--archived")] = False,
    pinned: Annotated[bool, typer.Option("--pinned")] = False,
    flagged: Annotated[bool, typer.Option("--flagged")] = False,
    any_filter: Annotated[bool, typer.Option("--any", help="Match any filter (OR) instead of all (AND)")] = False,
    tree: Annotated[bool, typer.Option("--tree")] = False,
    json_output: Annotated[bool, typer.Option("--json")] = False,
    markers: Annotated[Optional[bool], typer.Option("--markers/--no-markers", help="Show status markers after session names")] = None,
) -> None:
    """List sessions."""
    if all:
        status_filter = None
    elif archived:
        status_filter = "archived"
    else:
        status_filter = "active"

    pinned_filter = True if pinned else None
    flagged_filter = True if flagged else None
    mode = "any" if any_filter else "all"
    sessions = store.list(status=status_filter, pinned=pinned_filter, flagged=flagged_filter, filter_mode=mode)

    if not sessions:
        typer.echo("No sessions found.", err=True)
        return

    show_markers = _resolve_show_markers(markers)

    # Detect current session
    current = _detect_current_session()
    current_name = current.name if current else None

    if json_output:
        from dataclasses import asdict

        typer.echo(json.dumps([asdict(s) for s in sessions], indent=2))
        return

    if tree:
        _print_tree(sessions, current_name, show_markers)
        return

    # Default: table
    _print_table(sessions, current_name, show_markers)


def _print_table(sessions: list[Session], current_name: str | None, show_markers: bool = True) -> None:
    table = Table()
    table.add_column("NAME")
    table.add_column("DIR")
    table.add_column("TMUX")
    table.add_column("STATUS")

    for s in sessions:
        name_display = f"* {s.name}" if s.name == current_name else s.name
        name_display += _session_markers(s, show_markers)
        tmux_status = ""
        if s.tmux_session:
            tmux_status = "running" if tmux.session_exists(s.tmux_session) else "stopped"
        table.add_row(name_display, s.dir, tmux_status, s.status)

    from rich.console import Console

    console = Console(stderr=True)
    console.print(table)


def _print_tree(sessions: list[Session], current_name: str | None, show_markers: bool = True) -> None:
    session_map = {s.name: s for s in sessions}
    roots = [s for s in sessions if s.parent is None or s.parent not in session_map]

    rich_tree = Tree("Sessions")

    def _add_children(parent_node: Tree, parent_session: Session) -> None:
        for child_name in parent_session.children:
            if child_name in session_map:
                child = session_map[child_name]
                label = f"* {child.name}" if child.name == current_name else child.name
                label += _session_markers(child, show_markers)
                child_node = parent_node.add(f"{label} ({child.status})")
                _add_children(child_node, child)

    for s in roots:
        label = f"* {s.name}" if s.name == current_name else s.name
        label += _session_markers(s, show_markers)
        node = rich_tree.add(f"{label} ({s.status})")
        _add_children(node, s)

    from rich.console import Console

    console = Console(stderr=True)
    console.print(rich_tree)


# ---------------------------------------------------------------------------
# sesh switch
# ---------------------------------------------------------------------------


def _tree_picker(sessions: list[Session], current_name: str | None, show_markers: bool = True) -> str | None:
    """Show an interactive tree picker using Textual inline mode. Returns selected session name or None."""
    from textual.app import App, ComposeResult
    from textual.widgets import Tree as TextualTree

    session_map = {s.name: s for s in sessions}
    roots = [s for s in sessions if s.parent is None or s.parent not in session_map]

    class SessionPicker(App[str]):
        CSS = """
        Screen {
            &:inline { height: auto; max-height: 50vh; }
        }
        Tree { height: auto; max-height: 50vh; }
        """
        INLINE_PADDING = 0
        current_node = None

        def compose(self) -> ComposeResult:
            tree: TextualTree[str] = TextualTree("Sessions")
            tree.show_root = False
            self._build_tree(tree.root, roots)
            yield tree

        def _build_tree(self, parent_node, parent_sessions):
            for s in parent_sessions:
                label = f"* {s.name}" if s.name == current_name else s.name
                label += _session_markers(s, show_markers)
                children_sessions = [session_map[c] for c in s.children if c in session_map]
                if children_sessions:
                    node = parent_node.add(label, data=s.name)
                    if s.name == current_name:
                        self.current_node = node
                    self._build_tree(node, children_sessions)
                else:
                    node = parent_node.add_leaf(label, data=s.name)
                    if s.name == current_name:
                        self.current_node = node

        def on_mount(self) -> None:
            if self.current_node is not None:
                # Expand ancestors so the node is visible
                ancestor = self.current_node.parent
                while ancestor is not None:
                    ancestor.expand()
                    ancestor = ancestor.parent
                tree = self.query_one(TextualTree)
                tree.call_after_refresh(tree.move_cursor, self.current_node)

        def on_tree_node_selected(self, event: TextualTree.NodeSelected) -> None:
            if event.node.data is not None:
                self.exit(event.node.data)

    picker = SessionPicker()
    return picker.run(inline=True)


@app.command()
def switch(
    name: Annotated[Optional[str], typer.Argument()] = None,
    tree: Annotated[bool, typer.Option("--tree")] = False,
    pinned: Annotated[bool, typer.Option("--pinned")] = False,
    flagged: Annotated[bool, typer.Option("--flagged")] = False,
    any_filter: Annotated[bool, typer.Option("--any", help="Match any filter (OR) instead of all (AND)")] = False,
    next_session: Annotated[bool, typer.Option("--next")] = False,
    prev_session: Annotated[bool, typer.Option("--prev")] = False,
    markers: Annotated[Optional[bool], typer.Option("--markers/--no-markers", help="Show status markers after session names")] = None,
) -> None:
    """Switch to a session. Outputs JSON to stdout for shell wrapper."""
    pinned_filter = True if pinned else None
    flagged_filter = True if flagged else None
    mode = "any" if any_filter else "all"
    show_markers = _resolve_show_markers(markers)

    if name is None:
        sessions = store.list(status="active", pinned=pinned_filter, flagged=flagged_filter, filter_mode=mode)
        if not sessions:
            typer.echo("No active sessions.", err=True)
            raise typer.Exit(code=1)

        current = _detect_current_session()
        current_name = current.name if current else None

        if next_session or prev_session:
            if current_name is None:
                typer.echo("Could not detect current session.", err=True)
                raise typer.Exit(code=1)
            names = [s.name for s in sessions]
            if current_name not in names:
                typer.echo(f"Current session '{current_name}' not in list.", err=True)
                raise typer.Exit(code=1)
            idx = names.index(current_name)
            offset = 1 if next_session else -1
            name = names[(idx + offset) % len(names)]
        elif tree:
            name = _tree_picker(sessions, current_name, show_markers)
            if name is None:
                raise typer.Exit(code=1)
        else:
            # fzf picker
            lines = []
            current_line_num = None
            for idx, s in enumerate(sessions, 1):
                tmux_status = ""
                if s.tmux_session:
                    tmux_status = "tmux" if tmux.session_exists(s.tmux_session) else "tmux(stopped)"
                m = _session_markers(s, show_markers)
                lines.append(f"{s.name}{m}\t{s.dir}\t{tmux_status}")
                if current_name and s.name == current_name:
                    current_line_num = idx

            fzf_input = "\n".join(lines)
            fzf_cmd = ["fzf", "--delimiter=\t", "--with-nth=1,2,3", "--nth=1", "--no-sort"]
            if current_line_num is not None:
                fzf_cmd.extend(["--bind", f"load:pos({current_line_num})"])
            try:
                result = subprocess.run(
                    fzf_cmd,
                    input=fzf_input,
                    text=True,
                    capture_output=True,
                )
            except FileNotFoundError:
                typer.echo("fzf not found. Install fzf or pass a session name.", err=True)
                raise typer.Exit(code=1)

            if result.returncode != 0:
                raise typer.Exit(code=1)

            name = result.stdout.strip().split("\t")[0].split(" ")[0]

    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    # If session has a tmux session, ensure it exists
    if session.tmux_session:
        if not tmux.session_exists(session.tmux_session):
            typer.echo(f"Recreating tmux session '{session.tmux_session}'...", err=True)
            tmux.create_session(session.tmux_session, session.dir)

    output = {
        "name": session.name,
        "dir": session.dir,
        "tmux_session": session.tmux_session,
    }
    # Only JSON goes to stdout
    sys.stdout.write(json.dumps(output) + "\n")


# ---------------------------------------------------------------------------
# sesh attach-tmux
# ---------------------------------------------------------------------------


@app.command("attach-tmux")
def attach_tmux(
    name: Annotated[Optional[str], typer.Argument()] = None,
    existing: Annotated[Optional[str], typer.Option("--existing", help="Name of an existing tmux session to attach")] = None,
) -> None:
    """Attach a tmux session to an existing sesh. Creates a new tmux session by default, or use --existing to link an already-running one."""
    if name is None:
        current = _detect_current_session()
        if current is None:
            typer.echo("Could not detect current session.", err=True)
            raise typer.Exit(code=1)
        name = current.name

    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    if session.tmux_session and tmux.session_exists(session.tmux_session):
        typer.echo(f"Session '{name}' already has a running tmux session '{session.tmux_session}'.", err=True)
        raise typer.Exit(code=1)

    if existing:
        if not tmux.session_exists(existing):
            typer.echo(f"Tmux session '{existing}' not found.", err=True)
            raise typer.Exit(code=1)
        session.tmux_session = existing
        store.update(session)
        typer.echo(f"Attached existing tmux session '{existing}' to sesh '{name}'.", err=True)
    else:
        tmux_name = session.tmux_session or session.name
        tmux.create_session(tmux_name, session.dir)
        session.tmux_session = tmux_name
        store.update(session)
        typer.echo(f"Created tmux session '{tmux_name}' for sesh '{name}'.", err=True)


# ---------------------------------------------------------------------------
# sesh archive
# ---------------------------------------------------------------------------


@app.command()
def archive(
    name: Annotated[Optional[str], typer.Argument()] = None,
    kill_tmux: Annotated[bool, typer.Option("--kill-tmux")] = False,
) -> None:
    """Archive a session."""
    if name is None:
        current = _detect_current_session()
        if current is None:
            typer.echo("Could not detect current session.", err=True)
            raise typer.Exit(code=1)
        name = current.name

    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    if session.status != "active":
        typer.echo(f"Session '{name}' is not active (status: {session.status}).", err=True)
        raise typer.Exit(code=1)

    # Warn if active children
    active_children = []
    for child_name in session.children:
        try:
            child = store.get(child_name)
            if child.status == "active":
                active_children.append(child_name)
        except KeyError:
            pass
    if active_children:
        typer.echo(f"Warning: session has active children: {', '.join(active_children)}", err=True)

    session.status = "archived"

    if session.tmux_session:
        if kill_tmux:
            if tmux.session_exists(session.tmux_session):
                tmux.kill_session(session.tmux_session)
            session.tmux_session = None
        else:
            archived_name = f"archived/{name}"
            if tmux.session_exists(session.tmux_session):
                tmux.rename_session(session.tmux_session, archived_name)
            session.tmux_session = archived_name

    store.update(session)
    typer.echo(f"Archived session '{name}'.", err=True)


# ---------------------------------------------------------------------------
# sesh restore
# ---------------------------------------------------------------------------


@app.command()
def restore(
    name: Annotated[str, typer.Argument()],
) -> None:
    """Restore an archived session."""
    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    if session.status != "archived":
        typer.echo(f"Session '{name}' is not archived (status: {session.status}).", err=True)
        raise typer.Exit(code=1)

    session.status = "active"

    archived_tmux_name = f"archived/{name}"
    if tmux.session_exists(archived_tmux_name):
        tmux.rename_session(archived_tmux_name, name)
        session.tmux_session = name

    store.update(session)
    typer.echo(f"Restored session '{name}'.", err=True)


# ---------------------------------------------------------------------------
# sesh delete
# ---------------------------------------------------------------------------


@app.command()
def delete(
    name: Annotated[str, typer.Argument()],
    force: Annotated[bool, typer.Option("--force")] = False,
) -> None:
    """Delete a session."""
    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    if (session.status == "active" or session.children) and not force:
        typer.echo(f"Session '{name}' is active or has children. Use --force to delete.", err=True)
        raise typer.Exit(code=1)

    # Kill tmux session if running
    if session.tmux_session and tmux.session_exists(session.tmux_session):
        tmux.kill_session(session.tmux_session)

    # Remove from parent's children list
    if session.parent:
        try:
            parent_session = store.get(session.parent)
            if name in parent_session.children:
                parent_session.children.remove(name)
                store.update(parent_session)
        except KeyError:
            pass

    # Orphan children (set parent to None)
    for child_name in session.children:
        try:
            child = store.get(child_name)
            child.parent = None
            store.update(child)
        except KeyError:
            pass

    store.remove(name)
    typer.echo(f"Deleted session '{name}'.", err=True)


# ---------------------------------------------------------------------------
# AI session helpers
# ---------------------------------------------------------------------------


def _resolve_sesh(name: str | None) -> Session:
    """Resolve a sesh by name or auto-detect from context."""
    if name is not None:
        try:
            return store.get(name)
        except KeyError:
            typer.echo(f"Session '{name}' not found.", err=True)
            raise typer.Exit(code=1)
    current = _detect_current_session()
    if current is None:
        typer.echo("Could not detect current session. Pass SESH_NAME explicitly.", err=True)
        raise typer.Exit(code=1)
    return current


def _auto_ai_name(session: Session, ai_type: str) -> str:
    """Generate next auto-name like claude-1, claude-2, etc."""
    existing = {a.name for a in session.ai_sessions}
    i = 1
    while True:
        candidate = f"{ai_type}-{i}"
        if candidate not in existing:
            return candidate
        i += 1


def _ensure_tmux(session: Session) -> Session:
    """Ensure the sesh has a running tmux session, creating one if needed."""
    if session.tmux_session and tmux.session_exists(session.tmux_session):
        return session
    if not session.tmux_session:
        session.tmux_session = session.name
    if not tmux.session_exists(session.tmux_session):
        tmux.create_session(session.tmux_session, session.dir)
    store.update(session)
    return session


def _resolve_ai_command(ai_type: str, cmd: str | None = None) -> str:
    """Resolve the AI command binary. Priority: --cmd flag > config file > built-in default."""
    if cmd:
        return cmd
    config = load_config(_config_path)
    return config[f"{ai_type}_command"]


def _resolve_ai_session(session: Session, ai_name: str | None) -> AiSession:
    """Resolve an AI session by name, or auto-select if only one exists."""
    if not session.ai_sessions:
        typer.echo(f"No AI sessions in sesh '{session.name}'.", err=True)
        raise typer.Exit(code=1)

    if ai_name is None:
        if len(session.ai_sessions) == 1:
            return session.ai_sessions[0]
        names = ", ".join(a.name for a in session.ai_sessions)
        typer.echo(f"Multiple AI sessions exist. Specify one: {names}", err=True)
        raise typer.Exit(code=1)

    matches = [a for a in session.ai_sessions if a.name == ai_name]
    if not matches:
        typer.echo(f"AI session '{ai_name}' not found in sesh '{session.name}'.", err=True)
        raise typer.Exit(code=1)
    return matches[0]


def _find_claude_jsonl(session_id: str) -> Path | None:
    """Glob ~/.claude/projects/*/<session_id>.jsonl to locate the transcript file."""
    claude_dir = Path.home() / ".claude" / "projects"
    if not claude_dir.exists():
        return None
    matches = list(claude_dir.glob(f"*/{session_id}.jsonl"))
    return matches[0] if matches else None


def _extract_text_from_content(content) -> str:
    """Normalize Claude message content — handles string and array-of-blocks formats."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = []
        for block in content:
            if isinstance(block, dict) and block.get("type") == "text":
                parts.append(block.get("text", ""))
            elif isinstance(block, dict) and block.get("type") == "tool_result":
                # Skip tool results in transcript text
                pass
        return "\n".join(parts) if parts else ""
    return ""


def _parse_claude_jsonl(path: Path) -> list[dict]:
    """Read Claude JSONL, filter user/assistant messages, return normalized list."""
    messages = []
    for line in path.read_text().splitlines():
        if not line.strip():
            continue
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        if entry.get("type") not in ("user", "assistant"):
            continue
        msg = entry.get("message", {})
        role = msg.get("role")
        if role not in ("user", "assistant"):
            continue
        text = _extract_text_from_content(msg.get("content", ""))
        if not text.strip():
            continue
        messages.append({
            "role": role,
            "text": text,
            "timestamp": entry.get("timestamp", ""),
        })
    return messages


def _opencode_export(session: Session, ai_session: AiSession) -> dict:
    """Run opencode export <session_id> with cwd=session.dir, return parsed JSON."""
    command = ai_session.command or _resolve_ai_command("opencode")
    result = subprocess.run(
        [command, "export", ai_session.session_id],
        capture_output=True,
        text=True,
        cwd=session.dir,
    )
    if result.returncode != 0:
        typer.echo(f"opencode export failed: {result.stderr.strip()}", err=True)
        raise typer.Exit(code=1)
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        typer.echo(f"Failed to parse opencode export output.", err=True)
        raise typer.Exit(code=1)


def _parse_opencode_messages(export_data: dict) -> list[dict]:
    """Parse opencode export messages into normalized list."""
    messages = []
    for msg in export_data.get("messages", []):
        role = msg.get("role")
        if role not in ("user", "assistant"):
            continue
        parts = msg.get("parts", [])
        text_parts = []
        for part in parts:
            if isinstance(part, str):
                text_parts.append(part)
            elif isinstance(part, dict) and part.get("type") == "text":
                text_parts.append(part.get("text", ""))
        text = "\n".join(text_parts)
        if not text.strip():
            continue
        messages.append({
            "role": role,
            "text": text,
            "timestamp": msg.get("createdAt", ""),
        })
    return messages


def _create_ai_session(
    session: Session,
    ai_type: str,
    ai_name: str | None = None,
    cmd: str | None = None,
) -> AiSession:
    """Create and launch a new AI session in the sesh's tmux session."""
    if ai_name is None:
        ai_name = _auto_ai_name(session, ai_type)

    # Check for duplicate name
    for a in session.ai_sessions:
        if a.name == ai_name:
            typer.echo(f"AI session '{ai_name}' already exists in sesh '{session.name}'.", err=True)
            raise typer.Exit(code=1)

    command = _resolve_ai_command(ai_type, cmd)
    session = _ensure_tmux(session)

    if ai_type == "claude":
        sid = str(uuid.uuid4())
        ai = AiSession(name=ai_name, type="claude", session_id=sid, command=command)
        session.ai_sessions.append(ai)
        store.update(session)
        tmux_cmd = f"{command} --session-id {sid}"
        tmux.new_window(session.tmux_session, ai_name, tmux_cmd, session.dir)
    elif ai_type == "opencode":
        ai = AiSession(name=ai_name, type="opencode", session_id="pending", command=command)
        session.ai_sessions.append(ai)
        store.update(session)
        wrapper = (
            f'bash -c \''
            f'BEFORE=$({command} session list 2>/dev/null || true); '
            f'{command} {session.dir}; '
            f'AFTER=$({command} session list 2>/dev/null || true); '
            f'NEW_ID=$(comm -13 <(echo "$BEFORE" | sort) <(echo "$AFTER" | sort) | head -1); '
            f'if [ -n "$NEW_ID" ]; then sesh ai _register {session.name} {ai_name} "$NEW_ID"; fi'
            f'\''
        )
        tmux.new_window(session.tmux_session, ai_name, wrapper, session.dir)
    else:
        typer.echo(f"Unknown AI type '{ai_type}'. Use 'claude' or 'opencode'.", err=True)
        raise typer.Exit(code=1)

    typer.echo(f"Created AI session '{ai_name}' ({ai_type}) in sesh '{session.name}'.", err=True)
    return ai


# ---------------------------------------------------------------------------
# sesh ai list
# ---------------------------------------------------------------------------


@ai_app.command("list")
def ai_list(
    sesh_name: Annotated[Optional[str], typer.Argument()] = None,
    json_output: Annotated[bool, typer.Option("--json")] = False,
) -> None:
    """List AI sessions for a sesh."""
    session = _resolve_sesh(sesh_name)

    if not session.ai_sessions:
        typer.echo(f"No AI sessions in sesh '{session.name}'.", err=True)
        return

    if json_output:
        from dataclasses import asdict

        typer.echo(json.dumps([asdict(a) for a in session.ai_sessions], indent=2))
        return

    table = Table()
    table.add_column("NAME")
    table.add_column("TYPE")
    table.add_column("COMMAND")
    table.add_column("SESSION_ID")
    table.add_column("CREATED")

    for a in session.ai_sessions:
        table.add_row(a.name, a.type, a.command or a.type, a.session_id, a.created)

    from rich.console import Console

    console = Console(stderr=True)
    console.print(table)


# ---------------------------------------------------------------------------
# sesh ai new
# ---------------------------------------------------------------------------


@ai_app.command("new")
def ai_new(
    sesh_name: Annotated[Optional[str], typer.Argument()] = None,
    ai_type: Annotated[str, typer.Option("--type")] = "claude",
    ai_name: Annotated[Optional[str], typer.Option("--name")] = None,
    cmd: Annotated[Optional[str], typer.Option("--cmd", help="Override the AI command binary")] = None,
) -> None:
    """Create a new AI session and launch it in tmux."""
    session = _resolve_sesh(sesh_name)
    _create_ai_session(session, ai_type=ai_type, ai_name=ai_name, cmd=cmd)


# ---------------------------------------------------------------------------
# sesh ai resume
# ---------------------------------------------------------------------------


@ai_app.command("resume")
def ai_resume(
    ai_name: Annotated[Optional[str], typer.Argument()] = None,
    sesh_name: Annotated[Optional[str], typer.Argument()] = None,
) -> None:
    """Resume an existing AI session in tmux."""
    session = _resolve_sesh(sesh_name)
    ai = _resolve_ai_session(session, ai_name)
    session = _ensure_tmux(session)

    # Use stored command, fall back to config/defaults for old sessions
    command = ai.command or _resolve_ai_command(ai.type)

    if ai.type == "claude":
        tmux_cmd = f"{command} --resume {ai.session_id}"
    elif ai.type == "opencode":
        if ai.session_id == "pending":
            typer.echo(f"AI session '{ai.name}' has no session ID yet (pending).", err=True)
            raise typer.Exit(code=1)
        tmux_cmd = f"{command} -s {ai.session_id} {session.dir}"
    else:
        typer.echo(f"Unknown AI type '{ai.type}'.", err=True)
        raise typer.Exit(code=1)

    tmux.new_window(session.tmux_session, ai.name, tmux_cmd, session.dir)
    typer.echo(f"Resumed AI session '{ai.name}' in sesh '{session.name}'.", err=True)


# ---------------------------------------------------------------------------
# sesh ai enter
# ---------------------------------------------------------------------------


@ai_app.command("enter")
def ai_enter(
    ai_name: Annotated[Optional[str], typer.Argument()] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh")] = None,
    cmd: Annotated[Optional[str], typer.Option("--cmd", help="Override the AI command binary")] = None,
) -> None:
    """Enter an AI session interactively in the current terminal."""
    session = _resolve_sesh(sesh_name)

    if not session.ai_sessions:
        typer.echo(f"No AI sessions in sesh '{session.name}'.", err=True)
        raise typer.Exit(code=1)

    if ai_name is not None:
        ai = _resolve_ai_session(session, ai_name)
    elif len(session.ai_sessions) == 1:
        ai = session.ai_sessions[0]
    else:
        # fzf picker
        lines = []
        for a in session.ai_sessions:
            lines.append(f"{a.name}\t{a.type}\t{a.session_id}")
        fzf_input = "\n".join(lines)
        try:
            result = subprocess.run(
                ["fzf", "--delimiter=\t", "--with-nth=1,2", "--nth=1"],
                input=fzf_input,
                text=True,
                capture_output=True,
            )
        except FileNotFoundError:
            typer.echo("fzf not found. Specify an AI session name.", err=True)
            raise typer.Exit(code=1)
        if result.returncode != 0:
            raise typer.Exit(code=1)
        ai_name = result.stdout.strip().split("\t")[0]
        ai = _resolve_ai_session(session, ai_name)

    command = cmd or ai.command or _resolve_ai_command(ai.type)

    if ai.type == "claude":
        shell_cmd = f"{command} --resume {ai.session_id}"
    elif ai.type == "opencode":
        if ai.session_id == "pending":
            typer.echo(f"AI session '{ai.name}' has no session ID yet (pending).", err=True)
            raise typer.Exit(code=1)
        shell_cmd = f"{command} -s {ai.session_id} {session.dir}"
    else:
        typer.echo(f"Unknown AI type '{ai.type}'.", err=True)
        raise typer.Exit(code=1)

    os.execvp("sh", ["sh", "-c", shell_cmd])


# ---------------------------------------------------------------------------
# sesh ai add
# ---------------------------------------------------------------------------


@ai_app.command("add")
def ai_add(
    sesh_name: Annotated[Optional[str], typer.Argument()] = None,
    ai_type: Annotated[str, typer.Option("--type")] = "claude",
    ai_name: Annotated[str, typer.Option("--name")] = ...,
    session_id: Annotated[str, typer.Option("--id")] = ...,
) -> None:
    """Manually register an existing AI session ID."""
    session = _resolve_sesh(sesh_name)

    for a in session.ai_sessions:
        if a.name == ai_name:
            typer.echo(f"AI session '{ai_name}' already exists in sesh '{session.name}'.", err=True)
            raise typer.Exit(code=1)

    ai = AiSession(name=ai_name, type=ai_type, session_id=session_id)
    session.ai_sessions.append(ai)
    store.update(session)
    typer.echo(f"Added AI session '{ai_name}' ({ai_type}) to sesh '{session.name}'.", err=True)


# ---------------------------------------------------------------------------
# sesh ai remove
# ---------------------------------------------------------------------------


@ai_app.command("remove")
def ai_remove(
    ai_name: Annotated[str, typer.Argument()],
    sesh_name: Annotated[Optional[str], typer.Argument()] = None,
) -> None:
    """Remove an AI session record from a sesh."""
    session = _resolve_sesh(sesh_name)

    original_len = len(session.ai_sessions)
    session.ai_sessions = [a for a in session.ai_sessions if a.name != ai_name]

    if len(session.ai_sessions) == original_len:
        typer.echo(f"AI session '{ai_name}' not found in sesh '{session.name}'.", err=True)
        raise typer.Exit(code=1)

    store.update(session)
    typer.echo(f"Removed AI session '{ai_name}' from sesh '{session.name}'.", err=True)


# ---------------------------------------------------------------------------
# sesh ai _register (internal, hidden)
# ---------------------------------------------------------------------------


@ai_app.command("_register", hidden=True)
def ai_register(
    sesh_name: Annotated[str, typer.Argument()],
    ai_name: Annotated[str, typer.Argument()],
    session_id: Annotated[str, typer.Argument()],
) -> None:
    """Internal: update a pending AI session ID."""
    try:
        session = store.get(sesh_name)
    except KeyError:
        typer.echo(f"Session '{sesh_name}' not found.", err=True)
        raise typer.Exit(code=1)

    for ai in session.ai_sessions:
        if ai.name == ai_name:
            ai.session_id = session_id
            store.update(session)
            typer.echo(f"Registered session ID for '{ai_name}'.", err=True)
            return

    typer.echo(f"AI session '{ai_name}' not found in sesh '{sesh_name}'.", err=True)
    raise typer.Exit(code=1)


# ---------------------------------------------------------------------------
# sesh ai transcript
# ---------------------------------------------------------------------------


def _get_transcript(session: Session, ai: AiSession) -> list[dict]:
    """Get parsed transcript messages for an AI session."""
    if ai.type == "claude":
        path = _find_claude_jsonl(ai.session_id)
        if path is None:
            typer.echo(f"Could not find Claude JSONL for session '{ai.session_id}'.", err=True)
            raise typer.Exit(code=1)
        return _parse_claude_jsonl(path)
    elif ai.type == "opencode":
        export_data = _opencode_export(session, ai)
        return _parse_opencode_messages(export_data)
    else:
        typer.echo(f"Unknown AI type '{ai.type}'.", err=True)
        raise typer.Exit(code=1)


@ai_app.command("transcript")
def ai_transcript(
    ai_name: Annotated[Optional[str], typer.Argument()] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh")] = None,
) -> None:
    """Get the full chat transcript for an AI session. Outputs JSON to stdout."""
    session = _resolve_sesh(sesh_name)
    ai = _resolve_ai_session(session, ai_name)
    messages = _get_transcript(session, ai)

    output = {
        "sesh": session.name,
        "ai_session": ai.name,
        "ai_type": ai.type,
        "session_id": ai.session_id,
        "message_count": len(messages),
        "total_messages": len(messages),
        "messages": messages,
    }
    sys.stdout.write(json.dumps(output, indent=2) + "\n")


# ---------------------------------------------------------------------------
# sesh ai transcript-head / transcript-tail
# ---------------------------------------------------------------------------


@ai_app.command("transcript-head")
def ai_transcript_head(
    ai_name: Annotated[Optional[str], typer.Argument()] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh")] = None,
    count: Annotated[int, typer.Option("-n", "--count", help="Number of messages to show")] = 10,
    offset: Annotated[int, typer.Option("--offset", help="Skip this many messages from the start")] = 0,
) -> None:
    """Get the first N messages from an AI transcript. Outputs JSON to stdout."""
    session = _resolve_sesh(sesh_name)
    ai = _resolve_ai_session(session, ai_name)
    messages = _get_transcript(session, ai)
    total = len(messages)
    sliced = messages[offset:offset + count]

    output = {
        "sesh": session.name,
        "ai_session": ai.name,
        "ai_type": ai.type,
        "session_id": ai.session_id,
        "message_count": len(sliced),
        "total_messages": total,
        "messages": sliced,
    }
    sys.stdout.write(json.dumps(output, indent=2) + "\n")


@ai_app.command("transcript-tail")
def ai_transcript_tail(
    ai_name: Annotated[Optional[str], typer.Argument()] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh")] = None,
    count: Annotated[int, typer.Option("-n", "--count", help="Number of messages to show")] = 10,
    offset: Annotated[int, typer.Option("--offset", help="Skip this many messages from the end")] = 0,
) -> None:
    """Get the last N messages from an AI transcript. Outputs JSON to stdout."""
    session = _resolve_sesh(sesh_name)
    ai = _resolve_ai_session(session, ai_name)
    messages = _get_transcript(session, ai)
    total = len(messages)

    if offset > 0:
        end = max(total - offset, 0)
        start = max(end - count, 0)
        sliced = messages[start:end]
    else:
        sliced = messages[-count:] if total > count else messages

    output = {
        "sesh": session.name,
        "ai_session": ai.name,
        "ai_type": ai.type,
        "session_id": ai.session_id,
        "message_count": len(sliced),
        "total_messages": total,
        "messages": sliced,
    }
    sys.stdout.write(json.dumps(output, indent=2) + "\n")


# ---------------------------------------------------------------------------
# sesh ai last-message
# ---------------------------------------------------------------------------


@ai_app.command("last-message")
def ai_last_message(
    ai_name: Annotated[Optional[str], typer.Argument()] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh")] = None,
    role: Annotated[Optional[str], typer.Option("--role", help="Filter by role: user or assistant")] = None,
) -> None:
    """Get the last message from an AI session. Outputs JSON to stdout."""
    session = _resolve_sesh(sesh_name)
    ai = _resolve_ai_session(session, ai_name)
    messages = _get_transcript(session, ai)

    if role:
        messages = [m for m in messages if m["role"] == role]

    if not messages:
        typer.echo("No messages found.", err=True)
        raise typer.Exit(code=1)

    last = messages[-1]
    output = {
        "sesh": session.name,
        "ai_session": ai.name,
        "ai_type": ai.type,
        "role": last["role"],
        "text": last["text"],
        "timestamp": last["timestamp"],
    }
    sys.stdout.write(json.dumps(output, indent=2) + "\n")


# ---------------------------------------------------------------------------
# sesh ai send
# ---------------------------------------------------------------------------


@ai_app.command("send")
def ai_send(
    message: Annotated[str, typer.Argument()],
    name: Annotated[Optional[str], typer.Option("--name", help="AI session name")] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh")] = None,
) -> None:
    """Send a message to an AI session and return the response. Outputs JSON to stdout."""
    session = _resolve_sesh(sesh_name)
    ai = _resolve_ai_session(session, name)

    command = ai.command or _resolve_ai_command(ai.type)

    if ai.type == "claude":
        result = subprocess.run(
            [command, "-p", message, "--resume", ai.session_id, "--output-format", "json"],
            capture_output=True,
            text=True,
            cwd=session.dir,
        )
    elif ai.type == "opencode":
        result = subprocess.run(
            [command, "run", "-s", ai.session_id, message],
            capture_output=True,
            text=True,
            cwd=session.dir,
        )
    else:
        typer.echo(f"Unknown AI type '{ai.type}'.", err=True)
        raise typer.Exit(code=1)

    if result.returncode != 0:
        typer.echo(f"AI command failed: {result.stderr.strip()}", err=True)
        raise typer.Exit(code=1)

    # Try to parse as JSON, otherwise wrap raw output
    try:
        response_data = json.loads(result.stdout)
    except json.JSONDecodeError:
        response_data = result.stdout.strip()

    output = {
        "sesh": session.name,
        "ai_session": ai.name,
        "ai_type": ai.type,
        "response": response_data,
    }
    sys.stdout.write(json.dumps(output, indent=2) + "\n")
