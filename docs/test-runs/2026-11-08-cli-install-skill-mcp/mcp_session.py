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

"""§2 item 2 (second half): a live stdio `grain mcp -context <name>` session.

Spawns the real CLI with stdin/stdout pipes, speaks JSON-RPC 2.0
(initialize -> tools/list -> tools/call) and exercises all 13 external
tools against a live daemon. The process's working directory is the
"workspace" attachments are confined to.

  mcp_session.py --context narrow --repo acme/allowed --other-repo acme/secret \
      --server URL --code ACCESS_CODE [--pr acme/widgets#1]

HOME / XDG_CONFIG_HOME come from the environment (the context's own).
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

GRAIN = os.environ.get("GRAIN", "/home/debian/bin/grain")


class Session:
    def __init__(self, context, cwd):
        self.p = subprocess.Popen(
            [GRAIN, "mcp", "-context", context],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, cwd=cwd)
        self.next_id = 0

    def rpc(self, method, params=None):
        self.next_id += 1
        req = {"jsonrpc": "2.0", "id": self.next_id, "method": method}
        if params is not None:
            req["params"] = params
        self.p.stdin.write(json.dumps(req) + "\n")
        self.p.stdin.flush()
        line = self.p.stdout.readline()
        if not line:
            raise RuntimeError("grain mcp closed stdout; stderr: " + self.p.stderr.read())
        resp = json.loads(line)
        assert resp.get("jsonrpc") == "2.0" and resp.get("id") == self.next_id, resp
        return resp

    def call(self, name, args):
        resp = self.rpc("tools/call", {"name": name, "arguments": args})
        if "error" in resp:
            return True, json.dumps(resp["error"])
        res = resp["result"]
        text = "".join(c.get("text", "") for c in res.get("content", []))
        return bool(res.get("isError")), text

    def close(self):
        self.p.stdin.close()
        rc = self.p.wait(timeout=10)
        return rc, self.p.stderr.read()


RESULTS = []


def show(label, name, args, is_err, text, expect):
    ok = (expect == "ok" and not is_err) or (expect == "error" and is_err) or expect == "any"
    RESULTS.append((label, ok))
    print("### %s  [%s]" % (label, "PASS" if ok else "FAIL"))
    print("    call %s %s" % (name, json.dumps(args)[:300]))
    one = text.replace("\n", " ")
    print("    -> isError=%s %s" % (is_err, one[:600] + ("…" if len(one) > 600 else "")))
    return text


def check(s, label, name, args, expect="ok"):
    is_err, text = s.call(name, args)
    for _ in range(4):
        # The daemon holds a gma_ token to 60 requests/minute; ride it out
        # rather than recording a refusal the check is not about.
        if not (is_err and "rate limit" in text):
            break
        print("    (rate limited; retrying in 20s)")
        time.sleep(20)
        is_err, text = s.call(name, args)
    return show(label, name, args, is_err, text, expect)


def operator(server, code, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(server + path, data=data, method=method, headers={
        "Authorization": "Bearer " + code, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def first_id(text):
    try:
        v = json.loads(text)
    except ValueError:
        return ""
    for k in ("id", "task_id", "taskId"):
        if isinstance(v, dict) and v.get(k):
            return str(v[k])
    if isinstance(v, dict) and isinstance(v.get("task"), dict):
        return str(v["task"].get("id", ""))
    return ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--context", required=True)
    ap.add_argument("--repo", required=True)
    ap.add_argument("--other-repo", required=True)
    ap.add_argument("--server", required=True)
    ap.add_argument("--code", required=True)
    ap.add_argument("--pr", default="")
    ap.add_argument("--own-pr", default="", help="a pull request a grain task already owns")
    a = ap.parse_args()

    ws = tempfile.mkdtemp(prefix="grain-mcp-ws-")
    outside = tempfile.mkdtemp(prefix="grain-mcp-outside-")
    with open(os.path.join(ws, "notes.txt"), "w") as f:
        f.write("workspace attachment: retry budget 5\n")
    os.makedirs(os.path.join(ws, "docs"))
    with open(os.path.join(ws, "docs", "design.md"), "w") as f:
        f.write("# design\n")
    with open(os.path.join(outside, "secret.txt"), "w") as f:
        f.write("outside the workspace\n")
    os.symlink(os.path.join(outside, "secret.txt"), os.path.join(ws, "link-out.txt"))
    os.symlink(outside, os.path.join(ws, "linkdir"))
    for rel, body in ((".env", "API_KEY=x\n"), (".env.local", "API_KEY=y\n"),
                      (".ssh/id_ed25519", "key\n"), (".git/config", "[core]\n"),
                      (".aws/credentials", "[default]\n"), (".gnupg/x", "g\n"),
                      ("sub/.env.production", "z\n")):
        os.makedirs(os.path.dirname(os.path.join(ws, rel)) or ws, exist_ok=True)
        with open(os.path.join(ws, rel), "w") as f:
            f.write(body)
    xdg = os.environ.get("XDG_CONFIG_HOME", os.path.join(os.environ["HOME"], ".config"))
    cred = os.path.join(xdg, "grain", "credentials", a.context)
    os.symlink(os.path.join(xdg, "grain"), os.path.join(ws, "grain-config"))

    s = Session(a.context, ws)
    init = s.rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                "clientInfo": {"name": "walkthrough", "version": "0"}})
    print("== initialize -> " + json.dumps(init["result"]))
    s.p.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
    tools = s.rpc("tools/list")["result"]["tools"]
    names = sorted(t["name"] for t in tools)
    print("== tools/list (%d): %s" % (len(names), ", ".join(names)))
    RESULTS.append(("13 tools advertised", len(names) == 13))
    unknown = s.rpc("tools/call", {"name": "api_force_merge", "arguments": {}})
    print("== unknown tool -> " + json.dumps(unknown)[:300])
    bad = s.rpc("no/such/method")
    print("== unknown method -> " + json.dumps(bad)[:300])
    RESULTS.append(("unknown method is a JSON-RPC error", "error" in bad))

    print("\n== propose_task / read_grain_task / list_grain_tasks")
    t1 = first_id(check(s, "propose in scope", "propose_task",
                        {"title": "MCP walkthrough T1", "body": "filed over stdio", "repo": a.repo}))
    check(s, "propose to the out-of-scope repo", "propose_task",
          {"title": "MCP walkthrough leak", "body": "x", "repo": a.other_repo},
          expect="error" if a.context != "dev" else "any")
    check(s, "propose with no title", "propose_task", {"body": "x"}, expect="error")
    t2 = first_id(check(s, "propose T2 depending on T1", "propose_task",
                        {"title": "MCP walkthrough T2", "body": "after T1", "repo": a.repo,
                         "depends_on": [t1] if t1 else []}))
    print("    T1=%s T2=%s" % (t1, t2))
    check(s, "read T1", "read_grain_task", {"id": t1})
    check(s, "read unknown", "read_grain_task", {"id": "999999"}, expect="error")
    check(s, "list (repo)", "list_grain_tasks", {"repo": a.repo, "limit": 5})
    check(s, "list (other repo)", "list_grain_tasks", {"repo": a.other_repo, "limit": 5}, expect="any")
    check(s, "list search", "list_grain_tasks", {"search": "MCP walkthrough", "limit": 5})

    print("\n== wait_for_task across sliced intervals (operator closes T1 during slice 3)")
    st, _ = operator(a.server, a.code, "POST", "/api/tasks/%s/approve" % t1, {})
    print("    operator approve T1 -> %d" % st)

    def close_later():
        time.sleep(5)
        st2, body = operator(a.server, a.code, "POST", "/api/tasks/%s/close" % t1, {})
        print("    [operator closed T1 at +5s -> %d]" % st2)
    threading.Thread(target=close_later, daemon=True).start()
    start = time.time()
    for i in range(1, 6):
        is_err, text = s.call("wait_for_task", {"task_id": t1, "until": "closed", "timeout_seconds": 2})
        show("wait slice %d (+%.1fs)" % (i, time.time() - start), "wait_for_task",
             {"task_id": t1, "until": "closed", "timeout_seconds": 2}, is_err, text, "ok")
        if '"resume":true' not in text.replace(" ", ""):
            break
    check(s, "wait on unknown task", "wait_for_task", {"task_id": "999999", "timeout_seconds": 2}, expect="error")

    print("\n== reply_to_task / comment_on_task attachments (workspace %s)" % ws)
    target = t2
    check(s, "comment, no attachment", "comment_on_task", {"id": target, "body": "plain comment"})
    check(s, "comment, workspace file", "comment_on_task",
          {"id": target, "body": "with notes", "attachments": ["notes.txt"]})
    check(s, "comment, nested workspace file (absolute)", "comment_on_task",
          {"id": target, "body": "with design", "attachments": [os.path.join(ws, "docs/design.md")]})
    for label, path in (("symlink escaping the workspace", "link-out.txt"),
                        ("file under a symlinked dir", "linkdir/secret.txt"),
                        ("../ traversal", "../" + os.path.basename(outside) + "/secret.txt"),
                        ("absolute outside path", os.path.join(outside, "secret.txt")),
                        (".env", ".env"), (".env.local", ".env.local"),
                        ("nested .env.production", "sub/.env.production"),
                        (".ssh key", ".ssh/id_ed25519"), (".git/config", ".git/config"),
                        (".aws/credentials", ".aws/credentials"), (".gnupg", ".gnupg/x"),
                        ("$XDG_CONFIG_HOME/grain credential (absolute)", cred),
                        ("$XDG_CONFIG_HOME/grain via an in-workspace symlink", "grain-config/credentials/" + a.context),
                        ("/etc/passwd", "/etc/passwd")):
        check(s, "comment, " + label, "comment_on_task",
              {"id": target, "body": "should be refused", "attachments": [path]}, expect="error")
    check(s, "reply, workspace file (task not parked)", "reply_to_task",
          {"id": target, "body": "reply", "attachments": ["notes.txt"]}, expect="any")
    check(s, "reply, .ssh key", "reply_to_task",
          {"id": target, "body": "reply", "attachments": [".ssh/id_ed25519"]}, expect="error")
    check(s, "reply, symlink escaping", "reply_to_task",
          {"id": target, "body": "reply", "attachments": ["link-out.txt"]}, expect="error")

    print("\n== grain_inbox / ci_runs / ci_log / merge_queue")
    check(s, "inbox", "grain_inbox", {})
    check(s, "inbox other repo", "grain_inbox", {"repo": a.other_repo}, expect="any")
    runs = check(s, "ci_runs", "ci_runs", {"repo": a.repo, "limit": 3}, expect="any")
    run_id = ""
    try:
        v = json.loads(runs)
        lst = v if isinstance(v, list) else v.get("runs", [])
        if lst:
            run_id = str(lst[0].get("id") or lst[0].get("run_id") or "")
    except (ValueError, AttributeError):
        pass
    check(s, "ci_runs other repo", "ci_runs", {"repo": a.other_repo, "limit": 3},
          expect="error" if a.context != "dev" else "any")
    if run_id:
        check(s, "ci_log " + run_id, "ci_log", {"run_id": run_id, "tail": 5}, expect="any")
    # ci_log turns run_id/job into a ci://<repo>/<run>/<job>/log path when
    # the run is not readable as an id and a repo is named: that is where a
    # traversal could reach, and isSafeCIURISegment guards it.
    for label, args in (("run_id ../ with repo", {"repo": a.repo, "run_id": "../../etc/passwd"}),
                        ("run_id with / and repo", {"repo": a.repo, "run_id": "1/../../2"}),
                        ("job ../ with repo", {"repo": a.repo, "run_id": "424242", "job": "../../../etc/passwd"}),
                        ("job with / and repo", {"repo": a.repo, "run_id": "424242", "job": "test/../../log"}),
                        ("run_id .. with repo", {"repo": a.repo, "run_id": ".."}),
                        ("repo ../", {"repo": "../acme", "run_id": "1"}),
                        ("run_id ../ without repo", {"run_id": "../../etc/passwd"}),
                        ("url-encoded traversal", {"repo": a.repo, "run_id": "%2e%2e%2fsecret"})):
        check(s, "ci_log traversal: " + label, "ci_log", args, expect="error")
    if run_id:
        # On a readable run, job/step are name filters over its jobs, never a path.
        check(s, "ci_log job filter ../ on a readable run (name filter: no match)", "ci_log",
              {"run_id": run_id, "job": "../../../etc/passwd"}, expect="any")
    check(s, "merge_queue", "merge_queue", {"repo": a.repo}, expect="any")
    # By design the queue is narrowed (fsScope.PermitsCI), not refused: an
    # out-of-scope repo reads as an empty view.
    check(s, "merge_queue other repo (narrowed to nothing)", "merge_queue", {"repo": a.other_repo}, expect="any")

    print("\n== fs_read / fs_grep")
    check(s, "fs_read task record", "fs_read", {"uri": "task://%s/record.md" % t2, "lines": 15}, expect="any")
    check(s, "fs_read task list", "fs_read", {"uri": "task://%s" % t2, "list": True}, expect="any")
    check(s, "fs_grep task record", "fs_grep", {"uri": "task://%s" % t2, "pattern": "MCP walkthrough"}, expect="any")
    check(s, "fs_read local file:// path", "fs_read", {"uri": "file:///etc/passwd"}, expect="error")
    check(s, "fs_read bare local path", "fs_read", {"uri": "/etc/passwd"}, expect="error")
    check(s, "fs_read config:// (deployment-wide)", "fs_read", {"uri": "config://"}, expect="any")
    check(s, "fs_read task:// traversal", "fs_read", {"uri": "task://%s/../../etc/passwd" % t2}, expect="error")

    print("\n== adopt_pull_request")
    if a.pr:
        check(s, "adopt " + a.pr, "adopt_pull_request", {"pr": a.pr}, expect="any")
        check(s, "adopt again (same answer)", "adopt_pull_request", {"pr": a.pr}, expect="any")
    if a.own_pr:
        check(s, "adopt a pull request a grain task already owns", "adopt_pull_request", {"pr": a.own_pr}, expect="error")
    check(s, "adopt out-of-scope PR", "adopt_pull_request", {"pr": a.other_repo + "#1"},
          expect="error" if a.context != "dev" else "any")
    check(s, "adopt malformed", "adopt_pull_request", {"pr": "not-a-pr"}, expect="error")

    rc, err = s.close()
    print("\n== stdin closed -> grain mcp exit=%d stderr=%r" % (rc, err.strip()[:300]))
    RESULTS.append(("clean exit on stdin EOF", rc == 0 and err.strip() == ""))
    fails = [l for l, ok in RESULTS if not ok]
    print("\n== %d checks, %d unexpected: %s" % (len(RESULTS), len(fails), fails))


if __name__ == "__main__":
    main()
