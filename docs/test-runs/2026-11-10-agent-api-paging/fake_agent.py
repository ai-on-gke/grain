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

"""docs/test-plans/2026-11-10.md §6 item 3: a fake agy that drives the sandbox MCP.

Installed as an antigravity connection's CLI path (dispatch_run.py). grain
starts it in a dispatched task's sandbox the way it starts agy: a HOME of
its own carrying .gemini/config/mcp_config.json, the prompt as one
stream-json event on stdin, and stream-json expected on stdout.

It reads the grain-controller server (the one a run holding api grants is
given) out of mcp_config.json, or grain-sandbox when there is none, starts
it the way agy would (its command and args over stdio, or its serverUrl),
lists its tools, calls grain_invoke for api_deployment_reachable_repos,
api_list_tasks and api_events with and without parameters, and writes each
result's size to --out. Then it reports the calls as agy's stream-json and
a successful result. A run whose prompt does not carry --marker (any other
task the daemon dispatches to this connection) does nothing and succeeds.
"""

import argparse
import json
import os
import subprocess
import sys
import urllib.request

CAP = 10240

CALLS = [
    ("api_deployment_reachable_repos", {}),
    ("api_deployment_reachable_repos", {"query": {"push": "true", "owner": "acme", "limit": "5"}}),
    ("api_deployment_reachable_repos", {"query": {"q": "SERVICE-01"}}),
    ("api_list_tasks", {}),
    ("api_list_tasks", {"query": {"fields": "summary"}}),
    ("api_list_tasks", {"query": {"limit": "3", "q": "triage"}}),
    ("api_events", {}),
    ("api_events", {"query": {"since": "0", "limit": "5"}}),
]


class Stdio:
    def __init__(self, command, args, env):
        self.proc = subprocess.Popen([command] + list(args), stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=sys.stderr, env=env, text=True, bufsize=1)

    def rpc(self, msg):
        self.proc.stdin.write(json.dumps(msg) + "\n")
        self.proc.stdin.flush()
        if "id" not in msg:
            return None
        while True:
            line = self.proc.stdout.readline()
            if not line:
                raise RuntimeError("the MCP server closed its output")
            try:
                m = json.loads(line)
            except ValueError:
                continue
            if m.get("id") == msg["id"]:
                return m

    def close(self):
        self.proc.stdin.close()
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()


class HTTP:
    def __init__(self, url, headers):
        self.url, self.headers, self.session = url, dict(headers or {}), None

    def rpc(self, msg):
        hdrs = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
        hdrs.update(self.headers)
        if self.session:
            hdrs["Mcp-Session-Id"] = self.session
        req = urllib.request.Request(self.url, data=json.dumps(msg).encode(), headers=hdrs, method="POST")
        with urllib.request.urlopen(req, timeout=120) as resp:
            self.session = resp.headers.get("Mcp-Session-Id") or self.session
            body = resp.read().decode()
        if "id" not in msg:
            return None
        if body.lstrip().startswith("{"):
            return json.loads(body)
        for line in body.splitlines():
            if line.startswith("data:"):
                m = json.loads(line[5:])
                if m.get("id") == msg["id"]:
                    return m
        return {}

    def close(self):
        pass


def result_text(msg):
    if "error" in msg:
        return json.dumps(msg["error"]), True
    res = msg.get("result") or {}
    parts = res.get("content") or []
    return "".join(p.get("text", "") for p in parts if isinstance(p, dict)), bool(res.get("isError"))


def summarize(text):
    try:
        j = json.loads(text)
    except ValueError:
        return {}
    if not isinstance(j, dict):
        return {"items": len(j) if isinstance(j, list) else None}
    out = {k: j.get(k) for k in ("total", "matched", "truncated") if k in j}
    out["hasNextCursor"] = bool(j.get("nextCursor"))
    for k in ("repos", "tasks", "events", "items", "runs"):
        if isinstance(j.get(k), list):
            out["items"] = len(j[k])
            break
    return out


def emit(event):
    print(json.dumps(event), flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--marker", default="paging-dispatch-probe")
    args, rest = ap.parse_known_args()
    if "--version" in rest:
        print("fake agy (docs/test-runs/2026-11-10-agent-api-paging)")
        return
    if "models" in rest:
        return
    prompt = sys.stdin.readline()
    conv = "fake-agy-paging"
    emit({"event": "init", "conversation_id": conv,
          "init": {"tools": [], "permission_mode": "always-proceed"}})
    steps = probe(args.out) if args.marker in prompt else []
    for i, (name, params, output) in enumerate(steps):
        tool = "mcp_grain-controller_" + name
        emit({"event": "step_update", "step_update": {"conversation_id": conv, "step_index": i, "step_type": "tool",
                                                      "state": "ACTIVE", "tool_name": tool,
                                                      "tool_info": {"name": tool, "parameters": params}}})
        emit({"event": "step_update", "step_update": {"conversation_id": conv, "step_index": i, "step_type": "tool",
                                                      "state": "DONE", "tool_name": tool,
                                                      "tool_info": {"name": tool, "output": output}}})
    response = f"Called {len(steps)} MCP tools." if steps else "Nothing to do."
    n = len(steps)
    emit({"event": "step_update", "step_update": {"conversation_id": conv, "step_index": n,
                                                  "step_type": "agent_response", "state": "ACTIVE",
                                                  "text_delta": response}})
    emit({"event": "step_update", "step_update": {"conversation_id": conv, "step_index": n,
                                                  "step_type": "agent_response", "state": "DONE"}})
    emit({"event": "result", "result": {"conversation_id": conv, "status": "SUCCESS", "response": response,
                                        "num_turns": 1}})


def probe(out_path):
    home = os.environ.get("HOME", "")
    record = {"cwd": os.getcwd()}
    steps = []
    try:
        cfg = json.load(open(os.path.join(home, ".gemini", "config", "mcp_config.json")))
        servers = cfg["mcpServers"]
        record["servers"] = {name: sorted(k for k in sv if k in ("command", "serverUrl"))
                             for name, sv in servers.items()}
        # api grants are served by their own grain-controller server
        # (grain_explore_scope, grain_invoke); grain-sandbox is the
        # fallback for a run that holds none.
        name = "grain-controller" if "grain-controller" in servers else "grain-sandbox"
        server = servers[name]
        record["server"] = name
        if server.get("serverUrl"):
            conn = HTTP(server["serverUrl"], server.get("headers"))
        else:
            conn = Stdio(server["command"], server.get("args", []), dict(os.environ))
        conn.rpc({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                  "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                             "clientInfo": {"name": "fake-agy-paging", "version": "1"}}})
        conn.rpc({"jsonrpc": "2.0", "method": "notifications/initialized"})
        listed = conn.rpc({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
        record["tools"] = sorted(t["name"] for t in (listed.get("result") or {}).get("tools", []))
        rows = []
        for i, (tool, a) in enumerate(CALLS):
            params = {"tool": tool, "arguments": a, "reason": "paging walkthrough (dispatched fake agent)"}
            msg = conn.rpc({"jsonrpc": "2.0", "id": 10 + i, "method": "tools/call",
                            "params": {"name": "grain_invoke", "arguments": params}})
            text, is_err = result_text(msg)
            size = len(text.encode())
            rows.append({"tool": tool, "args": a, "bytes": size, "underCap": size <= CAP, "isError": is_err,
                         "error": text[:300] if is_err else None, **summarize(text)})
            steps.append(("grain_invoke", params, f"{size} B"))
        conn.close()
        record["calls"] = rows
    except Exception as exc:  # recorded, so the report says what broke
        record["error"] = repr(exc)
    with open(out_path, "w") as f:
        json.dump(record, f, indent=2)
    return steps


if __name__ == "__main__":
    main()
