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

import { execFileSync } from "node:child_process";
import { test, expect } from "./fixtures.js";

// The config-change card (PendingConfigChange.jsx), end to end: `grain
// demo -config-change` serves a real, throwaway grain/config and parks two
// tasks on real proposal branches of it, pushed and validated the way a
// session's propose_config_change does. Before it, both buttons answered
// 501 -- the demo had no config repository to decide against
// (docs/test-runs/2026-11-08-task-intake-inbox-lifecycle-task-3611.md,
// step 36).
//
// -github-sim -git-proxy too, so grain/config is also reachable the way a
// session reaches it: through grain's git proxy, which the demo forwarded
// to the simulated GitHub instead, answering 404
// (docs/test-runs/2026-11-09-demo-config-change.md, finding 1).
test.use({ demoArgs: ["-config-change", "-github-sim", "-git-proxy"] });

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

// A role without settings:write is refused both decisions by the server
// (pkg/ui/access_roles.go), so the card does not offer them: both buttons
// are drawn disabled inside AccessGate's wrapper, which names the missing
// scope. Only this page is told it is a viewer (role-gated-actions.spec.js).
test("a viewer is shown the change but offered neither decision", async ({
  page,
}) => {
  await page.route(
    (u) => u.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      const json = {
        ...(await response.json()),
        access: {
          role: "grain/viewer",
          roles: ["grain/viewer"],
          full: false,
          scopes: ["settings:read", "tasks:read"],
        },
      };
      await route.fulfill({ response, json });
    },
  );
  // First in the file, so the seeded change is still waiting: the specs
  // below decide both.
  const task = await taskNamed(page.request, POLL_TITLE);
  expect(task.pendingConfigChange).toBeTruthy();

  await page.goto(`/tasks/${task.id}`);
  const card = page.getByRole("region", {
    name: "Config change waiting for your approval",
  });
  await expect(card).toBeVisible();
  for (const name of ["Approve & Apply", "Reject"]) {
    await expect(
      card.getByRole("button", { name, exact: true }),
    ).toBeDisabled();
    await expect(
      card.locator('[data-access-denied="settings:write"]', {
        has: page.getByRole("button", { name, exact: true }),
      }),
    ).toHaveCount(1);
  }
  await card.locator('[data-access-denied="settings:write"]').first().hover();
  await expect(page.getByRole("tooltip")).toHaveText(
    "Needs settings:write, which your role grain/viewer does not grant.",
  );
});

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

test("the demo's git proxy serves grain/config as a session reaches it", async ({
  request,
}) => {
  const filed = await request.post("/api/tasks", {
    data: {
      title: `Read grain/config through the proxy ${Date.now()}`,
      repo: "grain/config",
      approved: true,
    },
  });
  expect(filed.ok()).toBeTruthy();
  const task = await filed.json();
  const started = await request.post("/api/e2e/git-proxy/runs", {
    data: { taskId: task.id },
  });
  expect(started.ok()).toBeTruthy();
  const run = await started.json();
  expect(run.remote).toMatch(/\/grain\/config\.git$/);

  const auth = `http.extraHeader=Authorization: Basic ${Buffer.from(
    `x:${run.token}`,
  ).toString("base64")}`;
  const refs = execFileSync("git", ["-c", auth, "ls-remote", run.remote], {
    encoding: "utf8",
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0" },
  });
  const status = await (await request.get("/api/state-repo")).json();
  expect(refs).toContain(`${status.head}\trefs/heads/main`);
});
