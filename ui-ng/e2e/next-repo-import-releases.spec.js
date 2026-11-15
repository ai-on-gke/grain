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

// The /next/ repository page's issue import, unsaved-edit guard, classic
// tab addresses and release actions, over a real `grain demo` with its
// GitHub simulation (2026-11-14 repository walkthrough,
// docs/test-runs/2026-11-14-repository-release-memory-ui.md):
// - Import issues now said "Issue import triggered" when the source
//   repository could not be listed, and there was no queue-at-once switch;
// - an unsaved issue-import or agent edit was dropped without a word on a
//   tab switch or on leaving the page;
// - /next/repos/<owner>/<name>/settings/<tab> (the classic address) loaded
//   a repository called "settings";
// - Promote RC, Merge back and Delete ran on the first click, Cut RC was
//   on while an RC was active, and a refused release name went to the
//   page-wide banner rather than beside its box.

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

const R = "/next/repos/acme/widgets";

const settingsOf = async (page) => {
  const res = await page.request.get("/api/repos/acme/widgets/settings");
  expect(res.ok()).toBe(true);
  return res.json();
};

test("Import issues now says it read nothing from a source it cannot list, and queue-at-once warns and persists", async ({
  page,
}) => {
  const before = (await settingsOf(page)).issueImport;
  try {
    await page.goto(R);
    const enabled = page.getByTestId("switch-issue-import-enabled");
    if ((await enabled.getAttribute("aria-checked")) !== "true") {
      await enabled.click();
    }
    await page.getByTestId("input-import-repo").fill("acme/nosuch-e2e");
    await expect(page.getByTestId("issue-import-trust-hint")).toContainText(
      "trust everybody who can open an issue on acme/nosuch-e2e",
    );
    const approved = page.getByTestId("switch-issue-import-approved");
    await expect(approved).toHaveAttribute("aria-checked", "false");
    await approved.click();
    await page.getByTestId("save-issue-import-btn").click();
    await expect
      .poll(async () => (await settingsOf(page)).issueImport)
      .toMatchObject({
        enabled: true,
        repo: "acme/nosuch-e2e",
        approved: true,
      });

    await page.getByTestId("import-issues-btn").click();
    const outcome = page.getByTestId("import-issues-outcome");
    await expect(outcome).toContainText(
      "Could not read the open issues on acme/nosuch-e2e",
    );
    await expect(outcome).not.toContainText("Read 0");

    await page.reload();
    await expect(
      page.getByTestId("switch-issue-import-approved"),
    ).toHaveAttribute("aria-checked", "true");
    await expect(page.getByTestId("input-import-repo")).toHaveValue(
      "acme/nosuch-e2e",
    );
  } finally {
    await page.request.put("/api/repos/acme/widgets/settings", {
      data: {
        issueImportEnabled: before.enabled,
        issueImportRepo: before.repo,
        issueImportLabel: before.label,
        issueImportApproved: before.approved,
      },
    });
  }
});

test("leaving the repository page with an unsaved edit asks, and Discard puts the saved value back", async ({
  page,
}) => {
  await page.goto(R);
  const label = page.getByTestId("input-import-label");
  await expect(label).toHaveValue("");
  await label.fill("unsaved-e2e");

  // A tab switch asks; Cancel stays put with the edit.
  await page.getByTestId("repo-tab-ci").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("unsaved issue import changes");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets$/);
  await expect(label).toHaveValue("unsaved-e2e");

  // The back link (a router navigation) asks too.
  await page
    .getByRole("link", { name: /Repositories/ })
    .first()
    .click();
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets$/);

  // Discard leaves and nothing was stored.
  await page.getByTestId("repo-tab-ci").click();
  await dialog.getByRole("button", { name: "Discard" }).click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/ci$/);
  expect((await settingsOf(page)).issueImport.label).toBe("");

  // Agent settings: a tab round trip no longer keeps a phantom value.
  await page.getByTestId("repo-tab-agents").click();
  const setup = page.getByTestId("input-repo-setup-cmd");
  await setup.fill("make deps-e2e");
  await page.getByTestId("repo-tab-branches").click();
  await expect(dialog).toContainText("unsaved agent settings changes");
  await dialog.getByRole("button", { name: "Discard" }).click();
  await expect(page).toHaveURL(/\/branches$/);
  await page.getByTestId("repo-tab-agents").click();
  await expect(setup).toHaveValue("");

  // Saved edits leave without a question.
  await setup.fill("make deps-e2e");
  await page.getByTestId("save-repo-agents-btn").click();
  await expect
    .poll(async () => (await settingsOf(page)).setupCommand)
    .toBe("make deps-e2e");
  await page.getByTestId("repo-tab-branches").click();
  await expect(page).toHaveURL(/\/branches$/);
  await expect(dialog).toHaveCount(0);
  await page.request.put("/api/repos/acme/widgets/setup-command", {
    data: { setupCommand: "" },
  });
});

test("the classic /settings/<tab> address opens that tab of the same repository", async ({
  page,
}) => {
  const bad = [];
  page.on("response", (r) => {
    if (r.status() >= 400 && r.url().includes("/api/")) bad.push(r.url());
  });
  await page.goto(`${R}/settings/ci`);
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/ci$/);
  await expect(page.getByTestId("repo-detail-heading")).toHaveText(
    "acme/widgets",
  );
  await expect(page.getByTestId("repo-tab-ci")).toHaveAttribute(
    "aria-current",
    "page",
  );
  await page.goto(`${R}/settings`);
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets$/);
  await expect(page.getByTestId("repo-detail-heading")).toHaveText(
    "acme/widgets",
  );
  expect(bad).toEqual([]);
});

test("release actions ask first, say why Cut RC is off, and a refused name is reported beside its box", async ({
  page,
}) => {
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(`${R}/releases`);
    const cut = page.getByTestId("cut-rc-1.0");
    await expect(cut).toBeDisabled();
    await expect(page.getByTestId("release-action-reasons")).toContainText(
      "Cut RC: 1.0.rc.1 is active: a new RC is cut once it is promoted.",
    );

    // Promote asks; Cancel changes nothing.
    await page.getByTestId("promote-rc-1.0").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText(
      "Promote 1.0.rc.1 to 1.0? This moves acme/widgets's 1.0 branch to 1.0.rc.1.",
    );
    const box = await dialog.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    await dialog.getByRole("button", { name: "Cancel" }).click();

    // Delete asks; Cancel keeps the release.
    await page.getByTestId("delete-release-1.0").click();
    await expect(dialog).toContainText("Remove release 1.0?");
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    const rels = await (
      await page.request.get("/api/repos/acme/widgets/releases")
    ).json();
    expect(rels.map((r) => r.name)).toContain("1.0");
    const cands = await (
      await page.request.get("/api/repos/acme/widgets/releases/1.0/candidates")
    ).json();
    expect(cands[0].status).toBe("active");

    // A refused name sits beside its box and clears on the next edit.
    const name = page.getByTestId("input-new-release-name");
    await name.fill("bad name");
    await page.getByTestId("create-release-btn").click();
    await expect(page.getByTestId("new-release-error")).toContainText(
      "invalid release name",
    );
    await expect(name).toHaveAttribute("aria-invalid", "true");
    await expect(page.getByTestId("repo-error-banner")).toHaveCount(0);
    await name.fill("bad name2");
    await expect(page.getByTestId("new-release-error")).toHaveCount(0);
    await expect(name).not.toHaveAttribute("aria-invalid", "true");
  }
});

test("a deep link to a later tab keeps that tab in view on a phone", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  for (const tab of ["releases", "branches"]) {
    await page.goto(`${R}/${tab}`);
    const btn = page.getByTestId(`repo-tab-${tab}`);
    await expect(btn).toHaveAttribute("aria-current", "page");
    await expect
      .poll(async () => {
        const b = await btn.boundingBox();
        return b.x >= 0 && b.x + b.width <= 390;
      })
      .toBe(true);
  }
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
});
