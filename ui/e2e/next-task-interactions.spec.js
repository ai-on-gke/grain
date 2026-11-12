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

// End-to-end interactive scenarios for /next/ over a real `grain demo`:
// Tasks 30/70 split, priority pill + Undo, backlog reorder, Inbox merge-item
// conversation & wake, conversation Expand / Full screen / Escape round trip,
// New task sheet, inline Edit, and Telemetry interactive chart hover/keys.

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

async function closeQuietly(baseURL, code, id) {
  if (!id) return;
  await fetch(`${baseURL}/api/tasks/${id}/close`, {
    method: "POST",
    headers: { Authorization: `Bearer ${code}` },
  }).catch(() => {});
}

test("Tasks split presets, GM3 priority pill + Undo, backlog reorder, New task and Edit", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  const ids = [];
  const t1 = `M14 interaction first ${Date.now()}`;
  const t2 = `M14 interaction second ${Date.now()}`;
  const createdTitle = `M14 sheet filed ${Date.now()}`;
  try {
    const r1 = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
      title: t1,
      approved: true,
    });
    const r2 = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
      title: t2,
      approved: true,
    });
    ids.push(r1.id, r2.id);

    await page.goto("/next/tasks");
    await page.waitForSelector(".main-stage > *:not(router-outlet)");

    // Exact Inbox-style split by default (30/70), switchable to 42/58 and collapsible.
    await expect(page.getByTestId("tasks-split-30")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    const w30 = (await page.locator("#tasks-list").boundingBox()).width;
    await page.getByTestId("tasks-split-42").click();
    await expect(page.getByTestId("tasks-split-42")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    const w42 = (await page.locator("#tasks-list").boundingBox()).width;
    expect(w42).toBeGreaterThan(w30 + 20);
    await page.getByTestId("tasks-split-30").click();

    // No native <select> elements anywhere on Tasks.
    expect(await page.locator("select").count()).toBe(0);

    // Select task #1 into the right inspector pane.
    await page.getByTestId(`open-${r1.id}`).click();
    await expect(page).toHaveURL(new RegExp(`task=${r1.id}`));
    await expect(page.getByTestId("tasks-pane")).toContainText(`#${r1.id}`);

    // Change priority via GM3 pill popover, then Undo from snackbar.
    const prioBtn = page.getByTestId(`prio-${r1.id}`);
    await prioBtn.click();
    await page.getByTestId(`prio-menu-${r1.id}-8`).click();
    await expect(prioBtn).toContainText("High 8");
    await expect(page.getByTestId("snackbar")).toBeVisible();
    await page.getByTestId("snackbar-action").click();
    await expect(prioBtn).toContainText("Normal 5");

    // Reorder task #2 via keyboard pickup bar.
    await page.getByTestId(`handle-${r2.id}`).click();
    await expect(page.getByTestId("pickup-bar")).toBeVisible();
    const reorderReq = page.waitForResponse(
      (res) => res.url().includes("/api/tasks/reorder") && res.status() === 200,
    );
    await page
      .getByTestId("pickup-bar")
      .getByRole("button", { name: "Top" })
      .click();
    await page.getByTestId("pickup-done").click();
    await reorderReq;

    // Inline Edit inside Tasks detail pane saves via PATCH /api/tasks/{id}.
    await page.getByTestId(`open-${r1.id}`).click();
    await page.getByTestId("act-edit").click();
    await page
      .getByTestId("edit-description")
      .fill("Updated from M14 scenario spec.");
    const patchReq = page.waitForResponse(
      (res) =>
        res.url().endsWith(`/api/tasks/${r1.id}`) &&
        res.request().method() === "PATCH" &&
        res.status() === 200,
    );
    await page.getByTestId("edit-save-btn").click();
    await patchReq;
    await page.getByTestId("task-tab-details").click();
    await expect(page.getByTestId("tasks-pane")).toContainText(
      "Updated from M14 scenario spec.",
    );

    // New Task FAB opens sheet and files a task via POST /api/tasks.
    await page.getByTestId("rail-new-task").click();
    await expect(page.getByTestId("new-task-sheet")).toBeVisible();
    await page.getByTestId("nt-title").fill(createdTitle);
    const createReq = page.waitForResponse(
      (res) =>
        res.url().endsWith("/api/tasks") &&
        res.request().method() === "POST" &&
        res.status() < 300,
    );
    await page.getByTestId("new-task-submit").click();
    const created = await (await createReq).json();
    if (created?.id) ids.push(created.id);
    await expect(page.getByTestId("new-task-sheet")).toHaveCount(0);
  } finally {
    for (const id of ids) await closeQuietly(demoServer, accessCode, id);
  }
});

test("Inbox conversation: Expand, Full screen + Escape draft round trip, composer containment, merge-item wake", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  const qTitle = `M14 convo question ${Date.now()}`;
  let qId = "";
  try {
    const qTask = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
      title: qTitle,
      approved: true,
    });
    qId = qTask.id;
    await apiJson(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${qId}/messages`,
      { summary: "Check Expand and Full screen", body: "Seeded message body." },
    );

    await page.goto("/next/inbox");
    await page.waitForSelector(".main-stage > *:not(router-outlet)");

    const row = page.getByTestId("queue-row").filter({ hasText: qTitle });
    await expect(row).toBeVisible();
    await row.locator(".row-main").click();

    // Composer stays inside its host card without horizontal overflow.
    const box = await page.evaluate(() => {
      const host = document
        .querySelector("g-conversation .convo-host")
        ?.getBoundingClientRect();
      const ta = document
        .querySelector('g-conversation [data-testid="task-composer"]')
        ?.getBoundingClientRect();
      return { host, ta };
    });
    expect(box.host).toBeTruthy();
    expect(box.ta).toBeTruthy();
    expect(box.ta.right).toBeLessThanOrEqual(box.host.right + 1);
    expect(box.ta.left).toBeGreaterThanOrEqual(box.host.left - 1);

    // Expand hides sibling evidence card, then collapses back.
    await page.getByTestId("convo-expand").click();
    await expect(page.locator("g-conversation.is-expanded")).toBeVisible();
    await page.getByTestId("convo-expand").click();
    await expect(page.locator("g-conversation.is-expanded")).toHaveCount(0);

    // Full screen opens native modal, keeps draft across Escape.
    await page.getByTestId("convo-fullscreen").click();
    const modal = page.getByTestId("convo-modal");
    await expect(modal).toBeVisible();
    await modal.getByTestId("task-composer").fill("Draft survives Escape");
    await page.keyboard.press("Escape");
    await expect(modal).not.toHaveAttribute("open", "");
    await expect(
      page.locator(".convo-host").getByTestId("task-composer"),
    ).toHaveValue("Draft survives Escape");

    // Seeded Merges group item (if present) supports Send & wake agent conversation.
    await page.getByTestId("filter-merges").click();
    const mergeRow = page.getByTestId("queue-row").first();
    if ((await mergeRow.count()) > 0) {
      await mergeRow.locator(".row-main").click();
      const convo = page.getByTestId("inspector-conversation");
      if ((await convo.count()) > 0) {
        const sendBtn = convo.getByTestId("act-send");
        if ((await sendBtn.count()) > 0 && !(await sendBtn.isDisabled())) {
          await convo.getByTestId("task-composer").fill("Re-run checks please");
          const commentReq = page.waitForResponse(
            (res) =>
              res.url().includes("/comments") &&
              res.request().method() === "POST",
          );
          await sendBtn.click();
          expect((await commentReq).status()).toBeLessThan(300);
        }
      }
    }
  } finally {
    await closeQuietly(demoServer, accessCode, qId);
  }
});

test("Telemetry chart timeline ticks and interactive hover / keyboard tooltip", async ({
  page,
}) => {
  test.setTimeout(90_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/next/telemetry");
  await page.waitForSelector('[data-testid="chart-throughput"]');

  // Time-axis labels render below the chart.
  const tickCount = await page
    .locator('[data-testid="chart-throughput"] .tel-axis-label')
    .count();
  expect(tickCount).toBeGreaterThanOrEqual(2);

  // Focus chart and step with ArrowRight -> tooltip opens, Escape closes.
  const chart = page.getByTestId("chart-throughput");
  await chart.focus();
  await page.keyboard.press("ArrowRight");
  const tip = page.getByTestId("tp-tip");
  await expect(tip).toBeVisible();
  await expect(tip).toContainText("Filed");
  await expect(tip).toContainText("Completed");
  await expect(tip).toContainText("Merged");
  await page.keyboard.press("Escape");
  await expect(tip).toHaveCount(0);
});
