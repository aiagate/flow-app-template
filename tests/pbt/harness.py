"""Shared harness for the membership property tests.

Creates a real SQLite file from the application ORM metadata (so the partial
unique index declared on ``TeamMembershipORM`` exists), then exposes the real
use case handlers bound to that database.
"""

from __future__ import annotations

from collections.abc import Mapping
from datetime import UTC, datetime
from pathlib import Path

from sqlalchemy import select
from sqlalchemy.ext.asyncio import (
    AsyncEngine,
    AsyncSession,
    async_sessionmaker,
    create_async_engine,
)
from sqlmodel import SQLModel

from app.contracts.ports import IEventBus
from app.domain.aggregates.team_membership import TeamMembership
from app.domain.value_objects import (
    MembershipId,
    MembershipRole,
    MembershipStatus,
    TeamId,
    UserId,
    Version,
)
from app.infrastructure.mappings.team_membership import team_membership_to_orm
from app.infrastructure.orm_models.team_membership_orm import TeamMembershipORM
from app.infrastructure.orm_registry import init_orm_mappings
from app.infrastructure.unit_of_work import SQLAlchemyUnitOfWork
from app.usecases.teams.create_team import CreateTeamCommand, CreateTeamHandler
from app.usecases.users.create_user import CreateUserCommand, CreateUserHandler

init_orm_mappings()

TEAM_NAME = "Team A"


class NoopEventBus(IEventBus):
    """Event bus stub; membership rules do not depend on published events."""

    async def publish(self, topic: str, payload: Mapping[str, object]) -> None:
        """Accept and discard events."""
        return None

    async def subscribe(self, topic: str, handler: object) -> None:
        """No subscriptions in tests."""
        return None

    async def start(self) -> None:
        """No start-up work."""
        return None

    async def stop(self) -> None:
        """No shut-down work."""
        return None


async def open_database(db_path: Path) -> AsyncEngine:
    """Create the schema (with its partial unique index) in a fresh file."""
    engine = create_async_engine(
        f"sqlite+aiosqlite:///{db_path}", connect_args={"timeout": 10}
    )
    async with engine.begin() as connection:
        await connection.run_sync(SQLModel.metadata.create_all)
    return engine


async def seed_team_and_users(
    unit_of_work: SQLAlchemyUnitOfWork, user_keys: tuple[str, ...]
) -> tuple[str, dict[str, str]]:
    """Create one team and the given users through their real use cases."""
    team_result = await CreateTeamHandler(unit_of_work).handle(
        CreateTeamCommand(name=TEAM_NAME)
    )
    team_id = team_result.unwrap().id
    user_ids: dict[str, str] = {}
    for index, key in enumerate(user_keys, start=1):
        created = await CreateUserHandler(unit_of_work, NoopEventBus()).handle(
            CreateUserCommand(
                display_name=f"User {index}", email=f"pbt-user{index}@example.com"
            )
        )
        user_ids[key] = created.unwrap().id
    return team_id, user_ids


def fresh_membership_id() -> str:
    """Generate a well-formed id that no database row uses."""
    return MembershipId.generate().expect("valid membership id").to_primitive()


async def insert_period(
    session_factory: async_sessionmaker[AsyncSession],
    team_id: str,
    user_id: str,
    status: str,
) -> None:
    """Insert one period row directly, bypassing the use cases."""
    now = datetime.now(UTC)
    membership = TeamMembership.restore(
        membership_id=MembershipId.generate().expect("valid membership id"),
        team_id=TeamId.from_primitive(team_id).unwrap(),
        user_id=UserId.from_primitive(user_id).unwrap(),
        role=MembershipRole.MEMBER,
        status=MembershipStatus.from_primitive(status).unwrap(),
        version=Version(0),
        created_at=now,
        updated_at=now,
    )
    row = team_membership_to_orm(membership)
    async with session_factory() as session:
        session.add(row)
        await session.commit()


async def fetch_periods(
    session_factory: async_sessionmaker[AsyncSession], team_id: str
) -> dict[str, tuple[str, str]]:
    """Return {membership_id: (user_id, status)} for one team."""
    async with session_factory() as session:
        result = await session.execute(
            select(TeamMembershipORM).where(
                TeamMembershipORM.team_id == team_id  # pyright: ignore[reportArgumentType]
            )
        )
        rows = result.scalars().all()
    return {(row.id or ""): (row.user_id, row.status) for row in rows}
