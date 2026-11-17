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

"""Scriptable fake Antigravity (agy) CLI for the §5 weekly usage walkthrough.

Extended from docs/test-runs/2026-11-13-grain-health-exit-codes/fake-agy.py
to emit configurable token usage and weekly/interval usage-limit refusals.
"""

import datetime
import json
import os
import re
import subprocess
import sys

sys.dont_write_bytecode = True

model_arg = ""
args = sys.argv[1:]
for idx, arg in enumerate(args):
    if arg == "--help":
        sys.exit(0)
    if arg == "--version":
        print("fake agy (weekly usage walkthrough)")
        sys.exit(0)
    if arg == "models":
        sys.exit(0)
    if arg == "--model" and idx + 1 < len(args):
        model_arg = args[idx + 1]
    elif arg.startswith("--model="):
        model_arg = arg.split("=", 1)[1]

prompt = sys.stdin.read()
walk_dir = os.environ.get("WALK_DIR", "/tmp/walk-weekly-usage")
os.makedirs(os.path.join(walk_dir, "runs"), exist_ok=True)

m = re.search(r"You are running as task ([A-Za-z0-9_-]+)", prompt)
home_base = os.path.basename(os.environ.get("HOME", "").rstrip("/"))
task_id = (
    m.group(1)
    if m
    else (home_base if home_base.isdigit() else os.environ.get("GRAIN_TASK_ID", "unknown"))
)
now_iso = datetime.datetime.now(datetime.timezone.utc).isoformat()
conv_id = f"conv-{task_id}"
is_stoprule_nudge = "A reply without a tool call ends your run" in prompt

action_candidates = [
    os.path.join(walk_dir, f"action-agy-{task_id}.json"),
    os.path.join(walk_dir, f"action-{task_id}.json"),
    os.path.join(walk_dir, "action-agy-default.json"),
    os.path.join(walk_dir, "action-default.json"),
]
action_path = next((p for p in action_candidates if os.path.exists(p)), "")

action = (
    {"kind": "plain_reply", "response": "already stopped"}
    if is_stoprule_nudge
    else {"kind": "nothing_needed", "reason": "walkthrough default completion"}
)
if action_path and not is_stoprule_nudge:
    with open(action_path, "r", encoding="utf-8") as af:
        loaded = json.load(af)
    if isinstance(loaded, list):
        if loaded:
            action = loaded.pop(0)
            with open(action_path, "w", encoding="utf-8") as af:
                json.dump(loaded, af)
    elif isinstance(loaded, dict):
        action = loaded

kind = action.get("kind", "nothing_needed")

with open(os.path.join(walk_dir, "runs", "invocations.jsonl"), "a", encoding="utf-8") as logf:
    logf.write(
        json.dumps(
            {
                "at": now_iso,
                "framework": "antigravity",
                "model": model_arg,
                "task_id": task_id,
                "kind": kind,
                "action": action,
            }
        )
        + "\n"
    )


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
    proc.stdin.write(
        json.dumps(
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "protocolVersion": "2025-03-26",
                    "capabilities": {},
                    "clientInfo": {"name": "fake-agy", "version": "1.0"},
                },
            }
        )
        + "\n"
    )
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
    proc.stdin.write(
        json.dumps(
            {
                "jsonrpc": "2.0",
                "id": req_id,
                "method": "tools/call",
                "params": {"name": tool_name, "arguments": arguments},
            }
        )
        + "\n"
    )
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


emit_event(
    {
        "event": "init",
        "conversation_id": conv_id,
        "init": {"tools": [], "permission_mode": "always-proceed"},
    }
)

if kind == "weekly_limit":
    dur = action.get("retry_after", "72h")
    msg = action.get(
        "message",
        f"You've hit your weekly limit. Usage limit reached; quota will reset after {dur}.",
    )
    emit_event(
        {
            "event": "result",
            "result": {
                "conversation_id": conv_id,
                "status": "ERROR",
                "error": msg,
                "num_turns": 1,
            },
        }
    )
    sys.stderr.write(msg + "\n")
    sys.exit(1)

if kind == "interval_limit":
    dur = action.get("retry_after", "2h15m")
    msg = action.get(
        "message",
        f"You have exhausted your capacity on this model. Your quota will reset after {dur}.",
    )
    emit_event(
        {
            "event": "result",
            "result": {
                "conversation_id": conv_id,
                "status": "ERROR",
                "error": msg,
                "num_turns": 1,
            },
        }
    )
    sys.stderr.write(msg + "\n")
    sys.exit(1)

step_idx = 0

if kind in ("resume_later", "nothing_needed", "comment_on_issue", "mcp_sequence"):
    proc = open_mcp()
    calls = action.get("calls")
    if not calls:
        if kind == "comment_on_issue":
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
            "conversation_id": conv_id,
            "step_index": step_idx,
            "step_type": "tool",
            "tool_name": agy_name,
        }
        emit_event(
            {
                "event": "step_update",
                "step_update": {**base, "state": "ACTIVE", "tool_info": {"name": agy_name, "parameters": tool_args}},
            }
        )
        emit_event(
            {
                "event": "step_update",
                "step_update": {
                    **base,
                    "state": "DONE",
                    "tool_info": {"name": agy_name, "parameters": tool_args, "output": text},
                },
            }
        )
        step_idx += 1

    with open(os.path.join(walk_dir, "runs", f"mcp-{task_id}.jsonl"), "a", encoding="utf-8") as mf:
        mf.write(json.dumps({"at": now_iso, "framework": "antigravity", "model": model_arg, "outputs": outputs}) + "\n")

    proc.stdin.close()
    proc.terminate()

emit_event(
    {
        "event": "step_update",
        "step_update": {
            "conversation_id": conv_id,
            "step_index": step_idx,
            "step_type": "agent_response",
            "state": "ACTIVE",
            "text_delta": action.get("response", "walkthrough turn finished"),
        },
    }
)
emit_event(
    {
        "event": "step_update",
        "step_update": {
            "conversation_id": conv_id,
            "step_index": step_idx,
            "step_type": "agent_response",
            "state": "DONE",
        },
    }
)

result_obj = {
    "conversation_id": conv_id,
    "status": "SUCCESS",
    "response": action.get("response", "walkthrough turn finished"),
    "num_turns": action.get("num_turns", 1),
}
usage = action.get(
    "usage",
    {"input_tokens": 120000, "output_tokens": 15000, "cache_read_tokens": 30000},
)
if usage:
    result_obj["usage"] = usage

emit_event({"event": "result", "result": result_obj})
