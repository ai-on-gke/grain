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

"""docs/test-plans/2026-11-10.md §6 item 3: real MCP clients.

Calls the paged list tools through every MCP surface a seeded daemon offers
and records each tool result's size against the ~10 KB tool-result budget:

  * POST /mcp as a task (X-Grain-Acting-Task, what a sandbox's api_* tools
    reach) and as a gma_ external-agent credential;
  * POST /api/mcp (the controller surface): grain_explore_scope's
    description of the paging parameters, then grain_invoke;
  * a stdio `grain mcp` session (the workstation roster).

Tokens are read from the files named on the command line and never printed.
"""

import argparse
import json
import os
import subprocess
import urllib.request

CAP = 10240


def rpc_http(url, headers, method, params, rid):
    body = json.dumps({"jsonrpc": "2.0", "id": rid, "method": method, "params": params}).encode()
    hdrs = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    hdrs.update(headers)
    req = urllib.request.Request(url, data=body, headers=hdrs, method="POST")
    with urllib.request.urlopen(req, timeout=60) as resp:
        return json.loads(resp.read())


def result_text(msg):
    res = msg.get("result") or {}
    parts = res.get("content") or []
    return "".join(p.get("text", "") for p in parts if isinstance(p, dict)), bool(res.get("isError"))


def summarize(text):
    try:
        j = json.loads(text)
    except Exception:
        return {}
    if not isinstance(j, dict):
        return {"items": len(j) if isinstance(j, list) else None}
    out = {k: j.get(k) for k in ("total", "matched", "truncated") if k in j}
    out["hasNextCursor"] = bool(j.get("nextCursor"))
    for k in ("repos", "tasks", "events", "items", "runs"):
        if isinstance(j.get(k), list):
            out["items"] = len(j[k])
            break
    return out


def call_tools(label, url, headers, calls):
    rows = []
    rpc_http(url, headers, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                          "clientInfo": {"name": "paging-walk", "version": "1"}}, 1)
    for i, (tool, args) in enumerate(calls):
        msg = rpc_http(url, headers, "tools/call", {"name": tool, "arguments": args}, 10 + i)
        text, is_err = result_text(msg)
        if "error" in msg:
            text, is_err = json.dumps(msg["error"]), True
        rows.append({"surface": label, "tool": tool, "args": args, "bytes": len(text.encode()),
                     "underCap": len(text.encode()) <= CAP, "isError": is_err,
                     "error": text[:200] if is_err else None, **summarize(text)})
    return rows


def stdio_session(grain, env, calls):
    msgs = [{"jsonrpc": "2.0", "id": 1, "method": "initialize",
             "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                        "clientInfo": {"name": "paging-walk", "version": "1"}}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list"}]
    for i, (tool, args) in enumerate(calls):
        msgs.append({"jsonrpc": "2.0", "id": 10 + i, "method": "tools/call",
                     "params": {"name": tool, "arguments": args}})
    inp = "".join(json.dumps(m) + "\n" for m in msgs)
    proc = subprocess.run([grain, "mcp"], input=inp, capture_output=True, text=True, env=env, timeout=120)
    by_id = {}
    for line in proc.stdout.splitlines():
        try:
            m = json.loads(line)
        except Exception:
            continue
        by_id[m.get("id")] = m
    tools = [t["name"] for t in (by_id.get(2, {}).get("result") or {}).get("tools", [])]
    rows = []
    for i, (tool, args) in enumerate(calls):
        m = by_id.get(10 + i, {})
        text, is_err = result_text(m)
        rows.append({"surface": "stdio grain mcp", "tool": tool, "args": args, "bytes": len(text.encode()),
                     "underCap": len(text.encode()) <= CAP, "isError": is_err or "error" in m,
                     "error": (text or json.dumps(m.get("error")))[:200] if (is_err or "error" in m) else None,
                     **summarize(text)})
    return tools, rows


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--code-file", required=True, help="the daemon's local-api-token file")
    ap.add_argument("--tokens-file", required=True, help="seed.go's -tokens-out")
    ap.add_argument("--grain", default="/home/debian/bin/grain")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()
    code = open(args.code_file).read().strip()
    tokens = json.load(open(args.tokens_file))

    list_calls = [
        ("api_deployment_reachable_repos", {}),
        ("api_deployment_reachable_repos", {"query": {"push": "true", "owner": "acme", "limit": "5"}}),
        ("api_deployment_reachable_repos", {"query": {"q": "SERVICE-01"}}),
        ("api_list_tasks", {}),
        ("api_list_tasks", {"query": {"fields": "summary"}}),
        ("api_list_tasks", {"query": {"limit": "3", "q": "triage"}}),
        ("api_events", {}),
        ("api_events", {"query": {"since": "0", "limit": "5"}}),
    ]
    rows = []
    # POST /mcp is the external-agent surface: one api_* tool per route.
    surfaces = [
        ("POST /mcp as gma_ full reader", {"Authorization": f"Bearer {tokens['full']}"}),
        ("POST /mcp as gma_ acme/widgets", {"Authorization": f"Bearer {tokens['widgets']}"}),
    ]
    for label, hdrs in surfaces:
        rows += call_tools(label, args.url + "/mcp", hdrs, list_calls)

    # POST /api/mcp is the controller surface a dispatched task's sandbox
    # MCP reaches with its task identity (X-Grain-Acting-Task): what
    # grain_explore_scope says of the paging parameters, then the same reads
    # through grain_invoke.
    explore = {}
    controllers = [
        ("POST /api/mcp as task 9000", {"Authorization": f"Bearer {code}", "X-Grain-Acting-Task": "9000",
                                        "X-Grain-Acting-Reason": "paging walkthrough"}),
        ("POST /api/mcp as task 9001 (acme/widgets only)", {"Authorization": f"Bearer {code}",
                                                           "X-Grain-Acting-Task": "9001",
                                                           "X-Grain-Acting-Reason": "paging walkthrough"}),
        ("POST /api/mcp as operator (access code)", {"Authorization": f"Bearer {code}"}),
    ]
    invoke_calls = [("grain_invoke", {"tool": t, "arguments": a, "reason": "paging walkthrough"}) for t, a in list_calls]
    for label, hdrs in controllers:
        rpc_http(args.url + "/api/mcp", hdrs, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                                             "clientInfo": {"name": "paging-walk", "version": "1"}}, 1)
        for scope, word in [("repos", "reachable"), ("tasks", "list"), ("tasks", "events")]:
            msg = rpc_http(args.url + "/api/mcp", hdrs, "tools/call",
                           {"name": "grain_explore_scope", "arguments": {"scope": scope, "tool": word}}, 2)
            text, is_err = result_text(msg)
            explore[f"{label}: {scope}/{word}"] = {
                "bytes": len(text.encode()), "isError": is_err,
                "mentions": {p: (f'"{p}"' in text) for p in ("limit", "cursor", "q", "since", "push", "owner", "host")},
            }
        rows += call_tools(label + " grain_invoke", args.url + "/api/mcp", hdrs, invoke_calls)

    env = dict(os.environ, GRAIN_SERVER=args.url, GRAIN_TOKEN=code, HOME="/tmp/paging-mcp-home")
    os.makedirs(env["HOME"], exist_ok=True)
    stdio_tools, stdio_rows = stdio_session(args.grain, env, [
        ("list_grain_tasks", {}), ("list_grain_tasks", {"repo": "acme/widgets"}),
        ("grain_inbox", {}), ("ci_runs", {"repo": "acme/widgets"}),
    ])
    rows += stdio_rows

    with open(args.out, "w") as f:
        json.dump({"rows": rows, "explore": explore, "stdio_tools": stdio_tools}, f, indent=2)
    for r in rows:
        print(f"{r['surface']:<45} {r['tool']:<32} {json.dumps(r['args'])[:70]:<70} {r['bytes']:>6} B"
              f" {'ok' if r['underCap'] else 'OVER'} {'ERR ' + (r['error'] or '')[:80] if r['isError'] else ''}")
    print(json.dumps(explore))
    print("stdio tools:", " ".join(stdio_tools))


if __name__ == "__main__":
    main()
