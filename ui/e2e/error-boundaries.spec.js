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

// grain/task-1435: one render throw used to unmount the whole app, and a
// 200 HTML answer for /api/config (a proxy or sign-in page in front of
// grain) left it on the loading splash forever. Both are driven here by
// stubbing grain's own API at the network boundary with an answer of the
// wrong shape -- the stand-in for a half-deployed server, a proxy's error
// object or a future API change -- since nothing else would make a
// healthy demo server produce one.

test(
  "a page that throws while drawing is contained, and the rail recovers it",
  { annotation: { type: "allows-render-error" } },
  async ({ page }) => {
    await page.route("**/api/schedules", (route) => {
      if (route.request().method() !== "GET") return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ not: "an array" }),
      });
    });

    await page.goto("/schedules");

    const fallback = page.getByTestId("render-error");
    await expect(fallback).toBeVisible();
    await expect(
      fallback.getByRole("heading", { name: "Something went wrong" }),
    ).toBeVisible();
    await expect(
      fallback.getByRole("button", { name: "Reload" }),
    ).toBeVisible();

    // The rail survived the page beside it, and taking it somewhere else
    // draws that page without a reload.
    await page
      .locator("aside")
      .getByRole("button", { name: /^Tasks/ })
      .click();
    await expect(fallback).toHaveCount(0);
    await expect(page.locator(".main-column")).toBeVisible();
  },
);

test("a sign-in page answering for /api/config says so instead of spinning", async ({
  page,
}) => {
  await page.route("**/api/config", (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<!doctype html><html><body>Sign in with Google</body></html>",
    }),
  );

  await page.goto("/");

  await expect(
    page.getByRole("heading", { name: "grain could not load" }),
  ).toBeVisible();
  await expect(page.getByText(/something that isn't grain/)).toBeVisible();
  await expect(page.locator(".loading-screen")).toHaveCount(0);

  // And it is not a dead end: once grain answers again, the poll that
  // kept retrying underneath brings the app up by itself.
  await page.unroute("**/api/config");
  await expect(page.locator(".main-column")).toBeVisible({ timeout: 15000 });
});
