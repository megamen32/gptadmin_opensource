import importlib.util
import json
import sqlite3
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("handoff_merge", ROOT / "scripts/gptadmin_handoff_merge.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

def create(path, job_key, result, request_key):
    db = sqlite3.connect(path)
    db.executescript("""
    CREATE TABLE task_records (kind TEXT NOT NULL, key TEXT NOT NULL, updated REAL NOT NULL, done REAL NOT NULL DEFAULT 0, payload BLOB NOT NULL, revision INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(kind,key)) WITHOUT ROWID;
    CREATE TABLE task_controls (task_id TEXT PRIMARY KEY, server TEXT NOT NULL, next_attempt REAL NOT NULL DEFAULT 0);
    CREATE TABLE task_requests (key TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, owner TEXT NOT NULL, created REAL NOT NULL, job_id TEXT NOT NULL, response BLOB, status INTEGER NOT NULL);
    CREATE TABLE task_status_counts(kind TEXT NOT NULL,status TEXT NOT NULL,server TEXT NOT NULL,n INTEGER NOT NULL,PRIMARY KEY(kind,status,server)) WITHOUT ROWID;
    CREATE TABLE task_store_meta(id INTEGER PRIMARY KEY,version INTEGER NOT NULL);
    INSERT INTO task_store_meta VALUES(1,1);
    CREATE TRIGGER task_counts_insert AFTER INSERT ON task_records BEGIN
      INSERT INTO task_status_counts VALUES(new.kind,coalesce(json_extract(new.payload,'$.status'),''),coalesce(json_extract(new.payload,'$.server'),json_extract(new.payload,'$.agent_id'),''),1)
      ON CONFLICT(kind,status,server) DO UPDATE SET n=n+1;
    END;
    """)
    with db:
        db.execute("insert into task_records values(?,?,?,?,?,?)", ("shell",job_key,1,1,json.dumps({"status":"completed","server":"test","result":result}),1))
        db.execute("insert into task_requests values(?,?,?,?,?,?,?)", (request_key,"fp","owner",1,job_key,b"ok",200))
    db.close()

def test_merges_both_histories_and_preserves_sources(tmp_path):
    first,second,out=(tmp_path/x for x in ("first.sqlite","second.sqlite","merged.sqlite"))
    create(first,"task-a","a","request-a")
    create(second,"task-b","b","request-b")
    report=module.merge(first,second,out)
    assert report["task_records"]["added"]==1
    assert report["task_requests"]["added"]==1
    db=sqlite3.connect(out)
    assert db.execute("select count(*) from task_records").fetchone()[0]==2
    assert db.execute("select sum(n) from task_status_counts").fetchone()[0]==2
    db.close()
    assert sqlite3.connect(first).execute("select count(*) from task_records").fetchone()[0]==1
    assert sqlite3.connect(second).execute("select count(*) from task_records").fetchone()[0]==1

def test_conflicting_receipt_fails_and_removes_partial(tmp_path):
    first,second,out=(tmp_path/x for x in ("first.sqlite","second.sqlite","merged.sqlite"))
    create(first,"same","a","request-a")
    create(second,"same","different","request-b")
    with pytest.raises(ValueError,match="conflicting"):
        module.merge(first,second,out)
    assert not out.exists()

def test_duplicate_identical_rows_are_not_replayed(tmp_path):
    first,second,out=(tmp_path/x for x in ("first.sqlite","second.sqlite","merged.sqlite"))
    create(first,"same","a","request")
    create(second,"same","a","request")
    report=module.merge(first,second,out)
    assert report["task_records"]["added"]==0
    assert report["task_requests"]["added"]==0

def test_existing_output_is_not_overwritten(tmp_path):
    first,second,out=(tmp_path/x for x in ("first.sqlite","second.sqlite","merged.sqlite"))
    create(first,"one","a","a")
    create(second,"two","b","b")
    out.write_bytes(b"preserve")
    with pytest.raises(FileExistsError):
        module.merge(first,second,out)
    assert out.read_bytes()==b"preserve"

def test_unknown_task_tables_and_bad_index_fail_closed(tmp_path):
    first,second,out=(tmp_path/x for x in ("first.sqlite","second.sqlite","merged.sqlite"))
    create(first,"one","a","a")
    create(second,"two","b","b")
    db=sqlite3.connect(second)
    db.execute("create table unexpected(value text)")
    db.close()
    with pytest.raises(ValueError,match="schema"):
        module.merge(first,second,out)
    assert not out.exists()
    db=sqlite3.connect(second)
    db.execute("drop table unexpected")
    db.close()
    db=sqlite3.connect(first)
    db.execute("delete from task_status_counts")
    db.commit()
    db.close()
    with pytest.raises(ValueError,match="status index"):
        module.merge(first,second,out)
    assert not out.exists()


def test_insecure_output_directory_is_rejected(tmp_path):
    first,second=(tmp_path/x for x in ("first.sqlite","second.sqlite"))
    create(first,"one","a","a")
    create(second,"two","b","b")
    unsafe=tmp_path/"shared"
    unsafe.mkdir(mode=0o755)
    unsafe.chmod(0o755)
    with pytest.raises(ValueError,match="owner-only"):
        module.merge(first,second,unsafe/"merged.sqlite")
    assert not (unsafe/"merged.sqlite").exists()
