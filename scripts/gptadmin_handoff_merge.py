#!/usr/bin/env python3
"""Merge two fenced GPTAdmin task snapshots without replaying any task."""
import argparse
import json
import os
from pathlib import Path
import sqlite3
import stat

TABLE_KEYS = {"task_records": ("kind", "key"), "task_controls": ("task_id",), "task_requests": ("key",)}
ALLOWED_TABLES = frozenset((*TABLE_KEYS, "task_status_counts", "task_store_meta"))

def regular(path):
    path = Path(path)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode):
        raise ValueError("snapshot must be a regular file")
    return path.resolve()

def schema(db, prefix, table):
    return [(x[1], x[2], x[3], x[5]) for x in db.execute(f"PRAGMA {prefix}.table_info({table})")]

def validate_schema(db, prefix):
    names = {r[0] for r in db.execute(f"SELECT name FROM {prefix}.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")}
    if names != ALLOWED_TABLES:
        raise ValueError("unexpected task database schema")
    if db.execute(f"PRAGMA {prefix}.quick_check").fetchone()[0] != "ok":
        raise ValueError("task snapshot failed integrity check")

def validate_counts(db):
    rows = db.execute("""
      SELECT count(*) FROM (
        SELECT kind,coalesce(json_extract(payload,'$.status'),'') AS status,
               coalesce(json_extract(payload,'$.server'),json_extract(payload,'$.agent_id'),'') AS server,
               count(*) AS n
        FROM task_records WHERE kind IN ('shell','relay') GROUP BY 1,2,3
        EXCEPT SELECT kind,status,server,n FROM task_status_counts WHERE n>0
      )""").fetchone()[0]
    reverse = db.execute("""
      SELECT count(*) FROM (
        SELECT kind,status,server,n FROM task_status_counts WHERE n>0
        EXCEPT SELECT kind,coalesce(json_extract(payload,'$.status'),''),
          coalesce(json_extract(payload,'$.server'),json_extract(payload,'$.agent_id'),''),count(*)
        FROM task_records WHERE kind IN ('shell','relay') GROUP BY 1,2,3
      )""").fetchone()[0]
    if rows or reverse:
        raise ValueError("task status index does not match history")

def merge(primary, other, output):
    primary, other, output = regular(primary), regular(other), Path(output)
    parent = output.parent.lstat()
    if not stat.S_ISDIR(parent.st_mode) or parent.st_mode & 0o077 or parent.st_uid != os.geteuid():
        raise ValueError("output directory must be owner-only and owned by the operator")
    fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    os.close(fd)
    db = source = None
    completed = False
    try:
        source = sqlite3.connect(primary.as_uri() + "?mode=ro", uri=True)
        db = sqlite3.connect(output)
        source.backup(db, pages=1024, sleep=0.05)
        source.close()
        source = None
        db.execute("ATTACH DATABASE ? AS other", (other.as_uri() + "?mode=ro",))
        validate_schema(db, "main")
        validate_schema(db, "other")
        for table in ALLOWED_TABLES:
            if schema(db, "main", table) != schema(db, "other", table):
                raise ValueError("task snapshot schema mismatch")
        if db.execute("SELECT id,version FROM main.task_store_meta").fetchall() != db.execute("SELECT id,version FROM other.task_store_meta").fetchall():
            raise ValueError("task store versions differ")
        validate_counts(db)
        report = {}
        with db:
            for table, keys in TABLE_KEYS.items():
                cols = [row[0] for row in schema(db, "main", table)]
                on = " AND ".join(f"a.{k}=b.{k}" for k in keys)
                diff = " OR ".join(f"a.{c} IS NOT b.{c}" for c in cols)
                if db.execute(f"SELECT 1 FROM main.{table} a JOIN other.{table} b ON {on} WHERE {diff} LIMIT 1").fetchone():
                    raise ValueError("conflicting task receipt")
                before = db.execute(f"SELECT count(*) FROM main.{table}").fetchone()[0]
                incoming = db.execute(f"SELECT count(*) FROM other.{table}").fetchone()[0]
                db.execute(f"INSERT OR IGNORE INTO main.{table} SELECT * FROM other.{table}")
                added = db.execute("SELECT changes()").fetchone()[0]
                after = db.execute(f"SELECT count(*) FROM main.{table}").fetchone()[0]
                if after != before + added:
                    raise ValueError("task merge count mismatch")
                report[table] = {"existing": before, "incoming": incoming, "added": added}
            validate_counts(db)
        if db.execute("PRAGMA quick_check").fetchone()[0] != "ok":
            raise ValueError("merged task database failed integrity check")
        db.close()
        db = None
        completed = True
        return report
    finally:
        if source is not None:
            source.close()
        if db is not None:
            db.close()
        if not completed:
            for suffix in ("", "-journal", "-wal", "-shm"):
                try:
                    os.unlink(str(output) + suffix)
                except FileNotFoundError:
                    pass

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--primary", required=True, type=Path)
    parser.add_argument("--other", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(merge(args.primary, args.other, args.output), sort_keys=True))

if __name__ == "__main__":
    main()
