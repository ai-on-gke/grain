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

// Operator-attribution spec §5.4 and §7.4: the sidebar's user tile shows
// who GET /api/whoami says this browser acts as -- never a name typed in
// -- and opens Settings -> Access.
test.describe("sidebar user tile", () => {
  test("shows the demo daemon's own whoami label, over the build stamp", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/");
    const res = await page.request.get("/api/whoami");
    expect(res.ok()).toBeTruthy();
    const whoami = await res.json();
    expect(whoami.allowed).toBe(true);
    expect(whoami.actor.label).toBeTruthy();

    const tile = page.getByTestId("user-tile");
    await expect(tile).toBeVisible();
    await expect(tile).toHaveAttribute("title", whoami.actor.label);
    // An unverified label reads as its name and a muted qualifier, so the
    // tile's text is the label with the space before "(" taken out.
    await expect(page.getByTestId("user-tile-label")).toHaveText(
      whoami.actor.label.replace(/\s+\(/, "("),
    );

    // The tile sits at the foot of the rail, over the build stamp, and
    // inside the rail's width.
    const stamp = page.getByTestId("build-stamp");
    if (await stamp.count()) {
      const t = await tile.boundingBox();
      const s = await stamp.boundingBox();
      expect(t.y + t.height).toBeLessThanOrEqual(s.y + 1);
    }

    await tile.click();
    await expect(page).toHaveURL(/\/settings\/access/);
  });

  test("shows a verified sign-in's email with no qualifier", async ({
    page,
  }) => {
    await page.route("**/api/whoami", (route) =>
      route.fulfill({
        json: {
          actor: {
            kind: "human",
            id: "alice@example.com",
            source: "iap",
            label: "alice@example.com",
          },
          onBehalfOf: null,
          source: "iap",
          verified: true,
          allowed: true,
        },
      }),
    );
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/");
    await expect(page.getByTestId("user-tile-label")).toHaveText(
      "alice@example.com",
    );
    await expect(page.getByTestId("user-tile-qualifier")).toHaveCount(0);
  });
});
