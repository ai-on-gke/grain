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

import { afterEach, describe, expect, it, vi } from "vitest";
import { keepInView } from "./keepInView.js";

// A card whose top the test moves, as content drawn above it would.
function card() {
  const el = document.createElement("div");
  let top = 100;
  el.getBoundingClientRect = () => ({ top, height: 50 });
  el.scrollIntoView = vi.fn(() => {
    top = 100;
  });
  document.body.appendChild(el);
  return { el, push: (by) => (top += by) };
}

// MutationObserver callbacks run as microtasks.
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
const draw = () => document.body.appendChild(document.createElement("p"));

describe("keepInView", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("scrolls back to a card that content drawn above it pushed away", async () => {
    const { el, push } = card();
    const stop = keepInView(el, { block: "center" });
    push(1500);
    draw();
    await settle();
    expect(el.scrollIntoView).toHaveBeenCalledWith({ block: "center" });

    // Drawing that does not move the card leaves the scroll alone.
    el.scrollIntoView.mockClear();
    draw();
    await settle();
    expect(el.scrollIntoView).not.toHaveBeenCalled();
    stop();
  });

  it("lets go once the reader scrolls", async () => {
    const { el, push } = card();
    const stop = keepInView(el, { block: "center" });
    window.dispatchEvent(new Event("wheel"));
    push(1500);
    draw();
    await settle();
    expect(el.scrollIntoView).not.toHaveBeenCalled();
    stop();
  });

  it("lets go after its time", async () => {
    vi.useFakeTimers();
    try {
      const { el, push } = card();
      keepInView(el, { block: "start" }, 1000);
      vi.advanceTimersByTime(1001);
      push(1500);
      draw();
      await vi.runAllTimersAsync();
      expect(el.scrollIntoView).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});
