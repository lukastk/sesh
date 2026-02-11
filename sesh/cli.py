from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Annotated, Optional

import typer
from rich.table import Table
from rich.tree import Tree

from sesh import tmux
from sesh.store import Session, SessionStore

app = typer.Typer(add_completion=False)
store = SessionStore()


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

    # 2. Try repoyard which --json on $PWD → match by repoyard_index_name
    cwd = os.getcwd()
    try:
        result = subprocess.run(
            ["repoyard", "which", "--json", "--path", cwd],
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            rd_data = json.loads(result.stdout)
            index_name = rd_data.get("index_name")
            if index_name:
                for s in sessions.values():
                    if s.repoyard_index_name == index_name:
                        return s
    except (FileNotFoundError, json.JSONDecodeError):
        pass

    # 3. Fall back to matching by dir == $PWD
    for s in sessions.values():
        if s.dir == cwd:
            return s

    return None


def _repoyard_detect(dir_path: str) -> tuple[str | None, str | None]:
    """Run repoyard which --json on a path. Returns (name, index_name) or (None, None)."""
    try:
        result = subprocess.run(
            ["repoyard", "which", "--json", "--path", dir_path],
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
) -> None:
    """Create a new session."""
    dir_path = str((dir or Path.cwd()).resolve())

    # Auto-detect name if not provided
    if name is None:
        rd_name, _ = _repoyard_detect(dir_path)
        name = rd_name or Path(dir_path).name

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

    # Detect repoyard index name
    _, index_name = _repoyard_detect(dir_path)

    session = Session(
        name=name,
        dir=dir_path,
        parent=parent,
        repoyard_index_name=index_name,
        tags=tag or [],
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
            "created": session.created,
            "parent": session.parent,
            "children": session.children,
            "repoyard_index_name": session.repoyard_index_name,
            "tags": session.tags,
        }
        typer.echo(json.dumps(data, indent=2))
    else:
        typer.echo(f"Name:      {session.name}")
        typer.echo(f"Dir:       {session.dir}")
        typer.echo(f"Status:    {session.status}")
        typer.echo(f"Created:   {session.created}")
        if session.tmux_session:
            status_str = "running" if tmux_live else "not running"
            typer.echo(f"Tmux:      {session.tmux_session} ({status_str})")
        if session.parent:
            typer.echo(f"Parent:    {session.parent}")
        if session.children:
            typer.echo(f"Children:  {', '.join(session.children)}")
        if session.repoyard_index_name:
            typer.echo(f"Repoyard:  {session.repoyard_index_name}")
        if session.tags:
            typer.echo(f"Tags:      {', '.join(session.tags)}")


# ---------------------------------------------------------------------------
# sesh list
# ---------------------------------------------------------------------------


@app.command("list")
def list_sessions(
    all: Annotated[bool, typer.Option("--all")] = False,
    archived: Annotated[bool, typer.Option("--archived")] = False,
    tree: Annotated[bool, typer.Option("--tree")] = False,
    json_output: Annotated[bool, typer.Option("--json")] = False,
) -> None:
    """List sessions."""
    if all:
        status_filter = None
    elif archived:
        status_filter = "archived"
    else:
        status_filter = "active"

    sessions = store.list(status=status_filter)

    if not sessions:
        typer.echo("No sessions found.", err=True)
        return

    # Detect current session
    current = _detect_current_session()
    current_name = current.name if current else None

    if json_output:
        from dataclasses import asdict

        typer.echo(json.dumps([asdict(s) for s in sessions], indent=2))
        return

    if tree:
        _print_tree(sessions, current_name)
        return

    # Default: table
    _print_table(sessions, current_name)


def _print_table(sessions: list[Session], current_name: str | None) -> None:
    table = Table()
    table.add_column("NAME")
    table.add_column("DIR")
    table.add_column("TMUX")
    table.add_column("STATUS")

    for s in sessions:
        name_display = f"* {s.name}" if s.name == current_name else s.name
        tmux_status = ""
        if s.tmux_session:
            tmux_status = "running" if tmux.session_exists(s.tmux_session) else "stopped"
        table.add_row(name_display, s.dir, tmux_status, s.status)

    from rich.console import Console

    console = Console(stderr=True)
    console.print(table)


def _print_tree(sessions: list[Session], current_name: str | None) -> None:
    session_map = {s.name: s for s in sessions}
    roots = [s for s in sessions if s.parent is None or s.parent not in session_map]

    rich_tree = Tree("Sessions")

    def _add_children(parent_node: Tree, parent_session: Session) -> None:
        for child_name in parent_session.children:
            if child_name in session_map:
                child = session_map[child_name]
                label = f"* {child.name}" if child.name == current_name else child.name
                child_node = parent_node.add(f"{label} ({child.status})")
                _add_children(child_node, child)

    for s in roots:
        label = f"* {s.name}" if s.name == current_name else s.name
        node = rich_tree.add(f"{label} ({s.status})")
        _add_children(node, s)

    from rich.console import Console

    console = Console(stderr=True)
    console.print(rich_tree)


# ---------------------------------------------------------------------------
# sesh switch
# ---------------------------------------------------------------------------


@app.command()
def switch(
    name: Annotated[Optional[str], typer.Argument()] = None,
) -> None:
    """Switch to a session. Outputs JSON to stdout for shell wrapper."""
    if name is None:
        # fzf picker
        sessions = store.list(status="active")
        if not sessions:
            typer.echo("No active sessions.", err=True)
            raise typer.Exit(code=1)

        lines = []
        for s in sessions:
            tmux_status = ""
            if s.tmux_session:
                tmux_status = "tmux" if tmux.session_exists(s.tmux_session) else "tmux(stopped)"
            lines.append(f"{s.name}\t{s.dir}\t{tmux_status}")

        fzf_input = "\n".join(lines)
        try:
            result = subprocess.run(
                ["fzf", "--delimiter=\t", "--with-nth=1,2,3", "--no-sort"],
                input=fzf_input,
                text=True,
                capture_output=True,
            )
        except FileNotFoundError:
            typer.echo("fzf not found. Install fzf or pass a session name.", err=True)
            raise typer.Exit(code=1)

        if result.returncode != 0:
            raise typer.Exit(code=1)

        name = result.stdout.strip().split("\t")[0]

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
