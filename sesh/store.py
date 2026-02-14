from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path


DEFAULT_CONFIG_PATH = Path.home() / ".config" / "sesh.json"


def load_config(config_path: Path | None = None) -> dict:
    """Load config from sesh.json. Missing file or keys use built-in defaults."""
    defaults = {"claude_command": "claude", "opencode_command": "opencode", "show_markers": True, "boxyard_integration": True}
    path = config_path or DEFAULT_CONFIG_PATH
    if path.exists():
        data = json.loads(path.read_text())
        defaults.update(data)
    return defaults


@dataclass
class AiSession:
    name: str
    type: str  # "claude" | "opencode"
    session_id: str
    created: str = field(default_factory=lambda: datetime.now(timezone.utc).isoformat())
    command: str = ""


@dataclass
class Session:
    name: str
    dir: str
    tmux_session: str | None = None
    status: str = "active"
    created: str = field(default_factory=lambda: datetime.now(timezone.utc).isoformat())
    parents: list[str] = field(default_factory=list)
    boxyard_index_name: str | None = None
    groups: list[str] = field(default_factory=list)
    pinned: bool = False
    flagged: bool = False
    boxyard_integration: bool | None = None
    ai_sessions: list[AiSession] = field(default_factory=list)


class SessionStore:
    def __init__(self, data_dir: Path | None = None) -> None:
        self.data_dir = data_dir or (Path.home() / ".sesh")
        self.sessions_file = self.data_dir / "sessions.json"

    def _ensure_dir(self) -> None:
        self.data_dir.mkdir(parents=True, exist_ok=True)

    def load(self) -> dict[str, Session]:
        self._ensure_dir()
        if not self.sessions_file.exists():
            return {}
        data = json.loads(self.sessions_file.read_text())
        sessions = {}
        for name, s in data.get("sessions", {}).items():
            ai_raw = s.pop("ai_sessions", [])
            # Migrate old repoyard_index_name → boxyard_index_name
            if "repoyard_index_name" in s:
                s["boxyard_index_name"] = s.pop("repoyard_index_name")
            # Migrate parent → parents
            if "parent" in s:
                old_parent = s.pop("parent")
                s["parents"] = [old_parent] if old_parent else []
            # Remove stored children (now computed dynamically)
            s.pop("children", None)
            # Migrate tags → groups
            if "tags" in s:
                s["groups"] = s.pop("tags")
            session = Session(**s, ai_sessions=[AiSession(**a) for a in ai_raw])
            sessions[name] = session
        return sessions

    def children_of(self, name: str) -> list[str]:
        """Compute children by scanning all sessions' parents."""
        sessions = self.load()
        return [s.name for s in sessions.values() if name in s.parents]

    def save(self, sessions: dict[str, Session]) -> None:
        self._ensure_dir()
        data = {
            "version": 1,
            "sessions": {name: asdict(s) for name, s in sessions.items()},
        }
        tmp = self.sessions_file.with_suffix(".tmp")
        tmp.write_text(json.dumps(data, indent=2) + "\n")
        tmp.rename(self.sessions_file)

    def get(self, name: str) -> Session:
        sessions = self.load()
        if name not in sessions:
            raise KeyError(f"Session '{name}' not found")
        return sessions[name]

    def add(self, session: Session) -> None:
        sessions = self.load()
        if session.name in sessions:
            raise KeyError(f"Session '{session.name}' already exists")
        sessions[session.name] = session
        self.save(sessions)

    def update(self, session: Session) -> None:
        sessions = self.load()
        if session.name not in sessions:
            raise KeyError(f"Session '{session.name}' not found")
        sessions[session.name] = session
        self.save(sessions)

    def remove(self, name: str) -> None:
        sessions = self.load()
        if name not in sessions:
            raise KeyError(f"Session '{name}' not found")
        del sessions[name]
        self.save(sessions)

    def list(self, status: str | None = None, pinned: bool | None = None, flagged: bool | None = None, groups: list[str] | None = None, filter_mode: str = "all") -> list[Session]:
        sessions = self.load()
        result = list(sessions.values())
        if status is not None:
            result = [s for s in result if s.status == status]
        bool_filters = []
        if pinned is not None:
            bool_filters.append(lambda s: s.pinned == pinned)
        if flagged is not None:
            bool_filters.append(lambda s: s.flagged == flagged)
        if groups is not None:
            groups_set = set(groups)
            bool_filters.append(lambda s, gs=groups_set: bool(set(s.groups) & gs))
        if bool_filters:
            combine = all if filter_mode == "all" else any
            result = [s for s in result if combine(f(s) for f in bool_filters)]
        return result

