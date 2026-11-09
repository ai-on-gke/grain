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

// /sessions and /session/<id> when GET /api/console-sessions fails before
// sessions have loaded (task 3721, 2026-11-09 responsive navigation and
// live-session walkthrough): once the five-second error snackbar fades,
// the page must say the sessions could not be loaded rather than claiming
// "No interactive sessions yet.", and recover when Retry succeeds.

import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

test("/sessions and /session/director report a failed /api/console-sessions load instead of claiming no sessions exist, then recover on Retry", async ({
  page,
}) => {
  await page.route("**/api/console-sessions", (route) => {
    if (route.request().method() === "GET") {
      return route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          error: "console session manager is still starting",
        }),
      });
    }
    return route.continue();
  });

  const expectedError =
    "The sessions could not be loaded (console session manager is still starting).";

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/sessions");
  await expect(page.getByText(expectedError)).toBeVisible();

  // Wait for the transient 5s error banner to clear so the standing state
  // on the page is what remains.
  await expect(page.locator(".error-banner")).toHaveCount(0, {
    timeout: 10_000,
  });

  await expect(page.getByText("No interactive sessions yet.")).toHaveCount(0);
  await expect(page.getByText(expectedError)).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry" })).toBeVisible();

  // Check bounding boxes and WCAG AA contrast at desktop (1440x900) and
  // phone (390x844) in both light and dark themes.
  for (const scheme of ["light", "dark"]) {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.emulateMedia({ colorScheme: scheme });
    await page.evaluate(
      (t) => localStorage.setItem("grain.themeMode", t),
      scheme,
    );
    await page.goto("/sessions");
    await expect(page.getByText(expectedError)).toBeVisible();

    const axe = await new AxeBuilder({ page })
      .withRules(["color-contrast"])
      .include(".tasks-load-error")
      .analyze();
    expect(axe.violations, `contrast violations in ${scheme}`).toEqual([]);

    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await expect(page.locator(":root")).toHaveAttribute(
        "data-phone",
        viewport.width < 600 ? "true" : "false",
      );
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(
        overflow,
        `horizontal overflow at ${viewport.width}px (${scheme})`,
      ).toBeLessThanOrEqual(0);
      const box = await page.locator(".tasks-load-error").evaluate((el) => {
        const rect = el.getBoundingClientRect();
        const parent = el.parentElement.getBoundingClientRect();
        return {
          width: rect.width,
          height: rect.height,
          insideLeft: rect.left >= parent.left - 1,
          insideRight: rect.right <= parent.right + 1,
        };
      });
      expect(box.width).toBeGreaterThan(100);
      expect(box.height).toBeGreaterThan(20);
      expect(box.insideLeft).toBe(true);
      expect(box.insideRight).toBe(true);
    }
  }

  // Deep-linking to /session/director while /api/console-sessions is 503
  // shows the same load failure (not "may have been deleted" and not
  // "No interactive sessions yet."), and clicking Retry opens Director.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/session/director");
  await expect(page.getByText(expectedError)).toBeVisible();
  await expect(page.getByText(/may have been deleted/i)).toHaveCount(0);
  await expect(page.getByText("No interactive sessions yet.")).toHaveCount(0);

  await page.unroute("**/api/console-sessions");
  await page.getByRole("button", { name: "Retry" }).click();

  await expect(page.getByText(expectedError)).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: /Edit Director capabilities/i }),
  ).toBeVisible();
});
