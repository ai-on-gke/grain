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

import { expect, test } from "./fixtures.js";

// A standing session's run stays open across messages while its agent
// waits in the chat tool, and a run keeps the grants it started with
// (ConsoleSessionPage.jsx, liveRunKey). So a change saved while a run is
// open is announced as pending, with an "Apply now" that stops the run.
//
// grain demo's director is never mid-run, so this answers its task's
// detail as running with an open attempt, and answers the save and the
// cancel itself: the worker's demo server is shared, and nothing here
// may leave the director's grants or its task changed for the next spec.

const SIZES = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

async function directorTaskID(request) {
  const response = await request.get("/api/console-sessions");
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  return body.sessions.find((s) => s.id === "director")?.taskId;
}

async function fakeLiveRun(page, taskID) {
  await page.route(new RegExp(`/api/tasks/${taskID}$`), async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const response = await route.fetch({ headers });
    const body = await response.json();
    await route.fulfill({
      response,
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        ...body,
        state: "running",
        activityBySetup: false,
        attempts: [
          ...(body.attempts || []).map((a) => ({
            ...a,
            finishedAt: a.finishedAt || "2026-09-08T10:05:00Z",
          })),
          { number: 99, startedAt: "2026-09-08T11:00:00Z" },
        ],
      }),
    });
  });
  const cancels = [];
  await page.route(/\/api\/console-sessions\/director(\/cancel)?$/, (route) => {
    const method = route.request().method();
    if (method === "GET") return route.fallback();
    if (route.request().url().endsWith("/cancel")) cancels.push(method);
    return route.fulfill({ status: 200, json: {} });
  });
  return cancels;
}

for (const [name, size] of Object.entries(SIZES)) {
  test(`offers to apply a grant saved during a live run (${name})`, async ({
    page,
    request,
  }) => {
    const taskID = await directorTaskID(request);
    expect(taskID).toBeTruthy();
    const cancels = await fakeLiveRun(page, taskID);
    await page.setViewportSize(size);
    await page.goto("/session/director");

    await page.locator("#director-capabilities-header").click();
    const panel = page.locator("#director-capabilities-content");
    await expect(panel).toContainText("A run is open on this session now");

    await panel.getByRole("checkbox").first().click();
    const banner = page.locator(".console-session-pending");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("does not have it");
    const apply = banner.getByRole("button", { name: "Apply now" });
    await expect(apply).toBeVisible();

    // The action sits in the banner's own right-hand slot, inside it,
    // and the banner fits the page rather than running it sideways.
    const bannerBox = await banner.boundingBox();
    const applyBox = await apply.boundingBox();
    expect(applyBox.x).toBeGreaterThanOrEqual(bannerBox.x);
    expect(applyBox.x + applyBox.width).toBeLessThanOrEqual(
      bannerBox.x + bannerBox.width + 0.5,
    );
    expect(applyBox.x).toBeGreaterThan(bannerBox.x + bannerBox.width / 2);
    expect(bannerBox.x + bannerBox.width).toBeLessThanOrEqual(size.width);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    // Above the panel, so closing the panel does not hide it.
    expect(bannerBox.y).toBeLessThan((await panel.boundingBox()).y);

    await apply.click();
    await expect.poll(() => cancels.length).toBe(1);
    await expect(banner).toHaveCount(0);
  });
}

test("active session parameters and permissions remain editable in ConsoleSessionPage and DetailOverlay when configRepoOnly is set", async ({
  page,
  request,
}) => {
  const taskID = await directorTaskID(request);
  expect(taskID).toBeTruthy();
  const initialSessions = (
    await (await request.get("/api/console-sessions")).json()
  ).sessions;
  const initialDirector = initialSessions.find((s) => s.id === "director");
  const createRes = await request.post("/api/tasks", {
    data: {
      title: "Interactive session parameter test",
      body: "Verify session parameter controls in DetailOverlay.",
      interactive: true,
    },
  });
  expect(createRes.ok()).toBe(true);
  const chatTask = await createRes.json();

  await page.route("**/api/config", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({
      response,
      json: { ...body, configRepoOnly: true },
    });
  });

  try {
    await page.setViewportSize(SIZES.desktop);
    await page.goto("/session/director");
    await page.locator("#director-capabilities-header").click();
    const panel = page.locator("#director-capabilities-content");
    await expect(panel).toBeVisible();

    const netSelect = panel.locator('select[name="networkAccess"]');
    await expect(netSelect).toBeEnabled();
    await netSelect.selectOption("none");
    await expect
      .poll(async () => {
        const { sessions } = await (
          await request.get("/api/console-sessions")
        ).json();
        return sessions.find((s) => s.id === "director")?.networkAccess;
      })
      .toBe("none");

    // Check task detail sidebar for the interactive session task
    await page.goto(`/tasks/${chatTask.id}`);
    const fieldset = page.locator("fieldset", {
      hasText: "Session parameters",
    });
    await expect(fieldset).toBeVisible();
    const sidebarNet = fieldset.locator('select[name="networkAccess"]');
    await expect(sidebarNet).toBeEnabled();
    await sidebarNet.selectOption("none");
    await expect
      .poll(async () => {
        const updated = await (
          await request.get(`/api/tasks/${chatTask.id}`)
        ).json();
        return updated.networkAccess;
      })
      .toBe("none");

    for (const vp of [SIZES.desktop, SIZES.phone]) {
      await page.setViewportSize(vp);
      const fsBox = await fieldset.boundingBox();
      const netBox = await sidebarNet.boundingBox();
      expect(netBox.x).toBeGreaterThanOrEqual(fsBox.x);
      expect(netBox.x + netBox.width).toBeLessThanOrEqual(
        fsBox.x + fsBox.width + 1,
      );
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);
    }
  } finally {
    await request
      .post(`/api/tasks/${chatTask.id}/close`, {
        data: { reason: "test cleanup" },
      })
      .catch(() => {});
    if (initialDirector) {
      await request.put("/api/console-sessions/director", {
        data: {
          capabilities: initialDirector.capabilities,
          networkAccess: initialDirector.networkAccess || "open",
          reads: initialDirector.reads || [],
          writes: initialDirector.writes || [],
          model: initialDirector.model || "",
          tasksApproved: Boolean(initialDirector.tasksApproved),
        },
      });
    }
  }
});
