"""Tests for issuing a team invite code (simple transaction script)."""

from datetime import UTC, datetime, timedelta, timezone

import pytest
from flow_res import is_err, is_ok
from sqlalchemy import func, select

from app.domain.repositories.interfaces import RepositoryError, RepositoryErrorType
from app.usecases.result import ErrorType, UseCaseError
from examples.simple_feature import IssueInviteCodeCommand, SimpleFeatureRuntime
from examples.simple_feature.schema import invite_codes


@pytest.mark.anyio
async def test_issue_persists_a_code_with_zero_uses(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """A valid command commits one row that later readers can see."""
    result = await example_runtime.issue_handler().handle(
        IssueInviteCodeCommand(
            team_id="team-01",
            code="WELCOME1",
            max_uses=3,
            expires_at=None,
        )
    )

    assert is_ok(result)
    issued = result.value
    assert issued.code == "WELCOME1"
    assert issued.team_id == "team-01"
    assert issued.max_uses == 3
    assert issued.remaining_uses == 3
    assert issued.created_at.tzinfo is not None

    async with example_runtime.session_factory() as session:
        row = (
            (
                await session.execute(
                    select(invite_codes).where(invite_codes.c.code == "WELCOME1")
                )
            )
            .mappings()
            .one()
        )

    assert row["team_id"] == "team-01"
    assert row["max_uses"] == 3
    assert row["used_count"] == 0
    assert row["expires_at"] is None
    assert isinstance(row["created_at"], datetime)


@pytest.mark.anyio
async def test_issue_generates_a_code_when_none_is_given(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """Omitting the code is supported and produces a persisted 8-char code."""
    result = await example_runtime.issue_handler().handle(
        IssueInviteCodeCommand(team_id="team-01", max_uses=1)
    )

    assert is_ok(result)
    assert len(result.value.code) == 8
    assert result.value.code.isalnum()
    assert result.value.code == result.value.code.upper()

    async with example_runtime.session_factory() as session:
        count = await session.scalar(select(func.count()).select_from(invite_codes))
    assert count == 1


@pytest.mark.anyio
async def test_issue_rejects_a_duplicate_code(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """The primary key duplicate surfaces as a repository ALREADY_EXISTS error."""
    command = IssueInviteCodeCommand(team_id="team-01", code="WELCOME1", max_uses=1)
    assert is_ok(await example_runtime.issue_handler().handle(command))

    duplicate = await example_runtime.issue_handler().handle(
        IssueInviteCodeCommand(team_id="team-02", code="WELCOME1", max_uses=5)
    )

    assert is_err(duplicate)
    assert isinstance(duplicate.error, RepositoryError)
    assert duplicate.error.type is RepositoryErrorType.ALREADY_EXISTS

    async with example_runtime.session_factory() as session:
        row = (
            (
                await session.execute(
                    select(invite_codes).where(invite_codes.c.code == "WELCOME1")
                )
            )
            .mappings()
            .one()
        )
    assert row["team_id"] == "team-01"
    assert row["max_uses"] == 1


@pytest.mark.anyio
@pytest.mark.parametrize(
    ("command", "message_fragment"),
    [
        (IssueInviteCodeCommand(team_id="", max_uses=1), "team_id"),
        (IssueInviteCodeCommand(team_id="   ", max_uses=1), "team_id"),
        (IssueInviteCodeCommand(team_id="team-01", max_uses=0), "max_uses"),
        (IssueInviteCodeCommand(team_id="team-01", max_uses=1001), "max_uses"),
        (
            IssueInviteCodeCommand(team_id="team-01", max_uses=1, code="ab"),
            "code",
        ),
        (
            IssueInviteCodeCommand(team_id="team-01", max_uses=1, code="BAD CODE"),
            "code",
        ),
    ],
)
async def test_issue_rejects_invalid_input_before_touching_the_database(
    example_runtime: SimpleFeatureRuntime,
    command: IssueInviteCodeCommand,
    message_fragment: str,
) -> None:
    """Invalid input returns VALIDATION_ERROR and writes no row."""
    result = await example_runtime.issue_handler().handle(command)

    assert is_err(result)
    assert isinstance(result.error, UseCaseError)
    assert result.error.type is ErrorType.VALIDATION_ERROR
    assert message_fragment in result.error.message

    async with example_runtime.session_factory() as session:
        count = await session.scalar(select(func.count()).select_from(invite_codes))
    assert count == 0


@pytest.mark.anyio
async def test_issue_normalizes_an_offset_expiry_to_utc(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """Stored expiries use one UTC clock regardless of the caller's offset."""
    expires_at = datetime(2030, 1, 1, 9, 0, tzinfo=timezone(timedelta(hours=9)))

    result = await example_runtime.issue_handler().handle(
        IssueInviteCodeCommand(
            team_id="team-01",
            code="TZTEST01",
            max_uses=1,
            expires_at=expires_at,
        )
    )

    assert is_ok(result)
    assert result.value.expires_at == expires_at

    async with example_runtime.session_factory() as session:
        stored = await session.scalar(
            select(invite_codes.c.expires_at).where(invite_codes.c.code == "TZTEST01")
        )
    assert stored is not None
    stored_utc = (
        stored.replace(tzinfo=UTC) if stored.tzinfo is None else stored.astimezone(UTC)
    )
    assert stored_utc == datetime(2030, 1, 1, 0, 0, tzinfo=UTC)


@pytest.mark.anyio
async def test_issue_rejects_an_expiry_that_is_not_in_the_future(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """Past and naive expiries are rejected instead of guessed."""
    past = await example_runtime.issue_handler().handle(
        IssueInviteCodeCommand(
            team_id="team-01",
            max_uses=1,
            expires_at=datetime.now(UTC) - timedelta(minutes=1),
        )
    )
    naive = await example_runtime.issue_handler().handle(
        IssueInviteCodeCommand(
            team_id="team-01",
            max_uses=1,
            expires_at=datetime(2030, 1, 1),
        )
    )

    for result in (past, naive):
        assert is_err(result)
        assert isinstance(result.error, UseCaseError)
        assert result.error.type is ErrorType.VALIDATION_ERROR
        assert "expires_at" in result.error.message
