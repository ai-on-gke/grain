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

import { expect, test } from "./fixtures.js";

// docs/test-runs/2026-11-07-repository-release-memory-ui.md (task 3160),
// bugs 2 and 3: under an external config repo every settings write is
// refused, yet the Releases tab's qualification plan form and Settings →
// Repositories' base-branch repair switch stayed live (the switch's
// refusal was swallowed, so it just sprang back). The repository's own
// Merge queue tab already drew its copy of the switch disabled. A real
// `grain demo -external-config-repo`, so /api/config's configRepoOnly is
// the server's own answer, not a stub.
test.use({
  demoArgs: [
    "-github-sim",
    "-qualification",
    "-external-config-repo=https://github.com/grain/config.git",
  ],
});

const BYPASS =
  "acme/widgets: Base-branch repair PRs may merge without a human when their CI is green";

for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the qualification plan and the bypass switch are read-only under the config repo on a ${name} screen`, async ({
    page,
  }) => {
    const errors = [];
    page.on("pageerror", (err) => errors.push(String(err)));
    await page.setViewportSize(viewport);

    const config = await (await page.request.get("/api/config")).json();
    expect(config.configRepoOnly).toBe(true);

    await page.goto("/repos/acme/widgets/releases");
    const save = page.getByRole("button", { name: "Save qualification plan" });
    await expect(save).toBeVisible();
    const notice = page.getByTestId("config-managed-notice");
    await expect(notice).toContainText("tables/qualification_config.json");
    await expect(save).toBeDisabled();
    await expect(page.getByRole("button", { name: "Add item" })).toBeDisabled();
    await expect(
      page.getByLabel("Require approval before running qualification tasks"),
    ).toBeDisabled();
    await expect(
      page.getByLabel("Promote automatically once qualification succeeds"),
    ).toBeDisabled();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);

    await page.goto("/settings/repositories");
    const box = page.getByLabel(BYPASS);
    await expect(box).toBeVisible();
    await expect(box).toBeDisabled();

    // The repo's own Merge queue tab agrees.
    await page.goto("/repos/acme/widgets/settings/merge-queue");
    await expect(
      page.getByRole("checkbox", { name: /Base-branch repair PRs/ }).first(),
    ).toBeDisabled();
    expect(errors).toEqual([]);
  });

  // docs/test-runs/2026-11-08-repository-release-memory-ui.md (task
  // 3613), bug 2: the demo wired the external config repo without its
  // owner/name (ui.Config.StateRepoRef), so /api/config named no
  // configRepo and grain/config's own Advanced tab read "not one of this
  // deployment's target repos" instead of refusing to remove it.
  test(`the external config repo is named and refuses removal on a ${name} screen`, async ({
    page,
  }) => {
    const errors = [];
    page.on("pageerror", (err) => errors.push(String(err)));
    await page.setViewportSize(viewport);

    const config = await (await page.request.get("/api/config")).json();
    expect(config.configRepo).toBe("grain/config");
    const deployment = await (await page.request.get("/api/deployment")).json();
    expect(deployment.configRepo).toBe("grain/config");

    await page.goto("/repos/grain/config/settings/advanced");
    const panel = page.locator("#repo-tabpanel-advanced");
    await expect(
      panel.getByText(
        "This is the deployment's own configuration repository and cannot be removed here.",
      ),
    ).toBeVisible();
    await expect(
      panel.getByRole("button", { name: "Remove", exact: true }),
    ).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    expect(errors).toEqual([]);
  });
}
