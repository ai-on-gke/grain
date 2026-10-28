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

// grain's built-in "Set up a GKE sandbox cluster" template
// (model.FileableSystemTemplates) on /processes, over `grain demo`:
// listed with the System chip inside its row, opened read-only, and
// filed from Use… with no repo box. Measured at desktop and phone widths
// because the chip row is the part a narrow screen could push out of the
// row.
const NAME = "Set up a GKE sandbox cluster";

function gkeRow(page) {
  return page
    .locator('.process-row[data-kind="template"]')
    .filter({ has: page.getByRole("link", { name: NAME, exact: true }) });
}

for (const [label, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test.describe(`the built-in GKE template (${label})`, () => {
    test.use({ viewport });

    test("is listed with the System chip inside its row", async ({ page }) => {
      await page.goto("/processes");
      const row = gkeRow(page);
      await expect(row).toBeVisible();
      const chip = row.locator(".MuiChip-root", { hasText: "🔒 System" });
      await expect(chip).toBeVisible();
      const r = await row.boundingBox();
      const c = await chip.boundingBox();
      expect(c.x).toBeGreaterThanOrEqual(r.x);
      expect(c.x + c.width).toBeLessThanOrEqual(r.x + r.width + 1);
      expect(c.y).toBeGreaterThanOrEqual(r.y);
      expect(c.y + c.height).toBeLessThanOrEqual(r.y + r.height + 1);
      // Nothing on the page is pushed wider than the viewport.
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
    });

    test("opens read-only", async ({ page }) => {
      await page.goto("/processes/templates/system:gke-sandbox-cluster");
      await expect(page.locator(".system-notice")).toContainText(
        "built into grain",
      );
      await expect(page.getByRole("button", { name: "Save" })).toBeDisabled();
      await expect(page.getByRole("button", { name: "Delete" })).toBeDisabled();
      await expect(
        page.getByRole("textbox", { name: "Name", exact: true }),
      ).toBeDisabled();
    });

    test("files from Use… with no repo box", async ({ page }) => {
      await page.goto("/processes");
      await gkeRow(page).getByRole("button", { name: "Use…" }).click();
      const dialog = page.getByRole("dialog", {
        name: "File a task from a template",
      });
      await expect(dialog).toBeVisible();
      await expect(dialog.locator(".use-template-repoless")).toBeVisible();
      await expect(dialog.getByText("owner/name")).toHaveCount(0);
      await expect(dialog.getByLabel("GCP sandbox project ID")).toBeVisible();
      await expect(dialog.getByText(/files a live chat/)).toBeVisible();
      // The notice sits inside the dialog's own width.
      const d = await dialog.boundingBox();
      const n = await dialog.locator(".use-template-repoless").boundingBox();
      expect(n.x).toBeGreaterThanOrEqual(d.x);
      expect(n.x + n.width).toBeLessThanOrEqual(d.x + d.width + 1);
      await expect(
        dialog.getByRole("button", { name: "Create task" }),
      ).toBeEnabled();
    });
  });
}
