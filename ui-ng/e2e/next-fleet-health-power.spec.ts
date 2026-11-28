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

import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

// Regression coverage for porting classic System -> Health and
// Reboot/Shut down host to /next/fleet (task 7104).

test("Fleet Health renders standing alerts, GitHub budget, daemon counters, host readiness, and scrolls Settings to #<field-id>", async ({
  page,
  axe,
}) => {
  const now = Date.now();
  const resetsAt = new Date(now + 40 * 60_000).toISOString();
  const exhaustedAt = new Date(now + 30 * 60_000).toISOString();
  const pausedUntil = new Date(now + 3 * 3600_000).toISOString();

  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...body,
        agentPause: {
          paused: true,
          pauses: [
            {
              connection: "antigravity",
              connectionName: "Antigravity",
              model: "gemini-2.5-pro",
              until: pausedUntil,
              secondsRemaining: 10800,
              reason: "quota reached",
              canLift: true,
            },
          ],
        },
      },
    });
  });

  await page.route("**/api/notifications**", (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({
      json: [
        {
          id: 901,
          subject: "release.pending:v2.4.0",
          severity: "warning",
          actions: ["approve-release"],
          repo: "acme/widgets",
          body: "v2.4.0 passed qualification.",
        },
        {
          id: 902,
          subject: "merge.stalled:1",
          title: "A merge has been stalled for 2 hours",
          severity: "warning",
          taskId: "1",
          body: "Pull request #1 has stood in recheck.",
        },
      ],
    });
  });

  await page.route(/\/api\/metrics\?.*section=github/, (route) =>
    route.fulfill({
      json: {
        github: {
          enabled: true,
          budget: {
            limit: 5000,
            remaining: 1117,
            resetsAt,
            spendPerHour: 2234,
            exhaustedAt,
            lowFraction: 0.15,
          },
          polls: {
            tracked: 3,
            backedOff: 2,
            baseSeconds: 30,
            capSeconds: 480,
            medianSeconds: 120,
            maxSeconds: 480,
          },
          callers: [
            {
              caller: "reconciler",
              spent: 840,
              revalidated: 1920,
              kinds: [{ kind: "PR status", spent: 510 }],
            },
            {
              caller: "merge-queue",
              spent: 277,
              revalidated: 610,
              kinds: [{ kind: "check runs", spent: 190 }],
            },
          ],
        },
      },
    }),
  );

  let hostUnreachable = false;
  await page.route("**/api/settings/cli-run", (route) =>
    route.fulfill({
      json: {
        settings: {},
        hostExecutor: hostUnreachable
          ? {
              hostd: {
                reachable: false,
                error: "host socket /run/grain/hostd.sock did not answer",
              },
            }
          : {
              hostd: {
                reachable: true,
                missing: [
                  {
                    name: "grain-workspace",
                    command:
                      "sudo systemctl start grain-hostd-allowlist.service",
                    entry:
                      "  grain-workspace:\n    command: /usr/bin/grain-workspace\n",
                  },
                ],
                suggest: ["claude"],
              },
            },
      },
    }),
  );

  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...body,
        pendingRestart: ["k8sPrivileged", "githubHost"],
        unrunnable: [
          { key: "pollInterval", stored: "0s", running: "30s" },
          { key: "sandboxBackend", stored: "bogus", running: "container" },
        ],
      },
    });
  });

  // Classic /system/health redirects straight to /next/fleet?tab=health.
  await page.goto("system/health");
  await expect(page).toHaveURL(/\/next\/fleet\?tab=health$/);
  await expect(page.getByRole("tab", { name: /Health/ })).toHaveAttribute(
    "aria-selected",
    "true",
  );

  // Registered daemon health counters (real demo server registers sign-in verification (demo)).
  const counters = page.getByTestId("health-counters");
  await expect(counters).toBeVisible();
  await expect(counters).toContainText("sign-in verification (demo)");
  await expect(counters).toContainText(/proxy:/);

  // GitHub API budget & idle-poll backoff + callers table.
  const gh = page.getByTestId("health-github-budget");
  await expect(gh).toContainText("GitHub API: 1,117 of 5,000 calls left");
  await expect(page.getByTestId("health-github-runs-out")).toContainText(
    /runs out in \d+m/,
  );
  await expect(page.getByTestId("health-github-polls")).toContainText(
    "Idle-poll backoff: 2 of 3 tracked PRs backed off",
  );
  await expect(page.getByTestId("health-github-callers")).toContainText(
    "reconciler",
  );

  // Host readiness missing entry + suggested CLI entry.
  await expect(page.getByTestId("hostd-health-missing")).toContainText(
    "grain-workspace",
  );
  await expect(page.getByTestId("hostd-entry-text")).toContainText(
    "/usr/bin/grain-workspace",
  );
  await expect(page.getByTestId("hostd-suggest")).toContainText("claude");

  // Standing alerts list with links & actions.
  const alertsCard = page.getByTestId("health-alerts-card");
  await expect(alertsCard).toContainText("v2.4.0 is ready to promote");
  await expect(alertsCard).toContainText(
    "A merge has been stalled for 2 hours",
  );
  await expect(alertsCard).toContainText(
    "Usage limit reached (gemini-2.5-pro)",
  );
  await expect(
    alertsCard.getByRole("link", { name: "Open #1" }),
  ).toHaveAttribute("href", "/next/task/1");
  await expect(
    alertsCard.getByRole("link", { name: "Open connection" }),
  ).toHaveAttribute("href", "/next/settings/models#card-model-backends");

  // Accessibility & contrast in light and dark, desktop and phone.
  expect(await axe()).toEqual([]);
  const contrastLight = await new AxeBuilder({ page })
    .include("#fleet-panel-health")
    .withRules(["color-contrast"])
    .analyze();
  expect(contrastLight.violations).toEqual([]);

  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    ),
  ).toBe(0);
  expect(
    await page.evaluate(
      () =>
        document.documentElement.scrollHeight -
        document.documentElement.clientHeight,
    ),
  ).toBe(0);
  await page.setViewportSize({ width: 1440, height: 900 });

  // Unreachable hostd state.
  hostUnreachable = true;
  await page.getByTestId("fleet-refresh").click();
  await expect(page.getByTestId("hostd-unreachable")).toContainText(
    "host socket /run/grain/hostd.sock did not answer",
  );

  // Pending-restart and unrunnable links navigate to /next/settings/<section>#<field>
  // and scroll the target field into the visible stage.
  const k8sLink = page.getByTestId("pending-setting-link-k8sPrivileged");
  await expect(k8sLink).toHaveAttribute(
    "href",
    "/next/settings/sandboxes#k8sPrivileged",
  );
  await k8sLink.click();
  await expect(page).toHaveURL(/\/next\/settings\/sandboxes#k8sPrivileged$/);
  const k8sRow = page.locator("#k8sPrivileged");
  await expect(k8sRow).toBeVisible();
  await expect
    .poll(async () => {
      const box = await k8sRow.boundingBox();
      const vh = page.viewportSize()!.height;
      return Boolean(box && box.y >= 56 && box.y < vh);
    })
    .toBe(true);
});

test("Reboot host and Shut down host are gated on /api/config and ask via <g-confirm-dialog> @phone @dark", async ({
  page,
}) => {
  const reboots: string[] = [];
  const shutdowns: string[] = [];
  let failPower = true;

  await page.route("**/api/host/reboot", (route) => {
    reboots.push(route.request().method());
    if (failPower) {
      return route.fulfill({
        status: 500,
        json: { error: "sudo: reboot denied" },
      });
    }
    return route.fulfill({ status: 200, json: { ok: true } });
  });

  await page.route("**/api/host/shutdown", (route) => {
    shutdowns.push(route.request().method());
    if (failPower) {
      return route.fulfill({
        status: 500,
        json: { error: "sudo: poweroff denied" },
      });
    }
    return route.fulfill({ status: 200, json: { ok: true } });
  });

  // Demo server defaults rebootEnabled and shutdownEnabled to false.
  await page.goto("fleet?tab=controls");
  const rebootBtn = page.getByTestId("host-reboot-btn");
  const shutdownBtn = page.getByTestId("host-shutdown-btn");
  await expect(rebootBtn).toBeDisabled();
  await expect(shutdownBtn).toBeDisabled();

  // Enable both via /api/config overlay.
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: { ...body, rebootEnabled: true, shutdownEnabled: true },
    });
  });
  await page.reload();
  await expect(rebootBtn).toBeEnabled();
  await expect(shutdownBtn).toBeEnabled();

  const dialog = page.getByTestId("confirm-dialog");

  // Cancel and Escape send nothing.
  await rebootBtn.click();
  await expect(dialog).toContainText("Reboot the host machine?");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();

  await shutdownBtn.click();
  await expect(dialog).toContainText("Shut down the host machine?");
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  expect(reboots).toEqual([]);
  expect(shutdowns).toEqual([]);

  // Refused 500 surfaces failure and does not claim host is rebooting/shutting down.
  await rebootBtn.click();
  await dialog.getByRole("button", { name: "Reboot host" }).click();
  await expect(
    page.getByText("Reboot failed: sudo: reboot denied"),
  ).toBeVisible();
  await expect(page.getByTestId("fleet-daemon-card")).not.toContainText(
    "Rebooting host",
  );
  expect(reboots).toEqual(["POST"]);

  await shutdownBtn.click();
  await dialog.getByRole("button", { name: "Shut down host" }).click();
  await expect(
    page.getByText("Shutdown failed: sudo: poweroff denied"),
  ).toBeVisible();
  await expect(page.getByTestId("fleet-daemon-card")).not.toContainText(
    "Shutting down",
  );
  expect(shutdowns).toEqual(["POST"]);

  // Accepted 200 surfaces requested notice and status pill.
  failPower = false;
  await rebootBtn.click();
  await dialog.getByRole("button", { name: "Reboot host" }).click();
  await expect(page.getByTestId("host-power-requested-notice")).toContainText(
    "Reboot requested.",
  );
  await expect(page.getByTestId("fleet-daemon-card")).toContainText(
    "Rebooting host",
  );
  expect(reboots).toEqual(["POST", "POST"]);

  await shutdownBtn.click();
  await dialog.getByRole("button", { name: "Shut down host" }).click();
  await expect(page.getByTestId("host-power-requested-notice")).toContainText(
    "Shutdown requested.",
  );
  await expect(page.getByTestId("fleet-daemon-card")).toContainText(
    "Shutting down",
  );
  expect(shutdowns).toEqual(["POST", "POST"]);
});
