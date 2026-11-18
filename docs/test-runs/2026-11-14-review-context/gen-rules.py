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

"""Writes rules.json, fake-agy.py's script for walk.sh: what each security
review, pr-review and author run does, in order. Run from /home/debian:
python3 work/docs/test-runs/2026-11-14-review-context/gen-rules.py"""

import json

SEC = r"\*\*This is a security review\.\*\*"
PR = r"^(?!.*\*\*This is a security review\.\*\*).*You are reviewing task"
GIT = "git -c user.name=Walk -c user.email=walk@example.com"


def run(desc, cmd):
    return {"tool": "run_command", "args": {"description": desc, "command": cmd}}


def q(body, path=None, line=None):
    a = {"body": body}
    if path:
        a["path"], a["line"] = path, line
    return {"tool": "add_review_comment", "args": a}


pr_open = run("reading the PR and its threads", "cd work && grain pr open 2>&1 | head -60; echo '--- grain pr threads'; grain pr threads 2>&1 | head -80")

long_qs = [q("C%02d: " % i + ("the allow-list check in lib/f%d.go should say why this constant exists and who reads it; " % i) * 3,
             "lib/f%d.go" % i, 3) for i in range(1, 12)]

rules = [
    {"match": SEC, "actions": [
        # S1, head 1: reads the threads, three questions, a trust-boundary fix; its draft gets a 422.
        {"kind": "mcp_sequence", "hold": True, "hold_seconds": 900, "response": "S1 done", "calls": [
            pr_open,
            q("Q1: should Allow also reject a repo with an empty owner, not only evil/x?", "pkg/gitproxy/proxy.go", 3),
            q("Q2: does bumping Handler to v2 need a changelog entry for API clients?", "api/handler.go", 3),
            q("Q3: is there a test pinning the allow-list behaviour?"),
            run("pushing the review's fix commit",
                "cd work && printf '\\n// Allow reports whether repo may be proxied (reviewed by task {task}).\\n' >> pkg/gitproxy/proxy.go && git add -A && "
                "git -c user.name=Reviewer -c user.email=reviewer@example.com commit -qm 'fix: document Allow\n\nSecurity review fix.' && "
                "git push -q origin HEAD:$(git rev-parse --abbrev-ref HEAD) 2>&1; git log --oneline -1"),
        ]},
        # S2, head 2: one open question plus eleven long ones that overflow the ~2 KB cap; held in flight.
        {"kind": "mcp_sequence", "hold": True, "hold_seconds": 900, "response": "S2 done", "calls": [
            pr_open,
            q("Q4: V2 is exported from api/handler.go -- is it part of the public API?", "api/handler.go", 4),
        ] + long_qs},
        # S3, head 3 (force-push): one question, so the owner's answer sends the author back for head 4.
        {"kind": "mcp_sequence", "response": "S3 done", "calls": [
            q("Q5: head 3 rewrote history -- was anything from heads 1 and 2 dropped?", "api/handler.go", 4),
        ]},
    ]},
    {"match": PR, "actions": [
        {"kind": "mcp_sequence", "hold": True, "hold_seconds": 900, "response": "P1 done", "calls": [
            pr_open,
            q("P1: Handler now returns v2 -- is any caller pinned to v1?", "api/handler.go", 3),
        ]},
        # Later pr-reviews: read the pull request's threads, then finish.
        {"kind": "mcp_sequence", "response": "later pr-review done", "calls": [
            pr_open, {"tool": "nothing_needed", "args": {"reason": "later head: nothing new"}},
        ]},
        {"kind": "mcp_sequence", "response": "later pr-review done", "calls": [
            pr_open, {"tool": "nothing_needed", "args": {"reason": "later head: nothing new"}},
        ]},
    ]},
    {"match": "WALK-AUTHOR", "actions": [
        {"kind": "mcp_sequence", "calls": [run("author head 1",
            "cd work && printf 'package gitproxy\\n\\nfunc Allow(repo string) bool { return repo != \"\" && repo != \"evil/x\" }\\n' > pkg/gitproxy/proxy.go && "
            "printf 'package api\\n\\nfunc Handler() string { return \"v2\" }\\n' > api/handler.go && git add -A && "
            + GIT + " commit -qm 'feat: tighten the git proxy allow-list\n\nHead 1.' && git push -q origin HEAD:$(git rev-parse --abbrev-ref HEAD) 2>&1; git log --oneline -1")]},
        {"kind": "mcp_sequence", "calls": [run("author head 2 (fast-forward, 4 files)",
            "cd work && git pull -q --ff-only origin $(git rev-parse --abbrev-ref HEAD) 2>&1; echo 'func V2() {}' >> api/handler.go && for i in 1 2 3; do echo '// h2' >> lib/f$i.go; done && git add -A && "
            + GIT + " commit -qm 'feat: head 2\n\nFast-forward.' && git push -q origin HEAD:$(git rev-parse --abbrev-ref HEAD) 2>&1; git log --oneline -1")]},
        {"kind": "mcp_sequence", "calls": [run("author head 3 (force-push, rewritten from main)",
            "cd work && git fetch -q origin && git reset -q --soft origin/main && echo '// rewritten' >> api/handler.go && git add -A && "
            + GIT + " commit -qm 'feat: head 3\n\nOne squashed commit replacing heads 1 and 2.' && git push -q -f origin HEAD:$(git rev-parse --abbrev-ref HEAD) 2>&1; git log --oneline -1")]},
        {"kind": "mcp_sequence", "calls": [run("author head 4 (merge of a base that adds a trust-boundary file)",
            "cd work && git fetch -q origin && git reset -q --hard origin/$(git rev-parse --abbrev-ref HEAD) && " + GIT + " merge -q --no-edit origin/main && echo '// h4' >> api/handler.go && git add -A && "
            + GIT + " commit -qm 'feat: head 4\n\nAfter merging main.' && git push -q origin HEAD:$(git rev-parse --abbrev-ref HEAD) 2>&1; git log --oneline -3")]},
    ]},
]
json.dump(rules, open("work/docs/test-runs/2026-11-14-review-context/rules.json", "w"), indent=1)
