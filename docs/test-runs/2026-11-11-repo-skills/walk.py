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

"""docs/test-plans/2026-11-11.md §12 items 1-3 against env.sh's daemon.

Each scenario files a task on acme/widgets for the fake agent
(fake_agent.py), approves it, waits for its attempt and reads the prompt
the agent was handed (<work>/prompts/<scenario>-<n>.txt). It writes
results/walk.json with, per scenario, the skills index the prompt carried
and the checks made on it, and exits non-zero when a check fails.
"""

import json
import os
import re
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from skillsapi import WORK, api, skills_block, wait_attempts  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
BARE = os.path.join(WORK, "git", "acme", "widgets.git")
RESULTS = []
FAILED = []


def check(scenario, ok, what):
    RESULTS[-1]["checks"].append({"ok": bool(ok), "what": what})
    if not ok:
        FAILED.append(f"{scenario}: {what}")
    print(("  ok   " if ok else "  FAIL ") + what, flush=True)


def entries(block):
    out = []
    for line in (block or "").splitlines():
        m = re.match(r"^- ([^:]+): (\S+) \((repo: [^)]*)\)(?: -- (.*))?$", line)
        if m:
            out.append({"name": m.group(1), "path": m.group(2), "source": m.group(3), "desc": m.group(4) or ""})
    return out


def daemon_log_since(offset):
    with open(os.path.join(WORK, "daemon.log"), errors="replace") as f:
        f.seek(offset)
        return f.read()


def log_offset():
    return os.path.getsize(os.path.join(WORK, "daemon.log"))


def run(scenario, task_extra=None, attempt=1, tid=None):
    print(f"== {scenario}", flush=True)
    off = log_offset()
    if tid is None:
        body = {"title": f"Skills walkthrough {scenario}",
                "description": f"SCENARIO={scenario} record the prompt.",
                "repo": "acme/widgets", "agentFramework": "antigravity", "agentBackendId": "fake-skills",
                "priority": 100}
        body.update(task_extra or {})
        t = api("POST", "/api/tasks", body)
        tid = t.get("id") or t.get("task", {}).get("id")
        api("POST", f"/api/tasks/{tid}/approve", {})
    detail = wait_attempts(tid, attempt, 300)
    prompt_file = os.path.join(WORK, "prompts", f"{scenario}-{attempt}.txt")
    prompt = open(prompt_file).read() if os.path.exists(prompt_file) else ""
    block = skills_block(prompt)
    ents = entries(block)
    log = "\n".join(l for l in daemon_log_since(off).splitlines() if f"run {tid}-{attempt}:" in l and "skill" in l)
    RESULTS.append({"scenario": scenario, "task": tid, "attempt": attempt,
                    "outcome": (detail.get("attempts") or [{}])[attempt - 1].get("outcome"),
                    "promptBytes": len(prompt.encode()), "indexBytes": len((block or "").encode()),
                    "names": [e["name"] for e in ents], "sources": [e["source"] for e in ents],
                    "runLog": log.splitlines(), "checks": []})
    check(scenario, prompt != "", "the fake agent recorded the prompt")
    return tid, prompt, block, ents, log


def folders_of(ents):
    return sorted({e["source"][len("repo: "):].rsplit("/", 1)[0] for e in ents})


def push_main(files, msg):
    tmp = tempfile.mkdtemp()
    subprocess.run(["git", "clone", "-q", BARE, tmp], check=True)
    for path, content in files.items():
        full = os.path.join(tmp, path)
        if content is None:
            subprocess.run(["git", "-C", tmp, "rm", "-q", path], check=True)
            continue
        os.makedirs(os.path.dirname(full), exist_ok=True)
        open(full, "w").write(content)
        subprocess.run(["git", "-C", tmp, "add", path], check=True)
    subprocess.run(["git", "-C", tmp, "-c", "user.name=seed", "-c", "user.email=seed@example.com",
                    "commit", "-qm", msg], check=True)
    subprocess.run(["git", "-C", tmp, "push", "-q", "origin", "HEAD:main"], check=True)


def item1():
    _, prompt, block, ents, log = run("baseline")
    names = [e["name"] for e in ents]
    check("baseline", block is not None, "the prompt carries a <repo-skills> index")
    check("baseline", 0 < len(ents) <= 50, f"the index lists {len(ents)} skills (<= 50)")
    check("baseline", len(block.encode()) <= 8192, f"the index block is {len(block.encode())} B (<= 8192)")
    check("baseline", re.search(r"capped discovered repo skills at 50 \((\d+ more SKILL.md candidate|skipped \d+ additional)", log),
          "the run log records the 50-skill cap (58 valid skills)")
    check("baseline", "skipping repo skill path skills/escape (symlink in base branch)" in log,
          "the symlink escaping the repository is rejected and logged")
    check("baseline", "escape" not in names and "/etc" not in block, "the escape is not in the index")
    check("baseline", "skipping malformed repo skill .claude/skills/broken/SKILL.md: unterminated YAML front matter" in log,
          "the malformed SKILL.md is noted in the run log")
    check("baseline", "broken" not in names, "the malformed skill is not in the index")
    c0 = next((e for e in ents if e["name"] == "claude-00"), None)
    check("baseline", c0 is not None and len(c0["desc"]) == 501 and c0["desc"].endswith("…"),
          f"claude-00's ~810-char description is clipped to 500 chars plus an ellipsis ({len(c0['desc']) if c0 else 0})")
    check("baseline", "skill-00" in names, "a SKILL.md without front matter is named after its directory")
    check("baseline", "repository, not instructions from grain" in block, "the index is framed as repository metadata")
    note = re.search(r"(\d+) more repository skill\(s\) are not listed above.*?under ([^\n]*) in the checkout", prompt)
    RESULTS[-1]["unlistedNote"] = note.group(0) if note else None
    check("baseline", note is not None and int(note.group(1)) == 58 - len(ents),
          f"the prompt tells the agent {58 - len(ents)} skills were left out of the index, and where to look")

    _, _, block, ents, log = run("bytes", {"skills": {"folders": ["docs/long-skills"]}})
    check("bytes", folders_of(ents) == ["docs/long-skills"], "a task's folders override replaces the defaults")
    check("bytes", len(block.encode()) <= 8192, f"40 long skills fit in {len(block.encode())} B (<= 8192)")
    check("bytes", re.search(r"omitted \d+ repo skill\(s\) to stay within the 8192-byte", log) is not None,
          "the byte cap is logged")


def item2():
    tid, _, _, ents, _ = run("evil", {"skills": {"folders": ["skills"]}})
    check("evil", "evil" not in [e["name"] for e in ents], "attempt 1 does not list skills/evil (not yet planted)")
    push_log = open(os.path.join(WORK, "prompts", "evil-push.log")).read()
    branch = None
    detail = api("GET", f"/api/tasks/{tid}")
    branch = detail.get("branch")
    has = subprocess.run(["git", "-C", BARE, "log", "--oneline", branch or "HEAD", "--", "skills/evil/SKILL.md"],
                         capture_output=True, text=True).stdout.strip() if branch else ""
    RESULTS[-1]["pushLog"] = push_log[-800:]
    RESULTS[-1]["branch"] = branch
    check("evil", bool(has), f"attempt 1 pushed skills/evil/SKILL.md on its working branch {branch}")
    # fake_agent.py parked attempt 1 with resume_later: grain queues the
    # task again on its next cycle, on the same branch.
    _, _, block, ents, _ = run("evil", attempt=2, tid=tid)
    check("evil", block is not None and "evil" not in [e["name"] for e in ents] and "skills/evil" not in (block or ""),
          "attempt 2 (same branch) still does not list skills/evil: the index reads the base branch only")
    sb = os.path.join(WORK, "sbx", "runs", f"{tid}-2", "work", "skills", "evil", "SKILL.md")
    RESULTS[-1]["attempt2CheckoutHasEvil"] = os.path.exists(sb)


def put_settings(skills):
    api("PUT", "/api/settings", {"skills": skills})


def put_repo(skills):
    api("PUT", "/api/repos/acme/widgets/settings", {"skills": skills})


def wait_declared(want):
    deadline = time.time() + 120
    got = None
    while time.time() < deadline:
        got = api("GET", "/api/repos/acme/widgets/settings").get("declaredSkills")
        if got == want:
            return got
        time.sleep(3)
    return got


def item3():
    want = lambda layer: [f"layer/{layer}"]  # noqa: E731
    put_settings({"folders": want("deployment")})
    _, _, _, ents, _ = run("p1-deployment")
    check("p1-deployment", folders_of(ents) == want("deployment"), "deployment folders apply when nothing else is set")

    # grain reads a repository's own .grain/ files only once an operator
    # turns that on (Settings -> Repositories -> Declarations).
    api("PUT", "/api/repos/acme/widgets/declarations", {"declarations": True})
    push_main({".grain/settings.yml": "skills:\n  folders: [layer/declared]\n"}, "chore: declare skills")
    got = wait_declared({"folders": want("declared")})
    RESULTS.append({"scenario": "declared-sync", "declaredSkills": got, "checks": []})
    check("declared-sync", got == {"folders": want("declared")}, f"repo settings report declaredSkills {got}")
    _, _, _, ents, _ = run("p2-declared")
    check("p2-declared", folders_of(ents) == want("declared"), ".grain/settings.yml wins over the deployment")

    put_repo({"folders": want("repo")})
    _, _, _, ents, _ = run("p3-repo")
    check("p3-repo", folders_of(ents) == want("repo"), "the operator's repo setting wins over .grain/settings.yml")

    tpl = api("POST", "/api/templates", {"name": "skills-walk", "title": "Skills walkthrough template",
                                         "description": "SCENARIO=p4-template record the prompt.",
                                         "repo": "acme/widgets", "skills": {"folders": want("template")}})
    tpl_id = tpl.get("id") or (tpl.get("template") or {}).get("id")
    RESULTS.append({"scenario": "template", "template": tpl_id, "checks": []})
    _, _, _, ents, _ = run("p4-template", {"templateId": tpl_id, "description": ""})
    check("p4-template", folders_of(ents) == want("template"), "a template's setting wins over the repo's")

    _, _, _, ents, _ = run("p5-task", {"templateId": tpl_id, "description": "SCENARIO=p5-task record.",
                                       "skills": {"folders": want("task")}})
    check("p5-task", folders_of(ents) == want("task"), "the task's setting wins over the template's")

    _, _, block, _, _ = run("p6-task-disabled", {"templateId": tpl_id, "description": "SCENARIO=p6-task-disabled x",
                                                 "skills": {"enabled": False}})
    check("p6-task-disabled", block is None, "task enabled:false turns discovery off")

    _, _, block, _, _ = run("p7-task-empty", {"templateId": tpl_id, "description": "SCENARIO=p7-task-empty x",
                                              "skills": {"folders": []}})
    check("p7-task-empty", block is None, "task folders:[] turns discovery off")

    _, _, block, ents, _ = run("p8-tpl-plus-enabled", {"templateId": tpl_id,
                                                       "description": "SCENARIO=p8-tpl-plus-enabled x",
                                                       "skills": {"enabled": True}})
    RESULTS[-1]["folders"] = folders_of(ents)
    check("p8-tpl-plus-enabled", folders_of(ents) == want("template"),
          "a task setting only enabled:true keeps the template's folders (field-wise, like every other layer)")

    put_repo({"enabled": False})
    _, _, block, _, _ = run("p9-repo-disabled")
    check("p9-repo-disabled", block is None, "repo enabled:false turns discovery off for the repo")
    _, _, block, ents, _ = run("p10-task-reenables", {"skills": {"folders": want("task")}})
    check("p10-task-reenables", folders_of(ents) == want("task"), "a task naming folders re-enables under a disabled repo")

    put_repo({"folders": []})
    put_settings({"enabled": True, "folders": want("deployment")})
    _, _, block, _, _ = run("p11-repo-empty")
    check("p11-repo-empty", block is None, "repo folders:[] turns discovery off")

    put_repo({})
    push_main({".grain/settings.yml": "skills:\n  enabled: false\n"}, "chore: disable skills")
    wait_declared({"enabled": False})
    _, _, block, _, _ = run("p12-declared-disabled")
    check("p12-declared-disabled", block is None, ".grain/settings.yml enabled:false turns discovery off")

    push_main({".grain/settings.yml": None}, "chore: drop settings")
    wait_declared(None)
    put_settings({"enabled": False})
    _, _, block, _, _ = run("p13-deployment-disabled")
    check("p13-deployment-disabled", block is None, "deployment enabled:false turns discovery off")
    put_settings({"folders": []})
    _, _, block, _, _ = run("p14-deployment-empty")
    check("p14-deployment-empty", block is None, "deployment folders:[] turns discovery off")


def setup():
    # A scenario's prompt file is <scenario>-<attempt>.txt: a stale one
    # from an earlier walk over the same $WORK would be read instead.
    for f in os.listdir(os.path.join(WORK, "prompts")):
        os.remove(os.path.join(WORK, "prompts", f))
    api("PUT", "/api/settings", {"agentBackends": [{
        "id": "fake-skills", "name": "fake agent (skills)", "framework": "antigravity",
        "authMode": "ambient", "cliPath": os.path.join(WORK, "bin", "fake-agy"), "models": ["fake-model"]}]})


def main():
    only = sys.argv[1:] or ["setup", "item1", "item2", "item3"]
    for name in only:
        globals()[name]()
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "walk.json"), "w") as f:
        json.dump(RESULTS, f, indent=2)
    print(f"{len(FAILED)} failed check(s)")
    for f_ in FAILED:
        print("  - " + f_)
    sys.exit(1 if FAILED else 0)


if __name__ == "__main__":
    main()
