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

// docs/test-plans/2026-11-01-late.md §1 "not proved" 4 and the Inbox half
// of 3 (docs/test-runs/2026-11-01-interactive-message-delivery.md): a
// console session that grain has rested -- parked on its own session
// note with a {message: true} deferral (ParkConsoleSessionTask) -- wakes
// on the operator's first reply, with no second message and no wait for
// the deferral, and the Inbox agrees with the task at 1440 and 390 px.

async function sessionTaskID(page) {
  const response = await page.request.get("/api/console-sessions");
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  return body.sessions.find((session) => session.id === "director")?.taskId;
}

async function task(page, id) {
  const response = await page.request.get(`/api/tasks/${id}`);
  expect(response.ok()).toBeTruthy();
  return response.json();
}

async function prompts(page) {
  const response = await page.request.get("/e2e/prompts");
  expect(response.ok()).toBeTruthy();
  return (await response.json()).prompts || [];
}

// The Inbox as a person sees it at width: how many of its rows link to
// the session's task.
async function inboxRowsFor(page, id, width, shot) {
  await page.setViewportSize({ width, height: 900 });
  await page.goto("/inbox");
  await expect(page.locator("main")).toBeVisible();
  await page.waitForLoadState("networkidle");
  await page.screenshot({ path: test.info().outputPath(`${shot}-${width}.png`) });
  return page.locator(`main a[href$="/tasks/${id}"]`).count();
}

test("a rested console session wakes on the first reply, and the Inbox agrees", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await page.goto("/session/director");
  const id = await sessionTaskID(page);
  expect(id).toBeTruthy();
  await expect
    .poll(async () => (await task(page, id)).state)
    .toBe("awaiting_reply");

  // Rest it: a live exchange ended with nobody replying, and the real
  // reconciler parks it again.
  const rest = await page.request.post(`/e2e/end-exchange?task=${id}`);
  expect(rest.status()).toBe(204);
  await expect
    .poll(async () => (await task(page, id)).state)
    .toBe("awaiting_reply");
  const rested = await task(page, id);
  test.info().annotations.push({
    type: "rested",
    description: JSON.stringify({ state: rested.state, comments: (rested.comments || []).length }),
  });

  // The Inbox at desktop and phone width shows the session the same way.
  const restedDesk = await inboxRowsFor(page, id, 1440, "rested");
  const restedPhone = await inboxRowsFor(page, id, 390, "rested");
  expect(restedPhone).toBe(restedDesk);

  // One reply, from the browser.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/session/director");
  const before = (await prompts(page)).length;
  const composer = page.locator(".cs-message-input__content-editor");
  await composer.fill("first reply after resting");
  const sentAt = Date.now();
  await composer.press("Enter");
  // The session's 50 ms cycle dispatches it at once; a deferral that
  // outlived the reply would leave it waiting far longer than this.
  await expect(page.getByText(`Scripted answer ${before + 1}.`)).toBeVisible({ timeout: 10_000 });
  const latency = Date.now() - sentAt;
  test.info().annotations.push({ type: "wake-latency-ms", description: String(latency) });
  const given = await prompts(page);
  expect(given).toHaveLength(before + 1);
  expect(given[given.length - 1]).toContain("first reply after resting");
  await expect
    .poll(async () => (await task(page, id)).state)
    .toBe("awaiting_reply");

  const answeredDesk = await inboxRowsFor(page, id, 1440, "answered");
  const answeredPhone = await inboxRowsFor(page, id, 390, "answered");
  expect(answeredPhone).toBe(answeredDesk);
  test.info().annotations.push({
    type: "inbox-rows",
    description: JSON.stringify({ restedDesk, restedPhone, answeredDesk, answeredPhone }),
  });
});
