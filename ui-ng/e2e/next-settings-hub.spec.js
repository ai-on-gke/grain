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

// Settings -> Hub at /next/settings/hub: what classic Settings -> Hub
// (ui/src/components/settings/HubSection.jsx) shows and /next/ did not
// (gap G6 of docs/test-runs/2026-11-14-connections-infrastructure-settings.md).
// `grain demo` registers with a simulated hub (pkg/demo/demo_hub.go).
// Every test that saves puts the hub settings back.

async function hubSettings(request) {
  const res = await request.get("/api/settings");
  expect(res.ok()).toBeTruthy();
  return (await res.json()).hub ?? {};
}

async function restoreHub(request, hub) {
  const res = await request.post("/api/settings", { data: { hub } });
  expect(res.ok(), await res.text()).toBeTruthy();
}

async function saveBar(page) {
  const btn = page.getByTestId("settings-save-btn");
  await expect(btn).toBeEnabled();
  const done = page.waitForResponse(
    (r) => r.url().endsWith("/api/settings") && r.request().method() === "POST",
  );
  await btn.click();
  return done;
}

test("Hub shows its connection, heartbeat, health and advertised scopes", async ({
  page,
}) => {
  await page.goto("/next/settings/hub");
  await expect(page.getByTestId("settings-section-heading")).toHaveText("Hub");
  const card = page.getByTestId("card-registry");
  await expect(card.locator(".s-card-title")).toHaveText("Hub connection");
  await expect(page.getByTestId("registry-status-pill")).toContainText(
    "Connected",
  );
  await expect(page.getByTestId("registry-endpoint")).toHaveText(
    "https://registry.grain.example.com · acme-widgets",
  );
  const line = page.getByTestId("registry-heartbeat-line");
  await expect(line).toContainText("Last heartbeat");
  await expect(line).toContainText("(every 15s)");
  await expect(line).toContainText("health ok");
  await expect(line).toContainText("awaiting reply");

  // Every advertised scope carries its prefix chip, its role and where
  // it came from.
  const rows = page.getByTestId("registry-scope-row");
  await expect(rows.first()).toBeVisible();
  const n = await rows.count();
  for (let i = 0; i < n; i++) {
    const row = rows.nth(i);
    await expect(row.locator(".hub-chip").first()).not.toHaveText("");
    await expect(row.getByTestId("registry-scope-role")).toHaveText(
      /primary|secondary/,
    );
    await expect(row.getByTestId("registry-scope-origin")).toHaveText(
      /from repo config|override|added in settings/,
    );
  }

  // Heartbeat now posts and reads back the snapshot it returns.
  const hb = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/registry/heartbeat") &&
      r.request().method() === "POST",
  );
  await page.getByTestId("registry-heartbeat-btn").click();
  expect((await hb).ok()).toBeTruthy();
  await expect(page.getByTestId("registry-status-pill")).toContainText(
    "Connected",
  );
});

test("The old registry address opens Hub", async ({ page }) => {
  await page.goto("/next/settings/registry");
  await expect(page.getByTestId("settings-section-heading")).toHaveText("Hub");
  await expect(page.getByTestId("card-hub-scopes")).toBeVisible();
});

test("Hub shows the hub's error and scope conflicts as alerts", async ({
  page,
}) => {
  await page.route("**/api/registry", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...body,
        state: "error",
        lastError: "heartbeat refused: entry `acme-widgets` is suspended",
        scopeConflicts: [
          {
            repo: "acme/widgets",
            path_prefix: "services/billing",
            conflicting_entry: "billing-grain",
          },
          { repo: "acme/gadgets", path_prefix: "" },
        ],
      },
    });
  });
  await page.goto("/next/settings/hub");
  await expect(page.getByTestId("registry-status-pill")).toContainText(
    "Heartbeat error",
  );
  const err = page.getByTestId("registry-last-error");
  await expect(err).toHaveAttribute("role", "alert");
  await expect(err).toContainText("is suspended");
  // The message is Markdown: the entry name is code, not backticks.
  await expect(err.locator("code")).toHaveText("acme-widgets");
  const conflicts = page.getByTestId("registry-scope-conflicts");
  await expect(conflicts).toHaveAttribute("role", "status");
  await expect(conflicts.locator("li")).toHaveCount(2);
  await expect(conflicts.locator("li").first()).toContainText(
    "acme/widgets (services/billing) is already claimed as primary by billing-grain",
  );
  await expect(conflicts.locator("li").nth(1)).toContainText(
    "acme/gadgets is already claimed as primary by another entry",
  );
});

test("Hub edits the OIDC audience, platform, purpose and listed flag", async ({
  page,
  request,
}) => {
  const before = await hubSettings(request);
  try {
    await page.goto("/next/settings/hub");
    const aud = page.getByTestId("input-hub-oidc-audience");
    // The audience only means something in OIDC mode.
    await expect(aud).toBeDisabled();
    await expect(page.getByTestId("input-hub-purpose")).toHaveValue(
      before.purpose,
    );
    const listed = page.getByTestId("switch-hub-listed");
    await expect(listed).toHaveAttribute("aria-checked", "true");
    await expect(page.getByTestId("select-hub-host-kind")).toContainText(
      "Workstation",
    );

    await page.getByTestId("select-hub-auth-mode").click();
    await page.getByTestId("select-hub-auth-mode-opt-oidc").click();
    await expect(aud).toBeEnabled();
    await aud.fill("https://hub.example.test/aud");
    await page.getByTestId("select-hub-host-kind").click();
    await page.getByTestId("select-hub-host-kind-opt-kubernetes").click();
    await page.getByTestId("input-hub-purpose").fill("Walk purpose");
    await listed.click();
    await expect(listed).toHaveAttribute("aria-checked", "false");

    const res = await saveBar(page);
    expect(res.status()).toBe(200);
    const after = await hubSettings(request);
    expect(after.authMode).toBe("oidc");
    expect(after.oidcAudience).toBe("https://hub.example.test/aud");
    expect(after.hostKind).toBe("kubernetes");
    expect(after.purpose).toBe("Walk purpose");
    expect(after.listed ?? false).toBe(false);

    await page.reload();
    await expect(page.getByTestId("input-hub-oidc-audience")).toHaveValue(
      "https://hub.example.test/aud",
    );
    await expect(page.getByTestId("select-hub-host-kind")).toContainText(
      "Kubernetes",
    );
    await expect(page.getByTestId("switch-hub-listed")).toHaveAttribute(
      "aria-checked",
      "false",
    );
  } finally {
    await restoreHub(request, before);
  }
});

test("Hub stores and clears the registration token in the secret store", async ({
  page,
}) => {
  const puts = [];
  const deletes = [];
  let stored = false;
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    await route.fulfill({
      response: res,
      json: { ...(await res.json()), hubTokenSet: stored },
    });
  });
  await page.route(
    "**/api/secrets/registry-registration-token/value",
    async (route) => {
      const m = route.request().method();
      if (m === "PUT") {
        puts.push(route.request().postDataJSON());
        stored = true;
      } else if (m === "DELETE") {
        deletes.push(m);
        stored = false;
      }
      await route.fulfill({ json: {} });
    },
  );
  await page.goto("/next/settings/hub");
  const state = page.getByTestId("registry-token-state");
  await expect(state).toContainText("Not set");
  const input = page.getByTestId("input-hub-token");
  await expect(input).toHaveAttribute("type", "password");
  await expect(input).toHaveAttribute("autocomplete", "new-password");
  const store = page.getByTestId("store-hub-token-btn");
  await expect(store).toBeDisabled();
  await input.fill("grc_example_not_a_real_token");
  await input.press("Enter");
  await expect(state).toContainText("Stored");
  expect(puts).toEqual([{ value: "grc_example_not_a_real_token" }]);
  // The box empties once the value is stored; nothing reads it back.
  await expect(input).toHaveValue("");
  await expect(store).toHaveText("Replace");

  // Clearing asks first.
  await page.getByTestId("clear-hub-token-btn").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Clear the registration token?");
  expect(deletes).toHaveLength(0);
  await dialog.getByRole("button", { name: "Clear token" }).click();
  await expect(state).toContainText("Not set");
  expect(deletes).toHaveLength(1);
});

test("Scope overrides add and remove Primary, Secondary and Exclude rules with a path prefix", async ({
  page,
  request,
}) => {
  const before = await hubSettings(request);
  try {
    await page.goto("/next/settings/hub");
    await expect(page.getByTestId("registry-no-rules")).toBeVisible();
    const add = page.getByTestId("add-hub-rule-btn");
    await expect(add).toBeDisabled();

    const addRule = async (repo, prefix, mode) => {
      await page.getByTestId("input-hub-rule-repo").fill(repo);
      await page.getByTestId("input-hub-rule-prefix").fill(prefix);
      await page.getByTestId("select-hub-rule-mode").click();
      await page.getByTestId(`select-hub-rule-mode-opt-${mode}`).click();
      await add.click();
      await expect(page.getByTestId("input-hub-rule-repo")).toHaveValue("");
    };
    await addRule("acme/monorepo", "/services/billing/", "primary");
    await addRule("acme/monorepo", "docs", "secondary");
    await addRule("acme/legacy", "", "exclude");

    const rows = page.getByTestId("registry-rule-row");
    await expect(rows).toHaveCount(3);
    // The prefix is trimmed of its slashes and shown as a chip.
    await expect(rows.nth(0)).toContainText("services/billing/");
    await expect(rows.nth(0).getByTestId("registry-rule-mode")).toHaveText(
      "primary",
    );
    await expect(rows.nth(1).getByTestId("registry-rule-mode")).toHaveText(
      "secondary",
    );
    await expect(rows.nth(2).getByTestId("registry-rule-mode")).toHaveText(
      "excluded",
    );

    expect((await saveBar(page)).status()).toBe(200);
    expect((await hubSettings(request)).scopes).toEqual([
      {
        repo: "acme/monorepo",
        pathPrefix: "services/billing",
        role: "primary",
      },
      { repo: "acme/monorepo", pathPrefix: "docs", role: "secondary" },
      { repo: "acme/legacy", pathPrefix: "", exclude: true },
    ]);

    // The advertised scopes are read again after the save, and an added
    // repository shows where it came from.
    const added = page
      .getByTestId("registry-scope-row")
      .filter({ hasText: "services/billing/" });
    await expect(added.getByTestId("registry-scope-origin")).toHaveText(
      "added in settings",
    );

    await page.reload();
    await expect(page.getByTestId("registry-rule-row")).toHaveCount(3);
    await page.getByTestId("remove-hub-rule-1").click();
    await expect(page.getByTestId("registry-rule-row")).toHaveCount(2);
    expect((await saveBar(page)).status()).toBe(200);
    expect(
      ((await hubSettings(request)).scopes ?? []).map((r) => r.pathPrefix),
    ).toEqual(["services/billing", ""]);
  } finally {
    await restoreHub(request, before);
  }
});

test("Hub's cards fit the screen at every width @phone @dark", async ({
  page,
  request,
}) => {
  const before = await hubSettings(request);
  try {
    await restoreHub(request, {
      ...before,
      scopes: [
        {
          repo: "acme/a-repository-with-a-rather-long-name",
          pathPrefix: "services/billing/some/deeply/nested/component",
          role: "primary",
        },
      ],
    });
    await page.goto("/next/settings/hub");
    await expect(page.getByTestId("registry-rule-row")).toHaveCount(1);
    for (const [cardId, ids] of [
      [
        "card-registry",
        [
          "registry-status-pill",
          "registry-heartbeat-btn",
          "registry-heartbeat-line",
          "registry-advertised-scopes",
        ],
      ],
      ["card-hub-config", ["input-hub-purpose", "store-hub-token-btn"]],
      ["card-hub-scopes", ["registry-rule-row", "add-hub-rule-btn"]],
    ]) {
      const card = await page.getByTestId(cardId).boundingBox();
      for (const id of ids) {
        const b = await page.getByTestId(id).first().boundingBox();
        expect(b.x, id).toBeGreaterThanOrEqual(card.x - 0.5);
        expect(b.x + b.width, id).toBeLessThanOrEqual(
          card.x + card.width + 0.5,
        );
      }
    }
    // A scope row's role sits on the right of its row.
    const row = await page.getByTestId("registry-rule-row").boundingBox();
    const remove = await page.getByTestId("remove-hub-rule-0").boundingBox();
    expect(remove.x + remove.width).toBeGreaterThan(row.x + row.width - 24);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  } finally {
    await restoreHub(request, before);
  }
});
