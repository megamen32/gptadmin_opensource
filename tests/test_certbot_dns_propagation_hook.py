"""Local wait-policy regression tests; these do not prove DNS or ACME works."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


HOOK = Path(__file__).resolve().parents[1] / "scripts/certbot_dns_propagation_hook.sh"
HARNESS = r'''
dig() {
  case "$*" in
    *+answer*) printf '%s\n' '_acme-challenge.example.test. 2 IN TXT "test-value"'; return ;;
  esac
  # Command substitutions run in subshells; persist only this fixture's
  # observation number. Wall-clock scheduling may skip SECONDS == 1.
  count=$(cat "$PROBE_COUNT" 2>/dev/null || printf 0)
  count=$((count + 1))
  printf '%s' "$count" > "$PROBE_COUNT"
  if (( count == 2 )); then
    if [[ "$PROBE_CASE" == failure ]]; then return 9; fi
    if [[ "$PROBE_CASE" == absent ]]; then return 0; fi
  fi
  printf '%s\n' '"test-value"'
}
# Advance Bash's elapsed-time counter deterministically instead of relying on
# wall-clock scheduling around integer-second SECONDS boundaries.
sleep() { SECONDS=$((SECONDS + PROBE_INTERVAL)); }
export -f dig sleep
bash "$1"
'''


class DNSPropagationHookTests(unittest.TestCase):
    def run_case(self, case, interval="1"):
        env = dict(os.environ, CERTBOT_DOMAIN="example.test", CERTBOT_VALIDATION="test-value",
                   DNS_AUTH_HOOK="/bin/true", DNS_AUTHORITIES="ns.example.test",
                   DNS_PROPAGATION_TTL="0", DNS_PROPAGATION_TIMEOUT="4", PROBE_CASE=case)
        with tempfile.TemporaryDirectory() as directory:
            env.update(PROBE_COUNT=str(Path(directory) / "count"), PROBE_INTERVAL=interval)
            return subprocess.run(["bash", "-c", HARNESS, "test", str(HOOK)], env=env,
                                  text=True, capture_output=True, timeout=8)

    def test_lookup_failure_does_not_erase_observed_propagation(self):
        result = self.run_case("failure")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_positive_answer_without_challenge_restarts_wait(self):
        result = self.run_case("absent")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("timed out", result.stderr)

    def test_missing_answer_is_exercised_when_scheduling_skips_second_one(self):
        result = self.run_case("absent", interval="2")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("timed out", result.stderr)


if __name__ == "__main__":
    unittest.main()
