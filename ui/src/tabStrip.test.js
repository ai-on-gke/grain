// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { describe, expect, it, vi } from "vitest";
import {
  REVEAL_AGAIN_MS,
  revealFocusedTab,
  revealSelectedTab,
} from "./tabStrip.js";

describe("revealFocusedTab", () => {
  it("scrolls a focused tab into view along the strip only", () => {
    const tab = document.createElement("button");
    tab.setAttribute("role", "tab");
    tab.scrollIntoView = vi.fn();
    revealFocusedTab({ target: tab });
    expect(tab.scrollIntoView).toHaveBeenCalledWith({
      block: "nearest",
      inline: "nearest",
    });
  });

  it("reveals the tab again once MUI has settled, only while it has focus", () => {
    vi.useFakeTimers();
    try {
      const tab = document.createElement("button");
      tab.setAttribute("role", "tab");
      document.body.append(tab);
      tab.scrollIntoView = vi.fn();
      tab.focus();
      revealFocusedTab({ target: tab });
      vi.advanceTimersByTime(REVEAL_AGAIN_MS);
      expect(tab.scrollIntoView).toHaveBeenCalledTimes(2);

      tab.scrollIntoView.mockClear();
      revealFocusedTab({ target: tab });
      tab.blur();
      vi.advanceTimersByTime(REVEAL_AGAIN_MS);
      expect(tab.scrollIntoView).toHaveBeenCalledTimes(1);
      tab.remove();
    } finally {
      vi.useRealTimers();
    }
  });

  it("leaves anything that is not a tab alone", () => {
    const button = document.createElement("button");
    button.scrollIntoView = vi.fn();
    revealFocusedTab({ target: button });
    expect(button.scrollIntoView).not.toHaveBeenCalled();
  });
});

// strip builds a Tabs-shaped root -- a .MuiTabs-scroller holding a
// selected tab -- whose boxes are the ones given, since jsdom lays
// nothing out.
function strip(stripBox, tabBox, scrollLeft) {
  const root = document.createElement("div");
  const scroller = document.createElement("div");
  scroller.className = "MuiTabs-scroller";
  scroller.scrollLeft = scrollLeft;
  const tab = document.createElement("button");
  tab.setAttribute("role", "tab");
  tab.setAttribute("aria-selected", "true");
  scroller.append(tab);
  root.append(scroller);
  scroller.getBoundingClientRect = () => stripBox;
  tab.getBoundingClientRect = () => tabBox;
  return { root, scroller };
}

describe("revealSelectedTab", () => {
  it("scrolls the strip on to a selected tab under its right-hand edge", () => {
    // The 2026-10-29 walkthrough's numbers: Advanced at 322-414 in a
    // strip whose scroll buttons left it 56-334 wide.
    const { root, scroller } = strip(
      { left: 56, right: 334 },
      { left: 322, right: 414 },
      299,
    );
    revealSelectedTab(root);
    expect(scroller.scrollLeft).toBe(379);
  });

  it("scrolls the strip back to a selected tab past its left-hand edge", () => {
    const { root, scroller } = strip(
      { left: 56, right: 334 },
      { left: 20, right: 100 },
      120,
    );
    revealSelectedTab(root);
    expect(scroller.scrollLeft).toBe(84);
  });

  it("leaves a strip alone when its selected tab is already in view", () => {
    const { root, scroller } = strip(
      { left: 56, right: 334 },
      { left: 100, right: 200 },
      40,
    );
    revealSelectedTab(root);
    expect(scroller.scrollLeft).toBe(40);
  });

  it("does nothing for a root that is not a tab strip", () => {
    expect(() => revealSelectedTab(null)).not.toThrow();
    expect(() =>
      revealSelectedTab(document.createElement("div")),
    ).not.toThrow();
  });
});
