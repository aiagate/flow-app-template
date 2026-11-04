"""Redeem a team invite code with a short transaction script."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime
from uuid import uuid4

from flow_med import Request, RequestHandler
from flow_res import Err, Ok, Result, is_err
from injector import inject

from app.contracts.ports import IUnitOfWork
from app.usecases.result import ErrorType, UseCaseError, UseCaseResultError

from .ports import ConsumeStatus, IInviteCodeRepository
from .rules import CODE_PATTERN, MAX_REDEEMED_BY_LENGTH


@dataclass(frozen=True)
class RedeemInviteCodeResult:
    """Read model returned by the redeem script."""

    code: str
    redeemed_by: str
    remaining_uses: int
    redeemed_at: datetime


@dataclass(frozen=True)
class RedeemInviteCodeCommand(
    Request[Result[RedeemInviteCodeResult, UseCaseResultError]]
):
    """Input needed to redeem one invite code."""

    code: str
    redeemed_by: str


class RedeemInviteCodeHandler(
    RequestHandler[
        RedeemInviteCodeCommand, Result[RedeemInviteCodeResult, UseCaseResultError]
    ]
):
    """Consume a use and log it in the same Unit of Work transaction."""

    @inject
    def __init__(self, uow: IUnitOfWork) -> None:
        self._uow = uow

    async def handle(
        self, request: RedeemInviteCodeCommand
    ) -> Result[RedeemInviteCodeResult, UseCaseResultError]:
        """Run validation, conditional increment, log insert, then commit."""
        validation_error = self._validate(request)
        if validation_error is not None:
            return Err(validation_error)

        redeemed_at = datetime.now(UTC)

        async with self._uow:
            repository = self._uow.GetCustomRepository(IInviteCodeRepository)
            consume_result = await repository.consume(request.code, redeemed_at)
            if is_err(consume_result):
                return Err(consume_result.error)

            outcome = consume_result.value
            if outcome.status is not ConsumeStatus.CONSUMED:
                return Err(self._status_error(request.code, outcome.status))

            log_result = await repository.record_redemption(
                redemption_id=uuid4().hex,
                code=request.code,
                redeemed_by=request.redeemed_by,
                at=redeemed_at,
            )
            if is_err(log_result):
                # The port already rolled back the shared session, undoing
                # the increment above.
                return Err(log_result.error)

            commit_result = await self._uow.commit()
            if is_err(commit_result):
                return Err(commit_result.error)

        return Ok(
            RedeemInviteCodeResult(
                code=request.code,
                redeemed_by=request.redeemed_by,
                remaining_uses=outcome.remaining_uses,
                redeemed_at=redeemed_at,
            )
        )

    @staticmethod
    def _validate(request: RedeemInviteCodeCommand) -> UseCaseError | None:
        """Reject malformed input before opening a transaction."""
        if CODE_PATTERN.fullmatch(request.code) is None:
            return UseCaseError(
                type=ErrorType.VALIDATION_ERROR,
                message="code must be 4-32 uppercase alphanumeric characters",
            )
        if not request.redeemed_by.strip():
            return UseCaseError(
                type=ErrorType.VALIDATION_ERROR,
                message="redeemed_by must not be empty",
            )
        if len(request.redeemed_by) > MAX_REDEEMED_BY_LENGTH:
            return UseCaseError(
                type=ErrorType.VALIDATION_ERROR,
                message=(
                    f"redeemed_by must be at most {MAX_REDEEMED_BY_LENGTH} characters"
                ),
            )
        return None

    @staticmethod
    def _status_error(code: str, status: ConsumeStatus) -> UseCaseError:
        """Translate a non-consumed outcome into a use-case error."""
        if status is ConsumeStatus.NOT_FOUND:
            return UseCaseError(
                type=ErrorType.NOT_FOUND,
                message=f"Invite code not found: {code}",
            )
        if status is ConsumeStatus.EXPIRED:
            return UseCaseError(
                type=ErrorType.CONFLICT,
                message=f"Invite code has expired: {code}",
            )
        return UseCaseError(
            type=ErrorType.CONFLICT,
            message=f"Invite code has no remaining uses: {code}",
        )
