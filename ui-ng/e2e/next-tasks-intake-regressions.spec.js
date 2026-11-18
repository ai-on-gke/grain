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

// Regressions from the 2026-11-16 task intake, Inbox and lifecycle
// walkthrough (docs/test-runs/2026-11-16-task-intake-inbox-lifecycle.md),
// on /next/tasks:
//
//   - a first read of the task list that fails shows no count on the
//     open tabs rather than "0" on every one, and its banner names the
//     HTTP status rather than the JSON parser's complaint about a body
//     that was not JSON;
//   - Bulk edit's Task priority reads "1e3" as 1000, as classic Bulk
//     edit does, shows the box as 1000 once it loses focus, and still
//     refuses zero, negative and non-whole values.

async function queuedTasks(demoServer, accessCode) {
  const res = await fetch(`${demoServer}/api/tasks?fields=list`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  return tasks.filter((t) => t.state === "queued" && !t.deferredUntil);
}

async function patch(demoServer, accessCode, id, body) {
  const res = await fetch(`${demoServer}/api/tasks/${id}`, {
    method: "PATCH",
    headers: {
      Authorization: `Bearer ${accessCode}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
  expect(res.ok).toBe(true);
}

test("a task list that never loaded shows no tab counts and names the status", async ({
  page,
}) => {
  await page.route(/\/api\/tasks(\?|$)/, (r) =>
    r.fulfill({ status: 503, contentType: "text/plain", body: "boom" }),
  );
  await page.goto("/next/tasks");
  const banner = page.getByText(/The task list could not be read/).first();
  await expect(banner).toBeVisible();
  await expect(banner).toContainText("The server answered 503");
  await expect(banner).not.toContainText("JSON");
  await expect(banner).not.toContainText("..");
  for (const tab of ["backlog", "proposed", "running", "merging"]) {
    await expect(
      page.getByTestId(`tab-${tab}`).first().locator(".tab-count"),
    ).toHaveCount(0);
  }
});

test("Bulk edit reads 1e3 as priority 1000 and refuses what is not a whole number", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const [a, b] = await queuedTasks(demoServer, accessCode);
  expect(b).toBeTruthy();
  try {
    await page.goto("/next/tasks?tab=backlog");
    for (const t of [a, b])
      await page
        .getByTestId(`row-${t.id}`)
        .first()
        .locator("input[type=checkbox]")
        .check();
    await page.getByTestId("batch-more").click();
    await page.getByRole("option", { name: /Bulk edit/ }).click();
    const dlg = page.getByRole("dialog");
    await dlg.getByTestId("bulk-priority-tick").check();
    const box = dlg.getByTestId("bulk-priority");
    const apply = dlg.getByTestId("batch-edit-apply");
    for (const bad of ["0", "-2", "2.5"]) {
      await box.fill(bad);
      await expect(dlg.getByTestId("bulk-priority-error")).toBeVisible();
      await expect(apply).toBeDisabled();
    }
    await box.fill("1e3");
    await expect(dlg.getByTestId("bulk-priority-error")).toHaveCount(0);
    await box.blur();
    await expect(box).toHaveValue("1000");
    const patches = [];
    page.on("request", (r) => {
      if (r.method() === "PATCH" && /\/api\/tasks\/\d+$/.test(r.url()))
        patches.push(r.postDataJSON());
    });
    await apply.click();
    await expect.poll(() => patches.length).toBe(2);
    expect(patches).toEqual([{ priority: 1000 }, { priority: 1000 }]);
    await page.reload();
    await expect(page.getByTestId(`row-${a.id}`).first()).toContainText("1000");
  } finally {
    for (const t of [a, b])
      if (t)
        await patch(demoServer, accessCode, t.id, {
          priority: t.priority ?? 5,
        });
  }
});

test("New task explains a fork head label and refuses a malformed one before filing", async ({
  page,
}) => {
  const posts = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith("/api/tasks"))
      posts.push(r.postDataJSON());
  });
  await page.goto("/next/tasks");
  await page.getByRole("button", { name: /^New/ }).first().click();
  const sheet = page.getByTestId("new-task-sheet");
  await sheet.getByTestId("nt-title").fill("Fork head probe");
  await sheet.getByTestId("nt-new-branch").uncheck();
  const base = sheet.getByTestId("nt-base");
  const submit = sheet.getByTestId("new-task-submit");

  await base.fill("octo-fork:feature-x");
  await expect(sheet.getByTestId("nt-fork-head-help")).toContainText(
    "branch feature-x on octo-fork's fork",
  );
  await expect(submit).toBeEnabled();

  await base.fill("bad::label");
  await expect(sheet.getByTestId("nt-fork-head-error")).toContainText(
    "has more than one ':'",
  );
  await expect(base).toHaveAttribute("aria-invalid", "true");
  await expect(submit).toBeDisabled();

  // An ordinary branch name is not a fork head, and ticking Create a new
  // branch makes the box the branch to start from, where no label is read.
  await base.fill("main");
  await expect(sheet.getByTestId("nt-fork-head-error")).toHaveCount(0);
  await base.fill("bad::label");
  await sheet.getByTestId("nt-new-branch").check();
  await expect(sheet.getByTestId("nt-fork-head-error")).toHaveCount(0);
  expect(posts).toEqual([]);
  await page.keyboard.press("Escape");
});
