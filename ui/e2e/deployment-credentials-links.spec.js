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

// -secrets-store, so a capability server's typed secret really reaches
// the store and is listed on the Deployment credentials card.
test.use({ demoArgs: ["-secrets-store"] });

// The retired Settings → Legacy capabilities tab is the Deployment
// credentials card at the foot of Settings → Capabilities (task 2876), and
// the links that named it still have to reach it. In the 2026-11-08
// connections walkthrough (docs/test-runs/2026-11-08-connections-
// infrastructure-settings-task-3616.md) /settings/credentials, its
// aliases and Settings → Git's read-only "Settings → Capabilities" note
// all opened Capabilities at its top, with the card some 5,900px -- six
// and a half screens -- below the fold.

const VIEWPORTS = [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
];

for (const [label, viewport] of VIEWPORTS) {
  test(`the retired Legacy capabilities addresses open their card (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    for (const [alias, anchor] of [
      ["/settings/credentials", "credentials"],
      ["/settings/legacy-capabilities", "credentials"],
      ["/settings/secrets", "other-secrets"],
    ]) {
      await page.goto(alias);
      await expect(
        page.getByRole("heading", { name: "Capabilities", level: 1 }),
      ).toBeVisible();
      // The server list draws a moment after the card: the card has to
      // stay in view once it has.
      await expect(
        page.locator("[data-capability-server-row]").first(),
      ).toBeAttached();
      await expect(page.locator(`#${anchor}`)).toBeInViewport();
      // The corrected address names the card, so a reload or a shared
      // link opens it too.
      await expect(page).toHaveURL(`/settings/capabilities#${anchor}`);
    }

    // A #hash of its own still wins over the alias's card.
    await page.goto("/settings/credentials#capability-policy");
    await expect(
      page.locator("[data-capability-server-row]").first(),
    ).toBeAttached();
    await expect(page.locator("#capability-policy")).toBeInViewport();
    await expect(page).toHaveURL("/settings/capabilities#capability-policy");
  });
}

// GET /api/settings and /api/config are stubbed to say the settings are
// held in the config repo (configRepoOnly), which a standalone demo never
// is: that is what draws Settings → Git read-only, with its note.
test("Settings → Git's read-only note opens the Deployment credentials card", async ({
  page,
}) => {
  for (const glob of ["**/api/settings", "**/api/config"]) {
    await page.route(glob, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const body = await response.json();
      await route.fulfill({
        response,
        json: { ...body, configRepoOnly: true },
      });
    });
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/settings/git");
  const note = page.getByTestId("git-secret-note");
  const link = note.getByRole("link", { name: "Settings → Capabilities" });
  await expect(link).toHaveAttribute(
    "href",
    "/settings/capabilities#credentials",
  );
  await link.click();
  await expect(
    page.getByRole("heading", { name: "Capabilities", level: 1 }),
  ).toBeVisible();
  await expect(page).toHaveURL("/settings/capabilities#credentials");
  await expect(
    page.locator("[data-capability-server-row]").first(),
  ).toBeAttached();
  await expect(page.locator("#credentials")).toBeInViewport();

  // Back returns to Git, as any section change does.
  await page.goBack();
  await expect(page).toHaveURL("/settings/git");
  await expect(page.getByTestId("git-secret-note")).toBeVisible();
});

// A capability server's own secrets are listed apart on the Deployment
// credentials card, below the servers on the same page. Adding,
// configuring and removing a server there used to leave that list as it
// was drawn: a removed server's secret stayed listed, linking to the gone
// server, until a reload (2026-11-08 connections walkthrough).
test("the Deployment credentials card follows a capability server's own secret without a reload", async ({
  page,
  request,
}) => {
  const DEMO = "https://capabilities.demo.grain.example/cloud";
  const ID = "https://capabilities.demo.grain.example/cloud-secrets-e2e";
  const demo = (
    await (await request.get("/api/capability-servers")).json()
  ).find((s) => s.id === DEMO);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/settings/capabilities#capability-servers");
  // The demo server's bearer token goes into the secrets store and the
  // header names it: a credential typed into a header is refused.
  const secret = async (method, value) =>
    page.evaluate(
      async ([m, v]) =>
        (
          await fetch("/api/secrets/links-e2e/token", {
            method: m,
            headers: { "Content-Type": "application/json" },
            body: m === "PUT" ? JSON.stringify({ value: v }) : undefined,
          })
        ).status,
      [method, value],
    );
  expect(
    await secret(
      "PUT",
      demo.config.headers.Authorization.replace(/^Bearer /, ""),
    ),
  ).toBeLessThan(300);
  await page.getByTestId("capability-server-add-button").click();
  const add = page.getByTestId("capability-server-add");
  await add.getByLabel(/Capability ID/).fill(ID);
  await add.getByLabel("Transport").click();
  await page.getByRole("option", { name: /HTTP/ }).click();
  await add.getByLabel(/Server URL/).fill(demo.config.serverUrl);
  await add
    .getByLabel(/Headers/)
    .fill("Authorization=Bearer $secret:links-e2e/token");
  await add.getByRole("button", { name: "Add server" }).click();
  const card = page.locator(`[data-capability-server="${ID}"]`);
  await card.getByRole("button", { name: "Edit server settings" }).click();
  const config = card.getByTestId("capability-server-config");
  await config.getByLabel(/^project/).fill("acme-e2e");
  await config.locator('input[id$="-apiToken"]').fill("e2e-token-never-shown");
  await config.getByRole("button", { name: "Save server settings" }).click();
  await expect(config).toHaveCount(0);

  // Configured: its secret is listed under the server, without a reload.
  const used = page.getByRole("list", {
    name: "Secrets used by capability servers",
  });
  await expect(used).toContainText(ID);
  await expect(page.locator("#other-secrets")).not.toContainText(
    "e2e-token-never-shown",
  );

  // Removed: the secret goes with it, and so does the listing.
  await card.getByRole("button", { name: "Remove" }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Remove" })
    .click();
  await expect(card).toHaveCount(0);
  await expect(page.locator("#other-secrets")).not.toContainText(ID);
  await page.reload();
  await expect(page.locator("#other-secrets")).not.toContainText(ID);
  await secret("DELETE");
});
