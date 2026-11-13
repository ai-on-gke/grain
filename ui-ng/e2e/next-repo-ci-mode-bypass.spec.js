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

// The /next/ repository page's CI mode pill and base-branch repair
// bypass switch, over a real `grain demo` (2026-11-13 repository
// walkthrough, docs/test-runs/2026-11-13-repository-release-memory-ui.md):
// the pill offered "Both" and "Off", which the server refuses, and read
// a repo on "none" as Grain CI; the bypass switch took the human gate off
// with no question, where the classic page asks.

import { test, expect } from "./fixtures.js";

const settingsOf = async (page) => {
  const res = await page.request.get("/api/repos/acme/widgets/settings");
  expect(res.ok()).toBe(true);
  return res.json();
};

test("CI mode pill offers grain, github and none, and a repo on none reads No CI after a reload", async ({
  page,
}) => {
  const before = (await settingsOf(page)).ciMode;
  try {
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto("/next/repos/acme/widgets/ci");
      const pill = page.getByTestId("select-repo-ci-mode");
      await pill.click();
      await expect(page.getByRole("option")).toHaveText([
        "Grain CI (.grain/workflows)",
        "GitHub Actions checks",
        "No CI (merge without waiting)",
      ]);
      const menu = await page
        .getByTestId("select-repo-ci-mode-menu")
        .boundingBox();
      expect(menu.x + menu.width).toBeLessThanOrEqual(width);
      await page.getByRole("option", { name: /^No CI/ }).click();
      await expect.poll(async () => (await settingsOf(page)).ciMode).toBe(
        "none",
      );
      await page.reload();
      await expect(pill).toContainText("No CI");
      await pill.click();
      await page.getByRole("option", { name: /^GitHub/ }).click();
      await expect.poll(async () => (await settingsOf(page)).ciMode).toBe(
        "github",
      );
    }
  } finally {
    await page.request.put("/api/repos/acme/widgets/ci-mode", {
      data: { ciMode: before },
    });
  }
});

test("turning on the base-repair bypass asks first; turning it off does not", async ({
  page,
}) => {
  const key = "baseRepairBypassesHumanGates";
  expect((await settingsOf(page))[key]).toBe(false);
  try {
    await page.goto("/next/repos/acme/widgets/merge-queue");
    const sw = page.getByRole("switch", {
      name: "Base-branch repairs bypass human review gates",
    });
    await expect(sw).toHaveAttribute("aria-checked", "false");

    await sw.click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toContainText(
      "Let base-branch repair PRs in acme/widgets merge without a human",
    );
    await page.getByTestId("confirm-cancel").click();
    await expect(dialog).toBeHidden();
    await expect(sw).toHaveAttribute("aria-checked", "false");
    expect((await settingsOf(page))[key]).toBe(false);

    await sw.click();
    await page.getByTestId("confirm-accept").click();
    await expect(sw).toHaveAttribute("aria-checked", "true");
    expect((await settingsOf(page))[key]).toBe(true);

    await sw.click();
    await expect(dialog).toBeHidden();
    await expect(sw).toHaveAttribute("aria-checked", "false");
    await expect.poll(async () => (await settingsOf(page))[key]).toBe(false);

    // Settings -> Repositories asks the same question.
    await page.goto("/next/settings/repositories");
    const row = page.getByRole("switch", {
      name: "Base repair bypasses human gates on acme/widgets",
    });
    await row.click();
    await expect(dialog).toContainText("acme/widgets");
    await page.getByTestId("confirm-cancel").click();
    await expect(row).toHaveAttribute("aria-checked", "false");
    expect((await settingsOf(page))[key]).toBe(false);
  } finally {
    await page.request.put(
      "/api/repos/acme/widgets/base-repair-bypasses-human-gates",
      { data: { [key]: false } },
    );
  }
});
