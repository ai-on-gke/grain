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
