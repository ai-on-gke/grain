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

// The list/detail split of the Inbox and Tasks (spec D-3): 30/70 by default, with a
// wide range either way so the list can be a narrow index or most of the
// screen. The width is kept as a share of the split, so it means the same
// thing on a laptop and on a wide monitor.

export const DEFAULT_RATIO = 0.3;
export const WIDE_RATIO = 0.42;
// Neither pane may get so narrow it stops being usable: the list keeps
// room for an id and a few words, the inspector for a decision dock.
export const MIN_LIST_PX = 200;
export const MIN_INSPECTOR_PX = 320;
export const STEP_PX = 16;
export const BIG_STEP_PX = 48;
export const RATIO_KEY = "grain.inbox.splitRatio";
export const COLLAPSED_KEY = "grain.inbox.collapsed";
// Below this the inspector is a sheet over the list, not beside it.
export const SPLIT_MIN_VIEWPORT = 1080;
// Below this width a selected task opens full screen instead of a pane.
export const PHONE_QUERY = "(max-width: 599.95px)";

export interface Bounds {
  min: number; // px
  max: number; // px
}

export function bounds(stage: number): Bounds {
  const max = Math.max(MIN_LIST_PX, stage - MIN_INSPECTOR_PX);
  return { min: Math.min(MIN_LIST_PX, max), max };
}

// clampRatio keeps a share inside the bounds for a stage width.
export function clampRatio(ratio: number, stage: number): number {
  if (!Number.isFinite(ratio) || stage <= 0) return DEFAULT_RATIO;
  const { min, max } = bounds(stage);
  const px = Math.min(max, Math.max(min, ratio * stage));
  return px / stage;
}

export function percent(ratio: number): number {
  return Math.round(ratio * 100);
}

export function loadRatio(key = RATIO_KEY, fallback = DEFAULT_RATIO): number {
  try {
    const v = Number(localStorage.getItem(key));
    return v > 0 && v < 1 ? v : fallback;
  } catch {
    return fallback;
  }
}

export function saveRatio(ratio: number, key = RATIO_KEY): void {
  try {
    localStorage.setItem(key, ratio.toFixed(4));
  } catch {
    // Storage unavailable: the width lasts for this page only.
  }
}

export function loadCollapsed(key = COLLAPSED_KEY): boolean {
  try {
    return localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

export function saveCollapsed(on: boolean, key = COLLAPSED_KEY): void {
  try {
    localStorage.setItem(key, on ? "1" : "0");
  } catch {
    // As above.
  }
}

// keyStep is how a key moves the separator (W3C APG window splitter):
// arrows by 16px (48 with Shift), Home and End to the ends. Returns the
// new ratio, or null for a key it does not handle.
export function keyStep(
  key: string,
  shift: boolean,
  ratio: number,
  stage: number,
): number | null {
  const { min, max } = bounds(stage);
  const px = ratio * stage;
  const step = shift ? BIG_STEP_PX : STEP_PX;
  let next: number;
  switch (key) {
    case "ArrowLeft":
      next = px - step;
      break;
    case "ArrowRight":
      next = px + step;
      break;
    case "Home":
      next = min;
      break;
    case "End":
      next = max;
      break;
    default:
      return null;
  }
  return clampRatio(next / stage, stage);
}
