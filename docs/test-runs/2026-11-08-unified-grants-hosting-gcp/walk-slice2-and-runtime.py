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

"""Live daemon walkthrough for 2026-11-08 §3 Requirements 3, 4a, and 4b."""

import json
import os
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.request

API = sys.argv[1]
SIM = sys.argv[2]
TOK = open(sys.argv[3]).read().strip()
WALK_DIR = sys.argv[4]
KEY_PEM = open(sys.argv[5]).read()


def req(method, path, body=None, base=API):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Authorization": f"Bearer {TOK}"}
    if data is not None:
        headers["Content-Type"] = "application/json"
    r = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(r, timeout=15) as resp:
            raw = resp.read().decode()
            return resp.status, (json.loads(raw) if raw.strip() else None)
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"raw": raw.strip()}


def banner(title):
    print(f"\n==================== {title} ====================", flush=True)


def setup():
    banner("0. Configure Live Daemon, Fake GitHub Repos & Secrets")
    for r in ("acme/app", "acme/widgets", "acme/docs", "acme/site"):
        code, _ = req("POST", "/api/repos", {"repo": r})
        print(f"POST /api/repos {r} -> {code}")
    code, _ = req("PUT", "/api/github-tokens/default", {"value": "walk-pat-token"})
    print(f"PUT /api/github-tokens/default -> {code}")
    # Clear deployment default capabilities so Slice 2 capability assertions are exact
    code, cur = req("GET", "/api/settings")
    code, _ = req("PUT", "/api/settings", {
        "pollInterval": cur.get("pollInterval", "2s"),
        "githubHost": cur.get("githubHost", "127.0.0.1:4250"),
        "defaultCapabilities": [],
    })
    print(f"PUT /api/settings (clear defaultCapabilities) -> {code}")
    code, _ = req("PUT", "/api/config", {
        "agentBackends": [{
            "id": "ag-walk",
            "name": "Walk Agent",
            "framework": "antigravity",
            "cliPath": os.path.join(WALK_DIR, "fake-agy"),
            "models": ["fake-model"],
        }],
    })
    print(f"PUT /api/config (ag-walk) -> {code}")
    code, _ = req("PUT", "/api/agent-backends/ag-walk/credential", {"value": "walk-fake-key"})
    print(f"PUT /api/agent-backends/ag-walk/credential -> {code}")
    # Seed GitHub App credentials for github#sandbox-repo
    c1, _ = req("PUT", "/api/secrets/github-app/app-id", {"value": "77"})
    c2, _ = req("PUT", "/api/secrets/github-app/private-key", {"value": KEY_PEM})
    print(f"PUT /api/secrets/github-app/{{app-id,private-key}} -> {c1}, {c2}")


def wait_task_done(tid, timeout_s=30):
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        code, d = req("GET", f"/api/tasks/{tid}")
        st = d.get("state")
        attempts = d.get("attempts") or []
        if st not in ("queued", "running") and attempts:
            return d
        time.sleep(0.4)
    _, d = req("GET", f"/api/tasks/{tid}")
    return d


def test_slice2_crud_and_checks():
    banner("1. Slice 2 POST /api/tasks & PATCH /api/tasks/{id} with grants: [...]")
    code, created = req("POST", "/api/tasks", {
        "title": "slice2-canonical-create",
        "description": "Created via canonical grants list",
        "grants": [
            {"capability": "https://grain.dev/capabilities/api#tasks:read", "with": {"repo": "acme/app"}},
            {"capability": "https://grain.dev/capabilities/api#ci:read"},
            {"capability": "https://grain.dev/capabilities/api#task-admin"},
            {"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/app", "base": "release"}},
            {"capability": "https://grain.dev/capabilities/repo#write", "with": {"repo": "acme/site", "base": "release"}},
            {"capability": "https://grain.dev/capabilities/repo#read", "with": {"repo": "acme/docs"}},
        ],
    })
    print(f"POST /api/tasks (canonical grants) -> HTTP {code}")
    print("  created:", json.dumps({
        k: created.get(k) for k in ("id", "repo", "base", "reads", "writes", "capabilities")
    }, sort_keys=True))
    tid = created["id"]

    # Edit grants with ONLY repo#read -> capabilities and writes must stay intact!
    code, edited_repo = req("PATCH", f"/api/tasks/{tid}", {
        "grants": [
            {"capability": "https://grain.dev/capabilities/repo#read", "with": {"repo": "acme/widgets"}},
        ],
    })
    print(f"PATCH /api/tasks/{tid} (only repo#read) -> HTTP {code}")
    print("  after repo-only edit:", json.dumps({
        k: edited_repo.get(k) for k in ("repo", "base", "reads", "writes", "capabilities")
    }, sort_keys=True))

    # Edit grants with ONLY api#* -> reads and writes must stay intact!
    code, edited_cap = req("PATCH", f"/api/tasks/{tid}", {
        "grants": [
            {"capability": "https://grain.dev/capabilities/api#ci:write", "with": {"repo": "acme/app"}},
        ],
    })
    print(f"PATCH /api/tasks/{tid} (only api#ci:write) -> HTTP {code}")
    print("  after cap-only edit:", json.dumps({
        k: edited_cap.get(k) for k in ("repo", "base", "reads", "writes", "capabilities")
    }, sort_keys=True))

    banner("2. Slice 2 Mixed-Spelling Refusal Matrix (400 Expected)")
    mixed_cases = [
        ("POST mix capabilities + api#*", "POST", "/api/tasks", {
            "title": "mix1", "description": "x", "repo": "acme/app",
            "capabilities": ["api:ci:read"],
            "grants": [{"capability": "https://grain.dev/capabilities/api#tasks:read"}],
        }),
        ("POST mix empty capabilities:[] + api#*", "POST", "/api/tasks", {
            "title": "mix2", "description": "x", "repo": "acme/app",
            "capabilities": [],
            "grants": [{"capability": "https://grain.dev/capabilities/api#tasks:read"}],
        }),
        ("POST mix capabilities + empty grants:[]", "POST", "/api/tasks", {
            "title": "mix3", "description": "x", "repo": "acme/app",
            "capabilities": ["api:ci:read"],
            "grants": [],
        }),
        ("POST mix reads + repo#read", "POST", "/api/tasks", {
            "title": "mix4", "description": "x", "repo": "acme/app",
            "reads": ["acme/docs"],
            "grants": [{"capability": "https://grain.dev/capabilities/repo#read", "with": {"repo": "acme/widgets"}}],
        }),
        ("POST mix empty reads:[] + repo#read", "POST", "/api/tasks", {
            "title": "mix5", "description": "x", "repo": "acme/app",
            "reads": [],
            "grants": [{"capability": "https://grain.dev/capabilities/repo#read", "with": {"repo": "acme/widgets"}}],
        }),
        ("POST mix writes + repo#write", "POST", "/api/tasks", {
            "title": "mix6", "description": "x", "repo": "acme/app",
            "writes": [{"repo": "acme/site", "base": "main"}],
            "grants": [{"capability": "https://grain.dev/capabilities/repo#write", "with": {"repo": "acme/widgets", "base": "main"}}],
        }),
        ("POST mix empty writes:[] + repo#write", "POST", "/api/tasks", {
            "title": "mix7", "description": "x", "repo": "acme/app",
            "writes": [],
            "grants": [{"capability": "https://grain.dev/capabilities/repo#write", "with": {"repo": "acme/widgets", "base": "main"}}],
        }),
        ("POST mix repo + repo#target", "POST", "/api/tasks", {
            "title": "mix8", "description": "x",
            "repo": "acme/app",
            "grants": [{"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/widgets", "base": "main"}}],
        }),
        ("POST mix base + repo#target", "POST", "/api/tasks", {
            "title": "mix9", "description": "x",
            "base": "develop",
            "grants": [{"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/app", "base": "main"}}],
        }),
        ("PATCH repo#target on existing task", "PATCH", f"/api/tasks/{tid}", {
            "grants": [{"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/widgets", "base": "main"}}],
        }),
        ("PATCH mix capabilities + empty grants:[]", "PATCH", f"/api/tasks/{tid}", {
            "capabilities": ["api:ci:read"],
            "grants": [],
        }),
    ]
    for label, method, path, payload in mixed_cases:
        c, resp = req(method, path, payload)
        summary = resp.get("error") if isinstance(resp, dict) and "error" in resp else {
            k: resp.get(k) for k in ("id", "repo", "base", "reads", "writes", "capabilities") if isinstance(resp, dict) and k in resp
        }
        print(f"  [{label}] -> HTTP {c}: {summary}")

    banner("3. GET /api/capability-methods & POST /api/capability-grants/check")
    c_m, methods = req("GET", "/api/capability-methods")
    by_server = {}
    items = methods if isinstance(methods, list) else methods.get("methods", methods.get("servers", []))
    if isinstance(methods, dict) and not items:
        print(f"GET /api/capability-methods -> HTTP {c_m} keys={list(methods.keys())}")
    for m in (items if isinstance(items, list) else []):
        cap = m.get("capability") or m.get("id") or ""
        by_server.setdefault(cap.split("#")[0], []).append(cap)
    print(f"GET /api/capability-methods -> HTTP {c_m}, servers={sorted(by_server.keys())}")
    for srv_id in ("https://grain.dev/capabilities/api", "https://grain.dev/capabilities/repo", "https://grain.dev/capabilities/github"):
        sample = sorted(by_server.get(srv_id, []))
        print(f"  {srv_id}: count={len(sample)} sample={sample[:8]}")

    c_chk, chk = req("POST", "/api/capability-grants/check", {
        "grants": [
            {"capability": "https://grain.dev/capabilities/api#tasks:read"},
            {"capability": "https://grain.dev/capabilities/api#tasks:read", "with": {"repo": "acme/app"}},
            {"capability": "https://grain.dev/capabilities/api#task-admin"},
            {"capability": "https://grain.dev/capabilities/api#tasks:read", "with": {"repo": "acme/app", "extra": 1}},
            {"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/app", "base": "main"}},
            {"capability": "https://grain.dev/capabilities/repo#write", "with": {"repo": "acme/site", "base": "release"}},
            {"capability": "https://grain.dev/capabilities/repo#read", "with": {"repo": "acme/docs"}},
            {"capability": "https://grain.dev/capabilities/github#sandbox-repo"},
        ],
        "subject": {"repo": "acme/app"},
    })
    print(f"POST /api/capability-grants/check -> HTTP {c_chk}")
    for row in (chk if isinstance(chk, list) else chk.get("grants", [chk])):
        print(" ", json.dumps(row, sort_keys=True))


def write_propose_step(calls):
    calls_path = os.path.join(WALK_DIR, "propose-calls.json")
    with open(calls_path, "w") as f:
        json.dump(calls, f)
    step_path = os.path.join(WALK_DIR, "step.sh")
    with open(step_path, "w") as f:
        f.write(f"""#!/bin/sh
[ -f "{WALK_DIR}/armed" ] || exit 0
rm -f "{WALK_DIR}/armed"
python3 - "$HOME/.gemini/config/mcp_config.json" "{calls_path}" "{WALK_DIR}/stream.jsonl" "{WALK_DIR}/propose-results.json" <<'PY'
import json, subprocess, sys
mcp_cfg, calls_file, stream_file, out_file = sys.argv[1:5]
cfg = json.load(open(mcp_cfg))["mcpServers"]["grain-sandbox"]
p = subprocess.Popen([cfg["command"]] + cfg["args"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
def send(m):
    p.stdin.write(json.dumps(m) + "\\n"); p.stdin.flush()
def recv(want):
    while True:
        line = p.stdout.readline()
        if not line:
            return None
        msg = json.loads(line)
        if msg.get("id") == want:
            return msg
send({{"jsonrpc":"2.0","id":1,"method":"initialize","params":{{"protocolVersion":"2025-03-26","capabilities":{{}},"clientInfo":{{"name":"walk","version":"0"}}}}}})
recv(1)
send({{"jsonrpc":"2.0","method":"notifications/initialized"}})
send({{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{{}}}})
tools_resp = recv(2)
propose_schema = None
for t in (tools_resp or {{}}).get("result", {{}}).get("tools", []):
    if t.get("name") == "propose_task":
        propose_schema = sorted((t.get("inputSchema", {{}}).get("properties") or {{}}).keys())
calls = json.load(open(calls_file))
results = [{{"propose_schema_props": propose_schema}}]
stream = open(stream_file, "a")
for idx, item in enumerate(calls, start=10):
    args = item["args"]
    send({{"jsonrpc":"2.0","id":idx,"method":"tools/call","params":{{"name":"propose_task","arguments":args}}}})
    r = recv(idx)
    res_obj = (r or {{}}).get("result") or {{}}
    err_obj = (r or {{}}).get("error")
    text = "\\n".join(c.get("text","") for c in res_obj.get("content", []))
    is_err = bool(res_obj.get("isError")) or (err_obj is not None)
    results.append({{"label": item["label"], "isError": is_err, "text": text, "rpcError": err_obj}})
    if not is_err:
        tname = "mcp_grain-sandbox_propose_task"
        stream.write(json.dumps({{"event":"step_update","step_update":{{"conversation_id":"fake-agy-walk","step_index":2*idx,"state":"DONE","step_type":"agent_response"}}}}) + "\\n")
        for st, extra in (("ACTIVE", {{}}), ("DONE", {{"output": text}})):
            info = {{"name": tname, "parameters": args}}
            info.update(extra)
            stream.write(json.dumps({{"event":"step_update","step_update":{{"conversation_id":"fake-agy-walk","step_index":2*idx+1,"state":st,"step_type":"tool","tool_name":tname,"tool_info":info}}}}) + "\\n")
stream.close()
json.dump(results, open(out_file, "w"), indent=2)
p.stdin.close(); p.terminate()
PY
""")
    os.chmod(step_path, 0o755)
    open(os.path.join(WALK_DIR, "armed"), "w").close()


def test_slice2_proposals():
    banner("4. Dispatched Parent -> Child Proposal Equivalence & Escalation Matrix")

    # Direction A (batch 1): Legacy parent -> Canonical child proposals (<= 4 per parent to stay below DefaultAutoSubmitFanOut=5)
    c, p1 = req("POST", "/api/tasks", {
        "title": "parent-legacy-batch1",
        "description": "Holds legacy api:tasks:read@acme/app + writes acme/app@main",
        "repo": "acme/widgets",
        "agentBackendId": "ag-walk",
        "autoMerge": True,
        "autoApproveProposals": True,
        "capabilities": ["api:tasks:read@acme/app"],
        "writes": [{"repo": "acme/app", "base": "main"}],
    })
    p1_id = p1["id"]
    res_file = os.path.join(WALK_DIR, "propose-results.json")
    if os.path.exists(res_file):
        os.remove(res_file)
    write_propose_step([
        {"label": "A0-mixed-grants-and-capabilities-refused", "args": {
            "title": "child-A0-mixed-refused",
            "body": "Mixes grants and capabilities for capability grants",
            "capabilities": ["api:tasks:read@acme/app"],
            "grants": [
                {"capability": "https://grain.dev/capabilities/api#tasks:read", "with": {"repo": "acme/app"}},
            ],
        }},
        {"label": "A1-equiv-canonical-grants", "args": {
            "title": "child-A1-equiv-canonical",
            "body": "Canonical grants equivalent to legacy parent",
            "auto_merge": True,
            "grants": [
                {"capability": "https://grain.dev/capabilities/api#tasks:read", "with": {"repo": "acme/app"}},
                {"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/app", "base": "main"}},
            ],
        }},
        {"label": "A2-widen-scope-canonical", "args": {
            "title": "child-A2-widen-scope",
            "body": "Widens tasks:read -> tasks:write",
            "auto_merge": True,
            "grants": [
                {"capability": "https://grain.dev/capabilities/api#tasks:write", "with": {"repo": "acme/app"}},
                {"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/app", "base": "main"}},
            ],
        }},
        {"label": "A3-widen-api-repo-canonical", "args": {
            "title": "child-A3-widen-api-repo",
            "body": "Widens @acme/app -> unscoped",
            "auto_merge": True,
            "grants": [
                {"capability": "https://grain.dev/capabilities/api#tasks:read"},
                {"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/app", "base": "main"}},
            ],
        }},
    ])
    req("POST", f"/api/tasks/{p1_id}/approve", {})
    done1 = wait_task_done(p1_id)
    print(f"Legacy parent batch 1 task #{p1_id} finished: state={done1.get('state')}")
    if os.path.exists(res_file):
        for r in json.load(open(res_file)):
            print("  MCP:", json.dumps(r, sort_keys=True))

    # Direction A (batch 2): Legacy parent -> Write/base widening + capabilities-object form
    c, p1b = req("POST", "/api/tasks", {
        "title": "parent-legacy-batch2",
        "description": "Holds legacy api:tasks:read@acme/app + writes acme/app@main",
        "repo": "acme/widgets",
        "agentBackendId": "ag-walk",
        "autoMerge": True,
        "autoApproveProposals": True,
        "capabilities": ["api:tasks:read@acme/app"],
        "writes": [{"repo": "acme/app", "base": "main"}],
    })
    p1b_id = p1b["id"]
    if os.path.exists(res_file):
        os.remove(res_file)
    write_propose_step([
        {"label": "A4-widen-write-repo-canonical", "args": {
            "title": "child-A4-widen-write-repo",
            "body": "Widens write repo to acme/site",
            "auto_merge": True,
            "grants": [
                {"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/app", "base": "main"}},
                {"capability": "https://grain.dev/capabilities/repo#write", "with": {"repo": "acme/site", "base": "main"}},
            ],
        }},
        {"label": "A5-widen-base-canonical", "args": {
            "title": "child-A5-widen-base",
            "body": "Widens target base main -> release",
            "auto_merge": True,
            "grants": [
                {"capability": "https://grain.dev/capabilities/api#tasks:read", "with": {"repo": "acme/app"}},
                {"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/app", "base": "release"}},
            ],
        }},
        {"label": "A6-alt-capabilities-object", "args": {
            "title": "child-A6-capabilities-obj",
            "body": "Canonical api#tasks:read via capabilities + legacy repo/branch",
            "repo": "acme/app",
            "branch": "main",
            "auto_merge": True,
            "capabilities": [
                {"capability": "https://grain.dev/capabilities/api#tasks:read", "with": {"repo": "acme/app"}},
            ],
        }},
    ])
    req("POST", f"/api/tasks/{p1b_id}/approve", {})
    done1b = wait_task_done(p1b_id)
    print(f"Legacy parent batch 2 task #{p1b_id} finished: state={done1b.get('state')}")
    if os.path.exists(res_file):
        for r in json.load(open(res_file)):
            print("  MCP:", json.dumps(r, sort_keys=True))

    # Direction B: Canonical parent -> Legacy child proposals
    c, p2 = req("POST", "/api/tasks", {
        "title": "parent-canonical",
        "description": "Created with canonical grants list",
        "agentBackendId": "ag-walk",
        "autoMerge": True,
        "autoApproveProposals": True,
        "grants": [
            {"capability": "https://grain.dev/capabilities/api#tasks:read", "with": {"repo": "acme/app"}},
            {"capability": "https://grain.dev/capabilities/repo#target", "with": {"repo": "acme/widgets", "base": ""}},
            {"capability": "https://grain.dev/capabilities/repo#write", "with": {"repo": "acme/app", "base": "main"}},
        ],
    })
    p2_id = p2["id"]
    if os.path.exists(res_file):
        os.remove(res_file)
    write_propose_step([
        {"label": "B1-equiv-legacy", "args": {
            "title": "child-B1-equiv-legacy",
            "body": "Legacy spelling equivalent to canonical parent",
            "repo": "acme/app",
            "branch": "main",
            "auto_merge": True,
            "capabilities": ["api:tasks:read@acme/app"],
        }},
        {"label": "B2-widen-scope-legacy", "args": {
            "title": "child-B2-widen-scope-legacy",
            "body": "Widens scope in legacy spelling",
            "repo": "acme/app",
            "branch": "main",
            "auto_merge": True,
            "capabilities": ["api:tasks:write@acme/app"],
        }},
        {"label": "B3-widen-repo-legacy", "args": {
            "title": "child-B3-widen-repo-legacy",
            "body": "Widens @acme/app -> unscoped in legacy spelling",
            "repo": "acme/app",
            "branch": "main",
            "auto_merge": True,
            "capabilities": ["api:tasks:read"],
        }},
        {"label": "B4-widen-base-legacy", "args": {
            "title": "child-B4-widen-base-legacy",
            "body": "Widens target base main -> release in legacy spelling",
            "repo": "acme/app",
            "branch": "release",
            "auto_merge": True,
            "capabilities": ["api:tasks:read@acme/app"],
        }},
    ])
    req("POST", f"/api/tasks/{p2_id}/approve", {})
    done2 = wait_task_done(p2_id)
    print(f"Canonical parent task #{p2_id} finished: state={done2.get('state')}")
    if os.path.exists(res_file):
        for r in json.load(open(res_file)):
            print("  MCP:", json.dumps(r, sort_keys=True))

    # Inspect all filed child tasks (close any queued ones first or inspect immediately)
    _, tasks_resp = req("GET", "/api/tasks?limit=100")
    tasks = tasks_resp if isinstance(tasks_resp, list) else tasks_resp.get("tasks", [])
    print("\nFiled child tasks summary:")
    for t in sorted(tasks, key=lambda x: int(x["id"]) if str(x.get("id", "")).isdigit() else 0):
        if str(t.get("title", "")).startswith("child-"):
            print(" ", json.dumps({
                "id": t.get("id"),
                "title": t.get("title"),
                "state": t.get("state"),
                "approved": bool(t.get("approvedBy") or t.get("approved")),
                "approvedBy": t.get("approvedBy"),
                "autoMerge": t.get("autoMerge"),
                "repo": t.get("repo"),
                "base": t.get("base"),
                "writes": t.get("writes"),
                "capabilities": t.get("capabilities"),
            }, sort_keys=True))


def test_github_sandbox_mintpass():
    banner("5. Live github#sandbox-repo MintPass & Release Cleanup")
    probe_out = os.path.join(WALK_DIR, "sandbox-repo-probe.json")
    step_path = os.path.join(WALK_DIR, "step.sh")
    with open(step_path, "w") as f:
        f.write(f"""#!/bin/sh
[ -f "{WALK_DIR}/armed-sbx" ] || exit 0
rm -f "{WALK_DIR}/armed-sbx"
python3 - "$HOME" "{SIM}" "{probe_out}" <<'PY'
import json, os, stat, subprocess, sys, urllib.request
home, sim, out = sys.argv[1:4]
tok_path = os.path.join(home, ".github-sandbox-token")
repo_path = os.path.join(home, ".github-sandbox-repo")
tok_exists = os.path.exists(tok_path)
repo_exists = os.path.exists(repo_path)
tok_mode = oct(stat.S_IMODE(os.stat(tok_path).st_mode)) if tok_exists else None
repo_val = open(repo_path).read().strip() if repo_exists else None
tok_val = open(tok_path).read().strip() if tok_exists else ""
sim_mid = json.loads(urllib.request.urlopen(sim + "/sim/sandbox-state").read().decode())
# Verify real git push to the ephemeral sandbox repo using the placed token!
push_ok = False
if repo_val and tok_val:
    url = f"{{sim}}/{{repo_val}}.git"
    r = subprocess.run(["git", "ls-remote", url], capture_output=True, text=True)
    push_ok = (r.returncode == 0)
json.dump({{
    "tokenExists": tok_exists,
    "tokenMode": tok_mode,
    "tokenPrefix": tok_val[:12],
    "repoExists": repo_exists,
    "repoFullName": repo_val,
    "gitLsRemoteOk": push_ok,
    "simLiveDuringRun": sim_mid.get("live"),
}}, open(out, "w"), indent=2)
PY
""")
    os.chmod(step_path, 0o755)
    open(os.path.join(WALK_DIR, "armed-sbx"), "w").close()

    c, t_sbx = req("POST", "/api/tasks", {
        "title": "walk-github-sandbox-repo",
        "description": "Exercises MintPass and Release for github#sandbox-repo",
        "repo": "acme/widgets",
        "agentBackendId": "ag-walk",
        "grants": [
            {"capability": "https://grain.dev/capabilities/github#sandbox-repo", "with": {}},
        ],
    })
    sbx_id = t_sbx["id"]
    req("POST", f"/api/tasks/{sbx_id}/approve", {})
    done_sbx = wait_task_done(sbx_id)
    print(f"Task #{sbx_id} finished: state={done_sbx.get('state')}")
    if os.path.exists(probe_out):
        print("  Inside-run probe:", json.dumps(json.load(open(probe_out)), sort_keys=True))
    _, sim_after = req("GET", "/sim/sandbox-state", base=SIM)
    print("  After-run walksim state:", json.dumps(sim_after, sort_keys=True))
    c_aud, aud = req("GET", f"/api/tasks/{sbx_id}/attempts/1/capability-audit")
    print(f"  Capability audit (HTTP {c_aud}):", json.dumps(aud, sort_keys=True)[:400])


def test_hosting_inheritance():
    banner("6. Live Hosting Inheritance Chain")
    open(os.path.join(WALK_DIR, "step.sh"), "w").write("#!/bin/sh\nexit 0\n")

    # 1. Baseline: deployment=unattended, repo=empty -> unattended
    _, t0 = req("POST", "/api/tasks", {"title": "host-0-baseline", "description": "x", "repo": "acme/docs"})
    print(f"1. Baseline (deployment=unattended): task hosting = {t0.get('hosting')}")

    # 2. Repo acme/docs defaultHosting = console -> acme/docs inherits console while acme/widgets stays unattended
    c_rs, _ = req("PUT", "/api/repos/acme/docs/settings", {"defaultHosting": "console"})
    _, t1_docs = req("POST", "/api/tasks", {"title": "host-1-repo-console", "description": "x", "repo": "acme/docs"})
    _, t1_wid = req("POST", "/api/tasks", {"title": "host-1-other-repo", "description": "x", "repo": "acme/widgets"})
    print(f"2. Repo acme/docs defaultHosting=console (PUT {c_rs}) -> acme/docs={t1_docs.get('hosting')}, acme/widgets={t1_wid.get('hosting')}")
    req("PUT", "/api/repos/acme/docs/settings", {"defaultHosting": ""})

    # 3. Deployment defaultHosting = console -> acme/widgets inherits console; then reset deployment defaultHosting=unattended
    _, cur = req("GET", "/api/settings")
    req("PUT", "/api/settings", {
        "pollInterval": cur.get("pollInterval", "2s"),
        "githubHost": cur.get("githubHost", "127.0.0.1:4250"),
        "defaultCapabilities": [],
        "defaultHosting": "console",
    })
    _, t2 = req("POST", "/api/tasks", {"title": "host-2-deploy-console", "description": "x", "repo": "acme/widgets"})
    print(f"3. Deployment defaultHosting=console -> acme/widgets task hosting = {t2.get('hosting')}")
    req("PUT", "/api/settings", {
        "pollInterval": cur.get("pollInterval", "2s"),
        "githubHost": cur.get("githubHost", "127.0.0.1:4250"),
        "defaultCapabilities": [],
        "defaultHosting": "unattended",
    })

    # 4. Template on acme/widgets with hosting = console -> both POST /api/tasks {templateId} and POST /api/tasks/from-template inherit console,
    #    while explicit task.hosting = "unattended" overrides at the leaf!
    c_tp, tpl = req("POST", "/api/templates", {
        "name": "tpl-console-walk",
        "title": "Console template task",
        "description": "Template body",
        "repo": "acme/widgets",
        "hosting": "console",
    })
    tpl_id = tpl["id"]
    _, t3a = req("POST", "/api/tasks", {"templateId": tpl_id})
    _, t3b = req("POST", "/api/tasks/from-template", {"repo": "acme/widgets", "template": "tpl-console-walk"})
    _, t3c = req("POST", "/api/tasks", {"templateId": tpl_id, "hosting": "unattended"})
    print(f"4. Template(console) (HTTP {c_tp}) -> via templateId={t3a.get('hosting')}, from-template={t3b.get('hosting')}, explicit task override={t3c.get('hosting')}")

    # 5. Schedule backed by tpl-console-walk -> composition reports template source + fired task inherits console!
    c_s1, s1 = req("POST", "/api/schedules", {
        "name": "sch-inherit-tpl",
        "templateId": tpl_id,
        "repo": "acme/widgets",
        "recurrence": {"kind": "daily", "timeOfDay": "04:00"},
    })
    s1_id = s1["id"]
    _, comp1 = req("GET", f"/api/schedules/{s1_id}/composition")
    c_r1, run1 = req("POST", f"/api/schedules/{s1_id}/run", {})
    fired1_id = (run1.get("task") or {}).get("id") or run1.get("taskId") or run1.get("id")
    _, f1 = req("GET", f"/api/tasks/{fired1_id}")
    print(f"5. Schedule + Template(console): composition=(hosting={comp1.get('hosting')}, source={comp1.get('hostingSource')}) fired task #{fired1_id} hosting={f1.get('hosting')}")

    # 6. Draft schedule composition over an unattended template with schedule hosting=console (#936) + standalone schedule(console) + PATCH task override + transition!
    _, tpl_u = req("POST", "/api/templates", {
        "name": "tpl-unattended-walk",
        "title": "Unattended template task",
        "description": "Template body",
        "repo": "acme/widgets",
        "hosting": "unattended",
    })
    c_dc, draft_comp = req("POST", "/api/schedules/composition", {
        "repo": "acme/widgets",
        "templateId": tpl_u["id"],
        "hosting": "console",
    })
    c_s2, s2 = req("POST", "/api/schedules", {
        "name": "sch-standalone-console",
        "title": "Standalone scheduled console task",
        "description": "Schedule body",
        "repo": "acme/widgets",
        "hosting": "console",
        "recurrence": {"kind": "daily", "timeOfDay": "04:30"},
    })
    s2_id = s2["id"]
    _, comp2 = req("GET", f"/api/schedules/{s2_id}/composition")
    _, run2 = req("POST", f"/api/schedules/{s2_id}/run", {})
    fired2_id = (run2.get("task") or {}).get("id") or run2.get("taskId") or run2.get("id")
    _, f2 = req("GET", f"/api/tasks/{fired2_id}")
    c_patch, f2_patched = req("PATCH", f"/api/tasks/{fired2_id}", {"hosting": "unattended"})
    transitions = f2_patched.get("transitions") or []
    hosting_trs = [tr for tr in transitions if "hosting" in str(tr.get("kind", "")).lower() or "hosting" in json.dumps(tr).lower()]
    print(f"6. DraftScheduleComposition(tpl=unattended, schedule=console) -> HTTP {c_dc} hosting={draft_comp.get('hosting')} source={draft_comp.get('hostingSource')}")
    print(f"7. Standalone Schedule(console) -> composition=(hosting={comp2.get('hosting')}, source={comp2.get('hostingSource')}) fired #{fired2_id} hosting={f2.get('hosting')} -> PATCH unattended (HTTP {c_patch}) hosting={f2_patched.get('hosting')} transitions={len(hosting_trs)}")


if __name__ == "__main__":
    setup()
    test_slice2_crud_and_checks()
    test_slice2_proposals()
    test_github_sandbox_mintpass()
    test_hosting_inheritance()
