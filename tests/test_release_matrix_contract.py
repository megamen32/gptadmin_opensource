from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[1]


def test_release_builder_defines_complete_user_facing_matrix() -> None:
    source = (ROOT / "tools" / "build.sh").read_text(encoding="utf-8")

    for platform in ("windows", "macos", "ubuntu", "android"):
        for arch in ("x64", "arm64"):
            for edition in ("full", "client"):
                assert f'emit_release_bundle "{platform}" "{arch}" "{edition}"' in source

    assert 'gptadmin-checksums.txt' in source
    assert 'gptadmin-release-matrix.json' in source
    assert 'build_hub_cross_platforms matrix' in source
    assert 'build_go_shellmcp_cross_platforms matrix' in source
    assert 'cp -f cli.py "$tmp/cli/gptadmin.py"' in source
    assert '"size": path.stat().st_size' in source
    assert '"build_version": build_version' in source


def test_public_release_uploads_only_the_concise_matrix() -> None:
    workflow_path = ROOT / ".github" / "workflows" / "build-and-sync.yml"
    if not workflow_path.exists():
        pytest.skip("private release workflow is intentionally absent from the public mirror")
    workflow = workflow_path.read_text(encoding="utf-8")

    assert 'gptadmin-{windows,macos,ubuntu,android}-{x64,arm64}-{full,client}.{zip,tar.gz}' in workflow
    assert 'build/gptadmin-checksums.txt' in workflow
    assert 'build/gptadmin-release-matrix.json' in workflow
    assert 'ANDROID_X86_64_CC=' in workflow
