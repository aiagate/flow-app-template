"""Tests for the optional dependency extras of the entry points.

The core install must stay free of entry-point SDKs. Each entry point owns an
extra (``api``, ``discord``, ``line``) so that only the SDK it needs is
installed. ``pyproject.toml`` and ``uv.lock`` are the source of truth; the
opt-in integration test additionally builds throwaway environments with uv.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess
import tomllib
from pathlib import Path
from typing import Any

import pytest

PROJECT_ROOT = Path(__file__).resolve().parents[2]
PYPROJECT_PATH = PROJECT_ROOT / "pyproject.toml"
LOCK_PATH = PROJECT_ROOT / "uv.lock"
INTEGRATION_ENV = "FLOW_APP_OPTIONAL_EXTRAS_INTEGRATION"

#: Extras and the entry-point SDKs each one is expected to install.
EXPECTED_EXTRA_SDKS: dict[str, frozenset[str]] = {
    "api": frozenset({"fastapi", "uvicorn"}),
    "discord": frozenset({"discord-py"}),
    # The LINE webhook is a FastAPI application, so it needs the web stack.
    "line": frozenset({"fastapi", "line-bot-sdk", "uvicorn"}),
}

_ENTRYPOINT_SDKS = frozenset().union(*EXPECTED_EXTRA_SDKS.values())

_SPLIT_REQUIREMENT = re.compile(r"[<>=!~;\[\s]")


def _canonical_name(requirement: str) -> str:
    """Return the normalized distribution name of a PEP 508 requirement."""
    name = _SPLIT_REQUIREMENT.split(requirement.strip(), maxsplit=1)[0]
    return name.lower().replace("_", "-")


def _toml(path: Path) -> dict[str, Any]:
    with path.open("rb") as handle:
        loaded = tomllib.load(handle)
    assert isinstance(loaded, dict)
    return loaded


def _project() -> dict[str, Any]:
    project: dict[str, Any] = _toml(PYPROJECT_PATH)["project"]
    return project


def _requirements(raw: object) -> list[str]:
    assert isinstance(raw, list)
    assert all(isinstance(item, str) for item in raw)
    return list(raw)


def _optional_dependencies() -> dict[str, list[str]]:
    raw = _project().get("optional-dependencies", {})
    assert isinstance(raw, dict)
    return {extra: _requirements(items) for extra, items in raw.items()}


def _locked_project() -> dict[str, Any]:
    lock = _toml(LOCK_PATH)
    packages = lock["package"]
    assert isinstance(packages, list)
    matches = [
        package
        for package in packages
        if isinstance(package, dict) and package.get("name") == "app"
    ]
    assert len(matches) == 1, "uv.lock must contain exactly one 'app' package"
    return matches[0]


def _locked_names(raw: object) -> set[str]:
    assert isinstance(raw, list)
    names: set[str] = set()
    for entry in raw:
        assert isinstance(entry, dict)
        name = entry["name"]
        assert isinstance(name, str)
        names.add(name)
    return names


def test_core_dependencies_exclude_entrypoint_sdks() -> None:
    """Bare core installs no Discord, LINE, or web framework SDK."""
    core_names = {
        _canonical_name(item) for item in _requirements(_project()["dependencies"])
    }

    assert core_names & _ENTRYPOINT_SDKS == set()


def test_entrypoint_extras_are_declared() -> None:
    """The declared extras match the entry points owned by this template."""
    assert set(_optional_dependencies()) == set(EXPECTED_EXTRA_SDKS)


@pytest.mark.parametrize("extra", sorted(EXPECTED_EXTRA_SDKS))
def test_extra_installs_only_its_entrypoint_sdks(extra: str) -> None:
    """Each extra carries the SDKs for its entry point and no others."""
    names = {_canonical_name(item) for item in _optional_dependencies().get(extra, [])}

    assert names & _ENTRYPOINT_SDKS == set(EXPECTED_EXTRA_SDKS[extra])


def test_uv_lock_matches_optional_dependencies() -> None:
    """uv.lock mirrors the extras so --frozen installs stay reproducible."""
    locked_project = _locked_project()
    locked_extras: dict[str, list[str]] = {}
    raw_optional = locked_project.get("optional-dependencies", {})
    assert isinstance(raw_optional, dict)
    for extra, entries in raw_optional.items():
        locked_extras[extra] = sorted(_locked_names(entries))

    expected = {
        extra: sorted(_canonical_name(item) for item in requirements)
        for extra, requirements in _optional_dependencies().items()
    }
    assert locked_extras == expected
    assert _locked_names(locked_project["dependencies"]) & _ENTRYPOINT_SDKS == set()


def _uv_executable() -> str:
    uv = shutil.which("uv")
    assert uv is not None, "uv must be available to run the integration test"
    return uv


def _clean_environment(venv: Path) -> dict[str, str]:
    """Build a uv environment from scratch without inherited Python paths."""
    environment = dict(os.environ)
    for key in ("PYTHONHOME", "PYTHONPATH", "VIRTUAL_ENV", "CONDA_PREFIX"):
        environment.pop(key, None)
    environment["UV_PROJECT_ENVIRONMENT"] = str(venv)
    return environment


def _venv_python(venv: Path) -> Path:
    return venv / ("Scripts" if os.name == "nt" else "bin") / "python"


def _sync(venv: Path, extras: tuple[str, ...]) -> None:
    command = [_uv_executable(), "sync", "--frozen", "--no-dev"]
    for extra in extras:
        command.extend(["--extra", extra])
    completed = subprocess.run(
        command,
        cwd=PROJECT_ROOT,
        env=_clean_environment(venv),
        capture_output=True,
        text=True,
        check=False,
        timeout=600,
    )
    assert completed.returncode == 0, completed.stderr


def _run_probe(
    venv: Path,
    code: str,
    extra_environment: dict[str, str] | None = None,
) -> None:
    environment = _clean_environment(venv)
    if extra_environment is not None:
        environment.update(extra_environment)
    completed = subprocess.run(
        [str(_venv_python(venv)), "-c", code],
        cwd=PROJECT_ROOT,
        env=environment,
        capture_output=True,
        text=True,
        check=False,
        timeout=300,
    )
    assert completed.returncode == 0, completed.stderr
    assert completed.stdout.strip().endswith("OK"), completed.stdout


def _probe_imports(modules: dict[str, bool]) -> str:
    """Build a probe that asserts module availability and reports OK."""
    lines = ["import importlib.util", ""]
    for module, expected in modules.items():
        lines.append(
            f"assert (importlib.util.find_spec({module!r}) is not None) is {expected!r}"
        )
    lines.append("print('OK')")
    return "\n".join(lines)


@pytest.mark.skipif(
    os.environ.get(INTEGRATION_ENV) != "1",
    reason=f"set {INTEGRATION_ENV}=1 to build throwaway uv environments",
)
def test_fresh_environments_isolate_entrypoint_sdks(tmp_path: Path) -> None:
    """Fresh core/API/discord/LINE environments contain only their SDKs."""
    scenarios = {
        "core": (
            (),
            {
                "app.domain.aggregates": True,
                "app.infrastructure.database": True,
                "app.container": True,
                "fastapi": False,
                "discord": False,
                "linebot": False,
            },
        ),
        "api": (
            ("api",),
            {"fastapi": True, "uvicorn": True, "discord": False, "linebot": False},
        ),
        "discord": (
            ("discord",),
            {"discord": True, "fastapi": False, "linebot": False},
        ),
        "line": (
            ("line",),
            {"linebot": True, "fastapi": True, "uvicorn": True, "discord": False},
        ),
    }

    for name, (extras, modules) in scenarios.items():
        venv = tmp_path / name
        _sync(venv, extras)
        _run_probe(venv, _probe_imports(modules))
        if name == "core":
            _run_probe(
                venv,
                "from app.presentation.worker.__main__ import main\nprint('OK')",
            )
        if name == "api":
            _run_probe(
                venv,
                (
                    "import asyncio\n"
                    "from app.presentation.api.__main__ import app, lifespan\n"
                    "async def run():\n"
                    "    async with lifespan(app):\n"
                    "        assert app.state.mediator is not None\n"
                    "asyncio.run(run())\n"
                    "print('OK')\n"
                ),
                {"DATABASE_URL": "sqlite+aiosqlite://"},
            )
        if name == "discord":
            _run_probe(
                venv,
                "from app.presentation.bot.__main__ import MyBot\nprint('OK')",
            )
        if name == "line":
            _run_probe(
                venv,
                "import app.presentation.line.__main__\nprint('OK')",
                {
                    "LINE_CHANNEL_SECRET": "test-secret",
                    "LINE_CHANNEL_ACCESS_TOKEN": "test-token",
                },
            )
