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

// Settings → Models (ui-ng): the default model and its ordered fallback
// models are chosen with g-model-picker from what the connections
// advertise, a stored model no connection advertises stays visible and
// flagged, and the edited list saves as the deployment's default model
// list. Rendered at a desktop and a phone width, light and dark, with axe
// holding the card to WCAG 2.1 AA.

import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

const VIEWPORTS = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];
const THEMES = ["light", "dark"];
const WCAG_AA = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"];

async function seed(request) {
  const put = await request.put("/api/deployment/agent-backends/claude-team", {
    data: {
      id: "claude-team",
      name: "Claude team",
      framework: "claude",
      models: ["claude-opus-5", "claude-sonnet-5"],
    },
  });
  expect(put.ok()).toBe(true);
  const dep = await request.put("/api/deployment", {
    data: {
      defaultModels: ["gemini-2.5-pro", "claude-opus-5", "retired-model-1"],
    },
  });
  expect(dep.ok()).toBe(true);
}

async function axeClean(page) {
  const result = await new AxeBuilder({ page })
    .include('[data-testid="card-model-defaults"]')
    .withTags(WCAG_AA)
    .analyze();
  expect(result.violations.map((v) => `${v.id}: ${v.nodes.length}`)).toEqual(
    [],
  );
}

test.describe("Settings → Models default and fallback models", () => {
  test.beforeEach(async ({ request }) => {
    await seed(request);
  });

  for (const vp of VIEWPORTS) {
    for (const theme of THEMES) {
      test(`renders the picker and fallback list at ${vp.name} in ${theme}, WCAG AA clean`, async ({
        page,
      }) => {
        test.setTimeout(90_000);
        await page.setViewportSize({ width: vp.width, height: vp.height });
        await page.addInitScript(
          (t) => localStorage.setItem("grain.themeMode", t),
          theme,
        );
        await page.goto("/next/settings/models");
        const card = page.getByTestId("card-model-defaults");
        await expect(card).toBeVisible();
        await expect(page.locator("html")).toHaveAttribute("data-theme", theme);

        // No free-text model field, no native select.
        await expect(card.getByTestId("input-default-model")).toHaveCount(0);
        expect(await card.locator("select").count()).toBe(0);
        await expect(card.getByTestId("picker-default-model")).toContainText(
          "gemini-2.5-pro",
        );
        await expect(card.getByTestId("fallback-row-0")).toContainText(
          "claude-opus-5",
        );
        await expect(card.getByTestId("fallback-row-1")).toContainText(
          "not advertised by any connection",
        );
        // Every row stays inside the card at this width.
        const cardBox = await card.boundingBox();
        for (const id of ["fallback-row-0", "fallback-row-1"]) {
          const box = await card.getByTestId(id).boundingBox();
          expect(box.x).toBeGreaterThanOrEqual(cardBox.x);
          expect(box.x + box.width).toBeLessThanOrEqual(
            cardBox.x + cardBox.width + 1,
          );
        }
        await axeClean(page);

        // The open picker: grouped by connection, search focused, and
        // models already in the list not offered.
        await card.getByTestId("picker-add-fallback").click();
        const panel = page.getByTestId("picker-add-fallback-panel");
        await expect(panel).toBeVisible();
        await expect(
          page.getByTestId("picker-add-fallback-search"),
        ).toBeFocused();
        const offered = await panel
          .getByTestId("picker-add-fallback-opt")
          .evaluateAll((els) => els.map((e) => e.dataset.model));
        expect(offered).toEqual(["gemini-2.5-flash", "claude-sonnet-5"]);
        await expect(panel.locator(".mp-group-name")).toHaveText([
          "Gemini 2.5 Pro (gemini-pro)",
          "Claude team (claude-team)",
        ]);
        await axeClean(page);
        await page.keyboard.press("Escape");
        await expect(panel).toBeHidden();
        await expect(card.getByTestId("picker-add-fallback")).toBeFocused();
      });
    }
  }

  test("adds, reorders and removes fallbacks and saves the ordered list", async ({
    page,
    request,
  }) => {
    test.setTimeout(90_000);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/next/settings/models");
    const card = page.getByTestId("card-model-defaults");
    await expect(card).toBeVisible();

    // Add by keyboard: search, Enter.
    await card.getByTestId("picker-add-fallback").click();
    await page.getByTestId("picker-add-fallback-search").fill("sonnet");
    await page.keyboard.press("Enter");
    await expect(card.getByTestId("fallback-row-2")).toContainText(
      "claude-sonnet-5",
    );
    // Move it to the top of the fallbacks, drop the retired one.
    // Wait for each move to render before the next click: the row test ids
    // are re-bound on change detection, so a second click fired before the
    // first move lands can hit the button of the row that used to sit there.
    await card.getByTestId("fallback-up-2").click();
    await expect(card.getByTestId("fallback-row-1")).toContainText(
      "claude-sonnet-5",
    );
    await card.getByTestId("fallback-up-1").click();
    await expect(card.getByTestId("fallback-row-0")).toContainText(
      "claude-sonnet-5",
    );
    await card.getByTestId("fallback-remove-2").click();
    await expect(card.locator('[data-testid^="fallback-row-"]')).toHaveCount(2);

    await page.getByTestId("settings-save-btn").click();
    await expect(page.getByTestId("settings-save-bar")).toBeHidden();
    const dep = await (await request.get("/api/deployment")).json();
    expect(dep.defaultModels).toEqual([
      "gemini-2.5-pro",
      "claude-sonnet-5",
      "claude-opus-5",
    ]);

    // Promoting a fallback to default takes it out of the fallbacks.
    await card.getByTestId("picker-default-model").click();
    await page
      .getByTestId("picker-default-model-opt")
      .and(page.locator('[data-model="claude-opus-5"]'))
      .click();
    await expect(card.getByTestId("picker-default-model")).toContainText(
      "claude-opus-5",
    );
    await expect(card.locator('[data-testid^="fallback-row-"]')).toHaveCount(1);
    await page.getByTestId("settings-save-btn").click();
    await expect(page.getByTestId("settings-save-bar")).toBeHidden();
    const after = await (await request.get("/api/deployment")).json();
    expect(after.defaultModels).toEqual(["claude-opus-5", "claude-sonnet-5"]);
  });
});

test("Settings Models - Discovered models, no framework keys card, and fallback models", async ({
  page,
}) => {
  await page.goto("/next/settings/models");

  // Verify "Refresh models" button for a connection
  const refreshBtn = page.getByTestId("refresh-backend-agy-ambient");
  if ((await refreshBtn.count()) > 0) {
    await refreshBtn.click();
    await expect(page.getByTestId("card-model-backends")).toContainText(
      "Refreshed",
    );
  }

  // The standalone framework keys card is retired: credentials live on
  // each connection.
  await expect(page.getByTestId("card-agent-keys")).toHaveCount(0);

  // Fallback models (done in existing tests, just verifying structure)
  const addFallbackBtn = page.getByTestId("picker-add-fallback");
  await expect(addFallbackBtn).toBeVisible();
});

// Refresh asks a provider now (POST .../discover, connections:write): a
// person without that scope is offered no Refresh at all, neither on the
// connection's row nor inside the model picker, while one who holds it
// sees the row's Refresh (docs/test-runs/2026-11-14-settings-models.md).
test("offers Refresh models only to a person who may ask providers", async ({
  page,
  request,
}) => {
  const put = await request.put(
    "/api/deployment/agent-backends/claude-discover",
    { data: { id: "claude-discover", framework: "claude" } },
  );
  expect(put.ok()).toBe(true);
  const viewerConfig = (url) => url.pathname === "/api/config";
  try {
    await page.goto("/next/settings/models");
    await expect(
      page.getByTestId("refresh-backend-claude-discover"),
    ).toBeVisible();

    await page.route(viewerConfig, async (route) => {
      const response = await route.fetch();
      const json = {
        ...(await response.json()),
        access: {
          role: "grain/viewer",
          roles: ["grain/viewer"],
          full: false,
          scopes: ["settings:read", "connections:read", "deployment:read"],
        },
      };
      await route.fulfill({ response, json });
    });
    await page.reload();
    await expect(page.getByTestId("card-model-defaults")).toBeVisible();
    await expect(page.getByTestId("card-model-backends")).toContainText(
      "claude-discover",
    );
    await expect(
      page.getByTestId("refresh-backend-claude-discover"),
    ).toHaveCount(0);
    // The picker is read-only for them, and nothing on the page offers to
    // ask a provider.
    await expect(page.getByTestId("picker-default-model")).toBeDisabled();
    await expect(page.getByRole("button", { name: /refresh/i })).toHaveCount(0);
  } finally {
    await page.unroute(viewerConfig);
    await request.delete("/api/deployment/agent-backends/claude-discover");
  }
});

// Clearing a connection's stored secret cannot be undone, so it asks
// first, as removing the connection does: Cancel sends nothing, and only
// Accept sends the DELETE. (`grain demo` keeps no secret store, so the
// DELETE is counted here rather than sent.)
test("clears a connection's stored secret only after confirming", async ({
  page,
  request,
}) => {
  const id = "claude-clear-walk";
  expect(
    (
      await request.put(`/api/deployment/agent-backends/${id}`, {
        data: { id, framework: "claude", models: ["claude-opus-5"] },
      })
    ).ok(),
  ).toBe(true);
  const deletes = [];
  const credential = `**/api/agent-backends/${id}/credential`;
  await page.route(credential, async (route) => {
    if (route.request().method() !== "DELETE") return route.fallback();
    deletes.push(route.request().url());
    await route.fulfill({ json: { set: false } });
  });
  try {
    await page.goto("/next/settings/connections");
    await page.getByTestId(`toggle-cred-${id}`).click();
    await page.getByTestId(`clear-cred-${id}`).click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toContainText(`Clear the credential for ${id}?`);
    await page.getByTestId("confirm-cancel").click();
    await expect(dialog).toHaveCount(0);
    expect(deletes).toEqual([]);

    await page.getByTestId(`clear-cred-${id}`).click();
    await page.getByTestId("confirm-accept").click();
    await expect(dialog).toHaveCount(0);
    await expect.poll(() => deletes.length).toBe(1);
  } finally {
    await page.unroute(credential);
    await request.delete(`/api/deployment/agent-backends/${id}`);
  }
});

// An old /settings/connections link opens Models -- where the model
// connections are -- at a phone width too, not the bare section list.
test("opens Models for an old settings/connections link on a phone", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/next/settings/connections");
  await expect(page.getByTestId("card-model-backends")).toBeVisible();
  await expect(page.getByTestId("settings-mobile-back")).toBeVisible();
});

// A connection whose provider has never answered carries Go's zero time
// as refreshedAt; it is shown with its error and no refresh time, not as
// refreshed on "Jan 1, 00:00 UTC".
test("shows no refresh time for a provider that never answered", async ({
  page,
}) => {
  const settings = (url) => url.pathname === "/api/settings";
  await page.route(settings, async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const response = await route.fetch();
    const json = await response.json();
    json.agentBackends = [
      ...(json.agentBackends || []),
      {
        id: "claude-never",
        framework: "claude",
        discovered: {
          refreshedAt: "0001-01-01T00:00:00Z",
          error: "no Claude Code OAuth token is configured for this model",
        },
      },
    ];
    await route.fulfill({ response, json });
  });
  try {
    await page.goto("/next/settings/models");
    const row = page
      .getByTestId("card-model-backends")
      .locator("tr", { hasText: "claude-never" });
    await expect(row).toContainText("0 models discovered");
    await expect(row).toContainText("no Claude Code OAuth token");
    await expect(row.locator(".s-disc-details summary")).not.toContainText(
      "UTC",
    );
  } finally {
    await page.unroute(settings);
  }
});
