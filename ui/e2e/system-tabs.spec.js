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

// Walkthrough: System's sandboxes and caches tabs (docs/test-plans/2026-09-25.md §3)
// Exercises System overlay tab navigation, Sandboxes live metrics and sparklines,
// Caches storage breakdowns (Go, NPM, PyPI, Debian, Playwright, OCI), cache flushing/pruning,
// and seamless tab switching without DOM errors or state loss.
test.use({ demoArgs: ["-observability"] });

async function startRun(page) {
  await page.getByRole("button", { name: /^CI \d+$/ }).click();
  await expect(page.getByRole("heading", { name: "CI" })).toBeVisible();
  await page.getByRole("button", { name: "Start a run" }).click();
  const repoSelect = page.locator('select[name="repo"]');
  await expect(repoSelect).toBeVisible();
  await repoSelect.selectOption("acme/widgets");
  await page.getByLabel("Branch").fill("main");
  await page.getByRole("button", { name: "Load workflows" }).click();
  await expect(page.getByLabel("Workflow")).toBeVisible();
  await page.getByRole("button", { name: "Start run", exact: true }).click();
  await expect(page.getByRole("button", { name: "Cancel run" })).toBeVisible();
  await page.getByRole("button", { name: "Back" }).click();
}

test("system sandboxes and caches tabs walkthrough", async ({ page }) => {
  test.setTimeout(90_000);

  // 1. Navigate to dashboard and initiate a background CI job to populate active sandbox metrics
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Tasks" })).toBeVisible();

  await startRun(page);

  // 2. Open Admin overlay via sidebar navigation
  const adminButton = page.getByRole("button", { name: "System" });
  await expect(adminButton).toBeVisible();
  await adminButton.click();

  // Overlay header and default tab
  await expect(
    page.getByRole("heading", { name: "System", exact: true }),
  ).toBeVisible();
  const logsTab = page.getByRole("tab", { name: "Logs" });
  const sandboxesTab = page.getByRole("tab", { name: "Sandboxes" });
  const cachesTab = page.getByRole("tab", { name: "Caches" });
  const topTab = page.getByRole("tab", { name: "Top" });
  const pipelineTab = page.getByRole("tab", { name: "Pipeline" });
  const powerTab = page.getByRole("tab", { name: "Power" });

  await expect(logsTab).toBeVisible();
  await expect(sandboxesTab).toBeVisible();
  await expect(cachesTab).toBeVisible();
  await expect(topTab).toBeVisible();
  await expect(pipelineTab).toBeVisible();
  await expect(powerTab).toBeVisible();

  // 3. Switch to Sandboxes tab and verify host & guest metrics
  await sandboxesTab.click();
  await expect(page.getByRole("heading", { name: "Sandboxes" })).toBeVisible();
  await expect(page.getByText(/Load average \(1\/5\/15 min\)/)).toBeVisible();
  await expect(page.getByText(/Memory:/)).toBeVisible();
  await expect(page.getByText(/Storage/)).toBeVisible();

  // Verify empty states for standing and persistent sandboxes when none are defined,
  // and that the rail shows no Staging group.
  const persistentPanel = page.getByTestId("persistent-sandboxes");
  await expect(persistentPanel).toBeVisible();
  await expect(persistentPanel).toContainText(
    "Persistent sandboxes keep a long-lived service running",
  );
  await expect(persistentPanel).toContainText("ask the director to set one up");
  await expect(persistentPanel).toContainText("tables/persistent_sandbox.json");
  await expect(
    persistentPanel.getByRole("link", { name: "director" }),
  ).toHaveAttribute("href", "/session/director");
  await expect(page.getByTestId("staging-group")).toHaveCount(0);

  const standingPanel = page.getByTestId("standing-sandboxes");
  await expect(standingPanel).toBeVisible();
  await expect(standingPanel).toContainText(
    "Standing sandboxes keep a long-lived container or VM image running",
  );
  await expect(standingPanel).toContainText("tables/standing_sandbox.json");
  await expect(
    standingPanel.getByRole("link", { name: "director" }),
  ).toHaveAttribute("href", "/session/director");

  // Verify active sandboxes table
  await expect(
    page.getByRole("columnheader", { name: "Run or CI job" }),
  ).toBeVisible();
  await expect(
    page.getByRole("columnheader", { name: "Status" }),
  ).toBeVisible();
  await expect(
    page.getByRole("columnheader", { name: "Load average" }),
  ).toBeVisible();
  await expect(
    page.getByRole("columnheader", { name: "CPU trend" }),
  ).toBeVisible();
  await expect(
    page.getByRole("columnheader", { name: "Memory trend" }),
  ).toBeVisible();

  // The CI rows are folded behind their summary row on load
  // (grain/task-990).
  const ciToggle = page.getByRole("button", { name: /^CI sandboxes \(\d+\)$/ });
  await expect(ciToggle).toHaveAttribute("aria-expanded", "false", {
    timeout: 10_000,
  });
  await ciToggle.click();
  const activeRow = page
    .getByRole("row")
    .filter({ has: page.getByRole("cell", { name: "ci", exact: true }) });
  await expect(activeRow.first()).toBeVisible({ timeout: 10_000 });

  // Test Refresh button
  const refreshButton = page.getByRole("button", { name: "Refresh" });
  await expect(refreshButton).toBeVisible();
  await refreshButton.click();
  await expect(activeRow.first()).toBeVisible();

  // 4. Switch to Caches tab and verify storage breakdowns across ecosystems
  await cachesTab.click();
  await expect(page.getByRole("heading", { name: "Caches" })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Proxy cache" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Actions cache" }),
  ).toBeVisible();

  // Verify ecosystems: apt (Debian), npm, pypi, go, docker (OCI), playwright
  await expect(page.getByText(/apt packages: 412 packages/)).toBeVisible();
  await expect(page.getByText(/npm packages: 128 packages/)).toBeVisible();
  await expect(page.getByText(/Python packages: 85 packages/)).toBeVisible();
  await expect(page.getByText(/Go modules: 64 packages/)).toBeVisible();
  await expect(page.getByText(/Docker Hub layers: 12 layers/)).toBeVisible();
  await expect(page.getByText(/Playwright browsers: 3 browsers/)).toBeVisible();
  await expect(page.getByText(/Total space consumed:/)).toBeVisible();

  // Verify Actions cache repository entries
  await expect(page.getByText(/acme\/widgets: 2\.0 GiB/)).toBeVisible();
  await expect(page.getByText(/acme\/gadgets: 1\.0 GiB/)).toBeVisible();

  // 5. Trigger cache prune / flush actions
  // Flush apt package cache
  const aptFlush = page.getByRole("button", { name: "Flush apt packages" });
  await expect(aptFlush).toBeVisible();
  await aptFlush.click();

  // Verify immediate metric update to 0 B for apt packages without overlay dismissal
  await expect(page.getByText(/apt packages: 0 packages, 0 B/)).toBeVisible({
    timeout: 5_000,
  });
  await expect(
    page.getByRole("heading", { name: "System", exact: true }),
  ).toBeVisible();

  // Flush actions cache for acme/widgets
  const actionsFlushBtn = page.getByRole("button", {
    name: "Flush the Actions cache of acme/widgets",
  });
  await actionsFlushBtn.click();
  await expect(page.getByText(/acme\/widgets: 0 B/)).toBeVisible({
    timeout: 5_000,
  });

  // Flush all remaining package caches via "Flush the whole cache" button
  const flushWholeBtn = page.getByRole("button", {
    name: "Flush the whole proxy cache",
  });
  await expect(flushWholeBtn).toBeVisible();
  await flushWholeBtn.click();
  await expect(page.getByText(/Total space consumed: 0 B/)).toBeVisible({
    timeout: 5_000,
  });
  await expect(
    page.getByText(/Playwright browsers: 0 browsers, 0 B/),
  ).toBeVisible();
  await expect(page.getByText(/Go modules: 0 packages, 0 B/)).toBeVisible();

  // 6. Verify seamless switching between all System overlay tabs without DOM errors or state loss
  // Top tab
  await topTab.click();
  await expect(page.getByRole("heading", { name: "Top" })).toBeVisible();

  // Pipeline tab
  await pipelineTab.click();
  await expect(page.getByText("Dispatch", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("button", { name: /Pause pipeline|Resume pipeline/ }),
  ).toBeVisible();

  // Power tab
  await powerTab.click();
  await expect(
    page
      .getByText(/Danger zone/i)
      .or(page.getByText(/Not available: restarting grain/i)),
  ).toBeVisible();

  // Logs tab
  await logsTab.click();
  await expect(page.locator("pre").first()).toBeVisible();

  // Sandboxes tab again
  await sandboxesTab.click();
  await expect(page.getByRole("heading", { name: "Sandboxes" })).toBeVisible();
  await expect(activeRow.first()).toBeVisible();

  // Clicking the director link in the persistent sandboxes empty state opens the director session,
  // and Back returns to System → Sandboxes.
  await page
    .getByTestId("persistent-sandboxes")
    .getByRole("link", { name: "director" })
    .click();
  await expect(page).toHaveURL(/\/session\/director$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/system\/sandboxes$/);

  // Caches tab again
  await cachesTab.click();
  await expect(page.getByRole("heading", { name: "Caches" })).toBeVisible();
  await expect(page.getByText(/Total space consumed: 0 B/)).toBeVisible();

  // 7. System is a view (design/design/settings-panel.design.md §2.1): the rail
  // leaves it, and Back returns to the tab that was open.
  await page.getByRole("button", { name: /^Tasks \d+/ }).click();
  await expect(
    page.getByRole("heading", { name: "System", exact: true }),
  ).toHaveCount(0);
  await page.goBack();
  await expect(page).toHaveURL(/\/system\/caches$/);
  await expect(page.getByRole("tab", { name: "Caches" })).toHaveAttribute(
    "aria-selected",
    "true",
  );

  // Cleanly complete CI step
  await page.request.post("/api/e2e/ci/finish");
});
