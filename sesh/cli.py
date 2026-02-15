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
group_app = typer.Typer(add_completion=False, help="Manage session groups.")
app.add_typer(group_app, name="group")
parent_app = typer.Typer(add_completion=False, help="Manage session parents.")
app.add_typer(parent_app, name="parent")
boxyard_app = typer.Typer(add_completion=False, help="Per-session boxyard integration settings.")
app.add_typer(boxyard_app, name="boxyard")
store = SessionStore()
_config_path: Path | None = None
_boxyard_fast = None
_boxyard_fast_loaded = False


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


def _get_boxyard_fast():
    """Lazy-load BoxyardFast instance. Returns None if boxyard not available."""
    global _boxyard_fast, _boxyard_fast_loaded
    if not _boxyard_fast_loaded:
        _boxyard_fast_loaded = True
        try:
            from boxyard._fast import BoxyardFast
            _boxyard_fast = BoxyardFast.from_file()
        except Exception:
            _boxyard_fast = None
    return _boxyard_fast


def _boxyard_detect(dir_path: str) -> tuple[str | None, str | None]:
    """Detect box name and index_name from a directory path."""
    fast = _get_boxyard_fast()
    if fast is None:
        return None, None
    result = fast.which(dir_path)
    if result is None:
        return None, None
    return result.get("name"), result.get("index_name")


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

    # 2. Try boxyard on $PWD → match by boxyard_index_name
    try:
        cwd = os.getcwd()
    except (FileNotFoundError, OSError):
        return None

    fast = _get_boxyard_fast()
    if fast is not None:
        by_result = fast.which(cwd)
        if by_result is not None:
            index_name = by_result.get("index_name")
            if index_name:
                for s in sessions.values():
                    if s.boxyard_index_name == index_name:
                        return s

    # 3. Fall back to matching by dir == $PWD
    for s in sessions.values():
        if s.dir == cwd:
            return s

    return None


def _enrich_sessions(sessions: list[Session]) -> list[Session]:
    """Merge boxyard groups and parents into sessions (in-memory only)."""
    config = load_config(_config_path)
    if not config.get("boxyard_integration", True):
        return sessions
    fast = _get_boxyard_fast()
    if fast is None:
        return sessions

    # Build reverse map: boxyard_index_name → sesh session name
    by_index = {s.boxyard_index_name: s.name for s in sessions if s.boxyard_index_name}

    for s in sessions:
        # Per-session override: skip if explicitly disabled
        if s.boxyard_integration is False:
            continue
        if not s.boxyard_index_name:
            continue
        # Extract box_id from index_name (format: box_id__name)
        parts = s.boxyard_index_name.split("__", 1)
        if not parts:
            continue
        box_id = parts[0]

        # Merge groups
        by_groups = fast.groups_of(box_id)
        for g in by_groups:
            if g not in s.groups:
                s.groups.append(g)

        # Merge parents
        by_parents = fast.parents_of(box_id)
        for p in by_parents:
            parent_index = p.get("index_name")
            if parent_index and parent_index in by_index:
                parent_name = by_index[parent_index]
                if parent_name not in s.parents:
                    s.parents.append(parent_name)

    return sessions


# ---------------------------------------------------------------------------
# sesh new
# ---------------------------------------------------------------------------


@app.command()
def new(
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected from dir if omitted)")] = None,
    dir: Annotated[Optional[Path], typer.Option("--dir", help="Working directory for the session")] = None,
    tmux_flag: Annotated[bool, typer.Option("--tmux", help="Create a tmux session")] = False,
    parent: Annotated[Optional[list[str]], typer.Option("--parent", help="Parent session (repeatable)")] = None,
    group: Annotated[Optional[list[str]], typer.Option("--group", help="Add to group (repeatable)")] = None,
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

    # Validate parents
    parents_list = parent or []
    for p in parents_list:
        if p not in sessions:
            typer.echo(f"Parent session '{p}' not found.", err=True)
            raise typer.Exit(code=1)

    # Detect boxyard index name
    _, index_name = _boxyard_detect(dir_path)

    session = Session(
        name=name,
        dir=dir_path,
        parents=parents_list,
        boxyard_index_name=index_name,
        groups=group or [],
        pinned=pin,
        flagged=flag,
    )
    store.add(session)

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


@app.command("sessions-file")
def sessions_file() -> None:
    """Print the path to the sessions file."""
    typer.echo(str(store.sessions_file))


@app.command()
def info(
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
    json_output: Annotated[bool, typer.Option("--json", help="Output as JSON")] = False,
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

    # Save sesh-owned values before enrichment
    sesh_groups = list(session.groups)
    sesh_parents = list(session.parents)

    # Enrich with boxyard data
    _enrich_sessions([session])

    # Compute children dynamically
    children = store.children_of(name)

    # Live-check tmux
    tmux_live = False
    if session.tmux_session:
        tmux_live = tmux.session_exists(session.tmux_session)

    # Resolve boxyard integration status
    config = load_config(_config_path)
    global_by = config.get("boxyard_integration", True)
    if session.boxyard_integration is not None:
        by_enabled = session.boxyard_integration
        by_source = "override"
    else:
        by_enabled = global_by
        by_source = "global"
    by_status = "enabled" if by_enabled else "disabled"

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
            "parents": session.parents,
            "children": children,
            "boxyard_index_name": session.boxyard_index_name,
            "boxyard_integration": f"{by_status} ({by_source})",
            "groups": session.groups,
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
        if session.parents:
            labeled = []
            for p in session.parents:
                labeled.append(f"{p}*" if p not in sesh_parents else p)
            typer.echo(f"Parents:   {', '.join(labeled)}")
        if children:
            typer.echo(f"Children:  {', '.join(children)}")
        if session.boxyard_index_name:
            typer.echo(f"Boxyard:   {session.boxyard_index_name}")
        typer.echo(f"Boxyard:   {by_status} ({by_source})")
        if session.groups:
            labeled = []
            for g in session.groups:
                labeled.append(f"{g}*" if g not in sesh_groups else g)
            typer.echo(f"Groups:    {', '.join(labeled)}")


# ---------------------------------------------------------------------------
# sesh pin / sesh unpin
# ---------------------------------------------------------------------------


@app.command()
def pin(
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
    toggle: Annotated[bool, typer.Option("--toggle", help="Toggle pin state")] = False,
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
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
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
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
    toggle: Annotated[bool, typer.Option("--toggle", help="Toggle flag state")] = False,
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
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
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
    all: Annotated[bool, typer.Option("--all", help="Show all sessions including archived")] = False,
    archived: Annotated[bool, typer.Option("--archived", help="Show only archived sessions")] = False,
    pinned: Annotated[bool, typer.Option("--pinned", help="Filter to pinned sessions")] = False,
    flagged: Annotated[bool, typer.Option("--flagged", help="Filter to flagged sessions")] = False,
    group: Annotated[Optional[list[str]], typer.Option("--group", help="Filter by group (repeatable)")] = None,
    any_filter: Annotated[bool, typer.Option("--any", help="Match any filter (OR) instead of all (AND)")] = False,
    tree: Annotated[bool, typer.Option("--tree", help="Show as parent-child tree")] = False,
    groups_tree: Annotated[bool, typer.Option("--tree-groups", help="Show as tree grouped by groups")] = False,
    json_output: Annotated[bool, typer.Option("--json", help="Output as JSON")] = False,
    show_groups: Annotated[bool, typer.Option("--show-groups", help="Show groups column in table view")] = False,
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

    # Enrich before filtering so boxyard groups are available for --group filter
    sessions = store.list(status=status_filter, pinned=pinned_filter, flagged=flagged_filter, filter_mode=mode)
    _enrich_sessions(sessions)

    # Apply group filter after enrichment
    if group:
        group_set = set(group)
        if mode == "any" and (pinned or flagged):
            # Groups participate in the OR filter alongside pinned/flagged
            # Re-fetch without group filter, then apply combined OR
            pass  # Already filtered by pinned/flagged with 'any', now add group match
        sessions = [s for s in sessions if bool(set(s.groups) & group_set)]

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

    if tree or groups_tree:
        if groups_tree:
            _print_group_tree(sessions, current_name, show_markers)
        else:
            _print_tree(sessions, current_name, show_markers)
        return

    # Default: table
    _print_table(sessions, current_name, show_markers, show_groups=show_groups)


def _print_table(sessions: list[Session], current_name: str | None, show_markers: bool = True, show_groups: bool = False) -> None:
    table = Table()
    table.add_column("NAME")
    table.add_column("DIR")
    table.add_column("TMUX")
    table.add_column("STATUS")
    if show_groups:
        table.add_column("GROUPS")

    for s in sessions:
        name_display = f"* {s.name}" if s.name == current_name else s.name
        name_display += _session_markers(s, show_markers)
        tmux_status = ""
        if s.tmux_session:
            tmux_status = "running" if tmux.session_exists(s.tmux_session) else "stopped"
        row = [name_display, s.dir, tmux_status, s.status]
        if show_groups:
            row.append(", ".join(s.groups) if s.groups else "")
        table.add_row(*row)

    from rich.console import Console

    console = Console(stderr=True)
    console.print(table)


def _print_tree(sessions: list[Session], current_name: str | None, show_markers: bool = True) -> None:
    session_map = {s.name: s for s in sessions}
    # Roots: sessions with no parents, or whose parents are all outside this session list
    roots = [s for s in sessions if not s.parents or not any(p in session_map for p in s.parents)]

    # Build children map (a session with multiple parents appears under each)
    children_map: dict[str, list[str]] = {s.name: [] for s in sessions}
    for s in sessions:
        for p in s.parents:
            if p in children_map:
                children_map[p].append(s.name)

    rich_tree = Tree("Sessions")

    def _add_children(parent_node: Tree, parent_name: str, visited: set[str]) -> None:
        for child_name in children_map.get(parent_name, []):
            if child_name in session_map:
                child = session_map[child_name]
                label = f"* {child.name}" if child.name == current_name else child.name
                label += _session_markers(child, show_markers)
                child_node = parent_node.add(f"{label} ({child.status})")
                if child_name not in visited:
                    visited.add(child_name)
                    _add_children(child_node, child_name, visited)

    for s in roots:
        label = f"* {s.name}" if s.name == current_name else s.name
        label += _session_markers(s, show_markers)
        node = rich_tree.add(f"{label} ({s.status})")
        _add_children(node, s.name, {s.name})

    from rich.console import Console

    console = Console(stderr=True)
    console.print(rich_tree)


def _print_group_tree(sessions: list[Session], current_name: str | None, show_markers: bool = True) -> None:
    """Print sessions grouped by group name as a tree."""
    # Collect all groups and their sessions
    group_sessions: dict[str, list[Session]] = {}
    ungrouped: list[Session] = []
    for s in sessions:
        if s.groups:
            for g in s.groups:
                group_sessions.setdefault(g, []).append(s)
        else:
            ungrouped.append(s)

    rich_tree = Tree("Sessions")

    for g in sorted(group_sessions):
        group_node = rich_tree.add(f"[bold]{g}[/bold]")
        for s in group_sessions[g]:
            label = f"* {s.name}" if s.name == current_name else s.name
            label += _session_markers(s, show_markers)
            group_node.add(f"{label} ({s.status})")

    if ungrouped:
        other_node = rich_tree.add("[dim](ungrouped)[/dim]")
        for s in ungrouped:
            label = f"* {s.name}" if s.name == current_name else s.name
            label += _session_markers(s, show_markers)
            other_node.add(f"{label} ({s.status})")

    from rich.console import Console

    console = Console(stderr=True)
    console.print(rich_tree)


# ---------------------------------------------------------------------------
# sesh switch
# ---------------------------------------------------------------------------


def _tree_picker(sessions: list[Session], current_name: str | None, show_markers: bool = True, groups_mode: bool = False) -> str | None:
    """Show an interactive tree picker using Textual inline mode. Returns selected session name or None."""
    from textual.app import App, ComposeResult
    from textual.widgets import Tree as TextualTree

    config = load_config(_config_path)
    max_h = config.get("tree_max_height", "90vh")

    session_map = {s.name: s for s in sessions}

    # Build children map for parent-child tree
    children_map: dict[str, list[str]] = {s.name: [] for s in sessions}
    for s in sessions:
        for p in s.parents:
            if p in children_map:
                children_map[p].append(s.name)

    roots = [s for s in sessions if not s.parents or not any(p in session_map for p in s.parents)]

    class SessionPicker(App[str]):
        CSS = f"""
        Screen {{
            &:inline {{ height: auto; max-height: {max_h}; }}
        }}
        Tree {{ height: auto; max-height: {max_h}; }}
        """
        INLINE_PADDING = 0
        current_node = None

        def compose(self) -> ComposeResult:
            tree: TextualTree[str] = TextualTree("Sessions")
            tree.show_root = False
            if groups_mode:
                self._build_group_tree(tree.root)
            else:
                self._build_tree(tree.root, roots, set())
            yield tree

        def _build_tree(self, parent_node, parent_sessions, visited):
            for s in parent_sessions:
                label = f"* {s.name}" if s.name == current_name else s.name
                label += _session_markers(s, show_markers)
                child_names = children_map.get(s.name, [])
                child_sessions = [session_map[c] for c in child_names if c in session_map]
                if child_sessions and s.name not in visited:
                    node = parent_node.add(label, data=s.name)
                    if s.name == current_name:
                        self.current_node = node
                    self._build_tree(node, child_sessions, visited | {s.name})
                else:
                    node = parent_node.add_leaf(label, data=s.name)
                    if s.name == current_name:
                        self.current_node = node

        def _build_group_tree(self, root_node):
            group_sessions: dict[str, list[Session]] = {}
            ungrouped: list[Session] = []
            for s in sessions:
                if s.groups:
                    for g in s.groups:
                        group_sessions.setdefault(g, []).append(s)
                else:
                    ungrouped.append(s)
            for g in sorted(group_sessions):
                group_node = root_node.add(g)
                for s in group_sessions[g]:
                    label = f"* {s.name}" if s.name == current_name else s.name
                    label += _session_markers(s, show_markers)
                    node = group_node.add_leaf(label, data=s.name)
                    if s.name == current_name:
                        self.current_node = node
            if ungrouped:
                other_node = root_node.add("(ungrouped)")
                for s in ungrouped:
                    label = f"* {s.name}" if s.name == current_name else s.name
                    label += _session_markers(s, show_markers)
                    node = other_node.add_leaf(label, data=s.name)
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
    name: Annotated[Optional[str], typer.Argument(help="Session name (interactive picker if omitted)")] = None,
    all: Annotated[bool, typer.Option("--all", help="Show all sessions including archived")] = False,
    archived: Annotated[bool, typer.Option("--archived", help="Show only archived sessions")] = False,
    tree: Annotated[bool, typer.Option("--tree", help="Use tree picker")] = False,
    groups_tree: Annotated[bool, typer.Option("--tree-groups", help="Use tree picker grouped by groups")] = False,
    group: Annotated[Optional[list[str]], typer.Option("--group", help="Filter by group (repeatable)")] = None,
    pinned: Annotated[bool, typer.Option("--pinned", help="Filter to pinned sessions")] = False,
    flagged: Annotated[bool, typer.Option("--flagged", help="Filter to flagged sessions")] = False,
    any_filter: Annotated[bool, typer.Option("--any", help="Match any filter (OR) instead of all (AND)")] = False,
    next_session: Annotated[bool, typer.Option("--next", help="Switch to next session in list")] = False,
    prev_session: Annotated[bool, typer.Option("--prev", help="Switch to previous session in list")] = False,
    markers: Annotated[Optional[bool], typer.Option("--markers/--no-markers", help="Show status markers after session names")] = None,
) -> None:
    """Switch to a session. Outputs JSON to stdout for shell wrapper."""
    if all:
        status_filter = None
    elif archived:
        status_filter = "archived"
    else:
        status_filter = "active"

    pinned_filter = True if pinned else None
    flagged_filter = True if flagged else None
    mode = "any" if any_filter else "all"
    show_markers = _resolve_show_markers(markers)

    if name is None:
        sessions = store.list(status=status_filter, pinned=pinned_filter, flagged=flagged_filter, filter_mode=mode)
        _enrich_sessions(sessions)

        # Apply group filter after enrichment
        if group:
            group_set = set(group)
            sessions = [s for s in sessions if bool(set(s.groups) & group_set)]

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
                # Current session not in filtered list — pick first or last
                name = names[0] if next_session else names[-1]
            else:
                idx = names.index(current_name)
                offset = 1 if next_session else -1
                name = names[(idx + offset) % len(names)]
        elif tree or groups_tree:
            name = _tree_picker(sessions, current_name, show_markers, groups_mode=groups_tree)
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
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
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
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
    kill_tmux: Annotated[bool, typer.Option("--kill-tmux", help="Kill the tmux session instead of renaming it")] = False,
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

    # Warn if active children (computed dynamically)
    children = store.children_of(name)
    active_children = []
    for child_name in children:
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
    name: Annotated[str, typer.Argument(help="Session name")],
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
    name: Annotated[str, typer.Argument(help="Session name")],
    force: Annotated[bool, typer.Option("--force", help="Force delete active sessions or sessions with children")] = False,
) -> None:
    """Delete a session."""
    try:
        session = store.get(name)
    except KeyError:
        typer.echo(f"Session '{name}' not found.", err=True)
        raise typer.Exit(code=1)

    children = store.children_of(name)
    if (session.status == "active" or children) and not force:
        typer.echo(f"Session '{name}' is active or has children. Use --force to delete.", err=True)
        raise typer.Exit(code=1)

    # Kill tmux session if running
    if session.tmux_session and tmux.session_exists(session.tmux_session):
        tmux.kill_session(session.tmux_session)

    # Orphan children (remove this session from their parents list)
    for child_name in children:
        try:
            child = store.get(child_name)
            if name in child.parents:
                child.parents.remove(name)
                store.update(child)
        except KeyError:
            pass

    store.remove(name)
    typer.echo(f"Deleted session '{name}'.", err=True)


# ---------------------------------------------------------------------------
# sesh group add / remove / list
# ---------------------------------------------------------------------------


@group_app.command("add")
def group_add(
    group: Annotated[str, typer.Argument(help="Group name")],
    name: Annotated[Optional[str], typer.Option("--name", "-n", help="Session name (auto-detected if omitted)")] = None,
) -> None:
    """Add a group to a session."""
    session = _resolve_sesh(name)
    if group in session.groups:
        typer.echo(f"Session '{session.name}' is already in group '{group}'.", err=True)
        raise typer.Exit(code=1)
    session.groups.append(group)
    store.update(session)
    typer.echo(f"Added group '{group}' to session '{session.name}'.", err=True)


@group_app.command("remove")
def group_remove(
    group: Annotated[str, typer.Argument(help="Group name")],
    name: Annotated[Optional[str], typer.Option("--name", "-n", help="Session name (auto-detected if omitted)")] = None,
) -> None:
    """Remove a group from a session."""
    session = _resolve_sesh(name)
    if group not in session.groups:
        typer.echo(f"Session '{session.name}' is not in group '{group}'.", err=True)
        raise typer.Exit(code=1)
    session.groups.remove(group)
    store.update(session)
    typer.echo(f"Removed group '{group}' from session '{session.name}'.", err=True)


@group_app.command("list")
def group_list(
    name: Annotated[Optional[str], typer.Option("--name", "-n", help="Show groups for this session only")] = None,
) -> None:
    """List groups. With --name: groups for that session. Without: all groups."""
    if name is not None:
        session = _resolve_sesh(name)
        sesh_groups = set(session.groups)
        _enrich_sessions([session])
        labeled = []
        for g in session.groups:
            labeled.append(f"{g}*" if g not in sesh_groups else g)
        if labeled:
            typer.echo(", ".join(labeled), err=True)
        else:
            typer.echo("No groups.", err=True)
    else:
        sessions = store.list()
        _enrich_sessions(sessions)
        all_groups = set()
        for s in sessions:
            all_groups.update(s.groups)
        if all_groups:
            for g in sorted(all_groups):
                typer.echo(g, err=True)
        else:
            typer.echo("No groups.", err=True)


# ---------------------------------------------------------------------------
# sesh parent add / remove
# ---------------------------------------------------------------------------


@parent_app.command("add")
def parent_add(
    parent: Annotated[str, typer.Argument(help="Parent session name")],
    name: Annotated[Optional[str], typer.Option("--name", "-n", help="Session name (auto-detected if omitted)")] = None,
) -> None:
    """Add a parent to a session."""
    session = _resolve_sesh(name)
    sessions = store.load()
    if parent not in sessions:
        typer.echo(f"Parent session '{parent}' not found.", err=True)
        raise typer.Exit(code=1)
    if parent in session.parents:
        typer.echo(f"'{parent}' is already a parent of '{session.name}'.", err=True)
        raise typer.Exit(code=1)
    session.parents.append(parent)
    store.update(session)
    typer.echo(f"Added parent '{parent}' to session '{session.name}'.", err=True)


@parent_app.command("remove")
def parent_remove(
    parent: Annotated[str, typer.Argument(help="Parent session name")],
    name: Annotated[Optional[str], typer.Option("--name", "-n", help="Session name (auto-detected if omitted)")] = None,
) -> None:
    """Remove a parent from a session."""
    session = _resolve_sesh(name)
    if parent not in session.parents:
        typer.echo(f"'{parent}' is not a parent of '{session.name}'.", err=True)
        raise typer.Exit(code=1)
    session.parents.remove(parent)
    store.update(session)
    typer.echo(f"Removed parent '{parent}' from session '{session.name}'.", err=True)


# ---------------------------------------------------------------------------
# sesh boxyard enable / disable / reset
# ---------------------------------------------------------------------------


@boxyard_app.command("enable")
def boxyard_enable(
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
) -> None:
    """Enable boxyard integration for a session (override)."""
    session = _resolve_sesh(name)
    session.boxyard_integration = True
    store.update(session)
    typer.echo(f"Boxyard integration enabled (override) for '{session.name}'.", err=True)


@boxyard_app.command("disable")
def boxyard_disable(
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
) -> None:
    """Disable boxyard integration for a session (override)."""
    session = _resolve_sesh(name)
    session.boxyard_integration = False
    store.update(session)
    typer.echo(f"Boxyard integration disabled (override) for '{session.name}'.", err=True)


@boxyard_app.command("reset")
def boxyard_reset(
    name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
) -> None:
    """Reset boxyard integration to inherit from global config."""
    session = _resolve_sesh(name)
    session.boxyard_integration = None
    store.update(session)
    typer.echo(f"Boxyard integration reset to global default for '{session.name}'.", err=True)


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
    sesh_name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
    json_output: Annotated[bool, typer.Option("--json", help="Output as JSON")] = False,
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
    sesh_name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
    ai_type: Annotated[str, typer.Option("--type", help="AI type: claude or opencode")] = "claude",
    ai_name: Annotated[Optional[str], typer.Option("--name", help="Name for the AI session")] = None,
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
    ai_name: Annotated[Optional[str], typer.Argument(help="AI session name")] = None,
    sesh_name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
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
    ai_name: Annotated[Optional[str], typer.Argument(help="AI session name")] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh", help="Session name (auto-detected if omitted)")] = None,
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
    sesh_name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
    ai_type: Annotated[str, typer.Option("--type", help="AI type: claude or opencode")] = "claude",
    ai_name: Annotated[str, typer.Option("--name", help="Name for the AI session")] = ...,
    session_id: Annotated[str, typer.Option("--id", help="Existing AI session ID to register")] = ...,
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
    ai_name: Annotated[str, typer.Argument(help="AI session name")],
    sesh_name: Annotated[Optional[str], typer.Argument(help="Session name (auto-detected if omitted)")] = None,
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
    sesh_name: Annotated[str, typer.Argument(help="Session name")],
    ai_name: Annotated[str, typer.Argument(help="AI session name")],
    session_id: Annotated[str, typer.Argument(help="AI session ID")],
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
    ai_name: Annotated[Optional[str], typer.Argument(help="AI session name")] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh", help="Session name (auto-detected if omitted)")] = None,
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
    ai_name: Annotated[Optional[str], typer.Argument(help="AI session name")] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh", help="Session name (auto-detected if omitted)")] = None,
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
    ai_name: Annotated[Optional[str], typer.Argument(help="AI session name")] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh", help="Session name (auto-detected if omitted)")] = None,
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
    ai_name: Annotated[Optional[str], typer.Argument(help="AI session name")] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh", help="Session name (auto-detected if omitted)")] = None,
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
    message: Annotated[str, typer.Argument(help="Message to send")],
    name: Annotated[Optional[str], typer.Option("--name", help="AI session name")] = None,
    sesh_name: Annotated[Optional[str], typer.Option("--sesh", help="Session name (auto-detected if omitted)")] = None,
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
