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

// A merge that failed is not a change being landed (2026-11-18 intake
// walkthrough). `grain demo -merge-rules` seeds "Raise the API pod memory
// limit", completed with merge-queue state merge_failed and waiting on a
// person's Retry merge. /next/ read it "Merging" on its Tasks row and its
// page header, where classic's row reads "Merge failed". Both now say
// "Merge failed", in the error tone, and a change really being merged
// still reads "Merging".

test.use({ demoArgs: ["-merge-rules"] });

const FAILED = "Raise the API pod memory limit";

async function taskByTitle(demoServer, accessCode, title) {
  const res = await fetch(`${demoServer}/api/tasks`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  const tasks = await res.json();
  const t = tasks.find((x) => x.title === title);
  if (!t) throw new Error(`no seeded task titled ${title}`);
  return t;
}

test("a failed merge reads Merge failed on its row and its page", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const t = await taskByTitle(demoServer, accessCode, FAILED);
  expect(t.state).toBe("completed");
  expect(t.mergeQueueState).toBe("merge_failed");

  await page.goto("tasks?tab=merging");
  const row = page.getByTestId(`row-${t.id}`);
  const oval = row.locator(".status-pill").first();
  await expect(oval).toHaveText("Merge failed");
  await expect(oval).toHaveClass(/\berror\b/);

  // A change on the merge queue that has not failed keeps "Merging".
  const tasks = await (
    await fetch(`${demoServer}/api/tasks`, {
      headers: { Authorization: `Bearer ${accessCode}` },
    })
  ).json();
  const landing = tasks.find(
    (x) => x.state === "completed" && x.mergeQueueState !== "merge_failed",
  );
  if (landing) {
    await expect(
      page.getByTestId(`row-${landing.id}`).locator(".status-pill").first(),
    ).toHaveText("Merging");
  }

  await page.goto(`task/${t.id}`);
  const header = page.getByTestId("task-state");
  await expect(header).toHaveText("Merge failed");
  await expect(header).toHaveClass(/\berror\b/);
});
