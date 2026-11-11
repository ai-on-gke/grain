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

"""Shared HTTP helpers for the §12 repo-skills walkthrough (env.sh's daemon)."""

import json
import os
import re
import time
import urllib.error
import urllib.request

URL = os.environ.get("SKILLS_DAEMON", "http://127.0.0.1:4281")
WORK = os.environ.get("WORK", "/var/tmp/grain-skills")


def code():
    return open(os.path.join(WORK, "data", "local-api-token")).read().strip()


def api(method, path, body=None, ok=(200, 201, 202, 204)):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(URL + path, data=data, method=method,
                                 headers={"Authorization": "Bearer " + code(), "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            raw = r.read()
            return json.loads(raw) if raw.strip() else None
    except urllib.error.HTTPError as e:
        raise RuntimeError(f"{method} {path}: HTTP {e.code}: {e.read()[:600]!r}")


def wait_attempts(tid, n, timeout=300):
    deadline = time.time() + timeout
    detail = {}
    while time.time() < deadline:
        detail = api("GET", f"/api/tasks/{tid}")
        attempts = detail.get("attempts") or []
        if len(attempts) >= n and attempts[n - 1].get("finishedAt"):
            return detail
        time.sleep(2)
    raise RuntimeError(f"task {tid}: attempt {n} did not finish: state={detail.get('state')}")


REPO_SKILLS_RE = re.compile(r"<repo-skills[^>]*>(.*?)</repo-skills>", re.S)


def skills_block(prompt):
    """The prompt's skills index section: everything from the heading that
    introduces it through the closing </repo-skills> tag (or None)."""
    m = REPO_SKILLS_RE.search(prompt)
    return m.group(0) if m else None
