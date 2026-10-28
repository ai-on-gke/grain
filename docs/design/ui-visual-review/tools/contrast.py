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

"""Contrast table for the review's palettes (WCAG 2.x relative luminance).

Prints a markdown table per option and theme, with AA pass/fail for text
and for the 3:1 non-text threshold used for icons and status dots.
Usage: python3 contrast.py [option]   (option = current|1|2|3|all)
"""
import sys

def lum(h):
    h = h.lstrip('#')
    r, g, b = (int(h[i:i+2], 16) / 255 for i in (0, 2, 4))
    f = lambda v: v / 12.92 if v <= 0.03928 else ((v + 0.055) / 1.055) ** 2.4
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)

def ratio(a, b):
    la, lb = sorted((lum(a), lum(b)), reverse=True)
    return (la + 0.05) / (lb + 0.05)

PALETTES = {
    "current": {
        "light": dict(canvas="#f7f6f3", surface="#ffffff", text="#24221d", secondary="#6e6a60", tertiary="#9a9588",
                      accent="#8a6a2e", on_accent="#ffffff", danger="#d0453a", proposed="#b45309", queued="#6e6a60",
                      interactive="#0e7c86", awaiting="#9333ea", awaiting_submit="#d95f02", completed="#0969da",
                      deferred="#64748b", closed="#9a9588", merged="#1a7f37", merging="#0969da", success="#1a9c53",
                      warning="#a8621c", info="#3f6b8a"),
        "dark": dict(canvas="#0f1013", surface="#14161a", text="#e9e7e1", secondary="#9b988f", tertiary="#6f6c65",
                     accent="#d9a441", on_accent="#17130a", danger="#ec7469", proposed="#f59e0b", queued="#9b988f",
                     interactive="#3fb8c4", awaiting="#c084fc", awaiting_submit="#fb923c", completed="#58a6ff",
                     deferred="#8fa1b8", closed="#6f6c65", merged="#3fb950", merging="#58a6ff", success="#3ec573",
                     warning="#e29a4e", info="#7fa8c7"),
    },
    "1": {
        "light": dict(canvas="#f7f6f3", surface="#ffffff", text="#24221d", secondary="#5c574d", tertiary="#6b655a",
                      accent="#8a6a2e", on_accent="#ffffff", danger="#c03a2f", proposed="#a35406", queued="#6e6a60",
                      interactive="#0b6f78", awaiting="#7e22ce", awaiting_submit="#c2410c", completed="#0b5fc0",
                      deferred="#55657a", closed="#7b766a", merged="#15803d", merging="#0b5fc0", success="#15803d",
                      warning="#a35406", info="#3f6b8a"),
        "dark": dict(canvas="#0f1013", surface="#15171b", text="#ebe8e1", secondary="#aaa69a", tertiary="#8d897e",
                     accent="#d9a441", on_accent="#17130a", danger="#f0786d", proposed="#f5a524", queued="#a39f94",
                     interactive="#45c3cf", awaiting="#c89bfc", awaiting_submit="#fb9a4a", completed="#6fb1ff",
                     deferred="#98a8bd", closed="#8d897e", merged="#4cc466", merging="#6fb1ff", success="#4cc466",
                     warning="#e8a45a", info="#8ab2d0"),
    },
    "2": {
        "light": dict(canvas="#f3f3f1", surface="#ffffff", text="#1b1c1e", secondary="#55575c", tertiary="#6b6e75",
                      accent="#7a5a22", on_accent="#ffffff", danger="#c81e1e", proposed="#b4530a", queued="#6b6e75",
                      interactive="#0b7285", awaiting="#7c3aed", awaiting_submit="#c2410c", completed="#1d4ed8",
                      deferred="#475569", closed="#6f727a", merged="#15803d", merging="#1d4ed8", success="#15803d",
                      warning="#b4530a", info="#1d4ed8"),
        "dark": dict(canvas="#121417", surface="#191c20", text="#e7e8ea", secondary="#a3a7ae", tertiary="#878c94",
                     accent="#e0b45c", on_accent="#1a1405", danger="#f87171", proposed="#f5a524", queued="#9aa0a8",
                     interactive="#22c1d6", awaiting="#c084fc", awaiting_submit="#fb923c", completed="#60a5fa",
                     deferred="#94a3b8", closed="#878c94", merged="#4ade80", merging="#60a5fa", success="#4ade80",
                     warning="#f5a524", info="#60a5fa"),
    },
    "3": {
        "light": dict(canvas="#f4f0e8", surface="#fffdf8", text="#1b1916", secondary="#5a5447", tertiary="#6e675a",
                      accent="#7d5a1c", on_accent="#ffffff", danger="#b3261e", proposed="#a8530a", queued="#6e675a",
                      interactive="#0b6f78", awaiting="#6d28d9", awaiting_submit="#b83a0b", completed="#1a56c4",
                      deferred="#4b5a6e", closed="#736c5d", merged="#167a3c", merging="#1a56c4", success="#167a3c",
                      warning="#a8530a", info="#1a56c4",
                      rail="#1b1916", rail_text="#ece7dc", rail_muted="#a69e8e", rail_accent="#e3b65a"),
        "dark": dict(canvas="#0c0d10", surface="#15161a", text="#ece7dc", secondary="#aaa393", tertiary="#8c8577",
                     accent="#e3b65a", on_accent="#1a1405", danger="#f4776c", proposed="#f6a623", queued="#a09a8c",
                     interactive="#3fc3cf", awaiting="#c99cff", awaiting_submit="#fb9a4a", completed="#6fb1ff",
                     deferred="#9aa9bd", closed="#8c8577", merged="#4fcf72", merging="#6fb1ff", success="#4fcf72",
                     warning="#f6a623", info="#6fb1ff",
                     rail="#0c0d10", rail_text="#ece7dc", rail_muted="#a09a8c", rail_accent="#e3b65a"),
    },
}

STATES = ["proposed", "queued", "interactive", "awaiting", "awaiting_submit", "completed", "merging", "merged",
          "deferred", "closed", "danger"]

def table(name, theme, p):
    rows = []
    def row(label, fg, bg, kind):
        r = ratio(fg, bg)
        need = 4.5 if kind == "text" else 3.0
        rows.append(f"| {label} | `{fg}` on `{bg}` | {r:.2f} | {'pass' if r >= need else 'FAIL'} ({need}) |")
    row("text / canvas", p["text"], p["canvas"], "text")
    row("text / surface", p["text"], p["surface"], "text")
    row("secondary / canvas", p["secondary"], p["canvas"], "text")
    row("secondary / surface", p["secondary"], p["surface"], "text")
    row("tertiary / canvas", p["tertiary"], p["canvas"], "text")
    row("tertiary / surface", p["tertiary"], p["surface"], "text")
    row("accent text / surface", p["accent"], p["surface"], "text")
    row("on-accent / accent (button)", p["on_accent"], p["accent"], "text")
    if "rail" in p:
        row("rail text / rail", p["rail_text"], p["rail"], "text")
        row("rail muted / rail", p["rail_muted"], p["rail"], "text")
        row("rail accent / rail", p["rail_accent"], p["rail"], "text")
    for s in STATES:
        row(f"state {s} as text / surface", p[s], p["surface"], "text")
    for s in STATES:
        row(f"state {s} as dot / canvas", p[s], p["canvas"], "ui")
    head = f"\n#### {name} — {theme}\n\n| sample | colours | ratio | AA |\n|---|---|---|---|\n"
    return head + "\n".join(rows)

def main():
    which = sys.argv[1] if len(sys.argv) > 1 else "all"
    names = PALETTES.keys() if which == "all" else [which]
    for n in names:
        for t in ("light", "dark"):
            print(table(f"option {n}" if n != "current" else "current", t, PALETTES[n][t]))

if __name__ == "__main__":
    main()
