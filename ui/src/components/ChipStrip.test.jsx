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

import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import ChipStrip, {
  OVERFLOW,
  firstWrapped,
  fitChipStrips,
} from "./ChipStrip.jsx";

// jsdom lays nothing out, so the lines the browser would put each chip
// on are given to it: tops[i] is chip i's top edge.
function strip(tops) {
  const el = document.createElement("span");
  for (const top of tops) {
    const chip = document.createElement("span");
    chip.getBoundingClientRect = () =>
      chip.classList.contains(OVERFLOW)
        ? { top: 0, bottom: 0, left: 0, right: 0 }
        : { top, bottom: top + 24 };
    el.append(chip);
  }
  return el;
}

const hidden = (el) =>
  [...el.children].map((c) => c.classList.contains(OVERFLOW));

describe("ChipStrip", () => {
  it("hides nothing when every chip is on the first line", () => {
    const el = strip([0, 0, 0]);
    expect(firstWrapped(el)).toBe(-1);
    fitChipStrips([el]);
    expect(hidden(el)).toEqual([false, false, false]);
  });

  it("hides the chips that wrapped, so they leave no gap behind", () => {
    const el = strip([0, 0, 30, 30]);
    fitChipStrips([el]);
    expect(hidden(el)).toEqual([false, false, true, true]);
  });

  it("hides every chip after the first that wrapped, in order", () => {
    // A narrow chip after a wide wrapped one is not pulled forward into
    // its place: the strip loses its last chips first.
    const el = strip([0, 30, 0]);
    expect(firstWrapped(el)).toBe(1);
    fitChipStrips([el]);
    expect(hidden(el)).toEqual([false, true, true]);
  });

  it("puts a hidden chip back when the row has room for it again", () => {
    const el = strip([0, 30]);
    fitChipStrips([el]);
    expect(hidden(el)).toEqual([false, true]);
    el.children[1].getBoundingClientRect = () => ({ top: 0, bottom: 24 });
    fitChipStrips([el]);
    expect(hidden(el)).toEqual([false, false]);
  });

  it("renders a .chips strip with its own class and attributes", () => {
    const { container } = render(
      <ChipStrip id="s" className="extra">
        <span>a</span>
      </ChipStrip>,
    );
    const el = container.querySelector("#s");
    expect(el.className).toBe("chips extra");
    expect(el.textContent).toBe("a");
  });
});
