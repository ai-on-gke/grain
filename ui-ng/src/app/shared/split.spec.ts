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

import {
  DEFAULT_RATIO,
  RATIO_KEY,
  bounds,
  clampRatio,
  keyStep,
  loadRatio,
  percent,
  saveRatio,
} from "./split";

describe("inbox split", () => {
  beforeEach(() => localStorage.clear());

  it("defaults to 30 / 70", () => {
    expect(DEFAULT_RATIO).toBe(0.3);
    expect(loadRatio()).toBe(0.3);
    expect(percent(clampRatio(DEFAULT_RATIO, 1368))).toBe(30);
  });

  it("lets the divider travel from 200px to the width less 320px", () => {
    expect(bounds(1368)).toEqual({ min: 200, max: 1048 });
    // About 15% to 77% on a 1440px screen's split.
    expect(percent(clampRatio(0, 1368))).toBe(15);
    expect(percent(clampRatio(1, 1368))).toBe(77);
    expect(percent(clampRatio(0.6, 1368))).toBe(60);
  });

  it("moves 16px per arrow, 48px with Shift, and jumps with Home and End", () => {
    const w = 1000;
    expect(keyStep("ArrowRight", false, 0.3, w)! * w).toBeCloseTo(316);
    expect(keyStep("ArrowLeft", true, 0.3, w)! * w).toBeCloseTo(252);
    expect(keyStep("Home", false, 0.3, w)! * w).toBeCloseTo(200);
    expect(keyStep("End", false, 0.3, w)! * w).toBeCloseTo(680);
    expect(keyStep("a", false, 0.3, w)).toBeNull();
  });

  it("remembers a width per browser and ignores nonsense", () => {
    saveRatio(0.55);
    expect(loadRatio()).toBeCloseTo(0.55);
    localStorage.setItem(RATIO_KEY, "7");
    expect(loadRatio()).toBe(0.3);
  });
});
