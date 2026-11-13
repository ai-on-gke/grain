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

// F5 of the same walkthrough: the other way round. The card's writes
// do not go through capabilities:admin at all, and against a live daemon
// this role's PUT /api/settings and PUT/DELETE /api/secrets each returned
// 200 (docs/test-runs/2026-11-08-deployment-credentials-card/routes.md),
// yet the whole card was drawn read-only, saying it needed
// capabilities:admin.
const SECRETSONLY = {
  role: "secretsonly",
  roles: ["secretsonly"],
  full: false,
  scopes: [
    "capabilities:read",
    "repos:read",
    "secrets:read",
    "secrets:write",
    "settings:read",
    "settings:write",
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
      // The first wait after a fresh load covers the secrets list
      // arriving on a busy CI runner, not the assertion itself.
      await expect(card.getByText("cosign-key")).toBeVisible({
        timeout: 15000,
      });
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

test("a role with settings:write and secrets:write but not capabilities:admin can use the card, and only the rest of Capabilities is read-only", async ({
  page,
}) => {
  await page.goto("/settings/capabilities");
  expect(await putSecret(page, "/api/secrets/cosign-key/key", "k")).toBe(200);
  try {
    await asRole(page, SECRETSONLY);
    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/settings/capabilities#credentials");
      const card = page.locator("#credentials");
      // The first wait after a fresh load covers the secrets list
      // arriving on a busy CI runner, not the assertion itself.
      await expect(card.getByText("cosign-key")).toBeVisible({
        timeout: 15000,
      });
      // The page's note names what capabilities:admin holds back, which
      // is not this card.
      const pageNote = page.getByTestId("settings-role-read-only");
      await expect(pageNote).toContainText(
        "Capability servers, the capability policy and API scope sets are read-only for you.",
      );
      await expect(pageNote).toContainText("Needs capabilities:admin");
      await expect(
        page.locator("#capability-policy [data-settings-readonly]").first(),
      ).toBeVisible();
      await expect(card.getByTestId("credentials-read-only")).toHaveCount(0);
      await expect(card.locator("[data-settings-readonly]")).toHaveCount(0);
      await expect(card.locator("input[name=githubHost]")).toBeEnabled();
      await expect(card.locator("input[name=gcpProject]")).toBeEnabled();
      await expect(
        card.getByRole("button", { name: "Delete secret" }),
      ).toBeEnabled();
      await expect(
        card.getByRole("button", { name: "Set secret" }),
      ).toBeEnabled();
      const overflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
      const n = await pageNote.boundingBox();
      const main = await page.getByRole("main").boundingBox();
      expect(n.x + n.width).toBeLessThanOrEqual(main.x + main.width + 1);
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
    // The first wait after a fresh load covers the secrets list
    // arriving on a busy CI runner, not the assertion itself.
    await expect(card.getByText("cosign-key")).toBeVisible({ timeout: 15000 });
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

// Item 1 again: /settings/secrets scrolled Other secrets' heading flush
// against the foot of the sticky header, where every SettingsCard anchor
// keeps a 16px scroll margin.
test("/settings/secrets leaves Other secrets clear of the header", async ({
  page,
}) => {
  await page.goto("/settings/capabilities");
  expect(await putSecret(page, "/api/secrets/cosign-key/key", "k")).toBe(200);
  try {
    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/settings/secrets");
      await expect(page).toHaveURL(/\/settings\/capabilities#other-secrets$/);
      const heading = page.locator("#other-secrets");
      await expect(heading).toBeInViewport();
      await page.waitForTimeout(300);
      const gap = await page.evaluate(() => {
        const header = document.querySelector("header");
        const top = document
          .querySelector("#other-secrets")
          .getBoundingClientRect().top;
        return top - (header ? header.getBoundingClientRect().bottom : 0);
      });
      expect(gap, `gap at ${viewport.width}px`).toBeGreaterThanOrEqual(8);
    }
  } finally {
    await page.evaluate(() =>
      fetch("/api/secrets/cosign-key", { method: "DELETE" }),
    );
  }
});
