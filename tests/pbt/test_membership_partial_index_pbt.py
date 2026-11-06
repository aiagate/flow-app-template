"""Database-level property test for the current-period partial unique index.

The aggregate cannot see other enrollment periods, so the "one PENDING or
ACTIVE row per (team, user)" rule is a database constraint. This test exercises
the real SQLite file and the index created from the ORM metadata, not a copy of
the repository code: rows are inserted directly and the commit outcome is
compared with the rule.
"""

from __future__ import annotations

import shutil
import tempfile
from pathlib import Path

import anyio
from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st
from sqlalchemy.exc import IntegrityError
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

from tests.pbt import harness

STATUSES = ("PENDING", "ACTIVE", "LEAVED")
USER_KEYS = ("u1", "u2")


async def run_inserts(inserts: list[tuple[str, str]], db_path: Path) -> list[bool]:
    """Insert the generated rows and return whether each insert committed."""
    engine = await harness.open_database(db_path)
    try:
        session_factory = async_sessionmaker(
            engine, class_=AsyncSession, expire_on_commit=False
        )
        from app.infrastructure.unit_of_work import SQLAlchemyUnitOfWork

        team_id, users = await harness.seed_team_and_users(
            SQLAlchemyUnitOfWork(session_factory), USER_KEYS
        )
        committed: list[bool] = []
        current: dict[str, set[str]] = {key: set() for key in USER_KEYS}
        for user_key, status in inserts:
            try:
                await harness.insert_period(
                    session_factory, team_id, users[user_key], status
                )
            except IntegrityError:
                committed.append(False)
                continue
            if status in {"PENDING", "ACTIVE"}:
                assert not current[user_key], (
                    "database accepted a second current period for "
                    f"{user_key}: {sorted(current[user_key])} + {status}"
                )
                current[user_key].add(status)
            committed.append(True)
        return committed
    finally:
        await engine.dispose()


def expected_outcomes(inserts: list[tuple[str, str]]) -> list[bool]:
    """Apply the documented rule independently of the database."""
    current: dict[str, set[str]] = {key: set() for key in USER_KEYS}
    outcomes: list[bool] = []
    for user_key, status in inserts:
        if status in {"PENDING", "ACTIVE"} and current[user_key]:
            outcomes.append(False)
        else:
            outcomes.append(True)
            if status in {"PENDING", "ACTIVE"}:
                current[user_key].add(status)
    return outcomes


@settings(max_examples=50, deadline=None, suppress_health_check=[HealthCheck.too_slow])
@given(
    inserts=st.lists(
        st.tuples(st.sampled_from(USER_KEYS), st.sampled_from(STATUSES)),
        min_size=1,
        max_size=6,
    )
)
def test_partial_index_matches_current_period_rule(
    inserts: list[tuple[str, str]],
) -> None:
    """Only one PENDING/ACTIVE row per user; LEAVED rows never conflict."""
    directory = Path(tempfile.mkdtemp(prefix="membership-index-pbt-"))
    try:
        committed = anyio.run(run_inserts, inserts, directory / "membership.db")
    finally:
        shutil.rmtree(directory, ignore_errors=True)
    assert committed == expected_outcomes(inserts), (
        f"index behaviour differs from the rule for {inserts}: "
        f"committed={committed} expected={expected_outcomes(inserts)}"
    )
