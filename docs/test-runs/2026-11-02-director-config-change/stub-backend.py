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

# Stub custom agent backend (docs/custom-agent-backends.md, protocol 2) for
# the §2 walkthrough: it starts the run's own grain mcpserver over stdio and
# makes the tool calls scripted in /var/tmp/stub/script.json (consumed by the
# turn that reads it), logging the prompt and every result to log.jsonl.
import json, os, subprocess, sys, time

DIR = "/var/tmp/stub"
req = json.load(sys.stdin)
env = dict(os.environ)
for kv in req["mcp_server"].get("env") or []:
    k, _, v = kv.partition("=")
    env[k] = v
srv = subprocess.Popen([req["mcp_server"]["command"]] + req["mcp_server"].get("args", []),
                       stdin=subprocess.PIPE, stdout=subprocess.PIPE, env=env, text=True)
nid = [0]

def rpc(method, params, notify=False):
    msg = {"jsonrpc": "2.0", "method": method, "params": params}
    if not notify:
        nid[0] += 1
        msg["id"] = nid[0]
    srv.stdin.write(json.dumps(msg) + "\n"); srv.stdin.flush()
    if notify:
        return None
    while True:
        line = srv.stdout.readline()
        if not line:
            raise RuntimeError("mcpserver closed")
        m = json.loads(line)
        if m.get("id") == nid[0]:
            return m

rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                   "clientInfo": {"name": "stub", "version": "1"}})
rpc("notifications/initialized", {}, notify=True)
tools = [t["name"] for t in rpc("tools/list", {})["result"]["tools"]]

steps = []
script = os.path.join(DIR, "script.json")
if os.path.exists(script):
    steps = json.load(open(script))
    os.rename(script, script + ".done.%d" % time.time())
calls, transcript = [], []
for s in steps:
    r = rpc("tools/call", {"name": s["tool"], "arguments": s["args"]})
    res = r.get("result") or {}
    text = "".join(c.get("text", "") for c in res.get("content", []))
    err = bool(res.get("isError")) or "error" in r
    if "error" in r:
        text = json.dumps(r["error"])
    calls.append({"name": s["tool"], "arguments": s["args"], "text": text, "is_error": err})
    transcript.append("%s -> %s" % (s["tool"], text))
with open(os.path.join(DIR, "log.jsonl"), "a") as f:
    f.write(json.dumps({"at": time.time(), "prompt": req["prompt"], "tools": tools, "calls": calls}) + "\n")
srv.stdin.close()
try:
    srv.wait(5)
except Exception:
    srv.kill()
final = "stub turn done" if not calls else calls[-1]["text"][:200]
json.dump({"protocol_version": 2, "result": {"final_text": final, "tool_calls": calls,
                                              "transcript": "\n".join(transcript)}}, sys.stdout)
