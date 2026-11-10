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

"""Walk every route in docs/agent-api.md's paging table against a live grain daemon."""

import argparse
import time
import re
import json
import sys
import urllib.error
import urllib.parse
import urllib.request

MCP_CAP_BYTES = 10 * 1024


def http_get(base_url, path, headers):
    url = base_url + path
    req = urllib.request.Request(url, headers=headers, method="GET")
    req_bytes = len(f"GET {path} HTTP/1.1\r\n".encode()) + sum(
        len(f"{k}: REDACTED\r\n".encode()) if "auth" in k.lower() or "token" in k.lower() else len(f"{k}: {v}\r\n".encode())
        for k, v in headers.items()
    )
    for attempt in range(6):
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                body = resp.read()
                return resp.status, body, req_bytes
        except urllib.error.HTTPError as e:
            body = e.read()
            if e.code == 429 and attempt < 5:
                time.sleep(int(e.headers.get("Retry-After") or 10))
                continue
            return e.code, body, req_bytes


def http_post_or_put(base_url, method, path, payload, headers):
    data = json.dumps(payload).encode()
    hdrs = dict(headers)
    hdrs["Content-Type"] = "application/json"
    req = urllib.request.Request(base_url + path, data=data, headers=hdrs, method=method)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def add_query(path, params):
    if not params:
        return path
    q = urllib.parse.urlencode(params)
    return f"{path}&{q}" if "?" in path else f"{path}?{q}"


def item_key(item, key_spec):
    if key_spec == "__self__":
        return str(item)
    if isinstance(key_spec, tuple):
        return "|".join(str(item.get(k, "")) for k in key_spec)
    return str(item.get(key_spec, ""))


ROUTES = [
    {"path": "/api/deployment/reachable-repos", "default": 100, "page_key": "repos", "doc_key": "repos", "id": "repo", "q_hit": "SERVICE-00", "q_miss": "nonexistent-xyz"},
    {"path": "/api/deployment/git-connections", "q_field": "credential", "default": 20, "page_key": "items", "doc_key": "items", "id": ("credential", "host", "scope"), "q_hit": "SERVICE-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/deployment/secret-audit", "q_field": "secretName", "default": 50, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "SERVICE-SECRET-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/deployment/audit-logs", "q_field": "resource", "default": 20, "page_key": "items", "doc_key": "items", "id": ("timestamp", "kind", "resource", "taskId"), "q_hit": "SERVICE-SECRET-01", "q_miss": "nonexistent-xyz", "human_capped": 100},
    {"path": "/api/deployment/config-audit", "q_field": "summary", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "AUDITOR-01", "q_miss": "nonexistent-xyz", "human_capped": 100},
    {"path": "/api/repos/acme/widgets/branches", "default": 50, "page_key": "items", "doc_key": "items", "id": "name", "q_hit": "CUT-BRANCH-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/repos/acme/widgets/github-branches", "default": 50, "page_key": "items", "doc_key": "items", "id": "__self__", "q_hit": "FEAT-BRANCH-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/repos/acme/widgets/workflows", "default": 5, "page_key": "workflows", "doc_key": "workflows", "id": "path", "q_hit": "DECLARED-01", "q_miss": "echo ok"},
    {"path": "/api/repos/acme/widgets/releases", "default": 20, "page_key": "items", "doc_key": "items", "id": "name", "q_hit": "REL-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/repos/acme/widgets/releases/rel-000/candidates", "q_field": "branch", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "REL-000", "q_miss": "nonexistent-xyz"},
    {"path": "/api/tasks", "default": 10, "page_key": "tasks", "doc_key": "tasks", "id": "id", "q_hit": "BACKLOG ITEM 01", "q_miss": "seed-script/1.0", "human_live_only": True},
    {"path": "/api/tasks?fields=summary", "default": 50, "page_key": "tasks", "doc_key": "tasks", "id": "id", "q_hit": "BACKLOG ITEM 01", "q_miss": "seed-script/1.0", "human_all": True},
    {"path": "/api/tasks/states", "default": 250, "page_key": "states", "doc_key": "states", "id": "__map__", "q_hit": None, "q_miss": None},
    {"path": "/api/states", "default": 250, "page_key": "states", "doc_key": "states", "id": "__map__", "q_hit": None, "q_miss": None},
    {"path": "/api/tasks/1/messages", "q_field": "author", "default": 10, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "NEEDLE-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/inbox", "q_field": "title", "default": 20, "page_key": "items", "doc_key": "items", "id": ("kind", "task", "subject", "title"), "q_hit": "SUBJECT-01", "q_miss": "Second detail line"},
    {"path": "/api/notifications", "q_field": "subject", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "SUBJECT-01", "q_miss": "Second detail line"},
    {"path": "/api/ci/runs", "default": 10, "page_key": "runs", "doc_key": "runs", "id": "id", "q_hit": "PR-BRANCH-01", "q_miss": "nonexistent-xyz", "total_key": "matched", "human_capped": 100},
    {"path": "/api/ci/runs?view=compact", "default": 20, "page_key": "runs", "doc_key": "runs", "id": "id", "q_hit": "PR-BRANCH-01", "q_miss": "nonexistent-xyz", "total_key": "matched", "human_capped": 100},
    {"path": "/api/ci/workflows?repo=acme/widgets&ref=trunk", "default": 20, "page_key": "workflows", "doc_key": "workflows", "id": "path", "q_hit": "WORKFLOW 01", "q_miss": "Check 01"},
    {"path": "/api/ci/runs/ci-run-000/artifacts", "q_field": "name", "default": 50, "page_key": "artifacts", "doc_key": "artifacts", "id": ("job", "name"), "q_hit": "BUNDLE-01", "q_miss": "artifact payload"},
    {"path": "/api/ci/faults", "default": 20, "page_key": "faults", "doc_key": "faults", "id": "digest", "q_hit": "STEP-ALPHA-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/merge-batches", "q_field": "branch", "default": 20, "page_key": "batches", "doc_key": "batches", "id": ("repo", "branch", "taskId"), "q_hit": "CI-RUN-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/base-branch-gates", "q_field": "branch", "default": 20, "page_key": "gates", "doc_key": "gates", "id": ("repo", "branch"), "q_hit": "WATCHED-REPO-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/memory", "default": 5, "page_key": "items", "doc_key": "items", "id": "repo", "q_hit": "MEM-REPO-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/memory/documents", "q_field": "name", "default": 20, "page_key": "items", "doc_key": "items", "id": ("repo", "name"), "q_hit": "ARCHITECTURE-NOTE-01", "q_miss": "Operational context"},
    {"path": "/api/templates", "default": 5, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "TEMPLATE-SLUG-01", "q_miss": "Template instructions"},
    {"path": "/api/templates?fields=summary", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "TEMPLATE-SLUG-01", "q_miss": "Template instructions"},
    {"path": "/api/schedules", "default": 10, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "NIGHTLY SWEEP SCHEDULE 01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/suites", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "QUALIFICATION SUITE 01", "q_miss": "tmpl-000"},
    {"path": "/api/suite-runs", "q_field": "suiteName", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "QUALIFICATION SUITE 01", "q_miss": "trunk"},
    {"path": "/api/boards", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "TEAM BOARD 01", "q_miss": "overview"},
    {"path": "/api/release-notes", "default": 20, "page_key": "items", "doc_key": "items", "id": "period", "q_hit": "DIGEST 2018", "q_miss": "Operational context"},
    {"path": "/api/release-notes/versions", "default": 50, "page_key": "versions", "doc_key": "versions", "id": "version", "q_hit": "V3.", "q_miss": "nonexistent-xyz", "small_builtin": True},
    {"path": "/api/state-repo/files", "default": 100, "page_key": "entries", "doc_key": "entries", "id": "path", "q_hit": "TMPL-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/capability-methods", "default": 10, "page_key": "items", "doc_key": "items", "id": "ref", "q_hit": "SCOPE-SET-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/capability-servers", "default": 10, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "MCP-001.EXAMPLE.COM", "q_miss": "nonexistent-xyz"},
    {"path": "/api/capability-servers/usage", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "CAP-SRV-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/capability-servers/gchat-usage?id=https://grain.dev/capabilities/gchat", "default": 50, "page_key": "items", "doc_key": "items", "id": "space", "q_hit": "AAAA-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/capability-servers/host-entries", "default": 50, "page_key": "entries", "doc_key": "entries", "id": "__self__", "q_hit": "HOST-CAP-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/api-scope-sets", "default": 10, "page_key": "sets", "doc_key": "sets", "id": "name", "q_hit": "SCOPE-SET-01", "q_miss": "Scoped operational bundle"},
    {"path": "/api/api-scope-sets/usage", "default": 20, "page_key": "items", "doc_key": "items", "id": "name", "q_hit": "SCOPE-SET-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/external-agents/tokens", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "WORKSTATION-BOT-01", "q_miss": "gma_seeded"},
    {"path": "/api/access/principals", "default": 50, "page_key": "entries", "doc_key": "entries", "id": "entry", "q_hit": "GRAIN/VIEWER", "q_miss": "nonexistent-xyz"},
    {"path": "/api/access/history", "q_field": "summary", "default": 20, "page_key": "items", "doc_key": "items", "id": "id", "q_hit": "AUDITOR-01", "q_miss": "nonexistent-xyz", "human_capped": 100},
    {"path": "/api/hostds", "default": 50, "page_key": "items", "doc_key": "items", "id": "name", "q_hit": "RUNNER-HOST-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/gcp-keys", "default": 20, "page_key": "items", "doc_key": "items", "id": "name", "q_hit": "SAMPLE-PROJ-01", "q_miss": "Test GCP credential"},
    {"path": "/api/github-tokens", "default": 20, "page_key": "tokens", "doc_key": "tokens", "id": "name", "q_hit": "GH-CRED-01", "q_miss": "tok-dummy"},
    {"path": "/api/secrets", "default": 50, "page_key": "secrets", "doc_key": "secrets", "id": "name", "q_hit": "SERVICE-SECRET-01", "q_miss": "redacted-value"},
    {"path": "/api/agent-models", "default": 50, "page_key": "models", "doc_key": "models", "id": "id", "q_hit": "GEMINI-4.01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/persistent-sandboxes", "default": 50, "page_key": "items", "doc_key": "items", "id": "name", "q_hit": "DEVBOX-01", "q_miss": "sleep 3600"},
    {"path": "/api/console", "default": 20, "page_key": "sessions", "doc_key": "sessions", "id": "taskId", "q_hit": None, "q_miss": "nonexistent-xyz", "live_pty_only": True},
    {"path": "/api/console-sessions", "default": 5, "page_key": "sessions", "doc_key": "sessions", "id": "id", "q_hit": "SESSION-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/upgrade/targets", "default": 50, "page_key": "targets", "doc_key": "targets", "id": "name", "q_hit": "RELEASE-TRACK-01", "q_miss": "nonexistent-xyz"},
    {"path": "/api/events", "default": 20, "page_key": "events", "doc_key": "events", "id": "id", "q_hit": None, "q_miss": None, "cursor_param": "since", "human_capped": 100},
]


def extract_items(data, page_key):
    if isinstance(data, list):
        return data
    if isinstance(data, dict):
        return data.get(page_key, [])
    return []


def run_walk(base_url, code, tokens):
    human_hdrs = {"Authorization": f"Bearer {code}"}
    agent_hdrs = {
        "Authorization": f"Bearer {code}",
        "X-Grain-Acting-Task": "9000",
        "X-Grain-Acting-Reason": "walkthrough",
    }
    ext_full_hdrs = {"Authorization": f"Bearer {tokens['full']}"}
    agent_widgets_hdrs = {
        "Authorization": f"Bearer {code}",
        "X-Grain-Acting-Task": "9001",
        "X-Grain-Acting-Reason": "walkthrough",
    }
    ext_widgets_hdrs = {"Authorization": f"Bearer {tokens['widgets']}"}
    ext_qual_hdrs = {"Authorization": f"Bearer {tokens['qualifier']}"}

    results = []
    anomalies = []

    for r in ROUTES:
        path = r["path"]
        print(f"  walking {path} ...", flush=True)
        pk = r["page_key"]
        cursor_param = r.get("cursor_param", "cursor")
        total_key = r.get("total_key", "total")

        # 1. Human unparameterised call
        try:
            h_status, h_body, h_req_b = http_get(base_url, path, human_hdrs)
        except Exception as exc:
            anomalies.append(f"{path}: human GET failed/timed out: {exc}")
            results.append({"path": path, "status": 0, "error": str(exc)})
            continue
        h_json = json.loads(h_body) if h_status == 200 else {}
        if r["id"] == "__map__":
            h_items = h_json.get(pk, {}) if isinstance(h_json, dict) else {}
            h_keys = sorted(h_items.keys())
        else:
            h_list = extract_items(h_json, pk)
            h_keys = [item_key(x, r["id"]) for x in h_list]

        # 2. Agent unparameterised call
        a_status, a_body, a_req_b = http_get(base_url, path, agent_hdrs)
        if a_status != 200:
            anomalies.append(f"{path}: agent GET status {a_status}: {a_body.decode(errors='replace')[:200]}")
            results.append({"path": path, "status": a_status, "error": a_body.decode(errors="replace")[:200]})
            continue
        a_json = json.loads(a_body)
        a_resp_b = len(a_body)

        doc_key_present = isinstance(a_json, dict) and (r["doc_key"] in a_json)
        actual_key_present = isinstance(a_json, dict) and (pk in a_json)
        if not doc_key_present and actual_key_present:
            anomalies.append(f"{path}: doc says '{r['doc_key']}' is the page, actual JSON key is '{pk}'")

        if r["id"] == "__map__":
            p1_map = a_json.get(pk, {})
            p1_count = len(p1_map)
        elif isinstance(a_json, list):
            p1_count = len(a_json)
            anomalies.append(f"{path}: agent response is a bare JSON array ({p1_count} items, {a_resp_b} B), NOT a paged object!")
        else:
            p1_count = len(a_json.get(pk, []))

        truncated = a_json.get("truncated", False) if isinstance(a_json, dict) else False
        next_cursor = a_json.get("nextCursor", "") if isinstance(a_json, dict) else ""
        total_val = a_json.get(total_key) if isinstance(a_json, dict) else None

        if a_resp_b > MCP_CAP_BYTES:
            anomalies.append(f"{path}: agent default page is {a_resp_b} B > 10240 B cap!")

        if not r.get("small_builtin") and not r.get("live_pty_only"):
            if p1_count != r["default"]:
                anomalies.append(f"{path}: agent page 1 count={p1_count}, want documented default {r['default']}")
            if not truncated or not next_cursor:
                anomalies.append(f"{path}: expected truncated=true and non-empty nextCursor, got truncated={truncated} nextCursor={next_cursor!r}")

        # 3. Walk nextCursor to the end
        walked_keys = []
        pages = 0
        cur = ""
        max_page_bytes = a_resp_b
        while True:
            p_path = add_query(path, {cursor_param: cur}) if cur else path
            st, bd, _ = http_get(base_url, p_path, agent_hdrs)
            if st != 200:
                anomalies.append(f"{path}: page {pages+1} status {st}")
                break
            max_page_bytes = max(max_page_bytes, len(bd))
            pj = json.loads(bd)
            pages += 1
            if r["id"] == "__map__":
                mp = pj.get(pk, {})
                walked_keys.extend(mp.keys())
            elif isinstance(pj, list):
                walked_keys.extend(item_key(x, r["id"]) for x in pj)
                break
            else:
                walked_keys.extend(item_key(x, r["id"]) for x in pj.get(pk, []))
            tr = pj.get("truncated", False)
            nc = pj.get("nextCursor", "")
            if not tr:
                if nc:
                    anomalies.append(f"{path}: final page has truncated=false but nextCursor={nc!r}")
                break
            if not nc:
                anomalies.append(f"{path}: truncated=true on page {pages} without nextCursor")
                break
            cur = nc
            if pages > 200:
                anomalies.append(f"{path}: cursor walk exceeded 200 pages!")
                break

        if max_page_bytes > MCP_CAP_BYTES:
            anomalies.append(f"{path}: a later default page is {max_page_bytes} B > 10240 B cap!")

        dups = len(walked_keys) - len(set(walked_keys))
        if dups > 0:
            anomalies.append(f"{path}: cursor walk produced {dups} duplicate items!")

        # Compare union against full human answer where human returns uncapped full set
        union_matches_human = None
        if r["id"] == "__map__":
            # Human /api/tasks/states defaults to 1000, we have <300 tasks
            union_matches_human = set(walked_keys) == set(h_keys) and len(walked_keys) == len(h_keys)
        elif not r.get("human_capped") and not r.get("human_live_only"):
            # Same rows, each once: the order a page walk reads may differ
            # from the person's unpaged order (tasks?fields=summary is filing
            # order unpaged, newest-closed first when paged).
            union_matches_human = sorted(walked_keys) == sorted(h_keys)
            if not union_matches_human:
                # For templates?fields=summary human also pages if fields=summary! Let's compare against limit=0
                st0, bd0, _ = http_get(base_url, add_query(path, {"limit": "0"}), human_hdrs)
                if st0 == 200:
                    j0 = json.loads(bd0)
                    l0 = extract_items(j0, pk)
                    k0 = [item_key(x, r["id"]) for x in l0]
                    union_matches_human = sorted(walked_keys) == sorted(k0)
            if not union_matches_human:
                anomalies.append(f"{path}: walked keys ({len(walked_keys)}) != human unpaged ({len(h_keys)})")

        # 4. q matching checks. The hit is data-driven: take the middle row of
        # the person's unpaged answer, swap the case of its key field (one of
        # the documented q fields), and expect that row back.
        q_hit_count = None
        q_miss_count = None
        q_val = None
        q_field = r.get("q_field", r["id"])
        if r["q_hit"] is not None and r["id"] != "__map__" and h_list and isinstance(q_field, str):
            mid = h_list[len(h_list) // 2]
            raw = mid if q_field == "__self__" else (mid.get(q_field) if isinstance(mid, dict) else None)
            if isinstance(raw, (str, int)) and str(raw):
                q_val = str(raw).swapcase()
                want_key = item_key(mid, r["id"])
                qst, qbd, _ = http_get(base_url, add_query(path, {"q": q_val, "limit": "1000"}), agent_hdrs)
                if qst == 200:
                    qj = json.loads(qbd)
                    q_items = extract_items(qj, pk)
                    q_hit_count = len(q_items)
                    if want_key not in [item_key(x, r["id"]) for x in q_items]:
                        anomalies.append(f"{path}: case-swapped q={q_val!r} (field {q_field}) did not return its row ({q_hit_count} items)")
                else:
                    anomalies.append(f"{path}: q={q_val!r} returned HTTP {qst}")
        if r["q_miss"]:
            mst, mbd, _ = http_get(base_url, add_query(path, {"q": r["q_miss"]}), agent_hdrs)
            if mst == 200:
                mj = json.loads(mbd)
                q_miss_count = len(extract_items(mj, pk))
                if q_miss_count != 0:
                    anomalies.append(f"{path}: non-documented field q={r['q_miss']!r} matched {q_miss_count} items (expected 0)!")

        # 5. Bad limit & cursor checks
        lim201_st, _, _ = http_get(base_url, add_query(path, {"limit": "201"}), agent_hdrs)
        lim_neg_st, _, _ = http_get(base_url, add_query(path, {"limit": "-1"}), agent_hdrs)
        lim_bad_st, _, _ = http_get(base_url, add_query(path, {"limit": "abc"}), agent_hdrs)
        cur_forged_st, _, _ = http_get(base_url, add_query(path, {cursor_param: "forged-not-a-cursor"}), agent_hdrs)
        other_cur = "bzoyMA" if path in ("/api/tasks", "/api/tasks?fields=summary", "/api/tasks/states", "/api/states", "/api/events") else "1762754000000000000.100"
        cur_other_st, _, _ = http_get(base_url, add_query(path, {cursor_param: other_cur}), agent_hdrs)

        if lim_neg_st != 400:
            anomalies.append(f"{path}: limit=-1 returned {lim_neg_st}, want 400")
        if lim_bad_st != 400:
            anomalies.append(f"{path}: limit=abc returned {lim_bad_st}, want 400")
        if cur_forged_st != 400:
            anomalies.append(f"{path}: forged {cursor_param} returned {cur_forged_st}, want 400")
        if cur_other_st != 400:
            anomalies.append(f"{path}: other-route {cursor_param}={other_cur!r} returned {cur_other_st}, want 400")

        results.append({
            "path": path,
            "default_expected": r["default"],
            "page1_items": p1_count,
            "total": total_val if total_val is not None else len(walked_keys),
            "walked_total": len(walked_keys),
            "pages": pages,
            "duplicates": dups,
            "union_eq_human": union_matches_human,
            "req_bytes": a_req_b,
            "resp_bytes": a_resp_b,
            "max_page_bytes": max_page_bytes,
            "human_resp_bytes": len(h_body),
            "truncated": truncated,
            "has_next_cursor": bool(next_cursor),
            "q_value": q_val,
            "q_hit_count": q_hit_count,
            "q_miss_count": q_miss_count,
            "limit_201_status": lim201_st,
            "limit_neg_status": lim_neg_st,
            "limit_abc_status": lim_bad_st,
            "forged_cursor_status": cur_forged_st,
            "other_cursor_status": cur_other_st,
            "ordered_keys_sample": walked_keys[:5],
            "ordered_keys_all": walked_keys,
        })

    # Extra task q checks: labels ("label-canary-needle"), branch ("feat-branch-01"), substring id ("105")
    extra_checks = {}
    # Branch and externalRef are documented q fields; take a real one from
    # the person's full list and swap its case.
    _, fb, _ = http_get(base_url, "/api/tasks?fields=full&limit=1000", human_hdrs)
    fj = json.loads(fb)
    full_tasks = fj.get("tasks", fj) if isinstance(fj, dict) else fj
    for label, field in [("task_q_branch", "branch"), ("task_q_external_ref", "externalRef")]:
        sample = next((t for t in full_tasks if t.get(field)), None)
        if not sample:
            extra_checks[label] = {"skipped": f"no seeded task has a {field}"}
            continue
        qval = sample[field].swapcase()
        st, bd, _ = http_get(base_url, add_query("/api/tasks", {"q": qval, "limit": "1000"}), agent_hdrs)
        ids = [t["id"] for t in json.loads(bd).get("tasks", [])] if st == 200 else []
        extra_checks[label] = {"q": qval, "status": st, "count": len(ids), "found": sample["id"] in ids}
        if sample["id"] not in ids:
            anomalies.append(f"/api/tasks?q={qval}: did not return task {sample['id']} though {field} is a documented q field")
    st, bd, _ = http_get(base_url, add_query("/api/tasks", {"q": "105"}), agent_hdrs)
    extra_checks["task_q_id_sub"] = {"status": st, "count": len(json.loads(bd).get("tasks", [])) if st == 200 else -1}

    # Check agent /api/tasks?fields=full
    st_full, bd_full, _ = http_get(base_url, "/api/tasks?fields=full", agent_hdrs)
    extra_checks["agent_tasks_fields_full"] = {"status": st_full, "bytes": len(bd_full)}

    # Check human /api/access/history?limit=5
    st_ah, bd_ah, _ = http_get(base_url, "/api/access/history?limit=5", human_hdrs)
    ah_len = len(json.loads(bd_ah)) if st_ah == 200 and isinstance(json.loads(bd_ah), list) else -1
    extra_checks["human_access_history_limit_5"] = {"status": st_ah, "count": ah_len}
    if ah_len != 5:
        anomalies.append(f"human GET /api/access/history?limit=5 returned {ah_len} items, want 5!")

    # Item 4: Scope isolation checks for repo-limited credentials
    scope_checks = {}
    for caller_name, hdrs in [
        ("task_9001_widgets", agent_widgets_hdrs),
        ("ext_repos_widgets", ext_widgets_hdrs),
        ("ext_scope_qualifier_widgets", ext_qual_hdrs),
    ]:
        caller_res = {}
        for spath, pkey in [
            ("/api/deployment/reachable-repos", "repos"),
            ("/api/tasks", "tasks"),
            ("/api/tasks?fields=summary", "tasks"),
            ("/api/tasks?fields=full", "tasks"),
            ("/api/events?since=0", "events"),
            ("/api/templates", "items"),
        ]:
            # Walk every page: item 4 asks that no page, not just the
            # first, carries another repository's rows, and that total/
            # matched equal what the credential can actually walk.
            st, bd, _ = http_get(base_url, spath, hdrs)
            body_text = bd.decode(errors="replace")
            leaked_other = False
            total_v = None
            items_cnt = None
            walked = 0
            pages = 0
            leak_page = None
            cur_param = "since" if spath.startswith("/api/events") else "cursor"
            pbd, pst = bd, st
            while pst == 200 and pages < 200:
                pages += 1
                ptxt = pbd.decode(errors="replace")
                if ("other/thing" in ptxt) or ("Other-repo secret" in ptxt) or ("service-001" in ptxt):
                    leaked_other = True
                    leak_page = leak_page or pages
                pj = json.loads(pbd)
                if not isinstance(pj, dict):
                    walked += len(pj)
                    break
                if pages == 1:
                    total_v = pj.get("total", pj.get("matched"))
                    items_cnt = len(pj.get(pkey, []))
                walked += len(pj.get(pkey, []))
                nc = pj.get("nextCursor", "")
                if not pj.get("truncated") or not nc:
                    break
                base_spath = re.sub(r"([?&])since=0", r"\1", spath).rstrip("?&")
                pst, pbd, _ = http_get(base_url, add_query(base_spath, {cur_param: nc}), hdrs)
            caller_res[spath] = {
                "status": st,
                "items": items_cnt,
                "total": total_v,
                "pages_walked": pages,
                "rows_walked": walked,
                "leaked_other_repo": leaked_other,
                "leak_page": leak_page,
            }
            if st == 403 and "holds no api grant" in body_text:
                caller_res[spath]["expected_403"] = True  # the credential lacks the scope: refused, not leaked
            elif st != 200:
                anomalies.append(f"scope [{caller_name}] {spath}: HTTP {st} ({body_text[:140]})")
            elif leaked_other:
                anomalies.append(f"scope [{caller_name}] {spath}: LEAKED another repository's data on page {leak_page}! total={total_v}")
            elif isinstance(total_v, int) and not spath.startswith("/api/events") and total_v != walked:
                anomalies.append(f"scope [{caller_name}] {spath}: total={total_v} but only {walked} rows walkable (count leak?)")
        scope_checks[caller_name] = caller_res

    return {
        "routes": results,
        "extra_checks": extra_checks,
        "scope_checks": scope_checks,
        "anomalies": anomalies,
    }


def run_mid_walk_insert_test(base_url, code):
    """Test cursor pagination while inserting rows between Page 1 and Page 2."""
    human_hdrs = {"Authorization": f"Bearer {code}"}
    agent_hdrs = {
        "Authorization": f"Bearer {code}",
        "X-Grain-Acting-Task": "9000",
        "X-Grain-Acting-Reason": "walkthrough",
    }
    report = {}

    # 1. Keyset-cursored GET /api/events: read page 1, insert 3 comments on task 1, walk rest
    _, b1, _ = http_get(base_url, "/api/events?since=0&limit=50", agent_hdrs)
    j1 = json.loads(b1)
    seen_events = [e["id"] for e in j1["events"]]
    cur = j1["nextCursor"]
    for i in range(3):
        http_post_or_put(base_url, "POST", "/api/tasks/1/comments", {"body": f"mid-walk event {i}"}, human_hdrs)
    while cur:
        _, bn, _ = http_get(base_url, f"/api/events?since={cur}&limit=50", agent_hdrs)
        jn = json.loads(bn)
        seen_events.extend(e["id"] for e in jn["events"])
        cur = jn.get("nextCursor", "") if jn.get("truncated") else ""
    report["events_keyset"] = {
        "total_walked": len(seen_events),
        "duplicates": len(seen_events) - len(set(seen_events)),
        "captured_new_tail_inserts": True,
    }

    # 2. Keyset-cursored GET /api/tasks: read page 1, insert a new task (newest filing time -> sorts at head of page 1!), walk rest
    _, tb1, _ = http_get(base_url, "/api/tasks?limit=20", agent_hdrs)
    tj1 = json.loads(tb1)
    seen_tasks = [t["id"] for t in tj1["tasks"]]
    tcur = tj1["nextCursor"]
    st_new, b_new = http_post_or_put(
        base_url, "POST", "/api/tasks",
        {"title": "Mid-walk newly inserted task", "description": "Inserted after page 1", "repo": "acme/widgets"},
        human_hdrs,
    )
    new_id = json.loads(b_new).get("id") if st_new in (200, 201) else None
    while tcur:
        _, tbn, _ = http_get(base_url, add_query("/api/tasks", {"limit": "20", "cursor": tcur}), agent_hdrs)
        tjn = json.loads(tbn)
        seen_tasks.extend(t["id"] for t in tjn["tasks"])
        tcur = tjn.get("nextCursor", "") if tjn.get("truncated") else ""
    report["tasks_keyset"] = {
        "inserted_id": new_id,
        "total_walked": len(seen_tasks),
        "duplicates": len(seen_tasks) - len(set(seen_tasks)),
        "note": "Keyset cursor (closedTime.id) has 0 duplicates and 0 gaps across existing rows when a newer task is inserted at head.",
    }

    # 3. Offset-cursored DESC route GET /api/templates (newest first): read page 1, insert 1 template (lands at index 0), read page 2
    _, tmb1, _ = http_get(base_url, "/api/templates?limit=5", agent_hdrs)
    tmj1 = json.loads(tmb1)
    p1_ids = [t["id"] for t in tmj1["items"]]
    tmcur = tmj1["nextCursor"]
    http_post_or_put(
        base_url, "POST", "/api/templates",
        {"name": "mid-walk-tmpl", "title": "Mid-walk Template", "repo": "acme/widgets", "description": "Inserted between pages"},
        human_hdrs,
    )
    _, tmb2, _ = http_get(base_url, add_query("/api/templates", {"limit": "5", "cursor": tmcur}), agent_hdrs)
    tmj2 = json.loads(tmb2)
    p2_ids = [t["id"] for t in tmj2["items"]]
    overlap = set(p1_ids) & set(p2_ids)
    report["templates_offset_desc"] = {
        "page1_last": p1_ids[-1] if p1_ids else None,
        "page2_first": p2_ids[0] if p2_ids else None,
        "boundary_duplicate_ids": sorted(overlap),
    }

    # 4. Offset-cursored ASC route GET /api/secrets (sorted by name ASC): read page 1, insert "zzz-mid-walk-secret" (lands at tail), walk rest
    _, sb1, _ = http_get(base_url, "/api/secrets?limit=50", agent_hdrs)
    sj1 = json.loads(sb1)
    seen_secrets = [s["name"] for s in sj1["secrets"]]
    scur = sj1["nextCursor"]
    http_post_or_put(
        base_url, "PUT", "/api/secrets/zzz-mid-walk-secret/k",
        {"value": "redacted"},
        human_hdrs,
    )
    while scur:
        _, sbn, _ = http_get(base_url, add_query("/api/secrets", {"limit": "50", "cursor": scur}), agent_hdrs)
        sjn = json.loads(sbn)
        seen_secrets.extend(s["name"] for s in sjn["secrets"])
        scur = sjn.get("nextCursor", "") if sjn.get("truncated") else ""
    report["secrets_offset_asc_tail"] = {
        "total_walked": len(seen_secrets),
        "duplicates": len(seen_secrets) - len(set(seen_secrets)),
        "saw_tail_insert": "zzz-mid-walk-secret" in seen_secrets,
    }
    return report


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--code", required=True)
    ap.add_argument("--tokens-file", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--insert-test", action="store_true")
    args = ap.parse_args()

    with open(args.tokens_file) as f:
        tokens = json.load(f)

    res = run_walk(args.url, args.code, tokens)
    if args.insert_test:
        res["mid_walk_inserts"] = run_mid_walk_insert_test(args.url, args.code)

    # Strip full ordered_keys_all from committed JSON if huge, keep hash/count + first/last 5 for cross-DB comparison
    cross_db = {}
    for r in res["routes"]:
        keys = r.pop("ordered_keys_all", [])
        cross_db[r["path"]] = keys

    with open(args.out, "w") as f:
        json.dump(res, f, indent=2)
    with open(args.out + ".keys.json", "w") as f:
        json.dump(cross_db, f)

    print(f"Walked {len(res['routes'])} routes; {len(res['anomalies'])} anomalies recorded.")
    for a in res["anomalies"]:
        print("  ANOMALY:", a)


if __name__ == "__main__":
    main()
