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

// docs/test-plans/2026-10-28.md §8, the browser half, over
// `grain demo -github-sim -fake-ci -merge-rules` with nothing stubbed in
// the page: Submit on the change `auto_merge: never` holds for a person,
// and Retry merge on the change the queue gave up on, each pressed for
// real and read back from the server. The merge queue's own half (no
// batch, the overlap re-check, Submit pinned to the head seen) is in
// pkg/orchestrator/mergequeue_walkthrough_20261028_test.go; the report
// is docs/test-runs/2026-10-28-merge-queue-security-review-submit.md.
//
// These presses change the worker's demo, which is why this file asks for
// a demo of its own (a demoArgs no other file uses) and runs serially.

test.use({ demoArgs: ["-github-sim", "-fake-ci", "-merge-rules"] });
test.describe.configure({ mode: "serial" });

const HELD = "Roll the ingress controller to v2";
const GIVEN_UP = "Raise the API pod memory limit";

async function taskByTitle(request, title) {
  const res = await request.get("/api/tasks");
  expect(res.ok()).toBe(true);
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const task = tasks.find((t) => t.title === title);
  expect(task, `no task titled ${title}`).toBeTruthy();
  return task;
}

async function taskDetail(request, id) {
  const res = await request.get(`/api/tasks/${id}`);
  expect(res.ok()).toBe(true);
  return res.json();
}

test("Submit on a held change turns its auto-merge on, pressed in the browser", async ({
  page,
  request,
}) => {
  const held = await taskByTitle(request, HELD);
  expect((await taskDetail(request, held.id)).autoMerge).toBeFalsy();

  await page.goto(`/tasks/${held.id}`);
  const submit = page.getByRole("button", { name: "Submit", exact: true });
  await expect(submit).toBeVisible();
  const posted = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/tasks/${held.id}/submit`) &&
      r.request().method() === "POST",
  );
  await submit.click();
  expect((await posted).ok()).toBe(true);
  await expect(submit).toHaveCount(0);
  await expect
    .poll(async () => (await taskDetail(request, held.id)).autoMerge)
    .toBe(true);
});

test("Retry merge in the inbox hands a given-up change back to the queue", async ({
  page,
  request,
}) => {
  const givenUp = await taskByTitle(request, GIVEN_UP);
  const before = (await taskDetail(request, givenUp.id)).reason?.reason;
  expect(before).not.toBe("completed/on_merge_queue");
  await page.goto("/inbox");
  const group = page.locator(".inbox-group", { hasText: "Unblock a merge" });
  const item = group.locator(".inbox-item", { hasText: GIVEN_UP });
  await expect(item).toHaveCount(1);

  const posted = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/tasks/${givenUp.id}/retry-merge`) &&
      r.request().method() === "POST",
  );
  await item.getByRole("button", { name: "Retry merge" }).click();
  expect((await posted).ok()).toBe(true);

  // Back on the queue, it is no longer a merge somebody has to unblock.
  await expect(item).toHaveCount(0);
  await expect
    .poll(async () => (await taskDetail(request, givenUp.id)).reason?.reason)
    .toBe("completed/on_merge_queue");
});
