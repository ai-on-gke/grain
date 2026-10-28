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

// docs/test-plans/2026-10-07.md §4: a release's name is its own prod
// branch and the stem of its .latest and .rc.N branches, so a name git
// refuses as a branch is refused here too -- from the releases pane and
// from a direct POST alike, with the rule spelled out -- while a name
// that only looks like one ("v3.0-lock") is still created.
// (docs/test-runs/2026-10-07-release-name-validation.md)
test.describe("Release names git cannot use as a branch", () => {
  const releases = "/api/repos/acme/widgets/releases";
  const refused = ["v3.0..x", "v3.lock", "v3."];
  const rule = /cannot contain "\.\.", end in "\." or "\.lock"/;

  test("the releases pane refuses three and creates the fourth", async ({
    page,
  }) => {
    await page.goto("/repos/acme/widgets/releases");
    const name = page.getByLabel("New release name");
    const submit = async (value) => {
      await name.fill(value);
      const created = page.waitForResponse(
        (r) => r.url().endsWith(releases) && r.request().method() === "POST",
      );
      await page.getByRole("button", { name: "Create release" }).click();
      return (await created).status();
    };

    for (const value of refused) {
      expect(await submit(value)).toBe(400);
      await expect(page.getByText(rule)).toBeVisible();
    }
    expect(await submit("v3.0-lock")).toBe(201);
    await expect(page.getByText("v3.0-lock.latest").first()).toBeVisible();
  });

  test("a direct POST gets the same answers", async ({ request }) => {
    for (const value of refused) {
      const res = await request.post(releases, { data: { name: value } });
      expect(res.status()).toBe(400);
      expect((await res.json()).error).toMatch(rule);
    }
    // Created by the pane above; a second release by that name is
    // refused for being in use, not for its name.
    const again = await request.post(releases, {
      data: { name: "v3.0-lock" },
    });
    expect(again.status()).toBe(400);
    expect((await again.json()).error).toMatch(/already has an unmerged/);

    const list = await (await request.get(releases)).json();
    const names = list.map((r) => r.name);
    expect(names).toContain("v3.0-lock");
    for (const value of refused) expect(names).not.toContain(value);
  });
});
