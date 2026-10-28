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

import { test, expect } from "./fixtures.js";

// Two panes in this UI open scrolled to their end rather than their top:
// an attempt's transcript (AttemptTranscriptOverlay.jsx, grain/task-22)
// and the System pane's log tail (LogsPage.jsx, grain/task-43). Each
// already has a component test next door that covers the logic well --
// AttemptTranscriptOverlay.test.jsx and LogsPage.test.jsx define
// scrollHeight and clientHeight on the element themselves (900 over 300)
// so scrollTop can say which end it is at, and both go on to cover the
// follow stopping once the reader scrolls up.
//
// What that shape cannot see is whether the container scrolls at all.
// jsdom has no layout, so those numbers are the test's own, not the
// browser's: a flex child with no min-height, an overflow rule that
// landed on the wrong element, a max-height dropped from .logs-view --
// any of them gives a pane that never scrolls, whose scrollTop is 0
// forever, and leaves both of those tests green. That is the one
// question asked here, of a real browser, and it is only a question at
// all against a document taller than the pane: `grain demo` seeds a
// 700-line transcript and a 300-line journal for exactly this
// (pkg/demo/demo.go's own comment on demoLogLines).
//
// See tasks.spec.js's doc comment for why this suite is the exception to
// "jsdom, not a browser".

// PINNED_SLACK_PX is AttemptTranscriptOverlay's and LogsPage's own
// constant: how far from the arithmetic bottom still counts as scrolled
// to the end, once a browser has rounded fractional line heights.
const PINNED_SLACK_PX = 24;

// atTheBottom asserts that a scrolling view really is one -- that it
// overflows, that it is not sitting at the top, and that what it is
// showing is its last line rather than its first.
async function atTheBottom(view) {
  // Polled, not read once: the scroll happens in an effect after the
  // fetched document renders, and a poll of either pane can re-render it
  // a moment later.
  await expect
    .poll(async () =>
      view.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight),
    )
    .toBeLessThanOrEqual(PINNED_SLACK_PX);

  const box = await view.evaluate((el) => ({
    scrollTop: el.scrollTop,
    scrollHeight: el.scrollHeight,
    clientHeight: el.clientHeight,
  }));
  // The assertion the jsdom tests cannot make: there is more document
  // than pane, so the pane scrolls, so scrollTop above meant something.
  expect(box.scrollHeight).toBeGreaterThan(box.clientHeight);
  expect(box.scrollTop).toBeGreaterThan(0);
}

test("opens an attempt's transcript scrolled to the end", async ({ page }) => {
  await page.goto("/");
  await page
    .locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" })
    .click();
  await expect(page.locator(".detail-header")).toBeVisible();

  // Typing on the attempt in the task's own Timeline is how the
  // transcript opens -- there is no button for it.
  await page.locator(".timeline-item-attempt").first().click();
  const transcript = page.locator(".MuiDialog-paper", {
    has: page.getByRole("heading", { name: /transcript$/ }),
  });
  const view = transcript.locator("pre.logs-view");
  await expect(view).toContainText("Committing this and pushing the branch");

  await atTheBottom(view);
});

test("opens the Logs pane on the journal's last lines", async ({ page }) => {
  await page.goto("/system/logs");
  await expect(
    page.getByRole("heading", { name: "System", exact: true }),
  ).toBeVisible();

  // The Logs tab is the one System opens on, and "daemon" the source it
  // picks -- which is the one `grain demo` configures.
  const view = page.locator("pre.logs-view");
  await expect(view).toContainText("grain-daemon[4242]");

  await atTheBottom(view);
});
