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

test("a deep link to a later Fleet tab keeps that tab in view on a phone @phone", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  for (const [gotoPath, tabId] of [
    ["fleet?tab=controls", "controls"],
    ["system/logs", "logs"],
  ]) {
    await page.goto(gotoPath);
    const btn = page.getByTestId(`fleet-tab-${tabId}`);
    await expect(btn).toHaveAttribute("aria-selected", "true");
    await expect
      .poll(async () => {
        const b = await btn.boundingBox();
        return Boolean(b && b.x >= 0 && b.x + b.width <= 390);
      })
      .toBe(true);
  }
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
});

test("pausing, resuming, and lifting quota pauses on Fleet update the global dispatch banner immediately", async ({
  page,
}) => {
  try {
    await page.goto("fleet?tab=controls");
    await page
      .getByLabel("Pipeline pause reason")
      .fill("walkthrough sync check");
    await page.getByRole("button", { name: "Pause pipeline" }).click();
    await page
      .getByTestId("confirm-dialog")
      .getByRole("button", { name: "Pause pipeline" })
      .click();

    const banner = page.getByTestId("global-dispatch-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("walkthrough sync check");
    await expect(page.getByTestId("live-pill")).toContainText(
      "Pipeline paused",
    );

    await page.getByRole("button", { name: "Resume pipeline" }).click();
    await page
      .getByTestId("confirm-dialog")
      .getByRole("button", { name: "Resume pipeline" })
      .click();
    await expect(banner).toHaveCount(0);
  } finally {
    await page.request.delete("/api/pipeline-pause").catch(() => {});
  }

  let paused = true;
  const pausePayload = {
    paused: true,
    pauses: [
      {
        connection: "gemini-pro",
        connectionName: "Gemini 2.5 Pro",
        model: "gemini-2.5-pro",
        until: new Date(Date.now() + 3600e3).toISOString(),
        secondsRemaining: 3600,
        reason: "quota reached",
        canLift: true,
      },
    ],
  };
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...body,
        agentPause: paused ? pausePayload : { paused: false, pauses: [] },
      },
    });
  });
  await page.route(/\/api\/pause(\?.*)?$/, (route) => {
    if (route.request().method() === "DELETE") {
      paused = false;
      return route.fulfill({ json: { pause: { paused: false, pauses: [] } } });
    }
    return route.fulfill({
      json: {
        enabled: true,
        pause: paused ? pausePayload : { paused: false, pauses: [] },
      },
    });
  });

  await page.reload();
  const banner = page.getByTestId("global-dispatch-banner");
  await expect(banner).toContainText("Gemini 2.5 Pro");
  await page.getByRole("button", { name: "Lift pause" }).click();
  await expect(banner).toHaveCount(0);
});

test("Settings -> Caches disables Flush when caches are off, and asks before flushing when on", async ({
  page,
}) => {
  await page.goto("settings/caches");
  const pkgBtn = page.getByRole("button", { name: "Flush proxy cache" });
  const actBtn = page.getByRole("button", { name: "Flush Actions cache" });
  await expect(pkgBtn).toBeDisabled();
  await expect(actBtn).toBeDisabled();
  await expect(page.getByTestId("card-caches")).toContainText(
    "Proxy cache not enabled on daemon",
  );
  await expect(page.getByTestId("card-caches")).toContainText(
    "Actions cache not enabled on daemon",
  );

  const flushes = [];
  await page.route("**/api/package-cache", (route) =>
    route.fulfill({
      json: {
        enabled: true,
        cache: [{ kind: "npm", entries: 12, bytes: 2 * 1024 * 1024 }],
      },
    }),
  );
  await page.route("**/api/actions-cache", (route) =>
    route.fulfill({
      json: { enabled: true, entries: 5, bytes: 4 * 1024 * 1024 },
    }),
  );
  await page.route("**/api/package-cache/flush", (route) => {
    flushes.push("package");
    return route.fulfill({ json: { entries: 12, bytes: 2097152, cache: [] } });
  });
  await page.route("**/api/actions-cache/flush", (route) => {
    flushes.push("actions");
    return route.fulfill({
      json: {
        entries: 5,
        bytes: 4194304,
        cache: { enabled: true, entries: 0, bytes: 0 },
      },
    });
  });

  await page.reload();
  await expect(pkgBtn).toBeEnabled();
  await expect(actBtn).toBeEnabled();

  await pkgBtn.click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText("Flush package proxy cache?");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(flushes).toEqual([]);

  await pkgBtn.click();
  await dialog.getByRole("button", { name: "Flush cache" }).click();
  await expect(page.getByTestId("card-caches")).toContainText(
    "0 cached packages · 0 B",
  );

  await actBtn.click();
  await expect(dialog).toContainText("Flush GitHub Actions cache?");
  await dialog.getByRole("button", { name: "Flush cache" }).click();
  await expect(page.getByTestId("card-caches")).toContainText(
    "Cached entries: 0",
  );
  expect(flushes).toEqual(["package", "actions"]);
});

test("CI names each row uniquely, keeps selected runs in the URL, narrows non-task Re-run failed to failed jobs, and asks once for an unknown run", async ({
  page,
}) => {
  const at = new Date(Date.now() - 120e3).toISOString();
  let runs = [
    {
      id: "ci-main-2",
      provider: "grain",
      repo: "acme/widgets",
      branch: "main",
      commit: "abcdef1234567890",
      workflow: "tests",
      event: "push",
      requestedBy: "alice@example.com",
      status: "completed",
      conclusion: "success",
      startedAt: at,
      finishedAt: at,
      jobs: [
        {
          jobId: "lint",
          name: "lint",
          conclusion: "success",
          status: "completed",
        },
      ],
    },
    {
      id: "ci-main-1",
      provider: "grain",
      repo: "acme/widgets",
      branch: "main",
      commit: "abcdef1234567890",
      workflow: "tests",
      event: "push",
      requestedBy: "alice@example.com",
      status: "completed",
      conclusion: "failure",
      startedAt: at,
      finishedAt: at,
      jobs: [
        {
          jobId: "lint",
          name: "lint",
          conclusion: "success",
          status: "completed",
        },
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
  let unknownGets = 0;
  await page.route(/\/api\/ci\/runs(\?.*)?$/, (route) => {
    if (route.request().method() === "POST") {
      posted.push(route.request().postDataJSON());
      const created = {
        id: "ci-rerun-3",
        provider: "grain",
        repo: "acme/widgets",
        branch: "main",
        workflow: "tests",
        status: "queued",
        startedAt: new Date().toISOString(),
        jobs: [],
      };
      runs = [created, ...runs];
      return route.fulfill({ status: 201, json: created });
    }
    return route.fulfill({
      json: { enabled: true, runs, matched: runs.length, stored: runs.length },
    });
  });
  await page.route(/\/api\/ci\/runs\/[^/?]+(\?.*)?$/, (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop();
    const run = runs.find((r) => r.id === id);
    if (!run) {
      unknownGets++;
      return route.fulfill({ status: 404, json: { error: `no CI run ${id}` } });
    }
    return route.fulfill({ json: run });
  });
  await page.route(/\/api\/ci\/runs\/[^/]+\/artifacts/, (route) =>
    route.fulfill({ json: { available: false, artifacts: [] } }),
  );

  await page.goto("ci");
  await expect(page.getByRole("button", { name: /ci-main-2/ })).toBeVisible();
  const failRow = page.getByRole("button", { name: /ci-main-1/ });
  await failRow.click();
  await expect(page).toHaveURL(/\/next\/ci\/runs\/ci-main-1$/);

  await page.reload();
  await expect(page).toHaveURL(/\/next\/ci\/runs\/ci-main-1$/);
  await expect(page.getByTestId("ci-detail-status")).toContainText("Failed");

  await page.getByTestId("ci-rerun-failed-btn").click();
  await expect(page).toHaveURL(/\/next\/ci\/runs\/ci-rerun-3$/);
  expect(posted).toEqual([
    expect.objectContaining({
      repo: "acme/widgets",
      ref: "main",
      commit: "abcdef1234567890",
      event: "push",
      jobs: ["e2e"],
      recheck: true,
    }),
  ]);

  await page.goto("ci/runs/ci-forgotten-404");
  await expect(page.getByTestId("ci-action-error")).toContainText(
    "no CI run ci-forgotten-404",
  );
  expect(unknownGets).toBe(1);
});

test("classic System -> Metrics ?f=repo:any:... links narrow /next/telemetry to that repository", async ({
  page,
}) => {
  const queries = [];
  await page.route(/\/api\/metrics\?.*/, async (route) => {
    queries.push(new URL(route.request().url()).search);
    return route.continue();
  });
  await page.goto("system/metrics?f=repo%3Aany%3Aacme%2Fwidgets&window=30d");
  await expect(page).toHaveURL(/\/next\/telemetry\?/);
  await expect(page.getByTestId("telemetry-clear-scope")).toBeVisible();
  await expect
    .poll(() =>
      queries.some(
        (q) => q.includes("repo=acme%2Fwidgets") && q.includes("window=30d"),
      ),
    )
    .toBe(true);
});

// The 2026-11-16 walkthrough (docs/test-runs/2026-11-16-deployment-operations.md):
// a host that reported no disks showed a "/" disk at "0 MB / 0 MB", and a
// daemon that reported no start time showed a green "Up —".
test("Fleet says a disk and an uptime it was not told are not reported", async ({
  page,
}) => {
  const now = Date.now();
  let withFigures = false;
  await page.route("**/api/sandboxes*", (route) =>
    route.fulfill({
      json: {
        enabled: true,
        host: {
          cpus: 4,
          cpuUtilization: 0.15,
          memoryUsedMB: 4096,
          memoryTotalMB: 16384,
          ...(withFigures
            ? {
                disks: [
                  { path: "/var/lib/grain", usedMB: 2048, totalMB: 8192 },
                ],
              }
            : {}),
        },
        ...(withFigures
          ? {
              daemon: {
                startedAt: new Date(now - 3 * 3600_000).toISOString(),
                starts: 1,
              },
            }
          : {}),
        sandboxes: [],
      },
    }),
  );

  await page.goto("fleet?tab=controls");
  const disk = page.getByTestId("meter-disk");
  await expect(page.getByTestId("meter-disk-none")).toHaveText("Not reported");
  await expect(disk).not.toContainText("0 MB");
  await expect(disk).not.toContainText("host volume");
  const daemon = page.getByTestId("fleet-daemon-card");
  await expect(page.getByTestId("fleet-daemon-uptime-unknown")).toHaveText(
    "Uptime not reported",
  );
  await expect(daemon).not.toContainText("Up —");

  withFigures = true;
  await page.reload();
  await expect(disk).toContainText("/var/lib/grain");
  await expect(disk).toContainText("2.0 GB / 8.0 GB");
  await expect(page.getByTestId("meter-disk-none")).toHaveCount(0);
  await expect(daemon).toContainText(/Up \d/);
  await expect(page.getByTestId("fleet-daemon-uptime-unknown")).toHaveCount(0);
});

// Classic System -> Pipeline asks before pausing and resuming, and warns
// that resuming a pause held for a scheduled upgrade cancels the upgrade;
// /next/fleet did neither (2026-11-16 walkthrough).
test("Fleet asks before pausing or resuming the pipeline, and says resuming cancels a scheduled upgrade", async ({
  page,
}) => {
  const writes = [];
  let held = null;
  await page.route("**/api/pipeline-pause", async (route) => {
    const method = route.request().method();
    if (method === "GET" && held) {
      return route.fulfill({ json: { pause: held } });
    }
    if (method !== "GET") writes.push(method);
    return route.fallback();
  });
  try {
    await page.goto("fleet?tab=controls");
    const dialog = page.getByTestId("confirm-dialog");
    await page.getByTestId("pipeline-pause-btn").click();
    await expect(dialog).toContainText("Pause the pipeline?");
    await dialog.getByTestId("confirm-cancel").click();
    await expect(dialog).toBeHidden();
    await page.getByTestId("pipeline-pause-btn").click();
    await expect(dialog).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeHidden();
    expect(writes).toEqual([]);
    await expect(page.getByTestId("pipeline-pause-state")).toContainText(
      "Dispatching",
    );

    held = {
      paused: true,
      runNowPasses: true,
      reason: "upgrading to v9.9.9 at 02:00 UTC",
      byKind: "automation",
      byId: "upgrade-schedule",
    };
    await page.reload();
    await page.getByTestId("pipeline-resume-btn").click();
    await expect(dialog).toContainText(
      "Resume and cancel the scheduled upgrade?",
    );
    await expect(dialog).toContainText("upgrading to v9.9.9 at 02:00 UTC");
    await expect(dialog).toContainText("Resuming cancels that upgrade");
    await dialog.getByTestId("confirm-cancel").click();
    expect(writes).toEqual([]);
    await expect(page.getByTestId("pipeline-pause-state")).toContainText(
      "Paused",
    );
  } finally {
    await page.unroute("**/api/pipeline-pause");
    await page.request.delete("/api/pipeline-pause").catch(() => {});
  }
});

// Start CI run put a refusal on the page's action banner, behind the
// dialog's scrim, and left the dialog open with nothing said (2026-11-16
// walkthrough: POST /api/ci/runs answers 403 on a repository not in grain
// CI mode).
test("CI's Run workflow dialog says why a run was refused", async ({
  page,
}) => {
  const refusal =
    'grain CI is not enabled for acme/widgets: set its CI Mode to "Use Grain CI"';
  const posts = [];
  await page.route("**/api/ci/runs", (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    posts.push(route.request().postDataJSON());
    return route.fulfill({ status: 403, json: { error: refusal } });
  });
  await page.goto("ci");
  await page.getByRole("button", { name: "Run workflow" }).first().click();
  const dialog = page.getByTestId("ci-run-dialog");
  await dialog.getByTestId("ci-run-repo-input").fill("acme/widgets");
  await dialog.getByTestId("ci-run-ref-input").fill("main");
  await dialog.getByRole("button", { name: "Start CI run" }).click();
  await expect(dialog.getByTestId("ci-dialog-error")).toContainText(refusal);
  await expect(
    dialog.getByRole("button", { name: "Start CI run" }),
  ).toBeEnabled();
  expect(posts).toHaveLength(1);

  // Closing and opening it again starts clean.
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
  await page.getByRole("button", { name: "Run workflow" }).first().click();
  await expect(page.getByTestId("ci-dialog-error")).toHaveCount(0);
});
