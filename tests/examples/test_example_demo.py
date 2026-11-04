"""Smoke test for the example's documented command."""

import os
import subprocess
import sys
from pathlib import Path


def test_module_entrypoint_runs_the_demo() -> None:
    """`python -m examples.simple_feature` completes one issue/redeem cycle."""
    repository_root = Path(__file__).resolve().parents[2]
    environment = os.environ.copy()
    environment.pop("PYTHONPATH", None)
    environment.pop("PYTHONHOME", None)

    completed = subprocess.run(
        [sys.executable, "-m", "examples.simple_feature"],
        cwd=repository_root,
        capture_output=True,
        text=True,
        timeout=60,
        check=False,
        env=environment,
    )

    assert completed.returncode == 0, completed.stderr
    assert "issued code=DEMO2026 remaining=2" in completed.stdout
    assert "redeemed code=DEMO2026 by=demo-user remaining=1" in completed.stdout
