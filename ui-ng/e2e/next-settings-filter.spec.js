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

// Filter settings finds a section by any field label, switch name or
// card title it shows (2026-11-16 Settings walkthrough: 139 of the 166
// labels the demo's sections showed found nothing, "Branch name
// template" among them). A label added to a section without its entry
// in settings-field-labels.ts fails here.

const SECTIONS = [
  "models",
  "sandboxes",
  "hostds",
  "repositories",
  "git",
  "ci",
  "config-repo",
  "tasks",
  "agents",
  "capabilities",
  "memory",
  "access",
  "audit",
  "general",
  "preferences",
  "cli",
  "caches",
  "engine",
  "registry",
  "upgrade",
];

const LABELS = [
  ".settings-stage .s-label",
  ".settings-stage .s-card-title",
  ".settings-stage .s-toggle-name",
]
  .map((s) => `${s}:visible`)
  .join(", ");

test("every label a section shows finds that section in Filter settings", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const filter = page.getByTestId("settings-filter-input");
  const misses = [];
  for (const section of SECTIONS) {
    await page.goto(`/next/settings/${section}`);
    await expect(page.getByTestId("settings-skeleton")).toHaveCount(0);
    // A section's cards arrive with its own requests: wait for them to
    // stop arriving before reading the labels.
    await page.waitForLoadState("networkidle");
    const labels = new Set(
      (await page.locator(LABELS).allInnerTexts())
        .map((l) => l.trim().replace(/\s+/g, " ").replace(/:$/, ""))
        .filter((l) => l && l.length < 70),
    );
    const finds = async (text) => {
      await filter.fill(text);
      return (await page.getByTestId(`settings-nav-${section}`).count()) > 0;
    };
    for (const label of labels) {
      // A title can carry live data in brackets ("Named GitHub
      // personal/bot tokens (default, <connection>)"): the words in
      // front of it are what has to find the section.
      const fixed = label.replace(/\s+\(.*$/, "");
      if (!(await finds(label)) && !(fixed && (await finds(fixed)))) {
        misses.push(`${section}: ${label}`);
      }
    }
    await filter.fill("");
  }
  expect(misses).toEqual([]);
});

test("a field label lists its section, and choosing it opens the section", async ({
  page,
}) => {
  await page.goto("/next/settings/general");
  await expect(page.getByTestId("settings-section-heading")).toHaveText(
    "General",
  );
  await page.keyboard.press("/");
  await expect(page.getByTestId("settings-filter-input")).toBeFocused();
  await page.keyboard.type("branch name template");
  await expect(page.locator(".settings-nav-item")).toHaveText(["Tasks"]);
  await page.getByTestId("settings-nav-tasks").click();
  await expect(page).toHaveURL(/\/next\/settings\/tasks$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/next\/settings\/general$/);
});
