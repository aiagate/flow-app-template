"""Tests for redeeming a team invite code (simple transaction script)."""

from datetime import UTC, datetime, timedelta

import pytest
from flow_res import is_err, is_ok
from sqlalchemy import func, insert, select

from app.domain.repositories.interfaces import RepositoryError, RepositoryErrorType
from app.usecases.result import ErrorType, UseCaseError
from examples.simple_feature import (
    IssueInviteCodeCommand,
    RedeemInviteCodeCommand,
    SimpleFeatureRuntime,
)
from examples.simple_feature.schema import invite_codes, invite_redemptions


async def _issue(
    runtime: SimpleFeatureRuntime,
    *,
    code: str = "WELCOME1",
    max_uses: int = 2,
    expires_at: datetime | None = None,
) -> None:
    """Create one code through the issue script."""
    result = await runtime.issue_handler().handle(
        IssueInviteCodeCommand(
            team_id="team-01",
            code=code,
            max_uses=max_uses,
            expires_at=expires_at,
        )
    )
    assert is_ok(result)


async def _insert_expired_code(runtime: SimpleFeatureRuntime, code: str) -> None:
    """Insert an already-expired row, which the issue script cannot create."""
    async with runtime.session_factory() as session:
        await session.execute(
            insert(invite_codes).values(
                code=code,
                team_id="team-01",
                max_uses=3,
                used_count=0,
                expires_at=datetime.now(UTC) - timedelta(days=1),
                created_at=datetime.now(UTC) - timedelta(days=2),
            )
        )
        await session.commit()


async def _read_counts(runtime: SimpleFeatureRuntime, code: str) -> tuple[int, int]:
    """Return (used_count, redemption rows) for one code."""
    async with runtime.session_factory() as session:
        used_count = await session.scalar(
            select(invite_codes.c.used_count).where(invite_codes.c.code == code)
        )
        redemptions = await session.scalar(
            select(func.count())
            .select_from(invite_redemptions)
            .where(invite_redemptions.c.code == code)
        )
    assert used_count is not None
    assert redemptions is not None
    return used_count, redemptions


@pytest.mark.anyio
async def test_redeem_commits_the_use_count_and_redemption_log(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """A successful redemption writes both statements in one commit."""
    await _issue(example_runtime, max_uses=2)

    result = await example_runtime.redeem_handler().handle(
        RedeemInviteCodeCommand(code="WELCOME1", redeemed_by="alice")
    )

    assert is_ok(result)
    assert result.value.code == "WELCOME1"
    assert result.value.redeemed_by == "alice"
    assert result.value.remaining_uses == 1
    assert result.value.redeemed_at.tzinfo is not None

    assert await _read_counts(example_runtime, "WELCOME1") == (1, 1)

    async with example_runtime.session_factory() as session:
        redeemed_by = await session.scalar(
            select(invite_redemptions.c.redeemed_by).where(
                invite_redemptions.c.code == "WELCOME1"
            )
        )
    assert redeemed_by == "alice"


@pytest.mark.anyio
async def test_redeem_returns_not_found_for_an_unknown_code(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """An unknown code is a use-case NOT_FOUND, not a database error."""
    result = await example_runtime.redeem_handler().handle(
        RedeemInviteCodeCommand(code="MISSING1", redeemed_by="alice")
    )

    assert is_err(result)
    assert isinstance(result.error, UseCaseError)
    assert result.error.type is ErrorType.NOT_FOUND


@pytest.mark.anyio
async def test_redeem_rejects_an_expired_code(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """An expired row is reported as a conflict and is not consumed."""
    await _insert_expired_code(example_runtime, "EXPIRED1")

    result = await example_runtime.redeem_handler().handle(
        RedeemInviteCodeCommand(code="EXPIRED1", redeemed_by="alice")
    )

    assert is_err(result)
    assert isinstance(result.error, UseCaseError)
    assert result.error.type is ErrorType.CONFLICT
    assert "expired" in result.error.message
    assert await _read_counts(example_runtime, "EXPIRED1") == (0, 0)


@pytest.mark.anyio
async def test_redeem_rejects_an_exhausted_code(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """Consuming the last use succeeds; the next attempt is a conflict."""
    await _issue(example_runtime, max_uses=1)

    first = await example_runtime.redeem_handler().handle(
        RedeemInviteCodeCommand(code="WELCOME1", redeemed_by="alice")
    )
    second = await example_runtime.redeem_handler().handle(
        RedeemInviteCodeCommand(code="WELCOME1", redeemed_by="bob")
    )

    assert is_ok(first)
    assert first.value.remaining_uses == 0
    assert is_err(second)
    assert isinstance(second.error, UseCaseError)
    assert second.error.type is ErrorType.CONFLICT
    assert "remaining" in second.error.message
    assert await _read_counts(example_runtime, "WELCOME1") == (1, 1)


@pytest.mark.anyio
async def test_failed_redemption_log_rolls_back_the_use_count(
    example_runtime: SimpleFeatureRuntime,
) -> None:
    """A unique-constraint failure undoes the increment from the same UoW."""
    await _issue(example_runtime, max_uses=2)

    first = await example_runtime.redeem_handler().handle(
        RedeemInviteCodeCommand(code="WELCOME1", redeemed_by="alice")
    )
    duplicate = await example_runtime.redeem_handler().handle(
        RedeemInviteCodeCommand(code="WELCOME1", redeemed_by="alice")
    )

    assert is_ok(first)
    assert is_err(duplicate)
    assert isinstance(duplicate.error, RepositoryError)
    assert duplicate.error.type is RepositoryErrorType.ALREADY_EXISTS
    assert await _read_counts(example_runtime, "WELCOME1") == (1, 1)

    # The rollback did not poison the code; another redeemer still succeeds.
    retry = await example_runtime.redeem_handler().handle(
        RedeemInviteCodeCommand(code="WELCOME1", redeemed_by="bob")
    )
    assert is_ok(retry)
    assert retry.value.remaining_uses == 0
    assert await _read_counts(example_runtime, "WELCOME1") == (2, 2)


@pytest.mark.anyio
@pytest.mark.parametrize(
    ("command", "message_fragment"),
    [
        (RedeemInviteCodeCommand(code="bad code", redeemed_by="alice"), "code"),
        (RedeemInviteCodeCommand(code="", redeemed_by="alice"), "code"),
        (RedeemInviteCodeCommand(code="WELCOME1", redeemed_by=""), "redeemed_by"),
        (RedeemInviteCodeCommand(code="WELCOME1", redeemed_by="   "), "redeemed_by"),
        (
            RedeemInviteCodeCommand(code="WELCOME1", redeemed_by="x" * 129),
            "redeemed_by",
        ),
    ],
)
async def test_redeem_rejects_invalid_input_before_consuming(
    example_runtime: SimpleFeatureRuntime,
    command: RedeemInviteCodeCommand,
    message_fragment: str,
) -> None:
    """Invalid input returns VALIDATION_ERROR without touching the row."""
    await _issue(example_runtime, max_uses=2)

    result = await example_runtime.redeem_handler().handle(command)

    assert is_err(result)
    assert isinstance(result.error, UseCaseError)
    assert result.error.type is ErrorType.VALIDATION_ERROR
    assert message_fragment in result.error.message
    assert await _read_counts(example_runtime, "WELCOME1") == (0, 0)
