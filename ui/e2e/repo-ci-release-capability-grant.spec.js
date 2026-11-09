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

// docs/test-runs/2026-11-09-repository-release-memory-ui.md (task 3716).
// The release server hands registry-push, package-publish and
// image-signing only to a repository's release jobs, and the repository's
// CI capabilities list is what grants them. The list's Add grant… picker
// asked GET /api/capability-methods?kind=ci&repo=… for verdicts, which
// judged a CI run with no trigger event, so all three were listed
// disabled ("this is a unnamed run") and could not be added at all.
test.setTimeout(90_000);
// A server of its own: this changes acme/widgets' CI capabilities.
test.use({ demoArgs: ["-as", "ci-release-grant"] });

const RELEASE =
  /^release server › (registry-push|package-publish|image-signing)/;

for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the CI capabilities picker offers the release server's methods (${name})`, async ({
    page,
  }) => {
    const errors = [];
    page.on("pageerror", (err) => errors.push(String(err)));
    await page.setViewportSize(viewport);
    try {
      await page.goto("/repos/acme/widgets/settings/ci");
      await page
        .getByRole("tabpanel")
        .getByRole("button", { name: "Add grant…" })
        .click();
      const items = page.getByRole("menuitem", { name: RELEASE });
      await expect(items).toHaveCount(3);
      for (const item of await items.all()) {
        await expect(item).not.toHaveAttribute("aria-disabled", "true");
        await expect(item).not.toContainText(/unnamed run|no trigger event/);
      }
      await page
        .getByRole("menuitem", { name: /^release server › registry-push/ })
        .click();
      await expect
        .poll(
          async () =>
            (
              await (
                await page.request.get(
                  "/api/repos/acme/widgets/ci-capabilities",
                )
              ).json()
            ).ciCapabilities,
        )
        .toEqual(["https://grain.dev/capabilities/release#registry-push={}"]);

      // A task is still refused them: they are CI capabilities.
      await page.goto("/repos/acme/widgets/settings/agents");
      await page
        .getByRole("tabpanel")
        .getByRole("button", { name: "Add grant…" })
        .first()
        .click();
      const taskItems = page.getByRole("menuitem", { name: RELEASE });
      await expect(taskItems).toHaveCount(3);
      for (const item of await taskItems.all()) {
        await expect(item).toHaveAttribute("aria-disabled", "true");
      }
      expect(errors).toEqual([]);
    } finally {
      await page.request.put("/api/repos/acme/widgets/ci-capabilities", {
        data: { ciCapabilities: [] },
      });
    }
  });
}
