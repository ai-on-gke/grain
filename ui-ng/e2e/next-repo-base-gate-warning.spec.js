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

// The /next/ repository page's base-gate warning, over a real `grain
// demo` (2026-11-14 repository walkthrough, item 5,
// docs/test-runs/2026-11-14-repository-release-memory-ui.md): with CI
// mode on GitHub or No CI, a watched default branch is never enrolled
// and nothing judges it. The classic Repo page says so on Overview and on
// Releases, in GET /api/base-branch-gates' sentence, with a way to CI
// Mode; /next/ said nothing. The warning has to come and go with the CI
// mode on the page itself, with no reload.

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

const R = "/next/repos/acme/widgets";
const DETAIL =
  "Nothing is testing acme/widgets's default branch, and the merge queue is merging onto it anyway.";

const settingsOf = async (page) => {
  const res = await page.request.get("/api/repos/acme/widgets/settings");
  expect(res.ok()).toBe(true);
  return res.json();
};

test("warns on Overview and Releases while CI mode is github or none, and links to CI mode @phone @dark", async ({
  page,
}) => {
  const before = await settingsOf(page);
  const warning = page.getByTestId("repo-base-gate-warning");
  const pill = page.getByTestId("select-repo-ci-mode");
  const pick = async (mode) => {
    await pill.click();
    await page.getByTestId(`select-repo-ci-mode-opt-${mode}`).click();
    await expect.poll(async () => (await settingsOf(page)).ciMode).toBe(mode);
  };
  // fitsViewport says the banner and its link sit inside the page's
  // width: the server's sentence is long and names a URL, and must wrap
  // rather than push the page sideways on a phone.
  const fitsViewport = async () => {
    const vw = page.viewportSize().width;
    const box = await warning.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(vw + 0.5);
    const link = await page
      .getByTestId("repo-base-gate-ci-mode-link")
      .boundingBox();
    expect(link.x + link.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
  };
  try {
    await page.goto(`${R}/ci`);
    // The gate is about a branch grain has been told to watch.
    const watch = page.getByRole("switch", { name: "Watch default branch" });
    if ((await watch.getAttribute("aria-checked")) !== "true") {
      await watch.click();
      await expect(watch).toHaveAttribute("aria-checked", "true");
    }
    await expect(warning).toHaveCount(0);

    for (const mode of ["github", "none"]) {
      await pick(mode);
      // Not on the tab that holds the control.
      await expect(warning).toHaveCount(0);

      await page.getByTestId("repo-tab-overview").click();
      await expect(warning).toHaveCount(1);
      await expect(warning).toContainText(DETAIL);
      await expect(warning).toHaveAttribute("role", "status");
      await fitsViewport();

      await page.getByTestId("repo-tab-releases").click();
      await expect(page).toHaveURL(/\/repos\/acme\/widgets\/releases/);
      await expect(warning).toHaveCount(1);
      await expect(warning).toContainText(DETAIL);
      await fitsViewport();

      // The link goes to the CI & Workflows tab's CI mode control.
      await page.getByTestId("repo-base-gate-ci-mode-link").click();
      await expect(page).toHaveURL(/\/repos\/acme\/widgets\/ci#repo-ci-mode$/);
      await expect(pill).toBeInViewport();
      await expect(pill).toBeFocused();
    }

    // Back on Grain CI the warning is gone from both tabs, no reload.
    await pick("grain");
    await page.getByTestId("repo-tab-overview").click();
    await expect(page.getByTestId("card-repo-config")).toBeVisible();
    await expect(warning).toHaveCount(0);
    await page.getByTestId("repo-tab-releases").click();
    await expect(page.getByTestId("card-repo-release-settings")).toBeVisible();
    await expect(warning).toHaveCount(0);
  } finally {
    // Watch off before grain CI is back on, so the seeder has no watched
    // branch to enrol for the tests after this one.
    await page.request.put("/api/repos/acme/widgets/watch-default-branch", {
      data: { watchDefaultBranch: Boolean(before.watchDefaultBranch) },
    });
    await page.request.put("/api/repos/acme/widgets/ci-mode", {
      data: { ciMode: before.ciMode || "grain" },
    });
  }
});
