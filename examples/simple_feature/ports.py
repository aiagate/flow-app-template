"""Application port owned by the simple-feature example."""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from datetime import datetime
from enum import Enum, auto

from flow_res import Result

from app.domain.repositories.interfaces import RepositoryError

from .records import InviteCode


class ConsumeStatus(Enum):
    """Outcome of one conditional increment attempt."""

    CONSUMED = auto()
    NOT_FOUND = auto()
    EXPIRED = auto()
    EXHAUSTED = auto()


@dataclass(frozen=True, slots=True)
class ConsumeOutcome:
    """Result of consuming one use from an invite code."""

    status: ConsumeStatus
    remaining_uses: int


class IInviteCodeRepository(ABC):
    """Narrow port for the three statements this feature actually needs."""

    @abstractmethod
    async def add(self, invite: InviteCode) -> Result[InviteCode, RepositoryError]:
        """Insert one invite code without committing the transaction."""
        pass

    @abstractmethod
    async def consume(
        self, code: str, at: datetime
    ) -> Result[ConsumeOutcome, RepositoryError]:
        """Atomically consume a use; ``at`` must be timezone-aware."""
        pass

    @abstractmethod
    async def record_redemption(
        self,
        redemption_id: str,
        code: str,
        redeemed_by: str,
        at: datetime,
    ) -> Result[None, RepositoryError]:
        """Insert a log without commit; ``at`` must be timezone-aware."""
        pass
