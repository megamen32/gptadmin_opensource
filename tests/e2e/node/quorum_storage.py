"""Inspect private fixture state after a real election, catch-up and Raft compaction."""
import asyncio
import base64
from contextlib import closing
from dataclasses import dataclass
import hashlib
from io import BytesIO
import json
import mmap
from pathlib import Path
import sqlite3
import struct
import subprocess
import time

from cryptography.hazmat.primitives.ciphers.aead import AESGCM


@dataclass
class Journal:
    begin: int
    end: int
    keyed: object
    unkeyed: list


def snapshots(node):
    return (node["dir"] / "raft" / "snapshots").rglob("state.bin")


INSTRUCTIONS = "/admin/api/instruction-sets/default"


def instruction_headers(request, leader, token):
    status, _, headers = request(leader, INSTRUCTIONS, token=token)
    if status != 200:
        raise RuntimeError(f"control checkpoint GET returned HTTP {status}")
    return headers


def put_instruction(request, leader, token, headers, content):
    etag = next(v for k, v in headers.items() if k.lower() == "etag")
    status, _, headers = request(leader, INSTRUCTIONS, {"content": content}, token,
                                 method="PUT", extra_headers={"If-Match": etag})
    if status != 200:
        raise RuntimeError(f"control checkpoint PUT returned HTTP {status}")
    return headers


async def snapshot_all(nodes, request, leader):
    checkpoint = request(leader, "/healthz")[1]["raft_applied_index"]
    # Production Raft compacts at 1024 entries. Update a constant-size, authentic control value
    # so every voter takes a snapshot newer than the marked results without changing Raft settings.
    token = nodes[leader]["env"]["CTL_TOKEN"]
    headers = instruction_headers(request, leader, token)
    for version in range(1050):
        headers = put_instruction(request, leader, token, headers, f"CI control checkpoint {version:04d}")
    deadline = time.monotonic() + 125
    while time.monotonic() < deadline:
        try:
            if all(any(json.loads((p.parent / "meta.json").read_text())["Index"] >= checkpoint
                       for p in snapshots(n)) for n in nodes):
                return
        except FileNotFoundError:
            pass
        await asyncio.sleep(1)
    raise RuntimeError(f"missing post-workload Raft snapshot at index >= {checkpoint}")


def needles(commands, content):
    tags = [content]
    for cmd in commands:
        tags.extend([cmd.payload_tag, cmd.output[:32], cmd.output[-32:]])
    patterns = []
    for tag in tags:
        raw = tag.encode()
        patterns.extend([raw, raw.hex().encode()])
        # A []byte embedded in JSON may have either Base64 alphabet and any byte alignment.
        for start in range(3):
            aligned = raw[start:start + (len(raw) - start) // 3 * 3]
            patterns.extend([base64.b64encode(aligned), base64.urlsafe_b64encode(aligned)])
    return tuple(set(patterns))


def clean(data, patterns, location):
    if any(data.find(pattern) >= 0 for pattern in patterns):
        raise RuntimeError(f"private payload, stdout or indexed content found in {location}")


def unseal(cipher, cluster, namespace, key, revision, value, patterns, location):
    aad = json.dumps(dict(cluster_id=cluster, namespace=namespace, key=key, revision=revision),
                     separators=(",", ":")).encode()
    while isinstance(value, dict) and {"nonce", "ciphertext"}.issubset(value):
        def decode(field):
            return base64.b64decode(value[field] + "==", validate=False)
        raw = cipher.decrypt(decode("nonce"), decode("ciphertext"), aad)
        clean(raw, patterns, location)
        value = json.loads(raw)
    return value


def inspect_sql(db, patterns, cipher, cluster, location):
    tables = [row[0] for row in db.execute("SELECT name FROM sqlite_schema WHERE type='table'")]
    for table in tables:
        quoted = '"' + table.replace('"', '""') + '"'
        for row in db.execute("SELECT * FROM " + quoted):
            for cell in row:
                if isinstance(cell, (str, bytes)):
                    clean(cell.encode() if isinstance(cell, str) else cell, patterns, location + "/" + table)
    if "task_quorum_authority" in tables:
        rows = db.execute("SELECT namespace,row_key,revision,payload FROM task_quorum_authority")
        for namespace, key, revision, payload in rows:
            unseal(cipher, cluster, namespace, key, revision, json.loads(payload), patterns, location)


def inspect_files(directory, patterns, cipher, cluster):
    count = 0
    for path in directory.rglob("*"):
        if path.is_symlink() or not path.is_file() or not path.stat().st_size:
            continue
        location = str(path.relative_to(directory.parent))
        with path.open("rb") as f, mmap.mmap(f.fileno(), 0, access=mmap.ACCESS_READ) as data:
            clean(data, patterns, location)
            sqlite = data[:16] == b"SQLite format 3\x00"
        if sqlite:
            with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as db:
                inspect_sql(db, patterns, cipher, cluster, location)
        count += 1
    if count == 0:
        raise RuntimeError(f"empty fixture directory {directory.name}")
    return count


def check_mutation(mutation, cipher, cluster, patterns, location):
    record = mutation["record"]
    value = record["value"]
    if not isinstance(value, dict) or not {"nonce", "ciphertext"}.issubset(value):
        raise RuntimeError(f"unrecognized external mutation encoding in {location}")
    unseal(cipher, cluster, mutation["namespace"], mutation["key"], record["revision"], value, patterns, location)


def inspect_snapshot(node, cipher, cluster, patterns, journal):
    sizes = []
    for path in snapshots(node):
        raw = path.read_bytes()
        location = node["dir"].name + "/raft/snapshot"
        clean(raw, patterns, location)
        if raw[:8] != b"GPTQCO1\n":
            raise RuntimeError("expected an owner-local Raft snapshot")
        length = struct.unpack(">Q", raw[8:16])[0]
        header = json.loads(raw[16:16 + length])
        check_slots(header["image"].get("operations", {}), journal)
        body = raw[16 + length:]
        if (header["image"].get("cluster_id") != cluster or len(body) != header["external_size"]
                or not body.startswith(b"GPTCTRL1\n") or hashlib.sha256(body[:-32]).digest() != body[-32:]):
            raise RuntimeError("invalid control snapshot envelope or checksum")
        frames = BytesIO(body[len(b"GPTCTRL1\n"):-32])

        def frame():
            size = struct.unpack(">I", frames.read(4))[0]
            return json.loads(frames.read(size))

        checkpoint = frame()
        if (checkpoint["cluster"] != cluster or checkpoint["sequence"] != header["image"]["external_sequence"]
                or checkpoint["rows"] < 1):
            raise RuntimeError("missing authority checkpoint in owner-local snapshot")
        for _ in range(checkpoint["rows"]):
            check_mutation(frame(), cipher, cluster, patterns, location)
        if frames.read(1):
            raise RuntimeError("uninspected trailing bytes in snapshot")
        sizes.append(len(raw))
    if not sizes:
        raise RuntimeError("no Raft snapshot to inspect")
    return max(sizes)


def check_slots(operations, journal):
    jobs = [op.get("job_id") for op in operations.values()]
    if jobs.count(journal.keyed.task_id) != 1 or any(cmd.task_id in jobs for cmd in journal.unkeyed):
        raise RuntimeError("snapshot did not retain exactly one keyed slot and zero unkeyed slots")


def inspect_log(node, cipher, cluster, patterns, samples, journal):
    location = node["dir"].name + "/raft/log"
    sizes = [0, 0]
    counts = [0, 0]
    encrypted = 0
    journal_entries = 0
    with subprocess.Popen(["/usr/local/bin/quorum-log-read", node["dir"] / "raft" / "raft.db"],
                          stdout=subprocess.PIPE) as process:
        while header := process.stdout.read(16):
            index, length = struct.unpack(">QQ", header)
            if length > 16 << 20:
                raise RuntimeError("oversized Raft command in fixture")
            raw = process.stdout.read(length)
            clean(raw, patterns, location)
            command = json.loads(raw)
            if command.get("cluster_id") != cluster:
                raise RuntimeError("unexpected cluster in voter log")
            if journal.begin < index <= journal.end:
                journal_entries += 1
                if command.get("operation"):
                    raise RuntimeError(f"{location}: unkeyed work or ordinary reads wrote to the compact journal")
            for mutation in command.get("mutations") or []:
                record = mutation["record"]
                if mutation.get("external") and not record.get("deleted"):
                    check_mutation(mutation, cipher, cluster, patterns, location)
                    encrypted += 1
            for i, (begin, end, _) in enumerate(samples):
                if begin < index <= end:
                    sizes[i] += length
                    counts[i] += 1
        if process.wait() or not encrypted or not all(counts) or not journal_entries:
            raise RuntimeError("missing authenticated Raft commands or sample intervals")
    growth = sum(len(c.output) for c in samples[1][2]) - sum(len(c.output) for c in samples[0][2])
    # With three 48 KiB results a single extra copy already exceeds this allowance.
    # This measures real serialized command bytes, independent of TLS/heartbeat timing and Bolt page slack.
    if sizes[1] - sizes[0] > growth // 4:
        raise RuntimeError(f"{location}: Raft grew by {sizes[1] - sizes[0]} bytes for {growth} added stdout bytes")
    return sizes


def check_peer_task_rows(node, commands):
    # A sealed queued payload can be invisible to a byte scan. Look up the exact
    # operations as well, allowing the small ownership/deduplication metadata.
    path = node["dir"] / "tasks_state.sqlite"
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as db:
        for command in commands:
            rows = db.execute("SELECT payload FROM task_records WHERE kind IN ('shell','relay') AND key=?",
                              (command.task_id,))
            for payload, in rows:
                job = json.loads(payload)
                if any(job.get(field) for field in ("arguments", "params", "sealed_pending", "result", "response")):
                    raise RuntimeError(f"{node['dir'].name} retained a foreign task body")


def inspect(nodes, polling, history, storage_owner, content, samples, journal):
    historical, historical_content = history
    current = [c for _, _, commands in samples for c in commands] + journal.unkeyed
    old_tags = needles([historical], historical_content)
    new_tags = needles(current, content)
    all_tags = tuple(set(old_tags + new_tags))
    config = json.loads((nodes[0]["dir"] / "quorum.json").read_text())
    cipher = AESGCM(Path(config["state_key_file"]).read_bytes())
    cluster = config["cluster_id"]
    for i, node in enumerate(nodes):
        foreign = (() if i == 0 else old_tags) + (() if i == storage_owner else new_tags)
        check_peer_task_rows(node, ([] if i == 0 else [historical]) + ([] if i == storage_owner else current))
        count = inspect_files(node["dir"], foreign, cipher, cluster)
        sizes = inspect_log(node, cipher, cluster, all_tags, samples, journal)
        snapshot_size = inspect_snapshot(node, cipher, cluster, all_tags, journal)
        print(f"node{i}: {count} files scanned, decrypted Raft log; 3x64B / 3x48KiB stdout cost "
              f"{sizes[0]} / {sizes[1]} log bytes, snapshot {snapshot_size} bytes", flush=True)
    print("ordinary reads and two unkeyed jobs: zero journal operations; one slot for the keyed replay", flush=True)
    count = inspect_files(polling.directory, all_tags, cipher, cluster)
    print(f"standalone: {count} files including logs and spool scanned for foreign bodies", flush=True)
