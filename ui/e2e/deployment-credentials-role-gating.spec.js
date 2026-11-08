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

// 2026-11-08 deployment credentials walkthrough
// (docs/test-runs/2026-11-08-deployment-credentials-card.md, item 2):
// the Deployment credentials card follows Capabilities'
// capabilities:admin, but saves through PUT /api/settings
// (settings:write) and /api/secrets (secrets:write). Against a real
// daemon a role holding capabilities:admin alone was offered an editable
// GitHub host, Set secret and Delete secret, each refused with 403.

test.use({ demoArgs: ["-secrets-store"] });

const CAPADMIN = {
  role: "capadmin",
  roles: ["capadmin"],
  full: false,
  scopes: [
    "capabilities:admin",
    "capabilities:read",
    "repos:read",
    "secrets:read",
    "settings:read",
    "tasks:read",
    "templates:read",
  ],
};

async function asRole(page, access) {
  await page.route(
    (u) => u.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      const json = { ...(await response.json()), access };
      await route.fulfill({ response, json });
    },
  );
}

async function putSecret(page, path, value) {
  return page.evaluate(
    async ([p, v]) =>
      (
        await fetch(p, {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ value: v }),
        })
      ).status,
    [path, value],
  );
}

test("a role with capabilities:admin but not settings:write or secrets:write sees the card read-only, saying why", async ({
  page,
}) => {
  await page.goto("/settings/capabilities");
  expect(await putSecret(page, "/api/secrets/cosign-key/key", "k")).toBe(200);
  try {
    await asRole(page, CAPADMIN);
    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/settings/capabilities#credentials");
      const card = page.locator("#credentials");
      await expect(card.getByText("cosign-key")).toBeVisible();
      // Capabilities itself is open to this role: no page-level note.
      await expect(page.getByTestId("settings-role-read-only")).toHaveCount(0);
      const note = card.getByTestId("credentials-read-only");
      await expect(note).toContainText("Needs settings:write");
      await expect(note).toContainText("Needs secrets:write");
      await expect(card.locator("input[name=githubHost]")).toBeDisabled();
      await expect(card.locator("input[name=gcpProject]")).toBeDisabled();
      await expect(
        card.getByRole("button", { name: "Delete secret" }),
      ).toBeDisabled();
      await expect(
        card.getByRole("button", { name: "Set secret" }),
      ).toBeDisabled();
      await expect(card.locator(".MuiChip-deletable")).toHaveCount(0);
      // The note wraps inside the card rather than widening the page.
      const overflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
      const n = await note.boundingBox();
      const c = await card.boundingBox();
      expect(n.x + n.width).toBeLessThanOrEqual(c.x + c.width + 1);
    }
  } finally {
    await page.unroute((u) => u.pathname === "/api/config");
    await page.evaluate(() =>
      fetch("/api/secrets/cosign-key", { method: "DELETE" }),
    );
  }
});

test("an operator still gets every control, and no read-only note", async ({
  page,
}) => {
  await page.goto("/settings/capabilities");
  expect(await putSecret(page, "/api/secrets/cosign-key/key", "k")).toBe(200);
  try {
    await page.goto("/settings/capabilities#credentials");
    const card = page.locator("#credentials");
    await expect(card.getByText("cosign-key")).toBeVisible();
    await expect(card.getByTestId("credentials-read-only")).toHaveCount(0);
    await expect(card.locator("input[name=githubHost]")).toBeEnabled();
    await expect(
      card.getByRole("button", { name: "Delete secret" }),
    ).toBeEnabled();
    await expect(
      card.getByRole("button", { name: "Set secret" }),
    ).toBeEnabled();
  } finally {
    await page.evaluate(() =>
      fetch("/api/secrets/cosign-key", { method: "DELETE" }),
    );
  }
});

// Item 1 of the same walkthrough: the old Integrations tab's addresses
// redirected to the top of Capabilities, the card some 5,800px below.
test("the old Integrations tab's addresses scroll to the Deployment credentials card", async ({
  page,
}) => {
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    for (const path of ["/admin/integrations", "/system/integrations"]) {
      await page.goto(path);
      await expect(page).toHaveURL(/\/settings\/capabilities(#credentials)?$/);
      const card = page.locator("#credentials");
      await expect(card).toBeInViewport();
      const top = (await card.boundingBox()).y;
      expect(top, `${path} at ${viewport.width}px`).toBeGreaterThanOrEqual(0);
      expect(top).toBeLessThan(viewport.height / 2);
    }
  }
});
