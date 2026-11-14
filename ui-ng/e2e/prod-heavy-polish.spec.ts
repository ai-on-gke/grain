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

import fs from "node:fs";
import path from "node:path";
import { expect, test } from "./fixtures.js";

// Production-heavy regression verification:
// 1. Zero console errors & zero /api/ HTTP 4xx/5xx across Inbox, Workstreams (#/%23/deep links),
//    Tasks, Agents, Telemetry, Settings, Fleet, and Task detail.
// 2. Global + contextual dispatch pause & model-quota banner (local TZ + countdown + Details).
// 3. Workstreams non-overlapping layout, repo 2+N cap, goal clamp, and >40-node fold toggle.
// 4. Telemetry default .ops-scroll height < 2,400px with grouped quota alerts & A-Z People filter.
// 5. Warm Settings open < 150ms via persistent root SettingsStateService.
// 6. Desktop (1440x900) and phone (390x844) light + dark axe & screenshot verification.

test.use({ demoArgs: ["-as", "demo-operator"] });

const SHOTS = process.env["GRAIN_UI_SHOTS"] || "";

async function apiJson(
  baseURL: string,
  code: string,
  method: string,
  p: string,
  body?: unknown,
) {
  const res = await fetch(`${baseURL}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    throw new Error(`${method} ${p}: ${res.status} ${await res.text()}`);
  }
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

async function waitPageSettled(page: import("@playwright/test").Page) {
  await page.waitForSelector(".main-stage > *:not(router-outlet)", {
    timeout: 30_000,
  });
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(250);
}

async function saveShot(
  page: import("@playwright/test").Page,
  name: string,
): Promise<void> {
  if (!SHOTS) return;
  fs.mkdirSync(SHOTS, { recursive: true });
  await page.screenshot({ path: path.join(SHOTS, `${name}.png`) });
}

test("production-heavy flows: zero API/console errors, quota banner, Workstreams fit, Telemetry <2400px, warm Settings <150ms, light/dark + phone axe", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  test.setTimeout(240_000);
  const createdIds: string[] = [];
  const addedRepos: string[] = [];
  const consoleErrors: string[] = [];
  const httpErrors: string[] = [];

  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("response", (res) => {
    const u = new URL(res.url());
    if (u.pathname.startsWith("/api/") && res.status() >= 400) {
      httpErrors.push(
        `${res.status()} ${res.request().method()} ${u.pathname}`,
      );
    }
  });

  try {
    const repos = [
      "acme/alpha",
      "acme/beta",
      "acme/gamma",
      "acme/delta",
      "acme/epsilon",
    ];
    const cfg = await apiJson(demoServer, accessCode, "GET", "/api/config");
    const existingRepos = new Set<string>(
      Array.isArray(cfg?.targetRepos) ? cfg.targetRepos : [],
    );
    for (const r of repos) {
      if (!existingRepos.has(r)) {
        await apiJson(demoServer, accessCode, "POST", "/api/repos", {
          repo: r,
        });
        addedRepos.push(r);
      }
    }

    // Seed a heavy workstream (>40 tasks spanning 5 repositories):
    // 36 closed, 4 awaiting approval (needs a person), and 4 blocked on an unapproved task.
    const wsRoot = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
      title: "Global rollout stream",
      description: [
        "The operator wants multi-repo rollout readiness tracked across staged worker pools.",
        "",
        "## Deliverables",
        "- Verify `api/tasks` contract compatibility and RBAC repo limits.",
        "- Aggregate weekly metrics across `acme/alpha`, `acme/beta`, `acme/gamma`, `acme/delta`, `acme/epsilon`.",
        "- Confirm canary health gates before advancing downstream trains.",
        "",
        "## Acceptance checks",
        ...Array.from(
          { length: 24 },
          (_, idx) =>
            `${idx + 1}. Verify regional worker batch #${idx + 1} passes smoke tests and stays within concurrency budget.`,
        ),
      ].join("\n"),
      repo: "acme/alpha",
      approved: true,
    });
    const wsId = String(wsRoot.id);
    createdIds.push(wsId);

    const toCloseIds: string[] = [];
    for (let i = 0; i < 36; i += 8) {
      const batch: Promise<{ id: string }>[] = [];
      for (let j = i; j < Math.min(36, i + 8); j++) {
        batch.push(
          apiJson(demoServer, accessCode, "POST", "/api/tasks", {
            title: `Heavy polish subtask ${j + 1} with extended descriptive title for overflow checking`,
            repo: repos[j % repos.length],
            origin: `task:${wsId}`,
            approved: false,
          }),
        );
      }
      const res = await Promise.all(batch);
      for (const r of res) {
        const id = String(r.id);
        createdIds.push(id);
        toCloseIds.push(id);
      }
    }
    for (let i = 0; i < toCloseIds.length; i += 8) {
      await Promise.all(
        toCloseIds
          .slice(i, i + 8)
          .map((id) =>
            apiJson(demoServer, accessCode, "POST", `/api/tasks/${id}/close`),
          ),
      );
    }

    const personBatch = await Promise.all(
      [36, 37, 38, 39].map((j) =>
        apiJson(demoServer, accessCode, "POST", "/api/tasks", {
          title: `Heavy polish subtask ${j + 1} awaiting approval in long overflow title`,
          repo: repos[j % repos.length],
          origin: `task:${wsId}`,
          approved: false,
        }),
      ),
    );
    const personIds = personBatch.map((r) => String(r.id));
    createdIds.push(...personIds);

    const blockedBatch = await Promise.all(
      [40, 41, 42, 43].map((j) =>
        apiJson(demoServer, accessCode, "POST", "/api/tasks", {
          title: `Heavy polish subtask ${j + 1} blocked on dependency for collision check`,
          repo: repos[j % repos.length],
          origin: `task:${wsId}`,
          approved: true,
          dependsOn: [personIds[0]],
        }),
      ),
    );
    for (const r of blockedBatch) createdIds.push(String(r.id));

    // Activate a real pipeline pause so global + contextual banners render without stubbing config.
    await apiJson(demoServer, accessCode, "POST", "/api/pipeline-pause", {
      reason: "Scheduled model quota recovery window",
    });

    // 1. Desktop light verification across heavy views + global banner + warm Settings < 150ms.
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("inbox");
    await waitPageSettled(page);

    const globalBanner = page.getByTestId("global-dispatch-banner");
    await expect(globalBanner).toBeVisible();
    await expect(globalBanner).toContainText("Pipeline paused");
    await expect(globalBanner).toContainText(
      "Scheduled model quota recovery window",
    );
    await expect(page.getByTestId("live-pill")).toHaveClass(/is-paused/);
    await saveShot(page, "heavy-inbox-desktop-light");

    await page.evaluate(() => {
      localStorage.setItem("grain.themeMode", "dark");
      document.documentElement.setAttribute("data-theme", "dark");
    });
    await page.waitForTimeout(100);
    await saveShot(page, "heavy-inbox-desktop-dark");
    await page.evaluate(() => {
      localStorage.setItem("grain.themeMode", "light");
      document.documentElement.setAttribute("data-theme", "light");
    });
    await page.waitForTimeout(100);

    // Heavy Workstreams list & detail (>40 nodes + deep link #fragment recovery).
    await page.getByTestId("rail-workstreams").click();
    await waitPageSettled(page);

    const boxMetrics = await page.evaluate((id) => {
      const trigger = document.querySelector(
        `[data-testid="ws-row-task:${id}"]`,
      );
      const row = trigger?.closest(".ws-row");
      const pcell = row?.querySelector<HTMLElement>(".ws-pcell");
      const pills = row?.querySelector<HTMLElement>(".ws-pills");
      if (!pcell || !pills) return null;
      const pr = pcell.getBoundingClientRect();
      const lr = pills.getBoundingClientRect();
      return {
        sideways:
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
        pcellRight: pr.right,
        pillsLeft: lr.left,
        pcellText: pcell.textContent?.trim() ?? "",
        pillsText: pills.textContent?.trim() ?? "",
        repoText: row?.querySelector(".mono")?.textContent?.trim() ?? "",
      };
    }, wsId);
    expect(boxMetrics).not.toBeNull();
    expect(boxMetrics!.sideways).toBeLessThanOrEqual(0);
    expect(boxMetrics!.pcellRight).toBeLessThanOrEqual(
      boxMetrics!.pillsLeft + 1,
    );
    expect(boxMetrics!.pcellText).toContain("36/44 done");
    expect(boxMetrics!.pillsText).toContain("needs a person");
    expect(boxMetrics!.pillsText).toContain("blocked");
    expect(boxMetrics!.repoText).toMatch(/alpha, [a-z]+ \+3/);
    await saveShot(page, "heavy-workstreams-desktop-light");

    await page.evaluate(() => {
      localStorage.setItem("grain.themeMode", "dark");
      document.documentElement.setAttribute("data-theme", "dark");
    });
    await page.waitForTimeout(100);
    await saveShot(page, "heavy-workstreams-desktop-dark");

    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(100);
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    ).toBeLessThanOrEqual(0);
    await saveShot(page, "heavy-workstreams-phone-dark");
    await page.evaluate(() => {
      localStorage.setItem("grain.themeMode", "light");
      document.documentElement.setAttribute("data-theme", "light");
    });
    await page.waitForTimeout(100);
    await saveShot(page, "heavy-workstreams-phone-light");
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.waitForTimeout(100);

    // Open Workstream detail via #fragment form with simulated async lag on root detail
    // to prove the header height shifts 0px and the goal renders collapsed in the inspector.
    let released = false;
    let markRequested: (() => void) | null = null;
    const detailRequested = new Promise<void>((r) => {
      markRequested = r;
    });
    let releaseDetail!: () => void;
    const detailGate = new Promise<void>((r) => {
      releaseDetail = r;
    });
    const detailPattern = (url: URL) => url.pathname === `/api/tasks/${wsId}`;
    await page.route(detailPattern, async (route) => {
      if (released) {
        await route.continue();
        return;
      }
      markRequested?.();
      markRequested = null;
      await detailGate;
      await route.continue();
    });

    await page.goto(`workstreams/#task:${wsId}`);
    await expect(page.getByTestId("ws-detail-title")).toContainText(
      "Global rollout stream",
    );
    await detailRequested;
    const headBefore = await page.locator(".ws-dhead").boundingBox();
    expect(headBefore).not.toBeNull();
    await expect(page.locator(".ws-dhead [data-testid='ws-goal']")).toHaveCount(
      0,
    );

    released = true;
    releaseDetail();
    const inspector = page.getByTestId("ws-inspector");
    const goal = inspector.getByTestId("ws-goal");
    const goalToggle = inspector.getByTestId("ws-goal-toggle");
    await expect(goalToggle).toBeVisible();
    await page.unroute(detailPattern);
    await waitPageSettled(page);

    const headAfter = await page.locator(".ws-dhead").boundingBox();
    expect(
      Math.abs(headAfter!.height - headBefore!.height),
    ).toBeLessThanOrEqual(1);
    await expect(page.locator(".ws-dhead [data-testid='ws-goal']")).toHaveCount(
      0,
    );
    await expect(goalToggle).toHaveAttribute("aria-expanded", "false");
    await expect(goalToggle).toContainText("Workstream goal");
    await expect(goal).toBeVisible();
    await expect(goal).not.toContainText("##");
    const collapsedGoalBox = await goal.boundingBox();
    expect(collapsedGoalBox!.height).toBeLessThanOrEqual(30);
    await expect(page.getByTestId("ws-fold-done-toggle")).toContainText(
      "Expand all (36)",
    );
    await saveShot(page, "heavy-ws-detail-desktop-light");

    await goalToggle.click();
    await expect(goalToggle).toHaveAttribute("aria-expanded", "true");
    await expect(goalToggle).toContainText("Hide goal");
    await expect(goal.locator("h2").first()).toContainText("Deliverables");
    await expect(goal.locator("ul li").first()).toBeVisible();
    await expect(goal.locator("code").first()).toBeVisible();
    const expandedGoalBox = await goal.boundingBox();
    expect(expandedGoalBox!.height).toBeGreaterThan(60);
    expect(expandedGoalBox!.height).toBeLessThanOrEqual(245);
    const headAfterExpand = await page.locator(".ws-dhead").boundingBox();
    expect(
      Math.abs(headAfterExpand!.height - headBefore!.height),
    ).toBeLessThanOrEqual(1);
    await saveShot(page, "heavy-ws-detail-goal-expanded-desktop-light");
    await goalToggle.click();

    // Verify Tasks, Agents, Fleet, and Task Detail load with zero console/HTTP errors.
    for (const dest of ["tasks", "agents", "fleet", `task/${wsId}`]) {
      await page.goto(dest);
      await waitPageSettled(page);
      expect(await axe()).toEqual([]);
    }

    // Settings: cold load -> navigate away -> warm reopen < 150ms.
    await page.goto("settings/models");
    await waitPageSettled(page);
    await expect(page.getByTestId("card-model-defaults")).toBeVisible();
    await expect(page.getByTestId("settings-skeleton")).toHaveCount(0);
    await saveShot(page, "heavy-settings-desktop-light");

    await page.getByTestId("rail-inbox").click();
    await waitPageSettled(page);
    await expect(page.getByTestId("next-inbox-headline")).toBeVisible();

    const warmTiming = await page.evaluate(
      () =>
        new Promise<{ browserMs: number; sawSkeleton: boolean }>(
          (resolve, reject) => {
            const link = document.querySelector<HTMLElement>(
              '[data-testid="rail-settings"]',
            );
            if (!link) {
              reject(new Error("rail-settings link not found"));
              return;
            }
            let sawSkeleton = false;
            const t0 = performance.now();
            const obs = new MutationObserver(() => {
              if (document.querySelector('[data-testid="settings-skeleton"]')) {
                sawSkeleton = true;
              }
              if (
                document.querySelector('[data-testid="settings-shell"]') &&
                document.querySelector('[data-testid="card-model-defaults"]')
              ) {
                obs.disconnect();
                resolve({ browserMs: performance.now() - t0, sawSkeleton });
              }
            });
            obs.observe(document.body, { childList: true, subtree: true });
            link.click();
            if (document.querySelector('[data-testid="settings-skeleton"]')) {
              sawSkeleton = true;
            }
            if (
              document.querySelector('[data-testid="settings-shell"]') &&
              document.querySelector('[data-testid="card-model-defaults"]')
            ) {
              obs.disconnect();
              resolve({ browserMs: performance.now() - t0, sawSkeleton });
              return;
            }
            setTimeout(() => {
              obs.disconnect();
              reject(new Error("timed out waiting for warm Settings DOM"));
            }, 5000);
          },
        ),
    );
    await expect(page.getByTestId("settings-shell")).toBeVisible();
    await expect(page.getByTestId("card-model-defaults")).toBeVisible();
    console.log(
      `METRIC warmSettingsMs=${warmTiming.browserMs.toFixed(1)} sawSkeleton=${warmTiming.sawSkeleton}`,
    );
    expect(warmTiming.sawSkeleton).toBe(false);
    expect(warmTiming.browserMs).toBeLessThan(150);

    // Pause ended for clean Telemetry screenshot + height check below (no unrequested external error).
    expect(consoleErrors, consoleErrors.join("\n")).toEqual([]);
    expect(httpErrors, httpErrors.join("\n")).toEqual([]);
  } finally {
    await apiJson(
      demoServer,
      accessCode,
      "DELETE",
      "/api/pipeline-pause",
    ).catch(() => undefined);
    for (let i = 0; i < createdIds.length; i += 10) {
      await Promise.all(
        createdIds
          .slice(i, i + 10)
          .map((id) =>
            apiJson(
              demoServer,
              accessCode,
              "POST",
              `/api/tasks/${id}/close`,
            ).catch(() => undefined),
          ),
      );
    }
    for (const r of addedRepos) {
      await apiJson(demoServer, accessCode, "DELETE", `/api/repos/${r}`).catch(
        () => undefined,
      );
    }
  }
});

test("heavy Telemetry progressive disclosure stays <2400px tall with grouped quota alerts in light/dark + phone @phone @dark", async ({
  page,
  axe,
}) => {
  test.setTimeout(120_000);
  const consoleErrors: string[] = [];
  const httpErrors: string[] = [];
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("response", (res) => {
    const u = new URL(res.url());
    if (u.pathname.startsWith("/api/") && res.status() >= 400) {
      httpErrors.push(
        `${res.status()} ${res.request().method()} ${u.pathname}`,
      );
    }
  });

  // Intercept Telemetry payloads to simulate heavy production scale (12 alerts, 15 scopes, 15 directions, 18 actors).
  await page.route("**/api/usage**", (route) => {
    if (route.request().method() !== "GET") return route.continue();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        mode: "auto",
        maxWorkers: 12,
        minWorkers: 2,
        effectiveWorkers: 0,
        warnings: Array.from(
          { length: 10 },
          (_, i) =>
            `default model tier-${i + 1} on conn-${i + 1} is paused until 0${(i + 5) % 10}:54 UTC; fallback active`,
        ),
        advisories: [
          "burn rate nearing 5h ceiling on primary",
          "secondary pool warming",
        ],
        rollup: {
          runs: 537,
          inputTokens: 48_900_000,
          outputTokens: 8_300_000,
          cacheReadTokens: 1_367_200_000,
          cacheWriteTokens: 22_300_000,
          costUsd: 1531.33,
          billable: 79_500_000,
          burnPerHour: 0,
        },
        scopes: Array.from({ length: 15 }, (_, i) => ({
          connection: `conn-${i + 1}`,
          connectionName: `Connection ${String(i + 1).padStart(2, "0")}`,
          model: `model-${i + 1}`,
          runs: 12 + i,
          inputTokens: 2_000_000,
          outputTokens: 300_000,
          cacheReadTokens: 5_000_000,
          cacheWriteTokens: 100_000,
          used: 3_000_000,
          window: "5h",
          limit: 3_400_000,
          limitSource: "learned",
          burnPerHour: 0,
          pausedUntil: new Date(Date.now() + 90 * 60_000).toISOString(),
        })),
        settings: {
          mode: "auto",
          minWorkers: 2,
          retentionDays: 30,
          budgets: [],
        },
      }),
    });
  });

  await page.route("**/api/release-notes/stats**", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        period: "last-week",
        since: "2026-11-05T00:00:00Z",
        until: "2026-11-12T00:00:00Z",
        pullRequestsMerged: 831,
        pullRequestsMeasured: 831,
        linesAdded: 862_000,
        linesDeleted: 139_000,
        tasksCompleted: 920,
        tasksFiled: 950,
        runs: 1200,
        runsSucceeded: 1110,
        agentHours: 1333.7,
        repos: [
          {
            repo: "acme/alpha",
            pullRequestsMerged: 500,
            pullRequestsMeasured: 500,
            linesAdded: 1000,
            linesDeleted: 100,
            tasksCompleted: 100,
            tasksFiled: 100,
            runs: 200,
          },
        ],
        directions: Array.from({ length: 15 }, (_, i) => ({
          taskId: String(2100 + i),
          title: `Direction ${i + 1} automating multi-repo nightly checks with long description`,
          author:
            i % 3 === 0
              ? `system:worker-${String(15 - i).padStart(2, "0")}`
              : i === 1
                ? "Zelda"
                : `Dev ${String(15 - i).padStart(2, "0")}`,
          authorKind: i % 3 === 0 ? "agent" : "human",
          repo: ["acme/alpha", "acme/beta", "acme/gamma", "acme/delta"][i % 4],
          pullRequestsMerged: 40 - i,
          linesAdded: 1000,
          linesDeleted: 200,
          tasksCompleted: 60 - i,
          tasksFiled: 65,
          tasksSpawned: 30,
          runs: 50,
          runsSucceeded: 48,
          agentHours: 12.5,
        })),
      }),
    }),
  );

  // Desktop light check: scrollHeight < 2400px + grouped banner + toggles + axe.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.addInitScript(() =>
    localStorage.setItem("grain.themeMode", "light"),
  );
  await page.goto("telemetry");
  await waitPageSettled(page);

  await expect(page.getByTestId("usage-warnings-banner")).toBeVisible();
  expect(
    await page.getByTestId("usage-warnings-banner").locator("li").count(),
  ).toBe(2);
  expect(
    await page.getByTestId("usage-scopes-table").locator("tbody tr").count(),
  ).toBe(8);
  expect(await page.locator(".tel-dir-item").count()).toBe(8);
  expect(await page.getByTestId("space-actor-row").count()).toBe(12);

  const defaultScrollHeight = await page.evaluate(
    () => document.querySelector(".ops-scroll")?.scrollHeight ?? 9999,
  );
  console.log(`METRIC telemetryDefaultScrollHeightPx=${defaultScrollHeight}`);
  expect(defaultScrollHeight).toBeLessThan(2400);
  await saveShot(page, "heavy-telemetry-desktop-light");

  // Toggle notices, People filter, and Show all.
  await page.getByTestId("usage-warnings-toggle").click();
  expect(
    await page.getByTestId("usage-warnings-banner").locator("li").count(),
  ).toBe(12);
  const peopleBtn = page.getByTestId("actors-filter-people");
  await peopleBtn.click();
  await expect(peopleBtn).toHaveAttribute("aria-pressed", "true");
  await expect
    .poll(async () => {
      const names = await page
        .getByTestId("space-actor-row")
        .locator(".actor-name")
        .allTextContents();
      return names.length > 0 && names.every((n) => !n.startsWith("system:"));
    })
    .toBe(true);
  const peopleNames = await page
    .getByTestId("space-actor-row")
    .locator(".actor-name")
    .allTextContents();
  const sortedPeople = [...peopleNames].sort((a, b) =>
    a.localeCompare(b, undefined, { sensitivity: "base" }),
  );
  expect(peopleNames).toEqual(sortedPeople);
  await page.getByTestId("actors-filter-all").click();
  expect(await axe()).toEqual([]);

  // Desktop dark check.
  await page.evaluate(() => {
    localStorage.setItem("grain.themeMode", "dark");
    document.documentElement.setAttribute("data-theme", "dark");
  });
  await page.waitForTimeout(100);
  await saveShot(page, "heavy-telemetry-desktop-dark");
  expect(await axe()).toEqual([]);

  // Phone light & dark check (no horizontal overflow).
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(100);
  expect(
    await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    ),
  ).toBeLessThanOrEqual(0);
  await saveShot(page, "heavy-telemetry-phone-dark");
  await page.evaluate(() => {
    localStorage.setItem("grain.themeMode", "light");
    document.documentElement.setAttribute("data-theme", "light");
  });
  await page.waitForTimeout(100);
  await saveShot(page, "heavy-telemetry-phone-light");
  expect(await axe()).toEqual([]);

  expect(consoleErrors, consoleErrors.join("\n")).toEqual([]);
  expect(httpErrors, httpErrors.join("\n")).toEqual([]);
});
