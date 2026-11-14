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
