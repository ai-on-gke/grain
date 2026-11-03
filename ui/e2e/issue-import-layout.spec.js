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

// The GitHub issue importer's two surfaces (model.RepoConfig.IssueImport):
// the "Imported" status and "imported" chip on the task the demo seeds
// for an imported issue nobody has approved, and the import section on
// the repository's Agents tab. Neither may push the page sideways, and
// the chip sits inside its row with its label centred on it.

import { expect, test } from "./fixtures.js";

const TITLE = "[#42] Search results drop the last page";

async function expectNoOverflow(page) {
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
}

async function expectInside(inner, outer) {
  const a = await inner.boundingBox();
  const b = await outer.boundingBox();
  expect(a && b).toBeTruthy();
  expect(a.x).toBeGreaterThanOrEqual(b.x - 1);
  expect(a.x + a.width).toBeLessThanOrEqual(b.x + b.width + 1);
  expect(a.y).toBeGreaterThanOrEqual(b.y - 1);
  expect(a.y + a.height).toBeLessThanOrEqual(b.y + b.height + 1);
}

for (const viewport of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
]) {
  test(`An unapproved imported issue reads as Imported (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const tasks = await (await page.request.get("/api/tasks")).json();
    const list = Array.isArray(tasks) ? tasks : tasks.tasks || [];
    const seeded = list.find((t) => t.title === TITLE);
    expect(seeded, "the demo's imported task").toBeTruthy();
    expect(seeded.state).toBe("proposed");
    expect(seeded.imported).toBe(true);
    expect(seeded.awaitingImportApproval).toBe(true);
    expect(seeded.origin).toBe("acme/widgets#42");

    await page.goto("/");
    const row = page.locator(".task-row").filter({ hasText: TITLE });
    await expect(row).toHaveCount(1);
    await expect(row.getByRole("img", { name: "Imported" })).toBeVisible();
    const chip = row.locator(".chip-imported");
    await expect(chip).toBeVisible();
    await expect(chip).toHaveText("imported");
    await expect(chip).toHaveAttribute(
      "title",
      "imported from GitHub issue acme/widgets#42",
    );
    await expectInside(chip, row);
    const c = await chip.boundingBox();
    const l = await chip.locator(".MuiChip-label").boundingBox();
    expect(
      Math.abs(l.y + l.height / 2 - (c.y + c.height / 2)),
    ).toBeLessThanOrEqual(1.5);
    await expectNoOverflow(page);
  });

  test(`The repository's issue import settings fit the Agents tab (${viewport.name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/repos/acme/widgets/settings/agents");
    const section = page.getByTestId("repo-issue-import");
    await expect(section).toBeVisible();
    const enable = section.getByLabel(
      "Import open GitHub issues as tasks in acme/widgets",
    );
    await expect(enable).not.toBeChecked();
    await expect(
      section.getByLabel(
        "Queue imported issues in acme/widgets without approval",
      ),
    ).not.toBeChecked();
    // Off, there is nothing to import now.
    const now = section.getByRole("button", { name: "Import issues now" });
    await expect(now).toBeDisabled();
    for (const field of ["Source repository", "Label", "Every (minutes)"]) {
      await expectInside(section.getByLabel(field), section);
    }
    await expectInside(now, section);
    await expectNoOverflow(page);
  });
}
