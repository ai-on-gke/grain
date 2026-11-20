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

// Regression coverage for the 2026-11-20 Settings walkthrough (task 7548):
// a failed Save changes names the field it could not save in the section
// header and the error banner (by its label, never its JSON key) and offers
// Retry; the Config repo file viewer says why it has no files to show; and
// General's Display timezone says it is a browser-only preference.

import { test, expect } from "./fixtures.js";

test.describe("Settings save failures and the config repo file viewer", () => {
  test("names the field a save failed on, retries it, and labels server refusals", async ({
    page,
  }) => {
    test.setTimeout(90_000);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/next/settings/tasks");
    const status = page.getByTestId("settings-save-status");
    await expect(status).toHaveText("All changes saved");

    const depth = page.locator("#t-depth");
    const original = await depth.inputValue();
    const changed = String(Number(original || "1") + 1);

    // The first POST /api/settings fails the way an unavailable server
    // does; the retry passes through to the real one.
    let failNext = true;
    await page.route("**/api/settings", async (route) => {
      if (route.request().method() === "POST" && failNext) {
        failNext = false;
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ error: "temporarily unavailable" }),
        });
        return;
      }
      await route.fallback();
    });

    try {
      await depth.fill(changed);
      await expect(status).toHaveText("Unsaved changes");
      await page.getByTestId("settings-save-btn").click();
      await expect(status).toHaveText(
        "Couldn't save Max unreviewed follow-up depth",
      );
      const banner = page.getByTestId("settings-error-banner");
      await expect(banner).toContainText("temporarily unavailable");
      await banner.getByTestId("settings-retry-save-btn").click();
      await expect(status).toHaveText("All changes saved");
      await expect(banner).toBeHidden();
      await page.reload();
      await expect(page.locator("#t-depth")).toHaveValue(changed);

      // A refusal that opens with the setting's JSON key is shown under
      // the field's label instead.
      const branch = page.locator("#t-branch");
      await branch.fill("no-placeholder");
      await page.getByTestId("settings-save-btn").click();
      await expect(status).toHaveText("Couldn't save Branch name template");
      await expect(banner).toContainText("Branch name template:");
      await expect(banner).not.toContainText("taskBranchTemplate");
      await page.getByTestId("settings-discard-btn").click();
      await expect(status).toHaveText("All changes saved");
      await expect(banner).toBeHidden();
    } finally {
      await page.unroute("**/api/settings");
      // Put the shared demo back the way the worker found it.
      await page.goto("/next/settings/tasks");
      const box = page.locator("#t-depth");
      if ((await box.inputValue()) !== original) {
        await box.fill(original);
        await page.getByTestId("settings-save-btn").click();
        await expect(status).toHaveText("All changes saved");
      }
    }
  });

  test("says why the config repo file viewer has nothing to show, and that General's timezone is this browser's", async ({
    page,
    axe,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    // The default demo manages no state repository, so the file list
    // answers 501; the viewer says so rather than "Select a config file".
    await page.goto("/next/settings/config-repo");
    const err = page.getByTestId("config-files-error");
    await expect(err).toContainText(
      "Couldn't list the config repository's files",
    );
    await expect(err).toContainText("does not manage a state repository");
    await expect(page.getByTestId("card-config-files")).not.toContainText(
      "Select a config file",
    );

    await page.goto("/next/settings/general");
    await expect(page.getByTestId("general-timezone-hint")).toContainText(
      "This browser only",
    );
    expect(await axe()).toEqual([]);
  });
});
