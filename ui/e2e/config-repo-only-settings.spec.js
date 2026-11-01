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

import { expect, test } from "./fixtures.js";

// docs/design/config-repo-settings.md §2.1, §2.3, §7.4: when the API
// sends a settings write to the config repo (GET /api/config's
// configRepoOnly), Settings keeps Models and Sandboxes editable and draws
// the departing sections -- and a repository's page -- read-only under a
// notice linking each file into the Config Repo Browser. `grain demo` has
// no config repo, so the flag is set on its /api/config answer here, and
// on GET /api/settings, which carries the same configRepoOnly for the
// row it answers with (the Settings page draws on that once loaded).
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

async function configRepoOnly(page) {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, configRepoOnly: true } });
  });
  await page.route("**/api/config", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({
      response,
      json: { ...body, configRepoOnly: true },
    });
  });
}

// inside says box b lies within box a, horizontally.
function inside(a, b) {
  return b.x >= a.x - 0.5 && b.x + b.width <= a.x + a.width + 0.5;
}

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`a config-repo-only section is read-only with its links inside the notice on a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await configRepoOnly(page);
    await page.goto("/settings/agents");

    const notice = page.getByTestId("config-managed-notice");
    await expect(notice).toBeVisible();
    // Console sessions are grain_config.console_sessions, so the one
    // file is tables/grain_config.json.
    const links = notice.getByTestId("config-repo-link");
    await expect(links).toHaveCount(1);
    await expect(links.first()).toHaveAttribute(
      "href",
      "/config?path=tables%2Fgrain_config.json",
    );
    await expect(
      page.getByLabel("Standing instructions for every run"),
    ).toBeDisabled();

    // The notice sits in the section's column, above its first card, and
    // each link wraps inside it rather than running off a phone.
    const noticeBox = await notice.boundingBox();
    const content = await page.getByTestId("settings-section").boundingBox();
    expect(inside(content, noticeBox)).toBe(true);
    for (const link of await links.all()) {
      expect(inside(noticeBox, await link.boundingBox())).toBe(true);
    }
    const firstCard = await page.locator("[data-settings-card]").first();
    expect((await firstCard.boundingBox()).y).toBeGreaterThan(
      noticeBox.y + noticeBox.height - 1,
    );
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);

    // Models stays editable, with no notice.
    await page.goto("/settings/models");
    await expect(
      page.getByRole("heading", { name: "Models", level: 1 }),
    ).toBeVisible();
    await expect(page.getByTestId("config-managed-notice")).toHaveCount(0);
  });

  // A bootstrap field stays writable under the lock and can be left
  // pending a restart, so Health links it to its card -- here Sandboxes →
  // Kubernetes, which is taller than either screen. Centring such a card
  // scrolled its heading and the field Health named (focused without
  // scrolling) off the top of the page; it is aligned to its top instead.
  test(`a Health link to a card taller than the screen lands on its heading and named field on a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await configRepoOnly(page);
    await page.route("**/api/settings", async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const body = await response.json();
      await route.fulfill({
        response,
        json: {
          ...body,
          configRepoOnly: true,
          pendingRestart: ["k8sNamespace"],
        },
      });
    });
    await page.goto("/system/health");

    const link = page
      .getByTestId("health-pending-restart")
      .getByRole("link", { name: "Kubernetes namespace" });
    await expect(link).toHaveAttribute(
      "href",
      "/settings/sandboxes#k8sNamespace",
    );
    await link.click();
    await expect(page).toHaveURL(/\/settings\/sandboxes#k8sNamespace$/);
    await expect(page.getByTestId("config-managed-notice")).toHaveCount(0);

    const card = page.locator("#kubernetes");
    const field = card.getByLabel("Kubernetes namespace");
    await expect(field).toBeFocused();
    await expect(field).toBeEnabled();
    expect((await card.boundingBox()).height).toBeGreaterThan(viewport.height);
    await expect(field).toBeInViewport();
    await expect
      .poll(async () => (await card.boundingBox()).y)
      .toBeGreaterThanOrEqual(0);
    // Nothing fixed covers the card's top edge.
    const top = await card.boundingBox();
    expect(
      await page.evaluate(
        ([x, y]) =>
          document
            .getElementById("kubernetes")
            .contains(document.elementFromPoint(x, y)),
        [top.x + top.width / 2, top.y + 4],
      ),
    ).toBe(true);
  });
}

// Git connections are grain_config.git_connections / git_credential and a
// repository's choice repo_config.git_credential (schema 22), so the Git
// notice links those two tables' files, not tables that no longer exist.
test("the Git section links the files that hold its connections", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  await configRepoOnly(page);
  await page.goto("/settings/git");

  const links = page
    .getByTestId("config-managed-notice")
    .getByTestId("config-repo-link");
  await expect(links).toHaveCount(2);
  await expect(links.nth(0)).toHaveAttribute(
    "href",
    "/config?path=tables%2Fgrain_config.json",
  );
  await expect(links.nth(1)).toHaveAttribute(
    "href",
    "/config?path=tables%2Frepo_config.json",
  );
});

test("a repository's settings are read-only, its default grants chips, its branches still addable", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  await configRepoOnly(page);
  await page.goto("/repos/acme/widgets/settings/agents");

  const notice = page.getByTestId("config-managed-notice");
  await expect(notice).toBeVisible();
  await expect(notice.getByTestId("config-repo-link")).toHaveCount(2);
  const grants = page.getByTestId("repo-default-grants");
  await expect(grants).toBeVisible();
  await expect(grants.getByTestId("config-repo-link")).toHaveAttribute(
    "href",
    "/config?path=settings%2Fcapabilities.yml",
  );
  await expect(grants.locator("input")).toHaveCount(0);

  await page.getByRole("tab", { name: "Branches" }).click();
  await expect(page.getByRole("button", { name: "Add branch" })).toBeEnabled();
});

test("a View in config repo link opens Settings → Config repo with the file named", async ({
  page,
}) => {
  await configRepoOnly(page);
  await page.goto("/settings/capabilities");
  await page
    .getByTestId("config-managed-notice")
    .getByTestId("config-repo-link")
    .first()
    .click();
  await expect(
    page.getByRole("heading", { name: "Config repo", level: 1 }),
  ).toBeVisible();
  await expect(page).toHaveURL(
    /\/settings\/config-repo\/tables\/grain_config\.json$/,
  );
});

test("toggling Lock settings in UI in General locks and unlocks settings", async ({
  page,
}) => {
  try {
    await page.setViewportSize(DESKTOP);
    await page.goto("/settings/general");

    const envBox = page.getByLabel("Environment name");
    const lockToggle = page.getByRole("checkbox", {
      name: /Lock settings in UI \(require config repo PRs\)/i,
    });
    await expect(envBox).toBeEnabled();
    await expect(lockToggle).not.toBeChecked();
    await expect(page.getByTestId("config-managed-notice")).toHaveCount(0);

    const sectionBox = await page.getByTestId("settings-section").boundingBox();
    const lockCardBox = await page.locator("#settings-lock").boundingBox();
    expect(inside(sectionBox, lockCardBox)).toBe(true);

    // Enable settings lock
    await lockToggle.click();
    await expect(lockToggle).toBeChecked();
    await expect(envBox).toBeDisabled();
    await expect(page.getByTestId("config-managed-notice")).toBeVisible();
    await expect(lockToggle).toBeEnabled();

    // Disable settings lock again
    await lockToggle.click();
    await expect(lockToggle).not.toBeChecked();
    await expect(envBox).toBeEnabled();
    await expect(page.getByTestId("config-managed-notice")).toHaveCount(0);
  } finally {
    await page.request
      .put("/api/settings", { data: { settingsLocked: false } })
      .catch(() => {});
  }
});
