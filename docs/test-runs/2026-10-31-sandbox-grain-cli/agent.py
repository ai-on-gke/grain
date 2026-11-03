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

# Walkthrough agent: drives grain's MCP server over stdio and runs every
# line of $WALK_CMDS through run_command, saving each result.
import json, os, subprocess, sys, time
req = json.load(sys.stdin)
outdir = "/home/debian/wt/out/" + time.strftime("%H%M%S")
os.makedirs(outdir, exist_ok=True)
json.dump(req, open(os.path.join(outdir, "request.json"), "w"), indent=1)
m = req["mcp_server"]
env = dict(os.environ)
for kv in m.get("env") or []:
    k, _, v = kv.partition("="); env[k] = v
p = subprocess.Popen([m["command"]] + (m.get("args") or []), stdin=subprocess.PIPE, stdout=subprocess.PIPE, env=env, text=True)
nid = [0]
def call(method, params, notify=False):
    msg = {"jsonrpc": "2.0", "method": method, "params": params}
    if not notify:
        nid[0] += 1; msg["id"] = nid[0]
    p.stdin.write(json.dumps(msg) + "\n"); p.stdin.flush()
    if notify: return None
    while True:
        line = p.stdout.readline()
        if not line: raise SystemExit("mcp server closed")
        r = json.loads(line)
        if r.get("id") == nid[0]: return r
call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "walk", "version": "0"}})
call("notifications/initialized", {}, notify=True)
tools = call("tools/list", {})
json.dump(tools, open(os.path.join(outdir, "tools.json"), "w"), indent=1)
calls = []
if os.path.exists("/home/debian/wt/walk_full"):
    names = subprocess.run(["docker","ps","--filter","label=grain.sandbox=true","--format","{{.Names}}"],capture_output=True,text=True).stdout.split()
    for n in names:
        subprocess.run(["docker","cp","/home/debian/bin/grain",n+":/home/debian/grain-full"])
        subprocess.run(["docker","cp","/home/debian/wt/cmp.sh",n+":/home/debian/cmp.sh"])
    open(os.path.join(outdir,"containers.txt"),"w").write("\n".join(names))
cmds = [l.rstrip("\n") for l in open(os.environ.get("WALK_CMDS", "/home/debian/wt/cmds.txt")) if l.strip() and not l.startswith("#")]
log = open(os.path.join(outdir, "transcript.txt"), "w")
for i, c in enumerate(cmds):
    t0 = time.time()
    r = call("tools/call", {"name": "run_command", "arguments": {"command": c, "description": "walkthrough step %d" % i, "timeout": 600000}})
    res = r.get("result") or {}
    text = "".join(x.get("text", "") for x in res.get("content", []))
    if "error" in r: text = json.dumps(r["error"])
    log.write("=== [%d] $ %s   (%.1fs)\n%s\n" % (i, c, time.time() - t0, text)); log.flush()
    calls.append({"name": "run_command", "arguments": {"command": c}, "text": text[:2000], "is_error": bool(res.get("isError"))})
p.stdin.close(); p.wait(timeout=30)
print(json.dumps({"protocol_version": 2, "result": {"final_text": "walkthrough commands ran", "tool_calls": calls, "transcript": "see transcript.txt"}}))
