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

// The config-change card (PendingConfigChange.jsx), end to end: `grain
// demo -config-change` serves a real, throwaway grain/config and parks two
// tasks on real proposal branches of it, pushed and validated the way a
// session's propose_config_change does. Before it, both buttons answered
// 501 -- the demo had no config repository to decide against
// (docs/test-runs/2026-11-08-task-intake-inbox-lifecycle-task-3611.md,
// step 36).
test.use({ demoArgs: ["-config-change"] });

const POLL_TITLE = "Ease off polling between reconcile cycles";
const PROMPT_TITLE = "Remind every run to run the unit tests";

async function taskNamed(request, title) {
  const res = await request.get("/api/tasks");
  expect(res.ok()).toBeTruthy();
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const task = tasks.find((t) => t.title === title);
  expect(task, `no seeded task titled ${title}`).toBeTruthy();
  const detail = await request.get(`/api/tasks/${task.id}`);
  expect(detail.ok()).toBeTruthy();
  return detail.json();
}

async function grainConfigOnMain(request) {
  const res = await request.get(
    "/api/state-repo/file?path=tables/grain_config.json",
  );
  expect(res.ok()).toBeTruthy();
  return (await res.json()).content;
}

test("Approve & Apply merges the proposed head into grain/config and the card leaves", async ({
  page,
}) => {
  const task = await taskNamed(page.request, POLL_TITLE);
  const pending = task.pendingConfigChange;
  expect(pending.branch).toBe(`grain/config/${task.id}/poll-interval`);
  expect(pending.validation.ok).toBe(true);
  const before = await (await page.request.get("/api/state-repo")).json();
  expect(before.repository).toBe("grain/config");
  expect(await grainConfigOnMain(page.request)).toContain(
    '"poll_interval_ms": 10000',
  );

  await page.goto(`/tasks/${task.id}`);
  const card = page.getByRole("region", {
    name: "Config change waiting for your approval",
  });
  await expect(card).toBeVisible();
  await expect(card.getByText(pending.summary)).toBeVisible();
  await expect(card.getByText(pending.branch)).toBeVisible();
  await expect(card.getByTestId("config-change-validation")).toContainText(
    "Schema valid",
  );
  await expect(
    card.getByText('"poll_interval_ms": 30000').first(),
  ).toBeVisible();

  const approved = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/tasks/${task.id}/config-change/approve`) &&
      r.request().method() === "POST",
  );
  await card.getByRole("button", { name: "Approve & Apply" }).click();
  const response = await approved;
  expect(response.status()).toBe(200);
  expect(response.request().postDataJSON()).toEqual({
    headSha: pending.headSha,
  });

  await expect(card).toHaveCount(0);
  await expect(
    page
      .getByText(
        `Approved and applied config change (${pending.headSha.slice(0, 12)})`,
      )
      .first(),
  ).toBeVisible();

  // grain/config's main moved past the proposal and now holds its edit,
  // and the task no longer offers it.
  const after = await (await page.request.get("/api/state-repo")).json();
  expect(after.head).not.toBe(before.head);
  expect(after.head).not.toBe(pending.headSha);
  expect(await grainConfigOnMain(page.request)).toContain(
    '"poll_interval_ms": 30000',
  );
  expect(
    (await taskNamed(page.request, POLL_TITLE)).pendingConfigChange,
  ).toBeFalsy();

  // The decision was about that commit, and it is gone: deciding again
  // is refused rather than merged twice.
  const again = await page.request.post(
    `/api/tasks/${task.id}/config-change/approve`,
    { data: { headSha: pending.headSha } },
  );
  expect(again.status()).toBe(400);
});

test("Reject deletes the proposal, hands the reason back and the card leaves", async ({
  page,
}) => {
  const task = await taskNamed(page.request, PROMPT_TITLE);
  const pending = task.pendingConfigChange;
  expect(pending.branch).toBe(`grain/config/${task.id}/prompt-extension`);
  const reason = `Not every repository has unit tests ${Date.now()}`;

  await page.goto(`/tasks/${task.id}`);
  const card = page.getByRole("region", {
    name: "Config change waiting for your approval",
  });
  await expect(card).toBeVisible();
  await expect(card.getByText(pending.summary)).toBeVisible();

  await card.getByRole("button", { name: "Reject", exact: true }).click();
  await card.getByLabel("Why are you rejecting it?").fill(reason);
  const rejected = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/tasks/${task.id}/config-change/reject`) &&
      r.request().method() === "POST",
  );
  await card.getByRole("button", { name: "Reject change" }).click();
  const response = await rejected;
  expect(response.status()).toBe(200);
  expect(response.request().postDataJSON()).toEqual({
    headSha: pending.headSha,
    reason,
  });

  await expect(card).toHaveCount(0);
  await expect(
    page
      .getByText(
        `Rejected config change (${pending.headSha.slice(0, 12)}): ${reason}`,
      )
      .first(),
  ).toBeVisible();

  // Nothing of it reached grain/config's main, and the task no longer
  // offers it.
  expect(await grainConfigOnMain(page.request)).toContain(
    '"prompt_extension": ""',
  );
  expect(
    (await taskNamed(page.request, PROMPT_TITLE)).pendingConfigChange,
  ).toBeFalsy();
});
