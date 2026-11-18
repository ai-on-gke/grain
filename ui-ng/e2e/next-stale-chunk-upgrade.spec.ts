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

test("Save-Data skips idle chunk preloading and a 404 lazy route chunk offers Reload onto the destination @phone @dark", async ({
  page,
  axe,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, "connection", {
      configurable: true,
      get: () => ({ saveData: true }),
    });
  });

  await page.goto("inbox");
  await expect(
    page.getByRole("region", { name: "Decision queue" }),
  ).toBeVisible();

  const countChunks = () =>
    page.evaluate(
      () =>
        performance
          .getEntriesByType("resource")
          .filter((e) => /\/next\/chunk-[^/]+\.js$/.test(e.name)).length,
    );

  const bootChunks = await countChunks();
  // Wait past ROUTE_PRELOAD_DELAY_MS (1500ms) and verify neither route chunks
  // nor shell @defer chunks prefetched under Save-Data.
  await page.waitForTimeout(2200);
  expect(await countChunks()).toBe(bootChunks);

  // Simulate a daemon upgrade replacing bundle hashes while this tab stayed
  // open: lazy route chunks from the old index.html now answer 404 no-store.
  let missingChunk = true;
  await page.route("**/next/chunk-*.js", async (route) => {
    if (missingChunk) {
      await route.fulfill({
        status: 404,
        contentType: "text/plain; charset=utf-8",
        headers: { "Cache-Control": "no-store" },
        body: "404 page not found\n",
      });
      return;
    }
    await route.continue();
  });

  try {
    const vp = page.viewportSize() ?? { width: 1440, height: 900 };
    const tasksLink =
      vp.width < 600
        ? page
            .getByRole("navigation", {
              name: "Primary destinations (compact)",
            })
            .getByRole("link", { name: "Tasks" })
        : page
            .getByRole("navigation", { name: "Primary destinations" })
            .getByRole("link", { name: /^Tasks/ });

    await tasksLink.click();

    const snack = page.getByTestId("snackbar");
    await expect(snack).toBeVisible();
    await expect(snack).toContainText("Page could not load");
    const reloadBtn = snack.getByRole("button", { name: "Reload" });
    await expect(reloadBtn).toBeVisible();

    const box = await snack.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(vp.width);
    expect(box!.y + box!.height).toBeLessThanOrEqual(vp.height);

    await expect(page.getByTestId("nav-progress")).toHaveCount(0);
    expect(await axe()).toEqual([]);

    // Once the browser reloads against the server's current index.html, chunks
    // resolve and Reload lands straight on /next/tasks.
    missingChunk = false;
    await reloadBtn.click();
    await expect(page).toHaveURL(/\/next\/tasks(\?|$)/);
    await expect(page.getByTestId("backlog-toolbar")).toBeVisible();
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
  }
});
