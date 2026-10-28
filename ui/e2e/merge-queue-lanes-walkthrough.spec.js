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

// docs/test-plans/2026-10-13.md §2, the parts a browser sees, against the
// demo the plan names (`grain demo -github-sim -fake-ci
// -merge-queue-states`). The queue itself -- two lanes of one repository,
// the row notes, the contention it records -- runs in
// tests/e2e/mergequeue_lanes_walkthrough_test.go, on the real
// orchestrator: the demo seeds rows but runs no queue. Report:
// docs/test-runs/2026-10-13-merge-queue-lanes-contention.md.
test.use({ demoArgs: ["-github-sim", "-fake-ci", "-merge-queue-states"] });

// max_merges_per_repo applies per (repository, base branch) lane since
// 5c2776f62, and the System setting says so.
test("System names the merge limit per repository branch and keeps 1", async ({
  page,
  request,
}) => {
  await page.goto("/settings/engine");
  const box = page.getByLabel("Max merges per repo branch");
  await expect(box).toBeVisible();
  await expect(
    page.getByText(
      "maximum ready pull requests merged into each branch of a repository per queue cycle",
    ),
  ).toBeVisible();
  await box.fill("1");
  await box.blur();
  await expect
    .poll(async () => {
      const res = await request.get("/api/settings");
      return res.ok() ? (await res.json()).maxMergesPerRepo : null;
    })
    .toBe(1);
  await page.reload();
  await expect(page.getByLabel("Max merges per repo branch")).toHaveValue("1");
});

// The merges report carries the contention the queue recorded. The demo's
// queue has done nothing over again, so every count is a real zero rather
// than missing. Metrics shows no Merges block for a window with nothing
// landed (MergesSection), and renders no contention figure even when it
// does: contention is read from /api/metrics (and the metrics MCP tool),
// which the report records as a finding.
test("the merges report carries contention counts, zeros included", async ({
  page,
  request,
}) => {
  const res = await request.get("/api/metrics?section=merges");
  expect(res.ok()).toBe(true);
  const body = await res.json();
  const c = body.merges?.contention;
  expect(c).toBeTruthy();
  expect(c.rebaseRetest).toMatchObject({
    rebases: 0,
    retests: 0,
    totalContention: 0,
  });
  await page.goto("/metrics");
  await expect(page.getByRole("heading", { name: "Throughput" })).toBeVisible();
  await expect(page.getByText("Landings & Lead Time")).toHaveCount(0);
});
