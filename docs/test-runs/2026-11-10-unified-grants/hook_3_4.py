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

# hook.py: Executed inside fake-agy.sh during a real `grain daemon -host-sandboxes`
# dispatch. Connects over stdio to the run's own `grain-sandbox` MCP server from
# $HOME/.gemini/config/mcp_config.json and exercises §3 items 3 and 4.
import json
import os
import subprocess
import sys

mcp_cfg_path, task_id, stream_file = sys.argv[1:4]
walk_dir = os.path.dirname(os.path.dirname(stream_file))

cfg = json.load(open(mcp_cfg_path))["mcpServers"]["grain-sandbox"]
cmd = [cfg["command"]] + cfg.get("args", [])
env = os.environ.copy()
env.update(cfg.get("env", {}))
is_interactive = "-interactive" in cfg.get("args", [])

proc = subprocess.Popen(
    cmd,
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
    env=env,
)


def send(msg):
    proc.stdin.write(json.dumps(msg) + "\n")
    proc.stdin.flush()


def recv(want_id):
    while True:
        line = proc.stdout.readline()
        if not line:
            return None
        m = json.loads(line)
        if m.get("id") == want_id:
            return m


send({
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": "fake-agy-walk", "version": "1"},
    },
})
recv(1)
send({"jsonrpc": "2.0", "method": "notifications/initialized"})

mode_tag = "live" if is_interactive else "relay"
req_id = 1
steps = []
records = []


def call_tool(tool_name, arguments, emit_step=True):
    global req_id
    req_id += 1
    send({
        "jsonrpc": "2.0",
        "id": req_id,
        "method": "tools/call",
        "params": {"name": tool_name, "arguments": arguments},
    })
    resp = recv(req_id) or {}
    res = resp.get("result", {})
    is_err = bool(res.get("isError"))
    text = "\n".join(c.get("text", "") for c in res.get("content", [])) or json.dumps(resp.get("error"))
    if emit_step:
        full_name = f"mcp_grain-sandbox_{tool_name}"
        base = {
            "conversation_id": "fake-agy-walk",
            "step_index": req_id,
            "step_type": "tool",
            "tool_name": full_name,
        }
        steps.append({**base, "state": "ACTIVE", "tool_info": {"name": full_name, "parameters": arguments}})
        steps.append({**base, "state": "DONE", "tool_info": {"name": full_name, "parameters": arguments, "output": text}})
    return {"isError": is_err, "text": text}


plan_file = os.path.join(walk_dir, "plan.json")
plan = json.load(open(plan_file)) if os.path.exists(plan_file) else {}
role = plan.get(str(task_id), "noop")

if role == "item3":
    # Item 3 children: self-debug, api#self-upgrade, api#secrets:write, api#tasks:read
    ITEM3_CHILDREN = [
        ("self-debug", ["self-debug"]),
        ("api#self-upgrade", ["https://grain.dev/capabilities/api#self-upgrade"]),
        ("api#secrets:write", ["https://grain.dev/capabilities/api#secrets:write"]),
        ("api#tasks:read", ["https://grain.dev/capabilities/api#tasks:read"]),
    ]
    for label, caps in ITEM3_CHILDREN:
        args = {
            "title": f"child [{mode_tag}] task-{task_id}: {label}",
            "body": f"§3 item 3 child asking for {label}",
            "capabilities": caps,
        }
        out = call_tool("propose_task", args, emit_step=True)
        records.append({"group": "item3", "label": label, "args": args, **out})
elif role == "item4":
    ITEM4_BAD = [
        ("base", {"base": "main"}),
        ("description", {"description": "wrong body field"}),
        ("fields", {"fields": {"env": "staging"}}),
        ("approve", {"approve": True}),
        ("capabilites", {"capabilites": ["https://grain.dev/capabilities/api#ci-read"]}),
        ("writes[].branch", {"writes": [{"repo": "acme/lib", "branch": "main"}]}),
        ("dependsOn", {"dependsOn": ["1"]}),
    ]
    for label, extra in ITEM4_BAD:
        args = {
            "title": f"bad-arg [{mode_tag}]: {label}",
            "body": f"§3 item 4 undeclared argument test: {label}",
            **extra,
        }
        out = call_tool("propose_task", args, emit_step=True)
        records.append({"group": "item4", "label": label, "args": args, **out})

# End the run cleanly with nothing_needed so an interactive console task doesn't
# park or retry if needed, or let's call comment_on_issue so it finishes cleanly.
call_tool("comment_on_issue", {"comment": f"Completed {role} ({mode_tag}) walkthrough."}, emit_step=True)

with open(stream_file, "w") as f:
    for s in steps:
        f.write(json.dumps({"event": "step_update", "step_update": s}) + "\n")

with open(os.path.join(walk_dir, "runs", f"mcp-results-task-{task_id}.json"), "w") as f:
    json.dump({"taskId": task_id, "interactive": is_interactive, "role": role, "records": records}, f, indent=2)

proc.stdin.close()
proc.terminate()
