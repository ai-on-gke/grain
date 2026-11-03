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
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures.js";
import { alertRow, openAlerts } from "./alerts.js";

// docs/test-plans/2026-10-25.md §1 (grain/task-927,
// docs/test-runs/2026-10-25-dispatch-control-manual-mode.md): manual mode
// and dispatch-control, walked against the real `grain demo` server. What
// shipped is docs/manual-mode.md: pause the deployment, then Run now (the
// pane's button, the list's "Run selected now") starts exactly the tasks
// picked. dispatch-control is the agent's half: the api:dispatch-control
// preset, whose model-only routes `grain mcpserver` serves as
// api_pause_pipeline, api_resume_pipeline and api_run_now (defer is
// tasks:write, which a run reaches through `grain api` in its sandbox),
// driven over stdio here the way a run's agent drives it.
//
// demo has no orchestrator, so nothing is dispatched: what these assert
// is the request (runNowAt) and every page that reports it.
//
// A distinct demoArgs gives this file a demo of its own: it pauses the
// deployment and spends Run now requests nothing can take back.
test.use({
  demoArgs: ["-open=false", "-as", "manual-mode-walkthrough"],
});
test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

async function ok(res, what) {
  expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
  return res.json();
}

// queued files a task and approves it, so it is one the queue would pick
// up but for the pause.
async function queued(request, title, extra = {}) {
  const task = await ok(
    await request.post("/api/tasks", {
      data: { title, description: "manual-mode walkthrough", ...extra },
    }),
    `create ${title}`,
  );
  await ok(
    await request.post(`/api/tasks/${task.id}/approve`),
    `approve ${task.id}`,
  );
  return task;
}

async function seededIn(request, state) {
  const rows = await ok(await request.get("/api/tasks"), "list");
  const row = rows.find((t) => t.state === state);
  expect(row, `a seeded ${state} task`).toBeTruthy();
  return row;
}

const row = (page, title) => page.locator(".task-row", { hasText: title });
const chip = (page, title) =>
  row(page, title).locator(".chip-held-for-run-now");

test("Run now and Run selected now under a pause, and the rows that say so", async ({
  page,
  request,
}) => {
  const stamp = Date.now();
  const a = await queued(request, `Manual A ${stamp}`);
  const b = await queued(request, `Manual B ${stamp}`);
  const c = await queued(request, `Manual C ${stamp}`);
  const blocked = await queued(request, `Manual blocked ${stamp}`, {
    dependsOn: [a.id],
  });
  const running = await seededIn(request, "running");
  const closed = await seededIn(request, "closed");

  const paused = await ok(
    await request.post("/api/pipeline-pause", {
      data: { reason: `hand-picking ${stamp}` },
    }),
    "pause",
  );
  expect(paused.pause.runNowPasses).toBe(true);
  try {
    // The pause is the deployment's alert, worded for manual mode.
    await page.goto("/");
    await openAlerts(page);
    await expect(
      alertRow(page, "Paused — only tasks you Run now will start"),
    ).toBeVisible();
    await page.keyboard.press("Escape");

    // Every queued task waits for Run now -- but not the blocked one,
    // which waits for its blocker, and not the running one.
    for (const t of [a, b, c]) await expect(chip(page, t.title)).toBeVisible();
    await expect(row(page, blocked.title)).toBeVisible();
    await expect(chip(page, blocked.title)).toHaveCount(0);
    await expect(chip(page, running.title)).toHaveCount(0);

    // Run now from the task's own pane.
    await page.goto(`/tasks/${a.id}`);
    await expect(page.locator(".held-for-run-now-hint")).toBeVisible();
    const runNow = page.getByRole("button", { name: "Run now", exact: true });
    const [answer] = await Promise.all([
      page.waitForResponse(
        (r) =>
          r.url().endsWith(`/api/tasks/${a.id}/run-now`) &&
          r.request().method() === "POST",
      ),
      runNow.click(),
    ]);
    expect(answer.status()).toBe(200);
    expect((await answer.json()).runNowAt).toBeTruthy();
    await expect(page.getByText("Asked for a run.")).toBeVisible();
    await expect(runNow).toBeDisabled();
    await expect(page.locator(".held-for-run-now-hint")).toHaveCount(0);

    // A running task's pane offers no Run now at all.
    await page.goto(`/tasks/${running.id}`);
    await expect(page.getByText(running.title).first()).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Run now", exact: true }),
    ).toHaveCount(0);

    // Back on the list the row no longer asks for what was just done
    // (the regression: runNowAt was on the pane's shape only).
    await page.goto("/");
    await expect(row(page, a.title)).toBeVisible();
    await expect(chip(page, a.title)).toHaveCount(0);

    // Run selected now, over two rows.
    await row(page, b.title).getByRole("checkbox").check();
    await row(page, c.title).getByRole("checkbox").check();
    await expect(page.getByText("2 selected")).toBeVisible();
    const posted = [];
    page.on("response", (r) => {
      if (r.url().endsWith("/run-now") && r.request().method() === "POST")
        posted.push([r.url(), r.status()]);
    });
    await page
      .getByRole("button", { name: "Run selected now", exact: true })
      .click();
    await expect.poll(() => posted.length).toBe(2);
    expect(posted.every(([, status]) => status === 200)).toBe(true);
    await expect(chip(page, b.title)).toHaveCount(0);
    await expect(chip(page, c.title)).toHaveCount(0);
    const rows = await ok(await request.get("/api/tasks"), "list");
    for (const t of [a, b, c])
      expect(rows.find((r) => r.id === t.id).runNowAt).toBeTruthy();
    expect(rows.find((r) => r.id === blocked.id).runNowAt).toBeFalsy();

    // Refusals: a running task and a closed one.
    const onRunning = await request.post(`/api/tasks/${running.id}/run-now`);
    expect(onRunning.status()).toBe(400);
    expect((await onRunning.json()).error).toMatch(/already running/);
    const onClosed = await request.post(`/api/tasks/${closed.id}/run-now`);
    expect(onClosed.status()).toBe(400);
    expect((await onClosed.json()).error).toMatch(/is closed/);
  } finally {
    await request.delete("/api/pipeline-pause");
  }
});

// mcpCalls runs one `grain mcpserver` for task taskID holding
// api:dispatch-control and makes each of calls over JSON-RPC stdio, returning
// each call's result.
function mcpCalls({ url, code, taskID, sandboxRoot, calls }) {
  const lines = [
    {
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: {
        protocolVersion: "2024-11-05",
        capabilities: {},
        clientInfo: { name: "manual-mode-walkthrough", version: "1" },
      },
    },
    { jsonrpc: "2.0", method: "notifications/initialized" },
    { jsonrpc: "2.0", id: 2, method: "tools/list", params: {} },
    ...calls.map(([name, args], i) => ({
      jsonrpc: "2.0",
      id: 10 + i,
      method: "tools/call",
      params: { name, arguments: args },
    })),
  ];
  const stdout = execFileSync(
    process.env.GRAIN_E2E_DEMO_BIN,
    [
      "mcpserver",
      "-sandbox-root",
      sandboxRoot,
      "-server",
      url,
      "-task",
      String(taskID),
      "-grant",
      "api:dispatch-control",
    ],
    {
      input: lines.map((l) => JSON.stringify(l)).join("\n") + "\n",
      env: { ...process.env, GRAIN_TOKEN: code },
      encoding: "utf8",
      timeout: 30_000,
    },
  );
  const byID = new Map(
    stdout
      .split("\n")
      .filter((l) => l.trim())
      .map((l) => JSON.parse(l))
      .map((m) => [m.id, m]),
  );
  const tools = byID.get(2).result.tools.map((t) => t.name);
  const results = calls.map((_, i) => {
    const r = byID.get(10 + i).result;
    return {
      isError: Boolean(r.isError),
      text: r.content.map((c) => c.text).join(" "),
    };
  });
  return { tools, results };
}

test("dispatch-control over a live grain mcpserver, and the pages that show it", async ({
  page,
  request,
  demoServer,
  accessCode,
}) => {
  const stamp = Date.now();
  const sandboxRoot = await fs.mkdtemp(
    path.join(os.tmpdir(), "grain-e2e-dispatch-control-"),
  );
  const steward = await queued(request, `Steward ${stamp}`, {
    capabilities: ["api:dispatch-control"],
  });
  const target = await queued(request, `Picked by the steward ${stamp}`);
  const escalating = await ok(
    await request.post("/api/tasks", {
      data: {
        title: `Asks for gcp-key ${stamp}`,
        description: "x",
        capabilities: ["gcp-key"],
      },
    }),
    "create escalating proposal",
  );
  const running = await seededIn(request, "running");
  const reason = `steward hand-picking ${stamp}`;
  try {
    const { tools, results } = mcpCalls({
      url: demoServer,
      code: accessCode,
      taskID: steward.id,
      sandboxRoot,
      calls: [
        ["api_pause_pipeline", { body: { reason } }],
        ["api_run_now", { id: String(target.id) }],
        ["api_run_now", { id: String(running.id) }],
        ["api_run_now", { id: String(escalating.id) }],
      ],
    });
    for (const name of [
      "api_pause_pipeline",
      "api_resume_pipeline",
      "api_run_now",
    ])
      expect(tools).toContain(name);
    const [pause, picked, onRunning, onEscalating] = results;
    expect(pause.isError, pause.text).toBe(false);
    expect(picked.isError, picked.text).toBe(false);
    expect(onRunning.isError).toBe(true);
    expect(onRunning.text).toMatch(/already running/);
    expect(onEscalating.isError).toBe(true);
    expect(onEscalating.text).toMatch(/gcp-key/);

    // The pause is the agent's, and the pages say so.
    const state = await ok(await request.get("/api/pipeline-pause"), "pause");
    expect(state.pause).toMatchObject({
      paused: true,
      reason,
      byKind: "agent",
      byId: steward.id,
      runNowPasses: true,
    });
    await page.goto("/system/pipeline");
    await expect(page.getByText(reason).first()).toBeVisible();
    await expect(
      page.getByText("Paused — only tasks you Run now will start").first(),
    ).toBeVisible();

    // The steward's Run now is on the list and in the task's thread.
    await page.goto("/");
    await expect(row(page, target.title)).toBeVisible();
    await expect(chip(page, target.title)).toHaveCount(0);
    await expect(chip(page, steward.title)).toBeVisible();
    const detail = await ok(
      await request.get(`/api/tasks/${target.id}`),
      "target detail",
    );
    expect(detail.runNowAt).toBeTruthy();
    // The agent API records the call on the task's conversation too, so
    // the steward's note need not be the last comment.
    expect(detail.comments.map((c) => c.author)).toContain(steward.id);

    // api_resume_pipeline lifts it (the agent's own pause), and the alert goes.
    const resumed = mcpCalls({
      url: demoServer,
      code: accessCode,
      taskID: steward.id,
      sandboxRoot,
      calls: [["api_resume_pipeline", { reason: "done hand-picking" }]],
    }).results[0];
    expect(resumed.isError, resumed.text).toBe(false);
    await page.goto("/system/pipeline");
    await expect(
      page.getByText("Paused — only tasks you Run now will start"),
    ).toHaveCount(0);
  } finally {
    await request.delete("/api/pipeline-pause");
    await fs.rm(sandboxRoot, { recursive: true, force: true });
  }
});
