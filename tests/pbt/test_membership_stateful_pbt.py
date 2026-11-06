"""Stateful property test: real handlers vs the reference model.

Hypothesis generates action sequences bounded to one team, two users and eight
steps. Every action is executed through the real use case handlers against a
real SQLite file created from the ORM metadata (including the partial unique
index). The reference model predicts the outcome first; the database rows are
then compared with the model, id by id.

A separate deterministic test forces a real departure and a real rejoin of the
same team/user, so the "new period on rejoin" rule is exercised even when a
generated sequence happens to contain only joins.

This is a sequential model. It says nothing about concurrent writers; the
existing two-session test ``test_concurrent_membership_insert_returns_one_conflict``
covers that separate concern.
"""

from __future__ import annotations

import shutil
import tempfile
from pathlib import Path
from typing import Any

import anyio
from flow_res import is_ok
from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

from app.domain.repositories import RepositoryError, RepositoryErrorType
from app.infrastructure.unit_of_work import SQLAlchemyUnitOfWork
from app.usecases.memberships.approve_join_request import (
    ApproveJoinRequestCommand,
    ApproveJoinRequestHandler,
)
from app.usecases.memberships.join_team import JoinTeamCommand, JoinTeamHandler
from app.usecases.memberships.leave_team import LeaveTeamCommand, LeaveTeamHandler
from app.usecases.memberships.request_join_team import (
    RequestJoinTeamCommand,
    RequestJoinTeamHandler,
)
from app.usecases.result import ErrorType, UseCaseError
from tests.pbt import harness
from tests.pbt.reference_model import (
    ALREADY_EXISTS,
    NOT_FOUND,
    OK,
    VALIDATION_ERROR,
    Expectation,
    MembershipSpec,
)

ACTIONS = ("join", "request", "approve", "leave")
USER_KEYS = ("u1", "u2")
MAX_STEPS = 8


def _classify(result: Any) -> str:
    """Map a use case result onto the reference model's vocabulary."""
    if is_ok(result):
        return OK
    error = result.error  # type: ignore[attr-defined]
    if isinstance(error, UseCaseError):
        if error.type is ErrorType.VALIDATION_ERROR:
            return VALIDATION_ERROR
        raise AssertionError(f"unexpected use case error: {error!r}")
    if isinstance(error, RepositoryError):
        if error.type is RepositoryErrorType.NOT_FOUND:
            return NOT_FOUND
        if error.type is RepositoryErrorType.ALREADY_EXISTS:
            return ALREADY_EXISTS
        raise AssertionError(f"unexpected repository error: {error!r}")
    raise AssertionError(f"unexpected error object: {error!r}")


def _compare_with_database(
    model: MembershipSpec,
    id_by_seq: dict[int, str],
    users: dict[str, str],
    rows: dict[str, tuple[str, str]],
    trace: list[str],
) -> None:
    """Assert that the database holds exactly the periods the model holds."""
    assert len(rows) == len(model.periods), (
        f"row count drift after {trace}: db={rows} model={model.periods}"
    )
    for period in model.periods:
        real_id = id_by_seq[period.seq]
        assert real_id in rows, f"missing row for period #{period.seq} after {trace}"
        user_id, status = rows[real_id]
        assert user_id == users[period.user], (
            f"user drift for period #{period.seq} after {trace}"
        )
        assert status == period.status, (
            f"status drift for period #{period.seq} after {trace}: "
            f"db={status} model={period.status}"
        )
    for key in USER_KEYS:
        current = [
            status
            for user_id, status in rows.values()
            if user_id == users[key] and status in {"PENDING", "ACTIVE"}
        ]
        assert len(current) <= 1, (
            f"database holds {len(current)} current periods for {key} after {trace}"
        )


async def _dispatch(
    unit_of_work: SQLAlchemyUnitOfWork,
    action: str,
    team_id: str,
    user_id: str,
    membership_id: str | None,
) -> tuple[Any, str | None]:
    """Run one action through the real handler and return (result, new id)."""
    if action == "join":
        result = await JoinTeamHandler(unit_of_work).handle(
            JoinTeamCommand(team_id=team_id, user_id=user_id)
        )
        return result, (result.unwrap().id if is_ok(result) else None)
    if action == "request":
        result = await RequestJoinTeamHandler(unit_of_work).handle(
            RequestJoinTeamCommand(team_id=team_id, user_id=user_id)
        )
        return result, (result.unwrap().id if is_ok(result) else None)
    target = membership_id or harness.fresh_membership_id()
    if action == "approve":
        result = await ApproveJoinRequestHandler(unit_of_work).handle(
            ApproveJoinRequestCommand(membership_id=target)
        )
        return result, None
    result = await LeaveTeamHandler(unit_of_work).handle(
        LeaveTeamCommand(membership_id=target)
    )
    return result, None


async def run_scenario(steps: list[tuple[str, str]], db_path: Path) -> None:
    """Execute one generated action sequence against the real implementation."""
    engine = await harness.open_database(db_path)
    try:
        session_factory = async_sessionmaker(
            engine, class_=AsyncSession, expire_on_commit=False
        )
        unit_of_work = SQLAlchemyUnitOfWork(session_factory)
        team_id, users = await harness.seed_team_and_users(unit_of_work, USER_KEYS)
        model = MembershipSpec()
        id_by_seq: dict[int, str] = {}
        trace: list[str] = []

        for action, user_key in steps:
            trace.append(f"{action}({user_key})")
            expectation: Expectation = model.expect(action, "t1", user_key)
            target_seq = expectation.target_seq
            result, created_id = await _dispatch(
                unit_of_work,
                action,
                team_id,
                users[user_key],
                id_by_seq.get(target_seq) if target_seq is not None else None,
            )
            assert _classify(result) == expectation.outcome, (
                f"outcome mismatch for {trace}: "
                f"model={expectation.outcome} real={result!r}"
            )
            if expectation.outcome == OK:
                seq = model.commit(action, "t1", user_key)
                if action in {"join", "request"}:
                    assert created_id is not None
                    id_by_seq[seq] = created_id
            rows = await harness.fetch_periods(session_factory, team_id)
            _compare_with_database(model, id_by_seq, users, rows, trace)
    finally:
        await engine.dispose()


@settings(
    max_examples=60,
    deadline=None,
    suppress_health_check=[HealthCheck.too_slow],
)
@given(
    steps=st.lists(
        st.tuples(st.sampled_from(ACTIONS), st.sampled_from(USER_KEYS)),
        max_size=MAX_STEPS,
    )
)
def test_handlers_match_reference_model(steps: list[tuple[str, str]]) -> None:
    """Every generated action sequence keeps implementation and model in step."""
    directory = Path(tempfile.mkdtemp(prefix="membership-pbt-"))
    try:
        anyio.run(run_scenario, steps, directory / "membership.db")
    finally:
        shutil.rmtree(directory, ignore_errors=True)


REJOIN_FLOWS = (("join",), ("request", "approve"))


async def _start_period(
    unit_of_work: SQLAlchemyUnitOfWork,
    flow: tuple[str, ...],
    team_id: str,
    user_id: str,
) -> str:
    """Create one period through the real handlers and return its id."""
    membership_id: str | None = None
    for action in flow:
        result, created_id = await _dispatch(
            unit_of_work, action, team_id, user_id, membership_id
        )
        assert is_ok(result), f"{action} failed while starting a period: {result!r}"
        if created_id is not None:
            membership_id = created_id
    assert membership_id is not None
    return membership_id


async def _run_rejoin_flow(
    flow: tuple[str, ...], db_path: Path
) -> tuple[str, str, str, dict[str, tuple[str, str]]]:
    """Join, leave, then rejoin the same team/user; return ids, user and rows."""
    engine = await harness.open_database(db_path)
    try:
        session_factory = async_sessionmaker(
            engine, class_=AsyncSession, expire_on_commit=False
        )
        unit_of_work = SQLAlchemyUnitOfWork(session_factory)
        team_id, users = await harness.seed_team_and_users(unit_of_work, ("u1",))
        user_id = users["u1"]
        first_id = await _start_period(unit_of_work, flow, team_id, user_id)
        left = await LeaveTeamHandler(unit_of_work).handle(
            LeaveTeamCommand(membership_id=first_id)
        )
        assert is_ok(left), f"leave failed: {left!r}"
        second_id = await _start_period(unit_of_work, flow, team_id, user_id)
        rows = await harness.fetch_periods(session_factory, team_id)
        return first_id, second_id, user_id, rows
    finally:
        await engine.dispose()


def test_rejoin_after_leave_creates_new_period_and_keeps_history() -> None:
    """A real departure then rejoin creates a new row and keeps the old one."""
    for flow in REJOIN_FLOWS:
        directory = Path(tempfile.mkdtemp(prefix="membership-rejoin-"))
        try:
            first_id, second_id, user_id, rows = anyio.run(
                _run_rejoin_flow, flow, directory / "membership.db"
            )
        finally:
            shutil.rmtree(directory, ignore_errors=True)
        assert second_id != first_id, f"rejoin reused a period id via {flow}"
        assert len(rows) == 2, f"departure history was not kept via {flow}: {rows}"
        assert rows[first_id] == (user_id, "LEAVED"), (
            f"departed period is not terminal via {flow}: {rows[first_id]}"
        )
        assert rows[second_id] == (user_id, "ACTIVE"), (
            f"rejoined period is not current via {flow}: {rows[second_id]}"
        )
