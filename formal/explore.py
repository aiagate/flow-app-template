"""Exhaustive bounded exploration of the TeamMembership rules.

This is the executable specification model: it is written from
``docs/domain/BOUNDARIES_AND_GLOSSARY.md`` only, never imports the
implementation, and enumerates *every* reachable state of the declared bounds
so the result does not rest on random sampling alone.

Bounds (declared, not inferred):
  * teams: 1 (``--teams``)
  * users: 2 (``--users``)
  * trace length: up to ``--max-depth`` actions (default 8)

Assumptions:
  * each action is atomic; no interleaving inside one action
  * no fairness assumption: every enabled action is explored
  * only the rules stated in ``docs/domain/BOUNDARIES_AND_GLOSSARY.md`` are
    modelled. Authorisation (who may approve) is deliberately absent.

Usage::

    python3 formal/explore.py --max-depth 8 --teams 1 --users 2
    python3 formal/explore.py --variant broken-approve-guard
"""

from __future__ import annotations

import argparse
import itertools
import json
from dataclasses import dataclass

CURRENT = ("PENDING", "ACTIVE")
STATUSES = ("PENDING", "ACTIVE", "LEAVED")


@dataclass(frozen=True, order=True)
class Period:
    """One enrollment period. ``id`` is the creation counter value."""

    id: int
    team: str
    user: str
    status: str


@dataclass(frozen=True)
class State:
    """Reachable state: periods in creation order plus terminal history."""

    periods: tuple[Period, ...]
    ever_leaved: frozenset[int]


@dataclass(frozen=True)
class Step:
    """One labelled transition."""

    action: str
    target: str
    state: State


def current_periods(state: State, team: str, user: str) -> tuple[Period, ...]:
    """Return the PENDING/ACTIVE periods of one (team, user)."""
    return tuple(
        period
        for period in state.periods
        if period.team == team and period.user == user and period.status in CURRENT
    )


def periods_of(state: State, team: str, user: str) -> tuple[Period, ...]:
    """Return every period of one (team, user), oldest first."""
    return tuple(
        period
        for period in state.periods
        if period.team == team and period.user == user
    )


def next_id(state: State) -> int:
    """Return the counter value for the next created period."""
    return max((period.id for period in state.periods), default=0) + 1


def invariants(state: State) -> list[str]:
    """Return the names of violated invariants (empty when the state is valid)."""
    violated: list[str] = []
    pairs = {(period.team, period.user) for period in state.periods}
    for team, user in pairs:
        if len(current_periods(state, team, user)) > 1:
            violated.append("one_current_period")
    if any(
        period.id in state.ever_leaved
        for period in state.periods
        if period.status != "LEAVED"
    ):
        violated.append("leaved_is_terminal")
    ids = [period.id for period in state.periods]
    if len(ids) != len(set(ids)):
        violated.append("distinct_period_ids")
    if any(period.id >= next_id(state) for period in state.periods):
        violated.append("ids_below_counter")
    if any(period.status not in STATUSES for period in state.periods):
        violated.append("status_domain")
    return violated


def enabled_actions(
    state: State, teams: tuple[str, ...], users: tuple[str, ...], variant: str
) -> list[Step]:
    """Enumerate every enabled action of the bounded model."""
    steps: list[Step] = []
    guard_join = variant != "broken-join-guard"
    guard_approve = variant != "broken-approve-guard"
    for team, user in itertools.product(teams, users):
        for action, status in (("join", "ACTIVE"), ("requestJoin", "PENDING")):
            if guard_join and current_periods(state, team, user):
                continue
            period = Period(id=next_id(state), team=team, user=user, status=status)
            steps.append(
                Step(
                    action=action,
                    target=f"{team}/{user}",
                    state=State(
                        periods=state.periods + (period,),
                        ever_leaved=state.ever_leaved,
                    ),
                )
            )
    for index, period in enumerate(state.periods):
        if period.status == "PENDING" or not guard_approve:
            updated = Period(
                id=period.id, team=period.team, user=period.user, status="ACTIVE"
            )
            periods = state.periods[:index] + (updated,) + state.periods[index + 1 :]
            steps.append(
                Step(
                    action="approve",
                    target=f"period#{period.id}",
                    state=State(periods=periods, ever_leaved=state.ever_leaved),
                )
            )
        if period.status != "LEAVED":
            updated = Period(
                id=period.id, team=period.team, user=period.user, status="LEAVED"
            )
            periods = state.periods[:index] + (updated,) + state.periods[index + 1 :]
            steps.append(
                Step(
                    action="leave",
                    target=f"period#{period.id}",
                    state=State(
                        periods=periods,
                        ever_leaved=state.ever_leaved | {period.id},
                    ),
                )
            )
    return steps


def explore(
    teams: tuple[str, ...],
    users: tuple[str, ...],
    max_depth: int,
    variant: str,
) -> dict[str, object]:
    """Breadth-first enumeration of all reachable states up to ``max_depth``."""
    initial = State(periods=(), ever_leaved=frozenset())
    queue: list[tuple[State, tuple[str, ...]]] = [(initial, ())]
    seen: set[State] = {initial}
    violations: list[dict[str, object]] = []
    depth_counts: dict[int, int] = {0: 1}
    transitions = 0
    while queue:
        state, trace = queue.pop(0)
        depth = len(trace)
        violated = invariants(state)
        if violated:
            violations.append(
                {
                    "trace": list(trace),
                    "violated": violated,
                    "periods": [period.__dict__ for period in state.periods],
                }
            )
            continue
        if depth == max_depth:
            continue
        for step in enabled_actions(state, teams, users, variant):
            transitions += 1
            next_state = step.state
            label = f"{step.action}({step.target})"
            if next_state in seen:
                continue
            seen.add(next_state)
            depth_counts[depth + 1] = depth_counts.get(depth + 1, 0) + 1
            queue.append((next_state, trace + (label,)))
    return {
        "variant": variant,
        "bounds": {
            "teams": list(teams),
            "users": list(users),
            "max_depth": max_depth,
        },
        "states": len(seen),
        "transitions": transitions,
        "states_by_depth": depth_counts,
        "violations": violations,
        "first_violation": violations[0] if violations else None,
        "invariants_checked": [
            "one_current_period",
            "leaved_is_terminal",
            "distinct_period_ids",
            "ids_below_counter",
            "status_domain",
            "approval_only_from_pending (structural: approve is enabled only "
            "for PENDING periods)",
        ],
    }


def main() -> None:
    """Run the exploration and print a JSON summary."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--max-depth", type=int, default=8)
    parser.add_argument("--teams", type=int, default=1)
    parser.add_argument("--users", type=int, default=2)
    parser.add_argument(
        "--variant",
        choices=("spec", "broken-join-guard", "broken-approve-guard"),
        default="spec",
    )
    arguments = parser.parse_args()
    teams = tuple(f"t{index}" for index in range(1, arguments.teams + 1))
    users = tuple(f"u{index}" for index in range(1, arguments.users + 1))
    result = explore(teams, users, arguments.max_depth, arguments.variant)
    print(json.dumps(result, ensure_ascii=False, indent=2, default=str))
    if result["violations"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
