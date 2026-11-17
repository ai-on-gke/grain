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

# hook.py MCP_CONFIG WALK_DIR: run by fake-agy.sh inside a real run.
#
# $WALK_DIR/plan-<task>.json (else plan.json) says what to do: {"tools": [[name, args], ...],
# "pause_after": N}. It calls each tool on the run's own grain-sandbox MCP
# server; with pause_after it waits after the Nth call until
# $WALK_DIR/continue exists, so the daemon can be stopped mid-run. Every
# result goes to $WALK_DIR/runs/<task>.json.
import json
import os
import subprocess
import sys
import time

mcp_cfg_path, walk_dir = sys.argv[1:3]
cfg = json.load(open(mcp_cfg_path))["mcpServers"]["grain-sandbox"]
args = cfg.get("args", [])
task = args[args.index("-task") + 1] if "-task" in args else "unknown"
plan_path = os.path.join(walk_dir, "plan-" + task + ".json")
plan = json.load(open(plan_path if os.path.exists(plan_path) else os.path.join(walk_dir, "plan.json")))
# Flag names only (and -grant values), never a value that may be a credential.
out = {"task": task, "command": cfg["command"], "calls": [], "flags": [a for a in args if a.startswith("-")],
       "grants": [args[k + 1] for k, a in enumerate(args[:-1]) if a == "-grant"]}

env = os.environ.copy()
env.update(cfg.get("env", {}))
proc = subprocess.Popen([cfg["command"]] + args, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                        stderr=open(os.path.join(walk_dir, "runs", task + ".mcpserver.stderr"), "w"), text=True, env=env)


def send(m):
    proc.stdin.write(json.dumps(m) + "\n")
    proc.stdin.flush()


def recv(i):
    while True:
        line = proc.stdout.readline()
        if not line:
            return {}
        m = json.loads(line)
        if m.get("id") == i:
            return m


send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
    "protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "walk", "version": "1"}}})
recv(1)
send({"jsonrpc": "2.0", "method": "notifications/initialized"})
send({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
out["tools"] = sorted(t["name"] for t in recv(2).get("result", {}).get("tools", []))

def redact_probe():
    """POST /api/secrets/redact with each credential this run holds, as
    the run itself could; records only status codes, never a token."""
    import urllib.request
    servers = [args[k + 1] for k, a in enumerate(args[:-1]) if a in ("-server", "--server")]
    servers += [v for k, v in env.items() if k in ("GRAIN_SERVER",)]
    tokens = {}
    for k, v in env.items():
        if "TOKEN" not in k or not v or k == "GRAIN_KMS_TOKEN":
            continue
        if k.endswith("_FILE"):
            # Whether the agent's own process can open the file the MCP
            # server is pointed at, and if so, what that token may do.
            try:
                tokens[k + " (read by the agent)"] = open(v).read().strip()
                out.setdefault("token_files", {})[k] = "readable by the agent"
            except OSError as e:
                out.setdefault("token_files", {})[k] = "not readable by the agent: " + type(e).__name__
            continue
        tokens[k] = v
    for k, a in enumerate(args[:-1]):
        if "token" in a.lower() and not args[k + 1].startswith("-"):
            tok = args[k + 1]
            if os.path.isfile(tok):
                tok = open(tok).read().strip()
            tokens["arg " + a] = tok
    results = []
    for srv in sorted(set(servers)):
        for name, tok in sorted(tokens.items()):
            req = urllib.request.Request(srv.rstrip("/") + "/api/secrets/redact", method="POST",
                                         data=b'{"lines":["probe"]}',
                                         headers={"Authorization": "Bearer " + tok, "Content-Type": "application/json"})
            try:
                code = urllib.request.urlopen(req, timeout=10).status
            except urllib.error.HTTPError as e:
                code = e.code
            except Exception as e:
                code = type(e).__name__
            results.append({"server": srv, "credential": name, "status": code})
    return results


for n, (name, arguments) in enumerate(plan.get("tools", []), start=1):
    i = 10 + n
    if name == "_redact_probe":
        out["calls"].append({"tool": name, "results": redact_probe()})
        continue
    send({"jsonrpc": "2.0", "id": i, "method": "tools/call", "params": {"name": name, "arguments": arguments}})
    raw = recv(i)
    res = raw.get("result", {})
    if not res.get("content"):
        out.setdefault("raw", []).append(json.dumps(raw)[:1500])
    out["calls"].append({"tool": name, "args": arguments, "isError": bool(res.get("isError")),
                         "text": "\n".join(c.get("text", "") for c in res.get("content", []))[:4000]})
    if plan.get("pause_after") == n:
        open(os.path.join(walk_dir, "paused"), "w").close()
        for _ in range(600):
            if os.path.exists(os.path.join(walk_dir, "continue")):
                break
            time.sleep(0.5)

proc.stdin.close()
os.makedirs(os.path.join(walk_dir, "runs"), exist_ok=True)
json.dump(out, open(os.path.join(walk_dir, "runs", task + ".json"), "w"), indent=1)
