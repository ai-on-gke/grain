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

// -notifications seeds the Inbox's alerts; the tasks are the stock demo's.
test.use({ demoArgs: ["-notifications"] });

// Regressions from the 2026-11-15 task intake, Inbox and lifecycle
// walkthrough (docs/test-runs/2026-11-15-task-intake-inbox-lifecycle.md),
// on the /next/ task page and Inbox:
//
//   - Pause immediately throws away what a run has not pushed, so it asks
//     first, as the classic Pause now does;
//   - a paused task that was also stopped offers one Resume, not a second
//     that calls Retry, which the server always refuses while paused;
//   - the goal editor reads the goal it is editing (the task row's
//     camelCase onLimit, not the declaration's on_limit), so saving it
//     again does not quietly turn "notify" back into "hold", and Clear
//     goal leaves no stale Goal line behind;
//   - an acknowledged alert is no longer open, so it leaves the Inbox
//     headline and the rail badge, though its row stays, marked.

async function taskByTitle(demoServer, accessCode, title) {
  const res = await fetch(`${demoServer}/api/tasks?fields=list`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  return tasks.find((t) => t.title === title);
}

test("Pause immediately asks first, and Cancel leaves the run going", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const t = await taskByTitle(
    demoServer,
    accessCode,
    "Bump the Go toolchain to 1.24",
  );
  expect(t?.state).toBe("running");
  const pauses = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith(`/api/tasks/${t.id}/pause`))
      pauses.push(r.postDataJSON());
  });
  await page.goto(`/next/task/${t.id}`);

  // In the action area beside Pause, not behind a More menu.
  await page.getByTestId("act-pause-immediate").click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText("whatever it has not pushed is lost");
  await page.getByTestId("confirm-cancel").click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByTestId("act-pause")).toBeVisible();
  expect(pauses).toEqual([]);

  await page.getByTestId("act-pause-immediate").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("act-resume-pause")).toBeVisible();
  expect(pauses).toEqual([{ immediate: true }]);

  await page.getByTestId("act-resume-pause").click();
  await expect(page.getByTestId("act-pause")).toBeVisible();
});

test("a stopped task that is also paused offers only the pause's Resume", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const t = await taskByTitle(
    demoServer,
    accessCode,
    "Add pagination to the tasks API",
  );
  expect(t?.state).toBe("running");
  await page.goto(`/next/task/${t.id}`);

  await page.getByTestId("act-stop").click();
  await expect(page.getByTestId("act-retry")).toHaveText("Resume");
  await page.getByTestId("act-pause").click();
  await expect(page.getByTestId("act-resume-pause")).toBeVisible();
  // Retry is refused while paused (ui.Client.Retry), so it is not offered.
  await expect(page.getByTestId("act-retry")).toHaveCount(0);
  await expect(
    page
      .getByRole("group", { name: "Task actions" })
      .getByRole("button", { name: "Resume", exact: true }),
  ).toHaveCount(1);

  // Lifting the pause brings the stop's own Resume back, and it works.
  await page.getByTestId("act-resume-pause").click();
  await expect(page.getByTestId("act-retry")).toBeVisible();
  await page.getByTestId("act-retry").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("act-retry")).toHaveCount(0);
  await expect
    .poll(
      async () =>
        (
          await taskByTitle(
            demoServer,
            accessCode,
            "Add pagination to the tasks API",
          )
        )?.state,
    )
    .toBe("queued");
});

test("the goal editor keeps a goal's at-limit choice across a re-save, and Clear goal clears it", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const t = await taskByTitle(
    demoServer,
    accessCode,
    "Extract pagination query helper",
  );
  const goalOf = async () => {
    const res = await fetch(`${demoServer}/api/tasks/${t.id}`, {
      headers: { Authorization: `Bearer ${accessCode}` },
    });
    return (await res.json()).goal ?? null;
  };
  const pick = async (testid, label) => {
    await page.getByTestId(testid).click();
    await page.getByRole("option", { name: label }).click();
  };
  await page.goto(`/next/task/${t.id}`);

  await page.getByTestId("act-edit").click();
  await pick("edit-goal-kind", "Base branch green");
  await pick("edit-goal-on-limit", "Notify & back off");
  await page.getByTestId("edit-goal-save").click();
  await expect.poll(async () => (await goalOf())?.onLimit).toBe("notify");

  await page.reload();
  await page.getByTestId("act-edit").click();
  await expect(page.getByTestId("edit-goal-on-limit")).toContainText(
    "Notify & back off",
  );
  // Wait for the re-save itself to answer, rather than for a fixed time:
  // the stored goal is read only once the PUT has landed.
  const put = page.waitForResponse(
    (r) =>
      r.request().method() === "PUT" &&
      r.url().endsWith(`/api/tasks/${t.id}/goal`),
  );
  await page.getByTestId("edit-goal-save").click();
  expect((await put).ok()).toBe(true);
  const resaved = await goalOf();
  expect(resaved?.kind).toBe("base-green");
  expect(resaved?.onLimit).toBe("notify");

  await page.getByTestId("edit-goal-clear").click();
  await expect.poll(goalOf).toBeNull();
  await page.reload();
  await expect(
    page.getByRole("definition").filter({ hasText: "base-green" }),
  ).toHaveCount(0);
});

test("acknowledging an alert takes it out of the Inbox count, and its row stays marked", async ({
  page,
}) => {
  await page.goto("/next/inbox");
  const badge = page.getByTestId("rail-inbox-count");
  const headline = page.locator("main .view-headline").first();
  await expect(headline).toContainText("waiting on you");
  const count = async () => Number((await badge.innerText()).trim());
  const before = await count();
  await expect(headline).toHaveText(`${before} decisions waiting on you`);

  const row = page
    .getByTestId("inbox-group-notification")
    .getByTestId("queue-row")
    .filter({ hasText: "A merge has been stalled" });
  await row.locator(".row-main").click();
  await page.getByTestId("action-ack").click();
  await expect(row.locator(".row-acked")).toHaveText("acknowledged");
  await expect(badge).toHaveText(String(before - 1));
  await expect(headline).toHaveText(`${before - 1} decisions waiting on you`);

  await page.reload();
  await expect(badge).toHaveText(String(before - 1));
  await expect(row).toHaveCount(1);
});
