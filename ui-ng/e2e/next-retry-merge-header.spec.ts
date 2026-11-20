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

import { expect, test } from "./fixtures";

// When a merge-failed task waits on a person's Retry merge (2026-11-18 intake
// walkthrough, O3), `/next/task/<id>` offers Retry merge as its primary header
// action, tucks Run now into More on completed tasks, and follows the retry
// into `completed/on_merge_queue` ("Merging") without a reload.
//
// A distinct `-as` gives this spec its own worker store so pressing Retry
// merge does not consume the merge_failed seed that next-merge-failed-state
// reads.
test.use({ demoArgs: ["-merge-rules", "-as", "next-retry-merge-header"] });

const FAILED_TITLE = "Raise the API pod memory limit";
const LANDING_TITLE = "Rotate the Gemini signing key";

interface DemoTask {
  id: string;
  title: string;
  state: string;
  mergeQueueState?: string;
  reason?: { reason: string; class?: string; control?: string };
}

async function listTasks(
  demoServer: string,
  accessCode: string,
): Promise<DemoTask[]> {
  const res = await fetch(`${demoServer}/api/tasks`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  expect(res.ok).toBe(true);
  const body = await res.json();
  return Array.isArray(body) ? body : body.tasks;
}

async function readTask(
  demoServer: string,
  accessCode: string,
  id: string,
): Promise<DemoTask> {
  const res = await fetch(`${demoServer}/api/tasks/${id}`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  expect(res.ok).toBe(true);
  return res.json();
}

test("merge-failed task header offers Retry merge as primary action, demotes Run now on completed tasks, and updates live @phone @dark", async ({
  page,
  axe,
  demoServer,
  accessCode,
}) => {
  const tasks = await listTasks(demoServer, accessCode);
  const failed = tasks.find((t) => t.title === FAILED_TITLE);
  expect(failed, `missing seeded task "${FAILED_TITLE}"`).toBeTruthy();
  expect(failed!.state).toBe("completed");
  expect(failed!.mergeQueueState).toBe("merge_failed");
  expect(failed!.reason).toMatchObject({
    reason: "completed/merge_failed",
    control: "Retry merge",
  });

  const landing = tasks.find((t) => t.title === LANDING_TITLE);
  expect(landing, `missing seeded task "${LANDING_TITLE}"`).toBeTruthy();
  expect(landing!.state).toBe("completed");
  expect(landing!.reason?.reason).toBe("completed/on_merge_queue");

  // 1. A completed task actively on the merge queue shows "Merging", no Retry
  //    merge, and Run now demoted: a quiet button after the divider in the
  //    action area (which has no More menu), never a main action.
  await page.goto(`task/${landing!.id}`);
  const actions = page.getByRole("group", { name: "Task actions" });
  await expect(page.getByTestId("task-state")).toHaveText("Merging");
  await expect(
    actions.getByRole("button", { name: "Retry merge" }),
  ).toHaveCount(0);
  const demoted = async () => {
    const runNow = actions.getByRole("button", { name: "Run now" });
    await expect(runNow).toHaveClass(/\bbtn-text\b/);
    await expect(runNow).not.toHaveClass(/\bbtn-primary\b/);
  };
  await demoted();
  await expect(page.getByTestId("act-more")).toHaveCount(0);

  // 2. The merge-failed task offers Retry merge as its primary header action,
  //    with Run now tucked inside More rather than competing in the header.
  await page.goto(`task/${failed!.id}`);
  const stateOval = page.getByTestId("task-state");
  await expect(stateOval).toHaveText("Merge failed");
  await expect(stateOval).toHaveClass(/\berror\b/);

  const retryBtn = actions.getByRole("button", { name: "Retry merge" });
  await expect(retryBtn).toBeVisible();
  await expect(retryBtn).toBeEnabled();
  await expect(retryBtn).toHaveClass(/\bbtn-primary\b/);
  await expect(actions.getByRole("button").first()).toHaveText("Retry merge");
  await demoted();

  // Layout & bounding-box invariants at both desktop and phone widths.
  const viewport = page.viewportSize()!;
  const [retryBox, groupBox, titleBox, pauseBox] = await Promise.all([
    retryBtn.boundingBox(),
    actions.boundingBox(),
    page.getByRole("heading", { level: 1 }).boundingBox(),
    actions.getByRole("button", { name: "Pause" }).boundingBox(),
  ]);
  expect(retryBox).not.toBeNull();
  expect(groupBox).not.toBeNull();
  expect(titleBox).not.toBeNull();
  expect(pauseBox).not.toBeNull();
  expect(retryBox!.x).toBeGreaterThanOrEqual(groupBox!.x - 1);
  expect(retryBox!.x + retryBox!.width).toBeLessThanOrEqual(
    groupBox!.x + groupBox!.width + 1,
  );
  expect(retryBox!.x + retryBox!.width).toBeLessThanOrEqual(viewport.width);
  expect(retryBox!.y).toBeGreaterThanOrEqual(
    titleBox!.y + titleBox!.height - 1,
  );
  if (Math.abs(retryBox!.y - pauseBox!.y) < 10) {
    expect(
      Math.abs(
        retryBox!.y +
          retryBox!.height / 2 -
          (pauseBox!.y + pauseBox!.height / 2),
      ),
    ).toBeLessThanOrEqual(2);
  }
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  expect(await axe()).toEqual([]);

  // 3. Pressing Retry merge posts /api/tasks/{id}/retry-merge and updates the
  //    header in place without a reload.
  let navigations = 0;
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) navigations++;
  });
  const retryPost = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      new URL(r.url()).pathname === `/api/tasks/${failed!.id}/retry-merge`,
  );
  await retryBtn.click();
  expect((await retryPost).status()).toBe(200);

  await expect(retryBtn).toHaveCount(0);
  await expect(stateOval).toHaveText("Merging");
  await expect(stateOval).not.toHaveClass(/\berror\b/);
  await demoted();
  expect(navigations).toBe(0);

  const after = await readTask(demoServer, accessCode, failed!.id);
  expect(after.state).toBe("completed");
  expect(after.mergeQueueState).toBeFalsy();
  expect(after.reason?.reason).toBe("completed/on_merge_queue");
});
