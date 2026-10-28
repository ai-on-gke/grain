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

// Console resting-note walkthrough (docs/test-plans/2026-10-11.md §9,
// docs/test-runs/2026-10-12-console-resting-notes.md): grain parks a
// standing session on "_Waiting for you._" after every exchange, and
// de4afaa8a made that one line rather than one per park, kept it out of
// the transcript, and kept it out of the next run's prompt.
//
// The server is consolerestserver: the real store, UI server and
// orchestrator. Only the session's agent is scripted -- it answers every
// run with "Scripted answer N." and records the prompt it was given -- and
// POST /e2e/end-exchange is a live run finishing an exchange nobody
// replied to, after which the real reconciler parks the session again.

const RESTING = "_Waiting for you._";
// The same line as the chat draws it: the underscores are markdown.
const RESTING_SHOWN = "Waiting for you.";

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

function isRestingNote(c) {
  return c.author === "console-session" && c.body.trim() === RESTING;
}

// What GET /api/tasks/{id} says about resting notes: how many, and
// whether any two sit next to each other in the conversation.
async function restingNotes(page, id) {
  const comments = (await task(page, id)).comments || [];
  let consecutive = 0;
  for (let i = 1; i < comments.length; i++) {
    if (isRestingNote(comments[i]) && isRestingNote(comments[i - 1])) {
      consecutive++;
    }
  }
  return { count: comments.filter(isRestingNote).length, consecutive };
}

async function endExchange(page, id) {
  const response = await page.request.post(`/e2e/end-exchange?task=${id}`);
  expect(response.status()).toBe(204);
}

async function send(page, text) {
  const composer = page.locator(".cs-message-input__content-editor");
  await composer.fill(text);
  await composer.press("Enter");
}

async function bubbles(page) {
  return page.locator(".cs-message .cs-message__content").allInnerTexts();
}

async function prompts(page) {
  const response = await page.request.get("/e2e/prompts");
  expect(response.ok()).toBeTruthy();
  return (await response.json()).prompts;
}

// The conversation part of an assembled prompt (commentThreadSection).
function thread(prompt) {
  const start = prompt.indexOf("Conversation on this task so far");
  expect(start, "the prompt carries the conversation").toBeGreaterThan(-1);
  const end = prompt.indexOf("\n\n", prompt.indexOf("\n- ", start));
  return prompt.slice(start, end === -1 ? undefined : end);
}

test("park cycles leave one resting note, no bubble and nothing in the prompt", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await page.goto("/session/director");
  await expect(
    page.getByRole("heading", { name: "Director", exact: true }),
  ).toBeVisible();
  const id = await sessionTaskID(page);
  expect(id).toBeTruthy();
  await expect
    .poll(async () => (await task(page, id)).state)
    .toBe("awaiting_reply");
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  const opening = await bubbles(page);

  // Three exchanges end with nobody replying. Each is a real park by
  // the reconciler; the first posts the resting line, the next two
  // reuse it.
  for (let cycle = 1; cycle <= 3; cycle++) {
    await endExchange(page, id);
    await expect
      .poll(async () => (await task(page, id)).state)
      .toBe("awaiting_reply");
    expect(await restingNotes(page, id)).toEqual({
      count: 1,
      consecutive: 0,
    });
  }
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expect(page.locator(".cs-message")).toHaveCount(opening.length);
  await expect(page.getByText(RESTING_SHOWN, { exact: true })).toHaveCount(0);

  // A person replies; a real run is dispatched, answers, and the session
  // parks after it -- and then another exchange ends without a reply.
  await send(page, "Human reply one");
  await expect(page.getByText("Scripted answer 1.")).toBeVisible();
  await expect
    .poll(async () => (await task(page, id)).state)
    .toBe("awaiting_reply");
  await endExchange(page, id);
  await expect
    .poll(async () => (await task(page, id)).state)
    .toBe("awaiting_reply");

  // The transcript: what was there, the reply, the answer -- in that
  // order and nothing else, no resting bubble anywhere.
  await expect(page.locator(".cs-message")).toHaveCount(opening.length + 2);
  const transcript = await bubbles(page);
  expect(transcript.slice(0, opening.length)).toEqual(opening);
  expect(transcript.slice(opening.length).map((b) => b.trim())).toEqual([
    "Human reply one",
    "Scripted answer 1.",
  ]);
  await expect(page.getByText(RESTING_SHOWN, { exact: true })).toHaveCount(0);
  await expect(
    page.locator(".cs-message--outgoing .cs-message__content"),
  ).toHaveText(["Human reply one"]);

  // The raw record: one resting note per exchange, never two in a row.
  expect(await restingNotes(page, id)).toEqual({ count: 2, consecutive: 0 });

  // The run the reply dispatched was given the conversation without
  // grain's park line in it.
  let given = await prompts(page);
  expect(given).toHaveLength(1);
  expect(given[0]).not.toContain(RESTING);
  expect(thread(given[0])).toContain("Human reply one");

  // The same words from a person are a message, not a park line: they
  // are drawn as one and handed to the run they dispatch.
  await send(page, RESTING);
  await expect(page.getByText("Scripted answer 2.")).toBeVisible();
  await expect(
    page.locator(".cs-message--outgoing .cs-message__content"),
  ).toHaveText(["Human reply one", RESTING_SHOWN]);
  given = await prompts(page);
  expect(given).toHaveLength(2);
  const lines = thread(given[1])
    .split("\n")
    .filter((line) => line.includes(RESTING));
  expect(lines).toEqual([expect.stringMatching(/^- a human \(.*\): /)]);
  await expect
    .poll(async () => (await task(page, id)).state)
    .toBe("awaiting_reply");
  expect(await restingNotes(page, id)).toEqual({ count: 3, consecutive: 0 });
});
