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

// Comprehensive UI workflows for System, Caches, Pipeline dispatch pause/resume, and Logs.

// The -observability demo is the one with caches to flush: the plain demo
// runs neither a proxy cache nor an Actions cache, so /caches there has no
// flush button at all. "-open=false" (already the fixture's own flag) makes
// this a demo of its own rather than the one system-overlay-tabs.spec.js
// may already have flushed on this worker.
test.use({ demoArgs: ["-observability", "-open=false"] });

test.describe("System Administration and Diagnostic Workflows", () => {
  test("flushes proxy and actions caches, pauses and resumes the pipeline, and inspects logs", async ({
    page,
  }) => {
    // 1. Navigate to /admin/caches (or /caches)
    await page.goto("/caches");
    await expect(page.getByRole("heading", { name: "Caches" })).toBeVisible();

    // Click Refresh button on Caches
    const refreshCacheBtn = page.getByRole("button", { name: "Refresh" });
    await expect(refreshCacheBtn).toBeVisible();
    await refreshCacheBtn.click();

    // "Flush the whole cache" for the proxy cache, named apart from the
    // Actions cache's button of the same text.
    await expect(page.getByText(/Total space consumed: [1-9]/)).toBeVisible();
    const flushWholeBtn = page.getByRole("button", {
      name: "Flush the whole proxy cache",
    });
    await expect(flushWholeBtn).toBeEnabled();
    await flushWholeBtn.click();
    await expect(page.getByText(/Total space consumed: 0 B/)).toBeVisible();

    // Flush one Actions cache repository first and confirm its row leaves
    // the list, then flush the rest and confirm the empty state replaces
    // the repository list without phantom 0 B rows.
    await expect(page.getByText("acme/widgets: 2.0 GiB")).toBeVisible();
    await expect(page.getByText("acme/gadgets: 1.0 GiB")).toBeVisible();
    await page
      .getByRole("button", { name: "Flush the Actions cache of acme/gadgets" })
      .click();
    await expect(page.getByText(/acme\/gadgets:/)).toHaveCount(0);
    await expect(page.getByText("acme/widgets: 2.0 GiB")).toBeVisible();
    const flushActionsWholeBtn = page.getByRole("button", {
      name: "Flush the whole Actions cache",
    });
    await expect(flushActionsWholeBtn).toBeEnabled();
    await flushActionsWholeBtn.click();
    await expect(page.getByText("empty (0 B of 20.0 GiB)")).toBeVisible();
    await expect(page.getByText(/Nothing cached yet\./)).toBeVisible();
    await expect(page.getByText(/acme\/widgets:/)).toHaveCount(0);
    await expect(flushActionsWholeBtn).toBeDisabled();

    // 2. Navigate to /admin/pipeline
    await page.goto("/system/pipeline");
    await expect(page.getByRole("tab", { name: "Pipeline" })).toBeVisible();

    // Test Pause pipeline, from a pipeline this test has made sure is
    // running rather than one it branches on: the pause is the store's,
    // so it outlives a test that stopped between the two clicks below.
    const unpaused = await page.request.delete("/api/pipeline-pause");
    expect(
      unpaused.ok(),
      `DELETE /api/pipeline-pause: ${unpaused.status()}`,
    ).toBe(true);
    await page.goto("/system/pipeline");
    const pauseBtn = page.getByRole("button", { name: "Pause pipeline" });
    await expect(pauseBtn).toBeVisible();

    page.once("dialog", (d) => d.accept());
    await page.getByLabel("Reason (optional)").fill("E2E Maintenance Test");
    await pauseBtn.click();

    // Verify the pipeline paused alert and Resume button appear
    const resumeBtn = page.getByRole("button", { name: "Resume pipeline" });
    await expect(resumeBtn).toBeVisible();

    // Test Resume pipeline
    page.once("dialog", (d) => d.accept());
    await resumeBtn.click();

    // Verify Pause button is restored
    await expect(pauseBtn).toBeVisible();

    // 3. Navigate to /admin/logs
    await page.goto("/system/logs");
    await expect(page.getByRole("tab", { name: "Logs" })).toBeVisible();

    // Test logs search input and refresh button
    const logsSearch = page.getByLabel("Search logs");
    await expect(logsSearch).toBeVisible();
    await logsSearch.fill("grain");
    const refreshLogsBtn = page.getByRole("button", { name: "Refresh" });
    await expect(refreshLogsBtn).toBeVisible();
    await refreshLogsBtn.click();

    // 4. Navigate to /admin/sandboxes
    await page.goto("/system/sandboxes");
    await expect(page.getByRole("tab", { name: "Sandboxes" })).toBeVisible();

    // 5. Navigate to /system/top and verify the process snapshot is a keyboard-focusable region
    await page.goto("/system/top");
    await expect(page.getByRole("tab", { name: "Top" })).toBeVisible();
    const topRegion = page.getByRole("region", { name: "Process snapshot" });
    await expect(topRegion).toBeVisible();
    await expect(topRegion).toHaveAttribute("tabindex", "0");
  });
});
