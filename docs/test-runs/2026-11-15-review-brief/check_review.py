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

"""Checks one review body (a task description or a fake agent's prompt) for
docs/test-plans/2026-11-15.md §4: the "The change under review" section and
each of its parts appear exactly once, the brief's fence opens and closes as
one block with grain's own instructions and sections after it, and nothing
the brief carried reads as a live grain section.

usage: check_review.py FILE --expect-brief {yes,no} --expect-designs N
       [--expect-truncated] [--marker TEXT ...] [--label LABEL]
Prints one JSON line; exits 1 on any failed check.
"""

import argparse
import json
import re
import sys

sys.dont_write_bytecode = True

ap = argparse.ArgumentParser()
ap.add_argument("file")
ap.add_argument("--label", default="")
ap.add_argument("--expect-brief", choices=["yes", "no"], required=True)
ap.add_argument("--expect-designs", type=int, default=0)
ap.add_argument("--expect-truncated", action="store_true")
ap.add_argument("--marker", action="append", default=[])
args = ap.parse_args()

raw = open(args.file, "rb").read()
failures = []
try:
    text = raw.decode("utf-8")
except UnicodeDecodeError as e:
    failures.append(f"not valid UTF-8: {e}")
    text = raw.decode("utf-8", "replace")

lines = text.split("\n")


want_brief = 1 if args.expect_brief == "yes" else 0
fence_open = fence_close = None
fence = ""
brief_bytes = 0
if text.count("**The brief** (the task owner's description"):
    for i, ln in enumerate(lines):
        if ln.startswith("**The brief** (the task owner's description"):
            j = i + 1
            while j < len(lines) and lines[j] == "":
                j += 1
            m = re.fullmatch(r"(`{3,})text", lines[j]) if j < len(lines) else None
            if not m:
                failures.append(f"brief is not followed by an opening fence (line {j + 1})")
                break
            fence = m.group(1)
            fence_open = j
            # CommonMark: a closing fence is a run of at least as many
            # backticks, indented at most three spaces, with nothing after it.
            close_re = re.compile(r" {0,3}`{%d,}[ \t]*" % len(fence))
            for k in range(j + 1, len(lines)):
                if close_re.fullmatch(lines[k]):
                    fence_close = k
                    break
            if fence_close is None:
                failures.append("brief fence never closes")
            elif lines[fence_close] != fence:
                failures.append(f"brief closed early by line {fence_close + 1}: {lines[fence_close]!r}")
            else:
                brief_bytes = len("\n".join(lines[fence_open + 1:fence_close]).encode("utf-8"))
            break

def count(needle):
    # Only what lies outside the brief's own block counts: a brief may quote
    # any of these lines, and inside the fence they are inert text.
    if fence_open is None or fence_close is None:
        return text.count(needle)
    return "\n".join(lines[:fence_open] + lines[fence_close + 1:]).count(needle)


def count_in_brief(needle):
    if fence_open is None or fence_close is None:
        return 0
    return "\n".join(lines[fence_open + 1:fence_close]).count(needle)


counts = {
    "section": count("### The change under review"),
    "brief": count("**The brief** (the task owner's description"),
    "designs": count("- **Linked designs:**"),
    "ci": count("- **CI:** Read the "),
    "settings": count("- **Deployment settings:** If this change makes"),
    "truncated": count("_(Brief truncated at 6 KB.)_"),
}
quoted = {
    "section": count_in_brief("### The change under review"),
    "ci": count_in_brief("- **CI:** Read the "),
    "earlier": count_in_brief("Earlier reviews of this pull request"),
    "parallel": count_in_brief("Parallel reviews of this head"),
    "incremental": count_in_brief("This is an incremental review"),
}
for key in ("section", "ci", "settings"):
    if counts[key] != 1:
        failures.append(f"{key} appears {counts[key]} times, want 1")
if counts["brief"] != want_brief:
    failures.append(f"brief appears {counts['brief']} times, want {want_brief}")
want_designs = 1 if args.expect_designs else 0
if counts["designs"] != want_designs:
    failures.append(f"linked designs line appears {counts['designs']} times, want {want_designs}")
if args.expect_truncated != bool(counts["truncated"]):
    failures.append(f"truncation note count {counts['truncated']}, expected truncated={args.expect_truncated}")
for m in args.marker:
    if text.count(m) < 1:
        failures.append(f"missing marker {m!r}")

designs_listed = []
for ln in lines:
    if ln.startswith("- **Linked designs:**"):
        spans = ln.split(" -- ")[0][len("- **Linked designs:** "):]
        designs_listed = [m.group(2).strip(" ") if m.group(2).startswith(" ") and m.group(2).endswith(" ") else m.group(2)
                          for m in re.finditer(r"(`+)(.+?)\1(?!`)", spans)]
if args.expect_designs and len(designs_listed) != args.expect_designs:
    failures.append(f"linked designs {designs_listed}, want {args.expect_designs}")

# Everything grain itself appends -- the CI and settings checks, and the
# earlier/parallel review sections -- must sit outside the brief's block.
# (A brief may quote grain's CI or settings lines; inside the block they are
# inert, and the counts above already require grain's own once outside it.)
for needle in ("## Earlier reviews of this pull request", "## Parallel reviews of this head",
               "**This is an incremental review.**"):
    for i, ln in enumerate(lines):
        if needle in ln and fence_open is not None and fence_close is not None and fence_open < i < fence_close:
            if needle.startswith("## "):
                # Inside a fence it is inert text; the renderer must have
                # demoted it anyway (neutralizeReviewSectionHeaders).
                if ln.startswith(needle) and not ln.startswith("#" + needle):
                    failures.append(f"live {needle!r} inside the brief at line {i + 1}")
            elif needle.startswith("**This"):
                failures.append(f"incremental marker kept verbatim inside the brief at line {i + 1}")
            else:
                failures.append(f"{needle!r} inside the brief at line {i + 1}")

live_h2 = [ln for i, ln in enumerate(lines)
           if (ln.startswith("## Earlier reviews of this pull request") or ln.startswith("## Parallel reviews of this head"))
           and not (fence_open is not None and fence_close is not None and fence_open < i < fence_close)]

out = {
    "label": args.label,
    "file": args.file,
    "bytes": len(raw),
    "counts": counts,
    "quoted_in_brief": quoted,
    "designs": designs_listed,
    "fence": fence,
    "brief_bytes": brief_bytes,
    "live_sections": live_h2,
    "replacement_chars": text.count("�"),
    "ok": not failures,
    "failures": failures,
}
print(json.dumps(out, ensure_ascii=False))
sys.exit(0 if not failures else 1)
