"""Logic fixtures for HAOS removal-on-stop, never a live Docker test.

The absent-container response shape is the one retained from the 2026-11-17
Supervisor stop/compensating-start incident.  All command execution is mocked.
These tests do not claim a new production stop/start acceptance.
"""

import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


HERE = Path(__file__).resolve().parent
SOURCE = HERE / "gptadmin_authority_controller.py"
if not SOURCE.exists():
    SOURCE = HERE.parent / "gptadmin_authority_controller.py"
SPEC = importlib.util.spec_from_file_location("authority_controller_haos_absent", SOURCE)
module = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(module)

NODE = "app_fixture_gptadmin_hub_standby"
MANAGEMENT = "app_fixture_ssh"
OLD_ID = "1" * 64
NEW_ID = "2" * 64
MANAGEMENT_ID = "3" * 64
IMAGE = "sha256:" + "4" * 64


def absent_state(**changes):
    value = {
        "pid": 0, "inner_pid": 0, "generation": "", "container_id": "",
        "active": "inactive", "container_running": False,
        "container_status": "absent", "container_restarting": False,
        "container_dead": False, "container_present": False,
        "absence_confirmed": True, "supervisor_state": "stopped",
        "lifecycle_jobs_idle": True, "management_container_id": MANAGEMENT_ID,
    }
    value.update(changes)
    return value


def live_state(container_id=OLD_ID, pid=42, generation="original:123"):
    return {
        "pid": pid, "inner_pid": 139, "generation": generation,
        "container_id": container_id, "image_id": IMAGE,
        "active": "active", "container_running": True,
        "container_status": "running", "container_restarting": False,
        "container_dead": False, "container_present": True,
        "management_container_id": MANAGEMENT_ID,
    }


def completed(returncode=0, stdout=b"", stderr=b""):
    result = subprocess.CompletedProcess(["fixture-only"], returncode, stdout, stderr)
    result.safe_metadata = {
        "command": "docker", "stage": "fixture.inspect", "returncode": returncode,
        "timed_out": False, "timeout_seconds": 15, "elapsed_seconds": 0.001,
    }
    return result


def missing(name=NODE):
    return completed(
        1, b"[]\n",
        ("Error response from daemon: No such container: " + name + "\n").encode(),
    )


class ContainerAbsenceClassificationTests(unittest.TestCase):
    def setUp(self):
        self.lifecycle = module.HAOS({"container": NODE})

    def test_exact_observed_absence_requires_explicit_opt_in(self):
        with patch.object(module, "command", return_value=missing()) as command:
            self.assertIsNone(self.lifecycle.inspect_container(NODE, allow_absent=True))
        self.assertEqual(command.call_count, 1)
        self.assertEqual(command.call_args.args[0][-1], NODE)
        with patch.object(module, "command", return_value=missing()):
            with self.assertRaises(module.Refused):
                self.lifecycle.inspect_container(NODE)

    def test_container_id_absence_is_bound_to_that_exact_id(self):
        with patch.object(module, "command", return_value=missing(OLD_ID)):
            self.assertIsNone(self.lifecycle.inspect_container(OLD_ID, allow_absent=True))
        with patch.object(module, "command", return_value=missing(NEW_ID)):
            with self.assertRaises(module.Refused):
                self.lifecycle.inspect_container(OLD_ID, allow_absent=True)

    def test_daemon_and_permission_errors_are_never_absence(self):
        for stderr in (
            b"Cannot connect to the Docker daemon at unix:///var/run/docker.sock\n",
            b"permission denied while trying to connect to the Docker daemon socket\n",
            b"Error response from daemon: authorization denied\n",
            b"Error response from daemon: No such container: another-container\n",
        ):
            with self.subTest(stderr=stderr), patch.object(
                module, "command", return_value=completed(1, b"[]\n", stderr)
            ):
                with self.assertRaises(module.Refused):
                    self.lifecycle.inspect_container(NODE, allow_absent=True)

    def test_mixed_or_nonempty_output_cannot_be_mistaken_for_absence(self):
        stderr = missing().stderr
        for stdout, returncode in ((b"", 1), (b"{}\n", 1), (b"[{}]\n", 1), (b"[]\n", 2)):
            with self.subTest(stdout=stdout, returncode=returncode), patch.object(
                module, "command", return_value=completed(returncode, stdout, stderr)
            ):
                with self.assertRaises(module.Refused):
                    self.lifecycle.inspect_container(NODE, allow_absent=True)

    def test_additional_diagnostic_lines_are_not_the_exact_absence_receipt(self):
        with patch.object(
            module, "command", return_value=completed(
                1, b"[]\n", missing().stderr + b"daemon state unavailable\n"
            )
        ):
            with self.assertRaises(module.Refused):
                self.lifecycle.inspect_container(NODE, allow_absent=True)

    def test_present_container_keeps_its_real_identity(self):
        value = {"Id": OLD_ID, "Image": IMAGE, "State": {"Running": True, "Pid": 42}}
        with patch.object(module, "command", return_value=completed(stdout=json.dumps([value]).encode())):
            self.assertEqual(self.lifecycle.inspect_container(NODE, allow_absent=True), value)

    def test_successful_empty_or_ambiguous_inspect_is_refused(self):
        for values in ([], [{"Id": OLD_ID}, {"Id": NEW_ID}]):
            with self.subTest(values=values), patch.object(
                module, "command", return_value=completed(stdout=json.dumps(values).encode())
            ):
                with self.assertRaises(module.Refused):
                    self.lifecycle.inspect_container(NODE, allow_absent=True)


class AbsentStoppedProofTests(unittest.TestCase):
    def setUp(self):
        self.lifecycle = module.HAOS({"container": NODE})

    def test_only_confirmed_quiet_absence_is_stopped(self):
        value = absent_state()
        self.assertTrue(self.lifecycle.stopped(value))
        self.assertFalse(self.lifecycle.has_node(value))
        self.assertEqual((value["pid"], value["inner_pid"]), (0, 0))
        self.assertEqual((value["generation"], value["container_id"]), ("", ""))

    def test_transitional_or_unproved_absence_cannot_authorize_offline_work(self):
        changes = (
            {"absence_confirmed": False},
            {"supervisor_state": "started"},
            {"supervisor_state": "unknown"},
            {"lifecycle_jobs_idle": False},
            {"container_running": True},
            {"container_restarting": True},
            {"container_dead": True},
            {"pid": 42},
        )
        for change in changes:
            with self.subTest(change=change):
                self.assertFalse(self.lifecycle.stopped(absent_state(**change)))

    def test_live_replacement_is_never_mistaken_for_the_absent_old_container(self):
        value = live_state(NEW_ID, 84, "replacement:456")
        self.assertFalse(self.lifecycle.stopped(value))
        self.assertTrue(self.lifecycle.has_node(value))


class PositiveAbsenceIntegrationTests(unittest.TestCase):
    """Fake native CLI responses, with the real HAOS proof composition."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.manifest = {
            "container": NODE, "management_container": MANAGEMENT,
            "image_id": IMAGE, "addon": "fixture_gptadmin", "addon_version": "1.0.fixture",
            "data_host_path": "/fixture/data", "share_host_path": "/fixture/share",
            "operation_dir": str(self.root),
            "file_guards": [{"path": "/data/config/node-identity.json", "sha256": "fixture-identity"}],
        }
        self.saved = {
            "manifest_sha256": "fixture-manifest",
            "service": live_state(),
            "auxiliaries": {"watchdog": False, "boot": "auto", "auto_update": True},
            "environment_file_sha256": "fixture-launcher",
        }
        self.capture = self.root / "capture.private.json"
        module.private_json(self.capture, self.saved, True)
        self.management = {
            "Id": MANAGEMENT_ID, "Config": {"Hostname": "fixture-ssh"},
            "State": {"Running": True, "Restarting": False, "Dead": False, "Pid": 3251},
            "Mounts": [{"Destination": "/share", "Source": "/fixture/share", "Type": "bind", "RW": True}],
        }
        self.containers = {MANAGEMENT: self.management}
        self.supervisor = {
            "slug": "fixture_gptadmin", "version": "1.0.fixture", "state": "stopped",
            "watchdog": False, "boot": "auto", "auto_update": False,
        }
        self.jobs = {"jobs": []}
        self.image = [{"Id": IMAGE}]
        self.ps_results = {}
        self.launcher = "fixture-launcher"
        self.file_hashes = {"/data/config/node-identity.json": "fixture-identity"}
        self.mutations = []
        self.commands = []
        self.lifecycle = module.HAOS(self.manifest, "fixture-manifest")
        self.patches = [
            patch.object(module, "command", side_effect=self.fake_command),
            patch.object(module.os, "uname", return_value=SimpleNamespace(nodename="fixture-ssh")),
            patch.object(self.lifecycle, "launcher_digest", side_effect=lambda: self.launcher),
            patch.object(self.lifecycle, "file_digest", side_effect=lambda p: self.file_hashes[p]),
        ]
        for item in self.patches:
            item.start()
            self.addCleanup(item.stop)

    def container(self, container_id=OLD_ID, pid=42):
        return {
            "Id": container_id, "Image": IMAGE,
            "HostConfig": {"RestartPolicy": {"Name": "no"}},
            "State": {"Running": True, "Pid": pid, "StartedAt": "fixture-start",
                      "Status": "running", "Restarting": False, "Dead": False},
            "Mounts": [
                {"Destination": "/data", "Source": "/fixture/data", "Type": "bind", "RW": True},
                {"Destination": "/share", "Source": "/fixture/share", "Type": "bind", "RW": True},
            ],
        }

    def fake_command(self, argv, **kwargs):
        self.commands.append(tuple(argv))
        if argv[0] == self.lifecycle.docker:
            if argv[1:4] == ["inspect", "--type", "container"]:
                name = argv[4]
                value = self.containers.get(name)
                if isinstance(value, subprocess.CompletedProcess):
                    return value
                return missing(name) if value is None else completed(stdout=json.dumps([value]).encode())
            if argv[1:2] == ["ps"]:
                key = argv[argv.index("--filter") + 1]
                return completed(stdout=self.ps_results.get(key, b""))
            if argv[1:3] == ["image", "inspect"]:
                self.assertEqual(argv[3], IMAGE)
                return completed(stdout=json.dumps(self.image).encode())
            if argv[1:2] == ["exec"]:
                self.assertIn(argv[2], (OLD_ID, NEW_ID))
                return completed(stdout=b'[[157,"fixture-inner-generation"]]')
        if argv[:3] == ["/usr/bin/ha", "addons", "info"]:
            self.assertEqual(argv[3], self.manifest["addon"])
            return completed(stdout=json.dumps({"data": self.supervisor}).encode())
        if argv[:3] == ["/usr/bin/ha", "jobs", "info"]:
            return completed(stdout=json.dumps({"data": self.jobs}).encode())
        if argv[:3] == ["/usr/bin/ha", "addons", "stop"]:
            self.mutations.append("stop")
            self.containers.pop(NODE, None)
            self.containers.pop(OLD_ID, None)
            self.supervisor["state"] = "stopped"
            return completed()
        if argv[:3] == ["/usr/bin/ha", "addons", "start"]:
            self.mutations.append("start")
            self.containers[NODE] = self.container(NEW_ID, 84)
            self.containers[NEW_ID] = self.containers[NODE]
            self.supervisor["state"] = "started"
            return completed()
        raise AssertionError("unexpected command in fake-only HAOS fixture: " + repr(argv))

    def test_absent_info_combines_the_retained_proofs_without_a_fake_live_identity(self):
        value = self.lifecycle.info()
        self.assertTrue(value["absence_confirmed"])
        self.assertFalse(value["container_present"])
        self.assertTrue(self.lifecycle.stopped(value))
        self.assertFalse(self.lifecycle.has_node(value))
        self.assertEqual((value["pid"], value["inner_pid"], value["generation"], value["container_id"]),
                         (0, 0, "", ""))
        filters = [c[c.index("--filter") + 1] for c in self.commands if "--filter" in c]
        self.assertEqual(set(filters), {"name=^/" + NODE + "$", "id=" + OLD_ID})
        self.assertEqual(self.mutations, [])

    def test_one_normal_stop_removes_container_and_one_start_returns_new_identity(self):
        self.containers[NODE] = self.container()
        self.containers[OLD_ID] = self.containers[NODE]
        self.supervisor["state"] = "started"
        stopped = self.lifecycle.stop()
        self.assertTrue(self.lifecycle.stopped(stopped))
        self.lifecycle.start()
        current = self.lifecycle.info()
        self.assertEqual(self.mutations, ["stop", "start"])
        self.assertEqual(current["container_id"], NEW_ID)
        self.assertEqual(current["pid"], 84)
        self.assertEqual(current["generation"], "fixture-start:fixture-inner-generation")
        self.assertTrue(self.lifecycle.has_node(current))
        self.assertFalse(self.lifecycle.stopped(current))

    def test_absent_name_does_not_hide_the_still_present_captured_container(self):
        self.containers[OLD_ID] = self.container()
        with self.assertRaises(module.Refused):
            self.lifecycle.info()
        self.assertEqual(self.mutations, [])

    def test_exact_name_or_id_appearing_during_daemon_confirmation_is_refused(self):
        for key in ("name=^/" + NODE + "$", "id=" + OLD_ID):
            with self.subTest(filter=key):
                self.ps_results = {key: (NEW_ID + "\n").encode()}
                with self.assertRaises(module.Refused):
                    self.lifecycle.info()
        self.assertEqual(self.mutations, [])

    def test_management_lookup_failure_never_authorizes_node_absence(self):
        self.containers[MANAGEMENT] = completed(1, b"", b"Docker daemon permission denied\n")
        with self.assertRaises(module.Refused):
            self.lifecycle.info()
        self.assertEqual(len(self.commands), 1)
        self.assertEqual(self.mutations, [])

    def test_unavailable_or_replaced_management_runtime_is_refused(self):
        original = copy.deepcopy(self.management)
        cases = (
            {"State": {**original["State"], "Running": False}},
            {"State": {**original["State"], "Restarting": True}},
            {"Config": {"Hostname": "another-controller"}},
            {"Id": "5" * 64},
        )
        for changes in cases:
            with self.subTest(changes=changes):
                self.containers[MANAGEMENT] = {**copy.deepcopy(original), **changes}
                with self.assertRaises(module.Refused):
                    self.lifecycle.info()
        self.assertEqual(self.mutations, [])

    def test_capture_manifest_image_and_private_mode_are_required(self):
        for change in ("manifest", "image", "mode"):
            with self.subTest(change=change):
                saved = copy.deepcopy(self.saved)
                if change == "manifest":
                    saved["manifest_sha256"] = "different-manifest"
                elif change == "image":
                    saved["service"]["image_id"] = "sha256:" + "6" * 64
                module.private_json(self.capture, saved)
                self.capture.chmod(0o644 if change == "mode" else 0o600)
                with self.assertRaises(module.Refused):
                    self.lifecycle.info()
        self.assertEqual(self.mutations, [])

    def test_changed_supervisor_identity_version_or_policy_is_refused(self):
        original = dict(self.supervisor)
        for key, value in ("slug", "another-addon"), ("version", "different"), ("watchdog", True), ("boot", "manual"):
            with self.subTest(key=key):
                self.supervisor = {**original, key: value}
                with self.assertRaises(module.Refused):
                    self.lifecycle.info()
        self.assertEqual(self.mutations, [])

    def test_busy_or_nonstopped_supervisor_is_transitioning_not_stopped(self):
        for jobs, state in (([{"name": "start-addon"}], "stopped"), ([], "started")):
            with self.subTest(jobs=jobs, state=state):
                self.jobs = {"jobs": jobs}
                self.supervisor["state"] = state
                value = self.lifecycle.info()
                self.assertEqual(value["active"], "transitioning")
                self.assertFalse(self.lifecycle.stopped(value))
                self.assertFalse(self.lifecycle.has_node(value))
                with self.assertRaises(module.Refused):
                    self.lifecycle.resume(self.saved["auxiliaries"])
        self.assertEqual(self.mutations, [])

    def test_unobservable_jobs_or_wrong_pinned_image_is_refused(self):
        for jobs, image in (({}, self.image), ({"jobs": []}, [{"Id": "sha256:" + "7" * 64}])):
            with self.subTest(jobs=jobs, image=image):
                self.jobs, self.image = jobs, image
                with self.assertRaises(module.Refused):
                    self.lifecycle.info()
        self.assertEqual(self.mutations, [])

    def test_changed_launcher_options_or_guarded_identity_is_refused(self):
        for launcher, identity in (("changed-options", "fixture-identity"), ("fixture-launcher", "changed-identity")):
            with self.subTest(launcher=launcher, identity=identity):
                self.launcher = launcher
                self.file_hashes["/data/config/node-identity.json"] = identity
                with self.assertRaises(module.Refused):
                    self.lifecycle.info()
        self.assertEqual(self.mutations, [])

    def test_retained_native_absence_receipts_feed_the_real_proof_composition(self):
        # Only the documented CLI shape and retained identity/guard data below
        # are historical observations. Empty ps/jobs replies are local logic
        # inputs, not a claim to have queried the production host again.
        fixture = json.loads((HERE / "fixtures" / "gptadmin_haos_absence_20261117.json").read_text())
        manifest = {**fixture["manifest"], "operation_dir": str(self.root)}
        manifest_hash = fixture["original_fence"]["body"]["manifest_sha256"]
        saved = {**fixture["captured"], "manifest_sha256": manifest_hash}
        module.private_json(self.capture, saved)
        lifecycle = module.HAOS(manifest, manifest_hash)
        observed_management = fixture["management"]
        management = {
            "Id": observed_management["id"], "State": observed_management["state"],
            "Mounts": observed_management["mounts"],
            "Config": {"Hostname": "fixture-ssh"},
        }
        commands = []

        def saved_cli(argv, **kwargs):
            commands.append(tuple(argv))
            if argv[0] == lifecycle.docker:
                if argv[1:4] == ["inspect", "--type", "container"]:
                    name = argv[4]
                    if name == manifest["management_container"]:
                        return completed(stdout=json.dumps([management]).encode())
                    value = fixture["docker_absence"][name]
                    return completed(value["rc"], value["stdout"].encode(), value["stderr"].encode())
                if argv[1:2] == ["ps"]:
                    return completed(stdout=b"")
                if argv[1:3] == ["image", "inspect"]:
                    return completed(stdout=json.dumps([{"Id": manifest["image_id"]}]).encode())
            if argv[:3] == ["/usr/bin/ha", "addons", "info"]:
                return completed(stdout=json.dumps({"data": fixture["supervisor_stopped"]}).encode())
            if argv[:3] == ["/usr/bin/ha", "jobs", "info"]:
                return completed(stdout=b'{"data":{"jobs":[]}}')
            raise AssertionError("unreviewed command in retained-data logic fixture: " + repr(argv))

        guard_hashes = {g["path"]: g["sha256"] for g in manifest["file_guards"]}
        with patch.object(module, "command", side_effect=saved_cli), patch.object(
            lifecycle, "launcher_digest", return_value=saved["environment_file_sha256"]
        ), patch.object(lifecycle, "file_digest", side_effect=lambda p: guard_hashes[p]):
            value = lifecycle.info()
        self.assertTrue(lifecycle.stopped(value))
        self.assertFalse(lifecycle.has_node(value))
        self.assertEqual(value["management_container_id"], observed_management["id"])
        self.assertEqual(value["image_id"], manifest["image_id"])
        self.assertEqual((value["container_id"], value["generation"], value["pid"]), ("", "", 0))
        inspected = [c[4] for c in commands if c[1:4] == ("inspect", "--type", "container")]
        self.assertEqual(inspected, [manifest["management_container"], manifest["container"],
                                     saved["service"]["container_id"]])


class AbsentRecoveryReceiptTests(unittest.TestCase):
    """Exercise the existing durable recovery, not a new manager implementation."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.calls = []
        self.state = absent_state()
        self.original = live_state()
        self.replacement = live_state(NEW_ID, 84, "replacement:456")
        self.lifecycle = module.HAOS({"container": NODE, "addon": "fixture_gptadmin"})
        self.lifecycle.info = lambda: dict(self.state)

        def start():
            self.calls.append("start")
            self.state = dict(self.replacement)

        self.lifecycle.start = start
        self.lifecycle.stop = lambda: self.fail("recovery must not submit another stop")
        self.runner = object.__new__(module.Controller)
        self.runner.manifest_hash = "fixture-manifest"
        self.runner.work = self.root / "operation"
        self.runner.work.mkdir(mode=0o700)
        self.runner.m = {
            "adapter": "haos", "local_origin": "http://fixture.invalid",
            "recovery_ready_timeout": 2,
        }
        self.runner.lifecycle = self.lifecycle
        self.runner.snapshot = lambda: {
            "service": dict(self.original),
            "environment_file_sha256": "fixture-original-launcher",
            "environment": {"CTL_TOKEN": "fixture-only-not-a-credential"},
            "auxiliaries": {"watchdog": False, "boot": "auto", "auto_update": True},
        }
        self.runner.runtime_guards = lambda: None
        self.runner.launcher_digest = lambda: "fixture-original-launcher"
        self.runner.check_binary = lambda info: self.calls.append(("binary", info["pid"]))
        fence = {
            "manifest_sha256": self.runner.manifest_hash, "plan_digest": None,
            "service": self.runner.service_identity(self.original),
            "phase": "stop_requested", "reason": "fixture-owned-drain",
        }
        module.private_json(self.runner.fence_path(), fence, True)
        self.command_patch = patch.object(
            module, "command", side_effect=AssertionError("real command is forbidden in this fixture")
        )
        self.command_patch.start()
        self.addCleanup(self.command_patch.stop)

    @staticmethod
    def ready_http(origin, token, action=None, path="/admin/api/node/drain"):
        if action is not None:
            raise AssertionError("a restarted, undrained fixture needs no control POST")
        return (200, {"ok": True}) if path == "/healthz" else (200, {"draining": False})

    def test_removed_container_recovers_once_and_records_the_new_real_generation(self):
        with patch.object(module, "http_json", side_effect=self.ready_http):
            receipt = self.runner.execute("recovery-fence", "restore-fenced", [])
            cached = self.runner.execute("recovery-fence", "restore-fenced", [])
        self.assertEqual(receipt, cached)
        self.assertEqual(self.calls.count("start"), 1)
        result = receipt["result"]
        self.assertTrue(result["original_runtime_restored"])
        self.assertEqual(result["service"]["container_id"], NEW_ID)
        self.assertEqual(result["service"]["generation"], "replacement:456")
        self.assertFalse(result["drain_cancel_issued"])
        fence = module.read_json(self.runner.fence_path())
        self.assertEqual(fence["phase"], "recovered")
        self.assertEqual(fence["service"]["container_id"], OLD_ID)
        self.assertEqual(fence["recovery_service"]["container_id"], NEW_ID)

    def test_unknown_start_is_not_replayed_under_same_or_different_step_id(self):
        def uncertain_start():
            self.calls.append("start")
            raise module.CommandFailure("fixture start timed out", {"timed_out": True})

        self.lifecycle.start = uncertain_start
        for step in ("recovery-fence", "recovery-fence", "different-step"):
            with self.subTest(step=step), self.assertRaises(module.Refused):
                self.runner.execute(step, "restore-fenced", [])
        self.assertEqual(self.calls.count("start"), 1)
        self.assertEqual(
            module.read_json(self.runner.fence_path())["phase"], "recovery_start_requested"
        )

    def test_absence_without_this_owned_stop_cannot_start_a_service(self):
        fence = module.read_json(self.runner.fence_path())
        fence["phase"] = "fenced"
        module.private_json(self.runner.fence_path(), fence)
        with self.assertRaises(module.Refused):
            self.runner.execute("recovery-fence", "restore-fenced", [])
        self.assertNotIn("start", self.calls)


if __name__ == "__main__":
    unittest.main()
