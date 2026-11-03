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

import { test, expect } from "./console-rest-fixtures.js";

async function sessionTaskID(page, id) {
  let taskId = null;
  await expect
    .poll(async () => {
      const response = await page.request.get("/api/console-sessions");
      if (!response.ok()) return null;
      const body = await response.json();
      taskId =
        body.sessions.find((session) => session.id === id)?.taskId || null;
      return taskId;
    })
    .toBeTruthy();
  return taskId;
}

async function task(page, id) {
  const response = await page.request.get(`/api/tasks/${id}`);
  expect(response.ok()).toBeTruthy();
  return response.json();
}

async function controlInteractive(page, payload) {
  const response = await page.request.post("/e2e/interactive-control", {
    data: payload,
  });
  expect(response.status()).toBe(204);
}

async function send(page, text) {
  const composer = page.locator(".cs-message-input__content-editor");
  await composer.fill(text);
  await composer.press("Enter");
}

test("interactive session preserves messages sent while Asked, while Running, and back-to-back across Running/Asked transitions", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const sessionId = `interactive${Date.now() % 100000}`;
  const created = await page.request.post("/api/console-sessions", {
    data: { id: sessionId, title: "Interactive Session", capabilities: [] },
  });
  expect(created.ok()).toBeTruthy();
  const updated = await page.request.put(`/api/console-sessions/${sessionId}`, {
    data: { capabilities: [] },
  });
  expect(updated.ok()).toBeTruthy();

  try {
    const taskId = await sessionTaskID(page, sessionId);
    await expect
      .poll(async () => (await task(page, taskId)).state)
      .toBe("awaiting_reply");

    await page.goto(`/session/${sessionId}`);
    await expect(
      page.getByRole("heading", { name: "Interactive Session", exact: true }),
    ).toBeVisible();

    const badge = page.locator(".console-session-header .badge");
    await expect(badge).toHaveText("Asked");
    await expect(page.getByText("waiting for your reply")).toBeVisible();

    // 1. Hold the next run in Running state, then send a message while Asked.
    await controlInteractive(page, { taskId, holdNext: true });
    await send(page, "Message 1 while Asked");

    // Optimistic / persisted user message renders immediately.
    await expect(
      page.locator(".cs-message--outgoing .cs-message__content"),
    ).toHaveText(["Message 1 while Asked"]);

    // Status indicator transitions from Asked -> Running while the turn executes.
    await expect
      .poll(async () => (await task(page, taskId)).state)
      .toBe("running");
    await expect(badge).toHaveClass(/badge-running/);
    await expect(badge).toHaveText("Interactive");
    await expect(page.locator(".cs-typing-indicator")).toBeVisible();

    // 2. Send a second message while the session is Running.
    await send(page, "Message 2 while Running");
    await expect(
      page.locator(".cs-message--outgoing .cs-message__content"),
    ).toHaveText(["Message 1 while Asked", "Message 2 while Running"]);

    // Release the run; the agent replies to both messages and parks back in Asked.
    await controlInteractive(page, { taskId, releaseRun: true });
    await expect(
      page.getByText(
        "Agent chat reply 1: received [Message 1 while Asked, Message 2 while Running]",
      ),
    ).toBeVisible();
    await expect
      .poll(async () => (await task(page, taskId)).state)
      .toBe("awaiting_reply");
    await expect(badge).toHaveClass(/badge-awaiting_reply/);
    await expect(badge).toHaveText("Asked");
    await expect(page.getByText("waiting for your reply")).toBeVisible();

    // 3. Send two messages back-to-back without waiting between them.
    await controlInteractive(page, { taskId, holdNext: true });
    const composer = page.locator(".cs-message-input__content-editor");
    await composer.fill("Message 3 back-to-back A");
    await composer.press("Enter");
    await composer.fill("Message 4 back-to-back B");
    await composer.press("Enter");

    // Both back-to-back messages render immediately in order and neither is dropped.
    await expect(
      page.locator(".cs-message--outgoing .cs-message__content"),
    ).toHaveText([
      "Message 1 while Asked",
      "Message 2 while Running",
      "Message 3 back-to-back A",
      "Message 4 back-to-back B",
    ]);

    await expect
      .poll(async () => (await task(page, taskId)).state)
      .toBe("running");
    await expect(badge).toHaveClass(/badge-running/);
    await expect(badge).toHaveText("Interactive");

    await controlInteractive(page, { taskId, releaseRun: true });
    await expect(
      page.getByText(
        "Agent chat reply 2: received [Message 3 back-to-back A, Message 4 back-to-back B]",
      ),
    ).toBeVisible();
    await expect
      .poll(async () => (await task(page, taskId)).state)
      .toBe("awaiting_reply");
    await expect(badge).toHaveClass(/badge-awaiting_reply/);
    await expect(badge).toHaveText("Asked");
    await expect(page.getByText("waiting for your reply")).toBeVisible();
  } finally {
    await page.request
      .delete(`/api/console-sessions/${sessionId}`)
      .catch(() => {});
  }
});
