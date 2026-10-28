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

// docs/test-plans/2026-10-06.md §6 steps 2, 3 and 5 in a real browser on
// the real `grain demo` server
// (docs/test-runs/2026-10-07-inbox-notifications-task-pane.md). Nothing of
// grain's is stubbed: the running task is demo's seeded "Bump the Go
// toolchain to 1.24", whose run is in flight in the store, and the task
// awaiting acknowledgement is its seeded "Why did last week's nightly suite
// slow down?". tests/e2e/ack_reopen_walkthrough_test.go is the same reopen
// through a real dispatch, which demo has none of.

const RUNNING = "Bump the Go toolchain to 1.24";
const IDLE = "Fix flaky retry test";
const ACK = "Why did last week's nightly suite slow down?";

async function taskByTitle(request, title) {
  const res = await request.get("/api/tasks");
  expect(res.ok()).toBe(true);
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const task = tasks.find((t) => t.title === title);
  expect(task, `seeded task "${title}"`).toBeTruthy();
  return task;
}

test("a running task's repo and base are plain rows, and PATCH refuses moving them", async ({
  page,
}) => {
  const running = await taskByTitle(page.request, RUNNING);
  expect(running.state).toBe("running");

  await page.goto(`/tasks/${running.id}`);
  const repos = page.locator("fieldset", {
    has: page.locator("legend", { hasText: /^Repos$/ }),
  });
  await expect(repos).toBeVisible();
  await expect(repos.locator(".declared-row")).toHaveText([
    /^Repo\s*acme\/widgets$/,
    /^Base branch\s*the repo's default$/,
  ]);
  // No repo picker and no branch box. The other repos the task may change
  // stay editable on purpose: they reach a proposal the run files.
  await expect(repos.getByLabel(/^(Repo|Base branch)$/)).toHaveCount(0);
  await expect(
    repos.getByRole("textbox", { name: "Another repo this task may change" }),
  ).toBeVisible();

  let res = await page.request.patch(`/api/tasks/${running.id}`, {
    data: { base: "release-1" },
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toBe(
    `${running.id} is running, so its base branch cannot be changed until its run ends`,
  );
  res = await page.request.patch(`/api/tasks/${running.id}`, {
    data: { repo: "acme/widgets", base: running.base || "" },
  });
  expect(res.status()).toBe(200);
});

test("Bulk edit of the base across a running and an idle task", async ({
  page,
}) => {
  const running = await taskByTitle(page.request, RUNNING);
  const idle = await taskByTitle(page.request, IDLE);

  await page.goto("/");
  await page
    .locator(".task-row", { hasText: RUNNING })
    .getByRole("checkbox")
    .check();
  await page
    .locator(".task-row", { hasText: IDLE })
    .getByRole("checkbox")
    .check();
  await expect(page.getByText("2 selected")).toBeVisible();
  await page.getByRole("button", { name: "Bulk edit…", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox", { name: /^Base branch/ }).check();
  await dialog.getByRole("textbox", { name: /Base branch/ }).fill("release-1");
  // Before it is applied, the form says what will happen to the running one.
  const notice = dialog.getByRole("alert").filter({ hasText: "running now" });
  await expect(notice).toBeVisible();
  console.log(`bulk edit notice: ${(await notice.innerText()).trim()}`);
  await expect(notice).toContainText("will be refused");

  await dialog.getByRole("button", { name: "Apply to 2 tasks" }).click();
  await expect(dialog).toHaveCount(0);
  const reported = page
    .getByRole("alert")
    .filter({ hasText: "cannot be changed until its run ends" });
  await expect(reported).toBeVisible();
  console.log(`bulk edit result: ${(await reported.innerText()).trim()}`);
  // The refused task stays selected for a retry; the other one moved.
  await expect(page.getByText("2 selected")).toBeVisible();
  expect((await taskByTitle(page.request, IDLE)).base).toBe("release-1");
  expect((await taskByTitle(page.request, RUNNING)).base || "").toBe(
    running.base || "",
  );
  // Put the idle task back for the tests after this one.
  const res = await page.request.patch(`/api/tasks/${idle.id}`, {
    data: { base: idle.base || "" },
  });
  expect(res.ok()).toBe(true);
});

test("a broadcast leaves an awaiting-ack task, and a person's comment reopens it", async ({
  page,
}) => {
  const task = await taskByTitle(page.request, ACK);
  expect(task.state).toBe("awaiting_reply");

  const sent = await page.request.post("/api/deployment/broadcast", {
    data: { message: "code freeze from 17:00" },
  });
  expect(sent.ok(), await sent.text()).toBe(true);
  expect((await sent.json()).task_ids).toContain(task.id);

  await page.goto(`/tasks/${task.id}`);
  await expect(
    page.locator(".timeline-item, .chat-message, .message-body", {
      hasText: "code freeze from 17:00",
    }),
  ).toBeVisible();
  expect((await taskByTitle(page.request, ACK)).state).toBe("awaiting_reply");
  await expect(
    page.getByText("Sending a comment reopens this task for another run."),
  ).toBeVisible();

  await page.getByPlaceholder("Reply...").fill("please also do X");
  await page.getByRole("button", { name: "Comment", exact: true }).click();
  await expect
    .poll(async () => (await taskByTitle(page.request, ACK)).state)
    .toBe("queued");
  await expect(
    page.getByText("Sending a comment reopens this task for another run."),
  ).toHaveCount(0);
});

test("opening and closing tasks shifts no layout", async ({ page }) => {
  await page.addInitScript(() => {
    window.__shifts = [];
    new PerformanceObserver((list) => {
      for (const e of list.getEntries())
        window.__shifts.push({ value: e.value, recent: e.hadRecentInput });
    }).observe({ type: "layout-shift", buffered: true });
  });
  await page.goto("/");
  const rows = page.locator(".task-row");
  await expect(rows.first()).toBeVisible();
  await page.waitForTimeout(1000);
  const before = await page.evaluate(() => window.__shifts.length);
  const count = Math.min(10, await rows.count());
  for (let i = 0; i < count; i++) {
    await rows.nth(i).click();
    await expect(page.locator(".detail-header h2")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.locator(".detail-header h2")).toHaveCount(0);
  }
  await page.waitForTimeout(500);
  const shifts = (await page.evaluate(() => window.__shifts)).slice(before);
  const total = shifts.reduce((sum, s) => sum + s.value, 0);
  const unprompted = shifts
    .filter((s) => !s.recent)
    .reduce((sum, s) => sum + s.value, 0);
  console.log(
    `layout shift over ${count} open/close: ${shifts.length} entries ` +
      `(${before} during the load before it), ` +
      `total ${total.toFixed(4)}, without recent input ${unprompted.toFixed(4)}`,
  );
  expect(total).toBeLessThan(0.1);
});

test("contacting running agents delivers a notice only to running tasks", async ({
  page,
}) => {
  const running = await taskByTitle(page.request, RUNNING);
  const idle = await taskByTitle(page.request, IDLE);

  await page.goto("/");
  await page.getByRole("button", { name: /^Contact running \(\d+\)$/ }).click();
  const dialog = page.getByRole("dialog", { name: "Contact running agents" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText(`#${running.id} ${RUNNING}`)).toBeVisible();

  await dialog.getByLabel("Notice").fill("pause pushes until 18:00");
  await dialog.getByRole("button", { name: "Send notice" }).click();
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByText(/Notice sent to \d+ running task agent/),
  ).toBeVisible();

  await page.goto(`/tasks/${running.id}`);
  await expect(
    page.locator(".timeline-item, .chat-message, .message-body", {
      hasText: "pause pushes until 18:00",
    }),
  ).toBeVisible();

  const idleMessages = await page.request.get(`/api/tasks/${idle.id}/messages`);
  expect(idleMessages.ok()).toBe(true);
  const body = await idleMessages.json();
  expect(
    (body.messages || []).some((m) => m.body === "pause pushes until 18:00"),
  ).toBe(false);
});
