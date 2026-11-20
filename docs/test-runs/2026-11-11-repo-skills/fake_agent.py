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

"""docs/test-plans/2026-11-11.md §12: a fake agy that dumps its full prompt.

Installed as an antigravity connection's CLI path (walk.py). grain starts it
the way it starts agy: a HOME of its own carrying
.gemini/config/mcp_config.json, the prompt as one stream-json event on stdin,
and stream-json expected on stdout.

Every prompt carrying "SCENARIO=<name>" is written to
<out>/<name>-<n>.txt (n counts that scenario's attempts from 1). A prompt
for the "evil" scenario's first attempt also drives the grain-sandbox MCP
server's run_command to commit skills/evil/SKILL.md on the run's working
branch and push it, then calls resume_later so grain dispatches the task
again: item 2 needs that next attempt not to see the planted skill.
"""

import argparse
import glob
import json
import os
import re
import subprocess
import sys


class Stdio:
    def __init__(self, command, args, env):
        self.proc = subprocess.Popen([command] + list(args), stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                     stderr=sys.stderr, env=env, text=True, bufsize=1)

    def rpc(self, msg):
        self.proc.stdin.write(json.dumps(msg) + "\n")
        self.proc.stdin.flush()
        if "id" not in msg:
            return None
        while True:
            line = self.proc.stdout.readline()
            if not line:
                raise RuntimeError("the MCP server closed its output")
            try:
                m = json.loads(line)
            except ValueError:
                continue
            if m.get("id") == msg["id"]:
                return m

    def close(self):
        self.proc.stdin.close()
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()


def result_text(msg):
    res = msg.get("result") or {}
    return "".join(p.get("text", "") for p in res.get("content") or [] if isinstance(p, dict))


def longest_string(v):
    best = ""
    if isinstance(v, str):
        return v
    if isinstance(v, dict):
        v = list(v.values())
    if isinstance(v, list):
        for x in v:
            s = longest_string(x)
            if len(s) > len(best):
                best = s
    return best


def push_evil(log):
    """Plants skills/evil on the working branch and parks; returns the
    (tool, arguments, output) steps to report as agy's stream-json, which
    is where grain reads an agent's tool calls from."""
    steps = []
    home = os.environ.get("HOME", "")
    cfg = json.load(open(os.path.join(home, ".gemini", "config", "mcp_config.json")))
    server = cfg["mcpServers"]["grain-sandbox"]
    conn = Stdio(server["command"], server.get("args", []), dict(os.environ))
    conn.rpc({"jsonrpc": "2.0", "id": 1, "method": "initialize",
              "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                         "clientInfo": {"name": "fake-agy-skills", "version": "1"}}})
    conn.rpc({"jsonrpc": "2.0", "method": "notifications/initialized"})
    cmd = ("cd work && mkdir -p skills/evil && "
           "printf -- '---\\nname: evil\\ndescription: Planted on the working branch; must never be indexed.\\n---\\n' "
           "> skills/evil/SKILL.md && git add skills/evil/SKILL.md && "
           "git -c user.name=fake -c user.email=fake@example.com commit -qm 'plant evil skill' && "
           "git push -q origin HEAD 2>&1; git log --oneline -1; git rev-parse --abbrev-ref HEAD")
    args = {"command": cmd, "description": "planting evil"}
    res = conn.rpc({"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                    "params": {"name": "run_command", "arguments": args}})
    steps.append(("run_command", args, result_text(res)))
    log.write(json.dumps(res) + "\n")
    # Park the task for its next attempt the way an agent does: grain
    # queues it again on its next cycle, on the same working branch.
    args = {"reason": "walkthrough: the next attempt must not index skills/evil"}
    res = conn.rpc({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
                    "params": {"name": "resume_later", "arguments": args}})
    steps.append(("resume_later", args, result_text(res)))
    conn.close()
    log.write(json.dumps(res) + "\n")
    return steps


def emit(event):
    print(json.dumps(event), flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    args, rest = ap.parse_known_args()
    if "--version" in rest:
        print("fake agy (docs/test-runs/2026-11-11-repo-skills)")
        return
    if "models" in rest:
        return
    raw = sys.stdin.readline()
    try:
        prompt = longest_string(json.loads(raw))
    except ValueError:
        prompt = raw
    m = re.search(r"SCENARIO=([a-z0-9-]+)", prompt)
    note = "Nothing to do."
    steps = []
    if m:
        name = m.group(1)
        os.makedirs(args.out, exist_ok=True)
        n = len(glob.glob(os.path.join(args.out, f"{name}-[0-9]*.txt"))) + 1
        with open(os.path.join(args.out, f"{name}-{n}.txt"), "w") as f:
            f.write(prompt)
        with open(os.path.join(args.out, f"{name}-{n}.argv.json"), "w") as f:
            json.dump(rest, f)
        if name.startswith("evil") and n == 1:
            with open(os.path.join(args.out, f"{name}-push.log"), "w") as log:
                try:
                    steps = push_evil(log)
                except Exception as exc:  # recorded so walk.py can say what broke
                    log.write(repr(exc) + "\n")
        note = f"Recorded {name} attempt {n}."
    conv = "fake-agy-skills"
    emit({"event": "init", "conversation_id": conv, "init": {"tools": [], "permission_mode": "always-proceed"}})
    for i, (name, params, output) in enumerate(steps):
        tool = "mcp_grain-sandbox_" + name
        emit({"event": "step_update", "step_update": {"conversation_id": conv, "step_index": i, "step_type": "tool",
                                                      "state": "ACTIVE", "tool_name": tool,
                                                      "tool_info": {"name": tool, "parameters": params}}})
        emit({"event": "step_update", "step_update": {"conversation_id": conv, "step_index": i, "step_type": "tool",
                                                      "state": "DONE", "tool_name": tool,
                                                      "tool_info": {"name": tool, "output": output}}})
    n = len(steps)
    emit({"event": "step_update", "step_update": {"conversation_id": conv, "step_index": n,
                                                  "step_type": "agent_response", "state": "ACTIVE",
                                                  "text_delta": note}})
    emit({"event": "step_update", "step_update": {"conversation_id": conv, "step_index": n,
                                                  "step_type": "agent_response", "state": "DONE"}})
    emit({"event": "result", "result": {"conversation_id": conv, "status": "SUCCESS", "response": note,
                                        "num_turns": 1}})


if __name__ == "__main__":
    main()
