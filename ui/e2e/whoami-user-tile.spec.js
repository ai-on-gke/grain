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

    // Inside the rail, and its label never wider than the tile.
    const rail = await page.locator("aside").first().boundingBox();
    const t = await tile.boundingBox();
    expect(t.x).toBeGreaterThanOrEqual(rail.x - 1);
    expect(t.x + t.width).toBeLessThanOrEqual(rail.x + rail.width + 1);
    const label = await page.getByTestId("user-tile-label").boundingBox();
    expect(label.x + label.width).toBeLessThanOrEqual(t.x + t.width + 1);

    await tile.click();
    await expect(page).toHaveURL(/\/settings\/access/);
    await expect(page.getByTestId("access-recorded-as")).toHaveText(
      whoami.actor.label,
    );
  });

  test("folded, keeps the tile as an icon inside the narrow rail", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/");
    await page.getByTestId("sidebar-toggle").click();
    const tile = page.getByTestId("user-tile");
    await expect(tile).toBeVisible();
    await expect(page.getByTestId("user-tile-label")).toHaveCount(0);
    const rail = await page.locator("aside").first().boundingBox();
    const t = await tile.boundingBox();
    expect(t.x + t.width).toBeLessThanOrEqual(rail.x + rail.width + 1);
    // Unfold again, for the next test on this worker's browser storage.
    await page.getByTestId("sidebar-toggle").click();
    await expect(page.getByTestId("user-tile-label")).toBeVisible();
  });

  test("on a phone, sits at the foot of the drawer under System", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/");
    await page.getByRole("button", { name: "Open navigation" }).click();
    const drawer = page.getByRole("dialog", { name: "Navigation" });
    const tile = drawer.getByTestId("user-tile");
    await expect(tile).toBeVisible();
    const system = await drawer
      .getByRole("button", { name: "System" })
      .first()
      .boundingBox();
    const t = await tile.boundingBox();
    expect(t.y).toBeGreaterThanOrEqual(system.y + system.height - 1);
    expect(t.x + t.width).toBeLessThanOrEqual(390);
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

  test("TaskChat shows PrincipalLabel headers visibly on outgoing human bubbles", async ({
    page,
    request,
  }) => {
    const whoRes = await request.get("/api/whoami");
    expect(whoRes.ok()).toBeTruthy();
    const whoami = await whoRes.json();

    const createRes = await request.post("/api/tasks", {
      data: {
        title: "Chat sender label visibility check",
        description: "Opening human prompt in chat.",
        noRepo: true,
        interactive: true,
      },
    });
    expect(createRes.status()).toBe(201);
    const created = await createRes.json();

    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(`/tasks/${created.id}`);
    const sender = page
      .locator(".cs-message--outgoing .cs-message__sender-name")
      .first();
    await expect(sender).toBeVisible();
    await expect(sender).toHaveText(whoami.actor.label);
  });
});
