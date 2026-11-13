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

// What the 2026-11-13 deployment-operations walkthrough found under /next/
// (docs/test-runs/2026-11-13-deployment-operations.md): the classic UI's
// System addresses fell through to the Inbox, Fleet's tabs were not in
// history, the build stamp led nowhere, Restart daemon reported a refused
// restart as under way, and a non-JSON error answer surfaced as a JSON
// parser message.

test("classic System addresses land on the matching /next/ page", async ({
  page,
}) => {
  const cases = [
    ["system/health", "/next/fleet"],
    ["system/sandboxes", "/next/fleet"],
    ["system/top", "/next/fleet"],
    ["system/logs", "/next/fleet?tab=logs"],
    ["system/pipeline", "/next/fleet?tab=controls"],
    ["system/power", "/next/fleet?tab=controls"],
    ["system/caches", "/next/settings/caches"],
    ["system/upgrade", "/next/settings/upgrade"],
    [
      "system/metrics?repo=acme%2Fwidgets",
      "/next/telemetry?repo=acme%2Fwidgets",
    ],
    ["admin/logs", "/next/fleet?tab=logs"],
    ["sandboxes", "/next/fleet"],
    ["caches", "/next/settings/caches"],
    [
      "metrics?repo=acme%2Fwidgets&window=30d",
      "/next/telemetry?repo=acme%2Fwidgets&window=30d",
    ],
  ];
  for (const [from, to] of cases) {
    await page.goto(from);
    await expect
      .poll(
        () => {
          const u = new URL(page.url());
          return u.pathname + u.search;
        },
        { message: from },
      )
      .toBe(to);
  }
  await expect(page.locator("#fleet-panel-logs")).toHaveCount(0);
  await page.goto("system/logs");
  await expect(page.locator("#fleet-panel-logs")).toBeVisible();
});

test("Fleet tabs are history entries: Back and Forward switch them", async ({
  page,
}) => {
  await page.goto("fleet");
  await expect(page.locator("#fleet-panel-sandboxes")).toBeVisible();
  await expect(page.getByTestId("fleet-tab-hostds")).toHaveText(/^\s*Hosts/);
  await page.getByTestId("fleet-tab-hostds").click();
  await expect(page.locator("#fleet-panel-hostds")).toBeVisible();
  await page.getByTestId("fleet-tab-logs").click();
  await expect(page).toHaveURL(/\/next\/fleet\?tab=logs$/);
  await expect(page.locator("#fleet-panel-logs")).toBeVisible();

  await page.goBack();
  await expect(page).toHaveURL(/\/next\/fleet\?tab=hostds$/);
  await expect(page.locator("#fleet-panel-hostds")).toBeVisible();
  await expect(page.getByTestId("fleet-tab-hostds")).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await page.goBack();
  await expect(page).toHaveURL(/\/next\/fleet$/);
  await expect(page.locator("#fleet-panel-sandboxes")).toBeVisible();
  await page.goForward();
  await page.goForward();
  await expect(page.locator("#fleet-panel-logs")).toBeVisible();
  await expect(page.getByTestId("logs-viewer")).toBeVisible();

  await page.reload();
  await expect(page.locator("#fleet-panel-logs")).toBeVisible();
});

test("the build stamp opens Settings -> Upgrade", async ({ page }) => {
  await page.goto("inbox");
  const stamp = page.getByTestId("build-stamp");
  await expect(stamp).toContainText(/grain \S+/);
  await stamp.click();
  await expect(page).toHaveURL(/\/next\/settings\/upgrade$/);
});

test("Restart daemon is off where restart is not enabled, and a refused restart says so", async ({
  page,
}) => {
  const posts = [];
  await page.route("**/api/host/restart", (route) => {
    posts.push(route.request().method());
    return route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "sudo: a password is required" }),
    });
  });

  // The demo starts without a restart command (restartEnabled: false).
  await page.goto("fleet?tab=controls");
  await expect(page.getByTestId("daemon-restart-btn")).toBeDisabled();

  // Where it is enabled, a refusal is reported and nothing is restarting.
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: { ...body, restartEnabled: true },
    });
  });
  await page.reload();
  const restart = page.getByTestId("daemon-restart-btn");
  await expect(restart).toBeEnabled();
  await restart.click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText("Restart grain daemon?");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
  expect(posts).toEqual([]);

  await restart.click();
  await dialog.getByRole("button", { name: "Restart daemon" }).click();
  await expect(
    page.getByText("Restart failed: sudo: a password is required"),
  ).toBeVisible();
  await expect(page.getByText("Restart requested.")).toHaveCount(0);
  await expect(page.getByTestId("fleet-daemon-card")).not.toContainText(
    "Restarting",
  );
  expect(posts).toEqual(["POST"]);
});

test("a non-JSON error answer names its HTTP status", async ({ page }) => {
  test.info().annotations.push({ type: "allows-page-error" });
  await page.route("**/api/metrics?**", (route) =>
    route.fulfill({
      status: 502,
      contentType: "text/html",
      body: "<html><body>Bad Gateway</body></html>",
    }),
  );
  await page.goto("telemetry");
  const err = page.getByTestId("telemetry-error");
  await expect(err).toContainText("The server answered 502");
  await expect(err).not.toContainText("Unexpected token");
});

// The demo runs no grain CI, so the runs are stubbed at /api/ci/runs; the
// page, its router and the user's clicks are real.
test("Re-run failed follows to the run it starts", async ({ page }) => {
  const at = new Date(Date.now() - 3600e3).toISOString();
  let runs = [
    {
      id: "ci-fail-1",
      provider: "grain",
      repo: "acme/widgets",
      branch: "feature/audit-log",
      workflow: "tests",
      taskId: "6",
      status: "completed",
      conclusion: "failure",
      startedAt: at,
      finishedAt: at,
      jobs: [
        { jobId: "go", name: "go", conclusion: "success", status: "completed" },
        {
          jobId: "e2e",
          name: "e2e",
          conclusion: "failure",
          status: "completed",
        },
      ],
    },
  ];
  const posted = [];
  await page.route(/\/api\/ci\/runs(\?.*)?$/, (route) => {
    if (route.request().method() === "POST") {
      posted.push(route.request().postDataJSON());
      runs = [
        {
          id: "ci-rerun-2",
          provider: "grain",
          repo: "acme/widgets",
          branch: "feature/audit-log",
          workflow: "tests",
          taskId: "6",
          status: "queued",
          startedAt: new Date().toISOString(),
          jobs: [],
        },
        ...runs,
      ];
      return route.fulfill({ status: 201, json: { id: "ci-rerun-2" } });
    }
    return route.fulfill({
      json: { enabled: true, runs, matched: runs.length, stored: runs.length },
    });
  });
  await page.route(/\/api\/ci\/runs\/[^/?]+(\?.*)?$/, (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop();
    const run = runs.find((r) => r.id === id);
    return run
      ? route.fulfill({ json: run })
      : route.fulfill({ status: 404, json: { error: `no CI run ${id}` } });
  });
  await page.route(/\/api\/ci\/runs\/[^/]+\/artifacts/, (route) =>
    route.fulfill({ json: { available: false, artifacts: [] } }),
  );

  await page.goto("ci/runs/ci-fail-1");
  await expect(page.getByTestId("ci-detail-status")).toContainText("Failed");
  await page.getByTestId("ci-rerun-failed-btn").click();
  await expect(page).toHaveURL(/\/next\/ci\/runs\/ci-rerun-2$/);
  await expect(page.getByTestId("ci-run-row-ci-rerun-2")).toHaveAttribute(
    "aria-current",
    "true",
  );
  expect(posted).toEqual([
    expect.objectContaining({
      taskId: "6",
      recheck: true,
      recheckFailed: true,
    }),
  ]);
});
