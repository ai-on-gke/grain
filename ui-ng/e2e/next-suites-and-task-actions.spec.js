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

// End-to-end scenarios for docs/test-plans/2026-11-13-afternoon.md §5:
//   1. /next/processes/suites rendering when a suite run ended before filing
//      pass 1 (pass: 0, tasks: null) alongside real runs (pass >= 1).
//   2. /next/ New-task sheet inline validation (missing title, missing
//      repository when defaultTarget is unset, missing custom branch name)
//      and viewer / repo-scoped !canFile() gating (including Ctrl+Enter).
//   3. /next/tasks batch Close confirmation ("Close 1 task?" / "Close 2 tasks?",
//      Keep, Escape keeping the inspector pane open, confirm), partial-failure
//      handling ("1 of 2 tasks failed: ...", honest snackbar, failed row kept
//      selected), and active suite task close pluralization.

import { test, expect } from "./fixtures.js";

async function apiJson(baseURL, code, method, urlPath, body) {
  const res = await fetch(`${baseURL}${urlPath}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${urlPath}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

async function closeQuietly(baseURL, code, id) {
  if (!id) return;
  await fetch(`${baseURL}/api/tasks/${id}/close`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ close_pull_request: false }),
  }).catch(() => {});
}

async function deleteQuietly(baseURL, code, urlPath) {
  await fetch(`${baseURL}${urlPath}`, {
    method: "DELETE",
    headers: { Authorization: `Bearer ${code}` },
  }).catch(() => {});
}

test(
  "/next/processes/suites renders pass: 0 / tasks: null suite runs beside real runs and pluralizes suite task close",
  { tag: ["@phone", "@dark"] },
  async ({ page, demoServer, accessCode, axe }) => {
    test.setTimeout(120_000);
    const taskIds = [];
    let suiteId = "";
    let templateId = "";

    try {
      const cfg = await apiJson(demoServer, accessCode, "GET", "/api/config");
      const repo =
        cfg.defaultTarget ||
        cfg.targetRepos?.[0] ||
        cfg.deployment?.targetRepos?.[0] ||
        "acme/widgets";

      const tmpl = await apiJson(
        demoServer,
        accessCode,
        "POST",
        "/api/templates",
        {
          name: `E2E audit template ${Date.now()}`,
          title: "E2E suite pass check",
          description: "Verify suite pass rendering",
        },
      );
      templateId = tmpl.id;

      const suite = await apiJson(
        demoServer,
        accessCode,
        "POST",
        "/api/suites",
        {
          name: `E2E audit suite ${Date.now()}`,
          mode: "count",
          count: 1,
          templateIds: [tmpl.id],
        },
      );
      suiteId = suite.id;

      const realRun = await apiJson(
        demoServer,
        accessCode,
        "POST",
        "/api/suite-runs",
        {
          suiteId: suite.id,
          repo,
          base: "main",
        },
      );
      for (const st of realRun.tasks || []) {
        taskIds.push(st.taskId);
      }

      const emptyRunId = 990001;
      await page.route(/\/api\/suite-runs(\?.*)?$/, async (route) => {
        if (route.request().method() !== "GET") {
          await route.continue();
          return;
        }
        const upstream = await route.fetch();
        const json = await upstream.json();
        const list = Array.isArray(json) ? json : json?.suiteRuns || [];
        const emptyPassRun = {
          id: emptyRunId,
          suiteId: suite.id,
          suiteName: suite.name,
          repo,
          base: "main",
          mode: "count",
          count: 2,
          maxPasses: 0,
          requireApproval: false,
          autoMerge: false,
          autoApproveProposals: false,
          status: "failed",
          pass: 0,
          createdAt: new Date(Date.now() + 1000).toISOString(),
          completedAt: new Date(Date.now() + 2000).toISOString(),
          error: "pass 1 could not be filed: template was deleted",
          tasks: null,
        };
        const merged = [emptyPassRun, ...list];
        await route.fulfill({
          response: upstream,
          json: Array.isArray(json) ? merged : { ...json, suiteRuns: merged },
        });
      });

      await page.goto("/next/processes/suites");
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page.getByTestId("suites-subview-runs").click();

      const emptyRow = page.getByTestId(`suite-run-row-${emptyRunId}`);
      const subsequentRow = page.getByTestId(`suite-run-row-${realRun.id}`);
      await expect(emptyRow).toBeVisible();
      await expect(subsequentRow).toBeVisible();
      await expect(emptyRow).toContainText("0 tasks");
      await expect(emptyRow).not.toContainText(/pass\s*0/i);
      await expect(subsequentRow).toContainText("Pass 1");

      await emptyRow.click();
      const inspector = page.getByTestId("suite-run-inspector");
      await expect(inspector).toBeVisible();
      await expect(inspector).toContainText("ended without filing a pass");
      await expect(inspector).toContainText(
        "pass 1 could not be filed: template was deleted",
      );
      await expect(page.locator('[data-testid^="suite-pass-"]')).toHaveCount(0);

      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
      expect(await axe()).toEqual([]);

      const backBtn = inspector.getByRole("button", { name: /Back/ });
      if ((await backBtn.count()) > 0 && (await backBtn.isVisible())) {
        await backBtn.click();
      }

      // Subsequent real run expands cleanly and cancelling its 1 task says "1 active suite task."
      await subsequentRow.click();
      await expect(inspector).toContainText(/pass\s*1/i);
      await expect(page.getByTestId("suite-pass-1")).toBeVisible();

      await page.getByTestId("suite-run-cancel-btn").click();
      await page.getByTestId("confirm-accept").click();
      await expect(page.getByTestId("snackbar")).toContainText(
        "Closed 1 active suite task.",
      );
    } finally {
      await page.unrouteAll({ behavior: "ignoreErrors" });
      for (const id of taskIds) {
        await closeQuietly(demoServer, accessCode, id);
      }
      if (suiteId) {
        await deleteQuietly(demoServer, accessCode, `/api/suites/${suiteId}`);
      }
      if (templateId) {
        await deleteQuietly(
          demoServer,
          accessCode,
          `/api/templates/${templateId}`,
        );
      }
    }
  },
);

test(
  "/next/ New-task sheet validates missing title, missing repository, and missing branch name inline and enforces viewer / !canFile() gating",
  { tag: ["@phone", "@dark"] },
  async ({ page, demoServer, accessCode, axe }) => {
    test.setTimeout(120_000);
    const createdIds = [];
    let configMode = "no-default-target";

    try {
      const baseCfg = await apiJson(
        demoServer,
        accessCode,
        "GET",
        "/api/config",
      );
      const targetRepo =
        baseCfg.defaultTarget ||
        baseCfg.targetRepos?.[0] ||
        baseCfg.deployment?.targetRepos?.[0] ||
        "acme/widgets";

      await page.route("**/api/config", async (route) => {
        const upstream = await route.fetch();
        const json = await upstream.json();
        if (configMode === "no-default-target") {
          delete json.defaultTarget;
        } else if (configMode === "viewer") {
          json.access = {
            role: "grain/viewer",
            roles: ["grain/viewer"],
            full: false,
            scopes: ["tasks:read"],
          };
        } else if (configMode === "repo-limited") {
          delete json.defaultTarget;
          json.access = {
            role: "ops/other-only",
            roles: ["ops/other-only"],
            full: false,
            scopes: ["tasks:read", "tasks:write"],
            repoLimits: { "tasks:write": ["other/allowed-only"] },
          };
        }
        await route.fulfill({ response: upstream, json });
      });

      const postAttempts = [];
      page.on("request", (req) => {
        if (req.method() === "POST" && req.url().endsWith("/api/tasks")) {
          postAttempts.push(req.postDataJSON());
        }
      });

      await page.goto("/next/inbox");
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page.locator("body").press("c");
      const sheet = page.getByTestId("new-task-sheet");
      await expect(sheet).toBeVisible();
      const sheetBox = await sheet.boundingBox();
      const sheetVpWidth = page.viewportSize()?.width ?? 1440;
      expect(sheetBox.x).toBeGreaterThanOrEqual(0);
      expect(sheetBox.x + sheetBox.width).toBeLessThanOrEqual(sheetVpWidth);

      // (a) Missing title and no template
      await page.getByTestId("new-task-submit").click();
      await expect(page.getByTestId("new-task-error")).toHaveText(
        "Give the task a title.",
      );
      await expect(page.getByTestId("nt-title")).toHaveAttribute(
        "aria-invalid",
        "true",
      );
      expect(postAttempts).toHaveLength(0);

      // (b) Missing repository when defaultTarget is unset
      await page
        .getByTestId("nt-title")
        .fill(`Walkthrough validation ${Date.now()}`);
      const repoBtn = page.getByTestId("nt-repo");
      await expect(repoBtn).toContainText("Select a repository…");
      await expect(repoBtn).not.toContainText("Deployment default target");
      await page.getByTestId("new-task-submit").click();
      await expect(page.getByTestId("new-task-error")).toHaveText(
        "Pick a repository, or choose No repository.",
      );
      await expect(repoBtn).toHaveClass(/is-invalid/);
      expect(postAttempts).toHaveLength(0);
      expect(await axe()).toEqual([]);

      // (c) Pick repository, enable custom branch with empty branch name
      await repoBtn.click();
      await page.getByTestId(`nt-repo-opt-${targetRepo}`).click();
      await expect(page.getByTestId("new-task-error")).toHaveCount(0);
      await page.getByTestId("nt-new-branch").check();
      await page.getByTestId("new-task-submit").click();
      await expect(page.getByTestId("new-task-error")).toHaveText(
        "Name the new branch, or untick the new-branch box.",
      );
      await expect(page.getByTestId("nt-new-branch-name")).toHaveAttribute(
        "aria-invalid",
        "true",
      );
      expect(postAttempts).toHaveLength(0);

      // (d) Complete branch name and file cleanly
      const branchName = `feat/e2e-5487-${Date.now()}`;
      await page.getByTestId("nt-new-branch-name").fill(branchName);
      const createdRespP = page.waitForResponse(
        (res) =>
          res.url().endsWith("/api/tasks") &&
          res.request().method() === "POST" &&
          res.status() < 300,
      );
      await page.getByTestId("new-task-submit").click();
      const created = await (await createdRespP).json();
      if (created?.id) createdIds.push(created.id);
      await expect(sheet).toHaveCount(0);

      // (e1) Pure viewer role disables New task controls and blocks `c`
      configMode = "viewer";
      await page.goto("/next/tasks");
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      const vpWidth = page.viewportSize()?.width ?? 1440;
      const fab = page.getByTestId(
        vpWidth < 600 ? "phone-new-task" : "rail-new-task",
      );
      await expect(fab).toBeDisabled();
      await page.locator("body").press("c");
      await expect(sheet).toHaveCount(0);

      // (e2) Repo-scoped caller inside sheet: !canFile() disables submit, shows banner, and blocks Ctrl+Enter
      configMode = "repo-limited";
      await page.goto("/next/tasks");
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page.locator("body").press("c");
      await expect(sheet).toBeVisible();
      await page.getByTestId("nt-repo").click();
      await page.getByTestId(`nt-repo-opt-${targetRepo}`).click();
      await page.getByTestId("nt-title").fill("Blocked by repoLimits");
      await expect(page.getByTestId("new-task-denied")).toContainText(
        "tasks:write",
      );
      await expect(page.getByTestId("new-task-submit")).toBeDisabled();
      const beforePosts = postAttempts.length;
      await page.getByTestId("nt-title").press("Control+Enter");
      await page.waitForTimeout(200);
      expect(postAttempts).toHaveLength(beforePosts);
    } finally {
      await page.unrouteAll({ behavior: "ignoreErrors" });
      for (const id of createdIds) {
        await closeQuietly(demoServer, accessCode, id);
      }
    }
  },
);

test(
  "/next/tasks batch Close pluralizes 1 vs 2+ tasks, cancels on Keep and Escape without closing the pane, and reports partial failures honestly",
  { tag: ["@phone", "@dark"] },
  async ({ page, demoServer, accessCode, axe }) => {
    test.setTimeout(120_000);
    const createdIds = [];

    try {
      const live1 = await apiJson(
        demoServer,
        accessCode,
        "POST",
        "/api/tasks",
        {
          title: `Batch close live A ${Date.now()}`,
          approved: true,
        },
      );
      const live2 = await apiJson(
        demoServer,
        accessCode,
        "POST",
        "/api/tasks",
        {
          title: `Batch close live B ${Date.now()}`,
          approved: true,
        },
      );
      createdIds.push(live1.id, live2.id);
      await apiJson(
        demoServer,
        accessCode,
        "POST",
        `/api/tasks/${live1.id}/messages`,
        { summary: "Confirm batch close A?", body: "Waiting on reply A." },
      );
      await apiJson(
        demoServer,
        accessCode,
        "POST",
        `/api/tasks/${live2.id}/messages`,
        { summary: "Confirm batch close B?", body: "Waiting on reply B." },
      );

      const q1 = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
        title: `Batch close partial ok ${Date.now()}`,
        approved: true,
      });
      const q2 = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
        title: `Batch close partial fail ${Date.now()}`,
        approved: true,
      });
      createdIds.push(q1.id, q2.id);

      const vpWidth = page.viewportSize()?.width ?? 1440;
      const askedUrl =
        vpWidth >= 600
          ? `/next/tasks?tab=asked&task=${live1.id}`
          : `/next/tasks?tab=asked`;
      await page.goto(askedUrl);
      await page.waitForSelector(".main-stage > *:not(router-outlet)");

      // 1 task selected -> "Close 1 task?", Escape cancels without closing desktop inspector pane
      await page
        .locator(`[data-testid="row-${live1.id}"] input[type="checkbox"]`)
        .check();
      await page.getByTestId("batch-close").click();
      const confirmSpan = page.getByTestId("close-confirm");
      await expect(confirmSpan).toContainText("Close 1 task?");
      const barBox = await page.getByTestId("batch-bar").boundingBox();
      expect(barBox.x).toBeGreaterThanOrEqual(0);
      expect(barBox.x + barBox.width).toBeLessThanOrEqual(vpWidth + 1);

      await page.keyboard.press("Escape");
      await expect(confirmSpan).toHaveCount(0);
      if (vpWidth >= 600) {
        await expect(page).toHaveURL(new RegExp(`task=${live1.id}`));
      }

      // Keep cancels cleanly
      await page.getByTestId("batch-close").click();
      await expect(confirmSpan).toBeVisible();
      await page.getByTestId("close-keep").click();
      await expect(confirmSpan).toHaveCount(0);

      // 2 tasks selected -> "Close 2 tasks?" -> confirm closes both with no Undo
      await page
        .locator(`[data-testid="row-${live2.id}"] input[type="checkbox"]`)
        .check();
      await page.getByTestId("batch-close").click();
      await expect(confirmSpan).toContainText("Close 2 tasks?");
      await page.getByTestId("close-yes").click();
      await expect(page.getByTestId("snackbar")).toContainText(
        "Closed 2 tasks",
      );
      await expect(page.getByTestId("snackbar-action")).toHaveCount(0);

      // Partial failure on Backlog: q1 closes, q2 fails with 500
      await page.route(`**/api/tasks/${q2.id}/close`, (route) =>
        route.fulfill({
          status: 500,
          contentType: "application/json",
          body: JSON.stringify({ error: "simulated close failure" }),
        }),
      );

      await page.goto("/next/tasks");
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page
        .locator(`[data-testid="row-${q1.id}"] input[type="checkbox"]`)
        .check();
      await page
        .locator(`[data-testid="row-${q2.id}"] input[type="checkbox"]`)
        .check();
      await page.getByTestId("batch-close").click();

      await expect(page.getByTestId("action-error")).toContainText(
        "1 of 2 tasks failed: simulated close failure",
      );
      await expect(page.getByTestId("snackbar")).toContainText(
        `Closed #${q1.id}`,
      );
      await expect(
        page.locator(`[data-testid="row-${q2.id}"] input[type="checkbox"]`),
      ).toBeChecked();
      expect(await axe()).toEqual([]);
    } finally {
      await page.unrouteAll({ behavior: "ignoreErrors" });
      for (const id of createdIds) {
        await closeQuietly(demoServer, accessCode, id);
      }
    }
  },
);
