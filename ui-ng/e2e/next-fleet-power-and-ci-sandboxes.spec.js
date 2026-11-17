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

// Two classic System features the 2026-11-14 deployment-operations
// walkthrough found with no /next/ counterpart
// (docs/test-runs/2026-11-14-deployment-operations.md): System → Power's
// Reboot host and Shut down host, and System → Sandboxes' CI rows folded
// behind a "CI sandboxes (N)" summary row.
//
// The power tests intercept POST /api/host/{reboot,shutdown} and nothing
// else on that path: a request that reached the demo would only be
// refused, but no test here lets one through.

// interceptPower answers every POST /api/host/{reboot,shutdown} with
// answer(action) and records which were made.
async function interceptPower(page, answer) {
  const posts = [];
  await page.route(/\/api\/host\/(reboot|shutdown)$/, (route) => {
    const action = /\/(reboot|shutdown)$/.exec(route.request().url())[1];
    posts.push(`${route.request().method()} ${action}`);
    return answer(route, action);
  });
  return posts;
}

// withPowerConfig rewrites /api/config's two power flags.
async function withPowerConfig(page, flags) {
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({ response: res, json: { ...body, ...flags } });
  });
}

test("Fleet offers Reboot host and Shut down host only where /api/config enables them", async ({
  page,
}) => {
  const posts = await interceptPower(page, (route) =>
    route.fulfill({ status: 202, json: { ok: true } }),
  );
  await withPowerConfig(page, {
    rebootEnabled: false,
    shutdownEnabled: false,
  });
  await page.goto("fleet?tab=controls");
  const card = page.getByTestId("fleet-host-power-card");
  await expect(card).toBeVisible();
  await expect(page.getByTestId("host-power-unavailable")).toBeVisible();
  await expect(page.getByTestId("host-reboot-btn")).toHaveCount(0);
  await expect(page.getByTestId("host-shutdown-btn")).toHaveCount(0);

  await page.unroute("**/api/config");
  await withPowerConfig(page, { rebootEnabled: true, shutdownEnabled: false });
  await page.reload();
  await expect(page.getByTestId("host-reboot-btn")).toBeVisible();
  await expect(page.getByTestId("host-shutdown-btn")).toHaveCount(0);
  await expect(page.getByTestId("host-power-unavailable")).toHaveCount(0);
  expect(posts).toEqual([]);
});

test("Reboot host and Shut down host confirm first and report a request the host accepted", async ({
  page,
  axe,
}) => {
  const posts = await interceptPower(page, (route) =>
    route.fulfill({ status: 202, json: { ok: true } }),
  );
  await withPowerConfig(page, { rebootEnabled: true, shutdownEnabled: true });
  await page.goto("fleet?tab=controls");

  const reboot = page.getByTestId("host-reboot-btn");
  const shutdown = page.getByTestId("host-shutdown-btn");
  await expect(reboot).toHaveText("Reboot host");
  await expect(shutdown).toHaveText("Shut down host");

  // Cancelling sends nothing.
  await reboot.click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText("Reboot the host machine?");
  await expect(dialog).toContainText("Every task currently running");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
  expect(posts).toEqual([]);

  await reboot.click();
  await dialog.getByRole("button", { name: "Reboot host" }).click();
  const requested = page.getByTestId("host-power-requested");
  await expect(requested).toContainText("Reboot requested.");
  expect(posts).toEqual(["POST reboot"]);

  await shutdown.click();
  await expect(dialog).toContainText("Shut down the host machine?");
  await expect(dialog).toContainText(
    "grain stays down until somebody powers the machine back on",
  );
  await dialog.getByRole("button", { name: "Shut down host" }).click();
  await expect(requested).toContainText("Shutdown requested.");
  await expect(requested).not.toContainText("Reboot requested.");
  expect(posts).toEqual(["POST reboot", "POST shutdown"]);

  expect(await axe()).toEqual([]);
});

test("A dropped connection counts as accepted, and a refusal shows its reason", async ({
  page,
}) => {
  let answer = "drop";
  const posts = await interceptPower(page, (route) =>
    answer === "drop"
      ? route.abort("connectionreset")
      : route.fulfill({
          status: 500,
          json: { error: "sudo: a password is required" },
        }),
  );
  await withPowerConfig(page, { rebootEnabled: true, shutdownEnabled: true });
  await page.goto("fleet?tab=controls");
  const dialog = page.getByTestId("confirm-dialog");

  // A host going down drops the connection before it answers.
  await page.getByTestId("host-shutdown-btn").click();
  await dialog.getByRole("button", { name: "Shut down host" }).click();
  await expect(page.getByTestId("host-power-requested")).toContainText(
    "Shutdown requested.",
  );

  // An answer is the host refusing: its reason, and nothing requested.
  answer = "refuse";
  await page.reload();
  await page.getByTestId("host-reboot-btn").click();
  await dialog.getByRole("button", { name: "Reboot host" }).click();
  await expect(
    page.getByText("Reboot host failed: sudo: a password is required"),
  ).toBeVisible();
  await expect(page.getByTestId("host-power-requested")).toHaveCount(0);
  expect(posts).toEqual(["POST shutdown", "POST reboot"]);
});

// sandboxesWith is GET /api/sandboxes answered with the demo's own
// response, plus the live rows given.
async function sandboxesWith(page, rows, standingSandboxes = []) {
  await page.route(/\/api\/sandboxes(\?.*)?$/, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...body,
        enabled: true,
        sandboxes: rows,
        standingSandboxes,
      },
    });
  });
}

test("Fleet folds CI sandboxes behind a summary row and remembers whether it is open @phone", async ({
  page,
}) => {
  const now = Date.now();
  const row = (sandbox, kind, minutes, extra = {}) => ({
    sandbox,
    name: sandbox,
    kind,
    backend: "granule",
    ready: true,
    cpus: 2,
    memoryMB: 4096,
    startedAt: new Date(now - minutes * 60_000).toISOString(),
    ...extra,
  });
  await sandboxesWith(
    page,
    [
      row("ci-acme-widgets-101", "ci", 5),
      row("4801-1", "agent", 3),
      row("ci-acme-widgets-102", "ci", 42),
      row("standing-docs", "ci", 90),
    ],
    [{ name: "docs", sandbox: "standing-docs", state: "running" }],
  );

  await page.goto("fleet");
  const table = page.getByTestId("fleet-live-sandboxes");
  const summary = page.getByTestId("ci-sandboxes-summary-row");
  const toggle = page.getByTestId("ci-sandboxes-toggle");

  // Collapsed by default: the agent row and the standing sandbox (which
  // reports kind ci) stay; the two CI rows fold into the summary.
  await expect(toggle).toHaveText(/CI sandboxes \(2\)/);
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByTestId("ci-sandboxes-summary")).toHaveText(
    "2 CI sandboxes · 4 vCPU / 8.0 GiB · longest 42m (ci-acme-widgets-102)",
  );
  await expect(table.getByTestId("sandbox-row-4801-1")).toBeVisible();
  await expect(table.getByTestId("sandbox-row-standing-docs")).toBeVisible();
  await expect(
    table.getByTestId("sandbox-row-ci-acme-widgets-101"),
  ).toHaveCount(0);

  // The summary sits below every row it does not fold.
  const rows = table.locator("tbody tr");
  await expect(rows.last()).toHaveAttribute(
    "data-testid",
    "ci-sandboxes-summary-row",
  );

  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(toggle).toHaveAttribute(
    "aria-controls",
    "live-sandbox-ci-acme-widgets-101 live-sandbox-ci-acme-widgets-102",
  );
  await expect(
    table.getByTestId("sandbox-row-ci-acme-widgets-101"),
  ).toBeVisible();
  await expect(
    table.getByTestId("sandbox-row-ci-acme-widgets-102"),
  ).toBeVisible();
  const order = await rows.evaluateAll((trs) =>
    trs.map((tr) => tr.getAttribute("data-testid")),
  );
  expect(order.indexOf("ci-sandboxes-summary-row")).toBeLessThan(
    order.indexOf("sandbox-row-ci-acme-widgets-101"),
  );
  expect(order.indexOf("sandbox-row-4801-1")).toBeLessThan(
    order.indexOf("ci-sandboxes-summary-row"),
  );

  // Open survives a reload, and so does closed.
  await page.reload();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(
    table.getByTestId("sandbox-row-ci-acme-widgets-101"),
  ).toBeVisible();
  await toggle.click();
  await page.reload();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(
    table.getByTestId("sandbox-row-ci-acme-widgets-101"),
  ).toHaveCount(0);
  await expect(summary).toBeVisible();
});

test("Fleet shows no CI summary row when no CI sandbox is running", async ({
  page,
}) => {
  await sandboxesWith(page, [
    {
      sandbox: "4801-1",
      name: "4801-1",
      kind: "agent",
      backend: "granule",
      ready: true,
    },
  ]);
  await page.goto("fleet");
  await expect(page.getByTestId("sandbox-row-4801-1")).toBeVisible();
  await expect(page.getByTestId("ci-sandboxes-summary-row")).toHaveCount(0);
});
