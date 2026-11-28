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

"""Fake Codex CLI for §9 walkthrough: inspects CODEX_HOME/auth.json and reports via MCP."""

import datetime
import hashlib
import json
import os
import re
import subprocess
import sys
import tomllib

sys.dont_write_bytecode = True

for arg in sys.argv[1:]:
    if arg in ("--help", "-h"):
        sys.exit(0)
    if arg in ("--version", "-V"):
        print("codex-cli 0.169.0")
        sys.exit(0)

if "debug" in sys.argv[1:] and "models" in sys.argv[1:]:
    print(json.dumps({
        "models": [
            {"slug": "gpt-5.1-codex-walk-a", "apply_patch_tool_type": None},
            {"slug": "gpt-5.1-codex-walk-b", "apply_patch_tool_type": None},
            {"slug": "gpt-5.1-codex-a", "apply_patch_tool_type": None},
            {"slug": "gpt-5.1-codex-b", "apply_patch_tool_type": None},
        ]
    }))
    sys.exit(0)

prompt = sys.stdin.read()
walk_dir = os.environ.get("WALK_DIR", "/tmp/walk-settings-parity")
os.makedirs(os.path.join(walk_dir, "runs"), exist_ok=True)

m = re.search(r"You are running as task ([A-Za-z0-9_-]+)", prompt)
task_id = m.group(1) if m else os.environ.get("GRAIN_TASK_ID", "unknown")

codex_home = os.environ.get("CODEX_HOME", "")
auth_path = os.path.join(codex_home, "auth.json") if codex_home else ""
auth_info = {
    "task_id": task_id,
    "at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "codex_home": codex_home,
    "openai_api_key_set": bool(os.environ.get("OPENAI_API_KEY")),
    "auth_exists": False,
    "auth_is_symlink": False,
    "auth_target": "",
    "auth_sha256": "",
    "auth_account": "",
    "auth_keys": [],
}

if auth_path and os.path.lexists(auth_path):
    auth_info["auth_exists"] = os.path.exists(auth_path)
    auth_info["auth_is_symlink"] = os.path.islink(auth_path)
    if auth_info["auth_is_symlink"]:
        auth_info["auth_target"] = os.readlink(auth_path)
    if auth_info["auth_exists"]:
        with open(auth_path, "rb") as f:
            raw = f.read()
        auth_info["auth_sha256"] = hashlib.sha256(raw).hexdigest()[:16]
        try:
            parsed = json.loads(raw.decode("utf-8"))
            if isinstance(parsed, dict):
                auth_info["auth_keys"] = sorted(parsed.keys())
                tokens = parsed.get("tokens")
                if isinstance(tokens, dict):
                    auth_info["auth_account"] = str(tokens.get("account_id", ""))
                elif "account" in parsed:
                    auth_info["auth_account"] = str(parsed["account"])
        except Exception as exc:
            auth_info["parse_error"] = str(exc)

with open(os.path.join(walk_dir, "runs", f"codex-{task_id}.json"), "w", encoding="utf-8") as rf:
    json.dump(auth_info, rf, indent=2)

# Report via grain-sandbox MCP comment_on_issue so the task completes cleanly.
cfg_path = os.path.join(codex_home, "config.toml") if codex_home else ""
if cfg_path and os.path.exists(cfg_path):
    try:
        with open(cfg_path, "rb") as cf:
            cfg = tomllib.load(cf)
        mcp_servers = cfg.get("mcp_servers") or {}
        srv = mcp_servers.get("grain-sandbox") or next(
            (v for v in mcp_servers.values() if isinstance(v, dict) and v.get("command")),
            {},
        )
        if srv.get("command"):
            env = os.environ.copy()
            env.update({k: str(v) for k, v in (srv.get("env") or {}).items()})
            proc = subprocess.Popen(
                [srv["command"]] + list(srv.get("args") or []),
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
                    "clientInfo": {"name": "fake-codex", "version": "1.0"},
                },
            }) + "\n")
            proc.stdin.flush()
            while True:
                line = proc.stdout.readline()
                if not line or json.loads(line).get("id") == 1:
                    break
            proc.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
            proc.stdin.write(json.dumps({
                "jsonrpc": "2.0",
                "id": 2,
                "method": "tools/call",
                "params": {
                    "name": "comment_on_issue",
                    "arguments": {"comment": "CODEX_AUTH_PROBE " + json.dumps(auth_info, sort_keys=True)},
                },
            }) + "\n")
            proc.stdin.flush()
            while True:
                line = proc.stdout.readline()
                if not line or json.loads(line).get("id") == 2:
                    break
            proc.stdin.close()
            proc.terminate()
    except Exception as exc:
        with open(os.path.join(walk_dir, "runs", f"codex-{task_id}.err"), "w", encoding="utf-8") as ef:
            ef.write(str(exc))

for ev in [
    {"type": "thread.started", "thread_id": f"codex-thread-{task_id}"},
    {"type": "item.completed", "item": {"id": "m1", "item_type": "agent_message", "text": json.dumps(auth_info)}},
    {"type": "turn.completed", "usage": {"input_tokens": 10, "output_tokens": 5}},
]:
    print(json.dumps(ev), flush=True)
