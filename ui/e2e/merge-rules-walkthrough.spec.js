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

// docs/test-plans/2026-10-05.md §2 step 5, in a browser on the real
// `grain demo` server (docs/test-runs/2026-10-05-merge-rules.md).
// `-merge-rules` seeds acme/widgets with the docs/declarations.md example
// as its .grain/merge-rules.yml and two deploy/** changes the merge queue
// classified under it, written as the queue writes them; tests/e2e's
// TestMergeRulesWalkthroughDeploy reaches the same states through the
// real queue. Nothing of grain's is stubbed.
test.use({ demoArgs: ["-merge-rules"] });

const WAITING = "Roll the ingress controller to v2";
const STALLED = "Raise the API pod memory limit";

async function taskByTitle(request, title) {
  const res = await request.get("/api/tasks");
  expect(res.ok()).toBe(true);
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const task = tasks.find((t) => t.title === title);
  expect(task, `seeded task "${title}"`).toBeTruthy();
  return task;
}

const REQUIREMENTS = [
  "ci.mode: grain — from repository setting",
  "ci.mode: both — from rule deploy",
  "auto_merge: never — from rule deploy",
  "stall_timeout: 30m — from rule deploy",
  "ci.grain: full suite — from rule deploy",
  "ci.github: build — from rule deploy",
  "ci.github: cla/google — from rule deploy",
];

test.describe("Merge rules walkthrough", () => {
  test("the task pane and the list row say what the rules require, and Submit is offered", async ({
    page,
  }) => {
    const task = await taskByTitle(page.request, WAITING);
    expect(task.autoMerge).toBe(false);
    expect(task.mergePolicy.Matched).toEqual(["deploy"]);

    await page.goto("/");
    const row = page
      .locator("tr, li, [role=row]", { hasText: WAITING })
      .first();
    await expect(row).toBeVisible();
    console.log(`list row: ${(await row.innerText()).replace(/\s+/g, " ")}`);

    await page.goto(`/tasks/${task.id}`);
    const policy = page.getByTestId("merge-policy");
    await expect(policy).toBeVisible();
    await expect(page.getByTestId("merge-requirement")).toHaveText(
      REQUIREMENTS,
    );
    await expect(policy).toContainText(
      "GitHub CI is narrowed to build, cla/google",
    );
    console.log(
      `task pane policy: ${(await policy.innerText()).replace(/\s+/g, " ")}`,
    );
    // The queue's note naming the missing check and the rule behind it.
    await expect(
      page
        .getByText("`cla/google` (required by `deploy`)", { exact: false })
        .first(),
    ).toBeVisible();
    // And its classification comment, in the timeline.
    const note = page.getByText("matched rule(s)", { exact: false }).first();
    await expect(note).toBeVisible();
    console.log(
      `timeline comment: ${(await note.innerText()).replace(/\s+/g, " ")}`,
    );

    // auto_merge: never withdrew auto-merge: a person's Submit merges it,
    // and there is nothing for Retry merge to retry.
    await expect(
      page.getByRole("button", { name: "Submit", exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: "Retry merge" })).toHaveCount(
      0,
    );
  });

  test("a change the queue gave up on after stall_timeout offers Retry merge", async ({
    page,
  }) => {
    const task = await taskByTitle(page.request, STALLED);
    expect(task.mergeQueueBlockedAt).toBeTruthy();
    await page.goto(`/tasks/${task.id}`);
    await expect(page.getByTestId("merge-requirement")).toHaveText(
      REQUIREMENTS,
    );
    await expect(
      page
        .getByText("gave up waiting on CI after 30m", { exact: false })
        .first(),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Retry merge" }),
    ).toBeVisible();
  });

  // docs/test-plans/2026-10-06.md §6 step 4: acme/widgets still carries
  // the older .grain/sensitive.yml beside its merge-rules.yml, and its
  // ci.grain names jobs this demo repo declares no workflow for. The sync
  // keeps the rules and the warnings; the Repos page says both.
  test("the Repos page names the merge-rules file and lists its warnings", async ({
    page,
  }) => {
    const res = await page.request.get("/api/repos/acme/widgets/declarations");
    expect(res.ok()).toBe(true);
    const status = (await res.json()).declarationsStatus;
    expect(status.mergeRules).toBe(".grain/merge-rules.yml");

    await page.goto("/repos/acme/widgets#advanced");
    await expect(
      page.getByText("merge rules in .grain/merge-rules.yml", { exact: false }),
    ).toBeVisible();
    const warnings = page.getByText(/^Warning: /);
    await expect(warnings).toHaveText([
      "Warning: .grain/merge-rules.yml and .grain/sensitive.yml both exist; .grain/merge-rules.yml is read and .grain/sensitive.yml is ignored -- delete it",
      'Warning: .grain/merge-rules.yml: rule docs: ci.grain names "lint", which no job or workflow in .grain/workflows declares',
      'Warning: .grain/merge-rules.yml: rule ui: ci.grain names "ui e2e", which no job or workflow in .grain/workflows declares',
    ]);
    expect(status.warnings).toHaveLength(3);
  });
});
