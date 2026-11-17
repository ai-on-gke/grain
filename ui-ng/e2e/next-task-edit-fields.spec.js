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

// The task fields and lifecycle controls the 2026-11-15 task intake
// walkthrough found missing from /next/ (docs/test-runs/
// 2026-11-15-task-intake-inbox-lifecycle.md, steps 4.9, 4.14 and 4.15):
// Owner, External ref, Reviewers, Keep running, Admission, "This fixes a
// red <base>", a Task priority that takes any whole number, and a reason
// on Close -- each written to the API the classic UI writes, and each
// still there after a reload.

async function apiJson(baseURL, code, method, p, body) {
  const res = await fetch(`${baseURL}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${p}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

async function firstTask(demoServer, accessCode, pred) {
  const tasks = await apiJson(demoServer, accessCode, "GET", "/api/tasks");
  const list = Array.isArray(tasks) ? tasks : (tasks.tasks ?? []);
  const t = list.find(pred);
  expect(t, "the demo seeds a task to edit").toBeTruthy();
  return t;
}

test("edits owner, ref, reviewers, admission, keep running and priority @phone @dark", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  const t = await firstTask(
    demoServer,
    accessCode,
    (x) => x.state === "queued" && x.repo,
  );
  const before = await apiJson(
    demoServer,
    accessCode,
    "GET",
    `/api/tasks/${t.id}`,
  );
  try {
    await page.goto(`task/${t.id}`);
    await page.getByTestId("act-edit").click();
    const panel = page.getByTestId("task-edit-panel");
    await expect(panel).toBeVisible();

    // Zero, negatives and fractions are refused in the classic wording,
    // and Save waits for a whole number.
    const priority = panel.getByTestId("edit-priority");
    const save = panel.getByTestId("edit-save-btn");
    for (const bad of ["0", "-3", "2.5"]) {
      await priority.fill(bad);
      await expect(panel.getByTestId("edit-priority-help")).toHaveText(
        "a whole number, 1 or more; default 5, higher numbers run first",
      );
      await expect(save).toBeDisabled();
    }
    await priority.fill("1e3");
    await expect(save).toBeEnabled();

    await panel.getByTestId("edit-owner").fill("e2e-owner@example.com");
    await panel.getByTestId("edit-external-ref").fill("CA-204");
    await panel.getByTestId("edit-reviewers").fill("carol, dave");
    await panel.getByTestId("edit-keep-running").check();
    await panel.getByTestId("edit-fixes-base").check();
    await panel.getByTestId("edit-admission").click();
    await panel.getByTestId("edit-admission-opt-bypass").click();
    await expect(panel.getByTestId("edit-admission-help")).toContainText(
      "Admitted past the worker and merger limits",
    );

    // The panel and every field in it stay inside the page at each width.
    const vw = page.viewportSize().width;
    for (const id of ["edit-owner", "edit-reviewers", "edit-admission"]) {
      const box = await panel.getByTestId(id).boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(vw + 0.5);
    }
    expect(await axe()).toEqual([]);

    const [req] = await Promise.all([
      page.waitForRequest(
        (r) => r.method() === "PATCH" && r.url().endsWith(`/api/tasks/${t.id}`),
      ),
      save.click(),
    ]);
    const body = req.postDataJSON();
    expect(body).toMatchObject({
      priority: 1000,
      owner: "e2e-owner@example.com",
      externalRef: "CA-204",
      reviewers: ["carol", "dave"],
      keepRunning: true,
      fixesBaseBranch: true,
      admission: "bypass",
    });
    expect(body).not.toHaveProperty("autoMerge");
    await expect(panel).toBeHidden();

    // A reload reads every value back from the server.
    await page.reload();
    await page.getByTestId("act-edit").click();
    await expect(panel.getByTestId("edit-priority")).toHaveValue("1000");
    await expect(panel.getByTestId("edit-owner")).toHaveValue(
      "e2e-owner@example.com",
    );
    await expect(panel.getByTestId("edit-external-ref")).toHaveValue("CA-204");
    await expect(panel.getByTestId("edit-reviewers")).toHaveValue(
      "carol, dave",
    );
    await expect(panel.getByTestId("edit-keep-running")).toBeChecked();
    await expect(panel.getByTestId("edit-fixes-base")).toBeChecked();
    await expect(panel.getByTestId("edit-admission")).toContainText(
      "Bypass queue",
    );
  } finally {
    await apiJson(demoServer, accessCode, "PATCH", `/api/tasks/${t.id}`, {
      priority: before.priority || 5,
      ...(before.owner ? { owner: before.owner } : {}),
      externalRef: before.externalRef || "",
      reviewers: before.reviewers || [],
      keepRunning: !!before.keepRunning,
      fixesBaseBranch: !!before.fixesBaseBranch,
      admission: before.admission || "standard",
    });
  }
});

test("Close on a running task takes a reason", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const t = await firstTask(
    demoServer,
    accessCode,
    (x) => x.state === "running",
  );
  // The close is answered here rather than by the demo, which this
  // worker's other specs share: what is under test is the request the
  // page sends, and the running task stays running for them.
  let sent = null;
  await page.route(`**/api/tasks/${t.id}/close`, async (route) => {
    sent = route.request().postDataJSON();
    await route.fulfill({ status: 204, body: "" });
  });
  await page.goto(`task/${t.id}`);
  await page.getByTestId("act-close").click();
  const confirm = page.getByTestId("close-confirm");
  await expect(confirm).toBeVisible();
  await expect(confirm.locator('[role="alert"]')).toContainText(
    "stops the run",
  );
  await confirm.getByTestId("close-reason").fill("superseded by e2e");
  await confirm.getByTestId("close-yes").click();
  await expect.poll(() => sent).toMatchObject({ reason: "superseded by e2e" });
});

test("Close on a queued task takes a reason and Reopen restores it @phone @dark", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  const created = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
    title: "Queued task for reversible close reason check",
    description: "Created by next-task-edit-fields to verify reversible Close.",
    approve: true,
    no_repo: true,
  });
  try {
    await page.goto(`task/${created.id}`);
    await page.getByTestId("act-close").click();

    const confirm = page.getByTestId("close-confirm");
    await expect(confirm).toBeVisible();
    await expect(confirm.locator('[role="alert"]')).toHaveCount(0);

    // Every control in the inline close row fits within the viewport and aligns on one row when wide enough.
    const vw = page.viewportSize().width;
    const reasonInput = confirm.getByTestId("close-reason");
    const closeYes = confirm.getByTestId("close-yes");
    const closeKeep = confirm.getByTestId("close-keep");
    for (const loc of [reasonInput, closeYes, closeKeep]) {
      const box = await loc.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(vw + 0.5);
    }
    if (vw >= 600) {
      const rBox = await reasonInput.boundingBox();
      const yBox = await closeYes.boundingBox();
      expect(
        Math.abs(rBox.y + rBox.height / 2 - (yBox.y + yBox.height / 2)),
      ).toBeLessThanOrEqual(2);
    }
    expect(await axe()).toEqual([]);

    await reasonInput.fill("duplicate of #19");
    const [closeReq] = await Promise.all([
      page.waitForRequest(
        (r) =>
          r.method() === "POST" &&
          r.url().endsWith(`/api/tasks/${created.id}/close`),
      ),
      closeYes.click(),
    ]);
    expect(closeReq.postDataJSON()).toEqual({
      close_pull_request: false,
      reason: "duplicate of #19",
    });

    await expect(page.getByTestId("act-reopen")).toBeVisible();
    await expect(page.getByTestId("snackbar-action")).toHaveText("Undo");

    // Reload and verify the recorded close reason persists in History.
    await page.reload();
    if (vw < 1080) {
      await page.getByTestId("task-tab-details").click();
    }
    await expect(page.locator(".history:visible")).toContainText(
      "duplicate of #19",
    );

    await page.getByTestId("act-reopen").click();
    await expect(page.getByTestId("act-close")).toBeVisible();
  } finally {
    await apiJson(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${created.id}/close`,
      {},
    ).catch(() => {});
  }
});
