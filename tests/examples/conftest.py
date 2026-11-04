"""Fixtures for the simple-feature example tests."""

from collections.abc import AsyncGenerator

import pytest
from sqlalchemy.ext.asyncio import AsyncEngine, AsyncSession, async_sessionmaker

from examples.simple_feature import (
    SimpleFeatureRuntime,
    create_example_schema,
)


@pytest.fixture
async def example_runtime(
    database_engine: AsyncEngine,
) -> AsyncGenerator[SimpleFeatureRuntime]:
    """Create the example-only tables on the isolated test database."""
    await create_example_schema(database_engine)
    session_factory = async_sessionmaker(
        database_engine,
        class_=AsyncSession,
        expire_on_commit=False,
    )
    yield SimpleFeatureRuntime(session_factory=session_factory)
