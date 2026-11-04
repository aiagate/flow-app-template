"""Tests that the example does not leak into application metadata."""

from sqlmodel import SQLModel

from examples.simple_feature.schema import example_metadata


def test_example_tables_are_absent_from_application_metadata() -> None:
    """Example tables live in a private MetaData instance, not SQLModel's."""
    example_tables = set(example_metadata.tables)
    application_tables = set(SQLModel.metadata.tables)

    assert example_tables == {
        "example_invite_codes",
        "example_invite_redemptions",
    }
    assert example_tables.isdisjoint(application_tables)
