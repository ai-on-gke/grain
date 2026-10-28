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
import { REVEAL_AGAIN_MS, revealFocusedTab } from "./tabStrip.js";

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
