from pathlib import Path
import subprocess
import unittest


WORKFLOW = Path(__file__).resolve().parents[2] / ".github/workflows/deploy-edge.yml"


def rollback_function() -> str:
    workflow = WORKFLOW.read_text(encoding="utf-8")
    body = workflow.split("          rollback() {\n", 1)[1].split("          trap rollback EXIT", 1)[0]
    return "rollback() {\n" + body


class RelayPolicyRollbackTrapTests(unittest.TestCase):
    def run_trap(self, failure: str, original_status: int = 0) -> subprocess.CompletedProcess[str]:
        script = """set -euo pipefail
rollback_armed=1
rollback_receipt=/tmp/rollback.json
APPLIED_PROJECT_RECEIPT=/tmp/applied.json
EDGE_DEPLOY_BIN=/tmp/bahia-deploy-edge
ready_url=http://edge.example:8080/ready
EDGE_HEALTH_HOST=edge.example
baseline=/tmp/baseline.json
/tmp/bahia-deploy-edge() { [[ "$FAIL" != sdk-apply ]]; }
curl() {
  case "$*" in
    *:8080/ready*) [[ "$FAIL" != readiness ]];;
    *:3334/relay*) [[ "$FAIL" != relay ]];;
    *:8081/*) [[ "$FAIL" != web ]];;
    *) return 0;;
  esac
}
python3() { [[ "$FAIL" != policy-gate ]]; }
""" + rollback_function() + f"\ntrap rollback EXIT\nexit {original_status}\n"
        return subprocess.run(
            ["bash", "-c", script], env={"FAIL": failure},
            capture_output=True, text=True, check=False,
        )

    def test_each_failed_rollback_check_is_loud_and_nonzero(self):
        checks = {
            "sdk-apply": "typed Compose project apply",
            "readiness": "Bahia readiness",
            "policy-gate": "relay-policy invariant",
            "relay": "relay HTTP endpoint",
            "web": "web HTTP endpoint",
        }
        self.assertNotIn("|| true", rollback_function())
        self.assertIn('"$EDGE_DEPLOY_BIN" rollback --receipt "$rollback_receipt"', rollback_function())
        for failure, message in checks.items():
            with self.subTest(failure=failure):
                result = self.run_trap(failure)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn(message, result.stderr)

    def test_deploy_never_writes_base_or_uses_compose_cli(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertNotIn('cp "$rollback" "$COMPOSE_FILE"', workflow)
        self.assertNotIn("docker compose -f", workflow)
        self.assertIn('"$EDGE_DEPLOY_BIN" apply --base-file "$COMPOSE_FILE"', workflow)
        self.assertIn('"$EDGE_DEPLOY_BIN" snapshot --source-receipt "$backup"', workflow)
        self.assertIn('"$EDGE_DEPLOY_BIN" rollback --receipt "$rollback_receipt"', workflow)

    def test_successful_rollback_preserves_original_rollout_failure(self):
        result = self.run_trap("none", original_status=23)
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertNotIn("Rollback verification failed", result.stderr)


if __name__ == "__main__":
    unittest.main()
