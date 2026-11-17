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

"""Scriptable fake Antigravity (agy) CLI for the 2026-11-16 §3 walkthrough.

Supports per-task action-<id>.json, ordered prompt-regex rules in rules.json
(with optional once=true), and action-default.json fallback. Records decoded
prompts and MCP tool responses under $WALK_DIR/runs/.
"""

import datetime
import fcntl
import json
import os
import re
import subprocess
import sys

sys.dont_write_bytecode = True

for arg in sys.argv[1:]:
    if arg == "--help":
        sys.exit(0)
    if arg == "--version":
        print("fake agy (review findings walkthrough)")
        sys.exit(0)
    if arg == "models":
        sys.exit(0)

prompt = sys.stdin.buffer.read().decode("utf-8", "surrogateescape")
walk_dir = os.environ.get("WALK_DIR", "/tmp/walk-review-gates")
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

decoded_lines = []
for line in prompt.splitlines():
    try:
        ev = json.loads(line)
    except ValueError:
        decoded_lines.append(line)
        continue
    content = (ev.get("message") or {}).get("content") if isinstance(ev, dict) else None
    if isinstance(content, str):
        decoded_lines.append(content)
    elif isinstance(content, list):
        decoded_lines.extend(c.get("text", "") for c in content if isinstance(c, dict))
decoded_text = "\n".join(decoded_lines)
is_stoprule_nudge = "A reply without a tool call ends your run" in decoded_text

seq = 1
while os.path.exists(os.path.join(walk_dir, "runs", f"prompt-{task_id}-{seq}.txt")):
    seq += 1
with open(
    os.path.join(walk_dir, "runs", f"prompt-{task_id}-{seq}.txt"),
    "w",
    encoding="utf-8",
    errors="surrogateescape",
) as pf:
    pf.write(decoded_text)


def load_action():
    if is_stoprule_nudge:
        return {"kind": "plain_reply", "response": "already stopped"}

    action_path = os.path.join(walk_dir, f"action-{task_id}.json")
    if os.path.exists(action_path):
        with open(action_path, "r+", encoding="utf-8") as af:
            fcntl.flock(af, fcntl.LOCK_EX)
            loaded = json.load(af)
            if isinstance(loaded, list):
                if loaded:
                    act = loaded.pop(0)
                    af.seek(0)
                    af.truncate()
                    json.dump(loaded, af)
                    return act
            elif isinstance(loaded, dict):
                return loaded

    rules_path = os.path.join(walk_dir, "rules.json")
    if os.path.exists(rules_path):
        with open(rules_path, "r+", encoding="utf-8") as rf:
            fcntl.flock(rf, fcntl.LOCK_EX)
            rules = json.load(rf)
            matched = None
            keep = []
            for rule in rules:
                if matched is None and re.search(rule["match"], decoded_text, re.DOTALL):
                    matched = rule["action"]
                    if not rule.get("once", False):
                        keep.append(rule)
                else:
                    keep.append(rule)
            if matched is not None:
                if len(keep) != len(rules):
                    rf.seek(0)
                    rf.truncate()
                    json.dump(keep, rf)
                return matched

    default_path = os.path.join(walk_dir, "action-default.json")
    if os.path.exists(default_path):
        with open(default_path, "r", encoding="utf-8") as df:
            return json.load(df)
    return {"kind": "nothing_needed", "reason": "walkthrough default completion"}


action = load_action()


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


emit_event({
    "event": "init",
    "conversation_id": conv_id,
    "init": {"tools": [], "permission_mode": "always-proceed"},
})

kind = action.get("kind", "nothing_needed")
step_idx = 0

if kind in ("nothing_needed", "comment_on_issue", "mcp_sequence"):
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
        "conversation_id": conv_id,
        "step_index": step_idx,
        "step_type": "agent_response",
        "state": "ACTIVE",
        "text_delta": action.get("response", "walkthrough turn finished"),
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
        "response": action.get("response", "walkthrough turn finished"),
        "num_turns": 1,
    },
})
