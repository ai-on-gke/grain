#!/usr/bin/env python3
"""A scripted custom agent backend (docs/custom-agent-backends.md, protocol 2).

It is the stub agent framework for the §9 walkthrough's real daemon: grain
starts it once per run, it starts the grain MCP server it is handed, and it
replays the steps written into the task's description:

    STUB-RUN: <shell command>        run through the run_command tool
    STUB-CALL: <tool> <json args>    call any other tool

Every tools/list and tool result is appended to $STUB_LOG_DIR/task-<id>.jsonl
(default /home/debian/live/stub-log), so a test can read what the run was
offered and what each call answered.
"""
import json
import os
import re
import subprocess
import sys

LOG_DIR = os.environ.get("STUB_LOG_DIR", "/home/debian/live/stub-log")


def main():
    req = json.load(sys.stdin)
    srv = req["mcp_server"]
    args = srv.get("args", [])
    task = args[args.index("-task") + 1] if "-task" in args else "unknown"
    env = dict(os.environ)
    for kv in srv.get("env") or []:
        k, _, v = kv.partition("=")
        env[k] = v
    os.makedirs(LOG_DIR, exist_ok=True)
    log = open(os.path.join(LOG_DIR, "task-%s.jsonl" % task), "a")

    p = subprocess.Popen([srv["command"]] + args, stdin=subprocess.PIPE,
                         stdout=subprocess.PIPE, env=env, text=True)
    next_id = [0]

    def rpc(method, params=None, notify=False):
        msg = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            msg["params"] = params
        if not notify:
            next_id[0] += 1
            msg["id"] = next_id[0]
        p.stdin.write(json.dumps(msg) + "\n")
        p.stdin.flush()
        if notify:
            return None
        while True:
            line = p.stdout.readline()
            if not line:
                raise RuntimeError("mcp server closed stdout")
            resp = json.loads(line)
            if resp.get("id") == msg["id"]:
                return resp

    rpc("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                       "clientInfo": {"name": "stub", "version": "1"}})
    rpc("notifications/initialized", notify=True)
    tools = [t["name"] for t in rpc("tools/list")["result"]["tools"]]
    log.write(json.dumps({"tools": sorted(tools), "prompt": req["prompt"]}) + "\n")

    # The prompt carries the whole conversation; only the steps after the
    # last STUB-BEGIN marker (the newest message) are replayed.
    lines = req["prompt"].splitlines()
    begins = [i for i, l in enumerate(lines) if l.rstrip().endswith("STUB-BEGIN")]
    if begins:
        lines = lines[begins[-1]:]
    calls = []
    for line in lines:
        m = re.match(r"\s*STUB-RUN:\s*(.*)$", line)
        if m:
            name, arguments = "run_command", {"command": m.group(1), "description": "stub step"}
        else:
            m = re.match(r"\s*STUB-CALL:\s*(\S+)\s*(.*)$", line)
            if not m:
                continue
            name, arguments = m.group(1), json.loads(m.group(2) or "{}")
        resp = rpc("tools/call", {"name": name, "arguments": arguments})
        res = resp.get("result") or {}
        text = "".join(c.get("text", "") for c in res.get("content", []))
        if "error" in resp:
            text, res = json.dumps(resp["error"]), {"isError": True}
        is_err = bool(res.get("isError"))
        log.write(json.dumps({"call": name, "arguments": arguments, "is_error": is_err, "text": text}) + "\n")
        log.flush()
        calls.append({"name": name, "arguments": arguments, "text": text, "is_error": is_err})
        if name == "propose_config_change" and not is_err:
            break  # the tool ends the turn
    p.stdin.close()
    p.wait(timeout=30)
    json.dump({"protocol_version": 2, "result": {
        "final_text": "stub finished %d steps" % len(calls),
        "tool_calls": calls,
        "transcript": "\n".join("%s -> %s" % (c["name"], c["text"][:400]) for c in calls)}}, sys.stdout)


if __name__ == "__main__":
    main()
