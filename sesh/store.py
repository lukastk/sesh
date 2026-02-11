from __future__ import annotations

import json
import shutil
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path


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


class SessionStore:
    DATA_DIR = Path.home() / ".sesh"
    SESSIONS_FILE = DATA_DIR / "sessions.json"

    def _ensure_dir(self) -> None:
        self.DATA_DIR.mkdir(parents=True, exist_ok=True)

    def load(self) -> dict[str, Session]:
        self._ensure_dir()
        if not self.SESSIONS_FILE.exists():
            return {}
        data = json.loads(self.SESSIONS_FILE.read_text())
        return {name: Session(**s) for name, s in data.get("sessions", {}).items()}

    def save(self, sessions: dict[str, Session]) -> None:
        self._ensure_dir()
        data = {
            "version": 1,
            "sessions": {name: asdict(s) for name, s in sessions.items()},
        }
        tmp = self.SESSIONS_FILE.with_suffix(".tmp")
        tmp.write_text(json.dumps(data, indent=2) + "\n")
        tmp.rename(self.SESSIONS_FILE)

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

    def list(self, status: str | None = None) -> list[Session]:
        sessions = self.load()
        if status is None:
            return list(sessions.values())
        return [s for s in sessions.values() if s.status == status]

    def session_dir(self, name: str) -> Path:
        d = self.DATA_DIR / "sessions" / name
        d.mkdir(parents=True, exist_ok=True)
        return d

    def remove_session_dir(self, name: str) -> None:
        d = self.DATA_DIR / "sessions" / name
        if d.exists():
            shutil.rmtree(d)
