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

// Regressions from the 2026-11-14 Settings walkthrough
// (docs/test-runs/2026-11-14-user-system-settings.md), each driven
// through the visible UI over the worker's real `grain demo`.

test("old Admin, System and Config Repo Browser links land on their Settings page", async ({
  page,
}) => {
  const cases = [
    ["/next/admin", /\/next\/settings\/general$/, "General"],
    ["/next/admin/workflow", /\/next\/settings\/tasks$/, "Tasks"],
    ["/next/admin/executors", /\/next\/settings\/hostds$/, "Hosts"],
    ["/next/system/executors", /\/next\/settings\/hostds$/, "Hosts"],
    [
      "/next/admin/integrations",
      /\/next\/settings\/capabilities#credentials$/,
      "Capabilities",
    ],
    [
      "/next/system/integrations",
      /\/next\/settings\/capabilities#credentials$/,
      "Capabilities",
    ],
    [
      "/next/settings/credentials",
      /\/next\/settings\/capabilities#credentials$/,
      "Capabilities",
    ],
    [
      "/next/settings/secrets",
      /\/next\/settings\/capabilities#other-secrets$/,
      "Capabilities",
    ],
    ["/next/admin/upgrade", /\/next\/settings\/upgrade$/, "Upgrade"],
    ["/next/admin/agents", /\/next\/settings\/agents$/, "Agents"],
    ["/next/admin/Models", /\/next\/settings\/models$/, "Models"],
    ["/next/admin/state", /\/next\/settings\/config-repo$/, "Config repo"],
    ["/next/admin/no-such-tab", /\/next\/settings\/general$/, "General"],
    [
      "/next/settings/config-repo/tables/grain_config.json",
      /\/next\/settings\/config-repo\?path=tables%2Fgrain_config\.json$/,
      "Config repo",
    ],
  ];
  for (const [from, to, heading] of cases) {
    await page.goto(from);
    await expect(page, from).toHaveURL(to);
    await expect(page.getByTestId("settings-section-heading")).toHaveText(
      heading,
    );
  }
});

test("a #card link scrolls that card into view", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 700 });
  await page.goto("/next/settings/access#external-agents");
  await expect(page.getByTestId("card-external-agent-tokens")).toBeInViewport();
});

test("/ on Settings focuses Filter settings, and a field label finds its section", async ({
  page,
}) => {
  await page.goto("/next/settings/general");
  await expect(page.getByTestId("settings-section-heading")).toHaveText(
    "General",
  );
  await page.getByTestId("settings-section-heading").click();
  await page.keyboard.press("/");
  await expect(page.getByTestId("settings-filter-input")).toBeFocused();
  await page.keyboard.type("max agent turns");
  await expect(page.locator('[data-testid^="settings-nav-"]')).toHaveText([
    "Agents",
  ]);
  await page.keyboard.press("Escape");

  // Off Settings, "/" is the header's Filter this view again.
  await page.goto("/next/inbox");
  await expect(page.getByTestId("view-filter")).toBeVisible();
  await page.locator("body").press("/");
  await expect(page.getByTestId("view-filter")).toBeFocused();
});

test("CLI shows the workstation installer, token sign-in and skill lines", async ({
  page,
  demoServer,
}) => {
  await page.goto("/next/settings/cli");
  const snippet = page.getByTestId("cli-install-snippet");
  await expect(snippet).toContainText(
    `curl -fsSL ${demoServer}/cli/install.sh | sh`,
  );
  await expect(snippet).not.toContainText("/api/hostd/install.sh");
  await expect(snippet).toContainText(
    "--issue-token --scope-set grain/external-dev --expires 30d",
  );
  await expect(snippet).toContainText("grain skill install");
});

test("Audit prints times in the display time zone", async ({ page }) => {
  await page.goto("/next/settings/audit");
  const first = page.getByTestId("audit-row").first().locator("td").first();
  await expect(first).toBeVisible();
  await expect(first).not.toHaveText(/T\d\d:\d\d:\d\d/);
  expect(await first.locator("time").getAttribute("datetime")).toMatch(
    /^\d{4}-\d\d-\d\dT/,
  );
  await page.evaluate(() =>
    localStorage.setItem("grain.timeZone", "America/New_York"),
  );
  try {
    await page.reload();
    await expect(
      page.getByTestId("audit-row").first().locator("td").first(),
    ).toContainText(/E[SD]T/);
  } finally {
    await page.evaluate(() => localStorage.removeItem("grain.timeZone"));
  }
});

test("an out-of-range count is refused by its label before saving and clears when fixed", async ({
  page,
}) => {
  await page.goto("/next/settings/agents");
  const turns = page.getByTestId("input-max-agent-turns");
  await expect(turns).toBeEnabled();
  const before = await turns.inputValue();
  await turns.fill("-3");
  await expect(page.getByTestId("error-ag-turns-err")).toHaveText(
    "Max agent turns must be a whole number of at least 0.",
  );
  await expect(turns).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByTestId("settings-save-btn")).toBeDisabled();
  await turns.fill(before || "0");
  await expect(page.getByTestId("error-ag-turns-err")).toHaveCount(0);
  // Back at the saved value: nothing is left to save.
  await expect(page.getByTestId("settings-save-btn")).toHaveCount(0);
});

test("Access grants with a role the deployment has, and every removal asks first", async ({
  page,
}) => {
  await page.goto("/next/settings/access");
  const entry = "user:walk-bob@example.com";
  const remove = page.getByTestId(`remove-principal-${entry}`);
  const dialog = page.getByRole("dialog");
  await expect(
    page.getByTestId("remove-principal-user:alice@example.com"),
  ).toBeVisible();
  await page.getByTestId("input-principal-entry").fill(entry);
  await page.getByTestId("add-principal-btn").click();
  await expect(remove).toBeVisible();
  await expect(page.getByTestId("settings-error-banner")).toHaveCount(0);
  try {
    // Your own entry: the question says it is yours.
    await page.getByTestId("remove-principal-user:alice@example.com").click();
    await expect(dialog).toContainText("your own");
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(
      page.getByTestId("remove-principal-user:alice@example.com"),
    ).toBeVisible();

    // Anyone else's: still asked, and Cancel keeps it.
    await remove.click();
    await expect(dialog).toContainText(`Remove ${entry}?`);
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(remove).toBeVisible();

    await remove.click();
    await dialog.getByRole("button", { name: "Remove" }).click();
    await expect(remove).toHaveCount(0);
    await page.reload();
    await expect(
      page.getByTestId("remove-principal-user:alice@example.com"),
    ).toBeVisible();
    await expect(remove).toHaveCount(0);
  } finally {
    await page.request.delete(
      `/api/access/principals/${encodeURIComponent(entry)}`,
    );
  }
});

test("a reload with unsaved settings asks first", async ({ page }) => {
  await page.goto("/next/settings/agents");
  const turns = page.getByTestId("input-max-agent-turns");
  await expect(turns).toBeEnabled();
  const before = await turns.inputValue();
  await turns.fill(String(Number(before || "0") + 1));
  let asked = "";
  page.once("dialog", async (d) => {
    asked = d.type();
    await d.dismiss();
  });
  await page.reload({ timeout: 3_000 }).catch(() => undefined);
  expect(asked).toBe("beforeunload");
  await page.getByRole("button", { name: "Discard" }).click();
});

test("Save memory saves the standing instructions too, and the next open reads them back", async ({
  page,
}) => {
  await page.goto("/next/settings/memory");
  const box = page.getByTestId("input-prompt-extension");
  await expect(box).toBeEnabled();
  const before = await box.inputValue();
  const edited = before ? `${before}\nWalkthrough note.` : "Walkthrough note.";
  try {
    await box.fill(edited);
    await page.getByTestId("save-memory-btn").click();
    await expect(page.getByTestId("settings-save-btn")).toHaveCount(0);
    await page.reload();
    await expect(page.getByTestId("input-prompt-extension")).toHaveValue(
      edited,
    );
  } finally {
    await page.request.post("/api/settings", {
      data: { promptExtension: before },
    });
  }
});

test("Preferences Customize rail works across tabs and Color theme fits on a 390px phone", async ({
  page,
  context,
}) => {
  await page.goto("/next/settings/preferences");
  await expect(page.getByTestId("pref-customize-nav-btn")).toBeVisible();

  const otherTab = await context.newPage();
  try {
    await otherTab.goto("/next/inbox");
    await expect(
      otherTab.locator('.nav-rail a[href="/next/telemetry"]'),
    ).toBeVisible();

    await page.getByTestId("pref-customize-nav-btn").click();
    const dialog = page.getByTestId("customize-nav-dialog");
    await expect(dialog).toBeVisible();
    await dialog.getByTestId("nav-row-telemetry").getByRole("switch").click();
    await dialog.getByRole("button", { name: /Save/i }).click();
    await expect(
      otherTab.locator('.nav-rail a[href="/next/telemetry"]'),
    ).toHaveCount(0);

    await page.getByTestId("pref-customize-nav-btn").click();
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: /Reset/i }).click();
    await dialog.getByRole("button", { name: /Save/i }).click();
    await expect(
      otherTab.locator('.nav-rail a[href="/next/telemetry"]'),
    ).toBeVisible();
  } finally {
    await otherTab.close();
  }

  // Phone layout: Color theme segmented buttons wrap inside the 390px viewport.
  await page.setViewportSize({ width: 390, height: 844 });
  const autoBox = await page.getByTestId("pref-theme-auto").boundingBox();
  expect(autoBox).not.toBeNull();
  expect(autoBox.x).toBeGreaterThanOrEqual(0);
  expect(autoBox.x + autoBox.width).toBeLessThanOrEqual(390);
});

test("CLI warns with an in-app Access link when external agents are off and offers Copy buttons", async ({
  page,
}) => {
  await page.goto("/next/settings/cli");
  await expect(page.getByTestId("cli-external-agent-api-off")).toBeVisible();
  await expect(page.getByTestId("copy-cli-install")).toBeVisible();
  await expect(page.getByTestId("copy-cli-login")).toBeVisible();
  await expect(page.getByTestId("copy-cli-skill")).toBeVisible();

  await page.getByTestId("cli-external-agent-api-link").click();
  await expect(page).toHaveURL(/\/next\/settings\/access#external-agents$/);
  await expect(page.getByTestId("card-external-agent-tokens")).toBeInViewport();
});

test("Audit stacks cleanly on a 390px phone and surfaces partial endpoint failures", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/next/settings/audit");
  await expect(page.getByTestId("audit-row").first()).toBeVisible();
  const tableBox = await page.locator(".s-table-stack").boundingBox();
  expect(tableBox).not.toBeNull();
  expect(tableBox.x + tableBox.width).toBeLessThanOrEqual(390);
  const docSw = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(docSw).toBeLessThanOrEqual(390);

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.route("**/api/deployment/audit-logs*", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "audit backend offline" }),
    }),
  );
  try {
    await page.getByTestId("refresh-audit-btn").click();
    await expect(page.getByTestId("audit-load-errors")).toContainText(
      "audit backend offline",
    );
    expect(await page.getByTestId("audit-row").count()).toBeGreaterThan(0);
  } finally {
    await page.unroute("**/api/deployment/audit-logs*");
  }
});

test("Unsaved deployment memory edits prompt before switching sections, leaving Settings, or reloading", async ({
  page,
}) => {
  await page.goto("/next/settings/memory");
  const mem = page.getByTestId("input-deployment-memory");
  await expect(mem).toBeEnabled();
  const before = await mem.inputValue();
  const edited = `${before}\nUnsaved memory guard test.`;
  await mem.fill(edited);

  let asked = "";
  page.once("dialog", async (d) => {
    asked = d.type();
    await d.dismiss();
  });
  await page.getByTestId("settings-nav-general").click();
  expect(asked).toBe("confirm");
  await expect(page).toHaveURL(/\/next\/settings\/memory$/);

  asked = "";
  page.once("dialog", async (d) => {
    asked = d.type();
    await d.dismiss();
  });
  await page.locator('a[href="/next/inbox"]').first().click();
  expect(asked).toBe("confirm");
  await expect(page).toHaveURL(/\/next\/settings\/memory$/);

  asked = "";
  page.once("dialog", async (d) => {
    asked = d.type();
    await d.dismiss();
  });
  await page.reload({ timeout: 3_000 }).catch(() => undefined);
  expect(asked).toBe("beforeunload");

  try {
    await page.getByTestId("save-memory-btn").click();
    await page.getByTestId("settings-nav-general").click();
    await expect(page).toHaveURL(/\/next\/settings\/general$/);
  } finally {
    await page.request.put("/api/memory", { data: { text: before } });
  }
});

test("General disables Settings lock without a config repo, refusal banners link to Config repo, and Config repo skips files when unavailable", async ({
  page,
}) => {
  const apiErrors = [];
  const onRes = (res) => {
    if (res.status() >= 400 && res.url().includes("/api/state-repo")) {
      apiErrors.push(`${res.status()} ${new URL(res.url()).pathname}`);
    }
  };
  page.on("response", onRes);
  try {
    await page.goto("/next/settings/general");
    await expect(page.getByTestId("switch-settings-locked")).toBeDisabled();
    await expect(page.getByTestId("settings-lock-hint")).toContainText(
      "Unavailable without a config repository",
    );

    await page.goto("/next/settings/config-repo");
    await expect(page.getByTestId("config-files-unavailable")).toBeVisible();
    expect(apiErrors).toEqual([]);
  } finally {
    page.off("response", onRes);
  }

  // Simulated 409 config_managed_externally refusal links in-app to Config repo
  // and clears once a subsequent save succeeds.
  await page.goto("/next/settings/general");
  const envInput = page.getByTestId("input-environment-name");
  const beforeEnv = await envInput.inputValue();
  let failOnce = true;
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() === "POST" && failOnce) {
      failOnce = false;
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({
          code: "config_managed_externally",
          error: "settings are managed in the config repository",
          change: [
            {
              path: "tables/grain_config.json",
              op: "modify",
              patch: "--- a\n+++ b\n",
            },
          ],
        }),
      });
      return;
    }
    await route.continue();
  });
  try {
    await envInput.fill("Walk Refusal Test");
    await page.getByTestId("settings-save-btn").click();
    await expect(page.getByTestId("refusal-open-pr-link")).toBeVisible();
    await page.getByTestId("refusal-open-pr-link").click();
    await expect(page).toHaveURL(/\/next\/settings\/config-repo$/);
    await expect(
      page.getByTestId("staged-config-changes-banner"),
    ).toBeVisible();

    await page.getByTestId("settings-nav-general").click();
    await page.getByTestId("settings-save-btn").click();
    await expect(page.getByTestId("settings-error-banner")).toHaveCount(0);
    await page.getByTestId("settings-nav-config-repo").click();
    await expect(page.getByTestId("staged-config-changes-banner")).toHaveCount(
      0,
    );
  } finally {
    await page.unroute("**/api/settings");
    await page.request.post("/api/settings", {
      data: { environmentName: beforeEnv },
    });
  }
});

test("external config repo locks Models, Engine, and Hub while leaving credentials testable and editable", async ({
  page,
}) => {
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const json = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...json,
        configRepoOnly: true,
        externalConfigRepo: "https://github.com/example/walk-config",
      },
    });
  });
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    const res = await route.fetch();
    const json = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...json,
        configRepoOnly: true,
        externalConfigRepo: "https://github.com/example/walk-config",
      },
    });
  });
  try {
    for (const sec of ["models", "engine", "registry"]) {
      await page.goto(`/next/settings/${sec}`);
      await expect(
        page.getByTestId("settings-config-repo-banner"),
      ).toBeVisible();
      await expect(
        page.getByTestId("open-in-config-repo-link"),
      ).toHaveAttribute(
        "href",
        /\/settings\/config-repo\?path=tables%2Fgrain_config\.json$/,
      );
    }

    await page.goto("/next/settings/models");
    await expect(page.getByTestId("edit-backend-gemini-pro")).toBeDisabled();
    await expect(page.getByTestId("remove-backend-gemini-pro")).toBeDisabled();
    await expect(page.getByTestId("input-new-backend-id")).toBeDisabled();
    await expect(page.getByTestId("save-token-budget")).toBeDisabled();
    await expect(page.getByTestId("toggle-cred-gemini-pro")).toBeEnabled();
    await expect(page.getByTestId("test-backend-gemini-pro")).toBeEnabled();
  } finally {
    await page.unroute("**/api/config");
    await page.unroute("**/api/settings");
  }
});

test("viewer role sees no bogus banner on Preferences/Audit, access:write on Access, and disabled Models edit controls", async ({
  page,
}) => {
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const json = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...json,
        access: {
          role: "grain/viewer",
          full: false,
          scopes: ["settings:read", "access:read", "audit:read"],
        },
      },
    });
  });
  try {
    for (const sec of ["preferences", "audit"]) {
      await page.goto(`/next/settings/${sec}`);
      await expect(page.getByTestId("settings-section-heading")).toBeVisible();
      await expect(page.getByTestId("settings-role-readonly")).toHaveCount(0);
    }
    await page.goto("/next/settings/access");
    await expect(page.getByTestId("settings-role-readonly")).toContainText(
      "access:write",
    );
    await page.goto("/next/settings/models");
    await expect(page.getByTestId("edit-backend-gemini-pro")).toBeDisabled();
    await expect(page.getByTestId("toggle-cred-gemini-pro")).toBeDisabled();
    await expect(page.getByTestId("input-new-backend-id")).toBeDisabled();
    await expect(page.getByTestId("save-token-budget")).toBeDisabled();
  } finally {
    await page.unroute("**/api/config");
  }
});
