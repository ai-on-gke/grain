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

"""Writes rules.json, the 2026-11-14 fake-agy.py's script for walk.sh: what
each targeted review and author run does, in order. Review rules come first:
a review's prompt quotes its author's title. Run from anywhere:
python3 docs/test-runs/2026-11-18-review-gates-feedback/gen-rules.py"""

import json
import os
import shlex

GIT = "git -c user.name=Walk -c user.email=walk@example.com"


def run(desc, cmd):
    return {"tool": "run_command", "args": {"description": desc, "command": cmd}}


def rc(body, path=None, line=None):
    """A review comment through the in-sandbox `grain pr review-comment`."""
    cmd = "cd work && grain pr review-comment " + shlex.quote(body)
    if path:
        cmd += " --path %s --line %d" % (path, line)
    return run("posting a review comment", cmd + " 2>&1")


def review(*calls):
    return {"kind": "mcp_sequence", "response": "review done", "calls": list(calls)}


def push(desc, edits, msg, force=False):
    flag = "-f " if force else ""
    return {"kind": "mcp_sequence", "calls": [run(desc,
        "cd work && git fetch -q origin && (git pull -q --ff-only origin $(git rev-parse --abbrev-ref HEAD) 2>/dev/null || true) && "
        + edits + " && git add -A && " + GIT + " commit -qm " + shlex.quote(msg)
        + " && git push -q " + flag + "origin HEAD:$(git rev-parse --abbrev-ref HEAD) 2>&1; git log --oneline -1")]}


def touch(*paths):
    return " && ".join("echo '// %s' >> %s" % ("walk", p) for p in paths)


PAY = ("payments/pay.go", 3)
LED = ("ledger/ledger.go", 3)
long_q = lambda i: "L%02d: " % i + ("the charge path in payments/pay.go should say who owns rounding and why it is safe; " * 3)

rules = []
authors = []
# A1-A3: one finding each, accepted in the UI. A5-A7: one finding each, a
# hold standing when it is accepted. A8: the agent-token attempt's target.
for tag in ["A1", "A2", "A3", "A5", "A6", "A7", "A8"]:
    rules.append({"match": r"Targeted review \(.*WALK-%s " % tag, "actions": [
        review(rc("%s: Charge should reject a negative amount -- intended?" % tag, *PAY))]})
    paths = ["payments/pay.go"] + (["frozen/f.go"] if tag == "A5" else [])
    authors.append({"match": "WALK-%s " % tag, "actions": [push("author " + tag, touch(*paths), "feat: %s\n\nWalk head." % tag.lower())]})
# A4: payments and ledger, one targeted review serving both triggers.
rules.append({"match": r"Targeted review \(.*WALK-A4 ", "actions": [review(
    rc("A4-pay: Charge rounds half-up -- confirm with finance?", *PAY),
    rc("A4-led: Post never checks the account exists?", *LED))]})
authors.append({"match": "WALK-A4 ", "actions": [push("author A4", touch("payments/pay.go", "ledger/ledger.go"), "feat: a4\n\nWalk head.")]})
# A9: the 422 fallback; its second review only reads its prompt.
rules.append({"match": r"Targeted review \(.*WALK-A9 ", "actions": [
    review(rc("Q9: should Charge log the amount it refuses?", *PAY)),
    review()]})
authors.append({"match": "WALK-A9 ", "actions": [
    push("author A9 head 1", touch("payments/pay.go"), "feat: a9\n\nHead 1."),
    push("author A9 head 2", touch("payments/pay.go"), "feat: a9 head 2\n\nAfter the owner's answer.")]})
# A10: the question cap: R1 fifteen long questions (answered), R2 two
# (answered), R3 three (left open), then a person's force-push -> R4.
rules.append({"match": r"Targeted review \(.*WALK-A10 ", "actions": [
    review(*[rc(long_q(i), "payments/pay.go", 3) for i in range(1, 16)]),
    review(rc("R2a: does head 2 keep the rounding owner?", *PAY), rc("R2b: is the new comment accurate?", *PAY)),
    review(rc("R3a: OPEN who signs off on refunds?", *PAY), rc("R3b: OPEN is the cap configurable?", *PAY),
           rc("R3c: OPEN does head 3 need a changelog?")),
    review()]})
authors.append({"match": "WALK-A10 ", "actions": [
    push("author A10 head 1", touch("payments/pay.go"), "feat: a10\n\nHead 1."),
    push("author A10 head 2", touch("payments/pay.go"), "feat: a10 head 2\n\nHead 2."),
    push("author A10 head 3", touch("payments/pay.go"), "feat: a10 head 3\n\nHead 3.")]})
# The agent-token attempt: a run that calls the route with its own token.
rules.append({"match": "WALK-AGENT ", "actions": [{"kind": "mcp_sequence", "calls": [
    run("an agent tries to accept findings",
        "grain api POST /api/tasks/$(cat /home/debian/walk-rg/a8-id 2>/dev/null || echo 0)/accept-findings "
        "--body '{\"gate\":\"targeted-review\",\"reason\":\"agent thinks it is fine\"}' 2>&1; echo exit=$?"),
    {"tool": "nothing_needed", "args": {"reason": "agent attempt recorded"}}]}]})

json.dump(rules + authors, open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "rules.json"), "w"), indent=1)
