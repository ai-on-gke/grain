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

// The demo's queued review (task 23) is stacked on task 22, which is
// waiting on a submit and so is not in the Waiting list. The list draws
// such a review as a row of its own -- and it used to draw it last
// whatever the backlog said, so a move "to the end of the list" was told
// it landed after task 23 and left the moved task at the top of the
// backlog (2026-11-11 saved-boards walkthrough).
//
// -human-review is what seeds that review at all.
test.use({ demoArgs: ["-human-review"] });

const queued = async (request) => {
  const res = await request.get("/api/tasks");
  expect(res.ok()).toBe(true);
  return (await res.json()).filter((t) => t.state === "queued");
};
const ids = (tasks) => tasks.map((t) => t.id);

const reorder = async (request, data) => {
  const res = await request.post("/api/tasks/reorder", { data });
  expect(res.ok(), `POST /api/tasks/reorder: ${res.status()}`).toBe(true);
};

test("a stacked review whose task is filtered out keeps its backlog place, and a move to the end lands at the end", async ({
  page,
}) => {
  const before = await queued(page.request);
  const queuedIds = new Set(ids(before));
  const review = before.find(
    (t) => t.stacked && t.generatedFrom && !queuedIds.has(t.generatedFrom),
  );
  expect(
    review,
    "the demo seeds a queued review of a task that is not queued",
  ).toBeTruthy();
  try {
    // Put the review first in the backlog, as a Kanban move would.
    await reorder(page.request, { ids: [review.id], beforeId: before[0].id });

    await page.goto("/?f=state:any:queued");
    const rows = page.locator(".task-row .row-link");
    const shown = async () =>
      (
        await rows.evaluateAll((as) => as.map((a) => a.getAttribute("href")))
      ).map((h) => h.split("/").pop());
    // The list is the backlog, less the tasks it does not list (a
    // standing session), with the review first where the backlog has it.
    const backlog = async () => {
      const on = new Set(await shown());
      return ids(await queued(page.request)).filter((id) => on.has(id));
    };
    await expect.poll(async () => (await shown())[0]).toBe(review.id);
    expect(await shown()).toEqual(await backlog());

    const list = await shown();
    const mover = list[1];
    const last = list[list.length - 1];
    await page
      .getByRole("button", { name: `Move task ${mover}`, exact: true })
      .click();
    const moved = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/tasks/reorder") &&
        r.request().method() === "POST",
    );
    await page
      .getByRole("button", { name: "Move here, to the end of the list" })
      .click();
    expect((await moved).request().postDataJSON()).toEqual({
      ids: [mover],
      afterId: last,
    });

    await page.reload();
    await expect.poll(async () => (await shown()).at(-1)).toBe(mover);
    expect(await shown()).toEqual(await backlog());
  } finally {
    const now = ids(await queued(page.request));
    if (now[0] !== before[0].id) {
      await reorder(page.request, { ids: [before[0].id], beforeId: now[0] });
    }
    for (let i = 1; i < before.length; i++) {
      await reorder(page.request, {
        ids: [before[i].id],
        afterId: before[i - 1].id,
      });
    }
    expect(ids(await queued(page.request))).toEqual(ids(before));
  }
});
