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

// Boards, dashboards and the rail when GET /api/tasks fails (task 3352,
// from the 2026-11-06 saved boards walkthrough): once the five-second
// error banner fades, views must still say the tasks could not be loaded
// rather than claiming the deployment has none, and recover when a later
// fetch or Retry succeeds.

import AxeBuilder from "@axe-core/playwright";
import { test, expect } from "./fixtures.js";

const seededBoards = [];

test.afterEach(async ({ page }) => {
  while (seededBoards.length > 0) {
    const id = seededBoards.pop();
    await page.request.delete(`/api/boards/${id}`).catch(() => {});
  }
});

async function seedBoard(page, data) {
  const res = await page.request.post("/api/boards", { data });
  expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
  const created = await res.json();
  seededBoards.push(created.id);
  return created;
}

test("boards, dashboards and the rail report a failed /api/tasks load instead of claiming no tasks, then recover on retry", async ({
  page,
}) => {
  const dashboard = await seedBoard(page, {
    name: "Operations Failure Check",
    kind: "panels",
    panels: [
      { id: "p-states", kind: "states", title: "States", span: 12 },
      { id: "p-list", kind: "list", title: "Queue", span: 6 },
      {
        id: "p-gantt",
        kind: "gantt",
        title: "Recent timeline",
        span: 6,
        view: { range: "7d", lanes: "state" },
      },
      {
        id: "p-metrics",
        kind: "metrics",
        title: "Throughput",
        span: 12,
        view: { range: "7d" },
      },
    ],
  });
  const listBoard = await seedBoard(page, {
    name: "Queue List Board",
    kind: "list",
  });

  const boardsRes = await page.request.get("/api/boards");
  expect(boardsRes.ok()).toBe(true);
  const boards = await boardsRes.json();
  const byKind = Object.fromEntries(boards.map((b) => [b.kind, b.id]));

  await page.route(/\/api\/tasks(\?.*)?$/, (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "synthetic tasks 500" }),
    }),
  );

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`/boards/${dashboard.id}`);

  const expectedError = "The tasks could not be loaded (synthetic tasks 500).";
  await expect(page.getByText(expectedError)).toHaveCount(3);

  // Wait for the transient 5s error banner to clear so the standing state
  // on the page and rail is what remains.
  await expect(page.locator(".error-banner")).toHaveCount(0, {
    timeout: 10_000,
  });

  await expect(page.getByText("Nothing here right now.")).toHaveCount(0);
  await expect(page.getByText("No tasks yet.")).toHaveCount(0);
  await expect(page.locator(".panel-state")).toHaveCount(0);
  await expect(page.locator(".panel-header .count")).toHaveCount(0);
  await expect(page.getByText(expectedError)).toHaveCount(3);
  await expect(page.getByRole("button", { name: "Retry" })).toHaveCount(3);
  // Metrics panel still loads from /api/metrics independently.
  await expect(page.locator(".panel-metrics")).toBeVisible();

  const railTasks = page.locator("aside").getByRole("button", {
    name: /^Tasks/,
  });
  await expect(railTasks).toContainText("could not be loaded");
  await expect(railTasks).not.toContainText("0");

  // Kanban, timeline, tree, list board and / all report the load failure
  // instead of an empty state.
  for (const path of [
    `/boards/${byKind.kanban}`,
    `/boards/${byKind.gantt}`,
    `/boards/${byKind.tree}`,
    `/boards/${listBoard.id}`,
    "/",
  ]) {
    await page.goto(path);
    await expect(page.getByText(expectedError)).toBeVisible();
    await expect(page.getByRole("button", { name: "Retry" })).toBeVisible();
    await expect(page.getByText("Nothing here right now.")).toHaveCount(0);
    await expect(page.getByText("No tasks yet.")).toHaveCount(0);
    await expect(
      page.getByText("No tasks in any of these columns."),
    ).toHaveCount(0);
    await expect(page.getByText("Nothing here.")).toHaveCount(0);
    await expect(page.getByText("No tasks in this state.")).toHaveCount(0);
    await expect(
      page.locator("aside").getByRole("button", { name: /^Tasks/ }),
    ).toContainText("could not be loaded");
  }

  // Check bounding boxes and WCAG AA contrast on dashboard at desktop
  // (1440x900) and phone (390x844) in both light and dark themes.
  for (const scheme of ["light", "dark"]) {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.emulateMedia({ colorScheme: scheme });
    await page.evaluate(
      (t) => localStorage.setItem("grain.themeMode", t),
      scheme,
    );
    await page.goto(`/boards/${dashboard.id}`);
    await expect(page.getByText(expectedError)).toHaveCount(3);

    const axe = await new AxeBuilder({ page })
      .withRules(["color-contrast"])
      .include(".tasks-load-error")
      .include("aside")
      .analyze();
    expect(axe.violations, `contrast violations in ${scheme}`).toEqual([]);

    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await expect(page.locator(":root")).toHaveAttribute(
        "data-phone",
        viewport.width < 600 ? "true" : "false",
      );
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(
        overflow,
        `horizontal overflow at ${viewport.width}px (${scheme})`,
      ).toBeLessThanOrEqual(0);
      const boxes = await page.locator(".tasks-load-error").evaluateAll((els) =>
        els.map((el) => {
          const rect = el.getBoundingClientRect();
          const parent = el.parentElement.getBoundingClientRect();
          return {
            width: rect.width,
            height: rect.height,
            insideLeft: rect.left >= parent.left - 1,
            insideRight: rect.right <= parent.right + 1,
          };
        }),
      );
      expect(boxes).toHaveLength(3);
      for (const box of boxes) {
        expect(box.width).toBeGreaterThan(100);
        expect(box.height).toBeGreaterThan(20);
        expect(box.insideLeft).toBe(true);
        expect(box.insideRight).toBe(true);
      }
    }
  }

  // Lift the 500 stub: clicking Retry loads the real tasks onto the
  // dashboard and restores the rail count.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.unroute(/\/api\/tasks(\?.*)?$/);
  await page.getByRole("button", { name: "Retry" }).first().click();

  await expect(page.getByText(expectedError)).toHaveCount(0);
  await expect(page.locator(".panel-state").first()).toBeVisible();
  await expect(page.locator(".panel-body .task-row").first()).toBeVisible();
  await expect(railTasks).not.toContainText("could not be loaded");
  const railText = await railTasks.textContent();
  expect(railText).toMatch(/Tasks\s*[1-9]\d*/);
});
