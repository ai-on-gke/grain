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

// Regressions found by the 2026-11-08 deployment-operations walkthrough
// (docs/test-runs/2026-11-08-deployment-operations-task-3464.md):
//
//   - UnbackedStateDot lacked display: inline-block, collapsing to 0px width
//     inside HealthTab's config-repo reset Alert and Settings' Config repo
//     section label, while duplicating the reset sentence in HealthTab's
//     accessibility tree;
//   - SandboxHealthPage formatted overcommit lastOomAt, standing sandbox
//     startedAt/nextAttempt, expired host-credential timestamps, and live
//     sandbox row Started tooltips with raw browser-local toLocaleString()
//     instead of the deployment's configured timeZone.
test.use({ demoArgs: ["-observability"], timezoneId: "UTC" });

test("config-repo reset dot renders with non-zero dimensions on System → Health and Settings nav without duplicating the alert label", async ({
  page,
}) => {
  await page.route(/\/api\/config(\?.*)?$/, async (route) => {
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const response = await route.fetch({ headers });
    const body = await response.json();
    body.stateRepoResetNotice = true;
    await route.fulfill({ response, json: body });
  });
  try {
    await page.goto("/system");
    const alert = page.getByTestId("health-config-repo");
    await expect(alert).toBeVisible();
    await expect(
      alert.getByRole("img", { name: /reset settings/i }),
    ).toHaveCount(0);
    const healthDot = alert.locator('span[aria-hidden="true"][title]');
    const healthBox = await healthDot.boundingBox();
    expect(healthBox).not.toBeNull();
    expect(healthBox.width).toBeGreaterThanOrEqual(7.5);
    expect(healthBox.height).toBeGreaterThanOrEqual(7.5);

    await alert.getByRole("button", { name: "Config repo" }).click();
    await expect(page).toHaveURL(/\/settings\/config-repo$/);
    const navDot = page
      .getByTestId("settings-nav")
      .getByRole("img", { name: /reset settings/i });
    const navBox = await navDot.boundingBox();
    expect(navBox).not.toBeNull();
    expect(navBox.width).toBeGreaterThanOrEqual(7.5);
    expect(navBox.height).toBeGreaterThanOrEqual(7.5);
  } finally {
    await page.unroute(/\/api\/config(\?.*)?$/);
  }
});

test("System → Health and Sandboxes format overcommit, standing, credential, and live-row timestamps in the deployment time zone", async ({
  page,
}) => {
  // In a UTC browser context, 2026-11-07T20:15:00Z is Nov 7 in UTC and Nov 8
  // (05:15 JST) in Asia/Tokyo.
  await page.route(/\/api\/config(\?.*)?$/, async (route) => {
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const response = await route.fetch({ headers });
    const body = await response.json();
    body.timeZone = "Asia/Tokyo";
    await route.fulfill({ response, json: body });
  });
  await page.route(/\/api\/settings(\?.*)?$/, async (route) => {
    const reqUrl = new URL(route.request().url());
    if (reqUrl.pathname !== "/api/settings") {
      await route.continue();
      return;
    }
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const response = await route.fetch({ headers });
    const body = await response.json();
    const prevOc = body.sandboxCommitment?.budget?.overcommit || {};
    body.sandboxCommitment = {
      ...(body.sandboxCommitment || {}),
      budget: {
        ...(body.sandboxCommitment?.budget || {}),
        overcommit: {
          ...prevOc,
          oomKills: 1,
          lastOomAt: "2026-11-07T20:15:00Z",
        },
      },
    };
    await route.fulfill({ response, json: body });
  });
  await page.route(/\/api\/sandboxes(\?.*)?$/, async (route) => {
    const reqUrl = new URL(route.request().url());
    const section = reqUrl.searchParams.get("section");
    if (section && section !== "budget") {
      await route.continue();
      return;
    }
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const response = await route.fetch({ headers });
    const body = await response.json();
    const oc = {
      ...(body.budget?.overcommit || {}),
      mode: "auto",
      cpuOvercommit: 2.25,
      memoryOvercommit: 1.25,
      oomKills: 1,
      lastOomAt: "2026-11-07T20:15:00Z",
    };
    body.overcommit = oc;
    body.budget = {
      ...(body.budget || {}),
      mode: "auto",
      cpuOvercommit: 2.25,
      memoryOvercommit: 1.25,
      overcommit: oc,
    };
    if (!section) {
      body.hostCredentials = {
        detail: "host credentials expired: renew on build-1.example.test",
        since: "2026-11-07T19:00:00Z",
        runs: 2,
      };
      body.standingSandboxes = [
        {
          name: "preview-worker",
          sandbox: "s-preview",
          image: "ghcr.io/acme/worker:latest",
          state: "backoff",
          failed: true,
          failures: 2,
          startedAt: "2026-11-07T20:00:00Z",
          nextAttempt: "2026-11-08T21:30:00Z",
          restarts: 2,
        },
      ];
      body.sandboxes = [
        ...(body.sandboxes || []),
        {
          sandbox: "agent-tz-check",
          name: "agent-tz-check",
          kind: "agent",
          backend: "docker",
          status: "running",
          startedAt: "2026-11-07T21:45:00Z",
          cpus: 2,
          memoryTotalMB: 4096,
        },
      ];
    }
    await route.fulfill({ response, json: body });
  });

  try {
    await page.goto("/system");
    const healthOc = page.getByTestId("sandbox-overcommit-status");
    await expect(healthOc).toContainText(/2026.*5:15|11\/8\/2026|Nov 8, 2026/);
    await expect(healthOc).not.toContainText(/11\/7\/2026|Nov 7, 2026/);

    await page.goto("/system/sandboxes");
    await expect(page.getByTestId("sandbox-overcommit-status")).toContainText(
      /2026.*5:15|11\/8\/2026|Nov 8, 2026/,
    );
    await expect(page.getByTestId("standing-sandboxes")).toContainText(
      /2026.*5:00|11\/8\/2026|Nov 8, 2026/,
    );
    await expect(page.getByTestId("standing-next-attempt")).toContainText(
      /2026.*6:30|11\/9\/2026|Nov 9, 2026/,
    );
    await expect(page.getByText(/host credentials expired/)).toContainText(
      /2026.*4:00|11\/8\/2026|Nov 8, 2026/,
    );
    const liveRow = page.locator("#live-sandbox-agent-tz-check");
    await expect(liveRow).toBeVisible();
    const titles = await liveRow
      .locator("td[title]")
      .evaluateAll((els) => els.map((el) => el.getAttribute("title")));
    expect(
      titles.some(
        (t) =>
          t?.startsWith("Started ") &&
          /11\/8\/2026|Nov 8, 2026|2026.*6:45/.test(t) &&
          !/11\/7\/2026|Nov 7, 2026/.test(t),
      ),
    ).toBe(true);
  } finally {
    await page.unroute(/\/api\/sandboxes(\?.*)?$/);
    await page.unroute(/\/api\/settings(\?.*)?$/);
    await page.unroute(/\/api\/config(\?.*)?$/);
  }
});
