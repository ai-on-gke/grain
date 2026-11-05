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
  ["tasks", "Default legacy capabilities"],
  ["general", "Upgrade automatically"],
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
  await page.goto("/settings/tasks");
  const select = page
    .getByTestId("settings-section")
    .getByRole("combobox", { name: "Default legacy capabilities" });
  await expect(select).toBeVisible();
  await expect(select).not.toHaveAttribute("aria-disabled", "true");
  await select.focus();
  await page.keyboard.press("ArrowDown");
  await expect(page.getByRole("listbox")).toBeVisible();
  await page.keyboard.press("Escape");
});
