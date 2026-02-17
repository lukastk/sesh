from __future__ import annotations

import subprocess


def session_exists(name: str) -> bool:
    result = subprocess.run(
        ["tmux", "has-session", "-t", f"={name}"],
        capture_output=True,
    )
    return result.returncode == 0


def create_session(name: str, dir: str, window_name: str = "CC") -> None:
    subprocess.run(
        ["tmux", "new-session", "-d", "-s", name, "-n", window_name, "-c", dir],
        check=True,
    )


def kill_session(name: str) -> None:
    subprocess.run(
        ["tmux", "kill-session", "-t", name],
        check=True,
    )


def rename_session(old: str, new: str) -> None:
    subprocess.run(
        ["tmux", "rename-session", "-t", old, new],
        check=True,
    )


def list_sessions() -> list[str]:
    result = subprocess.run(
        ["tmux", "list-sessions", "-F", "#{session_name}"],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        return []
    return [line for line in result.stdout.strip().splitlines() if line]


def new_window(session: str, name: str, cmd: str, cwd: str) -> None:
    subprocess.run(
        ["tmux", "new-window", "-t", session, "-n", name, "-c", cwd, cmd],
        check=True,
    )


def has_window(session: str, name: str) -> bool:
    result = subprocess.run(
        ["tmux", "list-windows", "-t", session, "-F", "#{window_name}"],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        return False
    return name in result.stdout.strip().splitlines()
