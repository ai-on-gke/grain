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

# hook_merge_gates.py: scripted agent run by fake-agy.sh against the run's real
# grain-sandbox MCP server. Only the model provider is stubbed: git pushes,
# review comments, branch scopes and merge gates run through the real daemon,
# gitproxy and GitHub simulator.
import json
import os
import subprocess
import sys
import time

cfg_path, task_id, prompt_path, out_path = sys.argv[1:5]
walk_dir = os.environ["WALK_DIR"]
raw = open(prompt_path).read()
prompt = raw
for line in raw.splitlines():
    try:
        ev = json.loads(line)
        c = ev.get("message", {}).get("content")
        if isinstance(c, str) and c:
            prompt = c
            break
    except Exception:
        pass

cfg = json.load(open(cfg_path))["mcpServers"]["grain-sandbox"]
env = os.environ.copy()
env.update(cfg.get("env", {}))
proc = subprocess.Popen(
    [cfg["command"]] + cfg.get("args", []),
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    text=True,
    env=env,
)
rid = 0


def rpc(method, params=None):
    global rid
    rid += 1
    proc.stdin.write(
        json.dumps({"jsonrpc": "2.0", "id": rid, "method": method, "params": params or {}}) + "\n"
    )
    proc.stdin.flush()
    while True:
        line = proc.stdout.readline()
        if not line:
            return None
        m = json.loads(line)
        if m.get("id") == rid:
            return m


rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "walk", "version": "1"}})
proc.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
proc.stdin.flush()


def tool(name, args):
    r = rpc("tools/call", {"name": name, "arguments": args}) or {}
    res = r.get("result") or r.get("error") or {}
    text = "".join(c.get("text", "") for c in res.get("content", [])) if isinstance(res, dict) else str(res)
    return {"isError": res.get("isError", "error" in r), "text": text[:3000]}


def sh(label, cmd):
    return tool("run_command", {"command": cmd + " 2>&1; echo exit=$?", "toolSummary": label})


out = {"taskId": task_id}

if "IMPL_MARKER_FULL_GATES" in prompt:
    out["role"] = "impl_full_gates"
    out["push"] = sh(
        "Push boundary and trigger files",
        "cd work && mkdir -p pkg/gitproxy config/secrets docs && "
        "echo 'package gitproxy' > pkg/gitproxy/proxy.go && "
        "echo 'token: plain' > config/secrets/prod.yml && "
        "echo '# Plan' > docs/plan.md && "
        "git add . && git -c user.name=walk -c user.email=walk@example.com commit -m 'feat: touch boundary and triggers\n\nTouches trust boundary and two trigger templates.' && "
        "git push origin HEAD",
    )
    out["note"] = tool("comment_on_issue", {"comment": "Pushed full-gates change", "toolSummary": "Done"})
elif "IMPL_MARKER_POST_MERGE" in prompt:
    out["role"] = "impl_post_merge"
    out["push"] = sh(
        "Push post-merge test files",
        "cd work && mkdir -p pkg/gitproxy config/secrets && "
        "echo 'package gitproxy // 2' > pkg/gitproxy/proxy2.go && "
        "echo 'token: plain2' > config/secrets/prod2.yml && "
        "git add . && git -c user.name=walk -c user.email=walk@example.com commit -m 'feat: change for partial override\n\nTouches boundary and secrets trigger.' && "
        "git push origin HEAD",
    )
    out["note"] = tool("comment_on_issue", {"comment": "Pushed post-merge change", "toolSummary": "Done"})
elif "IMPL_MARKER_SEC_INPLACE" in prompt:
    out["role"] = "impl_sec_inplace"
    out["push"] = sh(
        "Push boundary change for in-place security fix",
        "cd work && mkdir -p pkg/gitproxy && "
        "echo 'package gitproxy // 3' > pkg/gitproxy/proxy3.go && "
        "git add . && git -c user.name=walk -c user.email=walk@example.com commit -m 'feat: boundary change\n\nSecurity review will fix in place.' && "
        "git push origin HEAD",
    )
    out["note"] = tool("comment_on_issue", {"comment": "Pushed boundary change", "toolSummary": "Done"})
else:
    open(os.path.join(walk_dir, "runs", f"started-{task_id}"), "w").write("1\n")
    hold_all = os.path.join(walk_dir, "hold-reviews")
    allow_me = os.path.join(walk_dir, f"allow-{task_id}")
    deadline = time.time() + 120
    while os.path.exists(hold_all) and not os.path.exists(allow_me) and time.time() < deadline:
        time.sleep(0.2)

    out["branch"] = sh("Check branch", "cd work && git symbolic-ref --short HEAD")
    out["prompt_has_push_instruction"] = (
        "Push your change to a new branch" in prompt or "git push origin" in prompt
    )

    if "Push any fixes to" in prompt:
        out["role"] = "security_review"
        inplace_marker = os.path.join(walk_dir, "inplace-commit-review-id")
        want_id = open(inplace_marker).read().strip() if os.path.exists(inplace_marker) else ""
        if want_id == task_id:
            out["inplace_push"] = sh(
                "Commit fix on reviewed branch",
                "cd work && git fetch origin && mkdir -p notes && echo 'reviewed in place' > notes/sec-fix.txt && "
                "git add notes/sec-fix.txt && "
                "git -c user.name=sec -c user.email=sec@example.com commit -m 'fix: apply security review fix in place\n\nCommitted on the change branch.' && "
                "git push origin HEAD",
            )
        out["note"] = tool("comment_on_issue", {"comment": "No findings", "toolSummary": "Clean security review"})
    elif "trigger `secrets`" in prompt:
        out["role"] = "trigger_sensitive"
        out["incremental"] = "This is an incremental review." in prompt
        if not out["incremental"]:
            out["push_attempt"] = sh(
                "Attempt unauthorized push",
                "cd work && git -c user.name=rev -c user.email=rev@example.com commit --allow-empty -m 'should be refused' && "
                "git push origin HEAD",
            )
            out["pr_open_attempt"] = sh("Attempt PR open", "cd work && grain pr open")
            sh("Reset local commit", "cd work && git reset --hard HEAD~1")
            check_p2 = sh("Check prod2", "test -f work/config/secrets/prod2.yml; echo rc=$?")
            if "rc=0" in check_p2["text"]:
                out["finding"] = sh(
                    "Post post-merge finding",
                    'cd work && grain pr review-comment "post-merge leaked token" --path config/secrets/prod2.yml --line 1',
                )
            else:
                out["finding"] = sh(
                    "Post blocking finding",
                    'cd work && grain pr review-comment "plaintext token" --path config/secrets/prod.yml --line 1',
                )
        else:
            out["note"] = tool("comment_on_issue", {"comment": "No findings", "toolSummary": "Clean incremental review"})
    else:
        out["role"] = "trigger_planner"
        out["note"] = tool("comment_on_issue", {"comment": "No findings", "toolSummary": "Clean planner review"})

json.dump(out, open(out_path, "w"), indent=2)
proc.stdin.close()
