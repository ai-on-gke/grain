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

// A deployment default the capability policy denies, on a task filed
// from + New task (task 2283, from docs/test-runs/
// 2026-11-01-capability-api-presets.md §4): the server files the task
// without it and says so in its answer (droppedDefaults), and the page
// says so in a warning toast -- the task's thread was the only place it
// was said before, and nobody who has just pressed Create is reading it.

const JOURNAL = "https://grain.dev/capabilities/grain#journal";

test("filing without a default the policy denies says so in a toast", async ({
  page,
  request,
}) => {
  test.setTimeout(120_000);
  const settings = await (await request.get("/api/settings")).json();
  const policy = await (await request.get("/api/capability-policy")).json();
  try {
    expect(
      (
        await request.put("/api/settings", {
          data: { defaultCapabilities: [`${JOURNAL}={}`] },
        })
      ).ok(),
    ).toBe(true);
    expect(
      (
        await request.put("/api/capability-policy", {
          data: { policy: { rules: [{ capability: JOURNAL, deny: true }] } },
        })
      ).ok(),
    ).toBe(true);

    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: width > 600 ? 1000 : 844 });
      await page.goto("/");
      await page.getByRole("button", { name: /^\+? ?New task$/ }).click();
      await expect(
        page.getByRole("heading", { name: "New task" }),
      ).toBeVisible();
      await page.getByLabel("Title").fill(`E2E dropped default ${width}`);
      await page.getByLabel(/^Repos/).selectOption("none");
      const filing = page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/tasks") && r.request().method() === "POST",
      );
      await page.getByRole("button", { name: "Create task" }).click();
      const res = await filing;
      expect(res.status()).toBe(201);
      const task = await res.json();
      expect(task.droppedDefaults).toEqual([
        { grant: JOURNAL, reason: expect.stringContaining("rule 1: deny") },
      ]);

      const toast = page.getByTestId("dropped-defaults-notice");
      await expect(toast).toBeVisible();
      await expect(toast).toContainText(
        `Task #${task.id} was filed without a default grant`,
      );
      await expect(toast).toContainText(JOURNAL);
      // On screen and centred, its long grant URL wrapped inside it
      // rather than pushing it past either edge at a phone's width.
      const box = await toast.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(box.y + box.height).toBeLessThanOrEqual(width > 600 ? 1000 : 844);
      expect(Math.abs(box.x + box.width / 2 - width / 2)).toBeLessThanOrEqual(
        2,
      );
      expect(
        await toast.evaluate((el) => el.scrollWidth - el.clientWidth),
      ).toBeLessThanOrEqual(1);

      const dir = process.env.SHOTS_DIR;
      if (dir) {
        for (const theme of ["light", "dark"]) {
          await page.evaluate(
            (t) => localStorage.setItem("grain.themeMode", t),
            theme,
          );
          await page.emulateMedia({ colorScheme: theme });
          await page.waitForTimeout(200);
          await page.screenshot({
            path: `${dir}/dropped-defaults-${width}-${theme}.png`,
          });
          await toast.screenshot({
            path: `${dir}/dropped-defaults-toast-${width}-${theme}.png`,
          });
        }
        await page.evaluate(() => localStorage.removeItem("grain.themeMode"));
        await page.emulateMedia({ colorScheme: "light" });
      }

      await toast.getByRole("button", { name: "Close" }).click();
      await expect(toast).toHaveCount(0);
    }
  } finally {
    await request.put("/api/capability-policy", {
      data: { policy: policy.policy || {} },
    });
    await request.put("/api/settings", {
      data: { defaultCapabilities: settings.defaultCapabilities || [] },
    });
  }
});
