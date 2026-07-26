#!/usr/bin/env python3
"""Generate a deterministic SPDX-style SBOM for a GPTAdmin release."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path
from typing import Any


def package_id(name: str, version: str) -> str:
    """Return a stable SPDX package identifier."""

    digest = hashlib.sha256(f"{name}\0{version}".encode()).hexdigest()[:24]
    return f"SPDXRef-Package-{digest}"


def add_package(packages: dict[tuple[str, str], dict[str, str]], name: str, version: str, source: str) -> None:
    """Add one normalized dependency to the package set."""

    name = name.strip()
    version = version.strip()
    if not name or not version:
        return
    key = (name, version)
    packages[key] = {"name": name, "version": version, "source": source}


def parse_python_dependencies(root: Path, packages: dict[tuple[str, str], dict[str, str]]) -> None:
    """Record Python project requirements without resolving unpinned versions."""

    path = root / "pyproject.toml"
    if not path.is_file():
        return
    text = path.read_text(encoding="utf-8")
    dependency_lines = re.findall(r"^\s*dependencies\s*=\s*\[(.*?)\]", text, flags=re.MULTILINE | re.DOTALL)
    dev_lines = re.findall(r"^\s*dev\s*=\s*\[(.*?)\]", text, flags=re.MULTILINE | re.DOTALL)
    for block in dependency_lines + dev_lines:
        for requirement in re.findall(r"[\"']([^\"']+)[\"']", block):
            match = re.match(r"\s*([A-Za-z0-9_.-]+)(?:\[.*?\])?\s*(.*)$", requirement)
            if match:
                add_package(packages, match.group(1), match.group(2) or "UNRESOLVED", "pyproject.toml")


def parse_go_mod(path: Path, packages: dict[tuple[str, str], dict[str, str]]) -> None:
    """Record direct and indirect Go module requirements."""

    if not path.is_file():
        return
    in_require = False
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.split("//", 1)[0].strip()
        if line == "require (":
            in_require = True
            continue
        if in_require and line == ")":
            in_require = False
            continue
        if line.startswith("require "):
            line = line[len("require "):].strip()
        if not in_require and not raw_line.strip().startswith("require "):
            continue
        fields = line.split()
        if len(fields) >= 2:
            add_package(packages, fields[0], fields[1], path.relative_to(path.parents[1]).as_posix())


def parse_package_lock(path: Path, packages: dict[tuple[str, str], dict[str, str]]) -> None:
    """Record resolved npm packages from a lockfile."""

    if not path.is_file():
        return
    data = json.loads(path.read_text(encoding="utf-8"))
    for location, package in data.get("packages", {}).items():
        if not isinstance(package, dict) or not package.get("version"):
            continue
        name = package.get("name")
        if not name:
            name = location.rsplit("node_modules/", 1)[-1]
        add_package(packages, str(name), str(package["version"]), path.relative_to(path.parents[1]).as_posix())


def build_sbom(root: Path, *, build_version: int, build_ts: str, git_commit: str) -> dict[str, Any]:
    """Collect dependency manifests into a deterministic SPDX document."""

    packages: dict[tuple[str, str], dict[str, str]] = {}
    parse_python_dependencies(root, packages)
    for path in sorted(root.glob("go-*/go.mod")):
        parse_go_mod(path, packages)
    parse_package_lock(root / "admin-ui" / "package-lock.json", packages)
    package_entries = []
    for item in sorted(packages.values(), key=lambda value: (value["name"].lower(), value["version"], value["source"])):
        package_entries.append(
            {
                "SPDXID": package_id(item["name"], item["version"]),
                "name": item["name"],
                "versionInfo": item["version"],
                "downloadLocation": "NOASSERTION",
                "filesAnalyzed": False,
                "supplier": "NOASSERTION",
                "externalRefs": [{"referenceCategory": "OTHER", "referenceType": "gptadmin:source", "referenceLocator": item["source"]}],
            }
        )
    return {
        "spdxVersion": "SPDX-2.3",
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": "GPTAdmin",
        "documentNamespace": f"https://gptadmin.local/sbom/{build_version}/{git_commit}",
        "creationInfo": {"created": build_ts, "creators": ["Tool: GPTAdmin generate_sbom.py"]},
        "gptadminBuild": {"build_version": build_version, "build_ts": build_ts, "git_commit": git_commit},
        "packages": package_entries,
    }


def main() -> int:
    """Parse arguments, generate the document and atomically write it."""

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--build-version", type=int, required=True)
    parser.add_argument("--build-ts", required=True)
    parser.add_argument("--git-commit", required=True)
    args = parser.parse_args()
    payload = build_sbom(args.root.resolve(), build_version=args.build_version, build_ts=args.build_ts, git_commit=args.git_commit)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    temporary = args.output.with_name(f".{args.output.name}.tmp")
    temporary.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(args.output)
    print(f"generated SBOM: {args.output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
