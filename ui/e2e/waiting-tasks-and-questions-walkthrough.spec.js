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

import { test as base, expect } from "@playwright/test";
import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { accessCode, signInContext } from "./accesscode.js";
import { pageErrors } from "./pageerrors.js";
import { eventually, startDaemon, taskDetail } from "./realdaemon.js";

const runFile = promisify(execFile);
const test = base.extend(pageErrors);
test.setTimeout(180_000);

function toolStream(toolName, parameters, output, response = "Done.") {
  return [
    JSON.stringify({
      event: "init",
      init: {
        cwd: "/w",
        tools: [`mcp__grain-sandbox__${toolName}`],
        permission_mode: "bypass",
      },
    }),
    JSON.stringify({
      event: "step_update",
      step_update: {
        step_index: 0,
        state: "ACTIVE",
        step_type: "tool",
        tool_name: `mcp__grain-sandbox__${toolName}`,
        tool_info: {
          name: `mcp__grain-sandbox__${toolName}`,
          parameters,
        },
      },
    }),
    JSON.stringify({
      event: "step_update",
      step_update: {
        step_index: 0,
        state: "DONE",
        step_type: "tool",
        tool_name: `mcp__grain-sandbox__${toolName}`,
        tool_info: {
          name: `mcp__grain-sandbox__${toolName}`,
          output,
        },
      },
    }),
    JSON.stringify({
      event: "result",
      result: {
        status: "SUCCESS",
        response,
      },
    }),
    "",
  ].join("\n");
}

async function waitForSandboxReady(sandboxDir, runID) {
  const dir = path.join(sandboxDir, runID);
  await eventually(async () => {
    try {
      await fs.access(path.join(dir, "agent-ready"));
      return true;
    } catch {
      return false;
    }
  }, `sandbox ${runID} did not reach agent-ready`);
  return dir;
}

async function finishSandboxWithStream(sandboxDir, runID, streamText) {
  const dir = path.join(sandboxDir, runID);
  const tmp = path.join(dir, "agent-stream.tmp");
  const target = path.join(dir, "agent-stream");
  await fs.writeFile(tmp, streamText);
  await fs.rename(tmp, target);
}

async function runCLI(url, ...args) {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  const { stdout } = await runFile(bin, [
    "-server",
    url,
    "-token",
    accessCode(),
    ...args,
  ]);
  return stdout;
}

// Section 4 of docs/test-plans/2026-10-22.md:
// 1. wait_for_task parked UI ("Waiting on #X" vs "Blocked on #X"), validation
//    refusals, and resume prompt injection when the waited task closes.
// 2. /inbox pre-fetched question markdown (.inbox-question) with interactive
//    #N task links, pendingSecret write-only notice, and DetailOverlay's
//    PendingQuestion callout with poll-surviving draft and Send reply.
// 3. Queued task waiting status (QuotaWaitCapability) on TaskList, TaskBoard,
//    DetailOverlay, and the `grain` CLI at desktop (1280x800) and phone
//    (390x844) viewports.
test("wait_for_task parked UI and resume, /inbox pre-fetched question & PendingQuestion callout, and queued waiting status across TaskList, TaskBoard, DetailOverlay, and CLI", async ({
  page,
}) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "grain-waiting-e2e-"));
  const dataDir = path.join(root, "data");
  const sandboxDir = path.join(root, "sandboxes");
  await fs.mkdir(dataDir);
  await fs.mkdir(sandboxDir);
  await fs.writeFile(path.join(root, "gemini-key"), "test-only-key\n", {
    mode: 0o600,
  });

  let daemon;
  try {
    daemon = await startDaemon(root, dataDir, sandboxDir, [
      "-max-workers",
      "2",
    ]);
    // Replace the fake agy script before filing tasks so each run waits for
    // its own per-sandbox `agent-stream` file and prints whatever stream the
    // test writes for that attempt.
    await fs.writeFile(
      path.join(root, "bin", "agy"),
      `#!/bin/sh
set -eu
root=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--add-dir" ]; then
    root="$2"
    shift 2
  else
    shift
  fi
done
test -n "$root"
cat > "$root/agent-stdin"
touch "$root/agent-ready"
while [ ! -e "$root/agent-stream" ]; do sleep 0.05; done
cat "$root/agent-stream"
`,
      { mode: 0o755 },
    );

    await signInContext(page.context(), daemon.url);

    const createTask = async (title, body, extra = {}) => {
      const res = await page.request.post(`${daemon.url}/api/tasks`, {
        data: {
          title,
          body,
          noRepo: true,
          approved: true,
          ...extra,
        },
      });
      expect(res.ok(), `POST /api/tasks (${title}): ${res.status()}`).toBe(
        true,
      );
      return res.json();
    };

    // =========================================================================
    // 1. wait_for_task parked UI ("Waiting on #X" vs "Blocked on #X") & resume
    // =========================================================================
    const taskA = await createTask(
      "Deploy downstream service after schema migration",
      "Wait on the schema migration task before deploying.",
    );
    const taskB = await createTask(
      "Run production schema migration",
      "Apply the v42 database schema changes.",
    );

    await waitForSandboxReady(sandboxDir, `${taskA.id}-1`);
    await waitForSandboxReady(sandboxDir, `${taskB.id}-1`);

    // Fetch the standing director console session and create a dependent task
    // to verify wait-on validation refusals (self-wait, unknown task, standing
    // chat session, dependency cycle).
    const directorTaskID = await eventually(async () => {
      const res = await page.request.get(`${daemon.url}/api/console-sessions`);
      if (!res.ok()) return null;
      const body = await res.json();
      return body.sessions?.[0]?.taskId || null;
    }, "daemon did not materialize the standing director session");

    const depRes = await page.request.post(`${daemon.url}/api/tasks`, {
      data: {
        title: "Downstream verification",
        body: "Depends on Task A",
        noRepo: true,
        approved: false,
        dependsOn: [taskA.id],
      },
    });
    expect(depRes.ok()).toBe(true);
    const depTask = await depRes.json();

    // Self-wait is refused with 400.
    const selfWaitRes = await page.request.post(
      `${daemon.url}/api/tasks/${taskA.id}/wait-on`,
      { data: { ids: [taskA.id], park: true } },
    );
    expect(selfWaitRes.status()).toBe(400);
    expect(await selfWaitRes.text()).toContain("cannot depend on itself");

    // Non-existent task is refused with 400.
    const missingWaitRes = await page.request.post(
      `${daemon.url}/api/tasks/${taskA.id}/wait-on`,
      { data: { ids: ["99999"], park: true } },
    );
    expect(missingWaitRes.status()).toBe(400);
    expect(await missingWaitRes.text()).toContain(
      "depends on unknown task 99999",
    );

    // Standing chat session is refused with 400.
    const chatWaitRes = await page.request.post(
      `${daemon.url}/api/tasks/${taskA.id}/wait-on`,
      { data: { ids: [directorTaskID], park: true } },
    );
    expect(chatWaitRes.status()).toBe(400);
    expect(await chatWaitRes.text()).toContain("standing chat session");

    // Dependency cycle is refused with 400.
    const cycleWaitRes = await page.request.post(
      `${daemon.url}/api/tasks/${taskA.id}/wait-on`,
      { data: { ids: [depTask.id], park: true } },
    );
    expect(cycleWaitRes.status()).toBe(400);
    expect(await cycleWaitRes.text()).toContain("already waits on task");

    // Close the helper dependent task so it doesn't clutter the board.
    await page.request.post(`${daemon.url}/api/tasks/${depTask.id}/close`);

    // Now have Task A park on Task B via POST /api/tasks/{id}/wait-on + tool stream.
    const parkRes = await page.request.post(
      `${daemon.url}/api/tasks/${taskA.id}/wait-on`,
      { data: { ids: [taskB.id], park: true } },
    );
    expect(parkRes.ok(), `wait-on park failed: ${parkRes.status()}`).toBe(true);
    const parkPayload = await parkRes.json();
    expect(parkPayload.targets).toHaveLength(1);
    expect(parkPayload.targets[0].id).toBe(taskB.id);

    await finishSandboxWithStream(
      sandboxDir,
      `${taskA.id}-1`,
      toolStream(
        "wait_for_task",
        { task_id: taskB.id, park: true },
        `Parked. This task now depends on #${taskB.id} ("${taskB.title}") and will be queued again once it closes.`,
        "Parking until the schema migration finishes.",
      ),
    );

    const parkedDetail = await eventually(async () => {
      const d = await taskDetail(page.request, daemon.url, taskA.id);
      return d.state === "queued" && d.parked === true && d.blocked === true
        ? d
        : null;
    }, "Task A did not enter parked queued state");

    expect(parkedDetail.parked).toBe(true);
    expect(parkedDetail.blocked).toBe(true);
    expect(parkedDetail.dependsOn).toContain(taskB.id);
    expect(parkedDetail.attempts?.[0]?.outcome).toBe("parked");
    expect(parkedDetail.attempts?.[0]?.detail).toContain(taskB.id);

    for (const viewport of [
      { width: 1280, height: 800 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto(`${daemon.url}/`);

      const rowA = page.locator(".task-row", { hasText: taskA.title });
      await expect(rowA).toBeVisible();
      const rowBlockedBadge = rowA.locator(".badge-blocked");
      await expect(rowBlockedBadge).toBeVisible();
      await expect(rowBlockedBadge).toHaveAttribute(
        "title",
        new RegExp(`^Blocked on: #${taskB.id}\\b`),
      );
      // The whole label: the row's sub-case chip beside it reads
      // "blocked on task" (ui.Task.Reason).
      await expect(
        rowA.locator(".MuiChip-root", { hasText: /^Blocked$/ }),
      ).toHaveCount(0);

      await page.goto(`${daemon.url}/tasks/${taskA.id}`);
      const detailOverlay = page.locator(".detail-layout");
      await expect(detailOverlay).toBeVisible();
      const detailBlockedBadge = detailOverlay.locator(
        ".detail-state .badge-blocked",
      );
      await expect(detailBlockedBadge).toHaveText("Blocked");
      await expect(detailBlockedBadge).toHaveAttribute(
        "title",
        new RegExp(`^Waiting on: #${taskB.id}\\b`),
      );
      await expect(
        detailOverlay.locator(".detail-state .MuiChip-root", {
          hasText: "Blocked",
        }),
      ).toHaveCount(0);
      await expect(detailOverlay).toContainText(
        `grain: this task's run parked it, waiting on #${taskB.id}`,
      );
    }

    // Complete/close Task B and verify Task A wakes up for Attempt 2 with the
    // resumed-from-park closure summary in its prompt.
    await finishSandboxWithStream(
      sandboxDir,
      `${taskB.id}-1`,
      toolStream(
        "nothing_needed",
        { reason: "Schema migration v42 applied cleanly." },
        "Recorded.",
        "Migration complete.",
      ),
    );

    const attempt2Dir = await waitForSandboxReady(sandboxDir, `${taskA.id}-2`);
    const attempt2Stdin = JSON.parse(
      await fs.readFile(path.join(attempt2Dir, "agent-stdin"), "utf8"),
    );
    const attempt2Prompt = attempt2Stdin.message.content;

    expect(attempt2Prompt).toContain(
      "This run resumes from a park: the previous attempt called wait_for_task",
    );
    expect(attempt2Prompt).toContain(
      `Task ${taskB.id} "${taskB.title}": closed, outcome completed`,
    );
    expect(attempt2Prompt).toContain("Schema migration v42 applied cleanly.");

    // Finish Task A attempt 2 so worker slots are free for Scenario 2.
    await finishSandboxWithStream(
      sandboxDir,
      `${taskA.id}-2`,
      toolStream(
        "nothing_needed",
        { reason: "Downstream service deployed after schema migration." },
        "Recorded.",
        "Deployment complete.",
      ),
    );
    await eventually(async () => {
      const d = await taskDetail(page.request, daemon.url, taskA.id);
      return d.state === "closed" ? d : null;
    }, "Task A did not close after attempt 2");

    // =========================================================================
    // 2. /inbox pre-fetched question & PendingQuestion callout in DetailOverlay
    // =========================================================================
    const taskC = await createTask(
      "Clarify staging rollout target",
      "Check which staging cluster should receive the rollout first.",
    );
    const taskD = await createTask(
      "Configure webhook signing secret",
      "Verify Stripe webhook signatures in staging.",
    );

    await waitForSandboxReady(sandboxDir, `${taskC.id}-1`);
    await waitForSandboxReady(sandboxDir, `${taskD.id}-1`);

    const questionMarkdown = [
      `Should we point **config.yaml** at \`acme/widgets\` now that task ${taskB.id} is closed?`,
      "",
      "- Option A: `us-central1` staging",
      "- Option B: `eu-west1` staging",
    ].join("\n");

    await finishSandboxWithStream(
      sandboxDir,
      `${taskC.id}-1`,
      toolStream(
        "ask_question",
        { question: questionMarkdown },
        "Question recorded.",
        "Asked which staging target to use.",
      ),
    );

    await finishSandboxWithStream(
      sandboxDir,
      `${taskD.id}-1`,
      toolStream(
        "request_secret",
        {
          secret: "stripe-api-key",
          reason: "Needed to verify webhook signatures in staging.",
        },
        "Secret request recorded.",
        "Requested stripe-api-key.",
      ),
    );

    await eventually(async () => {
      const c = await taskDetail(page.request, daemon.url, taskC.id);
      const d = await taskDetail(page.request, daemon.url, taskD.id);
      return c.state === "awaiting_reply" &&
        c.comments?.length > 0 &&
        d.state === "awaiting_reply" &&
        d.pendingSecret?.name === "stripe-api-key"
        ? true
        : null;
    }, "Task C and Task D did not enter awaiting_reply");

    for (const viewport of [
      { width: 1280, height: 800 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto(`${daemon.url}/inbox`);

      const askingGroup = page.locator(".inbox-group", {
        hasText: "Asked: reply",
      });
      await expect(askingGroup).toBeVisible();

      const rowC = askingGroup.locator(".inbox-item", { hasText: taskC.title });
      await expect(rowC).toBeVisible();

      // Responder pre-fetches GET /api/tasks/{id} on mount and shows the
      // question's one-line summary BEFORE Reply is clicked; the
      // .inbox-question markdown is one click on it away.
      const toggle = rowC.locator(".question-disclosure-toggle");
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await expect(rowC.locator(".inbox-question")).toHaveCount(0);
      await toggle.click();
      await expect(toggle).toHaveAttribute("aria-expanded", "true");
      const preFetchedQuestion = rowC.locator(".inbox-question");
      await expect(preFetchedQuestion).toBeVisible();
      await expect(preFetchedQuestion.locator("strong")).toHaveText(
        "config.yaml",
      );
      await expect(preFetchedQuestion.locator("code").first()).toHaveText(
        "acme/widgets",
      );
      await expect(preFetchedQuestion.locator("li")).toHaveCount(2);

      // The `task <N>` link inside .inbox-question is interactive: a modified
      // click leaves the current page alone, while a normal click opens Task B
      // in DetailOverlay.
      const taskLink = preFetchedQuestion.locator(
        `a[href="#task-${taskB.id}"]`,
      );
      await expect(taskLink).toHaveText(`task ${taskB.id}`);
      await taskLink.click({ modifiers: ["Control"] });
      await expect(page.locator(".detail-layout")).toHaveCount(0);

      await taskLink.click();
      await expect(page.locator(".detail-layout .detail-header")).toContainText(
        taskB.title,
      );

      // Return to /inbox and verify Task D (pendingSecret) does NOT render a
      // plain-text reply box, directing the operator to the task pane's
      // write-only secret box instead.
      await page.goto(`${daemon.url}/inbox`);
      const rowD = askingGroup.locator(".inbox-item", { hasText: taskD.title });
      await expect(rowD).toBeVisible();
      await expect(rowD.locator(".inbox-question")).toHaveCount(0);
      await rowD.getByRole("button", { name: "Reply" }).click();
      const secretTray = rowD.locator(".inbox-responder");
      await expect(secretTray).toContainText("stripe-api-key");
      await expect(
        secretTray.getByRole("button", { name: "Open the task" }),
      ).toBeVisible();
      await expect(secretTray.getByRole("textbox")).toHaveCount(0);
    }

    // Open Task C in DetailOverlay at both viewports; verify PendingQuestion
    // sits above Description and Timeline, preserves a multi-word draft across
    // a background poll refresh, and sends the reply to requeue Task C.
    for (const viewport of [
      { width: 390, height: 844 },
      { width: 1280, height: 800 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto(`${daemon.url}/tasks/${taskC.id}`);

      const callout = page.getByTestId("pending-question");
      await expect(callout).toBeVisible();
      await expect(
        callout.locator(".pending-question-header strong"),
      ).toHaveText("Waiting for your reply");
      await expect(callout.locator(".pending-question-body strong")).toHaveText(
        "config.yaml",
      );
      await expect(callout.locator(`a[href="#task-${taskB.id}"]`)).toHaveText(
        `task ${taskB.id}`,
      );

      const calloutBox = await callout.boundingBox();
      const descBox = await page
        .locator(".detail-layout .description")
        .boundingBox();
      const timelineBox = await page
        .locator(".detail-layout .timeline")
        .boundingBox();
      expect(calloutBox).not.toBeNull();
      expect(descBox).not.toBeNull();
      expect(timelineBox).not.toBeNull();
      expect(calloutBox.y).toBeLessThan(descBox.y);
      expect(calloutBox.y).toBeLessThan(timelineBox.y);
    }

    const replyBox = page
      .getByTestId("pending-question")
      .getByRole("textbox", { name: `Reply to ${taskC.id}` });
    const draft = "Use Option A (us-central1 staging) and deploy now.";
    await replyBox.fill(draft);

    // Wait for at least one background poll of GET /api/tasks/{id} while the
    // draft is in the input and verify the draft is preserved intact.
    await page.waitForResponse(
      (res) =>
        res.url().endsWith(`/api/tasks/${taskC.id}`) &&
        res.request().method() === "GET" &&
        res.status() === 200,
    );
    await expect(replyBox).toHaveValue(draft);

    // Submit the reply from PendingQuestion; Task C transitions back to queued
    // and starts Attempt 2.
    await page
      .getByTestId("pending-question")
      .getByRole("button", { name: "Send" })
      .click();
    await expect(page.getByTestId("pending-question")).toHaveCount(0);

    await waitForSandboxReady(sandboxDir, `${taskC.id}-2`);

    // =========================================================================
    // 3. Queued task waiting status on TaskList, TaskBoard, DetailOverlay & CLI
    // =========================================================================
    // Task C attempt 2 is occupying 1 of the 2 worker slots. Start one more
    // task (Task F) to occupy the 2nd worker slot, then file Task E so Task E
    // stays queued with a QuotaWaitCapability ("waiting: ...") reason.
    const taskF = await createTask(
      "Hold second worker slot during capacity check",
      "Keep the second worker busy while Task E waits in the queue.",
    );
    await waitForSandboxReady(sandboxDir, `${taskF.id}-1`);

    const taskE = await createTask(
      "Queued task waiting on worker capacity",
      "Verify TaskList, TaskBoard, DetailOverlay, and CLI waiting status.",
    );

    const waitingDetail = await eventually(async () => {
      const d = await taskDetail(page.request, daemon.url, taskE.id);
      return d.state === "queued" &&
        d.activity?.startsWith("waiting: ") &&
        d.activityBySetup === true
        ? d
        : null;
    }, "Task E did not record a queued waiting reason");

    const waitReason = waitingDetail.activity; // e.g. "waiting: 2 of 2 workers busy"
    // The badge's title: "Waiting: <reason>" for a reason worded
    // "waiting: ...", rather than saying waiting twice (state.js's
    // stateTitle).
    const why = waitReason.replace(/^waiting:\s*/i, "");
    const waitTitle =
      why !== waitReason ? `Waiting: ${why}` : `Waiting — ${waitReason}`;

    // Create a Kanban board so we can inspect TaskBoard alongside TaskList.
    const boardRes = await page.request.post(`${daemon.url}/api/boards`, {
      data: { name: "Capacity Board", kind: "kanban" },
    });
    expect(boardRes.ok()).toBe(true);
    const board = await boardRes.json();

    for (const viewport of [
      { width: 1280, height: 800 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);

      // TaskList (`/`): state badge is `.badge-queued` with aria-label "Waiting",
      // state icon tooltip carries "Waiting: <reason>", and
      // `.task-activity` is omitted from the row.
      await page.goto(`${daemon.url}/`);
      const rowE = page.locator(".task-row", { hasText: taskE.title });
      await expect(rowE).toBeVisible();
      await expect(rowE.locator(".task-activity")).toHaveCount(0);
      await expect(rowE.locator(".badge-icon.badge-queued")).toHaveAttribute(
        "aria-label",
        "Waiting",
      );
      await expect(rowE.locator(".badge-icon.badge-queued")).toHaveAttribute(
        "title",
        waitTitle,
      );

      // TaskBoard (`/boards/<id>`): card state icon is `.badge-queued`,
      // tooltip carries "Waiting: <reason>", and `.board-card-activity`
      // is omitted from the card.
      await page.goto(`${daemon.url}/boards/${board.id}`);
      const cardE = page.locator(".board-card", { hasText: taskE.title });
      await expect(cardE).toBeVisible();
      await expect(cardE.locator(".board-card-activity")).toHaveCount(0);
      await expect(cardE.locator(".badge-icon.badge-queued")).toHaveAttribute(
        "title",
        waitTitle,
      );

      // DetailOverlay (`/tasks/<id>`): state badge reads "Waiting" with
      // title="Waiting: <reason>", and `.detail-activity` displays
      // "grain" and "waiting: <reason>".
      await page.goto(`${daemon.url}/tasks/${taskE.id}`);
      const detailStateBadge = page.locator(
        ".detail-layout .detail-state .badge-queued",
      );
      await expect(detailStateBadge).toHaveText("Waiting");
      await expect(detailStateBadge).toHaveAttribute("title", waitTitle);
      const detailActivity = page.locator(".detail-layout .detail-activity");
      await expect(detailActivity).toBeVisible();
      await expect(detailActivity).toContainText("grain");
      await expect(detailActivity).toContainText(waitReason);
    }

    // Verify `grain get <E>` (human-readable) and `grain -json list` CLI output
    // both surface the wait reason.
    const cliGetOut = await runCLI(daemon.url, "get", taskE.id);
    expect(cliGetOut).toContain("state:      queued");
    expect(cliGetOut).toContain(`activity:   ${waitReason}`);

    const cliListJSON = JSON.parse(await runCLI(daemon.url, "-json", "list"));
    const cliTaskE = cliListJSON.find((t) => t.id === taskE.id);
    expect(cliTaskE).toBeDefined();
    expect(cliTaskE.state).toBe("queued");
    expect(cliTaskE.activity).toBe(waitReason);

    // Cleanly finish the remaining live runs so the daemon shuts down without
    // orphaned subprocesses.
    await page.request.post(`${daemon.url}/api/tasks/${taskE.id}/close`);
    await finishSandboxWithStream(
      sandboxDir,
      `${taskC.id}-2`,
      toolStream(
        "nothing_needed",
        { reason: "Staging rollout completed." },
        "Recorded.",
        "Done.",
      ),
    );
    await finishSandboxWithStream(
      sandboxDir,
      `${taskF.id}-1`,
      toolStream(
        "nothing_needed",
        { reason: "Capacity check completed." },
        "Recorded.",
        "Done.",
      ),
    );
  } finally {
    if (daemon) await daemon.stop();
    await fs.rm(root, { recursive: true, force: true });
  }
});
