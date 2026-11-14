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

"""First-pass classifier for the classic ui/e2e suite (task 5078).

Reads the test list Playwright prints (`npx playwright test --list
--reporter=json` in ui/, saved as $LIST) and the spec sources, and sorts
each test into the migration plan's buckets:

  1  API/CLI   -- the real assertion is server behaviour; port to Go
  2  ui-ng     -- UI interaction on a page ui-ng has; rewrite at /next/
  3  freeze    -- stays in the classic suite until its page retires
  1+2, 1+3     -- mixed: the server half goes to 1, the rest to 2 or 3

It is a heuristic. Its error rate against a hand-checked sample is in
README.md; overrides.csv carries the hand corrections and wins.

Usage (from the repo root):
  (cd ui && npx playwright test --list --reporter=json > /tmp/list.json)
  LIST=/tmp/list.json python3 docs/test-plans/classic-ui-e2e-migration/classify.py
"""

import csv
import json
import os
import re
import sys
from collections import defaultdict

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
E2E = os.path.join(ROOT, "ui", "e2e")
OUT = os.path.dirname(os.path.abspath(__file__))

# ---------------------------------------------------------------- areas
#
# (area, ui-ng status, filename patterns, route prefixes). The status is
# read off ui-ng/src/app/shell/nav.ts and app.routes.ts: "yes" when ui-ng
# has the page, "partial" when it has the page but not the sub-view the
# classic specs drive, "no" when it has nothing for it yet, "n/a" for
# cross-cutting server behaviour with no page of its own.
AREAS = [
    ("ui-ng (already /next/)", "yes", [r"^next-"], []),
    ("Access code & sign-in", "n/a",
     [r"access-code", r"iap-header", r"insecure-origin", r"whoami", r"phone-signed-in-logout",
      r"user-tile", r"role-gated", r"secrets-key", r"security-perimeter"], []),
    ("Capabilities, grants & scopes", "yes",
     [r"capabilit", r"grant", r"api-scope", r"narrowing", r"admission", r"default-grants",
      r"credential-reach", r"local-scripts", r"gcp-permission", r"connection-credentials",
      r"session-pending-grants", r"session-default-reads", r"access-request", r"access-principals",
      r"repo-write-grant", r"repo-grants", r"grain_ci_grantable", r"repo-ci-release-capability",
      r"repo-ci-capabilities", r"external-agent-filer"],
     ["/settings/capabilities", "/settings/access"]),
    ("Releases & release memory", "partial",
     [r"release", r"version_display", r"qualification", r"suite-release"], []),
    ("Repos", "partial",
     [r"^repo", r"repos", r"issue-import", r"multi-branch", r"base-branch", r"paired_branch",
      r"port-", r"github-budget", r"external-pr", r"external-ref", r"internal-pr", r"diff-render"],
     ["/repos", "/repo/"]),
    ("CI", "yes", [r"^ci[-_]", r"metrics-github-ci", r"presubmit", r"verifier"], ["/ci"]),
    ("Merge queue & gates", "partial",
     [r"merge", r"dependency_automerge", r"inbox-retry-merge", r"path-triggers", r"cl-"], []),
    ("Processes (schedules, templates, suites, workflows)", "yes",
     [r"process", r"schedule", r"template", r"suite", r"recurring", r"run-now", r"workflow-schedules",
      r"standing-work", r"stale-schedules", r"batch_actions"],
     ["/processes", "/schedules", "/templates", "/suites"]),
    ("Sessions & Director", "yes",
     [r"session", r"director", r"console", r"chat", r"conversation", r"stopped-turn", r"interactive"],
     ["/session", "/sessions"]),
    ("Inbox", "yes", [r"inbox", r"waiting-tasks", r"review-questions", r"alerts-panel",
                      r"config-change-card", r"config-refusal"], ["/inbox"]),
    ("New task form", "yes", [r"new-task", r"new_task", r"fields", r"form-search", r"model-picker",
                              r"repo-field-search", r"filter-select", r"multi-search-select",
                              r"searchselect", r"dropped-defaults"], ["/new"]),
    ("Task detail", "yes",
     [r"^task[-_]", r"closed-task", r"comment-deep", r"edit-task", r"keep-running", r"attachment",
      r"committed-context", r"durable-task", r"close-reopen", r"goal-section", r"cleared-narrowing",
      r"unsaved-edits", r"stale-save", r"cluster_setup", r"ack", r"candidate-keyboard",
      r"dependency-long"],
     ["/tasks/"]),
    ("Tasks list, boards & timeline", "yes",
     [r"^tasks", r"board", r"gantt", r"list-stacked", r"closed_task_sorting", r"closed-search",
      r"bulk-edit", r"keyboard-open-rows", r"report-backlog", r"work-tree", r"main-tasks",
      r"interactive-task-intake", r"track6", r"manual-mode"],
     ["/tasks", "/boards", "/timeline"]),
    ("Telemetry & metrics", "yes", [r"metrics", r"observability", r"usage-settings", r"health-counters"],
     ["/metrics"]),
    ("Context & memory", "yes", [r"^memory"], ["/memory"]),
    ("Settings", "yes",
     [r"settings", r"config", r"defaults", r"team_repo_defaults", r"gchat", r"paths"],
     ["/settings", "/admin", "/config"]),
    ("Fleet / System / hosts", "partial",
     [r"system", r"hostd", r"host-", r"staging", r"sandbox", r"deployment", r"cli-run-locations",
      r"k8s", r"disk", r"database-backend", r"state-repo", r"install", r"restart", r"upgrade",
      r"disable-self-upgrade", r"auto-upgrade", r"hub-settings", r"audit-settings", r"declaration",
      r"bootstrap-playbooks", r"gemini-key", r"model-proxy", r"github-sandbox", r"model-source",
      r"provider-connection", r"git-connection", r"git-hosts", r"connections"],
     ["/system", "/staging"]),
    ("Shell, navigation & resilience", "yes",
     [r"shell", r"sidebar", r"rail", r"navigation", r"responsive", r"phone", r"narrow", r"scroll",
      r"reduced-motion", r"contrast", r"axe", r"disconnect", r"error-boundaries", r"load-failed",
      r"page-header", r"ui-fix", r"ui-followups", r"short-phone", r"dark", r"chip", r"host-wording",
      r"layout"],
     ["/"]),
]

TITLE_SERVER = re.compile(
    r"refus|forbid|\b40[0139]\b|denied|denies|reject|grant|scope|persist|surviv|lifecycle|"
    r"access code|signs? (?:in|out)|rotat|revok|validat|conflict|audit|attribut|saved?\b|"
    r"idempot|stale write|is not allowed|cannot|can't|without permission",
    re.I)
TITLE_MAINFLOW = re.compile(
    r"walkthrough|creat|answer|approv|close|reopen|retry|send|edit|cancel|merge|file[sd]?\b|"
    r"assign|pause|resume|run now|submit", re.I)

WEB_FIRST = re.compile(
    r"\.(?:not\.)?to(?:BeVisible|HaveText|HaveCount|HaveURL|BeFocused|HaveAttribute|HaveCSS|"
    r"BeEnabled|BeDisabled|BeChecked|ContainText|HaveValue|HaveClass|BeInViewport|BeHidden|"
    r"HaveTitle|BeAttached|HaveScreenshot|BeEditable|BeEmpty|HaveAccessibleName|"
    r"HaveAccessibleDescription|HaveId|HaveJSProperty|HaveValues|HaveRole)\(")
API_CALL = re.compile(
    r"\b(?:page\.|context\.|ctx\.)?request\.(?:get|post|put|delete|patch|fetch)\(|"
    r"\bapi\.(?:get|post|put|delete|patch)\(|\bfetch\(\s*[`'\"]?\$?\{?[^)]*?/api/")
API_ASSIGN = re.compile(
    r"(?:const|let|var)\s+(?:\{[^}]*\}|(\w+))\s*=\s*(?:await\s+)?(?:\(?\s*await\s+)?"
    r"(?:(?:page\.|context\.|ctx\.)?request\.\w+\(|api\.\w+\(|fetch\(|\w+\.json\(\)|\w+\.text\(\))")
INTERACT = re.compile(r"\.(?:click|dblclick|fill|press|pressSequentially|type|hover|check|uncheck|"
                      r"selectOption|tap|dragTo|setInputFiles)\(|keyboard\.|mouse\.")
KEYBOARD = re.compile(r"keyboard\.|\.press\(|toBeFocused|Tab\b|Escape|ArrowDown|ArrowUp")
PHONE = re.compile(r"setViewportSize\(\s*\{\s*width:\s*[1-6]\d\d\b|width:\s*[1-6]\d\d\s*,\s*height|"
                   r"isMobile|setViewportSize\(\s*PHONE|viewport:\s*PHONE")
DIALOG = re.compile(r"page\.on\(\s*[\"']dialog|\"dialog\"|'dialog'|alertdialog|confirm\b|Confirm",
                    re.I)
A11Y = re.compile(r"AxeBuilder|axe|toHaveAccessibleName|aria-|contrast|reducedMotion|prefers-",
                  re.I)
LAYOUT = re.compile(r"boundingBox\(|getComputedStyle|toHaveCSS|scrollWidth|clientWidth|"
                    r"scrollHeight|offsetWidth")
ROUTE_MOCK = re.compile(r"page\.route\(|context\.route\(")
CLI = re.compile(r"GRAIN_E2E_DEMO_BIN|execFile|spawn\(|realdaemon|startRealDaemon", re.I)
GOTO = re.compile(r"goto\(\s*[`'\"]([^`'\"]*)")
APIPATH = re.compile(r"[`'\"](?:\$\{[^}]+\})?(/(?:api|e2e)/[^`'\"?#]*)")


def strip_comments_and_strings(src):
    """Return src with comments blanked and string contents kept; also a
    list mapping. Only used for brace matching, so strings are blanked to
    spaces of the same length."""
    out = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        two = src[i:i + 2]
        if two == "//":
            j = src.find("\n", i)
            j = n if j < 0 else j
            out.append(" " * (j - i))
            i = j
        elif two == "/*":
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            out.append(re.sub(r"[^\n]", " ", src[i:j]))
            i = j
        elif c in "\"'`":
            j = i + 1
            depth = 0
            while j < n:
                if src[j] == "\\":
                    j += 2
                    continue
                if c == "`" and src[j:j + 2] == "${":
                    depth += 1
                    j += 2
                    continue
                if c == "`" and depth and src[j] == "}":
                    depth -= 1
                    j += 1
                    continue
                if src[j] == c and not depth:
                    break
                if src[j] == "\n" and c != "`":
                    break
                j += 1
            out.append(c + re.sub(r"[^\n]", " ", src[i + 1:j]) + (src[j] if j < n else ""))
            i = j + 1
        else:
            out.append(c)
            i += 1
    return "".join(out)


def body_at(src, blank, line):
    """The text of the test call that starts on 1-based line `line`:
    from the line to the brace that closes its callback."""
    offs = [0]
    for ln in src.split("\n"):
        offs.append(offs[-1] + len(ln) + 1)
    start = offs[line - 1]
    # The callback's opening brace is the first "{" after "=>" or
    # "async (" from the test( call.
    m = re.compile(r"=>\s*\{|function\s*\([^)]*\)\s*\{").search(blank, start)
    if not m:
        return src[start:start + 2000]
    i = m.end() - 1
    depth = 0
    for j in range(i, len(blank)):
        if blank[j] == "{":
            depth += 1
        elif blank[j] == "}":
            depth -= 1
            if depth == 0:
                return src[start:j + 1]
    return src[start:]


def local_functions(src, blank):
    """Name -> body for functions defined at a file's top level, so a
    test that calls a helper is read with the helper's calls in it."""
    funcs = {}
    for m in re.finditer(r"^(?:async\s+)?function\s+(\w+)\s*\(|^const\s+(\w+)\s*=\s*(?:async\s*)?\(",
                         blank, re.M):
        name = m.group(1) or m.group(2)
        k = blank.find("{", m.end())
        depth = 0
        for j in range(k, len(blank)):
            if blank[j] == "{":
                depth += 1
            elif blank[j] == "}":
                depth -= 1
                if depth == 0:
                    funcs[name] = src[m.start():j + 1]
                    break
    return funcs


def expects(body):
    """Each expect(...) call: (subject text, rest of the statement)."""
    res = []
    for m in re.finditer(r"\bexpect(?:\.soft|\.poll)?\(", body):
        i = m.end()
        depth = 1
        j = i
        while j < len(body) and depth:
            if body[j] == "(":
                depth += 1
            elif body[j] == ")":
                depth -= 1
            j += 1
        subject = body[i:j - 1]
        tail = body[j:j + 120].split(";")[0]
        res.append((m.group(0), subject, tail))
    return res


SPECIFIC = ("ui-ng (already /next/)", "Access code & sign-in", "Releases & release memory",
            "Merge queue & gates")
ROUTE_AREAS = [
    ("/settings/capabilities", "Capabilities, grants & scopes"),
    ("/settings/access", "Capabilities, grants & scopes"),
    ("/settings", "Settings"), ("/admin", "Settings"), ("/config", "Settings"),
    ("/system", "Fleet / System / hosts"), ("/staging", "Fleet / System / hosts"),
    ("/ci", "CI"), ("/inbox", "Inbox"), ("/session", "Sessions & Director"),
    ("/processes", "Processes (schedules, templates, suites, workflows)"),
    ("/schedules", "Processes (schedules, templates, suites, workflows)"),
    ("/templates", "Processes (schedules, templates, suites, workflows)"),
    ("/suites", "Processes (schedules, templates, suites, workflows)"),
    ("/repos", "Repos"), ("/repo/", "Repos"), ("/new", "New task form"),
    ("/tasks/", "Task detail"), ("/boards", "Tasks list, boards & timeline"),
    ("/metrics", "Telemetry & metrics"), ("/memory", "Context & memory"),
]
STATUS = {a: st for a, st, _, _ in AREAS}


def area_of(fname, body, filebody):
    """The page a test drives: a filename that names a cross-cutting
    area wins, then the first page the test (or its file) opens, then
    the rest of the filename patterns."""
    base = os.path.basename(fname)
    by_name = None
    for area, status, pats, _ in AREAS:
        if any(re.search(p, base) for p in pats):
            by_name = (area, status)
            break
    if by_name and by_name[0] in SPECIFIC:
        return by_name
    def by_route(paths):
        for p in paths:
            p = re.sub(r"\$\{[^}]*\}", "", p)
            if p in ("", "/", "about:blank") or p.startswith("/?"):
                continue
            for pre, area in ROUTE_AREAS:
                if p.startswith(pre):
                    return area, STATUS[area]
        return None
    return (by_route(GOTO.findall(body)) or by_name or by_route(GOTO.findall(filebody)) or
            ("Shell, navigation & resilience", "yes"))
    return "Shell, navigation & resilience", "yes"


def go_index():
    """Every /api/... path literal in a Go test file, path -> files."""
    idx = defaultdict(set)
    for top in ("pkg", "tests", "cmd"):
        for dp, _, fs in os.walk(os.path.join(ROOT, top)):
            for f in fs:
                if not f.endswith("_test.go"):
                    continue
                p = os.path.join(dp, f)
                try:
                    txt = open(p, encoding="utf-8", errors="ignore").read()
                except OSError:
                    continue
                for m in re.finditer(r"\"(/api/[a-zA-Z0-9_\-/{}%]+)", txt):
                    idx[norm_path(m.group(1))].add(os.path.relpath(p, ROOT))
    return idx


def norm_path(p):
    segs = []
    for s in p.strip("/").split("/"):
        if not s or s.startswith("{") or s.startswith("$") or s.startswith("%") or \
                re.fullmatch(r"[0-9a-f\-]{4,}|\d+|[A-Z0-9\-]+", s) or "${" in s:
            segs.append(":p")
        else:
            segs.append(s)
    # Keep the resource and the verb-ish last segment: enough to match
    # a Go test's literal without depending on ids.
    return "/" + "/".join(segs[:4])


# Go tests that name a great many API paths (harness-wide helpers and
# matrices) match everything and say nothing about one spec.
GENERIC_GO = 40


def go_cover(paths, idx, breadth):
    """Go test files naming the same API path, most specific first: the
    full path with its verb (/api/tasks/:p/close), then the resource
    under a fixed second segment (/api/repos/:p/releases)."""
    hits = []
    for p in paths:
        np = norm_path(p)
        segs = np.strip("/").split("/")
        if len(segs) < 3 or all(x == ":p" for x in segs[2:]):
            continue
        for f in sorted(idx.get(np, ())):
            if breadth[f] <= GENERIC_GO and f not in hits:
                hits.append(f)
    return hits


def classify(t, idx):
    fname, line, title, src, blank, funcs, filebody, fileflags = t
    body = body_at(src, blank, line)
    # Pull in one level of file-local helpers the test calls.
    called = set(re.findall(r"\b(\w+)\(", body))
    helper_text = "\n".join(funcs[c] for c in called if c in funcs and funcs[c] not in body)
    full = body + "\n" + helper_text
    area, status = area_of(fname, body, filebody)

    api_idents = set(m.group(1) for m in API_ASSIGN.finditer(full) if m.group(1))
    api_idents |= set(re.findall(r"const\s+\{\s*([\w\s,:]+)\}\s*=\s*await\s+\w+\.json\(\)", full))
    api_helpers = {n for n, fb in funcs.items() if API_CALL.search(fb)}
    for m in re.finditer(r"(?:const|let|var)\s+(\w+)\s*=\s*(?:await\s+)?\(?\s*(?:await\s+)?(\w+)\(",
                         full):
        if m.group(2) in api_helpers:
            api_idents.add(m.group(1))
    # A value read off an API response taints whatever is computed from it.
    for _ in range(3):
        for m in re.finditer(r"(?:const|let|var)\s+(\w+)\s*=\s*([^;\n]+)", full):
            if m.group(1) not in api_idents and any(
                    re.search(r"\b" + re.escape(i) + r"\b", m.group(2)) for i in list(api_idents)):
                api_idents.add(m.group(1))
    payload = set(re.findall(r"(?:const|let)\s+(\w+)\s*=\s*[^;]*(?:postDataJSON|postData)\(", full))
    api_idents -= payload
    n_api_assert = n_dom_assert = n_value_assert = n_layout_assert = n_setup_check = 0
    for kind, subj, tail in expects(full):
        s = subj.strip()
        if WEB_FIRST.search(tail) or re.match(r"(?:page|locator|row|dialog|button|\w*(?:Row|Button|"
                                              r"Dialog|Link|Input|Pane|Card|Chip|Menu|Field|Tab|Banner|"
                                              r"Badge|Header|Panel|Sheet|Picker|Tile|Box|Item|Select))"
                                              r"\b", s) and not re.search(r"\.status\(\)|\.json\(\)|"
                                                                          r"\.ok\(\)", s):
            n_dom_assert += 1
        elif re.search(r"\.ok\(\)", s) and re.search(r"toBe(?:Truthy\(|\(true)", tail) or \
                re.search(r"\.status\(\)", s) and re.search(r"toBe\(2\d\d\)|toBeLessThan\(300\)", tail):
            n_setup_check += 1
        elif re.search(r"\.status\(\)|\.json\(\)|\.ok\(\)|\.headers\(\)|request\.", s):
            n_api_assert += 1
        elif "poll" in kind and API_CALL.search(s):
            n_api_assert += 1
        elif re.search(r"box|width|height|left|right|top|bottom|\.x\b|\.y\b", s, re.I):
            n_layout_assert += 1
        elif any(re.search(r"\b" + re.escape(i) + r"\b", s) for i in payload):
            n_dom_assert += 1
        elif any(re.search(r"\b" + re.escape(i.strip().split(":")[0].strip()) + r"\b", s)
                 for i in api_idents if i.strip()):
            n_api_assert += 1
        else:
            n_value_assert += 1
    n_api_calls = len(API_CALL.findall(full))
    interact = len(INTERACT.findall(body))
    flags = []
    if KEYBOARD.search(body):
        flags.append("keyboard")
    if PHONE.search(body) or PHONE.search(title) or "phone" in fileflags:
        flags.append("phone")
    if DIALOG.search(body) or re.search(r"confirm|dialog", title, re.I):
        flags.append("dialog")
    if A11Y.search(body) or re.search(r"accessib|contrast|screen reader|focus", title, re.I):
        flags.append("a11y")
    if LAYOUT.search(body) or n_layout_assert or re.search(
            r"\bfits?\b|layout|align|overflow|wrap|width|centred|centered", title, re.I):
        flags.append("layout")
    if ROUTE_MOCK.search(full):
        flags.append("mocked-network")
    if CLI.search(full):
        flags.append("cli/real-daemon")
    leaf = title.split(" > ")[-1]
    server_title = bool(TITLE_SERVER.search(leaf))
    layout_title = bool(re.search(r"\bfits?\b|laid out|layout|\balign|centres|centered|centred|"
                                  r"overflow|inside its|sits at|right edge|wraps?\b|sideways|"
                                  r"cleanly|font|spacing", leaf, re.I))
    ui_kinds = set(flags) & {"keyboard", "dialog"}
    mainflow = bool(TITLE_MAINFLOW.search(title)) or "walkthrough" in fname

    ui_target = "2" if status == "yes" else "3"
    if area == "ui-ng (already /next/)":
        b, why = "2", "already drives ui-ng at /next/: move into the ui-ng suite as is"
    elif status == "n/a" and (n_api_assert or server_title):
        b = "1" if not n_dom_assert else "1+3"
        why = "server auth/access-code behaviour" + (
            "; the sign-in screen half stays classic" if n_dom_assert else "")
    elif n_api_assert and not n_dom_assert:
        b, why = "1", f"asserts API responses only ({n_api_assert} API expects, no DOM expects)"
    elif n_api_assert and n_dom_assert:
        b = "1+" + ui_target
        why = (f"mixed: {n_api_assert} API expects (server half) and {n_dom_assert} DOM expects "
               f"({'ui-ng has the page' if ui_target == '2' else 'ui-ng lacks the view: DOM half frozen'})")
    elif layout_title and not ui_kinds:
        b, why = "3", "classic layout/pixel check: ui-ng has its own design-book screenshot rules"
    elif ui_kinds and not re.search(r"refus|forbid|denied|reject|persist|surviv|validat", leaf, re.I):
        b = ui_target
        why = f"UI interaction ({', '.join(sorted(ui_kinds))})" + (
            " on a page ui-ng has" if ui_target == "2" else " on a view ui-ng lacks: freeze")
    elif server_title and n_api_calls and n_dom_assert:
        b = "1+" + ui_target
        why = "server behaviour (title) observed through the DOM after API setup: assert it on the API"
    elif server_title and n_dom_assert and re.search(r"refus|forbid|denied|reject|persist|surviv|"
                                                     r"revok|rotat|validat", title, re.I):
        b = "1+" + ui_target
        why = "refusal/persistence (title) observed only through the DOM: assert it on the API"
    elif "layout" in flags and not interact and "phone" not in flags:
        b, why = "3", "classic layout/pixel check: ui-ng has its own design-book screenshot rules"
    elif status == "yes" and (interact or set(flags) & {"keyboard", "phone", "dialog", "a11y"}):
        kinds = [f for f in flags if f in ("keyboard", "phone", "dialog", "a11y")]
        b = "2"
        why = "UI interaction" + (f" ({', '.join(kinds)})" if kinds else "") + " on a page ui-ng has"
    elif status in ("partial", "no") and interact:
        b, why = "3", "interaction on a view ui-ng does not have yet: freeze until it does, then rewrite"
    elif "mocked-network" in flags and n_dom_assert:
        b = ui_target
        why = ("client behaviour against a stubbed response (page.route)"
               + ("" if ui_target == "2" else ": ui-ng lacks the view, freeze"))
    elif status == "no":
        b, why = "3", "page not in ui-ng"
    else:
        b, why = "3", "static render check of seeded classic copy: freeze"
    paths = sorted(set(APIPATH.findall(full)))
    cover = go_cover(paths, idx, BREADTH) if "1" in b else []
    return {
        "file": f"ui/e2e/{fname}",
        "line": line,
        "title": title,
        "area": area,
        "ui_ng": status,
        "bucket": b,
        "reason": why,
        "api_expects": n_api_assert,
        "dom_expects": n_dom_assert,
        "api_calls": n_api_calls,
        "flags": " ".join(flags),
        "api_paths": " ".join(paths[:6]),
        "go_candidates": " ".join(cover[:3]),
    }


def main():
    lst = os.environ.get("LIST", "/tmp/list.json")
    d = json.load(open(lst))
    tests = []

    def walk(s, path):
        for sp in s.get("specs", []):
            tests.append((sp["file"], sp["line"], " > ".join(path + [sp["title"]])))
        for c in s.get("suites", []):
            t = c.get("title", "")
            walk(c, path + ([t] if t and not t.endswith(".js") else []))

    for s in d["suites"]:
        walk(s, [])
    cache = {}
    idx = go_index()
    global BREADTH
    BREADTH = defaultdict(int)
    for files in idx.values():
        for f in files:
            BREADTH[f] += 1
    rows = []
    for fname, line, title in tests:
        if fname not in cache:
            src = open(os.path.join(E2E, fname), encoding="utf-8").read()
            blank = strip_comments_and_strings(src)
            flags = set()
            if re.search(r"test\.use\(\s*\{[^}]*(?:viewport[^}]*width:\s*[1-6]\d\d\b|isMobile)", src):
                flags.add("phone")
            cache[fname] = (src, blank, local_functions(src, blank), flags)
        src, blank, funcs, flags = cache[fname]
        rows.append(classify((fname, line, title, src, blank, funcs, src, flags), idx))
    overrides = {}
    op = os.path.join(OUT, "overrides.csv")
    if os.path.exists(op):
        for r in csv.DictReader(open(op)):
            overrides[(r["file"], int(r["line"]), r["title"])] = r
    for r in rows:
        o = overrides.get((r["file"], r["line"], r["title"]))
        if o:
            r["script_bucket"] = r["bucket"]
            r["bucket"] = o["bucket"]
            r["reason"] = o["reason"] + " [hand-checked]"
        else:
            r["script_bucket"] = r["bucket"]
    # ported_to is written by hand as tests are ported (README.md, "When a
    # classic spec stops gating"), so a regeneration carries it over.
    ported = {}
    cp = os.path.join(OUT, "classification.csv")
    if os.path.exists(cp):
        for r in csv.DictReader(open(cp)):
            if r.get("ported_to"):
                ported[(r["file"], int(r["line"]), r["title"])] = r["ported_to"]
    for r in rows:
        r["ported_to"] = ported.get((r["file"], r["line"], r["title"]), "")
    fields = ["file", "line", "title", "area", "ui_ng", "bucket", "script_bucket", "reason",
              "api_expects", "dom_expects", "api_calls", "flags", "api_paths", "go_candidates",
              "ported_to"]
    with open(os.path.join(OUT, "classification.csv"), "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=fields)
        w.writeheader()
        for r in sorted(rows, key=lambda r: (r["file"], r["line"], r["title"])):
            w.writerow(r)
    print(f"{len(rows)} tests written", file=sys.stderr)


if __name__ == "__main__":
    main()
