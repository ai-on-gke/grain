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

// The Deployment credentials card and its secrets on
// /next/settings/capabilities, ported from classic Settings
// (gap G5 of docs/test-runs/2026-11-14-connections-infrastructure-settings.md).
// `grain demo` runs with no secret store, so the secrets tests answer
// /api/secrets from an in-memory store of their own; nothing real is
// written.

// mockSecretStore answers GET, PUT and DELETE /api/secrets* from `store`
// (name -> {keys, capabilityServers?, orphaned?}) and records each write.
async function mockSecretStore(page, store) {
  const writes = [];
  const body = () => ({
    enabled: true,
    secrets: Object.entries(store)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, s]) => ({ name, ...s })),
  });
  await page.route(/\/api\/secrets(\/[^?]*)?(\?.*)?$/, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const parts = url.pathname
      .replace(/^.*\/api\/secrets\/?/, "")
      .split("/")
      .filter(Boolean)
      .map(decodeURIComponent);
    const method = req.method();
    if (parts[0] === "redact") return route.fallback();
    if (method === "GET" && parts.length === 0) {
      return route.fulfill({ json: body() });
    }
    if (method === "PUT" && parts.length === 2) {
      const [name, key] = parts;
      writes.push({ method, name, key, body: req.postDataJSON() });
      const s = store[name] ?? (store[name] = { keys: [] });
      if (!s.keys.includes(key)) s.keys.push(key);
      return route.fulfill({ json: body() });
    }
    if (method === "DELETE") {
      const [name, key] = parts;
      writes.push({ method, name, key });
      if (key) {
        store[name].keys = store[name].keys.filter((k) => k !== key);
        if (store[name].keys.length === 0) delete store[name];
      } else {
        delete store[name];
      }
      return route.fulfill({ json: body() });
    }
    return route.fallback();
  });
  return writes;
}

// asRole makes GET /api/config report `scopes` as the signed-in role's.
async function asRole(page, role, scopes) {
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const json = await res.json();
    json.access = { role, roles: [role], full: false, scopes };
    await route.fulfill({ response: res, json });
  });
}

const card = (page) => page.getByTestId("card-deployment-credentials");

test("Integrations lands on the Deployment credentials card, below the header", async ({
  page,
  axe,
}) => {
  const settings = await (await page.request.get("/api/settings")).json();
  await page.goto("/next/system/integrations");
  await expect(page).toHaveURL(/\/next\/settings\/capabilities#credentials$/);
  await expect(page.locator("#credentials")).toBeInViewport();
  await expect(
    card(page).getByRole("heading", { name: "Deployment credentials" }),
  ).toBeVisible();

  // The card stops clear of every header pinned over the page.
  await expect
    .poll(() =>
      page.evaluate(() => {
        const card = document.getElementById("credentials");
        const pinned = [...document.querySelectorAll("header")].filter((h) =>
          ["sticky", "fixed"].includes(getComputedStyle(h).position),
        );
        const bottom = Math.max(
          0,
          ...pinned.map((h) => h.getBoundingClientRect().bottom),
        );
        return card.getBoundingClientRect().top - bottom;
      }),
    )
    .toBeGreaterThanOrEqual(0);

  const host = card(page).getByLabel("GitHub host", { exact: true });
  await expect(host).toHaveValue(settings.githubHost || "");
  await expect(card(page).getByLabel("GCP project")).toHaveValue(
    settings.gcpProject || "",
  );
  await expect(card(page).getByLabel("GCP service account count")).toHaveValue(
    String(settings.gcpServiceAccountCount || 1),
  );

  // An edit stages into the save bar; Discard puts the saved value back.
  await host.fill("ghe.example.com:8443");
  const discard = page.getByRole("button", { name: "Discard" });
  await expect(discard).toBeVisible();
  await card(page).getByLabel("GCP service account count").fill("0");
  await expect(page.getByTestId("cred-gcp-count-error")).toContainText(
    "at least 1",
  );
  await discard.click();
  await expect(host).toHaveValue(settings.githubHost || "");
  await expect(card(page).getByLabel("GCP service account count")).toHaveValue(
    String(settings.gcpServiceAccountCount || 1),
  );

  // No secret store on the demo: the card says so and offers no form.
  await expect(page.getByTestId("secrets-store-disabled")).toBeVisible();
  expect(await axe()).toEqual([]);
});

test("An old /settings/secrets link lands on the card's secrets", async ({
  page,
}) => {
  await page.goto("/next/settings/secrets");
  await expect(page).toHaveURL(/\/next\/settings\/capabilities#other-secrets$/);
  await expect(page.locator("#other-secrets")).toBeInViewport();
});

test("Secrets lists names and keys, masks values, and replaces and removes them", async ({
  page,
}) => {
  const store = {
    // What retired in-process capabilities left behind stays listed.
    "github-app": { keys: ["app-id", "private-key"] },
    "cosign-key": { keys: ["value"] },
    "jira-token": { keys: ["value"], capabilityServers: ["jira"] },
    "grain-server-old": { keys: ["value"], orphaned: true },
    // A repository's release credentials are set on its own page.
    "repo-release-acme-widgets": { keys: ["value"] },
  };
  const writes = await mockSecretStore(page, store);
  await page.goto("/next/settings/capabilities#other-secrets");
  const table = page.getByRole("table", { name: "Secrets" });
  await expect(table).toBeVisible();

  await expect(page.getByTestId("secret-row-github-app-app-id")).toBeVisible();
  await expect(
    page.getByTestId("secret-row-github-app-private-key"),
  ).toBeVisible();
  await expect(page.getByTestId("secret-row-cosign-key-value")).toBeVisible();
  await expect(
    page.getByTestId("secret-row-repo-release-acme-widgets-value"),
  ).toHaveCount(0);
  await expect(
    page.getByTestId("secret-row-grain-server-old-value"),
  ).toContainText("orphaned");

  // A capability server's own secret has no Replace or Remove.
  const owned = page.getByTestId("secret-row-jira-token-value");
  await expect(owned).toContainText("jira");
  await expect(owned.getByRole("button")).toHaveCount(0);

  // Values are never shown: only a mask.
  await expect(
    page.getByTestId("secret-row-github-app-private-key").locator("td").nth(2),
  ).toHaveText(/^\s*•+\s*hidden\s*$/);

  // Replace fills the form, focuses the masked value box and PUTs the
  // same name and key.
  await page.getByRole("button", { name: "Replace cosign-key/value" }).click();
  const value = page.getByLabel("Value", { exact: true });
  await expect(value).toBeFocused();
  await expect(page.getByLabel("Secret", { exact: true })).toHaveValue(
    "cosign-key",
  );
  await expect(page.getByLabel("Key", { exact: true })).toHaveValue("value");
  const security = await value.evaluate((el) =>
    getComputedStyle(el).getPropertyValue("-webkit-text-security"),
  );
  expect(security).toBe("disc");
  await value.fill("not-a-real-signing-key");
  await page.getByRole("button", { name: "Replace secret" }).click();
  await expect.poll(() => writes.length).toBe(1);
  expect(writes[0]).toEqual({
    method: "PUT",
    name: "cosign-key",
    key: "value",
    body: { value: "not-a-real-signing-key" },
  });
  await expect(value).toHaveValue("");
  await expect(page.locator("body")).not.toContainText(
    "not-a-real-signing-key",
  );
  await expect(
    page.getByRole("button", { name: "Store secret" }),
  ).toBeVisible();

  // Removing one key of several asks first; Cancel keeps it.
  await page.getByRole("button", { name: "Remove github-app/app-id" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Remove github-app/app-id?");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  expect(writes.length).toBe(1);
  await page.getByRole("button", { name: "Remove github-app/app-id" }).click();
  await dialog.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.getByTestId("secret-row-github-app-app-id")).toHaveCount(0);
  await expect(
    page.getByTestId("secret-row-github-app-private-key"),
  ).toBeVisible();
  expect(writes[1]).toEqual({
    method: "DELETE",
    name: "github-app",
    key: "app-id",
  });

  // Removing a secret's only key removes the secret.
  await page.getByRole("button", { name: "Remove cosign-key/value" }).click();
  await expect(dialog).toContainText("Remove the secret cosign-key?");
  await dialog.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.getByTestId("secret-row-cosign-key-value")).toHaveCount(0);
  expect(writes[2]).toEqual({
    method: "DELETE",
    name: "cosign-key",
    key: undefined,
  });

  // A new secret is stored from the same form.
  await page.getByLabel("Secret", { exact: true }).fill("webhook");
  await page.getByLabel("Key", { exact: true }).fill("token");
  await value.fill("not-a-real-token");
  await page.getByRole("button", { name: "Store secret" }).click();
  await expect(page.getByTestId("secret-row-webhook-token")).toBeVisible();
  expect(writes[3]).toMatchObject({ method: "PUT", name: "webhook" });
});

test("Locked settings: the fields are read-only, secrets stay editable", async ({
  page,
}) => {
  await mockSecretStore(page, { "cosign-key": { keys: ["value"] } });
  for (const glob of ["**/api/settings", "**/api/config"]) {
    await page.route(glob, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const res = await route.fetch();
      await route.fulfill({
        response: res,
        json: { ...(await res.json()), settingsLocked: true },
      });
    });
  }
  await page.goto("/next/settings/capabilities#credentials");
  await expect(page.getByTestId("credentials-read-only")).toContainText(
    "managed in the config repository",
  );
  for (const name of [
    "GitHub host",
    "GCP project",
    "GCP service account email",
    "GCP service account count",
  ]) {
    await expect(card(page).getByLabel(name, { exact: true })).toBeDisabled();
  }
  await expect(
    card(page).getByRole("switch", { name: "Speak plain HTTP to GitHub host" }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Replace cosign-key/value" }),
  ).toBeEnabled();
  await expect(
    page.getByRole("button", { name: "Remove cosign-key/value" }),
  ).toBeEnabled();
  await expect(page.getByLabel("Value", { exact: true })).toBeEnabled();
});

test("A role without settings:write or secrets:write sees the card read-only", async ({
  page,
}) => {
  await mockSecretStore(page, { "cosign-key": { keys: ["value"] } });
  await asRole(page, "grain/viewer", [
    "settings:read",
    "capabilities:read",
    "secrets:read",
  ]);
  await page.goto("/next/settings/capabilities#credentials");
  await expect(page.getByTestId("credentials-read-only")).toContainText(
    "Read-only for you",
  );
  await expect(page.getByTestId("credentials-read-only")).toContainText(
    "settings:write",
  );
  await expect(
    card(page).getByLabel("GitHub host", { exact: true }),
  ).toBeDisabled();
  // The secret is still listed, but nothing can change it.
  await expect(page.getByTestId("secret-row-cosign-key-value")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Replace cosign-key/value" }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Remove cosign-key/value" }),
  ).toBeDisabled();
  await expect(page.getByLabel("Value", { exact: true })).toBeDisabled();
  await expect(page.getByTestId("store-secret-btn")).toBeDisabled();
});

test("A role holding settings:write alone may edit the fields but not the secrets", async ({
  page,
}) => {
  await mockSecretStore(page, { "cosign-key": { keys: ["value"] } });
  await asRole(page, "grain/settings-editor", [
    "settings:read",
    "settings:write",
    "capabilities:read",
    "secrets:read",
  ]);
  await page.goto("/next/settings/capabilities#credentials");
  await expect(page.getByTestId("credentials-read-only")).toContainText(
    "Secrets are read-only for you",
  );
  await expect(
    card(page).getByLabel("GitHub host", { exact: true }),
  ).toBeEnabled();
  await expect(
    page.getByRole("button", { name: "Remove cosign-key/value" }),
  ).toBeDisabled();
});

test("A role without secrets:read is told why no secrets are listed", async ({
  page,
}) => {
  await asRole(page, "grain/viewer", ["settings:read", "capabilities:read"]);
  await page.goto("/next/settings/capabilities#other-secrets");
  await expect(page.getByTestId("secrets-list-restricted")).toContainText(
    "secrets:read",
  );
  await expect(page.getByTestId("secret-form")).toHaveCount(0);
});

test("The secrets table stays inside its card on a phone and in dark @phone @dark", async ({
  page,
  axe,
}) => {
  await mockSecretStore(page, {
    "github-app": { keys: ["app-id", "private-key"] },
    "a-very-long-secret-name-for-a-custom-capability-server": {
      keys: ["a-long-key-name"],
    },
  });
  await page.goto("/next/settings/capabilities#other-secrets");
  const table = page.getByRole("table", { name: "Secrets" });
  await expect(table).toBeVisible();
  const cardBox = await card(page).boundingBox();
  for (const row of await table.locator("tbody tr").all()) {
    const b = await row.boundingBox();
    expect(b.x).toBeGreaterThanOrEqual(cardBox.x - 1);
    expect(b.x + b.width).toBeLessThanOrEqual(cardBox.x + cardBox.width + 1);
  }
  const form = await page.getByTestId("secret-form").boundingBox();
  expect(form.x + form.width).toBeLessThanOrEqual(
    cardBox.x + cardBox.width + 1,
  );
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  expect(await axe()).toEqual([]);
});
