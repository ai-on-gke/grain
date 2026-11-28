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

"""Verify all Suite 1, Suite 2, and Suite 3 outputs from walk.sh."""

import json
import os
import sys

out_dir = sys.argv[1]
store = sys.argv[2]

failures = []


def check(cond, msg):
    status = "PASS" if cond else "FAIL"
    print(f"[{status}] {msg}")
    if not cond:
        failures.append(msg)


# ---------------------------------------------------------------------------
# Suite 1: Cross-task agent halting rail
# ---------------------------------------------------------------------------
print(f"\n=== VERIFY SUITE 1 ({store}) ===")
ids = json.load(open(os.path.join(out_dir, "s1-ids.json")))
MODEL_ONLY = {"park-until", "resume-later", "wait-on-park"}
ROUTE_ORDER = [
    "depends-on-attach", "depends-on-detach",
    "wait-on-block", "wait-on-park",
    "park-until", "resume-later",
    "defer", "withdraw-approval", "pause", "stop", "close",
]

# 1A: sandbox-cli (`grain api`) calls by narrow ACTING agent
acting_cli = json.load(open(os.path.join(out_dir, "s1-acting-cli.json")))
for row in acting_cli:
    label, route, rc = row["target_label"], row["route"], row["rc"]
    err = (row["stderr"] + " " + row["stdout"]).strip()
    if label in ("wider_admin", "wider_repo", "wider_parent"):
        # On sandbox-cli, model-only routes hit sandboxChannelGuard (403) first;
        # the other 8 hit agentCrossTaskRail (403 naming the missing grant/write).
        want_phrase = "model-only control action" if route in MODEL_ONLY else "cannot "
        check(rc != 0 and ("403" in err or "Forbidden" in err or want_phrase in err) and want_phrase in err,
              f"S1 CLI ACTING -> {label} {route}: refused ({want_phrase!r}) rc={rc} msg={err[:140]!r}")
    else:
        if route in MODEL_ONLY:
            check(rc != 0 and "model-only control action" in err,
                  f"S1 CLI ACTING -> {label} {route}: sandboxChannelGuard 403 rc={rc} msg={err[:140]!r}")
        else:
            check(rc == 0, f"S1 CLI ACTING -> {label} {route}: allowed rc={rc} err={err[:140]!r}")

# 1B: grain-controller MCP (`grain_invoke`) calls by narrow ACTING agent
acting_mcp_rec = json.loads(open(os.path.join(out_dir, "s1-acting-mcp.jsonl")).read().strip().splitlines()[-1])
mcp_calls = [o for o in acting_mcp_rec["outputs"] if o["server"] == "grain-controller"]
acting_mcp_targets = [
    ("wider_admin", ids["wider_admin"]),
    ("wider_repo", ids["wider_repo"]),
    ("wider_parent", ids["wider_parent"]),
    ("equal_sib_mcp", ids["equal_sib_mcp"]),
    ("child_mcp", ids["child_mcp"]),
]
check(len(mcp_calls) == len(acting_mcp_targets) * len(ROUTE_ORDER),
      f"S1 MCP ACTING call count = {len(mcp_calls)}, want {len(acting_mcp_targets) * len(ROUTE_ORDER)}")

idx = 0
for label, tid in acting_mcp_targets:
    for route in ROUTE_ORDER:
        call = mcp_calls[idx]
        idx += 1
        txt = call["text"]
        if label in ("wider_admin", "wider_repo", "wider_parent"):
            want_why = "writes to acme/gadgets" if label == "wider_repo" else "tasks:admin"
            check(call["isError"] and "403" in txt and f"task {tid}" in txt and want_why in txt,
                  f"S1 MCP ACTING -> {label} {route}: 403 ({want_why}) isError={call['isError']} txt={txt[:150]!r}")
        else:
            check(not call["isError"],
                  f"S1 MCP ACTING -> {label} {route}: 200 OK isError={call['isError']} txt={txt[:150]!r}")

# 1C: Wider targets were NOT mutated by ACTING's refused calls
for name, fn, lfn in [
    ("wider_admin", "s1-wider-admin-after.json", "s1-wider-admin-links.txt"),
    ("wider_repo", "s1-wider-repo-after.json", "s1-wider-repo-links.txt"),
    ("wider_parent", "s1-wider-parent-after.json", "s1-wider-parent-links.txt"),
]:
    t = json.load(open(os.path.join(out_dir, fn)))
    links = open(os.path.join(out_dir, lfn)).read().splitlines()
    check(t["state"] == "queued" and t.get("approved") is True and not t.get("paused") and not t.get("deferredAt"),
          f"S1 {name} untouched: state={t['state']} approved={t.get('approved')} paused={t.get('paused')} deferredAt={t.get('deferredAt')}")
    check(f"depends-on:{ids['helper_dep']}" in links,
          f"S1 {name} kept depends-on:{ids['helper_dep']} link: {links}")

# 1D: Wider parent -> narrower child (CLI + MCP) and wider child (MCP childOf exemption)
parent_cli = json.load(open(os.path.join(out_dir, "s1-parent-cli.json")))
for row in parent_cli:
    label, route, rc = row["target_label"], row["route"], row["rc"]
    err = (row["stderr"] + " " + row["stdout"]).strip()
    if route in MODEL_ONLY:
        check(rc != 0 and "model-only control action" in err,
              f"S1 CLI PARENT -> {label} {route}: sandboxChannelGuard 403 rc={rc}")
    else:
        check(rc == 0, f"S1 CLI PARENT -> {label} {route}: allowed rc={rc} err={err[:140]!r}")

parent_mcp_rec = json.loads(open(os.path.join(out_dir, "s1-parent-mcp.jsonl")).read().strip().splitlines()[-1])
parent_mcp_calls = [o for o in parent_mcp_rec["outputs"] if o["server"] == "grain-controller"]
parent_mcp_targets = [
    ("narrow_child_mcp", ids["narrow_child_mcp"]),
    ("wider_child_mcp", ids["wider_child_mcp"]),
]
check(len(parent_mcp_calls) == len(parent_mcp_targets) * len(ROUTE_ORDER),
      f"S1 MCP PARENT call count = {len(parent_mcp_calls)}, want {len(parent_mcp_targets) * len(ROUTE_ORDER)}")
idx = 0
for label, tid in parent_mcp_targets:
    for route in ROUTE_ORDER:
        call = parent_mcp_calls[idx]
        idx += 1
        check(not call["isError"],
              f"S1 MCP PARENT -> {label} {route}: 200 OK isError={call['isError']} txt={call['text'][:150]!r}")

# ---------------------------------------------------------------------------
# Suite 2: In-place security review commit attribution
# ---------------------------------------------------------------------------
print(f"\n=== VERIFY SUITE 2 ({store}) ===")

# Case A
meta_a = json.load(open(os.path.join(out_dir, "s2-caseA-meta.json")))
links_a1 = open(os.path.join(out_dir, "s2-caseA-rev1-links.txt")).read().splitlines()
aut_a = json.load(open(os.path.join(out_dir, "s2-caseA-author.json")))
rev_a2 = json.load(open(os.path.join(out_dir, "s2-caseA-rev2.json")))
pulls_a = json.load(open(os.path.join(out_dir, "s2-caseA-pulls.json")))["pulls"]
pr_a = next((p for p in pulls_a if p.get("Head") == meta_a["branch"]), {})
check(not any(l.startswith("reviewer-push:") for l in links_a1), f"S2 Case A: REV1 has no reviewer-push links: {links_a1}")
check(not any(l.startswith("reviewer-head:") for l in links_a1), f"S2 Case A: REV1 has no reviewer-head link (did not claim H1): {links_a1}")
check(aut_a.get("autoMerge") is False and not pr_a.get("Merged", False),
      f"S2 Case A: H1 did NOT clear hold or merge (autoMerge={aut_a.get('autoMerge')}, merged={pr_a.get('Merged')})")
desc_a2 = rev_a2.get("description", "")
earlier_a2 = desc_a2.split("## Earlier reviews of this pull request", 1)[-1].split("**This is an incremental review.**", 1)[0].split("## ", 1)[0] if "## Earlier reviews of this pull request" in desc_a2 else ""
check("Earlier reviews of this pull request" in desc_a2 and f"Task {meta_a['rev1']}" in earlier_a2 and "commits pushed: none" in earlier_a2,
      f"S2 Case A: REV2 lists REV1 with commits pushed: none: {earlier_a2.strip()!r}")
check(meta_a["h1"][:7] not in earlier_a2 and "caseA H1 outside commit" not in earlier_a2,
      f"S2 Case A: REV2 Earlier reviews does NOT attribute H1 ({meta_a['h1'][:12]}) to REV1")

# Case B
meta_b = json.load(open(os.path.join(out_dir, "s2-caseB-meta.json")))
links_b1 = open(os.path.join(out_dir, "s2-caseB-rev1-links.txt")).read().splitlines()
aut_b = json.load(open(os.path.join(out_dir, "s2-caseB-author.json")))
rev_b2 = json.load(open(os.path.join(out_dir, "s2-caseB-rev2.json")))
want_push_b = f"reviewer-push:{meta_b['h1']}..{meta_b['r1']}"
want_head_b = f"reviewer-head:{meta_b['r1']}"
check(want_push_b in links_b1, f"S2 Case B: gitproxy recorded {want_push_b} in {links_b1}")
check(want_head_b in links_b1, f"S2 Case B: recordReviewerHead claimed R1 ({want_head_b}), not H2 ({meta_b['h2'][:12]}): {links_b1}")
check(f"reviewer-head:{meta_b['h2']}" not in links_b1, "S2 Case B: reviewer-head is NOT H2")
check(aut_b.get("autoMerge") is False, f"S2 Case B: H2 re-held (autoMerge={aut_b.get('autoMerge')})")
desc_b2 = rev_b2.get("description", "")
earlier_b2 = desc_b2.split("## Earlier reviews of this pull request", 1)[-1].split("**This is an incremental review.**", 1)[0].split("## ", 1)[0] if "## Earlier reviews of this pull request" in desc_b2 else ""
check(meta_b["r1"][:7] in earlier_b2 and "review: caseB R1 fix" in earlier_b2,
      f"S2 Case B: REV2 Earlier reviews lists R1 ({meta_b['r1'][:12]}): {earlier_b2.strip()!r}")
check(meta_b["h1"][:7] not in earlier_b2 and "caseB H1 under review fix" not in earlier_b2,
      f"S2 Case B: REV2 Earlier reviews excludes H1 ({meta_b['h1'][:12]})")
check(meta_b["h2"][:7] not in earlier_b2 and "caseB H2 over review fix" not in earlier_b2,
      f"S2 Case B: REV2 Earlier reviews excludes H2 ({meta_b['h2'][:12]})")

# Case C
meta_c = json.load(open(os.path.join(out_dir, "s2-caseC-meta.json")))
links_c1_nonff = open(os.path.join(out_dir, "s2-caseC-rev1-links-after-nonff.txt")).read().splitlines()
nonff_log = open(os.path.join(out_dir, "s2-caseC-nonff-push.log")).read()
hook_log = open(os.path.join(out_dir, "s2-caseC-hook-push.log")).read()
links_c1 = open(os.path.join(out_dir, "s2-caseC-rev1-links.txt")).read().splitlines()
rev_c2 = json.load(open(os.path.join(out_dir, "s2-caseC-rev2.json")))
want_push_c = f"reviewer-push:{meta_c['h0']}..{meta_c['r_rej']}"
check("403" in nonff_log or "fast-forward" in nonff_log,
      f"S2 Case C (non-ff): gitproxy refused force-push rewrite: {nonff_log.strip()[:140]!r}")
check(not any(l.startswith("reviewer-push:") for l in links_c1_nonff),
      f"S2 Case C (non-ff): gitproxy recorded NO reviewer-push link for refused force-push: {links_c1_nonff}")
check("rejecting review push" in hook_log or "remote rejected" in hook_log,
      f"S2 Case C (hook): remote pre-receive rejected push: {hook_log.strip()[:140]!r}")
check(meta_c["branch_after_rej"] == meta_c["h0"],
      f"S2 Case C: remote hook rejected push (branch remained at H0 {meta_c['h0'][:12]})")
check(want_push_c in links_c1, f"S2 Case C: gitproxy recorded pre-forward {want_push_c} in {links_c1}")
check(not any(l.startswith("reviewer-head:") for l in links_c1),
      f"S2 Case C: recordReviewerHead claimed NO head for remote-rejected push: {links_c1}")
desc_c2 = rev_c2.get("description", "")
earlier_c2 = desc_c2.split("## Earlier reviews of this pull request", 1)[-1].split("**This is an incremental review.**", 1)[0].split("## ", 1)[0] if "## Earlier reviews of this pull request" in desc_c2 else ""
check("commits pushed: none" in earlier_c2 and meta_c["r_rej"][:7] not in earlier_c2 and meta_c["r_nonff"][:7] not in earlier_c2 and meta_c["h_after"][:7] not in earlier_c2,
      f"S2 Case C: REV2 Earlier reviews shows commits pushed: none (no R_nonff, R_rej, or H_after): {earlier_c2.strip()!r}")

# Case D
meta_d = json.load(open(os.path.join(out_dir, "s2-caseD-meta.json")))
links_d1 = open(os.path.join(out_dir, "s2-caseD-rev1-links.txt")).read().splitlines()
rev_d2 = json.load(open(os.path.join(out_dir, "s2-caseD-rev2.json")))
want_push_d = f"reviewer-push:{meta_d['h0']}..{meta_d['r1']}"
check(want_push_d in links_d1, f"S2 Case D: gitproxy recorded {want_push_d} in {links_d1}")
check(not any(l.startswith("reviewer-head:") for l in links_d1),
      f"S2 Case D: recordReviewerHead claimed NOTHING after force-push rewrite omitting R1: {links_d1}")
desc_d2 = rev_d2.get("description", "")
earlier_d2 = desc_d2.split("## Earlier reviews of this pull request", 1)[-1].split("**This is an incremental review.**", 1)[0].split("## ", 1)[0] if "## Earlier reviews of this pull request" in desc_d2 else ""
check("commits pushed: none" in earlier_d2 and meta_d["r1"][:7] not in earlier_d2 and meta_d["h_fp"][:7] not in earlier_d2,
      f"S2 Case D: REV2 Earlier reviews shows commits pushed: none: {earlier_d2.strip()!r}")

# ---------------------------------------------------------------------------
# Suite 3: trust-boundary/v31 (+ v32..v34) live hold triggers
# ---------------------------------------------------------------------------
print(f"\n=== VERIFY SUITE 3 ({store}) ===")
s3 = json.load(open(os.path.join(out_dir, "s3-results.json")))
for r in s3:
    key, path, want_cat, mode = r["key"], r["path"], r["wantCategory"], r["expectMode"]
    if mode == "hold":
        check(bool(r["securityHold"]) and r["securityHold"].startswith("trust-boundary/v"),
              f"S3 {key} ({path}): security-hold recorded ({r['securityHold']})")
        check(r["autoMergeAtCheck"] is False,
              f"S3 {key} ({path}): autoMerge withdrawn (autoMerge={r['autoMergeAtCheck']})")
        check(bool(r["reviewTask"]) and want_cat in r["reviewDescription"] and path in r["reviewDescription"],
              f"S3 {key} ({path}): security review {r['reviewTask']} cites category {want_cat!r} and path {path!r}")
        if want_cat == "hold-policy":
            check(bool(r["humanOnlyHold"]), f"S3 {key} ({path}): hold-policy also sets human-only-hold ({r['humanOnlyHold']})")
    else:
        check(not r["securityHold"] and not r["reviewTask"] and r["autoMergeAtCheck"] is True,
              f"S3 {key} ({path}): negative control NOT held (hold={r['securityHold']!r}, rev={r['reviewTask']!r}, autoMerge={r['autoMergeAtCheck']})")

print(f"\n=== RESULT ({store}): {len(failures)} failures ===")
if failures:
    for f in failures:
        print("  FAIL:", f)
    sys.exit(1)
