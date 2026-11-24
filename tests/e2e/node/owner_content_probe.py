"""Authenticated MCP content acceptance on the real three-voter failover fixture."""
import json
import sqlite3


def execute(rpc, node, token, code):
    args = {"code": code}
    for attempt in range(2):
        status, response, _ = rpc(node, token, "tools/call", {"name": "exec", "arguments": args})
        assert status == 200 and response and "result" in response, ("MCP content", status)
        d = response["result"].get("structuredContent", {})
        if attempt == 0 and d.get("executed") is False and d.get("context"):
            args["context"] = d["context"]
            continue
        return d


def prepare(rpc, nodes, owner, token, wait_for):
    keep = execute(rpc, owner, token, "return (await mcp.content.index('preserved-oc1-marker',{name:'keep'})).source;")
    drop = execute(rpc, owner, token, "return (await mcp.content.index('revoked-oc1-marker',{name:'drop'})).source;")
    assert keep.get("status") == drop.get("status") == "completed", ("content creation", keep, drop)
    kept, deleted = keep["result"], drop["result"]
    assert kept.startswith("oc1.") and deleted.startswith("oc1.")
    removed = execute(rpc, owner, token, f"return await mcp.content.delete({json.dumps(deleted)});")
    assert removed.get("result", {}).get("deleted"), ("content delete", removed)
    # Restore the initial, independently verified pre-upgrade format by
    # inserting a fresh ID into the original authoritative schema only.
    ident = kept.rsplit(".", 1)[0] + ".c001c00100aafade0000123456789abc"
    with sqlite3.connect(nodes[owner]["dir"] / "exec-content.sqlite", timeout=5) as db:
        cols = "principal,name,origin,format,bytes,chunks,created,expires,indexed,complete"
        db.execute(f"INSERT INTO content_sources(id,{cols}) SELECT ?,{cols} FROM content_sources WHERE id=?", (ident, kept))
        db.execute("INSERT INTO content_chunks SELECT ?,principal,chunk_id,line_start,line_end,body FROM content_chunks WHERE source_id=?", (ident, kept))
        db.execute("INSERT INTO content_fts SELECT ?,principal,chunk_id,body FROM content_fts WHERE source_id=?", (ident, kept))
    members = sorted(n["id"] for n in nodes[:3])
    backup_id = members[(members.index(nodes[owner]["id"]) + 1) % len(members)]
    backup = next(n for n in nodes if n["id"] == backup_id)

    def copied():
        with sqlite3.connect((backup["dir"] / "tasks_state.sqlite").as_uri() + "?mode=ro", uri=True) as db:
            return db.execute("SELECT body IS NOT NULL FROM content_standby WHERE id=?", (ident,)).fetchone() == (1,)
    wait_for("voter backfilled a retained content source", copied, 100)
    return kept, deleted, ident


def verify(rpc, leader, original, outsider, ids):
    kept, deleted, historical = map(json.dumps, ids)
    for id_ in (kept, historical):
        script = f"""const s={id_};return {{status:(await mcp.content.status(s)).source,one:(await mcp.content.read(s,1)).text,all:(await mcp.content.readAll(s)).text,hits:(await mcp.content.search("preserved",{{source:s}})).count}};"""
        value = execute(rpc, leader, original, script)
        result = value.get("result", {})
        assert result.get("one") == result.get("all") == "preserved-oc1-marker", ("content after owner death", value)
        assert result.get("hits") == 1 and result.get("status") == json.loads(id_)
        denied = execute(rpc, leader, outsider, f"return (await mcp.content.readAll({id_})).text;")
        assert "preserved-oc1-marker" not in json.dumps(denied), "foreign principal read standby content"
    denied = execute(rpc, leader, original, f"return (await mcp.content.readAll({deleted})).text;")
    assert "revoked-oc1-marker" not in json.dumps(denied), "acknowledged delete reappeared"
    print("MCP content owner offline: original and backfilled oc1 verified by status/read/readAll/search; foreign caller and deleted source denied", flush=True)
