from __future__ import annotations

import json
from pathlib import Path

import pytest

from automation.health_incident_monitor import health_monitor as hm


FIXTURE_DIR = Path(__file__).with_name("fixtures")


def load_fixture(name: str) -> dict:
    return json.loads((FIXTURE_DIR / name).read_text(encoding="utf-8"))


def test_fixture_degradation_emits_one_event_per_signal_and_redacts_raw_logs() -> None:
    fixture = load_fixture("degraded.json")

    result = hm.collect_health_signals(
        fixture=fixture,
        runner=hm.FixtureRunner(fixture),
    )

    assert len(result.events) == 3
    assert [event.signal_type for event in result.events] == ["cpu", "service", "log"]
    assert all(event.dedup_key for event in result.events)
    assert all(event.correlation_id == result.correlation_id for event in result.events)
    assert all("panic" not in event.summary.lower() for event in result.events)
    assert all("raw" not in event.summary.lower() for event in result.events)


def test_repeated_observation_preserves_dedup_key() -> None:
    fixture = load_fixture("degraded.json")

    first = hm.collect_health_signals(fixture=fixture, runner=hm.FixtureRunner(fixture))
    second = hm.collect_health_signals(fixture=fixture, runner=hm.FixtureRunner(fixture))

    assert [event.dedup_key for event in first.events] == [event.dedup_key for event in second.events]


def test_verification_rejects_different_source_identity() -> None:
    fixture = load_fixture("degraded.json")
    result = hm.collect_health_signals(fixture=fixture, runner=hm.FixtureRunner(fixture))

    verified = hm.verify_original_signal(
        source_id=result.source_id,
        verification_runner=hm.FixtureVerificationRunner(fixture),
    )
    forged = hm.verify_original_signal(
        source_id="other-source",
        verification_runner=hm.FixtureVerificationRunner(fixture),
    )

    assert verified.observed_state == "degraded"
    assert verified.source_id == result.source_id
    assert forged.observed_state == "degraded"
    assert forged.source_id == "other-source"
    assert forged.healthy is False
