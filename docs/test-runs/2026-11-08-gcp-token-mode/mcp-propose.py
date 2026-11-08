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

# mcp-propose.py: 2026-11-04-capabilities/mcp-call.py's stdio client for
# the run's grain MCP server, making step-propose.sh's two propose_task
# calls, printing what each returned, and appending agy's step_update
# events for each call to stream.jsonl for fake-agy to report (a real agy
# reports the calls it makes; grain files proposals from that report).
import json, subprocess
cfg = json.load(open("/home/debian/walk/mcp-live.json"))["mcpServers"]["grain-sandbox"]
p = subprocess.Popen([cfg["command"]] + cfg["args"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
def send(msg):
    p.stdin.write(json.dumps(msg) + "\n"); p.stdin.flush()
def recv(want_id):
    while True:
        line = p.stdout.readline()
        if not line:
            return None
        m = json.loads(line)
        if m.get("id") == want_id:
            return m
send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "fake-agy", "version": "0"}}})
recv(1)
send({"jsonrpc": "2.0", "method": "notifications/initialized"})
# The set to pass on: ext-deploy (person-granted), or what
# /home/debian/walk/propose-set names.
try:
    set_name = open("/home/debian/walk/propose-set").read().strip() or "ext-deploy"
except OSError:
    set_name = "ext-deploy"
grant = 'https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"%s"}' % set_name
calls = [
    {"title": "walk: pass gcp set %s on" % set_name, "body": "walk item 4: asks for the parent's gcp set " + set_name, "repo": "acme/widgets", "capabilities": [grant]},
    {"title": "walk: control, no grant", "body": "walk item 4: the same proposal with no grant", "repo": "acme/widgets"},
]
stream = open("/home/debian/walk/stream.jsonl", "a")
def step(index, state, info):
    stream.write(json.dumps({"event": "step_update", "step_update": {"conversation_id": "fake-agy-walk", "step_index": index,
        "state": state, "step_type": "tool", "tool_name": info["name"], "tool_info": info}}) + "\n")
for i, args in enumerate(calls, start=2):
    send({"jsonrpc": "2.0", "id": i, "method": "tools/call", "params": {"name": "propose_task", "arguments": args}})
    r = recv(i)
    text = "\n".join(c.get("text", "") for c in (r or {}).get("result", {}).get("content", []))
    name = "mcp_grain-sandbox_propose_task"
    stream.write(json.dumps({"event": "step_update", "step_update": {"conversation_id": "fake-agy-walk", "step_index": 2 * i,
        "state": "DONE", "step_type": "agent_response"}}) + "\n")
    step(2 * i + 1, "ACTIVE", {"name": name, "parameters": args})
    step(2 * i + 1, "DONE", {"name": name, "parameters": args, "output": text})
    print(f"--- propose_task {json.dumps(args.get('capabilities', []))} ->\n{text}\n")
stream.close(); p.stdin.close(); p.terminate()
