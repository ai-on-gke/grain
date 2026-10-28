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

import { expect, test } from "./fixtures.js";

// A change the merge queue gave up on is listed under "Unblock a merge",
// and that row now carries the sub-case's own Retry merge beside Open
// pull request (InboxPage.jsx's merge_blocked controls). `grain demo
// -merge-rules` seeds one: "Raise the API pod memory limit", merge_failed
// and given up. The click's request is answered here rather than by the
// server so the worker's shared demo keeps its given-up change for the
// other specs that read it.

test.use({ demoArgs: ["-merge-rules"] });

const GIVEN_UP = "Raise the API pod memory limit";

for (const viewport of [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
]) {
  test(`a given-up merge offers Retry merge in the inbox at ${viewport.width}px`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/inbox");
    const group = page.locator(".inbox-group", { hasText: "Unblock a merge" });
    const item = group.locator(".inbox-item", { hasText: GIVEN_UP });
    await expect(item).toHaveCount(1);
    const retry = item.getByRole("button", { name: "Retry merge" });
    await expect(retry).toHaveCount(1);
    await expect(retry).toBeVisible();
    await expect(
      item.getByRole("link", { name: "Open pull request" }),
    ).toBeVisible();

    // In the row's action slot, on screen, and clear of the title.
    const button = await retry.boundingBox();
    const actions = await item.locator(".inbox-actions").boundingBox();
    const title = await item.getByText(GIVEN_UP).first().boundingBox();
    expect(button.x).toBeGreaterThanOrEqual(actions.x - 1);
    expect(button.x + button.width).toBeLessThanOrEqual(
      actions.x + actions.width + 1,
    );
    expect(button.x + button.width).toBeLessThanOrEqual(viewport.width);
    const overlaps =
      button.x < title.x + title.width &&
      title.x < button.x + button.width &&
      button.y < title.y + title.height &&
      title.y < button.y + button.height;
    expect(overlaps).toBe(false);

    let posted = null;
    await page.route("**/api/tasks/*/retry-merge", async (route) => {
      posted = new URL(route.request().url()).pathname;
      await route.fulfill({ status: 200, json: {} });
    });
    await retry.click();
    await expect.poll(() => posted).toMatch(/^\/api\/tasks\/\d+\/retry-merge$/);
  });
}
