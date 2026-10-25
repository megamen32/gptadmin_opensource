"""Local logic only: SQLite and localhost HTTP; no deployment or native-Codex claims."""
import importlib.util
import json
import sqlite3
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch
from http.server import BaseHTTPRequestHandler, HTTPServer

SPEC = importlib.util.spec_from_file_location("ci_autofix", Path(__file__).parents[1] / "scripts/github_ci_autofix.py")
ci = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ci)


class WatcherTests(unittest.TestCase):
    def setUp(self):
        project_temp = Path(__file__).parents[1] / ".tmp"
        project_temp.mkdir(exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=project_temp, prefix="ci-autofix-test-")
        self.index_path = Path(self.temp.name) / "native-index.sqlite3"
        self.index_patch = patch.object(ci, "NATIVE_INDEX", self.index_path)
        self.index_patch.start()
        self.db = ci.connect(Path(self.temp.name) / "state.sqlite3")
        run = {"databaseId": 10, "runAttempt": 1, "url": "https://github.com/megamen32/demo/actions/runs/10",
               "createdAt": "2026-10-08T10:00:00Z", "workflow": {"databaseId": 3}}
        self.item = ci.event("megamen32/demo", "abc", "main", run, {})
        self.db.execute("""INSERT INTO events(key,repo,head,branch,run,attempt,workflow,url,created,name,cwd,seen,updated)
          VALUES(:key,:repo,:head,:branch,:run,:attempt,:workflow,:url,:created,:name,:cwd,:seen,:updated)""", self.item)
        self.db.execute("UPDATE events SET incident_id='local-test-incident'")
        self.db.commit()

    def tearDown(self):
        self.db.close()
        self.index_patch.stop()
        self.temp.cleanup()

    def state(self):
        return dict(self.db.execute("SELECT * FROM events").fetchone())

    def api(self, path, **kwargs):
        if path == "repos/megamen32/demo":
            return {"default_branch": "main"}
        if path == "rate_limit":
            return {"resources": {"core": {"remaining": 5000}}}
        if "/actions/workflows/" in path and "/runs?" in path:
            attempt = self.db.execute("SELECT max(attempt) FROM events WHERE run=10").fetchone()[0]
            return {"total_count": 1, "workflow_runs": [self.rest_run(10, attempt=attempt)]}
        if "/branches/" in path:
            return {"commit": {"sha": "abc"}}
        if "/workflows/" in path:
            return {"state": "active"}
        return {"status": "completed", "conclusion": "failure", "head_sha": "abc", "head_branch": "main", "event": "push", "run_attempt": 1}

    def native(self, status="running", *, completed=False, permission=None, **meta):
        # OpenCode session: id == native id; model carries the explicit
        # provider/model; meta only carries createdAt. A session that
        # already finished its first turn shows messageCount >= 1 and a
        # lastActivity later than createdAt. A permission-pending turn is
        # distinguished by needsPermission + permissionDetails.
        session = {"id": "native-id", "harness": "opencode", "title": self.item["name"], "cwd": ci.FALLBACK,
                   "status": status, "model": ci.PROVIDER_MODEL,
                   "messageCount": 1 if completed else 0,
                   "lastActivity": "2026-10-09T00:00:05Z" if completed else "2026-10-09T00:00:00Z",
                   "needsPermission": bool(permission),
                   "meta": {"createdAt": "2026-10-09T00:00:00Z", **meta}}
        if permission:
            session["permissionDetails"] = {"id": permission, "type": "tool", "description": "awaiting"}
        return session

    def native_codex(self, status="running", **meta):
        return {"id": "codex-native-id", "harness": "codex", "title": self.item["name"], "cwd": ci.FALLBACK,
                "status": status, "meta": {"nativeSessionId": "codex-native-id", "transport": "codex-app-server", **meta}}

    def readback(self, session):
        return lambda name, args: {"sessions": [session]} if name == "list_agents" else {"session": session}

    def test_intent_is_durable_before_dispatch_and_timeout_never_resends(self):
        calls = []
        def lost(name, args):
            other = ci.connect(Path(self.temp.name) / "state.sqlite3")
            self.assertEqual(other.execute("SELECT state FROM events").fetchone()[0], "dispatching")
            other.close()
            calls.append(args)
            raise TimeoutError()
        ci.dispatch(self.db, {}, lost, self.api)
        ci.dispatch(self.db, {}, lost, self.api)
        self.assertEqual(len(calls), 1)
        self.assertEqual(self.state()["state"], "dispatching")
        self.assertEqual(calls[0]["name"], self.item["name"])
        # The new routing must always carry the explicit OpenCode harness,
        # the exact provider/model and the 24h dedup inputId; a missing
        # or different value is the silent downgrade we are guarding against.
        self.assertEqual(calls[0]["harness"], "opencode")
        self.assertEqual(calls[0]["model"], "zai-coding-plan/glm-5.3-flash")
        self.assertEqual(calls[0]["inputId"], "gptadmin-ci-autofix:" + self.item["key"])
        self.assertEqual(calls[0]["create"], "if_missing")
        self.assertEqual(calls[0]["activation"], "always")

    def test_ack_is_not_running_native_proof_is_required(self):
        ci.dispatch(self.db, {}, lambda *_: {"ok": True}, self.api)
        self.assertEqual(self.state()["state"], "accepted_unconfirmed")
        # A freshly-idle session (just created, turn not started) must not
        # advance the slot. The dispatch ack alone is not running proof.
        ci.reconcile(self.db, self.readback(self.native("idle")))
        self.assertEqual(self.state()["state"], "accepted_unconfirmed")
        # status=running is the OpenCode proof of an active turn.
        ci.reconcile(self.db, self.readback(self.native("running")))
        self.assertEqual(self.state()["state"], "running")
        # Only a turn that was actually observed running may transition to
        # awaiting_ci on idle. A first-poll false-idle while the session is
        # still working must not falsely complete the dispatch reservation.
        ci.reconcile(self.db, self.readback(self.native("idle")))
        self.assertEqual(self.state()["state"], "running")
        ci.reconcile(self.db, self.readback(self.native("idle", completed=True)))
        self.assertEqual(self.state()["state"], "awaiting_ci")

    def test_known_id_never_calls_slow_listing_before_direct_info(self):
        ci.dispatch(self.db, {}, lambda *_: {"ok": True, "sessionId": "native-id"}, self.api)
        calls = []
        def call(name, args):
            calls.append(name)
            self.assertEqual(name, "agent_info", "Known native ID must never list agents")
            return {"session": self.native("idle", completed=True)}
        ci.reconcile(self.db, call)
        self.assertEqual(calls, ["agent_info"])
        # The Herder's first observation may report a misleading idle; the
        # reservation must stay open until a running turn is confirmed. Then
        # the real completion advances the slot.
        self.assertEqual(self.state()["state"], "accepted_unconfirmed")
        def running_call(name, args):
            return {"session": self.native("running")}
        ci.reconcile(self.db, running_call)
        self.assertEqual(self.state()["state"], "running")
        ci.reconcile(self.db, self.readback(self.native("idle", completed=True)))
        self.assertEqual(self.state()["state"], "awaiting_ci")

    def test_lost_delivery_reply_exact_readonly_index_then_verified_completion(self):
        with sqlite3.connect(self.index_path) as index:
            index.execute("CREATE TABLE threads(id TEXT, name TEXT, cwd TEXT)")
            index.executemany("INSERT INTO threads VALUES(?,?,?)", [
                ("wrong-cwd", self.item["name"], "/somewhere/else"),
                ("wrong-name", "other-task", ci.FALLBACK),
                ("native-id", self.item["name"], ci.FALLBACK)])
        before = self.index_path.read_bytes()
        ci.dispatch(self.db, {}, lambda *_: (_ for _ in ()).throw(TimeoutError()), self.api)
        calls = []
        def call(name, args):
            calls.append(name)
            self.assertEqual(name, "agent_info", "Exact native metadata must avoid slow listing")
            self.assertEqual(args["sessionId"], "native-id")
            return {"session": self.native("idle", completed=True)}
        ci.reconcile(self.db, call)
        self.assertEqual(calls, ["agent_info"])
        self.assertEqual(self.state()["session_id"], "native-id")
        # The reservation is still held: the index provided an id but the
        # session never reported a running turn through reconcile, so the
        # first idle observation must not advance the event to awaiting_ci.
        self.assertEqual(self.state()["state"], "dispatching")
        self.assertEqual(self.index_path.read_bytes(), before)

    def test_index_metadata_is_not_proof_and_ambiguous_index_does_not_list(self):
        with sqlite3.connect(self.index_path) as index:
            index.execute("CREATE TABLE threads(id TEXT, name TEXT, cwd TEXT)")
            index.execute("INSERT INTO threads VALUES(?,?,?)", ("native-id", self.item["name"], ci.FALLBACK))
        ci.change(self.db, self.state(), "dispatching")
        bad = self.native(activeTurnId="turn1")
        bad["meta"]["transport"] = "other-transport"
        ci.reconcile(self.db, lambda *_: {"session": bad})
        self.assertEqual(self.state()["state"], "dispatching")
        self.assertIsNone(self.state()["session_id"])
        with sqlite3.connect(self.index_path) as index:
            index.execute("INSERT INTO threads VALUES(?,?,?)", ("second-id", self.item["name"], ci.FALLBACK))
        ci.reconcile(self.db, lambda *_: self.fail("Ambiguous index must neither list nor guess"))
        self.assertEqual(self.state()["state"], "dispatching")

    def test_missing_index_uses_folder_filtered_listing(self):
        # The folder-filtered listing must use the event's recorded harness
        # (opencode for new dispatches) and confirm the explicit model.
        ci.change(self.db, self.state(), "dispatching")
        self.db.execute("UPDATE events SET harness='opencode' WHERE key=?", (self.item["key"],))
        self.db.commit()
        def call(name, args):
            if name == "list_agents":
                self.assertEqual(args["folder"], ci.FALLBACK)
                self.assertEqual(args["limit"], 100)
                self.assertEqual(args["harness"], "opencode")
                return {"sessions": [self.native("running")]}
            self.assertEqual(args["harness"], "opencode")
            return {"session": self.native("running")}
        ci.reconcile(self.db, call)
        self.assertEqual(self.state()["state"], "running")

    def test_manual_stop_is_terminal_and_is_not_overridden(self):
        ci.dispatch(self.db, {}, lambda *_: {"ok": True}, self.api)
        ci.reconcile(self.db, self.readback(self.native("stopped")))
        self.assertEqual(self.state()["state"], "stopped")
        calls = []
        ci.dispatch(self.db, {}, lambda *a: calls.append(a), self.api)
        self.assertEqual(calls, [])

    def test_stale_head_and_disabled_workflow_are_not_dispatched(self):
        for alter in ("stale", "disabled"):
            self.db.execute("UPDATE events SET state='queued'")
            def api(path):
                value = self.api(path)
                if alter == "stale" and "/branches/" in path:
                    return {"commit": {"sha": "new"}}
                if alter == "disabled" and "/workflows/" in path:
                    return {"state": "disabled_manually"}
                return value
            ci.dispatch(self.db, {}, lambda *_: self.fail("must not dispatch"), api)
            self.assertEqual(self.state()["state"], "obsolete")

    def test_failed_same_run_or_partial_rollup_cannot_be_green(self):
        self.db.execute("UPDATE events SET state='awaiting_ci'")
        run = {"databaseId": 10, "runAttempt": 1, "createdAt": self.item["created"], "workflow": {"databaseId": 3}}
        check = {"status": "COMPLETED", "conclusion": "SUCCESS", "checkSuite": {"workflowRun": run}}
        ci.verify_green(self.db, {"megamen32/demo": ("abc", {"nodes": [check], "totalCount": 1})}, self.api)
        self.assertEqual(self.state()["state"], "awaiting_ci")
        run["databaseId"] = 11
        ci.verify_green(self.db, {"megamen32/demo": ("abc", {"nodes": [check], "totalCount": 2})}, self.api)
        self.assertEqual(self.state()["state"], "awaiting_ci")
        ci.verify_green(self.db, {"megamen32/demo": ("abc", {"nodes": [check], "totalCount": 1})}, self.api)
        self.assertEqual(self.state()["state"], "awaiting_ci")
        def green(path):
            if path.endswith("actions/workflows?per_page=100"):
                return {"total_count": 1, "workflows": [{"id": 3, "state": "active"}]}
            if "/actions/workflows/3/runs?" in path:
                return {"total_count": 1, "workflow_runs": [self.rest_run(11, "success")]}
            return self.api(path)
        ci.verify_green(self.db, {"megamen32/demo": ("abc", {"nodes": [check], "totalCount": 1})}, green)
        self.assertEqual(self.state()["state"], "verified_green")

    def test_discovery_paginates_and_deduplicates_run_attempt_head(self):
        run = {"databaseId": 10, "runAttempt": 1, "url": self.item["url"],
               "createdAt": self.item["created"], "workflow": {"databaseId": 3}}
        check = {"status": "COMPLETED", "conclusion": "FAILURE", "checkSuite": {"workflowRun": run}}
        repo = {"nameWithOwner": "megamen32/demo", "isArchived": False,
                "defaultBranchRef": {"name": "main", "target": {"oid": "abc", "statusCheckRollup": {
                    "contexts": {"nodes": [check, check], "totalCount": 2}}}}}
        requests = []
        def api(cursor, **kwargs):
            requests.append(cursor)
            return {"data": {"rateLimit": {"remaining": 1000}, "user": {"repositories": {
                "nodes": [repo], "pageInfo": {"hasNextPage": cursor is None, "endCursor": "page2"}}}}}
        ci.discover(self.db, {}, api)
        self.assertEqual(requests, [None, "page2"])
        self.assertEqual(self.db.execute("SELECT count(*) FROM events").fetchone()[0], 1)
        run["runAttempt"] = 2
        ci.discover(self.db, {}, api)
        self.assertEqual(self.db.execute("SELECT count(*) FROM events").fetchone()[0], 2)
        repo["defaultBranchRef"]["target"]["oid"] = "new"
        ci.discover(self.db, {}, api)
        self.assertEqual(self.db.execute("SELECT count(*) FROM events").fetchone()[0], 3)

    def test_crash_reopen_does_not_dispatch_again_and_ambiguity_holds_slot(self):
        ci.change(self.db, self.state(), "dispatching")
        self.db.close()
        self.db = ci.connect(Path(self.temp.name) / "state.sqlite3")
        self.db.execute("UPDATE events SET updated=0")
        self.db.commit()
        ci.reconcile(self.db, lambda *_: {"sessions": []})
        self.assertEqual(self.state()["state"], "needs_human")
        ci.dispatch(self.db, {}, lambda *_: self.fail("ambiguous intent must never resend"), self.api)
        self.assertEqual(self.db.execute("SELECT count(*) FROM notices").fetchone()[0], 1)

    def test_missing_incident_receipt_blocks_native_dispatch(self):
        self.db.execute("UPDATE events SET incident_id=NULL")
        self.db.commit()
        ci.dispatch(self.db, {}, lambda *_: self.fail("must await Notice Place incident"), self.api)
        self.assertEqual(self.state()["state"], "queued")
        payload = json.loads(self.db.execute("SELECT payload FROM notices").fetchone()[0])
        self.assertEqual(payload["schema"], "notify.event.v1")
        self.assertEqual(payload["event_type"], "health.degraded")
        self.assertEqual(payload["kind"], "incident")
        self.assertNotIn("type", payload)

    def test_recovery_verification_precedes_resolve_and_receipt_is_durable(self):
        token = Path(self.temp.name) / "token"
        token.write_text("local-test-only")
        ci.change(self.db, self.state(), "verified_green", "Local envelope test only")
        calls = []
        def boundary(url, payload, headers):
            calls.append((url, payload, headers["Idempotency-Key"]))
            if url.endswith("/health/verification"):
                self.assertEqual(payload["observed_state"], "healthy")
                self.assertTrue(payload["verification_id"].startswith("ci-green-"))
                self.assertIn("evidence_refs", payload)
                return {"incident_id": "local-test-incident", "healthy": True, "source_id": payload["source_id"],
                        "payload": {"verification_id": payload["verification_id"]}}
            stored = json.loads(self.db.execute("SELECT payload FROM notices").fetchone()[0])
            self.assertEqual(stored["verification_id"], "ci-green-" + self.item["key"])
            self.assertEqual(payload["event_type"], "health.recovered")
            self.assertEqual(payload["action"], "resolve")
            self.assertEqual(payload["project"], "gptadmin-ci-autofix")
            self.assertEqual(payload["recipient"], "me")
            return {"incident_id": "local-test-incident"}
        with patch.object(ci, "post_json", boundary):
            ci.notices(self.db, {"notice_token_file": str(token)})
        self.assertEqual(len(calls), 2)
        self.assertTrue(calls[0][0].endswith("/health/verification"))
        self.assertTrue(calls[1][0].endswith("/v1/events"))
        self.assertEqual(self.db.execute("SELECT sent FROM notices").fetchone()[0], 1)

    def test_receipt_session_id_survives_changed_title(self):
        ci.dispatch(self.db, {}, lambda *_: {"ok": True, "sessionId": "native-id"}, self.api)
        item = self.native("running")
        item["title"] = "automatic changed title"
        def call(name, args):
            if name == "list_agents":
                return {"sessions": []}
            self.assertEqual(args["sessionId"], "native-id")
            return {"session": item}
        ci.reconcile(self.db, call)
        self.assertEqual(self.state()["state"], "running")

    def test_human_wait_releases_slot_only_without_native_active_turn(self):
        ci.dispatch(self.db, {}, lambda *_: {"ok": True}, self.api)
        # OpenCode reports a permission-pending turn via needsPermission/details;
        # that is the active-turn signal which must keep the slot reserved.
        ci.reconcile(self.db, self.readback(self.native("needs_input", permission="perm-1")))
        self.assertEqual(self.state()["state"], "needs_human")
        ci.reconcile(self.db, self.readback(self.native("needs_input")))
        self.assertEqual(self.state()["state"], "human_wait")

    def test_backend_failure_retries_discovery_smaller_without_advancing_cursor(self):
        calls = []
        def api(cursor, **kwargs):
            calls.append((cursor, kwargs["page_size"]))
            if kwargs["page_size"] == 100:
                raise ci.GitHubError(502)
            return {"data": {"rateLimit": {"remaining": 1000}, "user": {"repositories": {
                "nodes": [], "pageInfo": {"hasNextPage": False, "endCursor": None}}}}}
        ci.discover(self.db, {}, api)
        self.assertEqual(calls, [(None, 100), (None, 20)])

    def test_operator_stop_cannot_be_reopened_by_reconcile(self):
        ci.change(self.db, self.state(), "operator_stopped")
        ci.reconcile(self.db, self.readback(self.native("running")))
        self.assertEqual(self.state()["state"], "operator_stopped")
        ci.dispatch(self.db, {}, lambda *_: self.fail("terminal stop"), self.api)

    def test_native_completion_before_first_poll_releases_slot(self):
        ci.dispatch(self.db, {}, lambda *_: {"ok": True, "sessionId": "native-id"}, self.api)
        # Herder's agent_info can lag and report idle before we ever observed
        # running. The reservation must hold until a running turn was actually
        # confirmed; the second reconcile with a real running observation is
        # the only thing that may advance the slot to awaiting_ci on idle.
        ci.reconcile(self.db, self.readback(self.native("idle", completed=True)))
        self.assertEqual(self.state()["state"], "accepted_unconfirmed")
        ci.reconcile(self.db, self.readback(self.native("running")))
        self.assertEqual(self.state()["state"], "running")
        ci.reconcile(self.db, self.readback(self.native("idle", completed=True)))
        self.assertEqual(self.state()["state"], "awaiting_ci")
        self.assertEqual(self.state()["native_id"], "native-id")

    def test_rest_timeout_failure_is_actionable(self):
        def api(path):
            value = self.api(path)
            if "/actions/runs/" in path:
                value["conclusion"] = "timed_out"
            return value
        self.assertTrue(ci.preflight(self.state(), api))
        self.assertFalse(ci.preflight(self.state(), lambda path: {**api(path), **({"conclusion": "cancelled"} if "/actions/runs/" in path else {})}))

    def test_operator_stop_holds_slot_until_native_turn_finishes(self):
        ci.change(self.db, self.state(), "operator_stopping")
        self.db.execute("UPDATE events SET harness='opencode' WHERE key=?", (self.item["key"],))
        self.db.commit()
        # A still-running turn keeps the operator-stop reservation active.
        ci.reconcile(self.db, self.readback(self.native("running")))
        self.assertEqual(self.state()["state"], "operator_stopping")
        ci.dispatch(self.db, {}, lambda *_: self.fail("still-active native slot"), self.api)
        # Completion requires a turn that was actually observed running; the
        # slot is only released when an idle session reports a processed turn.
        ci.reconcile(self.db, self.readback(self.native("idle", completed=True)))
        self.assertEqual(self.state()["state"], "operator_stopped")

    def test_changed_default_branch_refuses_old_branch_failure(self):
        def api(path):
            return {"default_branch": "other"} if path == "repos/megamen32/demo" else self.api(path)
        ci.dispatch(self.db, {}, lambda *_: self.fail("old default branch"), api)
        self.assertEqual(self.state()["state"], "obsolete")

    def test_failed_job_keeps_intent_queued_until_workflow_finishes(self):
        def api(path):
            result = self.api(path)
            if "/actions/runs/" in path:
                result["status"] = "in_progress"
            return result
        ci.dispatch(self.db, {}, lambda *_: self.fail("unfinished workflow"), api)
        self.assertEqual(self.state()["state"], "queued")
        calls = []
        ci.dispatch(self.db, {}, lambda *args: calls.append(args) or {"ok": True}, self.api)
        self.assertEqual(len(calls), 1)

    def test_current_head_release_tag_is_actionable_but_old_tag_is_not(self):
        def api(path):
            if "/git/ref/heads/" in path:
                raise ci.GitHubError(404)
            if "/git/ref/tags/" in path:
                return {"object": {"type": "tag", "sha": "annotation"}}
            if "/git/tags/" in path:
                return {"object": {"type": "commit", "sha": "abc"}}
            result = self.api(path)
            if "/actions/runs/" in path:
                result.update(head_branch="v1010", event="workflow_dispatch")
            return result
        self.assertTrue(ci.preflight(self.state(), api))
        self.assertFalse(ci.preflight(self.state(), lambda path: {"object": {"type": "commit", "sha": "old"}} if "/git/tags/" in path else api(path)))

    def test_manual_stop_fences_other_attempt_of_same_head(self):
        self.db.execute("UPDATE events SET state='operator_stopped'")
        second = {**self.item, "key": "another-attempt", "attempt": 2}
        self.db.execute("INSERT INTO events(key,repo,head,branch,run,attempt,workflow,url,created,name,cwd,seen,updated,incident_id) VALUES(:key,:repo,:head,:branch,:run,:attempt,:workflow,:url,:created,:name,:cwd,:seen,:updated,'local-test-incident')", second)
        self.db.commit()
        def api(path):
            result = self.api(path)
            if "/actions/runs/" in path:
                result["run_attempt"] = 2
            return result
        ci.dispatch(self.db, {}, lambda *_: self.fail("operator stop fences retry"), api)
        self.assertEqual(self.db.execute("SELECT state FROM events WHERE key='another-attempt'").fetchone()[0], "suppressed_by_stop")

    def rest_run(self, identifier=11, conclusion="failure", workflow=3, head="abc", attempt=1):
        return {"id": identifier, "run_attempt": attempt, "workflow_id": workflow, "head_sha": head,
                "head_branch": "main", "event": "schedule", "status": "completed", "conclusion": conclusion,
                "created_at": self.item["created"], "html_url": f"https://github.com/megamen32/demo/actions/runs/{identifier}"}

    def test_null_rollup_scheduled_intake_deduplicates_and_requires_active_current_head(self):
        snapshots = {"megamen32/demo": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"})}
        calls = []
        def api(path):
            calls.append(path)
            if "/actions/runs?" in path:
                return {"total_count": 3, "workflow_runs": [self.rest_run(11), self.rest_run(12, workflow=4), self.rest_run(13, head="old")]}
            if path.endswith("/actions/workflows/4"):
                return {"state": "disabled_manually"}
            return self.api(path)
        cfg = {"workflow_repositories": ["megamen32/demo"]}
        ci.discover_runs(self.db, cfg, snapshots, api)
        ci.discover_runs(self.db, cfg, snapshots, api)
        found = list(self.db.execute("SELECT * FROM events WHERE run=11"))
        self.assertEqual(len(found), 1)
        self.assertEqual(found[0]["head"], "abc")
        self.assertEqual(self.db.execute("SELECT count(*) FROM events WHERE run IN (12,13)").fetchone()[0], 0)
        self.assertEqual(ci.meta(self.db, "rest_cursor_known"), "megamen32/demo")
        self.assertNotIn("deliver", calls)

    def test_rest_latest_success_suppresses_old_current_head_failure(self):
        snapshots = {"megamen32/demo": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"})}
        def api(path):
            if "/actions/runs?" in path:
                return {"total_count": 2, "workflow_runs": [self.rest_run(12, "success"), self.rest_run(11)]}
            return self.api(path)
        ci.discover_runs(self.db, {"workflow_repositories": ["megamen32/demo"]}, snapshots, api)
        self.assertEqual(self.db.execute("SELECT count(*) FROM events WHERE run=11").fetchone()[0], 0)

    def test_rest_known_cursor_survives_reopen_and_unknown_cache_head_invalidation(self):
        snapshots = {f"megamen32/repo{i:02d}": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"}) for i in range(36)}
        called = []
        def api(path):
            if "/actions/runs?" in path:
                called.append(path.split("/")[2])
                return {"total_count": 0, "workflow_runs": []}
            return self.api(path)
        cfg = {"workflow_repositories": list(snapshots)}
        ci.discover_runs(self.db, cfg, snapshots, api)
        first = set(called)
        self.assertEqual(len(first), 24)
        self.db.close()
        self.db = ci.connect(Path(self.temp.name) / "state.sqlite3")
        called.clear()
        ci.discover_runs(self.db, cfg, snapshots, api)
        self.assertEqual(len(set(called)), 24)
        self.assertEqual(len(first | set(called)), 36)
        unknown = {"megamen32/unknown": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"})}
        ci.discover_runs(self.db, {}, unknown, api)
        called.clear()
        ci.discover_runs(self.db, {}, unknown, api)
        self.assertEqual(called, [])
        unknown["megamen32/unknown"] = ("changed", unknown["megamen32/unknown"][1])
        ci.discover_runs(self.db, {}, unknown, api)
        self.assertEqual(called, ["unknown"])

    def test_rest_history_cursor_finds_workflow_outside_latest_twenty(self):
        snapshots = {"megamen32/demo": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"})}
        pages = []
        def api(path):
            if "/actions/runs?" in path:
                page = int(path.rsplit("=", 1)[1]); pages.append(page)
                runs = [self.rest_run(30, "success")] if page == 1 else [self.rest_run(11, workflow=9)]
                return {"total_count": 21, "workflow_runs": runs}
            return self.api(path)
        cfg = {"workflow_repositories": ["megamen32/demo"]}
        ci.discover_runs(self.db, cfg, snapshots, api)
        ci.discover_runs(self.db, cfg, snapshots, api)
        self.assertIn(2, pages)
        self.assertEqual(self.db.execute("SELECT count(*) FROM events WHERE run=11").fetchone()[0], 1)

    def test_rest_budget_preserves_rate_reserve_and_refuses_near_deadline_api(self):
        calls = []
        budget = ci.RestBudget(lambda path: calls.append(path), 2, 10)
        with self.assertRaises(ci.RestBudgetHeld):
            budget("anything")
        self.assertEqual(calls, [])
        budget = ci.RestBudget(lambda path: calls.append(path), 2, 45)
        budget("first"); budget("second")
        with self.assertRaises(ci.RestBudgetHeld):
            budget("third")
        snapshots = {"megamen32/demo": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"})}
        ci.discover_runs(self.db, {}, snapshots, lambda path: {"resources": {"core": {"remaining": 100}}})
        self.assertEqual(ci.meta(self.db, "rest_scan_status"), "held_rate_reserve")
        self.assertEqual(ci.meta(self.db, "rest_scan_calls"), "1")

    def test_rest_green_aggregate_catches_hidden_scheduled_failure_even_with_green_rollup(self):
        self.db.execute("UPDATE events SET state='awaiting_ci'")
        nodes = [{"status": "COMPLETED", "conclusion": "SUCCESS"}]
        for nodes in (nodes, []):
            snapshot = {"megamen32/demo": ("abc", {"nodes": nodes, "totalCount": len(nodes)})}
            def api(path):
                if path.endswith("actions/workflows?per_page=100"):
                    return {"total_count": 2, "workflows": [{"id": 3, "state": "active"}, {"id": 4, "state": "active"}]}
                if "/actions/workflows/3/runs?" in path:
                    return {"total_count": 1, "workflow_runs": [self.rest_run(11, "success")]}
                if "/actions/workflows/4/runs?" in path:
                    return {"total_count": 1, "workflow_runs": [self.rest_run(12, workflow=4)]}
                return self.api(path)
            ci.verify_green(self.db, snapshot, api)
            self.assertEqual(self.state()["state"], "awaiting_ci")
        def all_green(path):
            result = api(path)
            if "/actions/workflows/4/runs?" in path:
                result["workflow_runs"][0]["conclusion"] = "success"
            return result
        ci.verify_green(self.db, snapshot, all_green)
        self.assertEqual(self.state()["state"], "verified_green")

    def test_rest_green_partial_inventory_or_changed_head_is_never_accepted(self):
        self.db.execute("UPDATE events SET state='awaiting_ci'")
        snapshot = {"megamen32/demo": ("abc", {"nodes": [], "totalCount": 0})}
        ci.verify_green(self.db, snapshot, lambda path: {"total_count": 2, "workflows": [{"id": 3, "state": "active"}]} if path.endswith("actions/workflows?per_page=100") else self.api(path))
        self.assertEqual(self.state()["state"], "awaiting_ci")
        self.assertIsNone(ci.rest_green_proof(self.state(), "abc", lambda path: {"default_branch": "other"}))

    def test_rest_api_allocation_caps_known_unknown_and_partial_cursor_progress(self):
        snapshots = {f"megamen32/known{i:02d}": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"}) for i in range(36)}
        snapshots.update({f"megamen32/unknown{i:02d}": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"}) for i in range(10)})
        known = [name for name in snapshots if "/known" in name]
        calls = []
        def api(path):
            calls.append(path)
            if "/actions/runs?" in path:
                return {"total_count": 0, "workflow_runs": []}
            return self.api(path)
        ci.discover_runs(self.db, {"workflow_repositories": known}, snapshots, api)
        paths = [path for path in calls if "/actions/runs?" in path]
        self.assertEqual(sum("/known" in path for path in paths), 24)
        self.assertEqual(sum("/unknown" in path for path in paths), 4)
        self.assertLessEqual(len(calls), ci.REST_MAX_CALLS)
        self.assertLess(60*(ci.REST_MAX_CALLS+ci.REST_GREEN_CALLS+10), 5000)
        cursor = ci.meta(self.db, "rest_cursor_known")
        with patch.object(ci, "REST_MAX_CALLS", 3):
            ci.discover_runs(self.db, {"workflow_repositories": known}, snapshots, api)
        self.assertEqual(ci.meta(self.db, "rest_cursor_known"), cursor)
        self.assertNotEqual(ci.meta(self.db, "rest_cursor_unknown"), "megamen32/unknown03")
        self.assertEqual(ci.meta(self.db, "rest_scan_status"), "held_tick_budget")

    def test_shared_deadline_prevents_scan_and_graphql_api_start(self):
        snapshots = {"megamen32/demo": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"})}
        calls = []
        ci.discover_runs(self.db, {}, snapshots, lambda path: calls.append(path), deadline=ci.time.monotonic()+1)
        self.assertEqual(calls, [])
        self.assertEqual(ci.meta(self.db, "rest_scan_status"), "held_tick_budget")
        self.assertEqual(ci.discover(self.db, {}, lambda *a, **k: calls.append(a), deadline=ci.time.monotonic()+1), {})
        self.assertEqual(calls, [])

    def test_completed_history_backfill_does_not_restart_every_tick(self):
        snapshots = {"megamen32/demo": ("abc", {"nodes": [], "totalCount": 0, "_branch": "main"})}
        pages = []
        def api(path):
            if "/actions/runs?" in path:
                page = int(path.rsplit("=", 1)[1]); pages.append(page)
                return {"total_count": 21, "workflow_runs": [self.rest_run(30, "success")]}
            return self.api(path)
        cfg = {"workflow_repositories": ["megamen32/demo"]}
        for _ in range(4):
            ci.discover_runs(self.db, cfg, snapshots, api)
        self.assertEqual(pages.count(2), 1)
        self.assertEqual(self.db.execute("SELECT next_page FROM repo_scans").fetchone()[0], -1)

    def test_shared_eligibility_rejects_pr_and_feature_same_sha_in_preflight_and_green(self):
        for event_type, branch in (("pull_request", "main"), ("pull_request_target", "main"), ("push", "feature")):
            def preflight_api(path):
                if "/git/ref/tags/" in path:
                    raise ci.GitHubError(404)
                data = self.api(path)
                if "/actions/runs/" in path:
                    data.update(event=event_type, head_branch=branch)
                return data
            self.assertFalse(ci.preflight(self.state(), preflight_api))
            def green_api(path):
                if path.endswith("actions/workflows?per_page=100"):
                    return {"total_count": 1, "workflows": [{"id": 3, "state": "active"}]}
                if "/actions/workflows/3/runs?" in path:
                    return {"total_count": 1, "workflow_runs": [{**self.rest_run(11, "success"), "event": event_type, "head_branch": branch}]}
                return preflight_api(path)
            self.assertIsNone(ci.rest_green_proof(self.state(), "abc", green_api))

    def test_preflight_api_reserve_holds_existing_failure_without_dispatch(self):
        calls = []
        def exhausted(path):
            calls.append(path)
            self.assertEqual(path, "rate_limit")
            return {"resources": {"core": {"remaining": 109}}}
        ci.dispatch(self.db, {}, lambda *_: self.fail("reserve must prevent native dispatch"), exhausted)
        self.assertEqual(self.state()["state"], "queued")
        self.assertEqual(calls, ["rate_limit"])

    def test_tag_and_branch_alias_cannot_supply_release_evidence(self):
        run = {**self.rest_run(11, "success"), "event": "workflow_dispatch", "head_branch": "v1"}
        def ambiguous(path):
            return {"object": {"type": "commit", "sha": "abc"}}
        self.assertFalse(ci.eligible_run("megamen32/demo", "main", "abc", run, ambiguous))

    def test_shared_eligibility_accepts_only_bounded_current_head_tags(self):
        run = {**self.rest_run(11, "success"), "event": "workflow_dispatch", "head_branch": "v1"}
        def tag_api(path):
            if "/git/ref/heads/" in path:
                raise ci.GitHubError(404)
            return {"object": {"type": "commit", "sha": "abc"}}
        self.assertTrue(ci.eligible_run("megamen32/demo", "main", "abc", run, tag_api))
        self.assertFalse(ci.eligible_run("megamen32/demo", "main", "abc", run,
                        lambda path: {"object": {"type": "commit", "sha": "old"}}))
        count = []
        def nested(path):
            count.append(path)
            if "/git/ref/heads/" in path:
                raise ci.GitHubError(404)
            return {"object": {"type": "tag", "sha": "nested"}}
        self.assertFalse(ci.eligible_run("megamen32/demo", "main", "abc", run, nested))
        self.assertEqual(len(count), 4)

    def test_graphql_deadline_cursor_caches_early_results_and_eventually_reaches_late_repo(self):
        def repo(name):
            return {"nameWithOwner": "megamen32/"+name, "isArchived": False,
                    "defaultBranchRef": {"name": "main", "target": {"oid": "abc", "statusCheckRollup": None}}}
        calls = []
        clock = [0.0]
        def api(cursor, **kwargs):
            calls.append(cursor);clock[0] += 20
            name, more, end = {None: ("early", True, "p2"), "p2": ("middle", True, "p3"), "p3": ("late", False, None)}[cursor]
            return {"data": {"rateLimit": {"remaining": 5000}, "user": {"repositories": {
                "nodes": [repo(name)], "pageInfo": {"hasNextPage": more, "endCursor": end}}}}}
        with patch.object(ci.time, "monotonic", lambda: clock[0]):
            first = ci.discover(self.db, {}, api, deadline=30)
            self.assertEqual(set(first), {"megamen32/early"})
            self.assertEqual(ci.meta(self.db, "graphql_cursor"), "p2")
            self.db.close();self.db=ci.connect(Path(self.temp.name)/"state.sqlite3")
            second = ci.discover(self.db, {}, api, deadline=50)
            self.assertEqual(set(second), {"megamen32/early", "megamen32/middle"})
            third = ci.discover(self.db, {}, api, deadline=70)
            self.assertEqual(set(third), {"megamen32/early", "megamen32/middle", "megamen32/late"})
            self.assertEqual(ci.meta(self.db, "graphql_cursor"), "")
        self.assertEqual(calls, [None, "p2", "p3"])

    def test_newer_pr_same_sha_does_not_obsolete_valid_default_failure(self):
        def api(path):
            if "/actions/workflows/3/runs?" in path:
                return {"total_count": 2, "workflow_runs": [
                    {**self.rest_run(11, "success"), "event": "pull_request"}, self.rest_run(10)]}
            return self.api(path)
        self.assertTrue(ci.preflight(self.state(), api))
        def newer_default(path):
            if "/actions/workflows/3/runs?" in path:
                return {"total_count": 3, "workflow_runs": [self.rest_run(12, "success"),
                    {**self.rest_run(11, "success"), "event": "pull_request"}, self.rest_run(10)]}
            return self.api(path)
        self.assertFalse(ci.preflight(self.state(), newer_default))

    def test_green_selects_eligible_default_after_irrelevant_pr_and_feature(self):
        def api(path):
            if path.endswith("actions/workflows?per_page=100"):
                return {"total_count": 1, "workflows": [{"id": 3, "state": "active"}]}
            if "/actions/workflows/3/runs?" in path:
                return {"total_count": 3, "workflow_runs": [
                    {**self.rest_run(13, "success"), "event": "pull_request_target"},
                    {**self.rest_run(12, "success"), "event": "push", "head_branch": "feature"},
                    self.rest_run(11, "success")]}
            if "/git/ref/tags/" in path:
                raise ci.GitHubError(404)
            return self.api(path)
        self.assertEqual(ci.rest_green_proof(self.state(), "abc", api), self.rest_run(11)["html_url"])
        tagged_calls = []
        def no_eligible(path):
            if "/actions/workflows/3/runs?" in path:
                return {"total_count": 21, "workflow_runs": [
                    {**self.rest_run(i, "success"), "event": "push", "head_branch": f"feature{i}"} for i in range(30,10,-1)]}
            if "/git/ref/" in path:
                tagged_calls.append(path);raise ci.GitHubError(404)
            return self.api(path)
        self.assertIsNone(ci.preflight(self.state(), no_eligible))
        self.assertEqual(len(tagged_calls), 4)

    def test_authoritative_no_other_current_head_run_differs_from_unresolved_prefix(self):
        def api(path):
            if path.endswith("actions/workflows?per_page=100"):
                return {"total_count": 2, "workflows": [{"id": 3, "state": "active"}, {"id": 4, "state": "active"}]}
            if "/actions/workflows/3/runs?" in path:
                return {"total_count": 1, "workflow_runs": [self.rest_run(11, "success")]}
            if "/actions/workflows/4/runs?" in path:
                return {"total_count": 0, "workflow_runs": []}
            return self.api(path)
        self.assertEqual(ci.rest_green_proof(self.state(), "abc", api), self.rest_run(11)["html_url"])
        def incomplete(path):
            if "/actions/workflows/4/runs?" in path:
                return {"total_count": 21, "workflow_runs": [{**self.rest_run(12, "success", workflow=4), "event": "pull_request"}]}
            return api(path)
        self.assertIsNone(ci.rest_green_proof(self.state(), "abc", incomplete))

    def test_http_sse_transport(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(("data: " + json.dumps({"jsonrpc": "2.0", "id": payload["id"], "result": {"ok": True}}) + "\n\n").encode())
            def log_message(self, *args):
                pass
        server = HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        try:
            self.assertEqual(ci.post_json(f"http://127.0.0.1:{server.server_port}/mcp", {"id": 1})["result"], {"ok": True})
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_opencode_dispatch_records_harness_and_initial_model(self):
        calls = []
        def call(name, args):
            calls.append(args)
            return {"ok": True, "harness": ci.HARNESS, "model": ci.PROVIDER_MODEL, "sessionId": "native-id"}
        ci.dispatch(self.db, {}, call, self.api)
        self.assertEqual(calls[0]["harness"], "opencode")
        self.assertEqual(calls[0]["model"], "zai-coding-plan/glm-5.3-flash")
        self.assertEqual(calls[0]["inputId"], "gptadmin-ci-autofix:" + self.item["key"])
        self.assertEqual(self.db.execute("SELECT harness FROM events").fetchone()[0], "opencode")

    def test_opencode_dispatch_fails_closed_on_harness_downgrade(self):
        def call(name, args):
            return {"ok": True, "harness": "codex", "model": ci.PROVIDER_MODEL, "sessionId": "native-id"}
        ci.dispatch(self.db, {}, call, self.api)
        # The harness is fixed; provider/model fallback is a separate choice.
        self.assertEqual(self.state()["state"], "dispatching")
        self.assertIsNone(self.state()["session_id"])

    def test_opencode_dispatch_allows_omniroute_model_fallback(self):
        def call(name, args):
            return {"ok": True, "harness": "opencode", "model": "omniroute/free-stack", "sessionId": "native-id"}
        ci.dispatch(self.db, {}, call, self.api)
        self.assertEqual(self.state()["state"], "accepted_unconfirmed")
        self.assertEqual(self.state()["session_id"], "native-id")

    def test_opencode_readback_accepts_and_records_effective_fallback_model(self):
        # The Herder may omit harness/model on a successful delivery; absence
        # is not a fail-closed signal. The authoritative readback (agent_info)
        # is what confirms the explicit OpenCode route.
        def call(name, args):
            return {"ok": True, "sessionId": "native-id"}
        ci.dispatch(self.db, {}, call, self.api)
        self.assertEqual(self.state()["state"], "accepted_unconfirmed")
        effective = self.native("running")
        effective["model"] = "omniroute/free-stack"
        ci.reconcile(self.db, self.readback(effective))
        self.assertEqual(self.state()["state"], "running")
        self.assertIn("omniroute/free-stack", self.state()["reason"])

    def test_opencode_completion_requires_running_then_message_count(self):
        ci.dispatch(self.db, {}, lambda *_: {"ok": True, "sessionId": "native-id"}, self.api)
        # First observation is a false idle (real session is still working);
        # the reservation must hold and not advance.
        ci.reconcile(self.db, self.readback(self.native("idle", completed=True)))
        self.assertEqual(self.state()["state"], "accepted_unconfirmed")
        # Running proof is required before any completion can release the slot.
        ci.reconcile(self.db, self.readback(self.native("running")))
        self.assertEqual(self.state()["state"], "running")
        # Real completion: idle + messageCount>=1.
        ci.reconcile(self.db, self.readback(self.native("idle", completed=True)))
        self.assertEqual(self.state()["state"], "awaiting_ci")
        self.assertEqual(self.state()["native_id"], "native-id")

    def test_legacy_codex_receipts_remain_readable(self):
        # An event left over from the pre-migration codex path: the harness
        # column is unset, the session still uses codex-app-server transport,
        # and the same id is reported through the codex agent_info path.
        ci.change(self.db, self.state(), "accepted_unconfirmed")
        self.db.execute("UPDATE events SET session_id='codex-native-id' WHERE key=?", (self.item["key"],))
        self.db.commit()
        # Pre-migration rows recorded no harness; reconcile must look them up
        # through the codex harness, never the new opencode default. The
        # codex running proof is activeTurnId + transport + nativeSessionId.
        def call(name, args):
            if name == "list_agents":
                self.fail("Legacy codex receipts must use the codex harness, never opencode")
            self.assertEqual(args["harness"], "codex")
            self.assertEqual(args["sessionId"], "codex-native-id")
            return {"session": self.native_codex("running", activeTurnId="turn-1")}
        ci.reconcile(self.db, call)
        self.assertEqual(self.state()["state"], "running")
        self.assertEqual(self.state()["session_id"], "codex-native-id")


if __name__ == "__main__":
    unittest.main()
