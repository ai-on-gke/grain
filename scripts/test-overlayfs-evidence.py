#!/usr/bin/env python3
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Fixture tests: no real Docker, sudo, systemd or deployment access."""
import importlib.util
import io
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("evidence", ROOT / "overlayfs-evidence.py")
evidence = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evidence)


class EvidenceTest(unittest.TestCase):
    def records(self, output):
        return [json.loads(line) for line in output.getvalue().splitlines()]

    def test_all_commands_are_bounded_local_reads_and_missing_privilege_is_reported(self):
        calls = []
        def run(argv, timeout):
            calls.append((argv, timeout))
            return {"status": "unavailable", "error": "permission denied"}
        with tempfile.TemporaryDirectory() as directory:
            output = io.StringIO()
            evidence.Collector(output, Path(directory), run).collect()
        self.assertTrue(all(0 < timeout <= 4 for _, timeout in calls))
        allowed = {"uname", "docker", "systemctl", "cat", "journalctl", "sudo"}
        self.assertTrue(all(argv[0] in allowed for argv, _ in calls))
        for argv, _ in calls:
            if argv[0] == "docker":
                self.assertEqual(argv[1:3], ["--host", "unix:///var/run/docker.sock"])
                self.assertIn(argv[3], ("version", "info", "events", "inspect"))
            if "journalctl" in argv and "--list-boots" not in argv:
                self.assertIn("--lines", argv)
                self.assertIn("2026-11-18 03:10:50 UTC", argv)
                self.assertIn("2026-11-18 03:11:10 UTC", argv)
        records = self.records(output)
        self.assertEqual(records[-1]["section"], "summary")
        self.assertEqual(records[-1]["status"], "incomplete")
        self.assertTrue(any(r.get("error") == "permission denied" for r in records))

    def make_proc(self, root, pid, namespace, device):
        p = root / str(pid)
        (p / "ns").mkdir(parents=True)
        (p / "ns/mnt").symlink_to(namespace)
        (p / "root").symlink_to("/")
        (p / "cwd").symlink_to("/tmp")
        (p / "fdinfo").mkdir()
        (p / "fd").mkdir()
        (p / "fdinfo/7").write_text("pos: 0\nflags: 0100002\nmnt_id: 42\n")
        (p / "fd/7").symlink_to("/example/writable")
        (p / "environ").write_text("DO_NOT_READ_ENV")
        (p / "cmdline").write_text("DO_NOT_READ_CMDLINE")
        (p / "mountinfo").write_text(
            "42 1 " + device + " / /merged rw - overlay overlay rw,upperdir=/upper,workdir=/work\n")

    def test_cloned_namespace_mounts_are_distinguished_from_independent_superblocks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.make_proc(root, 101, "mnt:[1]", "0:44")
            self.make_proc(root, 102, "mnt:[2]", "0:44")
            output = io.StringIO()
            evidence.Collector(output, root).mounts()
            self.assertFalse(any(r["section"] == "overlap_candidate" for r in self.records(output)))
            self.make_proc(root, 103, "mnt:[3]", "0:55")
            output = io.StringIO()
            evidence.Collector(output, root).mounts()
            records = self.records(output)
            self.assertTrue(any(r["section"] == "overlap_candidate" for r in records))
            fd = next(r for r in records if r["section"] == "overlay_fd")
            self.assertEqual(fd["mnt_id"], "42")
            self.assertEqual(fd["flags"], "0100002")
            self.assertNotIn("DO_NOT_READ", output.getvalue())

    def test_namespace_visibility_loss_is_not_silently_reported_as_clean(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "123").mkdir()  # An inaccessible/exited namespace, not a clean snapshot.
            output = io.StringIO()
            collector = evidence.Collector(output, root)
            collector.mounts()
            self.assertTrue(collector.incomplete)
            self.assertTrue(any(r["section"] == "namespace_visibility" for r in self.records(output)))

    def test_output_cap_preserves_a_parseable_final_summary(self):
        output = io.StringIO()
        collector = evidence.Collector(output)
        collector.emit("large", text="x" * evidence.MAX_BYTES)
        collector.emit("summary", status="incomplete", budget_exhausted=collector.limited)
        self.assertLess(len(output.getvalue().encode()), evidence.MAX_BYTES)
        self.assertTrue(self.records(output)[-1]["budget_exhausted"])

    def test_global_deadline_prevents_more_commands(self):
        called = []
        collector = evidence.Collector(io.StringIO(), runner=lambda *args: called.append(args), seconds=-1)
        collector.run("late", ["uname", "-r"])
        self.assertFalse(called)
        self.assertTrue(collector.limited)

    def test_command_timeout_and_output_limit_are_enforced(self):
        start = time.monotonic()
        result = evidence.command([sys.executable, "-c", "import time;time.sleep(30)"], 0.1)
        self.assertEqual(result["status"], "timeout")
        self.assertLess(time.monotonic() - start, 3)
        result = evidence.command([sys.executable, "-c", "print('x'*1000000)"], 2, limit=1024)
        self.assertEqual(result["status"], "truncated")
        self.assertEqual(len(result["output"]), 1024)

    def test_common_credentials_are_redacted(self):
        text = "Environment=PASSWORD=bad\n--token=bad Bearer bad https://user:bad@example.com"
        clean = evidence.redact(text)
        self.assertNotIn("bad", clean)
        self.assertIn("<redacted>", clean)

    def test_caller_arguments_are_rejected_before_any_host_read(self):
        result = subprocess.run([sys.executable, "-I", str(ROOT / "overlayfs-evidence.py"), "$(touch /tmp/not-executed)"],
                                capture_output=True, text=True, timeout=3)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")

    def test_registration_embeds_exact_collector_and_requires_approval(self):
        result = subprocess.run([sys.executable, "-I", str(ROOT / "overlayfs-evidence-host-script.py")],
                                capture_output=True, text=True, check=True, timeout=3)
        bundle = json.loads(result.stdout)
        entry = bundle["entry"]
        script = entry["config"]["scripts"][0]
        self.assertEqual(entry["run"], "host")
        self.assertEqual(entry["command"], "grain-host-script")
        self.assertTrue(script["approve"])
        self.assertEqual(script["max_args"], 0)
        self.assertEqual(script["max_stdin"], 0)
        self.assertIn((ROOT / "overlayfs-evidence.py").read_text(), script["script"])
        self.assertEqual(bundle["grant"]["with"]["script"], script["name"])
        subprocess.run(["bash", "-n"], input=script["script"], text=True, check=True, timeout=3)


if __name__ == "__main__":
    unittest.main()
