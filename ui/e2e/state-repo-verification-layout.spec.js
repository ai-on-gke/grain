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

// Settings → Config Repo with an external repository in require_verified
// mode, holding a commit (task 3973): the writers warning sits above the
// repository field it is about, the held commit's Accept action sits in
// its notice's right-hand slot, and nothing pushes the page sideways --
// on a wide screen and on a phone. The status is the demo's own with a
// remote and a held commit laid over it, since the demo has no
// repository to hold one in.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

const HELD = "4f1c2e9ab37d5e60c18b9a2f7e4d3c1b0a998877";

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`require_verified's held commit fits a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.route("**/api/state-repo", async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      await route.fulfill({
        response: res,
        json: {
          ...body,
          available: true,
          mode: "external",
          remote: "https://github.com/acme/grain-config.git",
          branch: "main",
          verification: {
            requireVerified: true,
            requiredApprovals: 2,
            verifiedMergers: ["acme-grain[bot]"],
            allowedSigners: 1,
            forgeReviews: true,
            held: {
              sha: HELD,
              subject: "Add a deploy host script to mcp_servers",
              reason:
                "it is not signed by an allowed key, and it is not the merge of a pull request into main",
              since: "2026-11-10T07:30:00Z",
            },
          },
        },
      });
    });
    await page.goto("/settings/config-repo");

    const warning = page.locator("#external-repo-writers");
    await expect(warning).toContainText(
      "is an administrator of this deployment",
    );
    const url = page.getByLabel("Repository URL").first();
    const warningBox = await warning.boundingBox();
    const urlBox = await url.boundingBox();
    expect(warningBox.y + warningBox.height).toBeLessThanOrEqual(urlBox.y);

    const section = page.locator("#state-repo-verification");
    const held = section.getByRole("alert");
    await expect(held).toContainText(HELD.slice(0, 12));
    await expect(held).toContainText("Add a deploy host script");
    const accept = held.getByRole("button", { name: "Accept" });
    const heldBox = await held.boundingBox();
    const acceptBox = await accept.boundingBox();
    expect(acceptBox.x).toBeGreaterThan(heldBox.x + heldBox.width / 2);
    expect(acceptBox.x + acceptBox.width).toBeLessThanOrEqual(
      heldBox.x + heldBox.width,
    );
    await expect(
      section.getByRole("checkbox", { name: "Require verified commits" }),
    ).toBeChecked();
    await expect(section.getByLabel("Required approvals")).toHaveValue("2");

    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });
}
