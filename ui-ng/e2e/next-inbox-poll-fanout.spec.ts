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

// Per-poll request fan-out on /next/inbox and /next/tasks (grain task 5880):
//
// 1. Queue rows needing detail summaries hydrate in a single batched
//    GET /api/tasks/summaries read rather than one GET /api/tasks/{id} per row.
// 2. Subsequent quiet poll cycles do not refetch unchanged task details or
//    unchanged batch summaries, so per-poll request count drops sharply from
//    cold load to a conditional GET /api/tasks check.
// 3. When the sign-in proxy answers 503 verifier_unavailable, secondary polls
//    back off and split-pane views never put up duplicate error banners.
test.use({ demoArgs: ["-human-review", "-sensitive-hold", "-stalled-tasks"] });

async function fileProposal(
  baseURL: string,
  code: string,
  title: string,
): Promise<string> {
  const res = await fetch(`${baseURL}/api/tasks`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ title, approved: false }),
  });
  if (!res.ok) {
    throw new Error(`POST /api/tasks: ${res.status} ${await res.text()}`);
  }
  return ((await res.json()) as { id: string }).id;
}

async function closeQuietly(
  baseURL: string,
  code: string,
  id: string,
): Promise<void> {
  if (!id) return;
  await fetch(`${baseURL}/api/tasks/${id}/close`, {
    method: "POST",
    headers: { Authorization: `Bearer ${code}` },
  }).catch(() => {});
}

test("batches Inbox row hydration on cold load and drops per-poll requests on quiet cycles", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  const filedIds: string[] = [];
  try {
    for (let i = 1; i <= 3; i++) {
      filedIds.push(
        await fileProposal(
          demoServer,
          accessCode,
          `Poll fan-out proposal ${i} (${Date.now()})`,
        ),
      );
    }

    const recorded: { method: string; path: string; search: string }[] = [];
    page.on("request", (req) => {
      const u = new URL(req.url());
      if (!u.pathname.startsWith("/api/")) return;
      if (u.pathname === "/api/events") return;
      recorded.push({
        method: req.method(),
        path: u.pathname,
        search: u.search,
      });
    });

    await page.goto("/next/inbox");
    const rows = page.getByTestId("queue-row");
    await expect
      .poll(() => rows.count(), { timeout: 20_000 })
      .toBeGreaterThanOrEqual(5);

    // Let initial inspector detail, diff preview, and batched summaries settle.
    await page.waitForTimeout(800);

    const coldRequests = [...recorded];
    const coldSingleSummaries = coldRequests.filter(
      (r) =>
        /^\/api\/tasks\/[^/]+$/.test(r.path) &&
        r.path !== "/api/tasks/summaries" &&
        r.search.includes("comments_limit=1"),
    );
    const coldBatchSummaries = coldRequests.filter(
      (r) => r.path === "/api/tasks/summaries",
    );
    const coldFullDetails = coldRequests.filter(
      (r) =>
        /^\/api\/tasks\/[^/]+$/.test(r.path) &&
        r.path !== "/api/tasks/summaries" &&
        !r.search.includes("comments_limit=1"),
    );

    // Zero per-row summary fan-out; at most 1 batched summary read and 1
    // selected-row detail read using the shared conditional DETAIL_QUERY URL.
    expect(coldSingleSummaries).toEqual([]);
    expect(coldBatchSummaries.length).toBeLessThanOrEqual(1);
    expect(coldFullDetails.length).toBeLessThanOrEqual(1);
    if (coldFullDetails.length === 1) {
      expect(coldFullDetails[0].search).toBe(
        "?comments_limit=100&attempts_limit=20",
      );
    }

    // Observe two subsequent open-task poll cycles while nothing mutates.
    recorded.length = 0;
    for (let cycle = 0; cycle < 2; cycle++) {
      await page.waitForResponse(
        (res) =>
          new URL(res.url()).pathname === "/api/tasks" &&
          res.request().method() === "GET" &&
          (res.status() === 200 || res.status() === 304),
        { timeout: 25_000 },
      );
    }
    await page.waitForTimeout(300);

    const pollRequests = [...recorded];
    const perPollCount = pollRequests.length / 2;
    expect(perPollCount).toBeLessThan(coldRequests.length);
    expect(
      pollRequests.filter((r) => r.path === "/api/tasks/summaries"),
    ).toEqual([]);
    expect(
      pollRequests.filter(
        (r) =>
          /^\/api\/tasks\/[^/]+$/.test(r.path) &&
          r.path !== "/api/tasks/summaries",
      ),
    ).toEqual([]);

    expect(await axe()).toEqual([]);
  } finally {
    for (const id of filedIds) {
      await closeQuietly(demoServer, accessCode, id);
    }
  }
});

test("skips unchanged task detail polls on /next/tasks and shows one sustained verifier banner", async ({
  page,
  context,
  axe,
}) => {
  test.setTimeout(120_000);
  const detailReads: string[] = [];
  page.on("request", (req) => {
    const u = new URL(req.url());
    if (
      req.method() === "GET" &&
      /^\/api\/tasks\/\d+$/.test(u.pathname) &&
      u.search.includes("comments_limit=100")
    ) {
      detailReads.push(`${u.pathname}${u.search}`);
    }
  });

  await page.goto("/next/tasks");
  await expect
    .poll(() => detailReads.length, { timeout: 20_000 })
    .toBeGreaterThanOrEqual(1);
  const initialDetailReads = detailReads.length;

  // Wait across two open-task feed polls: the selected pane's row has not
  // moved in GET /api/tasks, so TaskStore makes zero detail refetches.
  for (let i = 0; i < 2; i++) {
    await page.waitForResponse(
      (res) =>
        new URL(res.url()).pathname === "/api/tasks" &&
        res.request().method() === "GET" &&
        (res.status() === 200 || res.status() === 304),
      { timeout: 25_000 },
    );
  }
  await page.waitForTimeout(300);
  expect(detailReads.length).toBe(initialDetailReads);

  // Now simulate transient verifier_unavailable 503s with distinct refs across
  // every /api/ route: neither the list pane nor the detail pane puts up a
  // duplicate alert banner during the transient window.
  let seq = 0;
  await context.route("**/api/**", async (route) => {
    const u = new URL(route.request().url());
    if (u.pathname === "/api/events") return route.continue();
    const ref = `ref${++seq}`;
    await route
      .fulfill({
        status: 503,
        headers: { "Retry-After": "1" },
        contentType: "application/json",
        body: JSON.stringify({
          code: "verifier_unavailable",
          error: `sign-in verification could not check your identity right now; retrying shortly (ref ${ref})`,
          retryAfter: 1,
          ref,
        }),
      })
      .catch(() => {});
  });

  try {
    await expect.poll(() => seq, { timeout: 15_000 }).toBeGreaterThanOrEqual(2);
    await expect(page.locator('[role="alert"]')).toHaveCount(0);
    await expect(page.getByTestId("verifier-trouble-banner")).toHaveCount(0);

    // Past VERIFIER_SUSTAINED_MS (30s), exactly one shell-level warning banner
    // appears across both panes, fits cleanly inside <main>, and clears on recovery.
    const banner = page.getByTestId("verifier-trouble-banner");
    await expect(banner).toHaveCount(1, { timeout: 35_000 });
    await expect(banner).toContainText(
      /Sign-in verification has been temporarily unable to check your identity for \d+s — retrying automatically\./,
    );
    await expect(page.locator('[role="alert"]')).toHaveCount(0);

    const mainBox = await page.locator("main#main").boundingBox();
    const bannerBox = await banner.boundingBox();
    expect(mainBox).not.toBeNull();
    expect(bannerBox).not.toBeNull();
    expect(bannerBox!.x).toBeGreaterThanOrEqual(mainBox!.x - 1);
    expect(bannerBox!.x + bannerBox!.width).toBeLessThanOrEqual(
      mainBox!.x + mainBox!.width + 1,
    );
    expect(bannerBox!.height).toBeGreaterThanOrEqual(28);
    expect(await axe()).toEqual([]);
  } finally {
    await context.unroute("**/api/**");
  }

  await expect(page.getByTestId("verifier-trouble-banner")).toHaveCount(0, {
    timeout: 70_000,
  });
});
