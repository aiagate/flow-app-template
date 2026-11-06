"""Reference model for the TeamMembership spec (independent of the implementation).

The model is written from the rules recorded in
``docs/domain/BOUNDARIES_AND_GLOSSARY.md``:

* a period is created as ``ACTIVE`` (immediate join) or ``PENDING`` (request);
* the same team/user may have at most one ``PENDING`` **or** ``ACTIVE`` period;
* ``leave`` ends a period as ``LEAVED`` and keeps the row as history;
* a rejoin creates a new period with a new id;
* approval moves ``PENDING`` to ``ACTIVE`` and nothing else.

It is deliberately a *specification* model: it never imports the aggregate, the
use cases, or the ORM. The stateful property test compares its predictions with
what the real handlers and the real SQLite database do.

The outcome vocabulary (``not_found`` / ``validation_error`` / ``already_exists``)
is the API contract of the use-case boundary, not a business rule: the API maps
``VALIDATION_ERROR`` to 400, ``NOT_FOUND`` to 404 and ``CONFLICT`` to 409 (see
``docs/api/implementation_guide.md``). Changing a business rule can change which
outcome an action produces without changing that mapping.
"""

from __future__ import annotations

from dataclasses import dataclass

CURRENT_STATUSES = frozenset({"PENDING", "ACTIVE"})
OK = "ok"
NOT_FOUND = "not_found"
VALIDATION_ERROR = "validation_error"
ALREADY_EXISTS = "already_exists"


@dataclass(frozen=True)
class Period:
    """One enrollment period as the specification sees it."""

    seq: int
    team: str
    user: str
    status: str


@dataclass(frozen=True)
class Expectation:
    """What the specification says one action must do."""

    outcome: str
    target_seq: int | None = None
    resulting_status: str | None = None


class MembershipSpec:
    """Reference state machine for join, request, approve and leave."""

    def __init__(self) -> None:
        """Start with no enrollment period and the first id unused."""
        self.periods: list[Period] = []
        self._next_seq = 1

    def periods_for(self, team: str, user: str) -> list[Period]:
        """Return that pair's periods in creation order."""
        return [
            period
            for period in self.periods
            if period.team == team and period.user == user
        ]

    def current(self, team: str, user: str) -> Period | None:
        """Return the single PENDING/ACTIVE period of a pair, if any."""
        current = [
            period
            for period in self.periods_for(team, user)
            if period.status in CURRENT_STATUSES
        ]
        assert len(current) <= 1, "spec state violated one current period"
        return current[0] if current else None

    def latest(self, team: str, user: str) -> Period | None:
        """Return the most recently created period of a pair, if any."""
        periods = self.periods_for(team, user)
        return periods[-1] if periods else None

    def expect_start(self, team: str, user: str, status: str) -> Expectation:
        """Predict the outcome of join (ACTIVE) or request (PENDING)."""
        if self.current(team, user) is not None:
            return Expectation(ALREADY_EXISTS)
        return Expectation(OK, resulting_status=status)

    def expect_approve(self, team: str, user: str) -> Expectation:
        """Predict the outcome of approving that pair's current period."""
        target = self.latest(team, user)
        if target is None:
            return Expectation(NOT_FOUND)
        if target.status != "PENDING":
            return Expectation(VALIDATION_ERROR, target_seq=target.seq)
        return Expectation(OK, target_seq=target.seq, resulting_status="ACTIVE")

    def expect_leave(self, team: str, user: str) -> Expectation:
        """Predict the outcome of leaving that pair's current period."""
        target = self.latest(team, user)
        if target is None:
            return Expectation(NOT_FOUND)
        if target.status == "LEAVED":
            return Expectation(VALIDATION_ERROR, target_seq=target.seq)
        return Expectation(OK, target_seq=target.seq, resulting_status="LEAVED")

    def expect(self, action: str, team: str, user: str) -> Expectation:
        """Predict one action without changing the model."""
        if action == "join":
            return self.expect_start(team, user, "ACTIVE")
        if action == "request":
            return self.expect_start(team, user, "PENDING")
        if action == "approve":
            return self.expect_approve(team, user)
        if action == "leave":
            return self.expect_leave(team, user)
        raise ValueError(f"unknown action: {action}")

    def commit(self, action: str, team: str, user: str) -> int:
        """Apply an action the specification said would succeed."""
        expectation = self.expect(action, team, user)
        assert expectation.outcome == OK, f"cannot commit rejected action: {action}"
        if action in {"join", "request"}:
            status = "ACTIVE" if action == "join" else "PENDING"
            seq = self._next_seq
            self._next_seq += 1
            self.periods.append(Period(seq=seq, team=team, user=user, status=status))
            return seq
        target = self.latest(team, user)
        assert target is not None
        assert expectation.resulting_status is not None
        index = self.periods.index(target)
        self.periods[index] = Period(
            seq=target.seq,
            team=team,
            user=user,
            status=expectation.resulting_status,
        )
        return target.seq

    def projection(self) -> list[tuple[str, str, str]]:
        """Return (user, status, seq) for every period, in creation order."""
        return [
            (period.user, period.status, str(period.seq)) for period in self.periods
        ]
