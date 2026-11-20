"""Read-only acceptance tests from the observed six-node membership projection.

The successful compensation receipt fc021c03dbedef15d2dffd6c4ff4b33f5884df6973d6d25c8be7b1cc3f86776f
retained these exact safe node fields and Go nanosecond lease strings. This
fixture preserves the real API envelope; it is not a replay of its credentials
or a claim that the retained historical leases are live now.
"""
import copy
from datetime import datetime, timezone
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location(
    "authority_membership_test_subject",
    Path(__file__).resolve().parents[1] / "gptadmin_authority_controller.py")
C = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(C)

OBSERVED = json.loads(r'''{
  "membership": {
    "writer_id": "bd0bf8c4-f7fe-4d1b-a82c-aa0862e11bd8",
    "writer_url": "https://mcp.bezrabotnyi.com",
    "revision": 956,
    "nodes": [
      {
        "endpoints": [
          "https://quorum44-node.bezrabotnyi.com"
        ],
        "lease_expires": "2026-11-17T15:14:17.555036715Z",
        "local_id": "a5a875dd-f32d-4f7a-98d9-7069bd574f6e",
        "name": "shell:node-44",
        "node_id": "764231b2ea705fd0544b0713aa6d8bd1",
        "snapshot_revision": 2097,
        "state": "ready"
      },
      {
        "endpoints": [
          "https://s88-node.t.became.bezrabotnyi.com"
        ],
        "lease_expires": "2026-11-17T15:14:16.248179536Z",
        "local_id": "f300f838-1ba8-4cb9-bae0-e8b477763913",
        "name": "shell:admin-node-88",
        "node_id": "250208ba8276dcc81fecad94e4839653",
        "snapshot_revision": 2097,
        "state": "ready"
      },
      {
        "endpoints": [
          "https://s100-node.t.became.bezrabotnyi.com"
        ],
        "lease_expires": "2026-11-17T15:13:44.016816276Z",
        "local_id": "bd0bf8c4-f7fe-4d1b-a82c-aa0862e11bd8",
        "name": "shell:admin-node-100",
        "node_id": "bd0bf8c4-f7fe-4d1b-a82c-aa0862e11bd8",
        "snapshot_revision": null,
        "state": "ready"
      },
      {
        "endpoints": [
          "https://node-mini.t.became.bezrabotnyi.com"
        ],
        "lease_expires": "2026-11-17T15:14:04.680761653Z",
        "local_id": "go-shellmcp-6d5289a76f0d701c",
        "name": "shell:mac-mini-node",
        "node_id": "77ad76b118b7f3a90a80e7630437eb7c",
        "snapshot_revision": 2097,
        "state": "ready"
      },
      {
        "endpoints": [
          "https://node-intel.t.became.bezrabotnyi.com"
        ],
        "lease_expires": "2026-11-17T15:14:02.521880322Z",
        "local_id": "go-shellmcp-f2f7f271d22f1b46",
        "name": "shell:mac-intel-node",
        "node_id": "eed8d12ee91db04022114705af94b124",
        "snapshot_revision": 2097,
        "state": "ready"
      },
      {
        "endpoints": [
          "http://203.0.113.10:9001"
        ],
        "lease_expires": "2026-11-17T15:14:15.044580346Z",
        "local_id": "haos-4283a8b4-0ff8-4b6d-be6c-14117c07e852",
        "name": "shell:haos",
        "node_id": "8ff996aeb86b3a701611ed16e8d113b7",
        "snapshot_revision": 2097,
        "state": "ready"
      }
    ]
  },
  "revision": 956,
  "sha256": "91d63e2c893d3c15f8c5b7d27f5dc6869f9c07644142c254c5cbbd3ebb095612"
}''')


class FixtureClock(datetime):
    @classmethod
    def now(cls, tz=None):
        value = cls(2026, 11, 17, 15, 13, 0, tzinfo=timezone.utc)
        return value if tz is not None else value.replace(tzinfo=None)


class MembershipAcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.transition = Path(self.directory.name) / "transition.json"
        self.transition.write_text(json.dumps({
            "writer_id": OBSERVED["membership"]["writer_id"],
            "writer_url": "https://mcp.bezrabotnyi.com",
            "source_identity": {"node_key": "synthetic-membership-credential"},
        }))
        self.transition_sha = C.digest(self.transition.read_bytes())
        self.expected = {
            "writer_id": OBSERVED["membership"]["writer_id"],
            "auth_generation": 2097,
            "members": {
                node["name"]: {"node_id": node["node_id"],
                               "local_id": node["local_id"],
                               "endpoint": node["endpoints"][0]}
                for node in OBSERVED["membership"]["nodes"]
            },
        }
        self.controller = C.Controller.__new__(C.Controller)

    def verify(self, wire, status=200):
        # One bounded observation. Production retry behavior is unchanged.
        with mock.patch.object(C, "http_json", return_value=(status, wire)) as request, \
             mock.patch.object(C, "datetime", FixtureClock), \
             mock.patch.object(C.time, "monotonic", side_effect=[0, 1, 51]), \
             mock.patch.object(C.time, "sleep"):
            value = self.controller.verify_membership(
                str(self.transition), self.transition_sha, json.dumps(self.expected))
        request.assert_called_once_with(
            "https://mcp.bezrabotnyi.com", "synthetic-membership-credential",
            path="/admin/api/nodes/membership")
        return value

    def refuse(self, wire, status=200):
        with self.assertRaises(C.Refused):
            self.verify(wire, status)

    def test_observed_envelope_and_all_six_exact_node_fields(self):
        result = self.verify(copy.deepcopy(OBSERVED))
        self.assertEqual(len(result["members"]), 6)
        self.assertEqual(result["members"], OBSERVED["membership"]["nodes"])
        self.assertEqual(result["writer_id"], self.expected["writer_id"])
        self.assertEqual(result["revision"], 956)
        self.assertEqual(result["body_sha256"], C.digest(json.dumps(OBSERVED, sort_keys=True).encode()))
        self.assertEqual(result["public_mcp_execution_acceptance"], "pending-independent-caller")

    def test_eight_digit_lease_is_accepted_without_rewriting_wire_value(self):
        wire = copy.deepcopy(OBSERVED)
        wire["membership"]["nodes"][0]["lease_expires"] = "2026-11-17T15:14:17.55503671Z"
        result = self.verify(wire)
        self.assertEqual(result["members"][0]["lease_expires"], "2026-11-17T15:14:17.55503671Z")

    def test_envelope_is_required_even_if_top_level_claims_match(self):
        self.refuse(copy.deepcopy(OBSERVED["membership"]))
        wire = copy.deepcopy(OBSERVED)
        wire["writer_id"] = self.expected["writer_id"]
        wire["membership"]["writer_id"] = "wrong"
        self.refuse(wire)
        wire["membership"] = []
        self.refuse(wire)

    def test_authentication_error_never_accepts_a_correct_body(self):
        for status in (401, 403, 503):
            with self.subTest(status=status):
                self.refuse(copy.deepcopy(OBSERVED), status)

    def test_each_node_identity_route_state_and_snapshot_remain_required(self):
        for index, original in enumerate(OBSERVED["membership"]["nodes"]):
            changes = {
                "node_id": "different-node", "local_id": "different-local",
                "name": "shell:different-owner", "state": "offline",
                "endpoints": ["https://wrong.example"],
                "lease_expires": "2026-11-17T15:12:59.999999999Z",
            }
            if original["node_id"] != self.expected["writer_id"]:
                changes["snapshot_revision"] = 2096
            for field, value in changes.items():
                with self.subTest(node=original["name"], field=field):
                    wire = copy.deepcopy(OBSERVED)
                    wire["membership"]["nodes"][index][field] = value
                    self.refuse(wire)

    def test_missing_duplicate_and_malformed_nodes_refuse(self):
        for transform in (
            lambda nodes: nodes[:-1],
            lambda nodes: nodes + [copy.deepcopy(nodes[0])],
            lambda nodes: nodes + [None],
            lambda nodes: "not-a-node-array",
        ):
            with self.subTest(transform=transform):
                wire = copy.deepcopy(OBSERVED)
                wire["membership"]["nodes"] = transform(wire["membership"]["nodes"])
                self.refuse(wire)

    def test_missing_or_noninteger_reader_snapshot_refuses(self):
        for value in (None, "2097", True):
            with self.subTest(value=value):
                wire = copy.deepcopy(OBSERVED)
                wire["membership"]["nodes"][0]["snapshot_revision"] = value
                self.refuse(wire)

    def test_invalid_naive_or_malformed_leases_refuse_without_type_error(self):
        for value in (None, "", "2026-11-17T15:14:17", "2026-11-17 15:14:17Z",
                      "2026-11-17T15:14:17.1234567890Z", "2026-11-17T15:14:17+24:00",
                      "2026-11-17T15:14:17+00:60", "2026-02-30T15:14:17Z"):
            with self.subTest(value=value):
                wire = copy.deepcopy(OBSERVED)
                wire["membership"]["nodes"][0]["lease_expires"] = value
                self.refuse(wire)

    def test_transition_digest_and_writer_are_checked_before_network_read(self):
        self.transition.write_text(self.transition.read_text() + " ")
        with mock.patch.object(C, "http_json") as request, self.assertRaises(C.Refused):
            self.controller.verify_membership(str(self.transition), self.transition_sha,
                                              json.dumps(self.expected))
        request.assert_not_called()


class RFC3339NanoLeaseTests(unittest.TestCase):
    def test_precision_and_explicit_offsets(self):
        for value, expected in (
            ("2026-11-17T15:14:17Z", datetime(2026, 11, 17, 15, 14, 17, tzinfo=timezone.utc)),
            ("2026-11-17T15:14:17.5Z", datetime(2026, 11, 17, 15, 14, 17, 500000, tzinfo=timezone.utc)),
            ("2026-11-17T15:14:17.55503671Z", datetime(2026, 11, 17, 15, 14, 17, 555036, tzinfo=timezone.utc)),
            ("2026-11-17T18:14:17.555036715+03:00", datetime(2026, 11, 17, 15, 14, 17, 555036, tzinfo=timezone.utc)),
            ("2026-11-17T10:14:17.555036715-05:00", datetime(2026, 11, 17, 15, 14, 17, 555036, tzinfo=timezone.utc)),
        ):
            with self.subTest(value=value):
                self.assertEqual(C.parse_membership_lease(value), expected)


if __name__ == "__main__":
    unittest.main()
