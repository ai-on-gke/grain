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

// Regressions from the 2026-11-14 connections & infrastructure settings
// walkthrough (docs/test-runs/2026-11-14-connections-infrastructure-settings.md):
// destructive removals on Git and Capabilities that went through on one
// click, a plain git host registered as a GitHub, a secret typed in the
// clear, Engine boxes whose blank meant a refused 0 and whose placeholder
// was not grain's default, and classic Integrations links that fell
// through to the Inbox and Fleet.

const row = (page, prefix, text) =>
  page.locator(`[data-testid^="${prefix}"]`, { hasText: text });

test("Git: a plain git host is sent as kind git, offers no GitHub App, and Remove asks first", async ({
  page,
}) => {
  await page.goto("/next/settings/git");
  await expect(page.getByTestId("card-git-connections")).toBeVisible();

  // GitHub (the default kind) offers a GitHub App ...
  await page.getByTestId("select-git-conn-type").click();
  await expect(page.getByRole("option", { name: /GitHub App/ })).toBeVisible();
  await page.keyboard.press("Escape");

  // ... a plain git host does not.
  await page.getByTestId("input-git-conn-host").fill("git.regress.example");
  await page.getByTestId("select-git-conn-kind").click();
  await page.getByRole("option", { name: "Plain git host" }).click();
  await page.getByTestId("select-git-conn-type").click();
  await expect(
    page.getByRole("option", { name: /SSH private key/ }),
  ).toBeVisible();
  await expect(page.getByRole("option", { name: /GitHub App/ })).toHaveCount(0);
  await page.keyboard.press("Escape");

  await page.getByTestId("input-git-conn-secret").fill("throwaway-not-a-token");
  const posted = page.waitForRequest(
    (r) =>
      r.method() === "POST" &&
      r.url().endsWith("/api/deployment/git-connections"),
  );
  await page.getByTestId("save-git-conn-btn").click();
  expect((await posted).postDataJSON()).toMatchObject({
    host: "git.regress.example",
    kind: "git",
    type: "pat",
  });
  const conn = row(page, "git-conn-row-", "git.regress.example");
  await expect(conn).toBeVisible();

  // Remove asks first; Cancel keeps the connection.
  await conn.getByRole("button", { name: "Remove" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Remove Git connection");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await page.reload();
  await expect(conn).toBeVisible();

  await conn.getByRole("button", { name: "Remove" }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Remove connection" })
    .click();
  await expect(conn).toHaveCount(0);
  await page.reload();
  await expect(page.getByTestId("card-git-connections")).toBeVisible();
  await expect(conn).toHaveCount(0);
});

test("Capabilities: removing a server asks first, and the secret form masks its value and says when there is no store", async ({
  page,
}) => {
  await page.goto("/next/settings/capabilities");
  const id = "https://capabilities.regress.example/throwaway";
  await page.getByTestId("input-cap-server-id").fill(id);
  await page.getByTestId("input-cap-server-url").fill("http://127.0.0.1:9/mcp");
  await page.getByTestId("save-cap-server-btn").click();
  const srv = page.getByTestId(`cap-server-row-${id}`);
  await expect(srv).toBeVisible();

  await page.getByTestId(`remove-cap-server-${id}`).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText(`Remove capability server ${id}?`);
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(srv).toBeVisible();

  await page.getByTestId(`remove-cap-server-${id}`).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Remove server" })
    .click();
  await expect(srv).toHaveCount(0);
  await page.reload();
  await expect(page.getByTestId("card-capability-servers")).toBeVisible();
  await expect(srv).toHaveCount(0);

  // The value box masks what is typed into it.
  const value = page.getByTestId("input-secret-value");
  await value.fill("not-a-real-secret");
  const security = await value.evaluate((el) =>
    getComputedStyle(el).getPropertyValue("-webkit-text-security"),
  );
  expect(security).toBe("disc");

  // `grain demo` runs with no secret store (GET /api/secrets says
  // enabled: false), where a PUT 404s: the form says so instead.
  const secrets = await (await page.request.get("/api/secrets")).json();
  if (secrets.enabled === false) {
    await expect(page.getByTestId("secrets-store-disabled")).toBeVisible();
    await page.getByTestId("input-secret-name").fill("regress");
    await page.getByTestId("input-secret-key").fill("token");
    await expect(page.getByTestId("store-secret-btn")).toBeDisabled();
  }
});

test("Engine: the poll placeholder is grain's default and blank boxes reset to defaults", async ({
  page,
}) => {
  const before = await (await page.request.get("/api/settings")).json();
  await page.goto("/next/settings/engine");
  const poll = page.getByTestId("input-poll-interval");
  const merges = page.getByTestId("input-max-merges-per-repo");
  const workers = page.getByTestId("input-max-workers");
  await expect(poll).toHaveAttribute("placeholder", before.pollIntervalDefault);

  try {
    await poll.fill("15s");
    await merges.fill("2");
    await workers.fill("3");
    await page.getByTestId("settings-save-btn").click();
    await expect(page.getByTestId("settings-save-btn")).toBeHidden();
    await page.reload();
    await expect(poll).toHaveValue("15s");
    await expect(merges).toHaveValue("2");
    await expect(workers).toHaveValue("3");

    // Blank is the default, not a refused zero.
    await poll.fill("");
    await merges.fill("");
    await workers.fill("");
    await page.getByTestId("settings-save-btn").click();
    await expect(page.getByTestId("settings-save-btn")).toBeHidden();
    await page.reload();
    await expect(poll).toHaveValue(before.pollIntervalDefault);
    await expect(merges).toHaveValue("5");
    await expect(workers).toHaveValue("10");

    // Out of range is still refused by name.
    await merges.fill("0");
    await page.getByTestId("settings-save-btn").click();
    await expect(
      page.getByText("maxMergesPerRepo must be at least 1"),
    ).toBeVisible();
  } finally {
    await page.request.post("/api/settings", {
      data: {
        pollInterval: before.pollInterval,
        maxWorkers: before.maxWorkers,
        maxMergesPerRepo: before.maxMergesPerRepo,
      },
    });
  }
});

test("classic Integrations links land on Settings -> Capabilities", async ({
  page,
}) => {
  for (const from of [
    "/next/admin/integrations",
    "/next/system/integrations",
  ]) {
    await page.goto(from);
    await expect(page).toHaveURL(/\/next\/settings\/capabilities$/);
    await expect(page.getByTestId("card-capability-servers")).toBeVisible();
  }
});

test("the new Git kind choice and Engine hints stay inside their cards at phone width @phone", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const inside = async (cardId, innerSel) => {
    const card = await page.getByTestId(cardId).boundingBox();
    const inner = await page.locator(innerSel).first().boundingBox();
    expect(card).not.toBeNull();
    expect(inner).not.toBeNull();
    expect(inner.x).toBeGreaterThanOrEqual(card.x - 1);
    expect(inner.x + inner.width).toBeLessThanOrEqual(card.x + card.width + 1);
    const overflow = await page
      .getByTestId(cardId)
      .evaluate((el) => el.scrollWidth - el.clientWidth);
    expect(overflow).toBeLessThanOrEqual(1);
  };
  await page.goto("/next/settings/git");
  await inside("card-git-connections", '[data-testid="select-git-conn-kind"]');
  await page.goto("/next/settings/engine");
  await inside("card-engine", "#e-mpr-hint");
  await inside("card-engine", "#e-poll-hint");
});
