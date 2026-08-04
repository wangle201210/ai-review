from __future__ import annotations

import base64
from contextlib import redirect_stderr, redirect_stdout
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
from io import StringIO
import json
import os
from pathlib import Path
import threading
import unittest
from unittest.mock import patch
import urllib.parse


SCRIPT_PATH = Path(__file__).resolve().parents[1] / "scripts" / "trigger_build.py"
SPEC = importlib.util.spec_from_file_location("trigger_build", SCRIPT_PATH)
assert SPEC is not None and SPEC.loader is not None
trigger_build = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(trigger_build)


class MockJenkinsHandler(BaseHTTPRequestHandler):
    expected_authorization = ""
    submissions: list[dict[str, object]] = []

    def do_GET(self) -> None:
        if self.path != "/crumbIssuer/api/json":
            self.send_error(404)
            return
        if self.headers.get("Authorization") != self.expected_authorization:
            self.send_error(401)
            return
        body = json.dumps(
            {"crumbRequestField": "Jenkins-Crumb", "crumb": "test-crumb"}
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self) -> None:
        if self.headers.get("Authorization") != self.expected_authorization:
            self.send_error(401)
            return
        if self.headers.get("Jenkins-Crumb") != "test-crumb":
            self.send_error(403)
            return
        length = int(self.headers.get("Content-Length", "0"))
        form = urllib.parse.parse_qs(self.rfile.read(length).decode())
        payload = json.loads(form["json"][0])
        self.submissions.append({"path": self.path, "payload": payload})
        self.send_response(201)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, format: str, *args: object) -> None:
        return


class CrossOriginRedirectHandler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        self.send_response(302)
        self.send_header("Location", "http://example.invalid/credential-leak")
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, format: str, *args: object) -> None:
        return


class TriggerBuildTest(unittest.TestCase):
    def run_server(self, handler: type[BaseHTTPRequestHandler]):
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join, 2)
        self.addCleanup(server.shutdown)
        return server

    def test_all_bundled_groups_contain_valid_projects(self) -> None:
        groups = trigger_build.available_groups()
        self.assertIn("all", groups)
        self.assertIn("all_41", groups)
        for group in groups:
            name, projects = trigger_build.read_project_group(group)
            self.assertEqual(name, group)
            self.assertTrue(trigger_build.unique_projects(projects), group)

    def test_preview_expands_bundled_group_without_credentials(self) -> None:
        stdout = StringIO()
        with patch.dict(os.environ, {}, clear=True), redirect_stdout(stdout):
            status = trigger_build.main(["--group", "all_41"])

        result = json.loads(stdout.getvalue())
        self.assertEqual(status, 0)
        self.assertFalse(result["network_request_sent"])
        self.assertEqual(result["groups"], ["all_41"])
        self.assertEqual(result["branch"], "main")
        self.assertIn("kraken", result["projects"])
        self.assertEqual(
            result["credential_sources"], ["JENKINS_USER", "JENKINS_TOKEN"]
        )

    def test_execute_sends_authenticated_crumb_and_build_form(self) -> None:
        user = "jenkins-user"
        token = "jenkins-token"
        encoded = base64.b64encode(f"{user}:{token}".encode()).decode()
        MockJenkinsHandler.expected_authorization = f"Basic {encoded}"
        MockJenkinsHandler.submissions = []
        server = self.run_server(MockJenkinsHandler)
        base_url = f"http://127.0.0.1:{server.server_port}"
        stdout = StringIO()
        stderr = StringIO()

        with (
            patch.dict(
                os.environ,
                {"JENKINS_USER": user, "JENKINS_TOKEN": token},
                clear=True,
            ),
            redirect_stdout(stdout),
            redirect_stderr(stderr),
        ):
            status = trigger_build.main(
                [
                    "--project",
                    "wealth",
                    "--branch",
                    "version/v2.15.1",
                    "--deploy-env",
                    "test-env",
                    "--operation-type",
                    "u",
                    "--base-url",
                    base_url,
                    "--execute",
                ]
            )

        self.assertEqual(status, 0, stderr.getvalue())
        result = json.loads(stdout.getvalue())
        self.assertEqual(result["status"], "submitted")
        self.assertEqual(result["http_status"], 201)
        self.assertNotIn(token, stdout.getvalue())
        self.assertEqual(len(MockJenkinsHandler.submissions), 1)
        submission = MockJenkinsHandler.submissions[0]
        self.assertEqual(
            submission["path"],
            "/job/nova/job/game-play/job/wealth/build?delay=0sec",
        )
        parameters = {
            item["name"]: item.get("value")
            for item in submission["payload"]["parameter"]
        }
        self.assertEqual(parameters["GitBranch"], "version/v2.15.1")
        self.assertEqual(parameters["DeployMicroServices"], "wealth")
        self.assertEqual(parameters["OperatingEnvs"], "test-env")
        self.assertEqual(parameters["OperationType"], "UpdateConfigMapAndRestart")
        self.assertEqual(submission["payload"]["Jenkins-Crumb"], "test-crumb")

    def test_execute_requires_both_environment_credentials(self) -> None:
        stdout = StringIO()
        stderr = StringIO()
        with (
            patch.dict(os.environ, {}, clear=True),
            redirect_stdout(stdout),
            redirect_stderr(stderr),
        ):
            status = trigger_build.main(
                ["--project", "wealth", "--branch", "main", "--execute"]
            )

        self.assertEqual(status, 2)
        self.assertEqual(stdout.getvalue(), "")
        self.assertIn("JENKINS_USER and JENKINS_TOKEN", stderr.getvalue())

    def test_execute_rejects_cross_origin_redirect(self) -> None:
        server = self.run_server(CrossOriginRedirectHandler)
        base_url = f"http://127.0.0.1:{server.server_port}"
        stderr = StringIO()
        with (
            patch.dict(
                os.environ,
                {"JENKINS_USER": "user", "JENKINS_TOKEN": "token"},
                clear=True,
            ),
            redirect_stdout(StringIO()),
            redirect_stderr(stderr),
        ):
            status = trigger_build.main(
                [
                    "--project",
                    "wealth",
                    "--branch",
                    "main",
                    "--base-url",
                    base_url,
                    "--execute",
                ]
            )

        self.assertEqual(status, 1)
        self.assertIn("cross-origin redirect", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
