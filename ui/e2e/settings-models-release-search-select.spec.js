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
import { chips, chooseOption, optionValues } from "./searchSelect.js";

// Follow-up 2 of 3 to grain/task-2879 (grain/task-2911): Settings, models,
// connections and release pickers over growable lists use SearchSelect /
// searchFilter, fit desktop, tablet and phone viewports without horizontal
// overflow, and align their open listboxes under their inputs.
test.use({ demoArgs: ["-github-sim", "-qualification", "-observability"] });

test.describe("SearchSelect across Settings, connections, metrics and releases", () => {
  test("searches and picks across tablet Section, Git connections, Metrics week and Releases", async ({
    page,
    request,
  }) => {
    const planUrl = "/api/repos/acme/widgets/qualification-plan";
    const originalPlan = await (await request.get(planUrl)).json();
    try {
      // Tablet Section picker (SettingsPage.jsx) groups and searches sections.
      await page.setViewportSize({ width: 768, height: 1024 });
      await page.goto("/settings/engine");
      const sectionPicker = page.getByRole("combobox", { name: "Section" });
      await expect(sectionPicker).toBeVisible();
      await sectionPicker.click();
      await sectionPicker.fill("git");
      expect(await optionValues(sectionPicker)).toContain("git");
      await chooseOption(sectionPicker, "git");
      await expect(page).toHaveURL(/\/settings\/git$/);

      // Per-repo Git connection picker (GitConnections.jsx).
      await page.setViewportSize({ width: 1440, height: 900 });
      const repoConn = page
        .getByTestId("repo-connection-acme/widgets")
        .getByRole("combobox", { name: "Connection" });
      await expect(repoConn).toBeVisible();
      expect(await optionValues(repoConn)).toContain("");

      // Metrics weekly release-notes picker aligns with its heading.
      await page.goto("/metrics");
      const weeklyHeading = page.getByRole("heading", { name: "Weekly" });
      const weekPicker = page.getByRole("combobox", { name: "week" });
      await expect(weekPicker).toBeVisible();
      const headingBox = await weeklyHeading.boundingBox();
      const weekWrapBox = await weekPicker
        .locator(
          "xpath=ancestor::div[contains(@class,'MuiOutlinedInput-root')]",
        )
        .boundingBox();
      expect(
        Math.abs(
          headingBox.y +
            headingBox.height / 2 -
            (weekWrapBox.y + weekWrapBox.height / 2),
        ),
      ).toBeLessThan(6);

      // Repo releases: Release picker and Qualification plan Template picker.
      await page.goto("/repos/acme/widgets/releases");
      const releasePicker = page.getByRole("combobox", { name: "Release" });
      await expect(releasePicker).toBeVisible();
      expect((await optionValues(releasePicker)).length).toBeGreaterThan(0);

      await page.getByRole("button", { name: "Add item" }).click();
      const templatePicker = page
        .getByRole("combobox", { name: "Template" })
        .last();
      await templatePicker.click();
      await templatePicker.fill("audit sec");
      const listbox = page.getByRole("listbox");
      await expect(listbox).toBeVisible();
      await expect(listbox.locator(".search-select-hit").first()).toBeVisible();
      const tmplBox = await templatePicker
        .locator(
          "xpath=ancestor::div[contains(@class,'MuiOutlinedInput-root')]",
        )
        .boundingBox();
      const lbBox = await listbox.boundingBox();
      expect(Math.abs(lbBox.x - tmplBox.x)).toBeLessThanOrEqual(2);
      expect(
        lbBox.y >= tmplBox.y + tmplBox.height - 2 ||
          lbBox.y + lbBox.height <= tmplBox.y + 2,
      ).toBe(true);
      expect(lbBox.width).toBeGreaterThanOrEqual(tmplBox.width - 2);
      await chooseOption(templatePicker, { label: "security-audit" });
      await expect(templatePicker).toHaveValue("security-audit");
    } finally {
      await request.put(planUrl, {
        data: {
          requireApproval: Boolean(originalPlan.requireApproval),
          autoPromote: Boolean(originalPlan.autoPromote),
          items: (originalPlan.items || []).map((it) => ({
            templateId: it.templateId,
            repeat: it.repeat || 1,
            dependsOn: it.dependsOn || [],
          })),
        },
      });
    }
  });

  for (const width of [1440, 390]) {
    test(`converted pickers stay inside a ${width}px viewport without horizontal overflow`, async ({
      page,
    }) => {
      await page.setViewportSize({ width, height: 900 });
      for (const path of [
        "/settings/models",
        "/settings/git",
        "/settings/preferences",
        "/settings/upgrade",
        "/repos",
        "/repos/acme/widgets/releases",
        "/metrics",
      ]) {
        await page.goto(path);
        await expect(page.locator("main, .app-shell").first()).toBeVisible();
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= window.innerWidth,
          ),
          `horizontal overflow on ${path} at ${width}px`,
        ).toBe(true);
        const boxes = await page
          .locator(".MuiAutocomplete-root")
          .evaluateAll((els) =>
            els
              .map((el) => el.getBoundingClientRect())
              .filter((r) => r.width > 0 && r.height > 0)
              .map((r) => ({ left: r.left, right: r.right, width: r.width })),
          );
        for (const b of boxes) {
          expect(b.left, `${path} at ${width}px`).toBeGreaterThanOrEqual(0);
          expect(b.right, `${path} at ${width}px`).toBeLessThanOrEqual(
            width + 1,
          );
        }
      }
    });
  }
  // The legacy-capability checklists on Settings → Tasks and a repo's CI
  // and Agents tabs: searchable, a checkbox on every row, the chosen
  // capabilities as chips inside the box, and the list hung under it.
  for (const width of [1440, 390]) {
    test(`capability checklists search and fit at ${width}px`, async ({
      page,
    }) => {
      await page.setViewportSize({ width, height: 900 });
      // Each search names a capability by its words in any order.
      for (const [path, tab, name, query, found] of [
        [
          "/settings/tasks",
          null,
          "Default legacy capabilities",
          "debug self",
          "self-debug",
        ],
        [
          "/repos/acme/widgets",
          "CI",
          "CI capabilities",
          "publish package",
          "https://grain.dev/capabilities/release#package-publish={}",
        ],
        [
          "/repos/acme/widgets",
          "Agents",
          "Default capabilities",
          "debug self",
          "self-debug",
        ],
      ]) {
        await page.goto(path);
        if (tab) await page.getByRole("tab", { name: tab }).click();
        const box = page.getByRole("combobox", { name, exact: true });
        await expect(box).toBeVisible();
        const field = box.locator(
          "xpath=ancestor::div[contains(@class,'MuiOutlinedInput-root')]",
        );
        const fieldBox = await field.boundingBox();
        expect(fieldBox.x, `${name} at ${width}px`).toBeGreaterThanOrEqual(0);
        expect(fieldBox.x + fieldBox.width).toBeLessThanOrEqual(width + 1);
        for (const chip of await chips(box).all()) {
          const c = await chip.boundingBox();
          expect(c.x).toBeGreaterThanOrEqual(fieldBox.x - 1);
          expect(c.x + c.width).toBeLessThanOrEqual(
            fieldBox.x + fieldBox.width + 1,
          );
        }

        await box.click();
        const listbox = page.getByRole("listbox");
        await expect(listbox).toBeVisible();
        await expect(
          listbox.getByRole("option").first().locator(".MuiCheckbox-root"),
        ).toBeVisible();
        // Measured again: opening the list can scroll the page.
        const open = await field.boundingBox();
        const lb = await listbox.boundingBox();
        expect(Math.abs(lb.x - open.x)).toBeLessThanOrEqual(2);
        expect(
          lb.y >= open.y + open.height - 2 || lb.y + lb.height <= open.y + 2,
          "the list hangs under (or, short of room, over) its box",
        ).toBe(true);
        expect(lb.x + lb.width).toBeLessThanOrEqual(width + 1);

        await box.fill(query);
        expect(await optionValues(box)).toContain(found);
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= window.innerWidth,
          ),
          `horizontal overflow on ${path} at ${width}px`,
        ).toBe(true);
      }
    });
  }
});
