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

"""Calls `grain mcpserver` tools over stdio: mcp_call.py <grain mcpserver argv...>, reading [[tool, args], ...] from calls.json ("__list" lists the tools)."""
import json, subprocess, sys
args = sys.argv[1:]
calls = json.loads(open("calls.json").read())
p = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open("mcp.err","a"), text=True)
def send(m): p.stdin.write(json.dumps(m)+"\n"); p.stdin.flush()
def recv():
    while True:
        line = p.stdout.readline()
        if not line: return None
        m = json.loads(line)
        if "id" in m: return m
send({"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"walk","version":"0"}}})
recv(); send({"jsonrpc":"2.0","method":"notifications/initialized"})
for i,(name,a) in enumerate(calls,1):
    if name == "__list":
        send({"jsonrpc":"2.0","id":i,"method":"tools/list"}); r = recv()
        print("TOOLS:", " ".join(t["name"] for t in r["result"]["tools"]))
        continue
    send({"jsonrpc":"2.0","id":i,"method":"tools/call","params":{"name":name,"arguments":a}}); r = recv()
    print("==", name, json.dumps(a)); res = r.get("result") or r
    print("\n".join(c.get("text","") for c in res.get("content",[])) if "content" in res else json.dumps(res)[:3000])
p.stdin.close(); p.wait(timeout=10)
