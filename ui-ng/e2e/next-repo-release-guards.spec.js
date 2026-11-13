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

// The /next/ repository page's destructive actions, issue import
// outcome, refused-release-name box, unsaved-edit guard and phone tab
// strip, over a real `grain demo -github-sim` (task 5386's walkthrough,
// docs/test-runs/2026-11-13-repository-release-memory-ui.md). Before
// it, Remove repository, Delete release, Promote RC and Merge back all
// acted on one click; Import issues now said only "triggered"; a bad
// release name went to the page banner; leaving with an unsaved edit
// dropped it; and a phone deep link to Releases left that tab off
// screen.
//
// No stub: the in-process GitHub simulation serves branches, releases
// and the (failing) issue listing for a repository that does not exist.

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim"] });

const REPO = "/api/repos/acme/widgets";

test("Remove repository and every release action ask first; a refused release name stays on its box", async ({
  page,
}) => {
  const rel = "walk-5386";
  try {
    await page.goto("/next/settings/repositories");
    await page.getByTestId("remove-repo-acme/widgets").click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toContainText("Remove acme/widgets");
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    await expect(page.getByTestId("remove-repo-acme/widgets")).toBeVisible();
    const cfg = await (await page.request.get("/api/config")).json();
    expect(JSON.stringify(cfg)).toContain("acme/widgets");

    await page.goto("/next/repos/acme/widgets/releases");
    const box = page.getByTestId("input-new-release-name");
    await box.fill("bad name");
    await page.getByTestId("create-release-btn").click();
    await expect(page.getByTestId("new-release-name-error")).toContainText(
      "invalid release name",
    );
    await expect(box).toHaveAttribute("aria-invalid", "true");
    await expect(page.getByTestId("repo-error-banner")).toHaveCount(0);
    await box.fill(rel);
    await expect(page.getByTestId("new-release-name-error")).toHaveCount(0);
    await page.getByTestId("create-release-btn").click();
    await expect(page.getByTestId(`release-row-${rel}`)).toBeVisible();

    for (const [btn, words] of [
      [`promote-rc-${rel}`, "Promote"],
      [`merge-release-${rel}`, "into acme/widgets's default branch"],
      [`delete-release-${rel}`, `Remove release ${rel}?`],
    ]) {
      await page.getByTestId(btn).click();
      await expect(dialog).toContainText(words);
      await page.getByTestId("confirm-cancel").click();
      await expect(dialog).toBeHidden();
    }
    await page.reload();
    await expect(page.getByTestId(`release-row-${rel}`)).toBeVisible();

    await page.getByTestId(`delete-release-${rel}`).click();
    await page.getByTestId("confirm-accept").click();
    await expect(page.getByTestId(`release-row-${rel}`)).toHaveCount(0);
    await page.reload();
    await expect(page.getByTestId(`release-row-${rel}`)).toHaveCount(0);
  } finally {
    await page.request.delete(`${REPO}/releases/${rel}`);
  }
});

test("Import issues now says it read nothing when the source repository cannot be listed", async ({
  page,
}) => {
  try {
    await page.goto("/next/repos/acme/widgets");
    const on = page.getByTestId("switch-issue-import-enabled");
    if ((await on.getAttribute("aria-checked")) !== "true") await on.click();
    await page.getByTestId("input-import-repo").fill("acme/nosuch-5386");
    await page.getByTestId("save-issue-import-btn").click();
    await expect
      .poll(
        async () =>
          (await (await page.request.get(`${REPO}/settings`)).json())
            .issueImport?.repo,
      )
      .toBe("acme/nosuch-5386");
    await page.getByTestId("import-issues-btn").click();
    await expect(page.getByTestId("import-issues-outcome")).toContainText(
      "Could not read the open issues on acme/nosuch-5386:",
    );
  } finally {
    await page.request.put(`${REPO}/settings`, {
      data: { issueImportEnabled: false, issueImportRepo: "" },
    });
  }
});

test("leaving with an unsaved setup command asks first; Cancel keeps the edit", async ({
  page,
}) => {
  await page.goto("/next/repos/acme/widgets/agents");
  const setup = page.getByLabel("Sandbox setup command");
  await setup.fill("make unsaved-5386");
  await page.getByTestId("back-to-repos-link").click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText(
    "You have unsaved changes here. Leave and discard them?",
  );
  await page.getByTestId("confirm-cancel").click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/agents$/);
  await expect(setup).toHaveValue("make unsaved-5386");

  await page.getByTestId("back-to-repos-link").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page).toHaveURL(/\/next\/settings\/repositories$/);
  const s = await (await page.request.get(`${REPO}/settings`)).json();
  expect(s.setupCommand ?? "").not.toContain("unsaved-5386");
});

test("a phone deep link keeps the selected repository tab on screen", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/next/repos/acme/widgets/releases");
  const tab = page.getByTestId("repo-tab-releases");
  await expect(tab).toHaveAttribute("aria-current", "page");
  await expect
    .poll(async () => {
      const b = await tab.boundingBox();
      return b.x >= 0 && b.x + b.width <= 390;
    })
    .toBe(true);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
});

// Review of task 5386: a tab click with an unsaved workflow asks through
// the leave guard; Cancel used to switch the pane anyway (Agents under a
// /ci URL), and clicking back on CI reloaded the workflows over the edit.
test("cancelling a tab switch keeps the tab and the unsaved workflow edit", async ({
  page,
}) => {
  await page.goto("/next/repos/acme/widgets/ci");
  const yaml = page.getByTestId("input-repo-workflow");
  await yaml.fill("name: unsaved-5386\n");
  await page.getByTestId("repo-tab-agents").click();
  await expect(page.getByTestId("confirm-dialog")).toContainText(
    "You have unsaved changes here.",
  );
  await page.getByTestId("confirm-cancel").click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/ci$/);
  await expect(page.getByTestId("repo-tab-ci")).toHaveAttribute(
    "aria-current",
    "page",
  );
  await page.getByTestId("repo-tab-ci").click();
  await expect(yaml).toHaveValue("name: unsaved-5386\n");

  await page.getByTestId("repo-tab-agents").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/agents$/);
});
