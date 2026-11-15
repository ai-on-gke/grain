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

# hook_ci.py: run inside fake-agy.sh. Talks stdio MCP to the run's own
# grain-sandbox server: lists its tools, runs `grain ci list`, `grain pr
# status` for the task's own repository and for another one, and asks
# request_access for grain#ci. Writes every answer to argv[3] as JSON.
import json
import os
import subprocess
import sys

cfg_path, task_id, out_path = sys.argv[1:4]
cfg = json.load(open(cfg_path))["mcpServers"]["grain-sandbox"]
env = os.environ.copy()
env.update(cfg.get("env", {}))
proc = subprocess.Popen([cfg["command"]] + cfg.get("args", []), stdin=subprocess.PIPE,
                        stdout=subprocess.PIPE, text=True, env=env)
rid = 0


def rpc(method, params=None):
    global rid
    rid += 1
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": rid, "method": method, "params": params or {}}) + "\n")
    proc.stdin.flush()
    while True:
        line = proc.stdout.readline()
        if not line:
            return None
        m = json.loads(line)
        if m.get("id") == rid:
            return m


rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "walk", "version": "1"}})
proc.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
proc.stdin.flush()
out = {"tools": [t["name"] for t in (rpc("tools/list") or {}).get("result", {}).get("tools", [])]}


def tool(name, args):
    r = rpc("tools/call", {"name": name, "arguments": args}) or {}
    res = r.get("result") or r.get("error") or {}
    text = "".join(c.get("text", "") for c in res.get("content", [])) if isinstance(res, dict) else str(res)
    return {"isError": res.get("isError", "error" in r), "text": text[:3000]}


for label, cmd in (
    ("ci_list_own", "grain ci list --limit 3"),
    ("pr_status_own", "grain pr status"),
    ("ci_list_other", "grain ci list --repo acme/lib --limit 3"),
    ("pr_status_other", "grain pr status --ref main --repo acme/lib"),
):
    out[label] = tool("run_command", {"command": cmd + " 2>&1; echo exit=$?", "toolSummary": label})
out["request_access_grain_ci"] = tool("request_access", {
    "summary": "Grant grain#ci so I can read CI", "reason": "walkthrough", "toolSummary": "ask",
    "requests": [{"kind": "grant", "capability": "https://grain.dev/capabilities/grain#ci"}]})
json.dump(out, open(out_path, "w"), indent=2)
proc.stdin.close()
