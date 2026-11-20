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

"""Scriptable fake Antigravity (agy) CLI for the 2026-11-19 §4 walkthrough."""

import datetime
import json
import os
import re
import subprocess
import sys
import time

sys.dont_write_bytecode = True

for arg in sys.argv[1:]:
    if arg == "--help":
        sys.exit(0)
    if arg == "--version":
        print("fake agy (2026-11-19 processes & reconcile walkthrough)")
        sys.exit(0)
    if arg == "models":
        sys.exit(0)

prompt = sys.stdin.read()
walk_dir = os.environ.get("WALK_DIR", "/tmp/walk-proc")
runs_dir = os.path.join(walk_dir, "runs")
os.makedirs(runs_dir, exist_ok=True)

m = re.search(r"You are running as task ([A-Za-z0-9_-]+)", prompt)
home_base = os.path.basename(os.environ.get("HOME", "").rstrip("/"))
task_id = (
    m.group(1)
    if m
    else (home_base if home_base.isdigit() else os.environ.get("GRAIN_TASK_ID", "unknown"))
)
started_ts = time.time()
started_iso = datetime.datetime.now(datetime.timezone.utc).isoformat()
conv_id = f"conv-{task_id}"
is_stoprule_nudge = "A reply without a tool call ends your run" in prompt

action_path = os.path.join(walk_dir, f"action-{task_id}.json")
if not os.path.exists(action_path):
    action_path = os.path.join(walk_dir, "action-default.json")

action = (
    {"kind": "plain_reply", "response": "already stopped"}
    if is_stoprule_nudge
    else {"kind": "plain_reply", "response": f"turn completed for task {task_id}"}
)
if os.path.exists(action_path) and not is_stoprule_nudge:
    with open(action_path, "r", encoding="utf-8") as af:
        loaded = json.load(af)
    if isinstance(loaded, list):
        if loaded:
            action = loaded.pop(0)
            with open(action_path, "w", encoding="utf-8") as af:
                json.dump(loaded, af)
    elif isinstance(loaded, dict):
        action = loaded

# Optional marker file written as soon as the turn begins so external harnesses
# can inject a human reply mid-turn / during unwind deterministically.
marker = action.get("start_marker")
if marker:
    with open(os.path.join(runs_dir, marker), "w", encoding="utf-8") as mf:
        mf.write(json.dumps({"task_id": task_id, "started_ts": started_ts, "started_iso": started_iso}))

def emit_event(obj):
    print(json.dumps(obj), flush=True)

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

emit_event({
    "event": "init",
    "conversation_id": conv_id,
    "init": {"tools": [], "permission_mode": "always-proceed"},
})

kind = action.get("kind", "plain_reply")
step_idx = 0

if kind in ("nothing_needed", "comment_on_issue"):
    proc = open_mcp()
    if kind == "comment_on_issue":
        calls = [("comment_on_issue", {"comment": action.get("comment", "done")})]
    else:
        calls = [("nothing_needed", {"reason": action.get("reason", "walkthrough complete")})]
    for req_id, (tool_name, tool_args) in enumerate(calls, start=2):
        resp = mcp_call(proc, req_id, tool_name, tool_args)
        result_obj = resp.get("result") or {}
        text = "\n".join(c.get("text", "") for c in result_obj.get("content", []))
        agy_name = f"mcp_grain-sandbox_{tool_name}"
        base = {
            "conversation_id": conv_id,
            "step_index": step_idx,
            "step_type": "tool",
            "tool_name": agy_name,
        }
        emit_event({"event": "step_update", "step_update": {**base, "state": "ACTIVE", "tool_info": {"name": agy_name, "parameters": tool_args}}})
        emit_event({"event": "step_update", "step_update": {**base, "state": "DONE", "tool_info": {"name": agy_name, "parameters": tool_args, "output": text}}})
        step_idx += 1
    proc.stdin.close()
    proc.terminate()

emit_event({
    "event": "step_update",
    "step_update": {
        "conversation_id": conv_id,
        "step_index": step_idx,
        "step_type": "agent_response",
        "state": "ACTIVE",
        "text_delta": action.get("response", f"turn completed for task {task_id}"),
    },
})
emit_event({
    "event": "step_update",
    "step_update": {
        "conversation_id": conv_id,
        "step_index": step_idx,
        "step_type": "agent_response",
        "state": "DONE",
    },
})
emit_event({
    "event": "result",
    "result": {
        "conversation_id": conv_id,
        "status": "SUCCESS",
        "response": action.get("response", f"turn completed for task {task_id}"),
        "num_turns": 1,
    },
})

# Optional slow-unwind delay AFTER emitting the result event (before process exit),
# or waiting on an external release file, to reproduce slow in-flight unwind.
wait_file = action.get("wait_for_file")
if wait_file:
    target_path = os.path.join(runs_dir, wait_file)
    deadline = time.time() + float(action.get("wait_timeout_sec", 15.0))
    while not os.path.exists(target_path) and time.time() < deadline:
        time.sleep(0.05)

sleep_sec = float(action.get("sleep_sec", 0.0))
if sleep_sec > 0:
    time.sleep(sleep_sec)

finished_ts = time.time()
finished_iso = datetime.datetime.now(datetime.timezone.utc).isoformat()
with open(os.path.join(runs_dir, "turns.jsonl"), "a", encoding="utf-8") as tf:
    tf.write(json.dumps({
        "task_id": task_id,
        "kind": kind,
        "started_ts": started_ts,
        "finished_ts": finished_ts,
        "started_iso": started_iso,
        "finished_iso": finished_iso,
        "has_followup_text": ("follow-up during unwind" in prompt),
        "prompt_len": len(prompt),
    }) + "\n")
