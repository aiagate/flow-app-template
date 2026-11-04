"""Issue a team invite code with a short transaction script."""

from __future__ import annotations

import secrets
from dataclasses import dataclass
from datetime import UTC, datetime

from flow_med import Request, RequestHandler
from flow_res import Err, Ok, Result, is_err
from injector import inject

from app.contracts.ports import IUnitOfWork
from app.usecases.result import ErrorType, UseCaseError, UseCaseResultError

from .ports import IInviteCodeRepository
from .records import InviteCode
from .rules import (
    CODE_PATTERN,
    GENERATED_CODE_LENGTH,
    MAX_TEAM_ID_LENGTH,
    MAX_USES_LIMIT,
)

_CODE_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"


@dataclass(frozen=True)
class IssueInviteCodeResult:
    """Read model returned by the issue script."""

    code: str
    team_id: str
    max_uses: int
    remaining_uses: int
    expires_at: datetime | None
    created_at: datetime


@dataclass(frozen=True)
class IssueInviteCodeCommand(
    Request[Result[IssueInviteCodeResult, UseCaseResultError]]
):
    """Input needed to issue one invite code."""

    team_id: str
    max_uses: int
    expires_at: datetime | None = None
    code: str | None = None


class IssueInviteCodeHandler(
    RequestHandler[
        IssueInviteCodeCommand, Result[IssueInviteCodeResult, UseCaseResultError]
    ]
):
    """Create a row; no aggregate, value object, or generic repository needed."""

    @inject
    def __init__(self, uow: IUnitOfWork) -> None:
        self._uow = uow

    async def handle(
        self, request: IssueInviteCodeCommand
    ) -> Result[IssueInviteCodeResult, UseCaseResultError]:
        """Insert the code and commit exactly one transaction."""
        created_at = datetime.now(UTC)
        validation_error = self._validate(request, created_at)
        if validation_error is not None:
            return Err(validation_error)

        invite = InviteCode(
            code=request.code or self._generate_code(),
            team_id=request.team_id,
            max_uses=request.max_uses,
            used_count=0,
            expires_at=(
                request.expires_at.astimezone(UTC)
                if request.expires_at is not None
                else None
            ),
            created_at=created_at,
        )

        async with self._uow:
            repository = self._uow.GetCustomRepository(IInviteCodeRepository)
            add_result = await repository.add(invite)
            if is_err(add_result):
                return Err(add_result.error)

            commit_result = await self._uow.commit()
            if is_err(commit_result):
                return Err(commit_result.error)

        return Ok(
            IssueInviteCodeResult(
                code=invite.code,
                team_id=invite.team_id,
                max_uses=invite.max_uses,
                remaining_uses=invite.remaining_uses,
                expires_at=invite.expires_at,
                created_at=invite.created_at,
            )
        )

    @staticmethod
    def _validate(
        request: IssueInviteCodeCommand, now: datetime
    ) -> UseCaseError | None:
        """Return the first validation failure, without opening a transaction."""
        if not request.team_id.strip():
            return UseCaseError(
                type=ErrorType.VALIDATION_ERROR,
                message="team_id must not be empty",
            )
        if len(request.team_id) > MAX_TEAM_ID_LENGTH:
            return UseCaseError(
                type=ErrorType.VALIDATION_ERROR,
                message=f"team_id must be at most {MAX_TEAM_ID_LENGTH} characters",
            )
        if not 1 <= request.max_uses <= MAX_USES_LIMIT:
            return UseCaseError(
                type=ErrorType.VALIDATION_ERROR,
                message=f"max_uses must be between 1 and {MAX_USES_LIMIT}",
            )
        if request.code is not None and CODE_PATTERN.fullmatch(request.code) is None:
            return UseCaseError(
                type=ErrorType.VALIDATION_ERROR,
                message="code must be 4-32 uppercase alphanumeric characters",
            )
        expires_at = request.expires_at
        if expires_at is not None:
            if expires_at.tzinfo is None or expires_at.utcoffset() is None:
                return UseCaseError(
                    type=ErrorType.VALIDATION_ERROR,
                    message="expires_at must include a timezone",
                )
            if expires_at <= now:
                return UseCaseError(
                    type=ErrorType.VALIDATION_ERROR,
                    message="expires_at must be in the future",
                )
        return None

    @staticmethod
    def _generate_code() -> str:
        """Generate a short code without ambiguous characters."""
        return "".join(
            secrets.choice(_CODE_ALPHABET) for _ in range(GENERATED_CODE_LENGTH)
        )
