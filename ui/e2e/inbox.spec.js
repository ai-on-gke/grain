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

// The inbox (InboxPage.jsx, grain/task-20) against a real server and a
// real browser -- see tasks.spec.js's own doc comment for what this
// suite is and what `grain demo` has already seeded by the time it runs.
//
// What is worth a real browser here rather than another jsdom test
// beside the component's own: /inbox as a route somebody can load cold,
// and the two answers that only mean anything end to end -- an approval
// and a reply fired from a row, each of which has to reach the real
// server, change the task's real state, and take the row off this page
// when it does. The reply is also the one place the question a run
// actually parked on (a comment, plus the observation pointing at it) is
// read back out of a real store.
//
// The proposal this suite approves is one it files itself, for the
// reason tasks.spec.js files its own: a test that approved the seeded
// proposal would leave the demo store a state poorer for whatever runs
// next.
//
// The parked question below is the exception, and cannot help being one:
// nothing a spec can reach files a question. Parking one is an agent's
// move, made through the MCP server's `ask_question`, and pkg/ui's API --
// all this file has, through page.request -- has no route that sets a
// task's pending question. So these two tests do spend the seeded
// fixture: answering it requeues "Investigate the merge queue stall" for
// good, and a second run of this file against the same server finds no
// task waiting on an answer and fails, both here and in the cold-load
// test above. That is why `make test-e2e-long` repeats the suite by
// running it three times over three fresh `grain demo` stores rather
// than with `--repeat-each=3` over one (grain/task-236) -- if you reach
// for the flag, this file is what fails first, and it is the flag that
// is wrong rather than these tests.

test("loads /inbox cold, grouped by what each task is waiting for", async ({
  page,
}) => {
  await page.goto("/inbox");

  await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
  // The seeded task parked on a question, under the group naming that
  // wait.
  const asking = page.locator(".inbox-group", { hasText: "Asked: reply" });
  await expect(
    asking.locator(".task-row", {
      hasText: "Investigate the merge queue stall",
    }),
  ).toBeVisible();
  // The seeded running task is grain's own business, not the reader's --
  // and the seeded deferred one has already had its answer.
  await expect(page.getByText("Bump the Go toolchain to 1.24")).toHaveCount(0);
  await expect(
    page.getByText("Replace the settings pane with a form library"),
  ).toHaveCount(0);
});

test("approves a proposal from its inbox row", async ({ page }) => {
  await page.goto("/");
  const title = `E2E inbox approve ${Date.now()}`;

  await page.getByRole("button", { name: "+ New task" }).click();
  await page.getByLabel("Title").fill(title);
  await page.getByLabel(/^Repos/).selectOption("none");
  // Unchecked so this files as a proposal -- which is what puts it in
  // the inbox in the first place.
  await page.getByLabel(/Queue immediately/).uncheck();
  await page.getByRole("button", { name: "Create task" }).click();

  await page.getByRole("button", { name: /^Inbox/ }).click();
  const row = page.locator(".inbox-item", { hasText: title });
  await expect(row).toBeVisible();

  await row.getByRole("button", { name: "Approve" }).click();

  // Approved means queued, and a queued task is waiting on grain rather
  // than on the reader -- so it leaves this page, and the pane the list's
  // own rows open never appears over it.
  await expect(page.locator(".inbox-item", { hasText: title })).toHaveCount(0);
  await expect(page.locator(".detail-header h2")).toHaveCount(0);
});

test("answers a parked question from its inbox row", async ({ page }) => {
  await page.goto("/inbox");
  const row = page.locator(".inbox-item", {
    hasText: "Investigate the merge queue stall",
  });

  await row.getByRole("button", { name: "Reply" }).click();

  // The question itself, read back off the real store's comment thread.
  await expect(row.locator(".inbox-question")).toContainText(
    "want me to fix both",
  );
  await row.getByRole("textbox").fill("Just unblock the queue for now.");
  await row.getByRole("button", { name: "Send" }).click();

  // A reply requeues the task (Client.AddComment's own "reply reopens"
  // rule), so it stops waiting on anybody and leaves the page.
  await expect(
    page.locator(".inbox-item", {
      hasText: "Investigate the merge queue stall",
    }),
  ).toHaveCount(0);
});

// grain task 1402: a parked question shows only its one-line summary on
// its inbox row, and the whole question -- markdown and all -- opens in
// place from a button that says whether it is open (aria-expanded), by
// mouse and from the keyboard alike. The question is this test's own,
// posted the way a run's chat turn posts one, so nothing another spec on
// this worker answered can take it away.
async function parkWithQuestion(page, title) {
  const created = await page.request.post("/api/tasks", {
    data: { title, approved: true },
  });
  expect(created.ok()).toBe(true);
  const task = await created.json();
  const asked = await page.request.post(`/api/tasks/${task.id}/messages`, {
    data: {
      summary: "Keep the v1 export API alongside v2, or remove it?",
      body:
        "The v1 export API still has two callers.\n\n" +
        "Removing it means migrating `billing-sync` first; keeping it means " +
        "another release of **dual maintenance**.",
    },
  });
  expect(asked.ok()).toBe(true);
  return task;
}

test("shows a parked question by its summary and expands it in place", async ({
  page,
}) => {
  const title = `Retire the v1 export API ${Date.now()}`;
  await parkWithQuestion(page, title);
  await page.goto("/inbox");
  const row = page.locator(".inbox-item", { hasText: title });
  const toggle = row.getByRole("button", {
    name: /Keep the v1 export API alongside v2, or remove it\?/,
  });
  await expect(toggle).toBeVisible();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(row.locator(".inbox-question")).toHaveCount(0);
  await expect(row).not.toContainText("dual maintenance");

  // The summary is one line however narrow the row: it is cut, not
  // wrapped.
  const summary = toggle.locator(".question-disclosure-summary");
  const lineHeight = await summary.evaluate(
    (el) => parseFloat(getComputedStyle(el).lineHeight) || 20,
  );
  expect((await summary.boundingBox()).height).toBeLessThan(lineHeight * 1.5);

  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  const question = row.locator(".inbox-question");
  await expect(question).toBeVisible();
  await expect(question).toContainText("two callers");
  await expect(question.locator("strong")).toHaveText("dual maintenance");
  // The body opens below the summary, not beside or over it.
  const toggleBox = await toggle.boundingBox();
  const questionBox = await question.boundingBox();
  expect(questionBox.y).toBeGreaterThanOrEqual(
    toggleBox.y + toggleBox.height - 1,
  );

  // And closes again from the keyboard.
  await toggle.focus();
  await page.keyboard.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(row.locator(".inbox-question")).toHaveCount(0);
  await page.keyboard.press("Space");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
});

// The task list's row for a parked task says what it was asked on its
// status line, and the task's own pane leads the question with it.
test("puts a parked question's summary on the task list and the task's pane", async ({
  page,
}) => {
  const title = `Retire the v2 export API ${Date.now()}`;
  const task = await parkWithQuestion(page, title);
  await page.goto("/");
  const row = page.locator(".task-row", { hasText: title });
  await expect(row.locator(".task-activity-note")).toHaveText(
    "Keep the v1 export API alongside v2, or remove it?",
  );

  await page.goto(`/tasks/${task.id}`);
  const pending = page.getByTestId("pending-question");
  await expect(pending.locator(".question-summary-line strong")).toHaveText(
    "Keep the v1 export API alongside v2, or remove it?",
  );
  await expect(pending).toContainText("two callers");
});
