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

// An attempt's capability audit (CapabilityAudit.jsx) over the real
// `grain demo`: the seeded first attempt of "Add pagination to the tasks
// API" carries what its host-script grant recorded (pkg/demo's
// demoCapabilityAudit). The timeline says how many records there are,
// the attempt's overlay lists them above its transcript, argv verbatim,
// and the list fits the overlay at a phone's width rather than pushing
// it sideways.
//
// SHOTS_DIR, when set, is where the overlay is also photographed at a
// desktop and a phone width in light and dark, for a reviewer to look at.

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

async function openAudit(page, entryShot) {
  await page.goto("/");
  await page
    .locator(".task-row", { hasText: "Add pagination to the tasks API" })
    .click();
  const attempt = page.locator(".timeline-item-attempt", {
    hasText: "3 capability audit records",
  });
  await expect(attempt).toBeVisible();
  if (entryShot) {
    await attempt.scrollIntoViewIfNeeded();
    await attempt.screenshot({ path: entryShot });
  }
  await attempt.click();
  const dialog = page.locator(".MuiDialog-paper", {
    has: page.getByRole("heading", { name: "Attempt #1 transcript" }),
  });
  await expect(dialog).toBeVisible();
  const audit = dialog.getByRole("region", { name: "Capability audit" });
  await expect(audit).toBeVisible();
  return { dialog, audit };
}

test.describe("capability audit", () => {
  for (const [width, viewport] of Object.entries(VIEWPORTS)) {
    test(`lists an attempt's audit records at ${width} width`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      const { dialog, audit } = await openAudit(page);

      const entries = audit.locator(".capability-audit-entry");
      await expect(entries).toHaveCount(3);
      await expect(entries.nth(0)).toContainText("deploy-preview");
      await expect(entries.nth(0)).toContainText(
        '["--env","staging","--ref","grain/task-',
      );
      await expect(entries.nth(1)).toContainText("argument 4 holds a NUL byte");
      await expect(entries.nth(2)).toContainText("left out 1 further record");
      // The transcript is still there beneath it.
      await expect(dialog.locator(".logs-view")).toBeVisible();

      // Nothing in the list is wider than the overlay it sits in.
      const pane = await dialog.boundingBox();
      for (let i = 0; i < 3; i++) {
        const box = await entries.nth(i).boundingBox();
        expect(box.x).toBeGreaterThanOrEqual(pane.x);
        expect(box.x + box.width).toBeLessThanOrEqual(pane.x + pane.width + 1);
      }
      const list = audit.locator(".capability-audit-list");
      const overflow = await list.evaluate(
        (el) => el.scrollWidth - el.clientWidth,
      );
      expect(overflow).toBeLessThanOrEqual(1);

      const dir = process.env.SHOTS_DIR;
      if (!dir) return;
      for (const theme of ["light", "dark"]) {
        await page.evaluate(
          (t) => localStorage.setItem("grain.themeMode", t),
          theme,
        );
        await page.emulateMedia({ colorScheme: theme });
        const { dialog: shown } = await openAudit(
          page,
          `${dir}/attempt-entry-${width}-${theme}.png`,
        );
        await page.waitForTimeout(300);
        await shown.screenshot({
          path: `${dir}/attempt-audit-${width}-${theme}.png`,
        });
      }
    });
  }
});
