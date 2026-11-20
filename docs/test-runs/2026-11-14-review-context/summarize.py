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

"""Summarize every review body walk.sh dumped: sections, incremental or full,
earlier-review entries and question bytes. Usage: summarize.py out/<store>"""

import glob
import json
import os
import re
import sys

EARLIER = "\n\n## Earlier reviews of this pull request\n\n"
PARALLEL = "\n\n## Parallel reviews of this head\n\n"
INC = "**This is an incremental review.**"

out = sys.argv[1]
steps = [l.split(" dump ")[1].strip() for l in open(os.path.join(out, "steps.log")) if " dump " in l]
for step in steps:
    print(f"### {step}")
    files = sorted(glob.glob(os.path.join(out, f"{step}-task*.json")),
                   key=lambda p: int(re.search(r"task(\d+)\.json$", p).group(1)))
    for p in files:
        t = json.load(open(p))
        body = t.get("description") or ""
        head = ""
        m = re.search(r"\(`[a-z-]+` at `([0-9a-f]+)`\)", body)
        e = body.rfind(EARLIER)
        par = body.rfind(PARALLEL)
        inc = re.search(r"`git diff ([0-9a-f]{12})[0-9a-f]* ([0-9a-f]{12})[0-9a-f]*`", body)
        sections = []
        if e >= 0:
            sections.append("earlier")
        if INC in body:
            sections.append("incremental %s..%s" % (inc.group(1), inc.group(2)) if inc else "incremental")
        elif "review" in t["title"].lower():
            sections.append("full")
        if par >= 0:
            peers = re.findall(r"Task (\d+) \(`([a-z-]+)`\) is also reviewing", body[par:])
            sections.append("parallel " + ",".join(f"{i}:{k}" for i, k in peers))
        print(f"- task {t['id']} [{t['state']}] {t['title'][:40]}: {'; '.join(sections)} ({len(body)} B)")
        if e >= 0:
            end = len(body)
            for stop in (body.find(INC, e), par if par > e else -1):
                if stop > e:
                    end = min(end, stop)
            sec = body[e + len(EARLIER):end]
            qbytes = 0
            for line in sec.splitlines():
                if line.startswith("- Task"):
                    print("    " + line[:200])
                elif line.startswith("  - "):
                    qbytes += len(line) + 1
                    print("      " + line[:110])
            print(f"    question bytes: {qbytes}")
