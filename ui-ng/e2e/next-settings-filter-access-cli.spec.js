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

// Regressions from the 2026-11-17 Settings walkthrough
// (docs/test-runs/2026-11-17-user-system-settings.md), each driven
// through the visible UI over the worker's real `grain demo`.

test("every heading and field label a section shows finds it in Filter settings", async ({
  page,
}) => {
  const sections = [
    "models",
    "tasks",
    "agents",
    "memory",
    "access",
    "audit",
    "general",
    "preferences",
    "cli",
    "config-repo",
  ];
  const filter = page.getByTestId("settings-filter-input");
  for (const id of sections) {
    await page.goto(`/next/settings/${id}`);
    const stage = page.getByTestId("settings-stage");
    await expect(stage.locator(".s-card-title").first()).toBeVisible();
    const labels = [
      ...new Set(
        (await stage.locator("h3, h4, .s-label").allInnerTexts())
          .map((l) => l.trim())
          .filter(Boolean),
      ),
    ];
    expect(labels.length, id).toBeGreaterThan(0);
    for (const label of labels) {
      await filter.fill(label);
      await expect(
        page.getByTestId(`settings-nav-${id}`),
        `"${label}" should list ${id}`,
      ).toBeVisible();
    }
    await filter.fill("");
  }
});

test("Grant access refuses a malformed entry by name and asks a service account for a reason", async ({
  page,
}) => {
  await page.goto("/next/settings/access");
  const entry = page.getByTestId("input-principal-entry");
  const grant = page.getByTestId("add-principal-btn");
  const error = page.getByTestId("principal-entry-error");

  for (const [typed, says] of [
    ["user:not-an-email", "user: entries must be an email address."],
    ["group:", "group: entries must be an email address."],
    [
      "domain:example.com",
      "Start the entry with user:, group: or serviceAccount:",
    ],
    [
      "serviceAccount:bot@example.com",
      "serviceAccount: entries must name a service account address (*.gserviceaccount.com).",
    ],
    [
      "user:svc@proj.iam.gserviceaccount.com",
      "Add a service account as serviceAccount:svc@proj.iam.gserviceaccount.com.",
    ],
  ]) {
    await entry.fill(typed);
    await expect(error, typed).toContainText(says);
    await expect(entry).toHaveAttribute("aria-invalid", "true");
    await expect(grant).toBeDisabled();
  }

  await entry.fill("serviceAccount:walk-bot@proj.iam.gserviceaccount.com");
  await expect(error).toHaveCount(0);
  await grant.click();
  await expect(page.getByTestId("principal-reason-error")).toHaveText(
    "Give a reason to admit a service account.",
  );
  await expect(
    page.getByTestId(
      "principal-row-serviceAccount:walk-bot@proj.iam.gserviceaccount.com",
    ),
  ).toHaveCount(0);
  await page.getByTestId("input-principal-reason").fill("walkthrough bot");
  await expect(page.getByTestId("principal-reason-error")).toHaveCount(0);

  // A bare address is filed as user:, as the server would.
  await entry.fill("Walk-Carol@Example.com");
  await expect(error).toHaveCount(0);
  await grant.click();
  const row = page.getByTestId("principal-row-user:walk-carol@example.com");
  await expect(row).toBeVisible();
  await page
    .getByTestId("remove-principal-user:walk-carol@example.com")
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Remove" })
    .click();
  await expect(row).toHaveCount(0);
});

test("CLI copies each workstation command on its own", async ({
  page,
  context,
  demoServer,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: demoServer,
  });
  await page.goto("/next/settings/cli");
  const clipboard = () => page.evaluate(() => navigator.clipboard.readText());

  await page.getByTestId("copy-cli-install").click();
  await expect(page.getByText("Install command copied")).toBeVisible();
  expect(await clipboard()).toBe(
    `curl -fsSL ${demoServer}/cli/install.sh | sh`,
  );

  await page.getByTestId("copy-cli-login").click();
  expect(await clipboard()).toMatch(
    /^grain login \S+ --name \S+ --issue-token --scope-set grain\/external-dev --expires 30d$/,
  );

  await page.getByTestId("copy-cli-skill").click();
  expect(await clipboard()).toBe("grain skill install --jetski");
});

test("a refused Branch name template is named by its label", async ({
  page,
}) => {
  await page.goto("/next/settings/tasks");
  const branch = page.locator("#t-branch");
  await branch.fill("bad..ref~{id}");
  await branch.press("Tab");
  await page.getByTestId("settings-save-btn").click();
  const banner = page.getByTestId("settings-error-banner");
  await expect(banner).toContainText("Branch name template:");
  await expect(banner).not.toContainText("taskBranchTemplate");
  await page.getByTestId("settings-discard-btn").click();
  await expect(page.getByTestId("settings-save-bar")).toBeHidden();
});

test("Settings lock is unavailable without a config repository", async ({
  page,
}) => {
  await page.goto("/next/settings/general");
  await expect(page.getByTestId("switch-settings-locked")).toBeDisabled();
  await expect(page.getByTestId("settings-lock-hint")).toHaveText(
    "Unavailable without a config repository: settings are always edited directly here.",
  );
});
