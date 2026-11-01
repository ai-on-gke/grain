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

// Three things the 2026-11-01 responsive navigation walkthrough found
// (docs/test-runs/2026-11-01-responsive-navigation-live-sessions.md):
// a repository tab missing from the rail's crumbs, the Logs source
// select cut off by the header slot on a tablet, and Customize sidebar
// closing the phone drawer whose pinned items it edits.

import { expect, test } from "./fixtures.js";

const TABLET = { width: 820, height: 1180 };
const PHONE = { width: 390, height: 844 };

test("a repository tab is the last crumb of the header rail", async ({
  page,
}) => {
  await page.goto("/repos/acme/widgets");
  const title = page.getByTestId("page-header-title");
  await expect(title).toHaveText("repositories/acme/widgets");

  await page.getByRole("tab", { name: "Merge queue" }).click();
  await expect(page).toHaveURL(
    /\/repos\/acme\/widgets\/settings\/merge-queue$/,
  );
  await expect(title).toHaveText("repositories/acme/widgets/merge queue");
  await expect(page.getByTestId("top-bar-page")).toHaveText("merge queue");

  // A reload, and Overview picked again, say the same as the tab strip.
  // (A tab picked on the page replaces the address rather than adding a
  // step for Back: App.jsx's otherTab.)
  await page.reload();
  await expect(title).toHaveText("repositories/acme/widgets/merge queue");
  await page.getByRole("tab", { name: "Overview" }).click();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets$/);
  await expect(title).toHaveText("repositories/acme/widgets");
});

test("the Logs source select is never cut by the header slot", async ({
  page,
}) => {
  await page.setViewportSize(TABLET);
  await page.goto("/system/logs");
  const slot = page.getByTestId("page-header-actions");
  const source = slot.getByRole("combobox");
  await expect(source).toBeVisible();
  const s = await slot.boundingBox();
  const c = await source.boundingBox();
  // Before the fix its 220px minimum ran it to x=629 in a slot ending at
  // x=597, its arrow behind the slot's clip.
  expect(c.x).toBeGreaterThanOrEqual(s.x - 1);
  expect(c.x + c.width).toBeLessThanOrEqual(s.x + s.width + 1);
});

test.describe("on a touch phone", () => {
  test.use({ viewport: PHONE, hasTouch: true, isMobile: true });

  test("Customize sidebar opens over the drawer and hands back to it", async ({
    page,
  }) => {
    await page.goto("/inbox");
    await page.getByRole("button", { name: "Open navigation" }).tap();
    // The drawer's paper by its class as well as its role: while the
    // Customize dialog is up, MUI hides the drawer from the
    // accessibility tree, which a role query honours.
    const drawer = page.locator(".MuiDrawer-paper[aria-label=Navigation]");
    await expect(drawer).toBeVisible();
    await drawer.getByRole("button", { name: "Customize sidebar" }).tap();
    await expect(
      page.getByRole("dialog", { name: /customize sidebar/i }),
    ).toBeVisible();
    await expect(drawer).toBeVisible();

    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("dialog", { name: /customize sidebar/i }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("dialog", { name: "Navigation" }),
    ).toBeVisible();
    await expect(
      drawer.getByRole("button", { name: "Customize sidebar" }),
    ).toBeFocused();

    // Choosing a destination still closes it.
    await drawer.getByRole("button", { name: /^processes/i }).tap();
    await expect(page).toHaveURL(/\/processes$/);
    await expect(drawer).toHaveCount(0);
  });
});
