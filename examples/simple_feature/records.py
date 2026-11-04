"""Primitive records passed across the example's port boundary."""

from dataclasses import dataclass
from datetime import datetime


@dataclass(frozen=True, slots=True)
class InviteCode:
    """A row-shaped invite code, intentionally without domain value objects."""

    code: str
    team_id: str
    max_uses: int
    used_count: int
    expires_at: datetime | None
    created_at: datetime

    @property
    def remaining_uses(self) -> int:
        """Return how many more redemptions the code accepts."""
        return max(self.max_uses - self.used_count, 0)
