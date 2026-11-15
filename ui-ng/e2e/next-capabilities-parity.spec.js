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

// Classic parity for /next/settings/capabilities (task 5520, from the
// 2026-11-13 connections & infrastructure walkthrough): stored secrets
// listed by name with replace and remove, API scope set removal, the
// Deployment credentials card, retired in-process grant notices and the
// needs-host warning. Runs over `grain demo -secrets-store`, so secrets
// really are stored and removed. Every test puts back what it changed.

test.use({ demoArgs: ["-secrets-store"] });

async function confirmDialog(page, button) {
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: button, exact: true }).click();
  await expect(dialog).toHaveCount(0);
}

test("Stored secrets lists names, masks the value, replaces and removes with a confirm", async ({
  page,
  request,
}) => {
  await page.goto("/next/settings/capabilities#other-secrets");
  const section = page.getByTestId("stored-secrets");
  await expect(section).toBeInViewport();
  await expect(page.getByTestId("secrets-store-disabled")).toHaveCount(0);

  const value = page.getByTestId("input-secret-value");
  await expect(value).toHaveAttribute("type", "password");
  await page.getByTestId("input-secret-name").fill("walk-parity");
  await page.getByTestId("input-secret-key").fill("token");
  await value.fill("first-value");
  const store = page.getByTestId("store-secret-btn");
  await expect(store).toHaveText("Store secret");
  try {
    await store.click();
    const row = page.getByTestId("secret-row-walk-parity");
    await expect(row).toContainText("walk-parity");
    await expect(row).toContainText("token");
    // Never the value, anywhere on the page.
    await expect(page.locator("body")).not.toContainText("first-value");
    await expect(value).toHaveValue("");

    // Replace fills the name and key in and focuses the value box.
    await page.getByTestId("replace-secret-walk-parity-token").click();
    await expect(page.getByTestId("input-secret-name")).toHaveValue(
      "walk-parity",
    );
    await expect(page.getByTestId("input-secret-key")).toHaveValue("token");
    await expect(value).toBeFocused();
    await value.fill("second-value");
    await expect(store).toHaveText("Replace secret");
    const put = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/secrets/walk-parity/token") &&
        r.request().method() === "PUT",
    );
    await store.click();
    expect((await put).ok()).toBeTruthy();

    // A second key, so removing one key leaves the secret.
    await page.getByTestId("input-secret-key").fill("extra");
    await value.fill("x");
    await store.click();
    await expect(row).toContainText("extra");

    // Removing a key asks first; Cancel keeps it.
    await page.getByTestId("remove-secret-key-walk-parity-extra").click();
    await confirmDialog(page, "Cancel");
    await expect(row).toContainText("extra");
    await page.getByTestId("remove-secret-key-walk-parity-extra").click();
    await confirmDialog(page, "Remove");
    await expect(
      page.getByTestId("remove-secret-key-walk-parity-extra"),
    ).toHaveCount(0);

    await page.getByTestId("remove-secret-walk-parity").click();
    await confirmDialog(page, "Remove");
    await expect(row).toHaveCount(0);
    const listed = await (await request.get("/api/secrets")).json();
    expect(listed.secrets.map((s) => s.name)).not.toContain("walk-parity");
  } finally {
    await request.delete("/api/secrets/walk-parity");
  }
});

test("Secrets stay usable while settings are locked; the rest of the page is dimmed", async ({
  page,
}) => {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: { ...body, configRepoOnly: true },
    });
  });
  await page.goto("/next/settings/capabilities#credentials");
  await expect(page.getByTestId("settings-config-repo-banner")).toBeVisible();
  await expect(
    page.getByTestId("input-credentials-gcp-project"),
  ).toBeDisabled();
  await expect(page.getByTestId("credentials-settings")).toHaveClass(
    /s-dimmed/,
  );
  await expect(page.getByTestId("card-capability-policy")).toHaveClass(
    /s-dimmed/,
  );
  await expect(page.getByTestId("credentials-read-only")).toContainText(
    "stored secrets stay editable",
  );
  await expect(page.getByTestId("input-secret-value")).toBeEnabled();
  await expect(page.getByTestId("stored-secrets")).not.toHaveClass(/s-dimmed/);
});

test("Deployment credentials shows and saves the GitHub host and GCP settings", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/settings")).json();
  await page.goto("/next/settings/credentials");
  await expect(page).toHaveURL(/\/next\/settings\/capabilities#credentials$/);
  const card = page.getByTestId("card-credentials");
  await expect(card).toBeInViewport();
  await expect(page.getByTestId("input-credentials-github-host")).toHaveValue(
    before.githubHost || "",
  );
  await expect(page.getByTestId("credentials-gcp-link")).toHaveAttribute(
    "href",
    /#capability-servers$/,
  );
  try {
    await page.getByTestId("input-credentials-gcp-project").fill("walk-123");
    await page.getByTestId("input-credentials-gcp-count").fill("3");
    const done = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "POST",
    );
    await page.getByTestId("settings-save-btn").click();
    expect((await done).ok()).toBeTruthy();
    const after = await (await request.get("/api/settings")).json();
    expect(after.gcpProject).toBe("walk-123");
    expect(after.gcpServiceAccountCount).toBe(3);
    await page.reload();
    await expect(page.getByTestId("input-credentials-gcp-project")).toHaveValue(
      "walk-123",
    );
  } finally {
    await request.post("/api/settings", {
      data: {
        gcpProject: before.gcpProject || "",
        gcpServiceAccountCount: before.gcpServiceAccountCount || 1,
      },
    });
  }
});

test("API scope sets: Remove asks first, naming what grants the set", async ({
  page,
  request,
}) => {
  const name = "walk/parity";
  const put = await request.put("/api/api-scope-sets", {
    data: { set: { name, scopes: ["tasks:read"] } },
  });
  expect(put.ok()).toBeTruthy();
  try {
    await page.goto("/next/settings/capabilities#api-scope-sets");
    const row = page.getByTestId("scope-set-row-" + name);
    await expect(row).toContainText("tasks:read");
    await expect(row).toContainText("declared");
    // A built-in set has no Remove.
    const builtin = page
      .locator('[data-testid^="scope-set-row-grain/"]')
      .first();
    await expect(builtin).toContainText("built-in");
    await expect(builtin.getByRole("button", { name: "Remove" })).toHaveCount(
      0,
    );

    await page.getByTestId("remove-scope-set-" + name).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Remove scope set walk/parity?");
    await expect(dialog).toContainText("Nothing grants api:walk/parity.");
    await confirmDialog(page, "Cancel");
    await expect(row).toBeVisible();
    await page.getByTestId("remove-scope-set-" + name).click();
    await confirmDialog(page, "Remove");
    await expect(row).toHaveCount(0);
    const sets = await (await request.get("/api/api-scope-sets")).json();
    expect(sets.sets.map((s) => s.name)).not.toContain(name);
  } finally {
    await request.post("/api/api-scope-sets/delete", { data: { name } });
  }
});

test("Retired in-process grants and away hosts say what to do", async ({
  page,
}) => {
  const placedId = "https://capabilities.walk.example.test/placed";
  // The demo's settings/capabilities.yml configures no retired
  // in_process entry, and its daemon cannot place a server on a host:
  // both are added to the real server's answers here.
  await page.route("**/api/capability-policy", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    body.policy = {
      ...body.policy,
      catalogue: {
        ...body.policy?.catalogue,
        inProcess: {
          "gcp-key": { restrictions: { repos: ["acme/*"] } },
          "github-sandbox": {},
        },
      },
    };
    await route.fulfill({ response: res, json: body });
  });
  await page.route("**/api/capability-servers", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    body.push({
      id: placedId,
      config: { command: "walk-tool", run: "host", hostd: "away-box" },
      status: { id: placedId, state: "needs-hostd:away-box" },
    });
    await route.fulfill({ response: res, json: body });
  });
  await page.goto("/next/settings/capabilities");
  await expect(page.getByTestId("retired-grant-gcp-key")).toContainText(
    "in_process.gcp-key is deprecated (repos acme/*)",
  );
  await expect(page.getByTestId("retired-grant-gcp-key")).toContainText(
    "https://grain.dev/capabilities/gcp#gcp-key",
  );
  await expect(page.getByTestId("retired-grant-github-sandbox")).toContainText(
    "github#sandbox-repo",
  );
  await expect(
    page.getByTestId("cap-server-needs-host-" + placedId),
  ).toContainText("host away-box, which is not connected");
  await page
    .getByTestId("capability-servers-vs-legacy")
    .getByRole("link", { name: "Deployment credentials" })
    .click();
  await expect(page.getByTestId("card-credentials")).toBeInViewport();
});
