// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { test as base, expect } from "./fixtures.js";

// Selection is explicit and confined to this worker's disposable demo. Unlike
// fixture overlays, these tests exercise its real persisted rows, collectors,
// evaluators, history and HTTP projection all the way into Angular.
const test = base.extend({
  demoArgs: [
    async ({}, use) => {
      const previous = process.env["GRAIN_GATE_OBSERVATION_TASKS"];
      process.env["GRAIN_GATE_OBSERVATION_TASKS"] = Array.from(
        { length: 32 },
        (_, i) => String(i + 1),
      ).join(",");
      try {
        await use([]);
      } finally {
        if (previous === undefined)
          delete process.env["GRAIN_GATE_OBSERVATION_TASKS"];
        else process.env["GRAIN_GATE_OBSERVATION_TASKS"] = previous;
      }
    },
    { scope: "worker" },
  ],
});

test("real persisted shadow results survive paging, reload and keyboard inspection @phone @dark", async ({
  page,
  axe,
}) => {
  const tasks = await (await page.request.get("/api/tasks")).json();
  const selected =
    tasks.find(
      (task: { id: string; state: string }) =>
        Number(task.id) >= 1 &&
        Number(task.id) <= 32 &&
        task.state === "queued",
    ) ?? tasks.find((task: { id: string }) => task.id === "1");
  expect(selected).toBeTruthy();
  const path = `/api/tasks/${selected.id}`;
  const response = await page.request.get(path);
  expect(response.ok()).toBeTruthy();
  const before = await response.json();
  expect(before.gate_observations.mode).toBe("observation_only");
  expect(before.gate_observations.authority).toBe("legacy");
  expect(before.gate_observation_diagnostics.evaluations).toBeGreaterThan(0);

  const mutations: string[] = [];
  page.on("request", (request) => {
    if (
      request.url().includes("/api/") &&
      !["GET", "HEAD"].includes(request.method())
    ) {
      mutations.push(request.method() + " " + new URL(request.url()).pathname);
    }
  });
  await page.goto(`/next/tasks/${selected.id}`);
  const card = page.getByTestId("gate-observations");
  await expect(card).toBeVisible();
  await expect(card).toContainText("Legacy services remain authoritative");
  for (const entry of before.gate_observations.results) {
    await expect(card).toContainText(
      entry.result.type + " v1 · " + entry.result.status,
    );
  }
  await expect(
    card.locator("button,a,input,select,textarea,[role=button]"),
  ).toHaveCount(0);
  const history = card.getByText(
    "Configuration, provenance and comparison history",
    { exact: true },
  );
  await history.focus();
  await page.keyboard.press("Enter");
  await expect(
    card.getByText("Source revision:", { exact: false }),
  ).toBeVisible();
  await expect(card).toContainText(before.gate_observations.source_revision);
  expect(await axe()).toEqual([]);
  const box = await card.boundingBox();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(
    page.viewportSize()!.width + 1,
  );

  // Comment/attempt pagination must not truncate the evaluator's source set
  // or produce a second evaluation from the same persisted source revision.
  const paged = await (
    await page.request.get(path + "?comments_limit=1&attempts_limit=1")
  ).json();
  expect(paged.gate_observations.source_revision).toBe(
    before.gate_observations.source_revision,
  );
  expect(paged.gate_observation_diagnostics.evaluations).toBe(
    before.gate_observation_diagnostics.evaluations,
  );
  await page.reload();
  await expect(card).toBeVisible();
  const after = await (await page.request.get(path)).json();
  expect(after.state).toBe(before.state);
  expect(after.attempts).toEqual(before.attempts);
  expect(after.closedAt).toEqual(before.closedAt);
  expect(after.failedAttempts).toEqual(before.failedAttempts);
  expect(after.gate_observation_diagnostics.evaluations).toBe(
    before.gate_observation_diagnostics.evaluations,
  );
  expect(mutations).toEqual([]);
});

test("a real comment event refreshes persisted predictions and Angular history", async ({
  page,
}) => {
  const tasks = await (await page.request.get("/api/tasks")).json();
  const selected = tasks.find(
    (task: { id: string; state: string }) =>
      Number(task.id) >= 1 && Number(task.id) <= 32 && task.state !== "closed",
  );
  expect(selected).toBeTruthy();
  const path = `/api/tasks/${selected.id}`;
  const before = await (await page.request.get(path)).json();
  await page.goto(`/next/tasks/${selected.id}`);
  await expect(page.getByTestId("gate-observations")).toBeVisible();

  // This is an explicit legacy fixture writer, separate from inspecting the
  // shadow card. Its persisted comment must invalidate the collection fence.
  const posted = await page.request.post(path + "/comments", {
    headers: { "Sec-Fetch-Site": "same-origin" },
    data: {
      body: "Qualification boundary feedback from the existing comment writer.",
    },
  });
  expect(posted.ok()).toBeTruthy();
  const written = await (await page.request.get(path)).json();
  expect(written.gate_observations.source_revision).not.toBe(
    before.gate_observations.source_revision,
  );
  expect(written.gate_observation_diagnostics.evaluations).toBe(
    before.gate_observation_diagnostics.evaluations + 1,
  );
  expect(
    written.gate_observation_diagnostics.history[0].view.source_revision,
  ).toBe(written.gate_observations.source_revision);

  await page.reload();
  const card = page.getByTestId("gate-observations");
  await expect(card).toBeVisible();
  await card
    .getByText("Configuration, provenance and comparison history", {
      exact: true,
    })
    .click();
  await expect(card).toContainText(written.gate_observations.source_revision);
  await expect(
    page.getByText(
      "Qualification boundary feedback from the existing comment writer.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    card.locator("button,a,input,select,textarea,[role=button]"),
  ).toHaveCount(0);
  const inspected = await (await page.request.get(path)).json();
  expect(inspected.state).toBe(written.state);
  expect(inspected.attempts).toEqual(written.attempts);
  expect(inspected.failedAttempts).toEqual(written.failedAttempts);
  expect(inspected.gate_observation_diagnostics.evaluations).toBe(
    written.gate_observation_diagnostics.evaluations,
  );
});

test("standalone /next/task and split /next/tasks preserve heading order for gate observations", async ({
  page,
}) => {
  const { default: AxeBuilder } = await import("@axe-core/playwright");
  const tasks = await (await page.request.get("/api/tasks")).json();
  const selected = tasks.find(
    (task: { id: string }) => Number(task.id) >= 1 && Number(task.id) <= 32,
  );
  expect(selected).toBeTruthy();

  for (const url of [
    `/next/task/${selected.id}`,
    `/next/tasks/${selected.id}`,
  ]) {
    await page.goto(url);
    const card = page.getByTestId("gate-observations");
    await expect(card).toBeVisible();
    const result = await new AxeBuilder({ page })
      .withRules(["heading-order"])
      .include('[data-testid="gate-observations"]')
      .analyze();
    expect(result.violations, url).toEqual([]);
  }
});
