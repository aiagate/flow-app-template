"""Composition root and runtime facade for the example."""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass

from sqlalchemy.ext.asyncio import AsyncEngine, AsyncSession, async_sessionmaker

from app.contracts.ports import IUnitOfWork
from app.infrastructure.unit_of_work import SQLAlchemyUnitOfWork

from .issue_invite_code import IssueInviteCodeHandler
from .ports import IInviteCodeRepository
from .redeem_invite_code import RedeemInviteCodeHandler
from .repository import SQLAlchemyInviteCodeRepository
from .schema import example_metadata

RepositoryFactories = dict[type, Callable[[AsyncSession], object]]


async def create_example_schema(engine: AsyncEngine) -> None:
    """Create only the example tables, outside the application metadata."""
    async with engine.begin() as connection:
        await connection.run_sync(example_metadata.create_all)


@dataclass(frozen=True)
class SimpleFeatureRuntime:
    """Wire the example port to the shared Unit of Work session."""

    session_factory: async_sessionmaker[AsyncSession]

    def create_uow(self) -> IUnitOfWork:
        """Build a UoW whose custom ports share its session."""
        factories: RepositoryFactories = {
            IInviteCodeRepository: SQLAlchemyInviteCodeRepository,
        }
        return SQLAlchemyUnitOfWork(self.session_factory, factories)

    def issue_handler(self) -> IssueInviteCodeHandler:
        """Return a handler holding one fresh Unit of Work."""
        return IssueInviteCodeHandler(self.create_uow())

    def redeem_handler(self) -> RedeemInviteCodeHandler:
        """Return a handler holding one fresh Unit of Work."""
        return RedeemInviteCodeHandler(self.create_uow())
