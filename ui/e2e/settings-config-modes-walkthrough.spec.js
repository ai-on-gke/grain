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

// Regressions from walking Settings in grain's three config-repo modes
// (docs/test-runs/2026-11-02-settings-config-modes.md):
//
//   - with an external config repo, Settings → Access drew Log out
//     disabled inside a read-only fieldset, under a banner saying to edit
//     tables/grain_config.json -- sign-in is outside the config repo
//     (design/design/config-repo-settings.design.md §1 invariant 7);
//   - in a read-only card a MUI Select stayed focusable and opened from
//     the keyboard, and picking a value sent a write the API refuses.
import { expect, test } from "./fixtures.js";

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };
const EXTERNAL = "https://github.com/acme/grain-config";

// withConfig answers GET /api/settings and /api/config with `extra`
// folded in, so the page draws the mode `extra` names.
//
// A request still in flight when the test navigates away is cancelled
// with its page, and the response it was reading is disposed under it
// ("Response has been disposed"). Nobody is waiting for that answer any
// more, so the handler lets it go rather than failing the test.
async function withConfig(page, extra) {
  for (const glob of ["**/api/settings", "**/api/config"]) {
    await page.route(glob, async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      let response, body;
      try {
        response = await route.fetch();
        body = await response.json();
      } catch (err) {
        if (/disposed|closed|cancel/i.test(String(err))) return;
        throw err;
      }
      await route
        .fulfill({ response, json: { ...body, ...extra } })
        .catch((err) => {
          if (!/disposed|closed|cancel|handled/i.test(String(err))) throw err;
        });
    });
  }
}

// writesTo records every write the page sends to /api/settings, and
// answers it without touching the shared demo server.
async function writesTo(page) {
  const writes = [];
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() === "GET") return route.fallback();
    writes.push(route.request().postData());
    await route.fulfill({ status: 409, json: { error: "refused" } });
  });
  return writes;
}

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`Access keeps Log out usable under an external config repo on a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await withConfig(page, {
      externalConfigRepo: EXTERNAL,
      externalConfigBranch: "main",
      configRepoOnly: true,
    });
    await page.goto("/settings/access");
    const section = page.getByTestId("settings-section");
    const card = section.getByRole("region", { name: "This browser" });
    await expect(card).toBeVisible();
    await expect(page.getByTestId("external-config-repo-banner")).toHaveCount(
      0,
    );
    await expect(section.locator("[data-settings-readonly]")).toHaveCount(0);
    const logout = card.getByRole("button", { name: "Log out" });
    await expect(logout).toBeEnabled();

    // The button sits inside its card, on screen.
    const c = await card.boundingBox();
    const b = await logout.boundingBox();
    expect(b.x).toBeGreaterThanOrEqual(c.x);
    expect(b.x + b.width).toBeLessThanOrEqual(c.x + c.width + 0.5);
    expect(b.y).toBeGreaterThanOrEqual(c.y);
    expect(b.y + b.height).toBeLessThanOrEqual(c.y + c.height + 0.5);
    expect(b.x + b.width).toBeLessThanOrEqual(viewport.width);

    // A section that holds config-repo settings still says where they are.
    await page.goto("/settings/general");
    await expect(page.getByTestId("external-config-repo-banner")).toBeVisible();
  });
}

for (const [section, label] of [
  ["general", "Upgrade automatically"],
  // Registry drew its locked cards in a bare disabled fieldset, which a
  // MUI Select does not obey (2026-11-07 settings walkthrough).
  ["registry", "Credential source"],
  ["registry", "Host kind"],
  ["registry", "Rule"],
]) {
  test(`a locked ${section} card's ${label} select is out of reach of the keyboard`, async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await withConfig(page, { configRepoOnly: true, settingsLocked: true });
    const writes = await writesTo(page);
    await page.goto(`/settings/${section}`);
    const select = page
      .getByTestId("settings-section")
      .getByRole("combobox", { name: label });
    await expect(select).toBeVisible();
    await expect(select).toHaveAttribute("aria-disabled", "true");
    await expect(select).toHaveAttribute("tabindex", "-1");

    await select.focus();
    for (const key of ["ArrowDown", "Enter", " "]) {
      await page.keyboard.press(key);
    }
    await expect(page.getByRole("listbox")).toHaveCount(0);
    expect(writes).toEqual([]);
  });
}

test("an unlocked card's select still opens from the keyboard", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  await withConfig(page, { configRepoOnly: false, settingsLocked: false });
  await page.goto("/settings/general");
  const select = page
    .getByTestId("settings-section")
    .getByRole("combobox", { name: "Upgrade automatically" });
  await expect(select).toBeVisible();
  await expect(select).not.toHaveAttribute("aria-disabled", "true");
  await select.focus();
  await page.keyboard.press("ArrowDown");
  await expect(page.getByRole("listbox")).toBeVisible();
  await page.keyboard.press("Escape");
});

// A save refused because the setting lives in the config repository
// (409 config_managed_externally, as a real daemon with settings locked
// answers a page opened before the lock) handed showError only its
// message: no refusal dialog, no Create Config PR, and the whole patch in
// a toast (2026-11-07 settings walkthrough). The body is the one that
// daemon sent, so the shared demo store is not locked under other specs.
test("a setting refused for the config repo opens the refusal dialog", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  const patch =
    "diff --git a/tables/grain_config.json b/tables/grain_config.json\n" +
    "--- a/tables/grain_config.json\n+++ b/tables/grain_config.json\n" +
    '@@ -1 +1 @@\n-  "autoMergeByDefault": true\n+  "autoMergeByDefault": false\n';
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() === "GET") return route.fallback();
    await route.fulfill({
      status: 409,
      json: {
        error:
          "this setting is not written directly: configuration is managed in this deployment's config repository grain/config (main). Commit this change to tables/grain_config.json instead:\n\n" +
          patch,
        code: "config_managed_externally",
        repo: "grain/config",
        remote: "",
        branch: "main",
        internal: true,
        url: "/repos/grain/config",
        path: ["tables/grain_config.json"],
        change: [{ path: "tables/grain_config.json", patch }],
        docs: "https://github.com/ai-on-gke/grain/blob/main/docs/external-config-repo.md",
      },
    });
  });
  await page.goto("/settings/tasks");
  const box = page.locator("input[name=autoMergeByDefault]");
  await expect(box).toBeVisible();
  const before = await box.isChecked();
  await box.click();

  const dialog = page.getByTestId("config-refusal-dialog");
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("tables/grain_config.json");
  await expect(
    dialog.getByRole("button", { name: "Create Config PR" }),
  ).toBeVisible();
  await expect(page.getByText("diff --git", { exact: false })).toHaveCount(
    await dialog.getByText("diff --git", { exact: false }).count(),
  );
  await expect(page.getByTestId("settings-status")).toHaveText(
    "Couldn't save Auto-merge new tasks by default",
  );
  await expect(
    page.getByRole("button", { name: "Retry", exact: true }),
  ).toHaveCount(0);
  await expect(box).toBeChecked({ checked: before });
});

// Deployment credentials (Settings → Capabilities, formerly Legacy
// capabilities) wrapped its GitHub and GCP config fields in a bare
// <Box component="fieldset" disabled> instead of <ReadOnlyFieldset>, leaving
// githubInsecureHttp without [data-settings-readonly] dimming when locked
// (docs/test-runs/2026-11-08-user-system-settings.md).
test("a locked Deployment credentials card dims config fields via ReadOnlyFieldset while keeping secrets editable", async ({
  page,
}) => {
  await withConfig(page, { configRepoOnly: true, settingsLocked: true });
  for (const viewport of [DESKTOP, PHONE]) {
    await page.setViewportSize(viewport);
    await page.goto("/settings/credentials");
    // The retired address opens Capabilities at the card it stands for.
    await expect(page).toHaveURL(/\/settings\/capabilities#credentials$/);

    const card = page.getByTestId("deployment-credentials");
    const box = card.locator("input[name=githubInsecureHttp]");
    await expect(box).toBeDisabled();
    expect(
      await box.evaluate((el) =>
        Boolean(el.closest("[data-settings-readonly]")),
      ),
    ).toBe(true);
    expect(
      await box.evaluate(
        (el) => getComputedStyle(el.closest(".MuiCheckbox-root")).opacity,
      ),
    ).toBe("0.5");

    const otherSecrets = card.locator("#other-secrets");
    await expect(otherSecrets).toBeVisible();
    expect(
      await otherSecrets.evaluate((el) =>
        Boolean(el.closest("[data-settings-readonly]")),
      ),
    ).toBe(false);

    const cardRect = await card.boundingBox();
    expect(cardRect.x).toBeGreaterThanOrEqual(0);
    expect(cardRect.x + cardRect.width).toBeLessThanOrEqual(viewport.width);
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    ).toBe(0);
  }
});
