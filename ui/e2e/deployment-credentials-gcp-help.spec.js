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

test.use({ demoArgs: ["-secrets-store"] });

test("Deployment credentials GCP help wraps cleanly and links to the gcp server and Other secrets", async ({
  page,
}) => {
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/settings/capabilities");

    const section = page.getByTestId("deployment-credentials");
    await section.scrollIntoViewIfNeeded();
    await expect(section).toBeVisible();

    const overflow = await page.evaluate(() => {
      const sec = document.querySelector(
        '[data-testid="deployment-credentials"]',
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
  await page.goto("/settings/capabilities");

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

test("Other secrets keeps retired capability secrets visible and aligned on desktop and phone", async ({
  page,
}) => {
  await page.goto("/settings/capabilities");
  try {
    for (const [path, value] of [
      ["/api/secrets/github-app/app-id", "12345"],
      ["/api/secrets/github-app/private-key", "pem-data"],
      ["/api/secrets/registry-push/value", "reg-token"],
    ]) {
      const res = await page.evaluate(
        async ([p, v]) => {
          const r = await fetch(p, {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ value: v }),
          });
          return r.status;
        },
        [path, value],
      );
      expect(res).toBe(200);
    }

    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      for (const theme of ["light", "dark"]) {
        await page.setViewportSize(viewport);
        await page.evaluate((t) => {
          localStorage.setItem("grain-theme", t);
        }, theme);
        await page.reload();
        await page.goto("/settings/capabilities#other-secrets");

        const card = page.locator("#other-secrets");
        await card.scrollIntoViewIfNeeded();
        await expect(card).toBeVisible();

        for (const secretName of ["github-app", "registry-push"]) {
          const row = card.locator(".secret-row").filter({
            has: page.locator(".secret-name", { hasText: secretName }),
          });
          await expect(row).toBeVisible();
          await expect(
            row.getByRole("button", { name: "Delete secret" }),
          ).toBeVisible();
        }

        await expect(
          card.getByRole("link", { name: "Capability servers" }),
        ).toHaveAttribute("href", "/settings/capabilities#capability-servers");

        const geometry = await page.evaluate(() => {
          const other = document.querySelector("#other-secrets");
          const otherRect = other.getBoundingClientRect();
          const rows = Array.from(other.querySelectorAll(".secret-row")).map(
            (r) => {
              const rect = r.getBoundingClientRect();
              const btn = r.querySelector("button")?.getBoundingClientRect();
              return {
                leftOffset: rect.left - otherRect.left,
                rightExceedsCard: rect.right - otherRect.right,
                btnRightExceedsRow: btn ? btn.right - rect.right : 0,
                height: rect.height,
              };
            },
          );
          return {
            docOverflow:
              document.documentElement.scrollWidth -
              document.documentElement.clientWidth,
            cardOverflow: other.scrollWidth - other.clientWidth,
            rows,
          };
        });

        expect(geometry.docOverflow).toBeLessThanOrEqual(0);
        expect(geometry.cardOverflow).toBeLessThanOrEqual(1);
        expect(geometry.rows.length).toBeGreaterThanOrEqual(2);
        for (const row of geometry.rows) {
          expect(row.leftOffset).toBeGreaterThanOrEqual(-1);
          expect(row.rightExceedsCard).toBeLessThanOrEqual(1);
          expect(row.btnRightExceedsRow).toBeLessThanOrEqual(1);
          expect(row.height).toBeGreaterThan(24);
        }
      }
    }
  } finally {
    await page.evaluate(async () => {
      await fetch("/api/secrets/github-app", { method: "DELETE" });
      await fetch("/api/secrets/registry-push", { method: "DELETE" });
    });
  }
});
