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

"""Scriptable fake Antigravity (agy) CLI for the 2026-11-19 §2 QA v6 walkthrough."""

import datetime
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
        print("fake agy (qa-v6 walkthrough)")
        sys.exit(0)
    if arg == "models":
        sys.exit(0)

raw_stdin = sys.stdin.read()
# Unwrap --input-format stream-json envelopes so substring checks see decoded text.
prompt = raw_stdin
try:
    for line in raw_stdin.splitlines():
        line = line.strip()
        if not line:
            continue
        obj = json.loads(line)
        content = (obj.get("message") or {}).get("content")
        if isinstance(content, str):
            prompt = content
            break
except Exception:
    pass

walk_dir = os.environ.get("WALK_DIR", "/tmp/walk-qa-v6")
runs_dir = os.path.join(walk_dir, "runs")
os.makedirs(runs_dir, exist_ok=True)

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

mcp_cfg_path = os.path.join(os.environ.get("HOME", ""), ".gemini", "config", "mcp_config.json")
is_no_tools = False
if os.path.exists(mcp_cfg_path):
    try:
        with open(mcp_cfg_path, "r", encoding="utf-8") as cf:
            cfg_all = json.load(cf)
        gs_args = (cfg_all.get("mcpServers") or {}).get("grain-sandbox", {}).get("args") or []
        is_no_tools = "-no-tools" in gs_args
    except Exception:
        pass

stage_hint = "agent"
if is_no_tools:
    head = prompt[:1200].lower()
    if "inspect the proposed file plan" in head or "sibling importers" in head or "plan-critic" in head or "revise_plan" in head:
        stage_hint = "s2-plan-critic"
    elif "extract 2 to 6 testable when" in head or "needs_clarification" in head:
        stage_hint = "s0-intent"
    elif "escape-class" in prompt.lower() or "orthogonal" in head or '"findings"' in prompt.lower():
        stage_hint = "s4-verifier"
    else:
        stage_hint = "isolated-other"
elif is_stoprule_nudge:
    stage_hint = "stoprule-nudge"

seq_path = os.path.join(runs_dir, "seq.txt")
seq = 1
try:
    with open(seq_path, "r+", encoding="utf-8") as sf:
        raw = sf.read().strip()
        seq = int(raw) + 1 if raw.isdigit() else 1
        sf.seek(0)
        sf.write(str(seq))
        sf.truncate()
except FileNotFoundError:
    with open(seq_path, "w", encoding="utf-8") as sf:
        sf.write("1")

prompt_file = os.path.join(runs_dir, f"prompt-{seq:03d}-{task_id}-{stage_hint}.txt")
with open(prompt_file, "w", encoding="utf-8") as pf:
    pf.write(prompt)

inv_log = os.path.join(runs_dir, "invocations.jsonl")
with open(inv_log, "a", encoding="utf-8") as il:
    il.write(json.dumps({
        "seq": seq,
        "at": now_iso,
        "taskId": task_id,
        "isNoTools": is_no_tools,
        "stageHint": stage_hint,
        "promptFile": prompt_file,
        "promptBytes": len(prompt.encode("utf-8")),
        "hasProposeDag": "## Workstream Subtask Decomposition (S0b — propose_dag)" in prompt,
        "hasSlicePlan": "## Goal Subtask Decomposition (S0b — slice_plan)" in prompt,
        "hasCodebaseContext": "<codebase-context>" in prompt,
        "hasReplacementChar": "\ufffd" in prompt,
    }) + "\n")


def emit_event(obj):
    print(json.dumps(obj), flush=True)


emit_event({
    "event": "init",
    "conversation_id": conv_id,
    "init": {"tools": [], "permission_mode": "always-proceed"},
})

if is_no_tools:
    if stage_hint == "s0-intent":
        reply = (
            "- WHEN a task submits valid inputs SHALL persist and return 200 OK.\n"
            "- WHEN invalid input arrives SHALL reject with 400 Bad Request.\n"
            "Non-goals: unrelated refactors."
        )
    elif stage_hint == "s2-plan-critic":
        reply = "PASS: Proposed plan covers sibling importers and regression tests cleanly."
    elif stage_hint == "s4-verifier":
        reply = '{"findings": []}'
    else:
        reply = "OK"
    emit_event({
        "event": "step_update",
        "step_update": {
            "conversation_id": conv_id,
            "step_index": 0,
            "step_type": "agent_response",
            "state": "ACTIVE",
            "text_delta": reply,
        },
    })
    emit_event({
        "event": "step_update",
        "step_update": {
            "conversation_id": conv_id,
            "step_index": 0,
            "step_type": "agent_response",
            "state": "DONE",
        },
    })
    emit_event({
        "event": "result",
        "result": {
            "conversation_id": conv_id,
            "status": "SUCCESS",
            "response": reply,
            "num_turns": 1,
            "usage": {"input_tokens": 120, "output_tokens": 40},
        },
    })
    sys.exit(0)

action_path = os.path.join(walk_dir, f"action-{task_id}.json")
if not os.path.exists(action_path) and "Propose a high-complexity root task for acme/widgets" in prompt:
    sess_action = os.path.join(walk_dir, "action-console-session.json")
    if os.path.exists(sess_action):
        action_path = sess_action
if not os.path.exists(action_path):
    action_path = os.path.join(walk_dir, "action-default.json")

action = (
    {"kind": "plain_reply", "response": "already stopped"}
    if is_stoprule_nudge
    else {"kind": "nothing_needed", "reason": "walkthrough default completion"}
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


def open_mcp():
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

    with open(os.path.join(runs_dir, f"mcp-{task_id}.jsonl"), "a", encoding="utf-8") as mf:
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
