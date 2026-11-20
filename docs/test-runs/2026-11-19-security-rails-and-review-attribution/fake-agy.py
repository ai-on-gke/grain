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

"""Scriptable fake Antigravity (agy) CLI for the 2026-11-19 §7 walkthrough.

Supports:
- grain-sandbox stdio MCP tool calls (run_command, comment_on_issue, nothing_needed, etc.)
- grain-controller Streamable HTTP MCP tool calls (grain_invoke, grain_explore_scope, etc.)
  read from ~/.gemini/config/mcp_config.json
- _signal_and_wait pseudo-tool for deterministic mid-run synchronization with outside pushes
- Per-task action-<id>.json, rules.json (first matching regex), and action-default.json
"""

import datetime
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

for arg in sys.argv[1:]:
    if arg == "--help":
        sys.exit(0)
    if arg == "--version":
        print("fake agy (2026-11-19 s7 walkthrough)")
        sys.exit(0)
    if arg == "models":
        sys.exit(0)

prompt = sys.stdin.read()
walk_dir = os.environ.get("WALK_DIR", "/home/debian/walk-s7")
os.makedirs(os.path.join(walk_dir, "runs"), exist_ok=True)

m = re.search(r"You are running as task ([A-Za-z0-9_-]+)", prompt)
home_base = os.path.basename(os.environ.get("HOME", "").rstrip("/"))
task_id = (
    m.group(1)
    if m
    else (home_base if home_base.isdigit() else os.environ.get("GRAIN_TASK_ID", "unknown"))
)
now_iso = datetime.datetime.now(datetime.timezone.utc).isoformat()
conv_id = "" if os.path.exists(os.path.join(walk_dir, "no-session")) else f"conv-{task_id}"

run_record = {
    "task_id": task_id,
    "started_seen_at": now_iso,
    "home": os.environ.get("HOME", ""),
    "is_stoprule_nudge": "A reply without a tool call ends your run" in prompt,
    "prompt_head": prompt[:600],
}
with open(os.path.join(walk_dir, "runs", f"task-{task_id}.jsonl"), "a", encoding="utf-8") as rf:
    rf.write(json.dumps(run_record) + "\n")

prompt_n = 1
while os.path.exists(os.path.join(walk_dir, "runs", f"prompt-{task_id}-{prompt_n}.txt")):
    prompt_n += 1
with open(os.path.join(walk_dir, "runs", f"prompt-{task_id}-{prompt_n}.txt"), "w", encoding="utf-8") as pf:
    pf.write(prompt)

explicit_action_path = os.path.join(walk_dir, f"action-{task_id}.json")
default_action_path = os.path.join(walk_dir, "action-default.json")

action = (
    {"kind": "plain_reply", "response": "already stopped"}
    if run_record["is_stoprule_nudge"]
    else {"kind": "nothing_needed", "reason": "walkthrough default completion"}
)

chosen = False
if not run_record["is_stoprule_nudge"] and os.path.exists(explicit_action_path):
    with open(explicit_action_path, "r", encoding="utf-8") as af:
        loaded = json.load(af)
    if isinstance(loaded, list):
        if loaded:
            action = loaded.pop(0)
            chosen = True
            with open(explicit_action_path, "w", encoding="utf-8") as af:
                json.dump(loaded, af)
    elif isinstance(loaded, dict):
        action = loaded
        chosen = True

rules_path = os.path.join(walk_dir, "rules.json")
if not chosen and os.path.exists(rules_path) and not run_record["is_stoprule_nudge"]:
    with open(rules_path, "r", encoding="utf-8") as rf:
        rules = json.load(rf)
    for rule in rules:
        if re.search(rule["match"], prompt, re.S):
            if rule.get("repeat") and rule.get("action"):
                action = rule["action"]
                chosen = True
            elif rule.get("actions"):
                action = rule["actions"].pop(0)
                chosen = True
            break
    if chosen:
        with open(rules_path, "w", encoding="utf-8") as rf:
            json.dump(rules, rf)

if not chosen and os.path.exists(default_action_path) and not run_record["is_stoprule_nudge"]:
    with open(default_action_path, "r", encoding="utf-8") as af:
        loaded = json.load(af)
    if isinstance(loaded, dict):
        action = loaded


def load_mcp_config():
    mcp_cfg_path = os.path.join(os.environ["HOME"], ".gemini", "config", "mcp_config.json")
    with open(mcp_cfg_path, "r", encoding="utf-8") as f:
        return json.load(f).get("mcpServers", {})


def open_sandbox_mcp():
    cfg = load_mcp_config()["grain-sandbox"]
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


def sandbox_mcp_call(proc, req_id, tool_name, arguments):
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


class ControllerMCPClient:
    """Minimal Streamable HTTP JSON-RPC client for grain-controller."""

    def __init__(self, url):
        self.url = url
        self.session = None
        self.req_id = 100
        self._post({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {"name": "fake-agy", "version": "1.0"},
            },
        })
        self._post({"jsonrpc": "2.0", "method": "notifications/initialized"}, expect_reply=False)

    def _post(self, payload, expect_reply=True):
        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": "2025-03-26",
        }
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        req = urllib.request.Request(
            self.url,
            data=json.dumps(payload).encode("utf-8"),
            headers=headers,
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                sid = resp.headers.get("Mcp-Session-Id")
                if sid:
                    self.session = sid
                raw = resp.read().decode("utf-8", errors="replace")
                ctype = resp.headers.get("Content-Type", "")
                if not expect_reply or not raw.strip():
                    return {}
                if ctype.startswith("text/event-stream"):
                    for line in raw.splitlines():
                        if line.startswith("data:"):
                            chunk = line[5:].strip()
                            if not chunk:
                                continue
                            obj = json.loads(chunk)
                            if obj.get("id") == payload.get("id"):
                                return obj
                    return {"error": f"no SSE response for id {payload.get('id')}: {raw[:300]}"}
                return json.loads(raw)
        except urllib.error.HTTPError as e:
            body = e.read().decode("utf-8", errors="replace")
            return {"error": {"http_status": e.code, "body": body}}
        except Exception as e:
            return {"error": str(e)}

    def call_tool(self, tool_name, arguments):
        self.req_id += 1
        return self._post({
            "jsonrpc": "2.0",
            "id": self.req_id,
            "method": "tools/call",
            "params": {"name": tool_name, "arguments": arguments},
        })


def emit_event(obj):
    print(json.dumps(obj), flush=True)


emit_event({
    "event": "init",
    "conversation_id": conv_id,
    "init": {"tools": [], "permission_mode": "always-proceed"},
})

marker = os.path.join(walk_dir, f"live-{task_id}")
with open(marker, "w", encoding="utf-8") as mf:
    mf.write(now_iso + "\n")

# Dump mcp_config.json for debugging/audit.
try:
    with open(os.path.join(walk_dir, "runs", f"mcp-config-{task_id}.json"), "w", encoding="utf-8") as cf:
        json.dump(load_mcp_config(), cf, indent=2)
except Exception:
    pass


def wait_for_file(path, timeout=90, beat_start=1000):
    deadline = time.time() + float(timeout)
    beat = beat_start
    last_beat = time.time()
    while not os.path.exists(path) and time.time() < deadline:
        time.sleep(0.15)
        if time.time() - last_beat > 15:
            last_beat = time.time()
            emit_event({"event": "step_update", "step_update": {
                "conversation_id": conv_id, "step_index": beat, "step_type": "agent_response",
                "state": "DONE", "text_delta": "still working"}})
            beat += 1
    return os.path.exists(path)


if action.get("hold"):
    hold = marker + ".hold"
    with open(hold, "w", encoding="utf-8") as hf:
        hf.write(now_iso + "\n")
    deadline = time.time() + float(action.get("hold_seconds", 90))
    beat = 1000
    last_beat = time.time()
    while os.path.exists(hold) and time.time() < deadline:
        time.sleep(0.15)
        if time.time() - last_beat > 15:
            last_beat = time.time()
            emit_event({"event": "step_update", "step_update": {
                "conversation_id": conv_id, "step_index": beat, "step_type": "agent_response",
                "state": "DONE", "text_delta": "still working"}})
            beat += 1

kind = action.get("kind", "nothing_needed")
step_idx = 0

if kind in ("nothing_needed", "comment_on_issue", "mcp_sequence"):
    proc = open_sandbox_mcp()
    ctrl_client = None

    def get_controller():
        global ctrl_client
        if ctrl_client is None:
            servers = load_mcp_config()
            if "grain-controller" not in servers:
                return None
            ctrl_client = ControllerMCPClient(servers["grain-controller"]["serverUrl"])
        return ctrl_client

    branch_m = (
        re.search(r'new branch named "([^"]+)"', prompt)
        or re.search(r'Push any fixes to "([^"]+)"', prompt)
        or re.search(r'git push origin (?:HEAD:)?([^\s`"]+)', prompt)
    )
    branch = branch_m.group(1) if branch_m else "unknown-branch"

    def subst(v):
        if isinstance(v, str):
            return (
                v.replace("{task}", task_id)
                .replace("{branch}", branch)
                .replace("{walk_dir}", walk_dir)
            )
        if isinstance(v, dict):
            return {k: subst(x) for k, x in v.items()}
        if isinstance(v, list):
            return [subst(x) for x in v]
        return v

    raw_calls = action.get("calls")
    if not raw_calls:
        if kind == "comment_on_issue":
            raw_calls = [{"tool": "comment_on_issue", "args": {"comment": action.get("comment", "done")}}]
        else:
            raw_calls = [{"tool": "nothing_needed", "args": {"reason": action.get("reason", "done")}}]

    outputs = []
    beat_counter = 2000
    for req_id, call in enumerate(raw_calls, start=2):
        server = call.get("server", "grain-sandbox")
        tool_name = call["tool"]
        tool_args = subst(call.get("args", {}))

        if tool_name == "_signal_and_wait":
            touch_path = tool_args.get("touch")
            if touch_path:
                with open(touch_path, "w", encoding="utf-8") as tf:
                    tf.write(f"{task_id} {branch}\n")
            wait_path = tool_args.get("wait")
            ok = True
            if wait_path:
                ok = wait_for_file(wait_path, timeout=tool_args.get("timeout", 90), beat_start=beat_counter)
                beat_counter += 20
            outputs.append({"server": "_sync", "tool": tool_name, "ok": ok, "args": tool_args})
            continue

        if server == "grain-controller":
            c = get_controller()
            if c is None:
                resp = {"error": "grain-controller not present in mcp_config.json"}
            else:
                resp = c.call_tool(tool_name, tool_args)
            agy_name = f"mcp_grain-controller_{tool_name}"
        else:
            resp = sandbox_mcp_call(proc, req_id, tool_name, tool_args)
            agy_name = f"mcp_grain-sandbox_{tool_name}"

        result_obj = resp.get("result") or {}
        text = "\n".join(c.get("text", "") for c in result_obj.get("content", []))
        if not text and resp.get("error"):
            text = json.dumps(resp["error"])
        outputs.append({
            "server": server,
            "tool": tool_name,
            "args": tool_args,
            "isError": bool(result_obj.get("isError", False) or resp.get("error")),
            "text": text,
        })
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
        mf.write(json.dumps({"at": now_iso, "task_id": task_id, "branch": branch, "outputs": outputs}) + "\n")

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
