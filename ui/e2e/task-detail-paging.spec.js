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

// Task detail paging, "Show earlier", and summarised comments collapsed under
// the compaction handover (design/design/durable-tasks.design.md#DT-15).

test.setTimeout(120_000);

test("task page loads 50 comments initially, fetches earlier pages via comments_before, and collapses compacted comments under the handover", async ({
  page,
  request,
}) => {
  const filed = await request.post("/api/tasks", {
    data: {
      title: "Paged compacted task",
      body: "Verify bounded task detail loading and compaction disclosure.",
      approved: false,
    },
  });
  expect(filed.ok()).toBe(true);
  const task = await filed.json();

  for (let i = 0; i < 60; i++) {
    const res = await request.post(`/api/tasks/${task.id}/comments`, {
      data: { body: `Historical note #${i} on task ${task.id}.` },
    });
    expect(res.ok()).toBe(true);
  }

  const compacted = await request.post(`/api/tasks/${task.id}/compact`, {
    data: {
      handover:
        "Standing handover folding the first sixty notes into one summary.",
    },
  });
  expect(compacted.ok()).toBe(true);

  for (let i = 0; i < 5; i++) {
    const res = await request.post(`/api/tasks/${task.id}/comments`, {
      data: { body: `Post-compaction update #${i}.` },
    });
    expect(res.ok()).toBe(true);
  }

  const detailQueries = [];
  page.on("request", (req) => {
    const u = new URL(req.url());
    if (u.pathname === `/api/tasks/${task.id}` && req.method() === "GET") {
      detailQueries.push(u.search);
    }
  });

  // Total comments = 60 + 1 handover + 5 post-compaction = 66 comments.
  // First load gets newest 50 (16 older remaining; 44 of the loaded 50 are <= compactedThrough).
  await page.goto(`/tasks/${task.id}`);
  await expect(page.getByText("Post-compaction update #4.")).toBeVisible();
  await expect(
    page.getByText(
      "Standing handover folding the first sixty notes into one summary.",
    ),
  ).toBeVisible();
  expect(detailQueries[0]).toBe("?comments_limit=50&attempts_limit=20");

  // Summarised comments on the loaded page are folded inside .timeline-summarised disclosures.
  const disclosures = page.locator(".timeline-summarised");
  await expect(disclosures).toHaveCount(44);
  await expect(disclosures.first()).not.toHaveAttribute("open", "");
  await expect(page.getByText("Historical note #0")).toHaveCount(0);

  // Layout check at desktop and phone widths: disclosures and Show earlier sit
  // cleanly within the timeline without horizontal overflow.
  const earlierBtn = page.getByRole("button", {
    name: "Show earlier (16 comments)",
  });
  await expect(earlierBtn).toBeVisible();

  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    const timelineBox = await page.locator(".timeline").boundingBox();
    const disclosureBox = await disclosures.first().boundingBox();
    const earlierBox = await earlierBtn.boundingBox();
    expect(timelineBox).not.toBeNull();
    expect(disclosureBox).not.toBeNull();
    expect(earlierBox).not.toBeNull();
    expect(disclosureBox.x).toBeGreaterThanOrEqual(timelineBox.x - 1);
    expect(disclosureBox.x + disclosureBox.width).toBeLessThanOrEqual(
      timelineBox.x + timelineBox.width + 1,
    );
    expect(earlierBox.x).toBeGreaterThanOrEqual(timelineBox.x - 1);
    expect(earlierBox.x + earlierBox.width).toBeLessThanOrEqual(
      timelineBox.x + timelineBox.width + 1,
    );
  }

  await page.setViewportSize({ width: 1280, height: 800 });
  await earlierBtn.click();
  await expect(disclosures).toHaveCount(60);
  await expect(page.getByRole("button", { name: /Show earlier/ })).toHaveCount(
    0,
  );
  expect(detailQueries.some((q) => q.includes("comments_before="))).toBe(true);

  // Deep-linking into a fresh load fetches earlier pages, opens that summarised
  // comment's disclosure, and scrolls directly to it.
  const full = await (await request.get(`/api/tasks/${task.id}`)).json();
  const earliest = full.comments[0];

  await page.goto(`/tasks/${task.id}#comment=${earliest.id}`);
  const earliestEntry = page.locator(`[data-comment-id="${earliest.id}"]`);
  await expect(earliestEntry).toContainText("Historical note #0");
  await expect(earliestEntry).toBeInViewport();
  await expect(
    earliestEntry.locator("details.timeline-summarised"),
  ).toHaveAttribute("open", "");
});
