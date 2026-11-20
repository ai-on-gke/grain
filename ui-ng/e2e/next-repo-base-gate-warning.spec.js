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

// Both cases share one demo worker whose flags match the 2026-11-16
// walkthrough repro (task 6333): acme/widgets has an enrolled BaseBranchCI
// row for main (seeded by -fake-ci and judged green via -base-branch-ci),
// while an added target repo (acme/gadgets) has never been enrolled.
test.use({
  demoArgs: ["-github-sim", "-fake-ci", "-base-branch-ci", "-qualification"],
});

const UNENROLLED_REPO = "acme/gadgets";
const ENROLLED_REPO = "acme/widgets";

const settingsOf = async (page, repo) => {
  const res = await page.request.get(`/api/repos/${repo}/settings`);
  expect(res.ok()).toBe(true);
  return res.json();
};

const gatesOf = async (page) => {
  const res = await page.request.get("/api/base-branch-gates");
  expect(res.ok()).toBe(true);
  return res.json();
};

// Shared controls and viewport check for both the never-enrolled and
// already-enrolled base-gate warning cases below.
function controlsOf(page, repo) {
  const warning = page.getByTestId("repo-base-gate-warning");
  const pill = page.getByTestId("select-repo-ci-mode");
  const pick = async (mode) => {
    await pill.click();
    await page.getByTestId(`select-repo-ci-mode-opt-${mode}`).click();
    await expect
      .poll(async () => (await settingsOf(page, repo)).ciMode)
      .toBe(mode);
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
  return { warning, pill, pick, fitsViewport };
}

test("warns on Overview and Releases while CI mode is github or none on a never-enrolled repo, and links to CI mode @phone @dark", async ({
  page,
}) => {
  const add = await page.request.post("/api/repos", {
    data: { repo: UNENROLLED_REPO },
  });
  expect(add.ok(), await add.text()).toBe(true);
  const { warning, pill, pick, fitsViewport } = controlsOf(
    page,
    UNENROLLED_REPO,
  );
  const detail = `Nothing is testing ${UNENROLLED_REPO}'s default branch, and the merge queue is merging onto it anyway.`;
  try {
    await page.goto(`/next/repos/${UNENROLLED_REPO}/ci`);
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
      await expect(warning).toContainText(detail);
      await expect(warning).toHaveAttribute("role", "status");
      await fitsViewport();

      await page.getByTestId("repo-tab-releases").click();
      await expect(page).toHaveURL(
        new RegExp(`/repos/${UNENROLLED_REPO}/releases`),
      );
      await expect(warning).toHaveCount(1);
      await expect(warning).toContainText(detail);
      await fitsViewport();

      // The link goes to the CI & Workflows tab's CI mode control.
      await page.getByTestId("repo-base-gate-ci-mode-link").click();
      await expect(page).toHaveURL(
        new RegExp(`/repos/${UNENROLLED_REPO}/ci#repo-ci-mode$`),
      );
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
    await page.request.put(
      `/api/repos/${UNENROLLED_REPO}/watch-default-branch`,
      { data: { watchDefaultBranch: false } },
    );
    await page.request.delete(`/api/repos/${UNENROLLED_REPO}`);
  }
});

// 2026-11-16 repository walkthrough (task 6333): a repository enrolled while
// it was on grain CI keeps its BaseBranchCI row after it moves to No CI or
// GitHub. While Watch default branch is on, the stale row must still raise
// the base-gate warning on Overview and Releases rather than silencing it;
// turning Watch default branch off or returning to Grain CI clears it.
test("warns on Overview and Releases when an enrolled watched branch leaves Grain CI @phone @dark", async ({
  page,
}) => {
  const before = await settingsOf(page, ENROLLED_REPO);
  const { warning, pill, pick, fitsViewport } = controlsOf(page, ENROLLED_REPO);
  const detail = `Nothing is testing ${ENROLLED_REPO}'s base branch main, and the merge queue is merging onto it anyway.`;
  try {
    // Start on Grain CI with a real judged BaseBranchCI row for main.
    const toGrain = await page.request.put(
      `/api/repos/${ENROLLED_REPO}/ci-mode`,
      { data: { ciMode: "grain" } },
    );
    expect(toGrain.ok(), await toGrain.text()).toBe(true);
    const green = await page.request.post("/api/e2e/base-branch/green", {
      data: { branch: "main" },
    });
    expect(green.ok(), await green.text()).toBe(true);
    expect((await green.json()).conclusion).toBe("success");

    await page.goto(`/next/repos/${ENROLLED_REPO}/ci`);
    const watch = page.getByRole("switch", { name: "Watch default branch" });
    if ((await watch.getAttribute("aria-checked")) !== "true") {
      await watch.click();
      await expect(watch).toHaveAttribute("aria-checked", "true");
    }

    // While Grain CI is active, the judged row raises no warning.
    await expect.poll(async () => (await gatesOf(page)).gates).toEqual([]);
    await page.getByTestId("repo-tab-overview").click();
    await expect(page.getByTestId("card-repo-config")).toBeVisible();
    await expect(warning).toHaveCount(0);
    await page.getByTestId("repo-tab-ci").click();

    for (const mode of ["none", "github"]) {
      await pick(mode);
      await expect
        .poll(async () => (await gatesOf(page)).gates)
        .toEqual([
          expect.objectContaining({
            repo: ENROLLED_REPO,
            branch: "main",
            judged: false,
            reason: "grain-ci-off",
          }),
        ]);
      // Not on the tab that holds the control.
      await expect(warning).toHaveCount(0);

      await page.getByTestId("repo-tab-overview").click();
      await expect(warning).toHaveCount(1);
      await expect(warning).toContainText(detail);
      await expect(warning).toHaveAttribute("role", "status");
      await fitsViewport();

      await page.getByTestId("repo-tab-releases").click();
      await expect(page).toHaveURL(
        new RegExp(`/repos/${ENROLLED_REPO}/releases`),
      );
      await expect(warning).toHaveCount(1);
      await expect(warning).toContainText(detail);
      await fitsViewport();

      await page.getByTestId("repo-base-gate-ci-mode-link").click();
      await expect(page).toHaveURL(
        new RegExp(`/repos/${ENROLLED_REPO}/ci#repo-ci-mode$`),
      );
      await expect(pill).toBeInViewport();
      await expect(pill).toBeFocused();
    }

    // Turning Watch default branch off quiets the retained row on both tabs,
    // with no reload.
    await watch.click();
    await expect(watch).toHaveAttribute("aria-checked", "false");
    await expect.poll(async () => (await gatesOf(page)).gates).toEqual([]);
    await page.getByTestId("repo-tab-overview").click();
    await expect(page.getByTestId("card-repo-config")).toBeVisible();
    await expect(warning).toHaveCount(0);
    await page.getByTestId("repo-tab-releases").click();
    await expect(page.getByTestId("card-repo-release-settings")).toBeVisible();
    await expect(warning).toHaveCount(0);

    // Re-enabling Watch default branch brings the warning back, and
    // returning to Grain CI clears it from both tabs with no reload.
    await page.getByTestId("repo-tab-ci").click();
    await watch.click();
    await expect(watch).toHaveAttribute("aria-checked", "true");
    await page.getByTestId("repo-tab-overview").click();
    await expect(warning).toHaveCount(1);
    await page.getByTestId("repo-base-gate-ci-mode-link").click();
    await pick("grain");
    await expect.poll(async () => (await gatesOf(page)).gates).toEqual([]);
    await page.getByTestId("repo-tab-overview").click();
    await expect(page.getByTestId("card-repo-config")).toBeVisible();
    await expect(warning).toHaveCount(0);
    await page.getByTestId("repo-tab-releases").click();
    await expect(page.getByTestId("card-repo-release-settings")).toBeVisible();
    await expect(warning).toHaveCount(0);
  } finally {
    await page.request.put(`/api/repos/${ENROLLED_REPO}/watch-default-branch`, {
      data: { watchDefaultBranch: Boolean(before.watchDefaultBranch) },
    });
    await page.request.put(`/api/repos/${ENROLLED_REPO}/ci-mode`, {
      data: { ciMode: before.ciMode || "github" },
    });
  }
});
