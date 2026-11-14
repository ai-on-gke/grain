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

"""Scriptable fake Antigravity (agy) CLI for the §1 task-source-template walkthrough.

Reads $WALK_DIR/action-<task>.json (or action-default.json): a dict, or a list
consumed one entry per run. An "mcp_sequence" action drives the real grain
mcpserver the daemon hands each run and records every tool result under runs/.
"{{TASK_ID}}" in any argument is replaced by the run's task id.
"""

import datetime
import json
import os
import re
import subprocess
import sys
import time

for arg in sys.argv[1:]:
    if arg in ("--help", "models"):
        sys.exit(0)
    if arg == "--version":
        print("fake agy (task-source-template walkthrough)")
        sys.exit(0)

prompt = sys.stdin.read()
walk_dir = os.environ.get("WALK_DIR", "/tmp/walk-source-template")
os.makedirs(os.path.join(walk_dir, "runs"), exist_ok=True)

m = re.search(r"You are running as task ([A-Za-z0-9_-]+)", prompt)
home_base = os.path.basename(os.environ.get("HOME", "").rstrip("/"))
task_id = m.group(1) if m else (home_base if home_base.isdigit() else "unknown")
now_iso = datetime.datetime.now(datetime.timezone.utc).isoformat()
conv_id = f"conv-{task_id}-{int(time.time() * 1000)}"
is_nudge = "A reply without a tool call ends your run" in prompt

n = 1
while os.path.exists(os.path.join(walk_dir, "runs", f"prompt-{task_id}-{n}.txt")):
    n += 1
with open(os.path.join(walk_dir, "runs", f"prompt-{task_id}-{n}.txt"), "w", encoding="utf-8") as f:
    f.write(prompt)

action_path = os.path.join(walk_dir, f"action-{task_id}.json")
if not os.path.exists(action_path):
    action_path = os.path.join(walk_dir, "action-default.json")
action = {"kind": "plain_reply"} if is_nudge else {"kind": "nothing_needed", "reason": "walkthrough default"}
if os.path.exists(action_path) and not is_nudge:
    with open(action_path, encoding="utf-8") as f:
        loaded = json.load(f)
    if isinstance(loaded, list):
        if loaded:
            action = loaded.pop(0)
            with open(action_path, "w", encoding="utf-8") as f:
                json.dump(loaded, f)
    else:
        action = loaded


def substitute(obj):
    if isinstance(obj, str):
        return obj.replace("{{TASK_ID}}", str(task_id))
    if isinstance(obj, list):
        return [substitute(x) for x in obj]
    if isinstance(obj, dict):
        return {k: substitute(v) for k, v in obj.items()}
    return obj


action = substitute(action)


def emit(obj):
    print(json.dumps(obj), flush=True)


def open_mcp():
    with open(os.path.join(os.environ["HOME"], ".gemini", "config", "mcp_config.json"), encoding="utf-8") as f:
        cfg = json.load(f)["mcpServers"]["grain-sandbox"]
    env = os.environ.copy()
    env.update(cfg.get("env") or {})
    proc = subprocess.Popen([cfg["command"]] + cfg.get("args", []), stdin=subprocess.PIPE,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
        "protocolVersion": "2025-03-26", "capabilities": {},
        "clientInfo": {"name": "fake-agy", "version": "1.0"}}}) + "\n")
    proc.stdin.flush()
    while True:
        line = proc.stdout.readline()
        if not line or json.loads(line).get("id") == 1:
            break
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
    proc.stdin.flush()
    return proc


def call(proc, req_id, name, args):
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": req_id, "method": "tools/call",
                                 "params": {"name": name, "arguments": args}}) + "\n")
    proc.stdin.flush()
    while True:
        line = proc.stdout.readline()
        if not line:
            return {"error": "EOF from mcpserver"}
        resp = json.loads(line)
        if resp.get("id") == req_id:
            return resp


emit({"event": "init", "conversation_id": conv_id, "init": {"tools": [], "permission_mode": "always-proceed"}})
step = 0
kind = action.get("kind", "nothing_needed")
if kind in ("nothing_needed", "comment_on_issue", "mcp_sequence"):
    proc = open_mcp()
    calls = action.get("calls") or [
        {"tool": kind if kind != "mcp_sequence" else "nothing_needed",
         "args": {"comment": action.get("comment", "done")} if kind == "comment_on_issue"
         else {"reason": action.get("reason", "done")}}]
    outputs = []
    for req_id, c in enumerate(calls, start=2):
        resp = call(proc, req_id, c["tool"], c["args"])
        res = resp.get("result") or {}
        text = "\n".join(p.get("text", "") for p in res.get("content", [])) or json.dumps(resp.get("error", ""))
        outputs.append({"tool": c["tool"], "isError": res.get("isError", False), "text": text})
        name = f"mcp_grain-sandbox_{c['tool']}"
        base = {"conversation_id": conv_id, "step_index": step, "step_type": "tool", "tool_name": name}
        emit({"event": "step_update", "step_update": {**base, "state": "ACTIVE", "tool_info": {"name": name, "parameters": c["args"]}}})
        emit({"event": "step_update", "step_update": {**base, "state": "DONE", "tool_info": {"name": name, "parameters": c["args"], "output": text}}})
        step += 1
    with open(os.path.join(walk_dir, "runs", f"mcp-{task_id}.jsonl"), "a", encoding="utf-8") as f:
        f.write(json.dumps({"at": now_iso, "attempt": n, "outputs": outputs}) + "\n")
    proc.stdin.close()
    proc.terminate()

emit({"event": "step_update", "step_update": {"conversation_id": conv_id, "step_index": step,
      "step_type": "agent_response", "state": "ACTIVE", "text_delta": "walkthrough turn finished"}})
emit({"event": "step_update", "step_update": {"conversation_id": conv_id, "step_index": step,
      "step_type": "agent_response", "state": "DONE"}})
emit({"event": "result", "result": {"conversation_id": conv_id, "status": "SUCCESS",
      "response": "walkthrough turn finished", "num_turns": 1}})
