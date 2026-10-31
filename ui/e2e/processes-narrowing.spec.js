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

// The Processes page narrowed with the task list's own toolbar
// (processFilters.js, grain/task-429): source, type, name and enabled
// as conditions, the order by next run, all of it in the URL -- so the
// view survives a reload and Back, and the same query asks GET
// /api/schedules the same question.

const SCHEDULE_ROW = '.process-row[data-kind="schedule"]';

async function addCondition(page, attribute) {
  await page.getByRole("button", { name: /^(Filter by…|Add filter)$/ }).click();
  await page.getByRole("menuitem", { name: attribute, exact: true }).click();
  await expect(page.getByRole("menu")).not.toBeVisible();
}

// pick chooses one value in the newest condition's menu.
async function pick(page, value) {
  await page
    .getByLabel(/^Value for filter \d+$/)
    .last()
    .click();
  await page.getByRole("option", { name: value, exact: true }).click();
  await page.keyboard.press("Escape");
}

test("narrows schedules by source, type, name and enabled, and keeps it in the URL", async ({
  page,
}) => {
  const stamp = Date.now();
  const paused = `Candidate cut ${stamp}`;
  const running = `Weekly sweep ${stamp}`;
  const created = [];
  const create = async (title) => {
    const res = await page.request.post("/api/schedules", {
      data: {
        title,
        repo: "acme/widgets",
        overlap: "skip",
        recurrence: {
          kind: "weekly",
          weekday: "wednesday",
          timeOfDay: "12:00",
        },
      },
    });
    expect(res.ok(), await res.text()).toBe(true);
    const s = await res.json();
    created.push(s.id);
    return s;
  };
  try {
    const p = await create(paused);
    await create(running);
    const res = await page.request.patch(`/api/schedules/${p.id}`, {
      data: { enabled: false },
    });
    expect(res.ok(), await res.text()).toBe(true);

    await page.goto("/processes?kind=schedule");
    const rowNamed = (name) => page.locator(SCHEDULE_ROW, { hasText: name });
    await expect(rowNamed(paused)).toBeVisible();
    await expect(rowNamed(running)).toBeVisible();
    // The demo's repository-declared schedule (pkg/demo).
    const declared = rowNamed("Nightly Repo Maintenance Sweep");
    await expect(declared).toBeVisible();

    await page
      .getByTitle("Add and edit the conditions this list is narrowed by")
      .click();

    // Source: created here, not declared by a repository.
    await addCondition(page, "Source");
    await pick(page, "This deployment (UI, API or config repo)");
    await expect(declared).toHaveCount(0);
    await expect(rowNamed(paused)).toBeVisible();

    // Type: one that files its own task.
    await addCondition(page, "Type");
    await pick(page, "Files its own task");
    await expect(rowNamed(paused)).toBeVisible();
    await expect(rowNamed(running)).toBeVisible();

    // Name contains "candidate".
    await addCondition(page, "Name");
    await page
      .getByLabel(/^Value for filter \d+$/)
      .last()
      .fill("candidate");
    await expect(rowNamed(running)).toHaveCount(0);
    await expect(rowNamed(paused)).toBeVisible();

    // Enabled is No.
    await addCondition(page, "Enabled");
    await pick(page, "No");
    await expect(rowNamed(paused)).toBeVisible();
    await expect(page.locator(SCHEDULE_ROW)).toHaveCount(1);

    // Sorted by next run, latest first.
    await page.getByTitle("Choose the order this list is shown in").click();
    await page.getByLabel("Direction for sort 1").click();
    await page.getByRole("option", { name: "Latest first" }).click();

    const narrowed = [
      "kind=schedule",
      "sort=next:desc",
      "f=source:any:deployment",
      "f=type:any:task",
      "f=name:has:candidate",
      "f=enabled:any:no",
    ];
    for (const part of narrowed) await expect(page).toHaveURL(new RegExp(part));
    const url = page.url();

    // A reload opens the same view, its conditions said as chips.
    await page.reload();
    expect(page.url()).toBe(url);
    await expect(rowNamed(paused)).toBeVisible();
    await expect(page.locator(SCHEDULE_ROW)).toHaveCount(1);
    await expect(
      page.getByRole("button", { name: "Name contains candidate" }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Enabled is any of No" }),
    ).toBeVisible();

    // Back from somewhere else lands on it again.
    await page.goto("/processes");
    await expect(rowNamed(running)).toBeVisible();
    await page.goBack();
    expect(page.url()).toBe(url);
    await expect(page.locator(SCHEDULE_ROW)).toHaveCount(1);

    // The page's query is the API's, kind chip and all: the same
    // schedule comes back.
    const query = new URL(url).search;
    const answer = await (
      await page.request.get(`/api/schedules${query}`)
    ).json();
    expect(answer.map((s) => s.id)).toEqual([p.id]);

    // And a source that is a repository: the declared one alone.
    await page.goto("/processes?kind=schedule&f=source:any:acme/widgets");
    await expect(declared).toBeVisible();
    await expect(rowNamed(paused)).toHaveCount(0);
  } finally {
    for (const id of created) await page.request.delete(`/api/schedules/${id}`);
  }
});

// Each kind chip's address, pasted onto GET /api/schedules, answers the
// schedules the page shows under it -- the 2026-10-25 walkthrough found
// the API ignoring `?kind=` (every schedule for Workflow schedules, or
// Templates) and refusing `sort=kind:asc`.
test("each kind chip's address asks the API for the schedules it shows", async ({
  page,
}) => {
  await page.goto("/processes");
  await expect(page.locator(SCHEDULE_ROW).first()).toBeVisible();
  const bar = page.getByTestId("process-kind-filter");
  const kinds = await bar
    .locator("[data-kind]")
    .evaluateAll((els) => els.map((e) => e.dataset.kind));
  expect(kinds).toEqual(expect.arrayContaining(["schedule", "workflow"]));
  for (const kind of [null, ...kinds]) {
    await page.goto(kind ? "/processes?kind=schedule" : "/processes");
    await expect(page.locator(SCHEDULE_ROW).first()).toBeVisible();
    // Pressing a chip from another kind's view, so each is a click.
    if (kind) {
      await bar.locator('[data-kind="schedule"]').click();
      await bar.locator(`[data-kind="${kind}"]`).click();
      await expect(page).toHaveURL(new RegExp(`kind=${kind}$`));
    }
    const shown = await page
      .locator(
        '.process-row[data-kind="schedule"], .process-row[data-kind="workflow"]',
      )
      .count();
    const res = await page.request.get(
      `/api/schedules${new URL(page.url()).search}`,
    );
    expect(res.ok(), `${kind}: ${await res.text()}`).toBe(true);
    expect((await res.json()).length, `kind ${kind}`).toBe(shown);
  }
  const sorted = await page.request.get("/api/schedules?sort=kind:asc");
  expect(sorted.ok(), await sorted.text()).toBe(true);

  // Sorting schedules by cadence and narrowing by requireApproval on the
  // page match GET /api/schedules for the same query string.
  await page.goto(
    "/processes?kind=schedule&sort=cadence:asc,name:asc&f=requireApproval:empty:",
  );
  await expect(page.locator(SCHEDULE_ROW).first()).toBeVisible();
  const pageNames = await page
    .locator(`${SCHEDULE_ROW} .row-link`)
    .allTextContents();
  const apiRes = await page.request.get(
    `/api/schedules${new URL(page.url()).search}`,
  );
  expect(apiRes.ok(), await apiRes.text()).toBe(true);
  const apiNames = (await apiRes.json()).map((s) => s.title);
  expect(apiNames).toEqual(pageNames);

  const reqApprovalYes = await page.request.get(
    "/api/schedules?f=requireApproval:any:yes",
  );
  expect(reqApprovalYes.ok(), await reqApprovalYes.text()).toBe(true);
  expect(await reqApprovalYes.json()).toEqual([]);
});

// The toolbar, its condition chips and the kind chips under them fit a
// phone's width: four conditions on a narrowed Processes page wrap
// inside the page rather than pushing it sideways.
test("a narrowed Processes page fits a phone", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(
    "/processes?kind=schedule&sort=next:desc&f=source:any:deployment" +
      "&f=type:any:task&f=name:has:candidate&f=enabled:any:no",
  );
  const chip = page.getByRole("button", { name: "Enabled is any of No" });
  await expect(chip).toBeVisible();
  const width = await page.evaluate(() => document.documentElement.clientWidth);
  for (const el of [
    page.locator(".narrowing-chips"),
    page.getByTestId("process-kind-filter"),
    page.getByTitle("Choose the order this list is shown in"),
    chip,
  ]) {
    const box = await el.boundingBox();
    expect(box, "laid out at all").not.toBeNull();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width + 0.5);
  }
  const scroll = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(scroll, "page scrolls sideways").toBeLessThanOrEqual(0);
});
