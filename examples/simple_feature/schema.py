"""Example-only tables for the simple-feature implementation path.

The tables deliberately live in their own ``MetaData`` instance. They are not
imported by ``app.infrastructure.orm_models`` and are therefore invisible to
Alembic and to the application's SQLModel metadata.
"""

from sqlalchemy import (
    Column,
    DateTime,
    ForeignKey,
    Integer,
    MetaData,
    String,
    Table,
    UniqueConstraint,
)

example_metadata = MetaData()

invite_codes = Table(
    "example_invite_codes",
    example_metadata,
    Column("code", String(32), primary_key=True),
    Column("team_id", String(64), nullable=False),
    Column("max_uses", Integer, nullable=False),
    Column("used_count", Integer, nullable=False, default=0),
    Column("expires_at", DateTime(timezone=True), nullable=True),
    Column("created_at", DateTime(timezone=True), nullable=False),
)

invite_redemptions = Table(
    "example_invite_redemptions",
    example_metadata,
    Column("id", String(32), primary_key=True),
    Column(
        "code",
        String(32),
        ForeignKey("example_invite_codes.code", ondelete="CASCADE"),
        nullable=False,
    ),
    Column("redeemed_by", String(128), nullable=False),
    Column("redeemed_at", DateTime(timezone=True), nullable=False),
    UniqueConstraint(
        "code",
        "redeemed_by",
        name="uq_example_invite_redemptions_code_redeemer",
    ),
)
