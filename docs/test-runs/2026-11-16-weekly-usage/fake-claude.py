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

"""Scriptable fake Claude Code CLI for the §5 weekly usage walkthrough.

Speaks claude --output-format stream-json, executes grain-sandbox MCP calls
from --mcp-config, emits token usage, and emits weekly ("You've hit your weekly
limit") or interval ("You've hit your 5-hour limit") refusals on command.
"""

import datetime
import json
import os
import re
import subprocess
import sys
import urllib.request

sys.dont_write_bytecode = True

model_arg = ""
mcp_config_path = ""
args = sys.argv[1:]
for idx, arg in enumerate(args):
    if arg == "--help":
        print("fake claude (weekly usage walkthrough)")
        sys.exit(0)
    if arg == "--version":
        print("fake claude 2.0.0 (weekly usage walkthrough)")
        sys.exit(0)
    if arg == "--model" and idx + 1 < len(args):
        model_arg = args[idx + 1]
    elif arg.startswith("--model="):
        model_arg = arg.split("=", 1)[1]
    if arg == "--mcp-config" and idx + 1 < len(args):
        mcp_config_path = args[idx + 1]
    elif arg.startswith("--mcp-config="):
        mcp_config_path = arg.split("=", 1)[1]

prompt = sys.stdin.read()
walk_dir = os.environ.get("WALK_DIR", "/tmp/walk-weekly-usage")
os.makedirs(os.path.join(walk_dir, "runs"), exist_ok=True)

m = re.search(r"You are running as task ([A-Za-z0-9_-]+)", prompt)
task_id = m.group(1) if m else os.environ.get("GRAIN_TASK_ID", "unknown")
now = datetime.datetime.now(datetime.timezone.utc)
now_iso = now.isoformat()
session_id = f"sess-{task_id}"

action_candidates = [
    os.path.join(walk_dir, f"action-claude-{task_id}.json"),
    os.path.join(walk_dir, f"action-{task_id}.json"),
    os.path.join(walk_dir, "action-claude-default.json"),
    os.path.join(walk_dir, "action-default.json"),
]
action_path = next((p for p in action_candidates if os.path.exists(p)), "")

action = {"kind": "nothing_needed", "reason": "walkthrough default completion"}
if action_path:
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
                "framework": "claude",
                "model": model_arg,
                "task_id": task_id,
                "kind": kind,
                "action": action,
            }
        )
        + "\n"
    )


def emit(obj):
    print(json.dumps(obj), flush=True)


def format_reset_clause(offset_hours, include_date):
    target = now + datetime.timedelta(hours=offset_hours)
    hour12 = target.hour % 12 or 12
    ampm = "am" if target.hour < 12 else "pm"
    clock = f"{hour12}:{target.minute:02d}{ampm}" if target.minute else f"{hour12}{ampm}"
    if include_date:
        return f"{target.strftime('%b')} {target.day}, {clock} (UTC)"
    return f"{clock} (UTC)"


if kind == "weekly_limit":
    if "message" in action:
        msg = action["message"]
    else:
        offset_h = float(action.get("reset_hours", 72))
        clause = format_reset_clause(offset_h, include_date=True)
        label = action.get("limit_label", "weekly")
        msg = f"You've hit your {label} limit · resets {clause}"
    is_err = bool(action.get("is_error", False))
    emit(
        {
            "type": "result",
            "subtype": "error_during_execution" if is_err else "success",
            "is_error": is_err,
            "session_id": session_id,
            "result": msg,
            "num_turns": 1,
        }
    )
    sys.exit(1 if is_err else 0)

if kind == "interval_limit":
    if "message" in action:
        msg = action["message"]
    else:
        offset_h = float(action.get("reset_hours", 2.25))
        clause = format_reset_clause(offset_h, include_date=False)
        label = action.get("limit_label", "5-hour")
        msg = f"You've hit your {label} limit · resets {clause}"
    is_err = bool(action.get("is_error", False))
    emit(
        {
            "type": "result",
            "subtype": "error_during_execution" if is_err else "success",
            "is_error": is_err,
            "session_id": session_id,
            "result": msg,
            "num_turns": 1,
        }
    )
    sys.exit(1 if is_err else 0)


def run_mcp_calls(calls):
    if not mcp_config_path or not os.path.exists(mcp_config_path):
        return []
    with open(mcp_config_path, "r", encoding="utf-8") as f:
        cfg = json.load(f)["mcpServers"]["grain-sandbox"]

    outputs = []
    if "url" in cfg:
        url = cfg["url"]
        headers = {"Content-Type": "application/json", **(cfg.get("headers") or {})}

        def post_rpc(payload):
            req = urllib.request.Request(
                url,
                data=json.dumps(payload).encode("utf-8"),
                headers=headers,
                method="POST",
            )
            with urllib.request.urlopen(req, timeout=30) as resp:
                raw = resp.read().decode("utf-8").strip()
                return json.loads(raw) if raw else {}

        post_rpc(
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "protocolVersion": "2025-03-26",
                    "capabilities": {},
                    "clientInfo": {"name": "fake-claude", "version": "1.0"},
                },
            }
        )
        for req_id, (tool_name, tool_args) in enumerate(calls, start=2):
            resp = post_rpc(
                {
                    "jsonrpc": "2.0",
                    "id": req_id,
                    "method": "tools/call",
                    "params": {"name": tool_name, "arguments": tool_args},
                }
            )
            result_obj = resp.get("result") or {}
            text = "\n".join(c.get("text", "") for c in result_obj.get("content", []))
            if not text and resp.get("error"):
                text = json.dumps(resp["error"])
            outputs.append({"tool": tool_name, "isError": result_obj.get("isError", False), "text": text})
        return outputs

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
                    "clientInfo": {"name": "fake-claude", "version": "1.0"},
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

    for req_id, (tool_name, tool_args) in enumerate(calls, start=2):
        proc.stdin.write(
            json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": req_id,
                    "method": "tools/call",
                    "params": {"name": tool_name, "arguments": tool_args},
                }
            )
            + "\n"
        )
        proc.stdin.flush()
        resp = {}
        while True:
            line = proc.stdout.readline()
            if not line:
                resp = {"error": "EOF from mcpserver"}
                break
            parsed = json.loads(line)
            if parsed.get("id") == req_id:
                resp = parsed
                break
        result_obj = resp.get("result") or {}
        text = "\n".join(c.get("text", "") for c in result_obj.get("content", []))
        if not text and resp.get("error"):
            text = json.dumps(resp["error"])
        outputs.append({"tool": tool_name, "isError": result_obj.get("isError", False), "text": text})

    proc.stdin.close()
    proc.terminate()
    return outputs


if kind in ("nothing_needed", "comment_on_issue", "mcp_sequence"):
    calls = action.get("calls")
    if not calls:
        if kind == "comment_on_issue":
            calls = [("comment_on_issue", {"comment": action.get("comment", "done")})]
        else:
            calls = [("nothing_needed", {"reason": action.get("reason", "done")})]
    else:
        calls = [(c["tool"], c["args"]) for c in calls]

    outputs = run_mcp_calls(calls)
    for i, ((tool_name, tool_args), out) in enumerate(zip(calls, outputs), start=1):
        tool_use_id = f"toolu_{task_id}_{i}"
        emit(
            {
                "type": "assistant",
                "session_id": session_id,
                "message": {
                    "content": [
                        {
                            "type": "tool_use",
                            "id": tool_use_id,
                            "name": f"mcp__grain-sandbox__{tool_name}",
                            "input": tool_args,
                        }
                    ]
                },
            }
        )
        emit(
            {
                "type": "user",
                "session_id": session_id,
                "message": {
                    "content": [
                        {
                            "type": "tool_result",
                            "tool_use_id": tool_use_id,
                            "content": out["text"],
                            "is_error": out["isError"],
                        }
                    ]
                },
            }
        )

    with open(os.path.join(walk_dir, "runs", f"mcp-{task_id}.jsonl"), "a", encoding="utf-8") as mf:
        mf.write(json.dumps({"at": now_iso, "framework": "claude", "model": model_arg, "outputs": outputs}) + "\n")

usage = action.get(
    "usage",
    {
        "input_tokens": 450000,
        "output_tokens": 50000,
        "cache_read_input_tokens": 100000,
        "cache_creation_input_tokens": 25000,
    },
)

emit(
    {
        "type": "result",
        "subtype": "success",
        "is_error": False,
        "session_id": session_id,
        "result": action.get("response", "walkthrough turn finished"),
        "num_turns": action.get("num_turns", 1),
        "total_cost_usd": action.get("cost_usd", 0.75),
        "usage": usage,
    }
)
