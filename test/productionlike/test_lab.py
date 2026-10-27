import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("lab", Path(__file__).with_name("lab.py"))
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class DeliveryBoundary(unittest.TestCase):
    def test_dispatch_event_uses_wire_strings_for_typed_workflow_inputs(self):
        self.assertEqual(lab.event_inputs({"postgres": True,"unhealthy": False,"version": "v1"}),
                         {"postgres": "true","unhealthy": "false","version": "v1"})

    def test_requested_acceptance_cannot_silently_skip(self):
        run={"inputs":{"postgres":True}}
        for logs in [b"", b'{"step":"Required PostgreSQL integration","stepResult":"failure","stage":"Main"}']:
            result={"state":"success"}
            lab.validate_acceptance(result,run,logs)
            self.assertEqual(result["state"],"failure")
        result={"state":"success"}
        lab.validate_acceptance(result,run,b'{"step":"Required PostgreSQL integration","stepResult":"success","stage":"Main"}')
        self.assertEqual(result["state"],"success")
        lab.validate_acceptance(result,{"inputs":{"force_failure":True}},b"")
        self.assertEqual(result["state"],"failure")

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        lab.save(self.root / "test-run.json", {"sha": "a" * 40})

    def test_failure_or_missing_digest_never_calls_management_api(self):
        cases = [
            {"state": "failure", "job_outputs": {"build-push": {"digest": "sha256:" + "a" * 64}}},
            {"state": "success"},
            {"state": "success", "job_outputs": {"build-push": {"digest": "latest"}}},
            {"state": "success", "pod_phase": "Running", "engine_running": True, "job_outputs": {"build-push": {"digest": "sha256:" + "a" * 64}}},
            {"state": "success", "pod_phase": "Failed", "engine_running": False, "job_outputs": {"build-push": {"digest": "sha256:" + "a" * 64}}},
        ]
        for result in cases:
            lab.save(self.root / "test-result.json", result)
            with mock.patch.object(lab, "forgelet") as api:
                with self.assertRaises(ValueError):
                    lab.deliver(self.root, "test")
                api.assert_not_called()

    def test_evicted_pod_is_recorded_without_reading_nonexistent_logs(self):
        lab.save(self.root / "test-run.json", {"job": "lab-test"})
        pod = {"metadata": {"name": "lab-test-123", "uid": "pod-uid"},
               "status": {"phase": "Failed", "reason": "Evicted", "message": "engine-data exceeded limit"}}
        with mock.patch.object(lab, "kubectl", return_value=lab.json.dumps({"items": [pod]}).encode()) as kube:
            lab.collect(self.root, "test")
            self.assertEqual(kube.call_count, 1)
        result = lab.json.loads((self.root / "test-result.json").read_text())
        self.assertEqual(result["state"], "failure")
        self.assertEqual(result["reason"], "Evicted")

    def test_success_delivers_digest_and_fixed_source_identity(self):
        digest = "sha256:" + "b" * 64
        lab.save(self.root / "test-result.json", {"state": "success", "pod_phase": "Succeeded", "engine_running": False, "job_outputs": {"build-push": {"digest": digest}}})
        with mock.patch.object(lab, "forgelet", side_effect=[{"version": 3}, {"id": "delivery"}]) as api:
            lab.deliver(self.root, "test")
            args = api.call_args_list[1].args
            self.assertEqual(args[1], "/api/github/run")
            self.assertEqual(args[2]["sha"], "a" * 40)
            self.assertEqual(args[2]["inputs"], {"digest": digest})
            self.assertEqual(args[2]["path"], ".github/workflows/homelab.yml")
            self.assertNotIn("secrets", args[2])

    def test_private_state_permissions(self):
        path = self.root / "credential"
        path.write_text("old")
        path.chmod(0o644)
        lab.save(path, b"private")
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_cancel_refuses_unowned_job_before_mutation(self):
        lab.save(self.root / "test-run.json", {"job": "other"})
        with mock.patch.object(lab,"kubectl",return_value=b'{"metadata":{"labels":{}}}') as kube:
            with self.assertRaises(ValueError): lab.cancel(self.root,"test")
            self.assertEqual(kube.call_count,1)

    def test_token_scope_rejects_extra_repositories_and_permissions(self):
        for repos,permissions in [([1,2],{"contents":"read"}),([1],{"contents":"write"}),([1],{"contents":"read","checks":"write"})]:
            with mock.patch.object(lab,"shell",return_value=b"signature"), mock.patch.object(lab,"github",return_value={"repositories":[{"id":i} for i in repos],"permissions":permissions,"token":"not-issued"}):
                with self.assertRaises(ValueError):lab.read_token(self.root,[1])
        with mock.patch.object(lab,"shell",return_value=b"signature"), mock.patch.object(lab,"github",return_value={"repositories":[{"id":1}],"permissions":{"contents":"read","metadata":"read"},"token":"limited"}) as github:
            self.assertEqual(lab.read_token(self.root,[1]),"limited")
            self.assertEqual(github.call_args.args[2],{"repository_ids":[1],"permissions":{"contents":"read"}})


if __name__ == "__main__":
    unittest.main()
