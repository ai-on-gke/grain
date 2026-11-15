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

// The sign-in verifier in front of grain's allowlist unable to check the
// caller for a while (pkg/ui's writeVerifierUnavailable: 503 with code
// verifier_unavailable). Every API read is refused that way here, in a
// real browser: for the first 30 seconds the page stays as it was, with
// no toast, no "cannot be reached" banner and no "not refreshing"; past
// that one banner names sign-in verification, above the top bar, and it
// goes on the first answer.
//
// SHOTS_DIR, when set, is where the banner is shot, for a reviewer.

import fs from "node:fs";
import path from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

const API = /\/api\//;
const verifierBody = JSON.stringify({
  error:
    "sign-in verification could not check your identity right now; retrying shortly (ref 0123456789ab)",
  code: "verifier_unavailable",
  reason: "tonic_unreachable",
  retryAfter: 5,
  ref: "0123456789ab",
});

test.describe.configure({ timeout: 90_000 });

for (const { width, height, name } of [
  { width: 1440, height: 900, name: "desktop" },
  { width: 390, height: 844, name: "phone" },
]) {
  for (const colorScheme of ["light", "dark"]) {
    test(`a sustained verifier outage is named as sign-in verification, not a disconnection (${name}, ${colorScheme})`, async ({
      page,
    }) => {
      await page.emulateMedia({ colorScheme });
      await page.setViewportSize({ width, height });
      await page.goto("/");
      await expect(page.locator(".task-row").first()).toBeVisible();

      const banner = page.getByTestId("verifier-banner");
      await page.route(API, (route) =>
        route.fulfill({
          status: 503,
          headers: { "Retry-After": "5" },
          contentType: "application/json",
          body: verifierBody,
        }),
      );
      try {
        // Well past three polls, well short of thirty seconds: quiet.
        await page.waitForTimeout(12_000);
        await expect(banner).toHaveCount(0);
        await expect(page.getByTestId("disconnected-banner")).toHaveCount(0);
        await expect(page.locator(".MuiSnackbar-root")).toHaveCount(0);
        await expect(page.getByText(/not refreshing/)).toHaveCount(0);

        await expect(banner).toBeVisible({ timeout: 30_000 });
        await expect(banner).toContainText(
          /Sign-in verification has been temporarily unable to check your identity for \d+s — retrying automatically\./,
        );
        await expect(page.getByTestId("disconnected-banner")).toHaveCount(0);
        await expect(page.getByText(/cannot be reached/)).toHaveCount(0);
        await expect(page.locator(".task-row").first()).toBeVisible();

        // Across the top of the page, above everything else, and legible.
        const box = await banner.boundingBox();
        expect(box.y).toBeLessThanOrEqual(0.5);
        expect(Math.abs(box.width - width)).toBeLessThanOrEqual(1);
        // In the flow, like DisconnectedBanner: the page -- rail, top
        // bar, phone bar -- starts below it rather than under it.
        const bar = await page.locator(".app-body").first().boundingBox();
        expect(bar.y).toBeGreaterThanOrEqual(box.y + box.height - 0.5);
        const fits = await banner.evaluate(
          (el) => el.scrollWidth <= el.clientWidth + 1,
        );
        expect(fits, "the banner overflows sideways").toBe(true);
        const axe = await new AxeBuilder({ page })
          .include('[data-testid="verifier-banner"]')
          .withRules(["color-contrast"])
          .analyze();
        expect(axe.violations).toEqual([]);

        if (process.env.SHOTS_DIR) {
          fs.mkdirSync(process.env.SHOTS_DIR, { recursive: true });
          await page.screenshot({
            path: path.join(
              process.env.SHOTS_DIR,
              `verifier-banner-${name}-${colorScheme}.png`,
            ),
          });
        }
      } finally {
        await page.unroute(API);
      }
      await expect(banner).toHaveCount(0, { timeout: 10_000 });
    });
  }
}
