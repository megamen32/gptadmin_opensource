from __future__ import annotations

from dataclasses import dataclass, field
from hashlib import sha256
from typing import Literal, Sequence


SignalType = Literal["cpu", "ram", "disk", "service", "log"]
ObservedState = Literal["healthy", "degraded"]


@dataclass(frozen=True)
class HealthEvent:
    host_id: str
    source_id: str
    signal_type: SignalType
    severity: str
    observed_at: float
    dedup_key: str
    correlation_id: str
    evidence_refs: tuple[str, ...]
    summary: str


@dataclass(frozen=True)
class HealthCollectionResult:
    host_id: str
    source_id: str
    correlation_id: str
    observed_at: float
    events: tuple[HealthEvent, ...]


@dataclass(frozen=True)
class VerificationResult:
    source_id: str
    healthy: bool
    observed_state: ObservedState
    evidence_refs: tuple[str, ...] = ()


@dataclass(frozen=True)
class FixtureRunner:
    fixture: dict

    def collect(self) -> dict:
        return self.fixture


@dataclass(frozen=True)
class FixtureVerificationRunner:
    fixture: dict

    def verify(self, source_id: str) -> dict:
        return {
            "fixture_source_id": self.fixture.get("source_id", ""),
            "source_id": source_id,
            "has_degradation": bool(_build_events(self.fixture, strict=False)),
        }


def collect_health_signals(*, fixture: dict, runner: FixtureRunner) -> HealthCollectionResult:
    payload = runner.collect() if runner else fixture
    host_id = str(payload["host_id"])
    source_id = str(payload["source_id"])
    observed_at = float(payload["observed_at"])
    correlation_id = _stable_token("correlation", source_id, host_id)
    events = tuple(_build_events(payload, correlation_id=correlation_id))
    return HealthCollectionResult(
        host_id=host_id,
        source_id=source_id,
        correlation_id=correlation_id,
        observed_at=observed_at,
        events=events,
    )


def verify_original_signal(*, source_id: str, verification_runner: FixtureVerificationRunner) -> VerificationResult:
    probe = verification_runner.verify(source_id)
    healthy = probe["fixture_source_id"] == source_id and not probe["has_degradation"]
    observed_state: ObservedState = "healthy" if healthy else "degraded"
    return VerificationResult(source_id=source_id, healthy=healthy, observed_state=observed_state)


def _build_events(payload: dict, correlation_id: str | None = None, strict: bool = True) -> list[HealthEvent]:
    host_id = str(payload["host_id"])
    source_id = str(payload["source_id"])
    observed_at = float(payload["observed_at"])
    correlation_id = correlation_id or _stable_token("correlation", source_id, host_id)
    events: list[HealthEvent] = []

    cpu = payload.get("cpu")
    if isinstance(cpu, dict) and float(cpu.get("percent", 0.0)) >= float(cpu.get("threshold", 90.0)):
        events.append(
            _event(
                host_id,
                source_id,
                "cpu",
                "degraded",
                observed_at,
                correlation_id,
                [f"cpu:{_stable_token('cpu', source_id, cpu.get('percent'), cpu.get('threshold'))}"],
                "cpu pressure exceeded configured threshold",
            )
        )

    ram = payload.get("ram")
    if isinstance(ram, dict) and float(ram.get("percent", 0.0)) >= float(ram.get("threshold", 90.0)):
        events.append(
            _event(
                host_id,
                source_id,
                "ram",
                "degraded",
                observed_at,
                correlation_id,
                [f"ram:{_stable_token('ram', source_id, ram.get('percent'), ram.get('threshold'))}"],
                "ram pressure exceeded configured threshold",
            )
        )

    disk = payload.get("disk")
    if isinstance(disk, dict) and float(disk.get("percent", 0.0)) >= float(disk.get("threshold", 90.0)):
        events.append(
            _event(
                host_id,
                source_id,
                "disk",
                "degraded",
                observed_at,
                correlation_id,
                [f"disk:{_stable_token('disk', source_id, disk.get('percent'), disk.get('threshold'))}"],
                "disk pressure exceeded configured threshold",
            )
        )

    for service in payload.get("services", []):
        if isinstance(service, dict) and service.get("failed"):
            name = str(service.get("name", "service"))
            events.append(
                _event(
                    host_id,
                    source_id,
                    "service",
                    "degraded",
                    observed_at,
                    correlation_id,
                    [f"service:{_stable_token('service', source_id, name)}"],
                    f"systemd service failed: {name}",
                )
            )

    for log in payload.get("logs", []):
        if isinstance(log, dict) and _log_matches(log):
            keyword = str(log.get("keyword", "keyword"))
            evidence_ref = f"log:{_stable_token('log', source_id, log.get('path'), keyword, log.get('level'))}"
            events.append(
                _event(
                    host_id,
                    source_id,
                    "log",
                    "degraded",
                    observed_at,
                    correlation_id,
                    [evidence_ref],
                    str(log.get("redacted_summary") or f"error log matched configured keyword: {keyword}"),
                )
            )

    if strict and not events:
        raise ValueError("fixture does not contain a degraded signal")
    return events


def _event(
    host_id: str,
    source_id: str,
    signal_type: SignalType,
    severity: str,
    observed_at: float,
    correlation_id: str,
    evidence_refs: Sequence[str],
    summary: str,
) -> HealthEvent:
    dedup_key = _stable_token("dedup", source_id, signal_type, severity, tuple(evidence_refs), summary)
    return HealthEvent(
        host_id=host_id,
        source_id=source_id,
        signal_type=signal_type,
        severity=severity,
        observed_at=observed_at,
        dedup_key=dedup_key,
        correlation_id=correlation_id,
        evidence_refs=tuple(evidence_refs),
        summary=_redact(summary),
    )


def _stable_token(*parts: object) -> str:
    digest = sha256()
    for part in parts:
        digest.update(repr(part).encode("utf-8"))
        digest.update(b"\0")
    return digest.hexdigest()[:24]


def _redact(summary: str) -> str:
    return summary.replace("panic", "redacted").replace("raw", "redacted")


def _log_matches(log: dict) -> bool:
    level = str(log.get("level", "")).lower()
    keyword = str(log.get("keyword", "")).strip()
    line = str(log.get("line", ""))
    return level in {"error", "critical", "alert", "emergency"} and bool(keyword) and keyword.lower() in line.lower()
