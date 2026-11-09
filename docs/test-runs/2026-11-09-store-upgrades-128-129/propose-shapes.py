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

# propose-shapes.py: one grain MCP session as the agent's CLI holds it (the
# server command from the run's mcp_config.json, over stdio), sending
# propose_task each refused mixed-grant shape of §2 item 3 and a control,
# and printing each answer. The calls are also written as agy tool steps to
# /home/debian/walk/stream-tools.jsonl, which fake-agy prints into the
# run's stream, so the end-of-run relay sees exactly what the agent sent.
import json, subprocess

cfg = json.load(open("/home/debian/walk/mcp-live.json"))["mcpServers"]["grain-sandbox"]
p = subprocess.Popen([cfg["command"]] + cfg["args"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)


def send(msg):
    p.stdin.write(json.dumps(msg) + "\n")
    p.stdin.flush()


def recv(want_id):
    while True:
        line = p.stdout.readline()
        if not line:
            return None
        m = json.loads(line)
        if m.get("id") == want_id:
            return m


TARGET = {"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/widgets", "base": "main"}}
READ = {"capability": "https://grain.dev/capabilities/repo#read", "with": {"repo": "acme/widgets"}}
SHAPES = [
    ("capabilities + grants: []", {"capabilities": ["api:ci-read"], "grants": []}),
    ("reads: [] + grants:", {"reads": [], "grants": [READ]}),
    # propose_task names the base "branch"; "base" is not one of its
    # arguments (sent anyway, to record what an unknown argument does).
    ("base + repo#target", {"base": "main", "grants": [TARGET]}),
    ("branch + repo#target", {"branch": "main", "grants": [TARGET]}),
    ("repo + repo#target", {"repo": "acme/widgets", "grants": [TARGET]}),
    ("no_repo + repo#target", {"no_repo": True, "grants": [TARGET]}),
    ("control: grants only", {"grants": ["api:ci-read"]}),
]

send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
    "protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "fake-agy", "version": "0"}}})
recv(1)
send({"jsonrpc": "2.0", "method": "notifications/initialized"})
steps = []
for i, (label, extra) in enumerate(SHAPES, start=2):
    args = {"title": "walk: " + label, "body": "§2 item 3 shape: " + label, **extra}
    send({"jsonrpc": "2.0", "id": i, "method": "tools/call", "params": {"name": "propose_task", "arguments": args}})
    r = recv(i) or {}
    text = "\n".join(c.get("text", "") for c in r.get("result", {}).get("content", [])) or json.dumps(r.get("error"))
    name = "mcp_grain-sandbox_propose_task"
    base = {"conversation_id": "fake-agy-walk", "step_index": i, "step_type": "tool", "tool_name": name}
    steps.append({**base, "state": "ACTIVE", "tool_info": {"name": name, "parameters": args}})
    steps.append({**base, "state": "DONE", "tool_info": {"name": name, "parameters": args, "output": text}})
    print(f"--- propose_task [{label}] {json.dumps(extra)}\n    -> isError={r.get('result', {}).get('isError')} {text}")
with open("/home/debian/walk/stream-tools.jsonl", "w") as f:
    for s in steps:
        f.write(json.dumps({"event": "step_update", "step_update": s}) + "\n")
p.stdin.close()
p.terminate()
