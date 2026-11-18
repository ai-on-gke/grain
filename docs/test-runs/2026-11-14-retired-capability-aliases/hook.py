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

# hook.py: run inside fake-agy.sh. Talks stdio MCP to the run's own
# grain-sandbox server and records what a run holding (or having lost) a
# retired grant actually gets: its tool list, `grain api routes`, `grain ls
# src://`, `grain ci log`, a git push (the simulator logs its credential),
# and propose_task / request_access naming bare names. Writes argv[3].
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
out = {"tools": sorted(t["name"] for t in (rpc("tools/list") or {}).get("result", {}).get("tools", []))}


def tool(name, args):
    r = rpc("tools/call", {"name": name, "arguments": args}) or {}
    res = r.get("result") or r.get("error") or {}
    text = "".join(c.get("text", "") for c in res.get("content", [])) if isinstance(res, dict) else str(res)
    return {"isError": res.get("isError", "error" in r), "text": text[:2500]}


for label, cmd in (
    ("api_routes", "grain api routes 2>&1 | head -40"),
    ("ls_src", "grain ls src://pkg 2>&1 | head -5"),
    ("ci_log", "grain ci log 2>&1 | tail -5"),
    ("git_push", "d=$(git rev-parse --show-toplevel 2>/dev/null || echo work); cd $d && "
                 "git -c user.name=w -c user.email=w@example.com commit -q --allow-empty -m walk && "
                 "git push origin HEAD 2>&1 | tail -3"),
):
    out[label] = tool("run_command", {"command": cmd + "; echo exit=$?", "toolSummary": label})
out["propose_bare"] = tool("propose_task", {
    "title": "walk: bare names", "body": "walkthrough", "capabilities": ["self-debug", "grain-ci"]})
out["request_access_bare"] = tool("request_access", {
    "summary": "Grant self-debug", "reason": "walkthrough",
    "requests": [{"kind": "grant", "capability": "self-debug"}]})
json.dump(out, open(out_path, "w"), indent=2)
proc.stdin.close()
