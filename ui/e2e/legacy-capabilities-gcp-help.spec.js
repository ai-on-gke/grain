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

test("Legacy capabilities GCP help wraps cleanly and links to the gcp server and Other secrets", async ({
  page,
}) => {
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/settings/credentials");

    const section = page.getByTestId("legacy-capabilities-deployment-wide");
    await section.scrollIntoViewIfNeeded();
    await expect(section).toBeVisible();

    const overflow = await page.evaluate(() => {
      const sec = document.querySelector(
        '[data-testid="legacy-capabilities-deployment-wide"]',
      );
      const projHelp = document.querySelector(
        '[data-setting-id="gcpProject"] .MuiFormHelperText-root',
      );
      const emailHelp = document.querySelector(
        '[data-setting-id="gcpServiceAccountEmail"] .MuiFormHelperText-root',
      );
      const secRect = sec.getBoundingClientRect();
      const projRect = projHelp.getBoundingClientRect();
      const emailRect = emailHelp.getBoundingClientRect();
      return {
        docOverflow:
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
        secOverflow: sec.scrollWidth - sec.clientWidth,
        projRightExceedsSec: projRect.right - secRect.right,
        emailRightExceedsSec: emailRect.right - secRect.right,
      };
    });

    expect(overflow.docOverflow).toBeLessThanOrEqual(0);
    expect(overflow.secOverflow).toBeLessThanOrEqual(1);
    expect(overflow.projRightExceedsSec).toBeLessThanOrEqual(1);
    expect(overflow.emailRightExceedsSec).toBeLessThanOrEqual(1);
  }

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/settings/credentials");

  const emailField = page.locator('[data-setting-id="gcpServiceAccountEmail"]');
  await emailField.scrollIntoViewIfNeeded();

  await emailField.getByRole("link", { name: "Other secrets" }).click();
  await expect(page.locator("#other-secrets")).toBeInViewport();

  await emailField.getByRole("link", { name: "gcp capability server" }).click();
  await expect(page).toHaveURL(
    /\/settings\/capabilities\/https%3A%2F%2Fgrain\.dev%2Fcapabilities%2Fgcp$/,
  );
  await expect(
    page.getByRole("heading", { name: "gcp server", level: 4 }),
  ).toBeVisible();
});
