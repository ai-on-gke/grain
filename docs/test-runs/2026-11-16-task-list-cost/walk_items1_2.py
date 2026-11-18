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

"""Walkthrough for docs/test-plans/2026-11-16.md §2 items 1 and 2.

Against a running grain daemon (PostgreSQL or SQLite):
  - Times cold, after-write, 304 quiet polls, ?fields=full, narrowed ?state=,
    paged ?state=closed, and 20-id vs 50-id /api/tasks/summaries polls.
  - Diffs every field of every open row between GET /api/tasks and
    GET /api/tasks?fields=full (stripping description).
  - Closes and reopens tasks across states (including a blocker task while
    another task depends on it) in an interleaved loop, verifying live list,
    closed tab, ?fields=full parity and ETag invalidation on every step.
"""

import argparse
import json
import statistics
import sys
import time
import urllib.error
import urllib.request


def req(base_url, token, method, path, body=None, if_none_match=None):
    url = base_url.rstrip("/") + path
    data = None
    headers = {"Authorization": f"Bearer {token}", "Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    if if_none_match:
        headers["If-None-Match"] = if_none_match
    r = urllib.request.Request(url, data=data, headers=headers, method=method)
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(r, timeout=60) as resp:
            raw = resp.read()
            dt_ms = (time.perf_counter() - t0) * 1000.0
            return resp.status, dict(resp.headers), raw, dt_ms
    except urllib.error.HTTPError as e:
        raw = e.read()
        dt_ms = (time.perf_counter() - t0) * 1000.0
        return e.code, dict(e.headers), raw, dt_ms


def header(headers, name):
    low = name.lower()
    for k, v in headers.items():
        if k.lower() == low:
            return v
    return ""


def normalize_full_as_live(task):
    out = dict(task)
    out.pop("description", None)
    return out


def diff_open_rows(live_rows, full_rows):
    """Compare every field of every open row between live and ?fields=full."""
    full_open = [normalize_full_as_live(t) for t in full_rows if t.get("state") != "closed"]
    live_norm = [normalize_full_as_live(t) for t in live_rows]
    diffs = []
    if len(live_norm) != len(full_open):
        diffs.append(f"length mismatch: live={len(live_norm)} full_open={len(full_open)}")
    live_ids = [t["id"] for t in live_norm]
    full_ids = [t["id"] for t in full_open]
    if live_ids != full_ids:
        only_live = sorted(set(live_ids) - set(full_ids))
        only_full = sorted(set(full_ids) - set(live_ids))
        diffs.append(f"id/order mismatch: only_live={only_live[:10]} only_full={only_full[:10]}")
    by_full = {t["id"]: t for t in full_open}
    for t in live_norm:
        tid = t["id"]
        other = by_full.get(tid)
        if other is None:
            continue
        if t != other:
            keys = sorted(set(t.keys()) | set(other.keys()))
            field_diffs = []
            for k in keys:
                if t.get(k) != other.get(k):
                    # Allow tiny wall-clock jitter on Stall/relative strings only if reported explicitly.
                    field_diffs.append(f"{k}: live={t.get(k)!r} vs full={other.get(k)!r}")
            diffs.append(f"task {tid}: " + "; ".join(field_diffs))
    return diffs, len(live_norm)


def fmt_stats(samples):
    s = sorted(samples)
    p50 = statistics.median(s)
    return f"min={s[0]:.2f}ms p50={p50:.2f}ms max={s[-1]:.2f}ms (n={len(s)})"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True)
    ap.add_argument("--token", required=True)
    ap.add_argument("--label", default="store")
    args = ap.parse_args()

    print(f"=== Task List Cost & Parity Walkthrough ({args.label}) ===")

    # 1. Cold GET /api/tasks
    code, hdrs, raw, cold_ms = req(args.url, args.token, "GET", "/api/tasks")
    assert code == 200, f"cold GET /api/tasks = {code}: {raw[:300]}"
    etag = header(hdrs, "ETag")
    assert etag, "GET /api/tasks missing ETag"
    live_rows = json.loads(raw)
    print(f"cold GET /api/tasks: {cold_ms:.2f}ms ({len(live_rows)} open rows, {len(raw)} bytes, ETag={etag})")

    # Verify rich state coverage in live rows.
    state_counts = {}
    for t in live_rows:
        st = t.get("state", "")
        state_counts[st] = state_counts.get(st, 0) + 1
    print(f"open state distribution: {dict(sorted(state_counts.items()))}")
    for want_state in ("proposed", "queued", "running", "deferred", "awaiting_reply", "awaiting_submit", "failed"):
        assert state_counts.get(want_state, 0) > 0, f"missing seeded state {want_state}"

    # Verify specific realistic features hydrated in live list.
    by_id = {t["id"]: t for t in live_rows}
    assert by_id["21"].get("blockedBy") == ["1"], f"task 21 blockedBy={by_id['21'].get('blockedBy')}"
    assert not by_id["30"].get("blockedBy"), f"task 30 (closed dep 400) unexpectedly blockedBy={by_id['30'].get('blockedBy')}"
    assert by_id["130"].get("rolledOverFrom") == "4990", f"task 130 rolledOverFrom={by_id['130'].get('rolledOverFrom')}"
    assert by_id["66"].get("repairing") is True, f"task 66 repairing={by_id['66'].get('repairing')}"
    assert by_id["125"].get("repairing") is True, f"task 125 repairing={by_id['125'].get('repairing')}"
    assert by_id["191"].get("mergeQueueState"), f"task 191 mergeQueueState={by_id['191'].get('mergeQueueState')}"
    assert by_id["25"].get("paused"), f"task 25 paused={by_id['25'].get('paused')}"
    assert by_id["136"].get("deferredUntil"), f"task 136 deferredUntil missing"
    assert by_id["156"].get("questionSummary"), f"task 156 questionSummary missing"
    assert by_id["168"].get("awaitingAccess") is True, f"task 168 awaitingAccess={by_id['168'].get('awaitingAccess')}"
    assert by_id["179"].get("turnStopped") is True, f"task 179 turnStopped={by_id['179'].get('turnStopped')}"
    assert len(by_id["1"].get("reads", [])) == 3, f"task 1 reads={by_id['1'].get('reads')}"
    assert len(by_id["1"].get("capabilities", [])) >= 3, f"task 1 capabilities={by_id['1'].get('capabilities')}"
    assert by_id["1"].get("customFields", {}).get("severity"), f"task 1 customFields={by_id['1'].get('customFields')}"

    # 2. Quiet 304 polls (x10)
    quiet_ms = []
    for i in range(10):
        c, h, b, dt = req(args.url, args.token, "GET", "/api/tasks", if_none_match=etag)
        assert c == 304, f"quiet poll {i} status={c}, want 304"
        assert len(b) == 0, f"quiet poll {i} had body len {len(b)}"
        assert header(h, "ETag") == etag, f"quiet poll {i} ETag drifted"
        quiet_ms.append(dt)
    print(f"quiet 304 GET /api/tasks: {fmt_stats(quiet_ms)}")

    # 3. Initial full parity diff: GET /api/tasks vs GET /api/tasks?fields=full
    c_full, h_full, raw_full, full_cold_ms = req(args.url, args.token, "GET", "/api/tasks?fields=full")
    assert c_full == 200, f"GET /api/tasks?fields=full = {c_full}"
    full_rows = json.loads(raw_full)
    print(f"cold GET /api/tasks?fields=full: {full_cold_ms:.2f}ms ({len(full_rows)} total rows, {len(raw_full)} bytes)")

    diffs, compared = diff_open_rows(live_rows, full_rows)
    if diffs:
        print("FAIL: initial open vs ?fields=full parity mismatches:")
        for d in diffs[:20]:
            print("  -", d)
        sys.exit(1)
    print(f"PASS: initial parity check matched 100% across all {compared} open rows (every field)")

    # 4. After-write rebuilds of GET /api/tasks (x5)
    rebuild_ms = []
    for i in range(5):
        c, _, b, _ = req(args.url, args.token, "PATCH", "/api/tasks/1", {"title": f"Proposed feature 1 (rev {i})"})
        assert c == 200, f"PATCH /api/tasks/1 = {c}: {b[:200]}"
        c2, h2, b2, dt = req(args.url, args.token, "GET", "/api/tasks", if_none_match=etag)
        assert c2 == 200, f"after-write GET /api/tasks = {c2}, want 200"
        new_etag = header(h2, "ETag")
        assert new_etag and new_etag != etag, "ETag did not change after title write"
        etag = new_etag
        rebuild_ms.append(dt)
    # Restore title
    req(args.url, args.token, "PATCH", "/api/tasks/1", {"title": "Proposed feature 1"})
    print(f"after-write rebuild GET /api/tasks: {fmt_stats(rebuild_ms)}")

    # 5. After-write narrowed open-only ?state=queued,proposed,running&limit=1 (x5)
    narrowed_ms = []
    for i in range(5):
        c, _, b, _ = req(args.url, args.token, "PATCH", "/api/tasks/2", {"title": f"Proposed feature 2 (rev {i})"})
        assert c == 200, f"PATCH /api/tasks/2 = {c}: {b[:200]}"
        c2, _, b2, dt = req(args.url, args.token, "GET", "/api/tasks?state=queued,proposed,running&limit=1")
        assert c2 == 200, f"narrowed GET = {c2}: {b2[:200]}"
        rows = json.loads(b2)
        assert len(rows) == 1, f"narrowed len={len(rows)}, want 1"
        narrowed_ms.append(dt)
    req(args.url, args.token, "PATCH", "/api/tasks/2", {"title": "Proposed feature 2"})
    print(f"after-write ?state=queued,proposed,running&limit=1: {fmt_stats(narrowed_ms)}")

    # 6. After-write ?fields=full (x3) & ?state=closed&cursor=&limit=50 (cold after write + warm cached)
    full_rebuild_ms = []
    for i in range(3):
        req(args.url, args.token, "PATCH", "/api/tasks/3", {"title": f"Proposed feature 3 (rev {i})"})
        c2, _, _, dt = req(args.url, args.token, "GET", "/api/tasks?fields=full")
        assert c2 == 200
        full_rebuild_ms.append(dt)
    req(args.url, args.token, "PATCH", "/api/tasks/3", {"title": "Proposed feature 3"})
    print(f"after-write ?fields=full: {fmt_stats(full_rebuild_ms)}")

    # Closed tab: cold after write, then warm cached
    req(args.url, args.token, "PATCH", "/api/tasks/3", {"title": "Proposed feature 3"})
    c_cl1, _, b_cl1, cl_cold_ms = req(args.url, args.token, "GET", "/api/tasks?state=closed&cursor=&limit=50")
    assert c_cl1 == 200
    cl_page = json.loads(b_cl1)
    c_cl2, _, _, cl_warm_ms = req(args.url, args.token, "GET", "/api/tasks?state=closed&cursor=&limit=50")
    assert c_cl2 == 200
    print(f"GET /api/tasks?state=closed&cursor=&limit=50: cold={cl_cold_ms:.2f}ms warm={cl_warm_ms:.2f}ms (total={cl_page.get('total')}, pageLen={len(cl_page.get('tasks', []))})")

    # 7. Timing /api/tasks/summaries quiet polls: 20 ids (<=32 cache) vs 50 ids (>32 cache)
    for batch_sz in (20, 50):
        ids_param = ",".join(str(x) for x in range(71, 71 + batch_sz))
        path = f"/api/tasks/summaries?ids={ids_param}&comments_limit=1&attempts_limit=1"
        c0, h0, b0, dt0 = req(args.url, args.token, "GET", path)
        assert c0 == 200, f"summaries({batch_sz}) = {c0}: {b0[:200]}"
        s_etag = header(h0, "ETag")
        assert s_etag
        s_quiet = []
        for _ in range(5):
            cq, _, _, dtq = req(args.url, args.token, "GET", path, if_none_match=s_etag)
            assert cq == 304, f"summaries({batch_sz}) quiet = {cq}, want 304"
            s_quiet.append(dtq)
        print(f"GET /api/tasks/summaries ({batch_sz} ids): cold={dt0:.2f}ms quiet304={fmt_stats(s_quiet)}")

    # 8. Item 2: Close & reopen transition loop across diverse task states,
    # interleaving open-only live list, closed-tab paged list, and ?fields=full.
    # Task 25 is a paused blocker for task 23; closing/reopening 25 mutates task 23's blockedBy & reason too!
    transition_ids = ["40", "10", "140", "200", "131", "25"]
    print(f"--- Transition loop (close/reopen {len(transition_ids)} tasks: {transition_ids}) ---")

    c0, h0, b0, _ = req(args.url, args.token, "GET", "/api/tasks")
    assert c0 == 200
    prev_etag = header(h0, "ETag")

    for idx, tid in enumerate(transition_ids):
        # Close task
        cc, _, bc, _ = req(args.url, args.token, "POST", f"/api/tasks/{tid}/close", {"reason": "walkthrough transition test"})
        assert cc == 200, f"POST /api/tasks/{tid}/close = {cc}: {bc[:200]}"

        # Vary read order across iterations:
        # even idx: live -> closed-tab -> full
        # odd idx:  closed-tab -> full -> live
        if idx % 2 == 0:
            c_live, h_live, b_live, _ = req(args.url, args.token, "GET", "/api/tasks", if_none_match=prev_etag)
            c_closed, _, b_closed, _ = req(args.url, args.token, "GET", "/api/tasks?state=closed&cursor=&limit=100")
            c_full, _, b_full, _ = req(args.url, args.token, "GET", "/api/tasks?fields=full")
        else:
            c_closed, _, b_closed, _ = req(args.url, args.token, "GET", "/api/tasks?state=closed&cursor=&limit=100")
            c_full, _, b_full, _ = req(args.url, args.token, "GET", "/api/tasks?fields=full")
            c_live, h_live, b_live, _ = req(args.url, args.token, "GET", "/api/tasks", if_none_match=prev_etag)

        assert c_live == 200, f"after closing {tid}: GET /api/tasks with old ETag = {c_live}, want 200"
        assert c_closed == 200 and c_full == 200
        new_etag = header(h_live, "ETag")
        assert new_etag != prev_etag, f"ETag unchanged after closing {tid}"
        prev_etag = new_etag

        live_list = json.loads(b_live)
        closed_page = json.loads(b_closed)
        full_list = json.loads(b_full)

        live_id_set = {t["id"] for t in live_list}
        closed_id_set = {t["id"] for t in closed_page["tasks"]}
        assert tid not in live_id_set, f"closed task {tid} still in live list"
        assert tid in closed_id_set, f"closed task {tid} missing from first page of closed tab"

        diffs, n_open = diff_open_rows(live_list, full_list)
        assert not diffs, f"after closing {tid}: live vs full mismatch: {diffs[:5]}"

        if tid == "25":
            t23 = next(t for t in live_list if t["id"] == "23")
            assert "25" not in (t23.get("blockedBy") or []), f"closing blocker 25 left 23 blockedBy={t23.get('blockedBy')}"

        # Quiet 304 check right after close
        c304, _, _, _ = req(args.url, args.token, "GET", "/api/tasks", if_none_match=prev_etag)
        assert c304 == 304, f"expected 304 right after close of {tid}, got {c304}"

        # Reopen task
        cr, _, br, _ = req(args.url, args.token, "POST", f"/api/tasks/{tid}/reopen", {})
        assert cr == 200, f"POST /api/tasks/{tid}/reopen = {cr}: {br[:200]}"

        if idx % 2 == 0:
            c_live, h_live, b_live, _ = req(args.url, args.token, "GET", "/api/tasks", if_none_match=prev_etag)
            c_closed, _, b_closed, _ = req(args.url, args.token, "GET", "/api/tasks?state=closed&cursor=&limit=100")
            c_full, _, b_full, _ = req(args.url, args.token, "GET", "/api/tasks?fields=full")
        else:
            c_full, _, b_full, _ = req(args.url, args.token, "GET", "/api/tasks?fields=full")
            c_closed, _, b_closed, _ = req(args.url, args.token, "GET", "/api/tasks?state=closed&cursor=&limit=100")
            c_live, h_live, b_live, _ = req(args.url, args.token, "GET", "/api/tasks", if_none_match=prev_etag)

        assert c_live == 200, f"after reopening {tid}: GET /api/tasks with old ETag = {c_live}, want 200"
        assert c_closed == 200 and c_full == 200
        new_etag = header(h_live, "ETag")
        assert new_etag != prev_etag, f"ETag unchanged after reopening {tid}"
        prev_etag = new_etag

        live_list = json.loads(b_live)
        closed_page = json.loads(b_closed)
        full_list = json.loads(b_full)

        live_id_set = {t["id"] for t in live_list}
        closed_id_set = {t["id"] for t in closed_page["tasks"]}
        assert tid in live_id_set, f"reopened task {tid} missing from live list"
        assert tid not in closed_id_set, f"reopened task {tid} still in closed tab"

        diffs, n_open = diff_open_rows(live_list, full_list)
        assert not diffs, f"after reopening {tid}: live vs full mismatch: {diffs[:5]}"

        if tid == "25":
            t23 = next(t for t in live_list if t["id"] == "23")
            assert "25" in (t23.get("blockedBy") or []), f"reopening blocker 25 did not restore 23 blockedBy={t23.get('blockedBy')}"

        print(f"  task {tid}: close -> live/closed/full parity OK -> reopen -> live/closed/full parity OK (open={n_open})")

    print(f"PASS: all items 1 & 2 checks succeeded on {args.label}")


if __name__ == "__main__":
    main()
