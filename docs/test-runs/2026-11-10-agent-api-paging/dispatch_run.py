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

"""docs/test-plans/2026-11-10.md §6 item 3: one dispatched task on a fake agent.

On a seeded daemon (env.sh), after the walk: adds an antigravity connection
whose CLI is fake_agent.py, files and approves a task on acme/widgets that
holds api:deployment:read, api:tasks:read and api:repos:read, waits for its
attempt to finish, and writes what the fake agent recorded inside the
sandbox (the MCP servers it was given, the tools of the one it used, and
each grain_invoke result's size) together with the attempt's outcome.
"""

import argparse
import json
import os
import stat
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
MARKER = "paging-dispatch-probe"


def call(url, code, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url + path, data=data, method=method,
                                 headers={"Authorization": "Bearer " + code, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            raw = r.read()
            return json.loads(raw) if raw.strip() else None
    except urllib.error.HTTPError as e:
        raise SystemExit(f"{method} {path}: HTTP {e.code}: {e.read()[:400]!r}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--code-file", required=True)
    ap.add_argument("--work", required=True, help="the env's work dir (start_paging_env's second argument)")
    ap.add_argument("--out", required=True)
    ap.add_argument("--timeout", type=int, default=600)
    args = ap.parse_args()
    code = open(args.code_file).read().strip()

    record = os.path.join(args.work, "dispatched-mcp.json")
    if os.path.exists(record):
        os.remove(record)
    cli = os.path.join(args.work, "bin", "fake-agent-paging")
    with open(cli, "w") as f:
        f.write(f"#!/bin/sh\nexec python3 {os.path.join(HERE, 'fake_agent.py')} --out {record} "
                f"--marker {MARKER} \"$@\"\n")
    os.chmod(cli, os.stat(cli).st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    call(args.url, code, "PUT", "/api/settings", {"agentBackends": [{
        "id": "fake-paging", "name": "fake agent (paging)", "framework": "antigravity",
        "authMode": "ambient", "cliPath": cli, "models": ["fake-model"]}]})

    task = call(args.url, code, "POST", "/api/tasks", {
        "title": "Dispatched paging probe",
        "description": f"{MARKER}: call grain_invoke for the paged list tools and record their sizes.",
        "repo": "acme/widgets", "agentFramework": "antigravity", "agentBackendId": "fake-paging",
        "priority": 100,
        "capabilities": ["api:deployment:read", "api:tasks:read", "api:repos:read"]})
    tid = task.get("id") or task.get("task", {}).get("id")
    call(args.url, code, "POST", f"/api/tasks/{tid}/approve", {})
    # env.sh polls every hour and approving does not wake the reconciler;
    # a capability-server action does (Server.wakeReconciler). cap-000 is
    # seed_api.py's and already disabled, so this changes nothing else.
    call(args.url, code, "POST", "/api/capability-servers/disable",
         {"id": "https://tools.example.com/capabilities/cap-000"})

    deadline = time.time() + args.timeout
    detail = {}
    while time.time() < deadline:
        detail = call(args.url, code, "GET", f"/api/tasks/{tid}")
        attempts = detail.get("attempts") or []
        if attempts and attempts[-1].get("finishedAt"):
            break
        time.sleep(3)
    attempts = detail.get("attempts") or []
    last = attempts[-1] if attempts else {}
    out = {"task": tid, "state": detail.get("state"), "attempts": len(attempts),
           "outcome": last.get("outcome"), "detail": (last.get("detail") or "")[:500]}
    if os.path.exists(record):
        out["inSandbox"] = json.load(open(record))
    with open(args.out, "w") as f:
        json.dump(out, f, indent=2)
    print(json.dumps({k: v for k, v in out.items() if k != "inSandbox"}))
    for row in (out.get("inSandbox") or {}).get("calls", []):
        print(f"  {row['tool']} {json.dumps(row['args'])}: {row['bytes']} B error={row['isError']}")


if __name__ == "__main__":
    main()
