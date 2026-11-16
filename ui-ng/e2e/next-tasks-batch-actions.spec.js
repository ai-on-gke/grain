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

// The /next/tasks selection bar's batch actions that only classic had
// (docs/test-runs/2026-11-15-task-intake-inbox-lifecycle.md, "Gaps on
// /next/", F2): Run selected now and the manual-mode "waits for Run now"
// row marker, stopping a bulk action part-way, Bulk edit's free Task
// priority, capability attach/detach, and Submit offered by state. Each
// asserts what the server stored, not only what the page drew.
//
// It pauses the deployment and spends Run now requests nothing can take
// back, so it gets a demo of its own.
test.use({ demoArgs: ["-open=false", "-as", "next-tasks-batch-actions"] });
test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

async function api(baseURL, code, method, p, body) {
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

async function listed(demoServer, code) {
  const tasks = await api(demoServer, code, "GET", "/api/tasks?fields=full");
  return Array.isArray(tasks) ? tasks : (tasks.tasks ?? []);
}

// queued files a task and approves it, so it is one the queue would pick
// up but for the pause.
async function queued(demoServer, code, title, extra = {}) {
  const t = await api(demoServer, code, "POST", "/api/tasks", {
    title,
    description: "selection bar walkthrough",
    ...extra,
  });
  await api(demoServer, code, "POST", `/api/tasks/${t.id}/approve`);
  return t;
}

// accept confirms the shared dialog, ticking its "someone else's work"
// box when the scope it names asks for one.
async function accept(dialog, name) {
  // The box renders with the button, so wait for the button before asking
  // whether there is a box: counting first races the dialog opening, finds
  // no box, and then waits on a button the unticked box keeps disabled.
  const button = dialog.getByRole("button", { name, exact: true });
  await expect(button).toBeVisible();
  const tick = dialog.getByTestId("confirm-multi-repo");
  if (await tick.count()) await tick.check();
  await button.click();
}

const row = (page, id) =>
  page.locator(".task-row", { has: page.getByTestId(`open-${id}`) });
const tick = (page, id) =>
  page.getByRole("checkbox", { name: `Select #${id}`, exact: true });

test("Run selected now under a pause, the rows that wait for it, and Stop part-way @phone", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const stamp = Date.now();
  const made = [];
  for (const n of ["A", "B", "C"])
    made.push(
      await queued(demoServer, accessCode, `Manual ${n} ${stamp}`, {
        priority: 9,
      }),
    );
  const blocked = await queued(
    demoServer,
    accessCode,
    `Manual blocked ${stamp}`,
    { dependsOn: [made[0].id], priority: 9 },
  );

  const paused = await api(
    demoServer,
    accessCode,
    "POST",
    "/api/pipeline-pause",
    {
      reason: `hand-picking ${stamp}`,
    },
  );
  expect(paused.pause.runNowPasses).toBe(true);
  try {
    await page.goto("tasks?tab=backlog");
    for (const t of made)
      await expect(row(page, t.id).getByTestId("waits-run-now")).toBeVisible();
    // A blocked task waits for its blocker, not for the pause.
    await expect(row(page, blocked.id)).toBeVisible();
    await expect(
      row(page, blocked.id).getByTestId("waits-run-now"),
    ).toHaveCount(0);

    // Two rows: the confirm dialog names the pause, then both are asked
    // for and the selection clears.
    await tick(page, made[1].id).check();
    await tick(page, made[2].id).check();
    const bar = page.getByTestId("batch-bar");
    await expect(bar).toContainText("2 selected");
    // The bar fits the screen at every width: it wraps rather than running
    // its new buttons off the right edge, and nothing scrolls sideways.
    const vw = page.viewportSize().width;
    const barBox = await bar.boundingBox();
    expect(barBox.x).toBeGreaterThanOrEqual(0);
    expect(barBox.x + barBox.width).toBeLessThanOrEqual(vw + 0.5);
    for (const control of await bar.locator("button").all()) {
      const box = await control.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(barBox.x - 0.5);
      expect(box.x + box.width).toBeLessThanOrEqual(
        barBox.x + barBox.width + 0.5,
      );
      expect(box.y).toBeGreaterThanOrEqual(barBox.y - 0.5);
      expect(box.y + box.height).toBeLessThanOrEqual(
        barBox.y + barBox.height + 0.5,
      );
    }
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await bar.getByTestId("batch-run-now").click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toContainText("Start 2 tasks now");
    await expect(dialog).toContainText("The deployment is paused");
    await accept(dialog, "Run 2");
    await expect(bar).toHaveCount(0);
    await expect(page.getByText(/Asked 2 tasks to run/)).toBeVisible();
    let rows = await listed(demoServer, accessCode);
    for (const t of made.slice(1))
      expect(rows.find((r) => r.id === t.id).runNowAt).toBeTruthy();
    expect(rows.find((r) => r.id === made[0].id).runNowAt).toBeFalsy();
    // The marker does not come back once a task has runNowAt.
    await page.reload();
    await expect(row(page, made[1].id)).toBeVisible();
    for (const t of made.slice(1))
      await expect(row(page, t.id).getByTestId("waits-run-now")).toHaveCount(0);
    await expect(
      row(page, made[0].id).getByTestId("waits-run-now"),
    ).toBeVisible();

    // Stop part-way: six rows, each run-now call held back a moment, so
    // Stop lands while the first four are in flight and the last two are
    // never sent. They stay ticked; the server has runNowAt on four.
    const six = [];
    for (let i = 0; i < 6; i++)
      six.push(
        await queued(demoServer, accessCode, `Stop part-way ${i} ${stamp}`, {
          priority: 10,
        }),
      );
    await page.route("**/api/tasks/*/run-now", async (route) => {
      await new Promise((r) => setTimeout(r, 1500));
      await route.continue();
    });
    await page.reload();
    for (const t of six) await tick(page, t.id).check();
    await expect(bar).toContainText("6 selected");
    await bar.getByTestId("batch-run-now").click();
    await accept(dialog, "Run 6");
    await expect(bar.getByTestId("batch-progress")).toContainText(
      /Starting \d of 6/,
    );
    await bar.getByTestId("batch-stop").click();
    await expect(bar.getByTestId("batch-progress")).toHaveCount(0);
    await expect(bar).toContainText("2 selected");
    await expect(
      page.getByText(/Stopped after 4 of 6; 2 left unchanged/),
    ).toBeVisible();
    rows = await listed(demoServer, accessCode);
    const asked = six.filter((t) => rows.find((r) => r.id === t.id).runNowAt);
    expect(asked).toHaveLength(4);
    for (const t of six.slice(4)) {
      expect(rows.find((r) => r.id === t.id).runNowAt).toBeFalsy();
      await expect(tick(page, t.id)).toBeChecked();
    }
    await page.unroute("**/api/tasks/*/run-now");
  } finally {
    await api(demoServer, accessCode, "DELETE", "/api/pipeline-pause");
  }
});

test("Bulk edit sets a free Task priority, and capabilities attach and detach", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  const stamp = Date.now();
  const x = await queued(demoServer, accessCode, `Bulk X ${stamp}`);
  const y = await queued(demoServer, accessCode, `Bulk Y ${stamp}`);
  await page.goto("tasks?tab=backlog");
  const bar = page.getByTestId("batch-bar");

  // Bulk edit: 2.5 is refused beside the field (1e3 is read as 1000, as
  // classic reads it: next-tasks-intake-regressions.spec.js); 12 lands
  // on both.
  await tick(page, x.id).check();
  await tick(page, y.id).check();
  await bar.getByTestId("batch-more").click();
  await page.getByRole("option", { name: "Bulk edit…" }).click();
  const form = page.getByTestId("batch-edit-dialog");
  await expect(form).toContainText("Edit 2 tasks");
  expect(await axe()).toEqual([]);
  await form.getByTestId("bulk-priority").fill("2.5");
  await expect(form.getByTestId("bulk-priority-error")).toBeVisible();
  await expect(form.getByTestId("batch-edit-apply")).toBeDisabled();
  await form.getByTestId("bulk-priority").fill("12");
  await expect(form.getByTestId("bulk-priority")).not.toHaveAttribute(
    "aria-invalid",
    "true",
  );
  await form.getByTestId("batch-edit-apply").click();
  await expect(form).toHaveCount(0);
  await expect(bar).toHaveCount(0);
  await expect(page.getByText(/Edited 2 tasks/)).toBeVisible();
  for (const t of [x, y]) {
    const d = await api(demoServer, accessCode, "GET", `/api/tasks/${t.id}`);
    expect(d.priority).toBe(12);
  }

  // Capability: attach one these tasks lack, then detach it, which asks
  // first in the confirm dialog. The server stores a capability under its
  // canonical reference (https://grain.dev/capabilities/api#task-admin
  // for api:task-admin), so one is matched on the name after its last
  // separator.
  const tail = (id) => id.split("=")[0].split(/[:#/]/).pop();
  const heldBy = async (t) =>
    (
      (await api(demoServer, accessCode, "GET", `/api/tasks/${t.id}`))
        .capabilities ?? []
    ).map(tail);
  const before = await heldBy(x);
  const cfg = await api(demoServer, accessCode, "GET", "/api/config");
  const cap = (cfg.capabilities ?? []).find(
    (c) => c.ready !== false && c.id && !before.includes(tail(c.id)),
  );
  test.skip(!cap, "the demo configures no capability these tasks lack");
  const holds = async (t) => (await heldBy(t)).includes(tail(cap.id));
  await tick(page, x.id).check();
  await tick(page, y.id).check();
  await bar.getByTestId("batch-more").click();
  await page.getByRole("option", { name: "Capability…" }).click();
  await form.getByTestId("bulk-capability").click();
  await page
    .getByRole("option", { name: cap.name || cap.id, exact: true })
    .click();
  await form.getByTestId("batch-capability-attach").click();
  await expect(bar).toHaveCount(0);
  expect(await holds(x)).toBe(true);
  expect(await holds(y)).toBe(true);

  await tick(page, x.id).check();
  await tick(page, y.id).check();
  await bar.getByTestId("batch-more").click();
  await page.getByRole("option", { name: "Capability…" }).click();
  await form.getByTestId("bulk-capability").click();
  await page
    .getByRole("option", { name: cap.name || cap.id, exact: true })
    .click();
  await form.getByTestId("batch-capability-detach").click();
  const confirm = page.getByTestId("confirm-dialog");
  await expect(confirm).toContainText("Detach capability");
  await accept(confirm, "Detach");
  await expect(bar).toHaveCount(0);
  expect(await holds(x)).toBe(false);
  expect(await holds(y)).toBe(false);
});

test("Submit is offered only for changes ready to submit and asks first", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const rows = await listed(demoServer, accessCode);
  const ready = rows.find((t) => t.state === "awaiting_submit");
  test.skip(!ready, "the demo seeds no task waiting to be submitted");
  const queuedRow = await queued(
    demoServer,
    accessCode,
    `Not ready ${Date.now()}`,
  );

  // A queued task has nothing to submit: More does not offer it.
  await page.goto("tasks?tab=backlog");
  await tick(page, queuedRow.id).check();
  const bar = page.getByTestId("batch-bar");
  await bar.getByTestId("batch-more").click();
  await expect(page.getByRole("option", { name: /^Submit/ })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await bar.getByRole("button", { name: "Clear" }).click();

  await page.goto("tasks?tab=merging");
  await tick(page, ready.id).check();
  await bar.getByTestId("batch-more").click();
  await page.getByRole("option", { name: /^Submit/ }).click();
  const confirm = page.getByTestId("confirm-dialog");
  await expect(confirm).toContainText(`Submit #${ready.id}?`);
  await expect(confirm.getByTestId("confirm-scope")).toContainText(
    ready.repo ?? "",
  );
  // Cancel leaves the task and the selection as they were.
  await confirm.getByTestId("confirm-cancel").click();
  await expect(confirm).toHaveCount(0);
  await expect(bar).toContainText("1 selected");
  const after = await api(
    demoServer,
    accessCode,
    "GET",
    `/api/tasks/${ready.id}`,
  );
  expect(after.state).toBe("awaiting_submit");
});
