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

import { useCallback, useRef } from "react";

// revealFocusedTab is a scrollable MUI Tabs strip's onFocus. Arrow keys
// move focus along the strip (MUI's own keyboard handling), but MUI only
// scrolls the *selected* tab into view: at 700 px, every other
// ArrowRight on the Admin strip left the focused tab mostly past the
// right edge, under the scroll button, so a keyboard user could not see
// where they were (found in the 2026-10-06 deployment-operations
// walkthrough). Focus events bubble in React, so one handler on the
// strip covers every tab in it.
//
// It says it twice. MUI re-measures the strip on every render and, when
// the indicator has moved by as much as a sub-pixel, scrolls the
// selected tab back into view. Jumping to the far end with End makes a
// scroll button appear, which is such a render, so the selected first
// tab could take the strip back from the focused last one a frame later
// (deployment-operations-navigation.spec.js at 700 px, depending on how
// wide the rail happened to be). The second reveal comes after that has
// settled, and only while the tab still has focus.
export const REVEAL_AGAIN_MS = 200;

export function revealFocusedTab(event) {
  const tab = event.target;
  if (tab?.getAttribute?.("role") !== "tab") return;
  const reveal = () =>
    tab.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  reveal();
  setTimeout(() => {
    if (tab.ownerDocument?.activeElement === tab) reveal();
  }, REVEAL_AGAIN_MS);
}

// revealSelectedTab scrolls a scrollable MUI Tabs strip -- root is the
// Tabs' own element -- just far enough along that its selected tab is
// wholly inside the visible part of the strip. Only the strip scrolls:
// scrollIntoView would also move the page, which is not what a strip
// changing width asked for.
export function revealSelectedTab(root) {
  const scroller = root?.querySelector?.(".MuiTabs-scroller");
  const selected = root?.querySelector?.('[role="tab"][aria-selected="true"]');
  if (!scroller || !selected) return;
  const strip = scroller.getBoundingClientRect();
  const tab = selected.getBoundingClientRect();
  if (tab.left < strip.left) {
    scroller.scrollLeft -= strip.left - tab.left;
  } else if (tab.right > strip.right) {
    scroller.scrollLeft += tab.right - strip.right;
  }
}

// REVEAL_AFTER_MUI_MS outlasts MUI Tabs' animated scroll, which runs
// for the theme's standard transition (300ms).
export const REVEAL_AFTER_MUI_MS = 400;

// useRevealSelectedTab keeps a scrollable strip's selected tab in view
// whenever the strip itself changes width, and returns the ref to put on
// the Tabs. MUI scrolls the selected tab into view when the selection
// moves, but not when the visible part of the strip narrows under it --
// and with scrollButtons="auto" that is exactly what happens on the
// first layout of a narrow window: MUI scrolls the selected tab into a
// strip as wide as the row, then finds the tabs overflow, adds its two
// scroll buttons, and leaves the tab sitting under the right-hand one.
// A phone opening /repos/<owner>/<name>/settings/advanced from Settings
// -> Repositories saw 12px of the selected tab (found in the 2026-10-29
// repository walkthrough). A rotated phone or a resized window is the
// same narrowing, and gets the same answer.
export function useRevealSelectedTab() {
  const stop = useRef(null);
  return useCallback((root) => {
    stop.current?.();
    stop.current = null;
    const scroller = root?.querySelector?.(".MuiTabs-scroller");
    if (!scroller || typeof ResizeObserver === "undefined") return;
    // A keyboard user moving along the strip is followed by
    // revealFocusedTab instead, and this would pull the strip back off
    // the tab they are on.
    const reveal = () => {
      const focused = root.ownerDocument?.activeElement;
      if (focused?.getAttribute?.("role") === "tab" && root.contains(focused))
        return;
      revealSelectedTab(root);
    };
    let again = null;
    const observer = new ResizeObserver(() => {
      reveal();
      // MUI's own scroll to the selected tab is animated, towards the
      // place it worked out for the wider strip, and it overwrites any
      // scroll made while it runs: say it again once that has finished.
      clearTimeout(again);
      again = setTimeout(reveal, REVEAL_AFTER_MUI_MS);
    });
    observer.observe(scroller);
    stop.current = () => {
      observer.disconnect();
      clearTimeout(again);
    };
  }, []);
}
