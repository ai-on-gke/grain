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
