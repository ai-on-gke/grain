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

"""Scriptable fake Antigravity (agy) CLI for §9 dispatch and wake walkthrough."""

import datetime
import json
import os
import re
import subprocess
import sys
import time

for arg in sys.argv[1:]:
    if arg == "--help":
        sys.exit(0)
    if arg == "--version":
        print("fake agy (dispatch-wakes walkthrough)")
        sys.exit(0)
    if arg == "models":
        sys.exit(0)

prompt = sys.stdin.read()
walk_dir = os.environ.get("WALK_DIR", "/tmp/walk-dispatch-wakes")
os.makedirs(os.path.join(walk_dir, "runs"), exist_ok=True)

m = re.search(r"You are running as task ([A-Za-z0-9_-]+)", prompt)
home_base = os.path.basename(os.environ.get("HOME", "").rstrip("/"))
task_id = (
    m.group(1)
    if m
    else (home_base if home_base.isdigit() else os.environ.get("GRAIN_TASK_ID", "unknown"))
)
now_iso = datetime.datetime.now(datetime.timezone.utc).isoformat()

run_record = {
    "task_id": task_id,
    "started_seen_at": now_iso,
    "home": os.environ.get("HOME", ""),
    "is_stoprule_nudge": "A reply without a tool call ends your run" in prompt,
    "prompt_head": prompt[:600],
}
with open(os.path.join(walk_dir, "runs", f"task-{task_id}.jsonl"), "a", encoding="utf-8") as rf:
    rf.write(json.dumps(run_record) + "\n")

action_path = os.path.join(walk_dir, f"action-{task_id}.json")
if not os.path.exists(action_path):
    action_path = os.path.join(walk_dir, "action-default.json")

action = (
    {"kind": "plain_reply", "response": "already stopped"}
    if run_record["is_stoprule_nudge"]
    else {"kind": "nothing_needed", "reason": "walkthrough default completion"}
)
if os.path.exists(action_path) and not run_record["is_stoprule_nudge"]:
    with open(action_path, "r", encoding="utf-8") as af:
        loaded = json.load(af)
    if isinstance(loaded, list):
        if loaded:
            action = loaded.pop(0)
            with open(action_path, "w", encoding="utf-8") as af:
                json.dump(loaded, af)
    elif isinstance(loaded, dict):
        action = loaded


def open_mcp():
    mcp_cfg_path = os.path.join(os.environ["HOME"], ".gemini", "config", "mcp_config.json")
    with open(mcp_cfg_path, "r", encoding="utf-8") as f:
        cfg = json.load(f)["mcpServers"]["grain-sandbox"]
    env = os.environ.copy()
    env.update(cfg.get("env") or {})
    proc = subprocess.Popen(
        [cfg["command"]] + cfg.get("args", []),
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
    )
    proc.stdin.write(json.dumps({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "initialize",
        "params": {
            "protocolVersion": "2025-03-26",
            "capabilities": {},
            "clientInfo": {"name": "fake-agy", "version": "1.0"},
        },
    }) + "\n")
    proc.stdin.flush()
    while True:
        line = proc.stdout.readline()
        if not line:
            break
        resp = json.loads(line)
        if resp.get("id") == 1:
            break
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
    proc.stdin.flush()
    return proc


def mcp_call(proc, req_id, tool_name, arguments):
    proc.stdin.write(json.dumps({
        "jsonrpc": "2.0",
        "id": req_id,
        "method": "tools/call",
        "params": {"name": tool_name, "arguments": arguments},
    }) + "\n")
    proc.stdin.flush()
    while True:
        line = proc.stdout.readline()
        if not line:
            return {"error": "EOF from mcpserver"}
        resp = json.loads(line)
        if resp.get("id") == req_id:
            return resp


def emit_event(obj):
    print(json.dumps(obj), flush=True)


kind = action.get("kind", "nothing_needed")

if kind == "usage_limit":
    msg = action.get(
        "message",
        "RESOURCE_EXHAUSTED: You have exhausted your capacity on this model. Your quota will reset after 30m0s.",
    )
    sys.stderr.write(msg + "\n")
    sys.stderr.flush()
    emit_event({
        "event": "init",
        "conversation_id": f"conv-{task_id}",
        "init": {"tools": [], "permission_mode": "always-proceed"},
    })
    emit_event({
        "event": "result",
        "result": {
            "conversation_id": f"conv-{task_id}",
            "status": "ERROR",
            "error": msg,
            "response": msg,
            "num_turns": 1,
        },
    })
    sys.exit(1)

emit_event({
    "event": "init",
    "conversation_id": f"conv-{task_id}",
    "init": {"tools": [], "permission_mode": "always-proceed"},
})

marker = os.path.join(walk_dir, f"live-{task_id}")
with open(marker, "w", encoding="utf-8") as mf:
    mf.write(now_iso + "\n")

if kind == "sleep":
    secs = float(action.get("seconds", 30))
    time.sleep(secs)

step_idx = 0
if kind in ("resume_later", "nothing_needed", "comment_on_issue", "mcp_sequence"):
    proc = open_mcp()
    calls = action.get("calls")
    if not calls:
        if kind == "resume_later":
            calls = []
            if action.get("push_branch"):
                branch_m = re.search(r'new branch named "([^"]+)"', prompt)
                branch = branch_m.group(1) if branch_m else action["push_branch"]
                calls.append((
                    "run_command",
                    {
                        "command": (
                            f"cd work && echo '{task_id}' > walk.txt && git add walk.txt && "
                            f"git commit -m 'chore: touch walk.txt\n\nWalkthrough commit.' && "
                            f"git push origin HEAD:{branch}"
                        ),
                        "description": "pushing branch before resume_later",
                    },
                ))
            if action.get("pre_shell"):
                calls.append(("run_command", {"command": action["pre_shell"], "description": "pre-park command"}))
            calls.append(("resume_later", action["mcp_args"]))
        elif kind == "comment_on_issue":
            calls = [("comment_on_issue", {"comment": action.get("comment", "done")})]
        else:
            calls = [("nothing_needed", {"reason": action.get("reason", "done")})]
    else:
        calls = [(c["tool"], c["args"]) for c in calls]

    outputs = []
    for req_id, (tool_name, tool_args) in enumerate(calls, start=2):
        resp = mcp_call(proc, req_id, tool_name, tool_args)
        result_obj = resp.get("result") or {}
        text = "\n".join(c.get("text", "") for c in result_obj.get("content", []))
        if not text and resp.get("error"):
            text = json.dumps(resp["error"])
        outputs.append({"tool": tool_name, "isError": result_obj.get("isError", False), "text": text})
        agy_name = f"mcp_grain-sandbox_{tool_name}"
        base = {
            "conversation_id": f"conv-{task_id}",
            "step_index": step_idx,
            "step_type": "tool",
            "tool_name": agy_name,
        }
        emit_event({"event": "step_update", "step_update": {**base, "state": "ACTIVE", "tool_info": {"name": agy_name, "parameters": tool_args}}})
        emit_event({"event": "step_update", "step_update": {**base, "state": "DONE", "tool_info": {"name": agy_name, "parameters": tool_args, "output": text}}})
        step_idx += 1

    with open(os.path.join(walk_dir, "runs", f"mcp-{task_id}.jsonl"), "a", encoding="utf-8") as mf:
        mf.write(json.dumps({"at": now_iso, "outputs": outputs}) + "\n")

    proc.stdin.close()
    proc.terminate()

emit_event({
    "event": "step_update",
    "step_update": {
        "conversation_id": f"conv-{task_id}",
        "step_index": step_idx,
        "step_type": "agent_response",
        "state": "ACTIVE",
        "text_delta": action.get("response", "walkthrough turn finished"),
    },
})
emit_event({
    "event": "step_update",
    "step_update": {
        "conversation_id": f"conv-{task_id}",
        "step_index": step_idx,
        "step_type": "agent_response",
        "state": "DONE",
    },
})
emit_event({
    "event": "result",
    "result": {
        "conversation_id": f"conv-{task_id}",
        "status": "SUCCESS",
        "response": action.get("response", "walkthrough turn finished"),
        "num_turns": 1,
    },
})
