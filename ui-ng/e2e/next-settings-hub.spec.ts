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

import type { APIRequestContext, Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// Settings -> Hub at /next/settings/hub (task 6345, the review of PR
// #1635): who may change what, the config-repo lock, Plan / Preview
// changes for a hub edit, and the registration token against a real
// secret store (-secrets-store). Every test puts back what it changed.
test.use({ demoArgs: ["-secrets-store"] });

type Access = { role: string; scopes: string[] };

const VIEWER: Access = {
  role: "grain/viewer",
  scopes: ["settings:read", "deployment:read"],
};

// settings:write but not secrets:write: the catalog fields are theirs,
// where the credential goes is not (checkHubAuthTargetWrite).
const EDITOR: Access = {
  role: "grain/editor",
  scopes: [
    "settings:read",
    "settings:write",
    "secrets:read",
    "deployment:read",
  ],
};

async function actAs(page: Page, access: Access): Promise<void> {
  await page.route(
    (url) => url.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      const json = {
        ...(await response.json()),
        access: { ...access, roles: [access.role], full: false },
      };
      await route.fulfill({ response, json });
    },
  );
}

async function hubSettings(
  request: APIRequestContext,
): Promise<Record<string, unknown>> {
  const res = await request.get("/api/settings");
  expect(res.ok()).toBeTruthy();
  return (await res.json()).hub ?? {};
}

const authTarget = (page: Page) => [
  page.getByLabel("Hub URL"),
  page.getByLabel("Entry name"),
  page.getByRole("button", { name: /^Hub credential mode/ }),
  page.getByLabel("OIDC audience"),
];

test("a viewer sees Hub read-only, with why Heartbeat now is off", async ({
  page,
}) => {
  await actAs(page, VIEWER);
  await page.goto("/next/settings/hub");
  await expect(
    page.getByRole("heading", { name: "Hub connection" }),
  ).toBeVisible();
  const beat = page.getByRole("button", { name: "Heartbeat now" });
  await expect(beat).toBeDisabled();
  await expect(
    page.getByText(/does not hold settings:write/).first(),
  ).toBeVisible();
  for (const control of authTarget(page)) await expect(control).toBeDisabled();
  await expect(page.getByLabel("Purpose")).toBeDisabled();
  await expect(
    page.getByRole("switch", { name: "Listed in hub directory" }),
  ).toBeDisabled();
  await expect(page.getByRole("button", { name: "Add rule" })).toBeDisabled();
  // They are not told about secrets:write on top.
  await expect(
    page.getByText(/where this daemon sends its credential/),
  ).toHaveCount(0);
});

test("an editor without secrets:write edits the catalog but not where the credential goes", async ({
  page,
  request,
}) => {
  const before = await hubSettings(request);
  await actAs(page, EDITOR);
  try {
    await page.goto("/next/settings/hub");
    for (const control of authTarget(page)) {
      await expect(control).toBeDisabled();
    }
    await expect(
      page.getByText(
        /where this daemon sends its credential\. .*grain\/editor does not hold secrets:write/,
      ),
    ).toBeVisible();
    // Heartbeat now is theirs (settings:write).
    await expect(
      page.getByRole("button", { name: "Heartbeat now" }),
    ).toBeEnabled();

    const purpose = page.getByLabel("Purpose");
    await expect(purpose).toBeEnabled();
    await purpose.fill("Editor-owned purpose");
    const saved = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Save changes" }).click();
    expect((await saved).ok()).toBeTruthy();
    expect((await hubSettings(request))["purpose"]).toBe(
      "Editor-owned purpose",
    );
  } finally {
    const res = await request.post("/api/settings", { data: { hub: before } });
    expect(res.ok()).toBeTruthy();
  }
});

test("a config-repo lock marks Hub managed there and leaves the token editable", async ({
  page,
}) => {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const response = await route.fetch();
    const json = { ...(await response.json()), configRepoOnly: true };
    await route.fulfill({ response, json });
  });
  await page.goto("/next/settings/hub");
  await expect(page.getByText("Managed in config repo")).toBeVisible();
  await expect(
    page.getByText(/Secrets and API keys remain editable/),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "View in Config repo" }),
  ).toBeVisible();
  for (const control of authTarget(page)) await expect(control).toBeDisabled();
  await expect(page.getByLabel("Purpose")).toBeDisabled();
  await expect(page.getByLabel("Registration token (grc_…)")).toBeEnabled();
});

test("Plan / Preview changes lists a hub edit field by field and passes axe @dark", async ({
  page,
  axe,
}) => {
  await page.goto("/next/settings/hub");
  await page
    .getByLabel("Purpose")
    .fill(
      "A deliberately long purpose that would once have widened the plan's table into a sideways scroll: https://hub.example.test/entries/acme-core",
    );
  await page.getByRole("button", { name: "Plan / Preview changes" }).click();
  const table = page.getByRole("table", { name: "Planned setting changes" });
  await expect(table).toBeVisible();
  const row = table.getByRole("row", { name: /Hub purpose/ });
  await expect(row).toBeVisible();
  await expect(table).not.toContainText("{");
  const wrap = page.getByRole("region", { name: "Planned setting changes" });
  const overflow = await wrap.evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  expect(await axe()).toEqual([]);
  await page.getByRole("button", { name: "Discard" }).click();
});

test("the registration token stores and clears against a real secret store", async ({
  page,
  request,
}) => {
  await page.goto("/next/settings/hub");
  const token = page.getByLabel("Registration token (grc_…)");
  await expect(token).toBeEnabled();
  await expect(token).toHaveAttribute("type", "password");
  const credential = page.getByRole("heading", {
    name: "Registration credential",
  });
  const state = credential.locator("xpath=..").getByText(/^(Stored|Not set)$/);
  await expect(state).toHaveText("Not set");

  try {
    await token.fill("grc_e2e_settings_hub");
    const put = page.waitForResponse(
      (r) =>
        r.url().includes("/api/secrets/registry-registration-token/value") &&
        r.request().method() === "PUT",
    );
    await page.getByRole("button", { name: "Store", exact: true }).click();
    expect((await put).ok()).toBeTruthy();
    await expect(state).toHaveText("Stored");
    await expect(token).toHaveValue("");
    await expect(
      page.getByRole("button", { name: "Replace", exact: true }),
    ).toBeVisible();
    expect(
      (await (await request.get("/api/settings")).json()).hubTokenSet,
    ).toBe(true);

    await page.getByRole("button", { name: "Clear", exact: true }).click();
    await page.getByRole("button", { name: "Clear token" }).click();
    await expect(state).toHaveText("Not set");
    expect(
      (await (await request.get("/api/settings")).json()).hubTokenSet,
    ).toBe(false);
  } finally {
    await request.delete("/api/secrets/registry-registration-token/value");
  }
});
