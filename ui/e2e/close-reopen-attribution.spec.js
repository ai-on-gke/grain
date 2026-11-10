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

// Who closed or reopened a task (model.TaskEvent) is on the timeline's
// meta line under the step. Walking docs/test-plans/2026-11-02.md §5
// found two things wrong with it
// (docs/test-runs/2026-11-02-close-reopen-attribution.md):
//   - a reason with an unbroken token in it (a URL, a branch name) ran
//     out of a phone's timeline item instead of wrapping inside it;
//   - closing a task already closed re-stamped its closure, so the
//     timeline grew a second "Closed" step with nobody on it.

import { test, expect } from "./fixtures.js";

const VIEWPORTS = [
  { label: "desktop", width: 1440, height: 900 },
  { label: "phone", width: 390, height: 844 },
];

const LONG_REASON =
  "cycling in-flight tasks after the 2026-10-30 incident: superseded by the rewrite at " +
  "https://github.com/acme/widgets/pull/104/files#diff-4f2a9c0e7b1d3e5f6a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f " +
  "and nothing on this branch is worth keeping";

test("a person's close names them, and its long reason wraps inside its timeline item", async ({
  page,
  request,
}) => {
  const created = await request.post("/api/tasks", {
    data: {
      title: `E2E close attribution ${Date.now()}`,
      repo: "acme/widgets",
    },
  });
  expect(created.ok()).toBe(true);
  const { id } = await created.json();

  // A person's Close, from the task page itself. Proposed and Closed both
  // carry attribution on their meta lines.
  await page.setViewportSize(VIEWPORTS[0]);
  await page.goto(`/tasks/${id}`);
  await page.getByRole("button", { name: "Close", exact: true }).click();
  const meta = page.locator(".timeline-transition-by");
  await expect(meta).toHaveCount(2);
  // The demo is signed into with its access code as alice, so the proposal
  // and close are hers through the code, said as such rather than as the
  // deployment's own address (docs/test-runs/2026-11-03-operator-attribution.md F3).
  await expect(meta.first()).toContainText(
    /^by (operator|alice@example\.com) \(access code\)/,
  );
  await expect(meta.nth(1)).toContainText(
    /^by (operator|alice@example\.com) \(access code\)/,
  );

  // Reopened, then closed again with a reason the meta line has to wrap.
  expect(
    (await request.post(`/api/tasks/${id}/reopen`, { data: {} })).ok(),
  ).toBe(true);
  expect(
    (
      await request.post(`/api/tasks/${id}/close`, {
        data: { reason: LONG_REASON },
      })
    ).ok(),
  ).toBe(true);
  // Closing it once more changes nothing, and records nothing.
  expect(
    (
      await request.post(`/api/tasks/${id}/close`, {
        data: { reason: "closing again" },
      })
    ).ok(),
  ).toBe(true);

  for (const vp of VIEWPORTS) {
    await page.setViewportSize(vp);
    await page.goto(`/tasks/${id}`);
    const steps = page.locator(".timeline-item .timeline-title");
    await expect(steps.filter({ hasText: /^Reopened$/ })).toHaveCount(1);
    // Two closes, both attributed (alongside Proposed and Reopened): the
    // no-op close added no fifth step.
    await expect(steps.filter({ hasText: /^Closed$/ })).toHaveCount(2);
    await expect(meta).toHaveCount(4);
    await expect(page.getByText("closing again")).toHaveCount(0);

    const line = meta.filter({ hasText: "pull/104" });
    await line.scrollIntoViewIfNeeded();
    const item = line.locator(
      "xpath=ancestor::*[contains(concat(' ', normalize-space(@class), ' '), ' timeline-item ')][1]",
    );
    const lb = await line.boundingBox();
    const ib = await item.boundingBox();
    expect(
      lb.x,
      `${vp.label}: meta line starts inside its item`,
    ).toBeGreaterThanOrEqual(ib.x - 0.5);
    expect(
      lb.x + lb.width,
      `${vp.label}: meta line ends inside its item`,
    ).toBeLessThanOrEqual(ib.x + ib.width + 0.5);
    expect(
      lb.y + lb.height,
      `${vp.label}: meta line inside its item`,
    ).toBeLessThanOrEqual(ib.y + ib.height + 0.5);
    // The box is sized by its item either way; it is the text that ran
    // out of it.
    const [scrollWidth, clientWidth] = await line.evaluate((e) => [
      e.scrollWidth,
      e.clientWidth,
    ]);
    expect(
      scrollWidth,
      `${vp.label}: the reason wraps rather than overflowing`,
    ).toBeLessThanOrEqual(clientWidth + 1);
  }
});
