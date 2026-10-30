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

// The rail's Staging group and the header rail's "More page actions"
// menu, from the 2026-10-29 responsive navigation walkthrough
// (docs/test-runs/2026-10-29-responsive-navigation-live-sessions.md).
//
// `grain demo` runs no persistent-sandbox supervisor, so GET
// /api/persistent-sandboxes is answered here -- the one boundary
// stubbed. Everything else is the real server and the real rail.

const DESKTOP = { width: 1440, height: 900 };
const TABLET = { width: 820, height: 1180 };
const PHONE = { width: 390, height: 844 };

const STAGING = [
  {
    name: "web-preview",
    enabled: true,
    state: "running",
    hostPort: 8081,
    restarts: 0,
  },
  {
    name: "docs-site",
    enabled: true,
    state: "restarting",
    restarts: 4,
    lastError: "exit 1",
    uiSubpath: "/staging/docs-site",
  },
  { name: "switched-off", enabled: false, state: "stopped", hostPort: 9000 },
];

async function stubStaging(page, { proxied = false } = {}) {
  await page.route("**/api/persistent-sandboxes", (route) =>
    route.fulfill({ json: STAGING }),
  );
  if (!proxied) return;
  // The page is loaded straight off the demo's port, so "came through
  // a proxy" has to be said by the daemon's own config, as it is behind
  // UberProxy (GET /api/config's proxied).
  await page.route("**/api/config", async (route) => {
    const response = await route.fetch();
    const config = await response.json();
    await route.fulfill({ response, json: { ...config, proxied: true } });
  });
}

test("the rail lists staging environments, linked, marked and pinnable", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  await stubStaging(page, { proxied: true });
  await page.goto("/inbox");

  const web = page.getByTestId("staging-item-web-preview");
  const docs = page.getByTestId("staging-item-docs-site");
  await expect(web).toBeVisible();
  await expect(page.getByTestId("staging-item-switched-off")).toHaveCount(0);

  // A host port is linked on this host and, behind a proxy, said to be
  // probably unreachable; a sub-path is linked root-relative.
  await expect(web.getByRole("link")).toHaveAttribute(
    "href",
    /^http:\/\/127\.0\.0\.1:8081\/$/,
  );
  await expect(web).toContainText("not reachable through this proxy");
  await expect(
    web.getByLabel("Host port not reachable through this proxy"),
  ).toBeVisible();
  await expect(docs.getByRole("link")).toHaveAttribute(
    "href",
    "/staging/docs-site/",
  );
  await expect(docs.getByRole("img", { name: "crash-looping" })).toBeVisible();

  // Pinning lists it under Pinned too, and survives a reload.
  await web.hover();
  await page.getByRole("button", { name: "Pin web-preview" }).click();
  await expect(
    page.getByRole("button", { name: "Unpin web-preview" }),
  ).toHaveCount(2);
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Unpin web-preview" }),
  ).toHaveCount(2);
  await page.getByRole("button", { name: "Unpin web-preview" }).first().click();
  await expect(
    page.getByRole("button", { name: "Pin web-preview" }),
  ).toHaveCount(1);
});

test.describe("on a touch phone", () => {
  test.use({ viewport: PHONE, hasTouch: true, isMobile: true });

  test("the drawer's staging pin is drawn, not left for a hover that cannot happen", async ({
    page,
  }) => {
    await stubStaging(page);
    await page.goto("/inbox");
    await page.getByRole("button", { name: "Open navigation" }).tap();
    const drawer = page.getByRole("dialog", { name: "Navigation" });
    const pin = drawer.getByRole("button", { name: "Pin web-preview" });
    await pin.scrollIntoViewIfNeeded();
    await expect(pin).toBeVisible();
    // Before the fix this was 0: a button a tap could land on that
    // nobody could see.
    expect(
      Number(await pin.evaluate((el) => getComputedStyle(el).opacity)),
    ).toBeGreaterThan(0.5);
    await pin.tap();
    await expect(
      drawer.getByRole("button", { name: "Unpin web-preview" }),
    ).toHaveCount(2);
    // Pinning is not navigating: the drawer stays open on it.
    await expect(drawer).toBeVisible();
    await drawer
      .getByRole("button", { name: "Unpin web-preview" })
      .first()
      .tap();
  });

  // Customize opens its overlay over the drawer rather than navigating:
  // closing it lands back in the rail it customized, with focus on the
  // button that opened it. Before the fix the tap closed the drawer, and
  // Escape out of Customize left the reader on the bare page.
  test("Customize keeps the drawer open under it and hands focus back", async ({
    page,
  }) => {
    await page.goto("/inbox");
    await page.getByRole("button", { name: "Open navigation" }).tap();
    const drawer = page.getByRole("dialog", { name: "Navigation" });
    const customize = drawer.getByRole("button", {
      name: "Customize sidebar",
    });
    await expect(customize).toHaveAttribute("aria-haspopup", "dialog");
    await customize.tap();
    const overlay = page.getByRole("dialog", { name: "Customize sidebar" });
    await expect(overlay).toBeVisible();
    await expect(page.locator(".MuiDrawer-paper")).toBeAttached();
    await page.keyboard.press("Escape");
    await expect(overlay).toHaveCount(0);
    await expect(drawer).toBeVisible();
    await expect(customize).toBeFocused();
  });
});

test("the header's More page actions menu closes on Escape and on a press outside it", async ({
  page,
}) => {
  await page.setViewportSize(TABLET);
  await page.goto("/tasks");
  const header = page.getByTestId("page-header");
  const more = header.getByTestId("page-header-more");
  const slot = header.getByTestId("page-header-actions");
  await expect(more).toBeVisible();
  const row = await header.boundingBox();

  // From the keyboard: Enter opens it, Escape closes it and leaves
  // focus on "more".
  await more.focus();
  await page.keyboard.press("Enter");
  await expect(more).toHaveAttribute("aria-expanded", "true");
  await expect(slot).toHaveClass(/page-header-slot-expanded/);
  // A menu under the rail, not a second row of it.
  expect((await header.boundingBox()).height).toBeCloseTo(row.height, 0);
  await page.keyboard.press("Tab");
  await page.keyboard.press("Escape");
  await expect(more).toHaveAttribute("aria-expanded", "false");
  await expect(slot).not.toHaveClass(/page-header-slot-expanded/);
  await expect(more).toBeFocused();

  // With the pointer: a press on the page below closes it, one inside
  // it does not.
  await more.click();
  await expect(more).toHaveAttribute("aria-expanded", "true");
  const menu = await slot.boundingBox();
  await page.mouse.click(menu.x + menu.width - 4, menu.y + 4);
  await expect(more).toHaveAttribute("aria-expanded", "true");
  await page.mouse.click(row.x + row.width / 2, menu.y + menu.height + 300);
  await expect(more).toHaveAttribute("aria-expanded", "false");
  await expect(more).toBeVisible();
});
