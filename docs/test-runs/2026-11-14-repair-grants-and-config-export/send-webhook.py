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

"""Deliver a signed, concluded GitHub Actions run to a grain daemon's
-github-webhook-addr: one workflow_job per job, then the workflow_run.

  send-webhook.py ADDR SECRET REPO WORKFLOW BRANCH RUN_ID CONCLUSION JOB=CONCLUSION...

A tag BRANCH (v1.2.0-rc.1) is sent as a tag ref, the way Stage 1 runs are.
"""

import hashlib
import hmac
import json
import sys
import urllib.request
import uuid

addr, secret, repo, workflow, branch, run_id, conclusion = sys.argv[1:8]
jobs = [a.split("=", 1) for a in sys.argv[8:]]
run_id = int(run_id)
sha = hashlib.sha1(f"{repo}{run_id}".encode()).hexdigest()
now = "2026-11-15T14:00:00Z"
repository = {"full_name": repo, "name": repo.split("/")[1],
              "owner": {"login": repo.split("/")[0]}, "default_branch": "main"}


def send(event, payload):
    body = json.dumps(payload).encode()
    sig = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
    req = urllib.request.Request(f"http://{addr}/webhooks/github", data=body, method="POST", headers={
        "Content-Type": "application/json", "X-GitHub-Event": event,
        "X-GitHub-Delivery": str(uuid.uuid4()), "X-Hub-Signature-256": sig})
    with urllib.request.urlopen(req) as resp:
        print(event, resp.status, resp.read().decode().strip()[:200])


for i, (name, concl) in enumerate(jobs):
    send("workflow_job", {"action": "completed", "repository": repository, "workflow_job": {
        "id": run_id * 10 + i + 1, "run_id": run_id, "run_attempt": 1, "workflow_name": workflow,
        "head_branch": branch, "head_sha": sha, "name": name, "status": "completed",
        "conclusion": concl, "started_at": now, "completed_at": now,
        "html_url": f"https://github.com/{repo}/actions/runs/{run_id}/job/{run_id * 10 + i + 1}",
        "steps": [{"name": "go test ./...", "status": "completed", "conclusion": concl}]}})

send("workflow_run", {"action": "completed", "repository": repository, "workflow_run": {
    "id": run_id, "name": workflow, "path": f".github/workflows/{workflow.lower()}.yml",
    "event": "push" if not branch.startswith("v") else "push", "run_attempt": 1,
    "head_branch": branch, "head_sha": sha, "status": "completed", "conclusion": conclusion,
    "html_url": f"https://github.com/{repo}/actions/runs/{run_id}",
    "run_started_at": now, "updated_at": now, "head_repository": {"full_name": repo}}})
