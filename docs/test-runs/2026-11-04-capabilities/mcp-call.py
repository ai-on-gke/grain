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

# call.py: one grain MCP session as the agent's CLI would hold it -- the
# server command from the run's mcp_config.json over stdio -- making two
# run_command calls and printing each result's text.
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
for i, cmd in enumerate(["echo first call after the drop", "echo second call"], start=2):
    send({"jsonrpc": "2.0", "id": i, "method": "tools/call", "params": {"name": "run_command", "arguments": {"command": cmd}}})
    r = recv(i)
    text = "\n".join(c.get("text", "") for c in (r or {}).get("result", {}).get("content", []))
    print(f"--- tools/call run_command {cmd!r} ->\n{text}\n")
p.stdin.close(); p.terminate()
