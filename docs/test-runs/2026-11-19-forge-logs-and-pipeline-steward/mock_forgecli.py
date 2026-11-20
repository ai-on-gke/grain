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

"""Mock command-backed forge CLI (`forgecli`) for the 2026-11-19 §8 walkthrough."""

import io
import json
import os
import sys
import time
import zipfile

sys.dont_write_bytecode = True

state_dir = os.environ.get("FORGECLI_STATE", "/tmp/walk-s8/forgecli-state")
os.makedirs(state_dir, exist_ok=True)


def load_mode():
    path = os.path.join(state_dir, "mode.json")
    if os.path.exists(path):
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    return {}


def record_call(entry):
    with open(os.path.join(state_dir, "calls.jsonl"), "a", encoding="utf-8") as f:
        f.write(json.dumps(entry) + "\n")


def emit_http(status_line, headers, body=b""):
    out = sys.stdout.buffer
    out.write(f"HTTP/1.1 {status_line}\r\n".encode("utf-8"))
    for k, v in headers.items():
        out.write(f"{k}: {v}\r\n".encode("utf-8"))
    out.write(b"\r\n")
    if isinstance(body, str):
        body = body.encode("utf-8")
    if body:
        out.write(body)
    out.flush()


def make_run42_zip():
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", compression=zipfile.ZIP_DEFLATED) as zf:
        zf.writestr("1_build.txt", "forgecli build ok\n")
        zf.writestr("test_unit/2_run.txt", "forgecli unit run ok\n")
        zf.writestr("test_unit/1_setup.txt", "forgecli unit setup ok\n")
        zf.writestr("3_lint.txt", "forgecli lint ok\n")
    return buf.getvalue()


argv = sys.argv[1:]
if len(argv) >= 2 and argv[0] == "auth" and argv[1] == "token":
    record_call({"cmd": "auth token", "gh_host": os.environ.get("GH_HOST", "")})
    sys.stdout.write("fake-forgecli-token\n")
    sys.exit(0)

if not argv or argv[0] != "api":
    sys.stderr.write(f"unsupported forgecli argv: {argv}\n")
    sys.exit(2)

method = "GET"
headers_in = {}
endpoint = ""
i = 1
while i < len(argv):
    a = argv[i]
    if a == "--include":
        i += 1
    elif a == "--method" and i + 1 < len(argv):
        method = argv[i + 1]
        i += 2
    elif a == "-H" and i + 1 < len(argv):
        hv = argv[i + 1]
        if ":" in hv:
            k, v = hv.split(":", 1)
            headers_in[k.strip().lower()] = v.strip()
        i += 2
    elif a == "--input" and i + 1 < len(argv):
        i += 2
    else:
        endpoint = a
        i += 1

ep_path = endpoint.split("?", 1)[0].lstrip("/")
mode = load_mode()
record_call({
    "cmd": "api",
    "method": method,
    "endpoint": endpoint,
    "ep_path": ep_path,
    "if_none_match": headers_in.get("if-none-match", ""),
    "mode": mode,
})

now_iso = "2026-11-19T22:00:00Z"

if method == "HEAD":
    emit_http("200 OK", {"Content-Type": "application/json", "X-Drain-Kind": "HEAD"})
    time.sleep(float(mode.get("drain_delay", 0.15)))
    with open(os.path.join(state_dir, "drain-head.ok"), "w", encoding="utf-8") as f:
        f.write("clean-exit\n")
    sys.exit(0)

if ep_path == "user":
    emit_http("200 OK", {"Content-Type": "application/json"}, json.dumps({"login": "forge-bot"}))
    sys.exit(0)

if ep_path == "repos/acme/widgets":
    etag = '"widgets-v1"'
    if headers_in.get("if-none-match") == etag:
        emit_http("304 Not Modified", {"ETag": etag, "X-Drain-Kind": "304"})
        time.sleep(float(mode.get("drain_delay", 0.15)))
        with open(os.path.join(state_dir, "drain-304.ok"), "w", encoding="utf-8") as f:
            f.write("clean-exit\n")
        sys.exit(1)
    body = json.dumps({
        "full_name": "acme/widgets",
        "default_branch": "main",
        "permissions": {"push": True, "pull": True},
    })
    emit_http("200 OK", {"Content-Type": "application/json", "ETag": etag}, body)
    sys.exit(0)

if ep_path == "repos/acme/widgets/drain204":
    emit_http("204 No Content", {"X-Drain-Kind": "204"})
    time.sleep(float(mode.get("drain_delay", 0.15)))
    with open(os.path.join(state_dir, "drain-204.ok"), "w", encoding="utf-8") as f:
        f.write("clean-exit\n")
    sys.exit(0)

if ep_path == "repos/acme/widgets/actions/runs":
    body = json.dumps({
        "total_count": 1,
        "workflow_runs": [{
            "id": 42,
            "name": "CI",
            "path": ".github/workflows/ci.yml",
            "head_branch": "main",
            "head_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            "event": "push",
            "status": "completed",
            "conclusion": "failure",
            "run_attempt": 1,
            "created_at": now_iso,
            "updated_at": now_iso,
            "html_url": "https://depot.code.forge.test/acme/widgets/actions/runs/42",
        }],
    })
    emit_http("200 OK", {"Content-Type": "application/json"}, body)
    sys.exit(0)

if ep_path.startswith("repos/acme/widgets/actions/runs/") and ep_path.count("/") == 5:
    run_id = int(ep_path.rsplit("/", 1)[1])
    body = json.dumps({
        "id": run_id,
        "name": "CI",
        "path": ".github/workflows/ci.yml",
        "head_branch": "main",
        "head_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "event": "push",
        "status": "completed",
        "conclusion": "failure",
        "run_attempt": 1,
        "created_at": now_iso,
        "updated_at": now_iso,
        "html_url": f"https://depot.code.forge.test/acme/widgets/actions/runs/{run_id}",
    })
    emit_http("200 OK", {"Content-Type": "application/json"}, body)
    sys.exit(0)

if ep_path.endswith("/jobs") and ep_path.startswith("repos/acme/widgets/actions/runs/"):
    run_id = int(ep_path.split("/")[5])
    base = run_id * 10
    body = json.dumps({
        "total_count": 3,
        "jobs": [
            {
                "id": base + 1, "run_id": run_id, "run_attempt": 1, "name": "build",
                "status": "completed", "conclusion": "success",
                "created_at": now_iso, "started_at": now_iso, "completed_at": now_iso,
                "html_url": f"https://depot.code.forge.test/acme/widgets/actions/runs/{run_id}/job/{base+1}",
            },
            {
                "id": base + 2, "run_id": run_id, "run_attempt": 1, "name": "test/unit",
                "status": "completed", "conclusion": "failure",
                "created_at": now_iso, "started_at": now_iso, "completed_at": now_iso,
                "html_url": f"https://depot.code.forge.test/acme/widgets/actions/runs/{run_id}/job/{base+2}",
            },
            {
                "id": base + 3, "run_id": run_id, "run_attempt": 1, "name": "lint",
                "status": "completed", "conclusion": "success",
                "created_at": now_iso, "started_at": now_iso, "completed_at": now_iso,
                "html_url": f"https://depot.code.forge.test/acme/widgets/actions/runs/{run_id}/job/{base+3}",
            },
        ],
    })
    emit_http("200 OK", {"Content-Type": "application/json"}, body)
    sys.exit(0)

if ep_path.startswith("repos/acme/widgets/actions/jobs/") and ep_path.endswith("/logs"):
    job_mode = mode.get("job_log", "cut_signed_url")
    if job_mode == "cut_signed_url":
        emit_http("200 OK", {"Content-Type": "text/plain; charset=utf-8"}, "partial log line before cut\n")
        sys.stderr.write(
            'Get "https://pipelines.example.com/logs/42?sig=SUPER_SECRET_SIG&se=2099": '
            "stream error: stream ID 3; INTERNAL_ERROR\n"
        )
        sys.exit(1)
    if job_mode == "preheader_signed_url":
        sys.stderr.write(
            'Get "https://pipelines.example.com/logs/42?sig=PREHEADER_SECRET_SIG&se=2099": '
            "dial tcp: i/o timeout\n"
        )
        sys.exit(1)
    if job_mode == "cut_html":
        emit_http("200 OK", {"Content-Type": "text/html; charset=utf-8"}, "<html><body>half a pa")
        sys.stderr.write(
            'Get "https://pipelines.example.com/html/42?sig=HTML_SECRET_SIG&se=2099": '
            "unexpected EOF\n"
        )
        sys.exit(1)
    if job_mode == "ok":
        emit_http("200 OK", {"Content-Type": "text/plain"}, "direct job log ok\n")
        sys.exit(0)

if ep_path.startswith("repos/acme/widgets/actions/runs/") and ep_path.endswith("/logs"):
    run_mode = mode.get("run_zip", "cut_signed_url")
    if run_mode == "cut_signed_url":
        emit_http("200 OK", {"Content-Type": "application/zip"}, b"PK\x03\x04partial")
        sys.stderr.write(
            'Get "https://pipelines.example.com/runzip/42?sig=SUPER_SECRET_SIG&se=2099": '
            "stream error: stream ID 5; INTERNAL_ERROR\n"
        )
        sys.exit(1)
    if run_mode == "zip_ok":
        emit_http("200 OK", {"Content-Type": "application/zip"}, make_run42_zip())
        sys.exit(0)
    if run_mode == "not_found":
        emit_http("404 Not Found", {"Content-Type": "application/json"}, '{"message":"Not Found"}')
        sys.stderr.write("gh: Not Found (HTTP 404)\n")
        sys.exit(1)

emit_http("404 Not Found", {"Content-Type": "application/json"}, json.dumps({"message": f"unhandled {ep_path}"}))
sys.stderr.write(f"gh: Not Found (HTTP 404) for {ep_path}\n")
sys.exit(1)
