"""SQLAlchemy Core implementation of the example's narrow port."""

from __future__ import annotations

import logging
from datetime import UTC, datetime
from typing import Any, cast

from flow_res import Err, Ok, Result
from sqlalchemy import insert, or_, select, update
from sqlalchemy.engine import CursorResult, RowMapping
from sqlalchemy.exc import IntegrityError, SQLAlchemyError
from sqlalchemy.ext.asyncio import AsyncSession

from app.domain.repositories.interfaces import RepositoryError, RepositoryErrorType

from .ports import ConsumeOutcome, ConsumeStatus, IInviteCodeRepository
from .records import InviteCode
from .schema import invite_codes, invite_redemptions

logger = logging.getLogger(__name__)


def _as_utc(value: datetime) -> datetime:
    """Treat SQLite's naive datetimes as UTC, matching what the scripts store."""
    if value.tzinfo is None:
        return value.replace(tzinfo=UTC)
    return value.astimezone(UTC)


class SQLAlchemyInviteCodeRepository(IInviteCodeRepository):
    """Use the Unit of Work's session; never open or commit a session here."""

    def __init__(self, session: AsyncSession) -> None:
        """Bind the port to the transaction opened by the Unit of Work."""
        self._session = session

    async def _fetch(self, code: str) -> RowMapping | None:
        result = await self._session.execute(
            select(invite_codes).where(invite_codes.c.code == code)
        )
        return result.mappings().first()

    async def add(self, invite: InviteCode) -> Result[InviteCode, RepositoryError]:
        """Insert one row and flush so constraint failures surface here."""
        try:
            await self._session.execute(
                insert(invite_codes).values(
                    code=invite.code,
                    team_id=invite.team_id,
                    max_uses=invite.max_uses,
                    used_count=invite.used_count,
                    expires_at=invite.expires_at,
                    created_at=invite.created_at,
                )
            )
            await self._session.flush()
            return Ok(invite)
        except IntegrityError:
            await self._session.rollback()
            return Err(
                RepositoryError(
                    type=RepositoryErrorType.ALREADY_EXISTS,
                    message=f"Invite code already exists: {invite.code}",
                )
            )
        except SQLAlchemyError as error:
            await self._session.rollback()
            logger.exception("Database error occurred while adding an invite code")
            return Err(
                RepositoryError(
                    type=RepositoryErrorType.UNEXPECTED,
                    message=str(error),
                )
            )

    async def consume(
        self, code: str, at: datetime
    ) -> Result[ConsumeOutcome, RepositoryError]:
        """Increment only when the row exists, is live, and has a use left."""
        now = at.astimezone(UTC)
        statement = (
            update(invite_codes)
            .where(
                invite_codes.c.code == code,
                invite_codes.c.used_count < invite_codes.c.max_uses,
                or_(
                    invite_codes.c.expires_at.is_(None),
                    invite_codes.c.expires_at > now,
                ),
            )
            .values(used_count=invite_codes.c.used_count + 1)
        )
        try:
            result = cast(CursorResult[Any], await self._session.execute(statement))
            if result.rowcount == 1:
                row = await self._fetch(code)
                if row is None:
                    return Err(
                        RepositoryError(
                            type=RepositoryErrorType.UNEXPECTED,
                            message=f"Invite code disappeared after update: {code}",
                        )
                    )
                remaining = int(row["max_uses"]) - int(row["used_count"])
                return Ok(ConsumeOutcome(ConsumeStatus.CONSUMED, remaining))

            row = await self._fetch(code)
            if row is None:
                return Ok(ConsumeOutcome(ConsumeStatus.NOT_FOUND, 0))
            expires_at = cast(datetime | None, row["expires_at"])
            if expires_at is not None and _as_utc(expires_at) <= now:
                return Ok(ConsumeOutcome(ConsumeStatus.EXPIRED, 0))
            return Ok(ConsumeOutcome(ConsumeStatus.EXHAUSTED, 0))
        except SQLAlchemyError as error:
            await self._session.rollback()
            logger.exception("Database error occurred while consuming an invite code")
            return Err(
                RepositoryError(
                    type=RepositoryErrorType.UNEXPECTED,
                    message=str(error),
                )
            )

    async def record_redemption(
        self,
        redemption_id: str,
        code: str,
        redeemed_by: str,
        at: datetime,
    ) -> Result[None, RepositoryError]:
        """Insert the log row; the code/redeemer pair is unique."""
        try:
            await self._session.execute(
                insert(invite_redemptions).values(
                    id=redemption_id,
                    code=code,
                    redeemed_by=redeemed_by,
                    redeemed_at=at.astimezone(UTC),
                )
            )
            await self._session.flush()
            return Ok(None)
        except IntegrityError:
            await self._session.rollback()
            return Err(
                RepositoryError(
                    type=RepositoryErrorType.ALREADY_EXISTS,
                    message=(
                        f"Invite code {code} has already been redeemed by {redeemed_by}"
                    ),
                )
            )
        except SQLAlchemyError as error:
            await self._session.rollback()
            logger.exception(
                "Database error occurred while recording an invite redemption"
            )
            return Err(
                RepositoryError(
                    type=RepositoryErrorType.UNEXPECTED,
                    message=str(error),
                )
            )
