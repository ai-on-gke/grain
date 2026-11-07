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

"""A fake GitHub Enterprise Server for the 2026-11-02 §9 walkthrough.

It serves the REST API only under /api/v3 and GraphQL only at
/api/graphql, the way a GHES does, and answers 404 to /graphql,
/api/v3/graphql and any REST path outside /api/v3 -- so a client that
gets the GHES layout wrong is caught by its own log line rather than by
an answer that happens to work. Every request is logged, one JSON line
each, to --log (and stderr).

It knows one repository, acme/widgets, with one open pull request (#7,
head branch "feature", head sha HEAD_SHA) carrying one unresolved review
thread, and a merge queue on main.

  POST /__mode {"graphql401": true}  makes /api/graphql answer 401 (bad
                                     credentials) while REST keeps
                                     answering, to show the endpoint note.

Usage: fake_ghes.py --port 8443 --log requests.jsonl
"""

import argparse
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

OWNER, REPO = "acme", "widgets"
HEAD_SHA = "1111111111111111111111111111111111111111"
BASE_SHA = "2222222222222222222222222222222222222222"
STATE = {"graphql401": False, "resolved": False, "queued": False}
LOG = None


def pr(host):
    return {
        "number": 7, "id": 7007, "node_id": "PR_kwDOfake7", "state": "open", "draft": False,
        "title": "Add the widget", "body": "", "merged": False, "mergeable": True,
        "mergeable_state": "clean", "html_url": f"http://{host}/{OWNER}/{REPO}/pull/7",
        "user": {"login": "grain-bot"},
        "head": {"ref": "feature", "sha": HEAD_SHA, "label": f"{OWNER}:feature",
                 "repo": {"full_name": f"{OWNER}/{REPO}"}},
        "base": {"ref": "main", "sha": BASE_SHA, "label": f"{OWNER}:main",
                 "repo": {"full_name": f"{OWNER}/{REPO}"}},
        "created_at": "2026-11-02T05:00:00Z", "updated_at": "2026-11-02T05:00:00Z",
    }


def graphql(query, variables):
    q = " ".join(query.split())
    if "resolveReviewThread" in q:
        STATE["resolved"] = True
        return {"data": {"resolveReviewThread": {"thread": {"id": variables.get("threadId") or variables.get("id"), "isResolved": True}}}}
    if "enqueuePullRequest" in q:
        STATE["queued"] = True
        return {"data": {"enqueuePullRequest": {"mergeQueueEntry": {
            "id": "MQE_fake1", "position": 1, "state": "QUEUED", "enqueuedAt": "2026-11-02T05:20:00Z",
            "headCommit": {"oid": HEAD_SHA}}}}}
    if "reviewThreads" in q:
        return {"data": {"repository": {"pullRequest": {"reviewThreads": {
            "pageInfo": {"hasNextPage": False, "endCursor": None},
            "nodes": [{"id": "PRRT_fake1", "isResolved": STATE["resolved"], "isOutdated": False,
                       "path": "widget.go", "line": 3,
                       "comments": {"nodes": [{"databaseId": 501}]}}]}}}}}
    # Whatever else is asked of the pull request: its node id and its
    # merge-queue entry (null until enqueued).
    entry = None
    if STATE["queued"]:
        entry = {"id": "MQE_fake1", "position": 1, "state": "QUEUED",
                 "enqueuedAt": "2026-11-02T05:20:00Z", "headCommit": {"oid": HEAD_SHA}}
    return {"data": {"repository": {
        "mergeQueue": {"id": "MQ_fake", "url": f"/{OWNER}/{REPO}/queue/main"},
        "pullRequest": {"id": "PR_kwDOfake7", "number": 7, "headRefOid": HEAD_SHA,
                        "isInMergeQueue": STATE["queued"], "mergeQueueEntry": entry}}}}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def answer(self, status, payload, note=""):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        line = {"t": time.strftime("%H:%M:%S"), "method": self.command, "path": self.path, "status": status}
        if note:
            line["note"] = note
        text = json.dumps(line)
        print(text, file=sys.stderr, flush=True)
        if LOG:
            with open(LOG, "a") as f:
                f.write(text + "\n")

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n) if n else b""

    def do_POST(self):
        raw = self.body()
        path = self.path.split("?")[0]
        if path == "/__mode":
            STATE.update(json.loads(raw or b"{}"))
            return self.answer(200, STATE, "mode")
        if path == "/api/graphql":
            if STATE["graphql401"]:
                return self.answer(401, {"message": "Bad credentials", "documentation_url": "https://docs.github.com/enterprise-server@3.14/graphql"}, "forced 401")
            req = json.loads(raw or b"{}")
            q = req.get("query", "")
            op = next((w for w in ("resolveReviewThread", "enqueuePullRequest", "reviewThreads", "mergeQueue") if w in q), "other")
            return self.answer(200, graphql(q, req.get("variables") or {}), "graphql:" + op)
        if path in ("/graphql", "/api/v3/graphql"):
            return self.answer(404, {"message": "Not Found"}, "WRONG GraphQL endpoint for GHES")
        if path.startswith("/api/v3/"):
            return self.answer(404, {"message": "Not Found"}, "unscripted REST POST")
        return self.answer(404, {"message": "Not Found"}, "outside /api/v3")

    def do_PATCH(self):
        self.body()
        return self.answer(404, {"message": "Not Found"}, "unscripted")

    def do_GET(self):
        host = self.headers.get("Host", "ghes")
        full = self.path
        path = full.split("?")[0]
        if not path.startswith("/api/v3/"):
            return self.answer(404, {"message": "Not Found"}, "outside /api/v3" if path != "/graphql" else "WRONG GraphQL endpoint for GHES")
        rest = path[len("/api/v3"):]
        base = f"/repos/{OWNER}/{REPO}"
        if rest == base:
            return self.answer(200, {"full_name": f"{OWNER}/{REPO}", "name": REPO, "owner": {"login": OWNER},
                                     "default_branch": "main", "private": True, "fork": False,
                                     "permissions": {"admin": False, "push": True, "pull": True}})
        if rest == base + "/pulls":
            if "head=" in full and "feature" not in full:
                return self.answer(200, [])
            return self.answer(200, [pr(host)])
        if rest == base + "/pulls/7":
            return self.answer(200, pr(host))
        if rest in (base + "/pulls/7/reviews", base + "/pulls/7/comments", base + "/issues/7/comments",
                    base + "/pulls/7/files", base + "/pulls/7/commits"):
            if rest.endswith("/pulls/7/comments"):
                return self.answer(200, [{"id": 501, "body": "please rename this", "path": "widget.go", "line": 3,
                                          "user": {"login": "reviewer"}, "created_at": "2026-11-02T05:05:00Z",
                                          "pull_request_review_id": 900, "html_url": "x"}])
            return self.answer(200, [])
        if rest.startswith(base + "/commits/") and rest.endswith("/check-runs"):
            return self.answer(200, {"total_count": 0, "check_runs": []})
        if rest.startswith(base + "/commits/") and rest.endswith("/check-suites"):
            return self.answer(200, {"total_count": 0, "check_suites": []})
        if rest.startswith(base + "/commits/") and rest.endswith("/status"):
            return self.answer(200, {"state": "pending", "statuses": [], "sha": HEAD_SHA})
        if rest.startswith(base + "/commits/") and rest.endswith("/statuses"):
            return self.answer(200, [])
        if rest.startswith(base + "/branches/"):
            name = rest[len(base + "/branches/"):]
            sha = HEAD_SHA if name == "feature" else BASE_SHA
            return self.answer(200, {"name": name, "commit": {"sha": sha}, "protected": name == "main"})
        if rest.startswith(base + "/actions/runs"):
            return self.answer(200, {"total_count": 0, "workflow_runs": []})
        return self.answer(404, {"message": "Not Found"}, "unscripted REST GET")


def main():
    global LOG
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8443)
    ap.add_argument("--log", default="")
    a = ap.parse_args()
    LOG = a.log
    ThreadingHTTPServer(("127.0.0.1", a.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
