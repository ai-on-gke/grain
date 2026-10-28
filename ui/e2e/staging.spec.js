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

// The rail's Staging group (Sidebar.jsx's StagingItem, staging.js) over
// the demo's two persistent sandboxes (pkg/demo's
// demo_persistent_sandboxes.go): docs-preview, served under the UI at
// /staging/docs-preview/, and grain-staging, on host port 4280 only and
// crash-looping.

const DESKTOP = { width: 1440, height: 900 };

test.use({ demoArgs: ["-staging"] });

test.describe("staging environments in the rail", () => {
  test("lists each with its state and opens a sub-path in a new tab", async ({
    page,
    context,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    const group = page.getByTestId("staging-group");
    await expect(group.getByText("Staging", { exact: true })).toBeVisible();

    const docs = group.getByTestId("staging-item-docs-preview");
    await expect(docs).toHaveAttribute("data-state", "running");
    const docsLink = docs.getByRole("link", { name: /docs-preview/ });
    await expect(docsLink).toHaveAttribute("href", "/staging/docs-preview/");
    await expect(docsLink).toHaveAttribute("target", "_blank");

    const [opened] = await Promise.all([
      context.waitForEvent("page"),
      docsLink.click(),
    ]);
    await opened.waitForLoadState();
    await expect(
      opened.getByRole("heading", { name: "docs-preview" }),
    ).toBeVisible();
    await opened.close();

    // A host port is linked on the host the page came from.
    const staging = group.getByTestId("staging-item-grain-staging");
    await expect(staging).toHaveAttribute("data-state", "crash-looping");
    const origin = new URL(page.url());
    await expect(
      staging.getByRole("link", { name: /grain-staging/ }),
    ).toHaveAttribute("href", `${origin.protocol}//${origin.hostname}:4280/`);
    await expect(
      staging.getByText("not reachable through this proxy"),
    ).toHaveCount(0);
  });

  test("says a host port will not load through a proxy", async ({ page }) => {
    await page.setViewportSize(DESKTOP);
    // What GET /api/config says when the request came through UberProxy
    // or IAP: forwarding headers were on it.
    await page.route("**/api/config", async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      await route.fulfill({ response: res, json: { ...body, proxied: true } });
    });
    await page.goto("/");
    const group = page.getByTestId("staging-group");
    const staging = group.getByTestId("staging-item-grain-staging");
    await expect(
      staging.getByText("not reachable through this proxy"),
    ).toBeVisible();
    await expect(staging.getByRole("link")).toHaveAttribute(
      "title",
      /ui_subpath/,
    );
    // The sub-path is the UI's own origin, and loads wherever it does.
    await expect(
      group
        .getByTestId("staging-item-docs-preview")
        .getByText("not reachable through this proxy"),
    ).toHaveCount(0);
  });

  test("lays an entry out inside the rail: dot, name, pin", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    const aside = await page.locator("aside").boundingBox();
    const row = page
      .getByTestId("staging-group")
      .getByTestId("staging-item-grain-staging");
    await row.hover();
    const box = await row.boundingBox();
    const dot = await row.locator(".dot").boundingBox();
    const name = await row.getByText("grain-staging").boundingBox();
    const pin = await row
      .getByRole("button", { name: "Pin grain-staging" })
      .boundingBox();
    // Inside the rail, with nothing poking past its right edge.
    expect(box.x).toBeGreaterThanOrEqual(aside.x);
    expect(box.x + box.width).toBeLessThanOrEqual(aside.x + aside.width);
    expect(pin.x + pin.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
    // Dot, then name, then pin, left to right.
    expect(dot.x + dot.width).toBeLessThanOrEqual(name.x);
    expect(name.x + name.width).toBeLessThanOrEqual(pin.x);
    // The dot centred on the name's line, as the state dots above are.
    expect(
      Math.abs(dot.y + dot.height / 2 - (name.y + name.height / 2)),
    ).toBeLessThanOrEqual(3);
  });

  test("pins and unpins an entry", async ({ page }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    const aside = page.locator("aside");
    try {
      await aside.getByRole("button", { name: "Pin grain-staging" }).click();
      await expect(aside.getByTestId("staging-item-grain-staging")).toHaveCount(
        2,
      );
      await page.reload();
      await expect(aside.getByTestId("staging-item-grain-staging")).toHaveCount(
        2,
      );
      await aside
        .getByRole("button", { name: "Unpin grain-staging" })
        .first()
        .click();
      await expect(aside.getByTestId("staging-item-grain-staging")).toHaveCount(
        1,
      );
    } finally {
      await page.evaluate(() => localStorage.removeItem("grain.pinned"));
    }
  });

  test("the persistent sandboxes panel links both and offers raw logs", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/system/sandboxes");
    const panel = page.getByTestId("persistent-sandboxes");
    await expect(
      panel.getByRole("link", { name: "/staging/docs-preview/" }),
    ).toBeVisible();
    await expect(panel.getByRole("link", { name: "port 4280" })).toBeVisible();
    await expect(
      panel.getByTestId("persistent-sandbox-restart-reason"),
    ).toContainText("exited with status 1");
    const raw = panel.getByRole("link", { name: "Raw" }).last();
    await expect(raw).toHaveAttribute(
      "href",
      "/api/persistent-sandboxes/grain-staging/logs?format=text",
    );
    const res = await page.request.get(
      "/api/persistent-sandboxes/grain-staging/logs?format=text",
    );
    expect(await res.text()).toContain("address already in use");
  });
});
