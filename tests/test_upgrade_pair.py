"""Exercise real filesystem/SQLite backups with system services replaced by spies."""
from pathlib import Path
from types import SimpleNamespace
import importlib.util
import json
import sqlite3
import pytest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("upgrade_pair", ROOT / "scripts/gptadmin_upgrade_pair.py")
upgrade = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)

@pytest.fixture
def pair(tmp_path, monkeypatch):
    runtime = tmp_path / "runtime"
    config = tmp_path / "config"
    (runtime / "bin").mkdir(parents=True)
    (runtime / "public/admin").mkdir(parents=True)
    (runtime / "bin/gptadmin_hub").write_bytes(b"original")
    (runtime / "public/admin/index.html").write_text("original-ui")
    config.mkdir()
    with sqlite3.connect(config / "tasks_state.sqlite") as db:
        db.executescript("CREATE TABLE task_store_meta(id INTEGER PRIMARY KEY,version INTEGER); INSERT INTO task_store_meta VALUES(1,1); CREATE TABLE task_records(kind TEXT,key TEXT,payload TEXT);")
        db.execute("INSERT INTO task_records VALUES(?,?,?)", ("shell", "old", '{"status":"completed"}'))
    upstream = tmp_path / "upstream.conf"
    upstream.write_text("original-upstream")
    monkeypatch.setattr(upgrade, "ROOT", runtime)
    monkeypatch.setattr(upgrade, "CONFIG", config)
    monkeypatch.setattr(upgrade, "UPSTREAM", upstream)
    monkeypatch.setattr(upgrade, "WATCHERS", ())
    calls = []
    monkeypatch.setattr(upgrade, "command", lambda *args: calls.append(args))
    def healthy(expected):
        assert (runtime / "bin/gptadmin_hub").read_text() == expected
        with sqlite3.connect(config / "tasks_state.sqlite") as db:
            db.execute("UPDATE task_store_meta SET version=2")
        return {"9001": {"git_commit": expected}, "19001": {"git_commit": expected}}
    monkeypatch.setattr(upgrade, "healthy", healthy)
    for name in ("candidate", "fallback"):
        (tmp_path / name).write_text(name)
        (tmp_path / (name + "-ui")).mkdir()
        (tmp_path / (name + "-ui") / "index.html").write_text(name + "-ui")
    bundle = tmp_path / "bundle"
    upgrade.prepare(SimpleNamespace(bundle=bundle, candidate=tmp_path / "candidate", fallback=tmp_path / "fallback", ui=tmp_path / "candidate-ui", fallback_ui=tmp_path / "fallback-ui", commit="candidate", fallback_commit="fallback"))
    return SimpleNamespace(bundle=bundle, config=config, runtime=runtime, upstream=upstream, calls=calls, healthy=healthy)

def test_pair_upgrade_and_compatible_rollback_preserve_new_tasks(pair):
    upgrade.transition(pair.bundle, False)
    assert json.loads((pair.bundle / "receipt.json").read_text())["status"] == "applied"
    with sqlite3.connect(pair.config / "tasks_state.sqlite") as db:
        db.execute("INSERT INTO task_records VALUES(?,?,?)", ("shell", "new", '{"status":"completed"}'))
    upgrade.transition(pair.bundle, True)
    with sqlite3.connect(pair.config / "tasks_state.sqlite") as db:
        assert db.execute("SELECT COUNT(*) FROM task_records").fetchone()[0] == 2
        assert db.execute("SELECT version FROM task_store_meta").fetchone()[0] == 2
    assert (pair.runtime / "bin/gptadmin_hub").read_text() == "fallback"
    assert pair.upstream.read_text() == "original-upstream"
    assert ("systemctl", "stop", *upgrade.SERVICES) in pair.calls

def test_candidate_failure_keeps_local_results_and_uses_compatible_fallback(pair, monkeypatch):
    def healthy(expected):
        if expected == "candidate":
            with sqlite3.connect(pair.config / "tasks_state.sqlite") as db:
                db.execute("UPDATE task_store_meta SET version=2")
                db.execute("INSERT INTO task_records VALUES(?,?,?)", ("shell", "delivered", '{"status":"completed"}'))
            raise RuntimeError("injected candidate failure")
        return pair.healthy(expected)
    monkeypatch.setattr(upgrade, "healthy", healthy)
    with pytest.raises(RuntimeError, match="injected"):
        upgrade.transition(pair.bundle, False)
    assert (pair.runtime / "bin/gptadmin_hub").read_text() == "fallback"
    with sqlite3.connect(pair.config / "tasks_state.sqlite") as db:
        assert db.execute("SELECT COUNT(*) FROM task_records").fetchone()[0] == 2
    assert json.loads((pair.bundle / "receipt.json").read_text())["status"] == "failed_candidate_fell_back"

def test_pending_tasks_block_before_any_service_change(pair):
    with sqlite3.connect(pair.config / "tasks_state.sqlite") as db:
        db.execute("INSERT INTO task_records VALUES(?,?,?)", ("shell", "pending", '{"status":"queued"}'))
    with pytest.raises(RuntimeError, match="queued/approval"):
        upgrade.transition(pair.bundle, False)
    assert pair.calls == []
    assert (pair.runtime / "bin/gptadmin_hub").read_text() == "original"
