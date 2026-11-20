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

// A github-sim deployment whose Git connection lists what it reaches,
// so the Git connection column has a connection to name.
test.use({
  demoArgs: ["-as", "demo-operator", "-github-sim", "-qualification"],
});

test("Settings Git - Verify Git scope column, scope field, and connections banner", async ({
  page,
}) => {
  await page.goto("/next/settings/git");

  await expect(page.locator("th", { hasText: "Scope" })).toBeVisible();
  await expect(page.getByTestId("input-git-conn-scope")).toBeVisible();
  await expect(page.getByTestId("gh-token-default-hint")).toBeVisible();

  const verifyBtn = page.getByTestId("check-git-connections-btn");
  await verifyBtn.click();
  const banner = page.getByTestId("git-check-banner");
  await expect(banner).toBeVisible();
});

test("Settings Repositories - Add repo picker, reachability confirm dialog, and removal confirm dialog", async ({
  page,
}) => {
  await page.goto("/next/settings/repositories");

  const repoInput = page.getByTestId("input-add-repo");
  await repoInput.click();
  await repoInput.fill("acme/demo-repo");

  const addBtn = page.getByTestId("add-repo-btn");
  await addBtn.click();

  // If unreachable or policy-gated, accept via <g-confirm-dialog>
  const addDialog = page.getByTestId("confirm-dialog");
  if (await addDialog.isVisible().catch(() => false)) {
    await page.getByTestId("confirm-accept").click();
  }

  await expect(page.getByTestId("repo-row-acme/demo-repo")).toBeVisible();

  // Remove opens <g-confirm-dialog> naming per-repo settings deletion
  await page.getByTestId("remove-repo-acme/demo-repo").click();
  await expect(page.getByTestId("confirm-dialog")).toBeVisible();
  await expect(page.getByTestId("confirm-dialog")).toContainText(
    "Per-repo settings",
  );
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("repo-row-acme/demo-repo")).toHaveCount(0);
});

// The Declarations and Git connection columns (task 5870): the facts and
// wording classic Settings → Repositories gives each repository, each a
// link to the view that changes it. On a phone the table collapses into
// labelled rows rather than scrolling sideways.
test.describe("Settings Repositories - declarations and Git connection", () => {
  test("each row says whether its declarations are read and which connection reaches it @phone @dark", async ({
    page,
  }, testInfo) => {
    // What classic Settings → Repositories says for each repository, the
    // data and wording these columns carry over.
    await page.goto("/settings/repositories");
    const classicRows = page.getByTestId("settings-repo-row");
    await expect(classicRows.first()).toBeVisible();
    const classic = {};
    for (const row of await classicRows.all()) {
      const decl = row.getByTestId("settings-repo-declarations");
      const git = row.getByTestId("settings-repo-git");
      await expect(decl).not.toHaveText("…");
      await expect(git).not.toHaveText("…");
      classic[await row.getAttribute("data-repo")] = {
        declarations: (await decl.textContent()).trim(),
        git: (await git.textContent()).trim(),
      };
    }

    await page.goto("/next/settings/repositories");
    const table = page.getByRole("table", { name: "Configured repositories" });
    await expect(table).toBeVisible();
    await expect(
      table.getByRole("columnheader", { name: "Declarations" }),
    ).toHaveCount(1);
    await expect(
      table.getByRole("columnheader", { name: "Git connection" }),
    ).toHaveCount(1);

    const rows = table.locator('tbody tr[data-testid^="repo-row-"]');
    await expect(rows.first()).toBeVisible();
    const repos = [];
    for (const row of await rows.all()) {
      repos.push(
        (await row.getAttribute("data-testid")).slice("repo-row-".length),
      );
    }
    expect(repos.sort()).toEqual(Object.keys(classic).sort());

    for (const repo of repos) {
      const decl = page.getByTestId(`repo-declarations-${repo}`).locator("a");
      await expect(decl).toHaveText(classic[repo].declarations);
      expect(["Read", "Not read"]).toContain(classic[repo].declarations);
      await expect(decl).toHaveAttribute(
        "href",
        `/next/processes/declarations?repo=${encodeURIComponent(repo)}`,
      );
      const git = page.getByTestId(`repo-git-${repo}`).locator("a");
      await expect(git).toHaveText(classic[repo].git);
      expect(classic[repo].git).not.toBe("Unknown");
      await expect(git).toHaveAttribute("href", "/next/settings/git");
    }

    // The table never scrolls sideways, on any width.
    const wrap = page.locator(".s-table-wrap", { has: table });
    const overflow = await wrap.evaluate(
      (el) => el.scrollWidth - el.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      ),
    ).toBeLessThanOrEqual(1);

    const first = repos[0];
    const declCell = page.getByTestId(`repo-declarations-${first}`);
    const gitCell = page.getByTestId(`repo-git-${first}`);
    const repoCell = page.getByTestId(`repo-link-${first}`);
    const declBox = await declCell.boundingBox();
    const gitBox = await gitCell.boundingBox();
    const repoBox = await repoCell.boundingBox();
    if (testInfo.project.name === "phone") {
      // Collapsed into the row: each cell is a line of the card under the
      // repository, full width, its header said beside it.
      expect(declBox.y).toBeGreaterThan(repoBox.y);
      expect(gitBox.y).toBeGreaterThan(declBox.y);
      expect(Math.abs(declBox.x - gitBox.x)).toBeLessThanOrEqual(1);
      const label = await declCell.evaluate(
        (el) => getComputedStyle(el, "::before").content,
      );
      expect(label).toBe('"Declarations"');
      const head = await table.locator("thead").boundingBox();
      expect(head.height).toBeLessThanOrEqual(1);
    } else {
      // Columns of one row, side by side.
      expect(Math.abs(declBox.y - gitBox.y)).toBeLessThanOrEqual(2);
      expect(gitBox.x).toBeGreaterThan(declBox.x);
    }

    // The declarations link opens that repository's .grain/ declarations.
    await page.getByTestId(`repo-declarations-${first}`).locator("a").click();
    await expect(page).toHaveURL(/\/next\/processes\/declarations\?repo=/);
    const inspector = page.getByTestId("decl-inspector");
    await expect(inspector).toBeVisible();
    await expect(inspector.locator(".proc-insp-title")).toHaveText(first);
  });
});
