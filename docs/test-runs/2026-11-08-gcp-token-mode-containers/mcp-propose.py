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

# mcp-propose.py: one grain MCP session as the agent's CLI would hold it
# (2026-11-04-capabilities/mcp-call.py's way: the server command from the
# run's mcp_config.json over stdio), calling propose_task with the run's
# own person-granted gcp grant, and printing the result's text. It also
# writes agy's tool steps for the call to /home/debian/walk/stream-tools.jsonl,
# which fake-agy.sh prints into the run's stream.
import json, subprocess, sys
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
steps = []
# The set as the run would type it, and as its own task holds it (the
# server's primary default filled in).
grant = 'https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"org-deploy"}'
held = 'https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"org-deploy","primary":true}'
for i, args in enumerate([
    {"title": "Deploy widgets with org-deploy", "body": "Follow-up that needs the parent's gcp set.", "capabilities": [grant]},
    {"title": "Deploy widgets with org-deploy, as held", "body": "The same, spelled as the parent holds it.", "capabilities": [held]},
    {"title": "No capabilities", "body": "A control: asks for nothing."},
], start=2):
    send({"jsonrpc": "2.0", "id": i, "method": "tools/call", "params": {"name": "propose_task", "arguments": args}})
    r = recv(i)
    text = "\n".join(c.get("text", "") for c in (r or {}).get("result", {}).get("content", []))
    name = "mcp_grain-sandbox_propose_task"
    base = {"conversation_id": "fake-agy-walk", "step_index": i, "step_type": "tool", "tool_name": name}
    steps.append({**base, "state": "ACTIVE", "tool_info": {"name": name, "parameters": args}})
    steps.append({**base, "state": "DONE", "tool_info": {"name": name, "parameters": args, "output": text}})
    print(f"--- tools/call propose_task {json.dumps(args)} ->\n{text}\nisError={(r or {}).get('result', {}).get('isError')}\n")
with open("/home/debian/walk/stream-tools.jsonl", "w") as f:
    for s in steps:
        f.write(json.dumps({"event": "step_update", "step_update": s}) + "\n")
p.stdin.close(); p.terminate()
