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

"""mcp-walk.py <grain> [args...]: an external MCP client over stdio.

Starts `<grain> mcp` (GRAIN_SERVER / GRAIN_DATA_DIR pick the daemon), does
the initialize handshake, lists the tools, then walks list_grain_tasks
(summary and full), grain_inbox and ci_runs from the first page to the
last by passing nextCursor back as cursor. For each walk it prints the
pages, the largest result in bytes against the 10 KB tool budget, the ids
seen, duplicates, and whether the ids seen equal the first page's total.
Run it with `python3 -B` so no __pycache__ is written.
"""
import json
import subprocess
import sys

BUDGET = 10 * 1024

proc = subprocess.Popen([sys.argv[1], "mcp", *sys.argv[2:]], stdin=subprocess.PIPE,
                        stdout=subprocess.PIPE, text=True, bufsize=1)
seq = 0


def call(method, params=None, notify=False):
    global seq
    msg = {"jsonrpc": "2.0", "method": method}
    if params is not None:
        msg["params"] = params
    if not notify:
        seq += 1
        msg["id"] = seq
    proc.stdin.write(json.dumps(msg) + "\n")
    proc.stdin.flush()
    if notify:
        return None
    while True:
        line = proc.stdout.readline()
        if not line:
            raise SystemExit("grain mcp closed its stdout")
        resp = json.loads(line)
        if resp.get("id") == seq:
            if "error" in resp:
                raise SystemExit(f"{method}: {resp['error']}")
            return resp["result"]


def tool(name, args):
    res = call("tools/call", {"name": name, "arguments": args})
    text = "".join(c.get("text", "") for c in res.get("content", []))
    return text, res.get("isError", False)


init = call("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                           "clientInfo": {"name": "paging-walk", "version": "1"}})
call("notifications/initialized", notify=True)
print("server:", init.get("serverInfo"))
names = [t["name"] for t in call("tools/list")["tools"]]
print("tools:", len(names), [n for n in names if n in ("list_grain_tasks", "grain_inbox", "ci_runs")])


def walk(label, name, args, key, ident):
    cursor, pages, biggest, seen, total = None, 0, 0, [], None
    while True:
        a = dict(args)
        if cursor:
            a["cursor"] = cursor
        text, err = tool(name, a)
        if err:
            print(f"{label}: error on page {pages + 1}: {text[:300]}")
            return
        pages += 1
        biggest = max(biggest, len(text.encode()))
        page = json.loads(text)
        if total is None:
            total = page.get("total")
        seen += [ident(r) for r in page.get(key) or []]
        cursor = page.get("nextCursor")
        if not cursor or pages > 500:
            break
    dups = len(seen) - len(set(seen))
    print(f"{label}: pages={pages} rows={len(seen)} total={total} dups={dups} "
          f"maxBytes={biggest} underBudget={biggest <= BUDGET} complete={len(set(seen)) == total}")


walk("list_grain_tasks summary", "list_grain_tasks", {}, "tasks", lambda r: r["id"])
walk("list_grain_tasks full", "list_grain_tasks", {"fields": "full"}, "tasks", lambda r: r["id"])
walk("grain_inbox", "grain_inbox", {}, "items", lambda r: json.dumps(r, sort_keys=True))
walk("ci_runs", "ci_runs", {}, "runs", lambda r: r.get("id"))
proc.stdin.close()
proc.wait(timeout=10)
