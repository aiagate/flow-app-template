"""Run the simple-feature example end to end from the command line."""

from __future__ import annotations

import asyncio

from flow_res import is_err
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker, create_async_engine

from .composition import SimpleFeatureRuntime, create_example_schema
from .issue_invite_code import IssueInviteCodeCommand
from .redeem_invite_code import RedeemInviteCodeCommand


async def run_demo(database_url: str = "sqlite+aiosqlite:///:memory:") -> int:
    """Issue and redeem one code against a disposable database."""
    engine = create_async_engine(database_url)
    try:
        await create_example_schema(engine)
        runtime = SimpleFeatureRuntime(
            session_factory=async_sessionmaker(
                engine,
                class_=AsyncSession,
                expire_on_commit=False,
            )
        )

        issued = await runtime.issue_handler().handle(
            IssueInviteCodeCommand(
                team_id="demo-team",
                code="DEMO2026",
                max_uses=2,
            )
        )
        if is_err(issued):
            print(f"issue failed: {issued.error}")
            return 1
        print(
            f"issued code={issued.value.code} remaining={issued.value.remaining_uses}"
        )

        redeemed = await runtime.redeem_handler().handle(
            RedeemInviteCodeCommand(code="DEMO2026", redeemed_by="demo-user")
        )
        if is_err(redeemed):
            print(f"redeem failed: {redeemed.error}")
            return 1
        print(
            f"redeemed code={redeemed.value.code} "
            f"by={redeemed.value.redeemed_by} "
            f"remaining={redeemed.value.remaining_uses}"
        )
        return 0
    finally:
        await engine.dispose()


def main() -> None:
    """CLI entry point used by ``python -m examples.simple_feature``."""
    raise SystemExit(asyncio.run(run_demo()))


if __name__ == "__main__":
    main()
