from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path


DEFAULT_CONFIG_PATH = Path.home() / ".config" / "sesh.json"


def load_config(config_path: Path | None = None) -> dict:
    """Load config from sesh.json. Missing file or keys use built-in defaults."""
    defaults = {"claude_command": "claude", "opencode_command": "opencode"}
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
    parent: str | None = None
    children: list[str] = field(default_factory=list)
    repoyard_index_name: str | None = None
    tags: list[str] = field(default_factory=list)
    pinned: bool = False
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
            session = Session(**s, ai_sessions=[AiSession(**a) for a in ai_raw])
            sessions[name] = session
        return sessions

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

    def list(self, status: str | None = None, pinned: bool | None = None) -> list[Session]:
        sessions = self.load()
        result = list(sessions.values())
        if status is not None:
            result = [s for s in result if s.status == status]
        if pinned is not None:
            result = [s for s in result if s.pinned == pinned]
        return result

