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

// The /next/ repository page, over a real `grain demo` with its GitHub
// simulation (2026-11-18 repository walkthrough,
// docs/test-runs/2026-11-18-repository-release-memory-ui.md):
// - a refused setting's banner stayed up after that setting saved
//   cleanly, until dismissed by hand;
// - with two releases, only the selected one's candidates were read, so
//   the other row's Cut RC was on (with no reason) while its RC was
//   still active, and its Promote dialog could not name the RC;
// - there was no way to file a task from the page: the rail's New task
//   opened on the deployment's default target, whichever repository's
//   page it was opened from.

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification", "-reconcile=1s"] });

const R = "/next/repos/acme/widgets";

test("a refused setting's banner clears when that setting saves cleanly, and only then", async ({
  page,
}) => {
  await page.goto(`${R}/agents`);
  const caps = page.getByLabel(/Default capabilities/);
  const save = page.getByTestId("save-repo-agents-btn");
  const banner = page.getByTestId("repo-error-banner");
  await caps.fill("nosuch-cap-e2e");
  await save.click();
  await expect(banner).toContainText("unknown capability nosuch-cap-e2e");

  await caps.fill("");
  await save.click();
  await expect(banner).toHaveCount(0);

  // Another setting saving leaves a refusal it did not cause alone.
  await caps.fill("nosuch-cap-e2e");
  await save.click();
  await expect(banner).toContainText("unknown capability nosuch-cap-e2e");
  await caps.fill("");
  await page.getByTestId("repo-tab-merge-queue").click();
  const onGreen = page.getByTestId("switch-merge-on-green");
  const was = await onGreen.getAttribute("aria-checked");
  await onGreen.click();
  await expect(onGreen).not.toHaveAttribute("aria-checked", was);
  await expect(banner).toHaveCount(1);
  await onGreen.click();
  await expect(onGreen).toHaveAttribute("aria-checked", was);
});

test("every release row judges Cut RC and Promote RC by its own candidates", async ({
  page,
}) => {
  const name = "walk-e2e";
  try {
    await page.goto(`${R}/releases`);
    await page.getByTestId("input-new-release-name").fill(name);
    await page.getByTestId("create-release-btn").click();
    await expect(page.getByTestId(`release-row-${name}`)).toBeVisible();
    // The reconciler provisions the new release and cuts its first RC.
    await expect
      .poll(
        async () => {
          const res = await page.request.get(
            `/api/repos/acme/widgets/releases/${name}/candidates`,
          );
          return res.ok() ? (await res.json()).length : 0;
        },
        { timeout: 30_000 },
      )
      .toBeGreaterThan(0);
    await page.reload();
    // The new release sorts first and is the one selected; 1.0 is not.
    await expect(page.getByTestId(`inspect-rc-${name}`)).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await expect(page.getByTestId("inspect-rc-1.0")).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    const cut = page.getByTestId("cut-rc-1.0");
    await expect(cut).toBeDisabled();
    await expect(cut).toHaveAttribute(
      "title",
      "1.0.rc.1 is active: a new RC is cut once it is promoted.",
    );
    await page.getByTestId("promote-rc-1.0").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Promote 1.0.rc.1 to 1.0?");
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
  } finally {
    await page.request.delete(`/api/repos/acme/widgets/releases/${name}`);
  }
});

test("New task on a repository's page files into that repository", async ({
  page,
}) => {
  const title = `Filed from the gadgets page ${Date.now()}`;
  const added = await page.request.post("/api/repos", {
    data: { repo: "acme/gadgets" },
  });
  expect(added.ok()).toBe(true);
  try {
    await page.goto("/next/repos/acme/gadgets");
    await page.getByTestId("repo-new-task-btn").click();
    const sheet = page.getByRole("dialog");
    await expect(sheet).toContainText("acme/gadgets");
    await sheet.getByLabel(/^Title/).fill(title);
    await sheet
      .getByRole("button", { name: /^Create/ })
      .last()
      .click();
    await expect(sheet).toHaveCount(0);
    await expect
      .poll(async () => {
        const res = await page.request.get(
          `/api/tasks?q=${encodeURIComponent(title)}`,
        );
        const body = await res.json();
        const rows = Array.isArray(body) ? body : (body.tasks ?? []);
        return rows.map((t) => t.repo);
      })
      .toEqual(["acme/gadgets"]);
  } finally {
    await page.request.delete("/api/repos/acme/gadgets");
  }
});
