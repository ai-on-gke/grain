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

// TaskChat's optimistic and back-to-back sends in a real browser, over
// consolerestserver's real store and orchestrator
// (docs/test-plans/2026-11-01-late.md §3, "What is NOT proved" 1-5;
// docs/test-runs/2026-11-01-task-chat-optimistic-sends.md). The sibling
// interactive-session-chat.spec.js covers the happy path at one width;
// this one covers phone width and dark mode, a slow and a failing POST,
// a burst with an attachment, two tabs, and the keyboard.

import { test, expect } from "./console-rest-fixtures.js";

const WIDTHS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};
const COMPOSER = ".cs-message-input__content-editor";
const OUTGOING = ".cs-message--outgoing .cs-message__content .task-chat-body";

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

async function taskState(page, id) {
  const response = await page.request.get(`/api/tasks/${id}`);
  expect(response.ok()).toBeTruthy();
  return (await response.json()).state;
}

async function controlInteractive(page, payload) {
  const response = await page.request.post("/e2e/interactive-control", {
    data: payload,
  });
  expect(response.status()).toBe(204);
}

// openSession creates a fresh standing session, waits for its task to
// park in Asked, and opens it with the next run held in Running so no
// scripted reply lands mid-assertion. The returned cleanup deletes it.
async function openSession(page, title) {
  const sessionId = `optimistic${Date.now() % 1000000}`;
  const created = await page.request.post("/api/console-sessions", {
    data: { id: sessionId, title, capabilities: [] },
  });
  expect(created.ok()).toBeTruthy();
  const cleanup = async () => {
    await page.request
      .delete(`/api/console-sessions/${sessionId}`)
      .catch(() => {});
  };
  const taskId = await sessionTaskID(page, sessionId);
  await expect.poll(() => taskState(page, taskId)).toBe("awaiting_reply");
  await controlInteractive(page, { taskId, holdNext: true });
  await page.goto(`/session/${sessionId}`);
  await expect(
    page.getByRole("heading", { name: title, exact: true }),
  ).toBeVisible();
  return { sessionId, taskId, cleanup };
}

async function release(page, taskId) {
  await controlInteractive(page, { taskId, releaseRun: true }).catch(() => {});
}

async function storedHumanBodies(page, taskId) {
  const response = await page.request.get(`/api/tasks/${taskId}/messages`);
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  const list = Array.isArray(body) ? body : body.messages || [];
  return list.filter((m) => m.authorKind === "human").map((m) => m.body);
}

// holdComments makes every POST to the task's comments endpoint wait
// until the returned release() is called, then answer with `status`
// (the real server's answer when status is 0).
async function holdComments(page, taskId, { failFirst = 0 } = {}) {
  const waiting = [];
  let seen = 0;
  await page.route(`**/api/tasks/${taskId}/comments`, async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    const n = ++seen;
    await new Promise((resolve) => waiting.push(resolve));
    if (n <= failFirst) {
      return route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "walkthrough: injected failure" }),
      });
    }
    return route.continue();
  });
  return {
    pending: () => waiting.length,
    releaseOne: () => waiting.shift()?.(),
  };
}

async function assertComposerClear(page) {
  const composerBox = await page.locator(".cs-message-input").boundingBox();
  const listBox = await page.locator(".cs-message-list").boundingBox();
  expect(composerBox).not.toBeNull();
  expect(listBox).not.toBeNull();
  // The transcript ends where the composer starts: nothing drawn over it.
  expect(listBox.y + listBox.height).toBeLessThanOrEqual(composerBox.y + 1);
  const viewport = page.viewportSize();
  expect(composerBox.x).toBeGreaterThanOrEqual(0);
  expect(composerBox.x + composerBox.width).toBeLessThanOrEqual(
    viewport.width + 1,
  );
  // The newest bubble is scrolled into the transcript, above the composer.
  const last = page.locator(".cs-message--outgoing").last();
  const lastBox = await last.boundingBox();
  expect(lastBox.y + lastBox.height).toBeLessThanOrEqual(composerBox.y + 1);
  expect(lastBox.y).toBeGreaterThanOrEqual(listBox.y - 1);
  expect(lastBox.x + lastBox.width).toBeLessThanOrEqual(viewport.width + 1);
}

async function bubblesInOrder(page) {
  const boxes = await page
    .locator(".cs-message--outgoing")
    .evaluateAll((els) => els.map((e) => e.getBoundingClientRect().top));
  for (let i = 1; i < boxes.length; i += 1) {
    expect(boxes[i]).toBeGreaterThan(boxes[i - 1]);
  }
}

for (const [widthName, viewport] of Object.entries(WIDTHS)) {
  for (const theme of ["light", "dark"]) {
    test(`optimistic and queued bubbles at ${widthName} in ${theme}: marked sending, clear of the composer, no jump or duplicate`, async ({
      page,
    }) => {
      test.setTimeout(90_000);
      await page.setViewportSize(viewport);
      await page.emulateMedia({ colorScheme: theme });
      await page.addInitScript(
        (t) => localStorage.setItem("grain.themeMode", t),
        theme,
      );
      const { taskId, cleanup } = await openSession(
        page,
        `Optimistic ${widthName} ${theme}`,
      );
      try {
        const gate = await holdComments(page, taskId);
        const composer = page.locator(COMPOSER);
        await composer.fill("First, held in flight");
        await composer.press("Enter");
        await composer.fill("Second, queued behind it");
        await composer.press("Enter");
        await expect(page.locator(OUTGOING)).toHaveText([
          "First, held in flight",
          "Second, queued behind it",
        ]);
        // Neither has landed yet, so neither may read as sent.
        await expect(page.locator(".task-chat-pending")).toHaveCount(2);
        await assertComposerClear(page);
        await bubblesInOrder(page);
        await page.screenshot({
          path: test.info().outputPath(`pending-${widthName}-${theme}.png`),
        });
        const before = await page
          .locator(".cs-message--outgoing")
          .first()
          .boundingBox();

        await expect.poll(gate.pending).toBe(1);
        gate.releaseOne();
        await expect.poll(gate.pending).toBe(1);
        gate.releaseOne();
        await expect(page.locator(".task-chat-pending")).toHaveCount(0);
        // The server's copies replaced the optimistic ones one for one.
        await expect(page.locator(OUTGOING)).toHaveText([
          "First, held in flight",
          "Second, queued behind it",
        ]);
        const after = await page
          .locator(".cs-message--outgoing")
          .first()
          .boundingBox();
        expect(Math.abs(after.x - before.x)).toBeLessThanOrEqual(1);
        expect(Math.abs(after.width - before.width)).toBeLessThanOrEqual(2);
        await assertComposerClear(page);
        await bubblesInOrder(page);
      } finally {
        await page.unrouteAll({ behavior: "ignoreErrors" });
        await release(page, taskId);
        await cleanup();
      }
    });
  }
}

test("a slow POST is shown as sending, never as sent, and announced", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const { taskId, cleanup } = await openSession(page, "Slow post");
  try {
    const gate = await holdComments(page, taskId);
    const composer = page.locator(COMPOSER);
    await composer.fill("Slow one");
    await composer.press("Enter");
    await expect(page.locator(OUTGOING)).toHaveText(["Slow one"]);
    await expect(page.locator(".task-chat-pending")).toHaveCount(1);
    await expect(page.locator(".task-chat-pending")).toContainText("Sending");
    await expect(
      page.getByRole("status").filter({ hasText: /Sending/ }),
    ).toBeVisible();
    // Focus stays in the composer while it is in flight.
    await expect(composer).toBeFocused();
    await page.waitForTimeout(1500);
    await expect(page.locator(".task-chat-pending")).toHaveCount(1);
    await expect.poll(gate.pending).toBe(1);
    gate.releaseOne();
    await expect(page.locator(".task-chat-pending")).toHaveCount(0);
    await expect(page.locator(OUTGOING)).toHaveText(["Slow one"]);
    await expect
      .poll(() => storedHumanBodies(page, taskId))
      .toContain("Slow one");
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await release(page, taskId);
    await cleanup();
  }
});

test("a failed first send says so and does not drop the queued second", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const { taskId, cleanup } = await openSession(page, "Failed first");
  try {
    const gate = await holdComments(page, taskId, { failFirst: 1 });
    const composer = page.locator(COMPOSER);
    await composer.fill("Fails on the server");
    await composer.press("Enter");
    await composer.fill("Queued behind the failure");
    await composer.press("Enter");
    await expect.poll(gate.pending).toBe(1);
    gate.releaseOne();
    await expect.poll(gate.pending).toBe(1);
    // The failure is visible, and the failed text is not drawn as sent.
    await expect(page.getByText("walkthrough: injected failure")).toBeVisible();
    await expect(composer).toHaveText("Fails on the server");
    await expect(page.locator(OUTGOING)).toHaveText([
      "Queued behind the failure",
    ]);
    gate.releaseOne();
    await expect(page.locator(".task-chat-pending")).toHaveCount(0);
    await expect(page.locator(OUTGOING)).toHaveText([
      "Queued behind the failure",
    ]);
    await expect
      .poll(() => storedHumanBodies(page, taskId))
      .toEqual(["Queued behind the failure"]);
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await release(page, taskId);
    await cleanup();
  }
});

test("when both queued sends fail, neither message is lost", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const { taskId, cleanup } = await openSession(page, "Both fail");
  try {
    const gate = await holdComments(page, taskId, { failFirst: 2 });
    const composer = page.locator(COMPOSER);
    await composer.fill("Lost A");
    await composer.press("Enter");
    await composer.fill("Lost B");
    await composer.press("Enter");
    await expect.poll(gate.pending).toBe(1);
    gate.releaseOne();
    await expect.poll(gate.pending).toBe(1);
    gate.releaseOne();
    // Neither landed. The first is back in the box; the second, with the
    // box taken, stays in the transcript marked "Not sent" with a Retry.
    await expect(composer).toHaveText("Lost A");
    const failed = page.locator(".task-chat-failed");
    await expect(failed).toHaveCount(1);
    await expect(failed).toContainText("Lost B");
    await expect(failed).toContainText("Not sent");
    await expect(page.locator(".task-chat-sending")).toHaveCount(0);
    await expect(
      page.getByRole("status").filter({ hasText: "Message not sent" }),
    ).toHaveCount(1);
    await assertComposerClear(page);
    await page.screenshot({ path: test.info().outputPath("failed-retry.png") });
    // Retry, with the server answering again: B lands, once.
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await failed.getByRole("button", { name: "Retry" }).click();
    await expect(page.locator(".task-chat-failed")).toHaveCount(0);
    await expect(page.locator(OUTGOING)).toHaveText(["Lost B"]);
    await expect
      .poll(() => storedHumanBodies(page, taskId))
      .toEqual(["Lost B"]);
    await expect(composer).toHaveText("Lost A");
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    await release(page, taskId);
    await cleanup();
  }
});

test("a burst of four, one with an attachment, keeps its order in the UI and the store", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const { taskId, cleanup } = await openSession(page, "Burst");
  try {
    const composer = page.locator(COMPOSER);
    await composer.fill("Burst 1");
    await composer.press("Enter");
    await composer.fill("Burst 2 with a file");
    await page.locator('.task-chat-compose input[type="file"]').setInputFiles({
      name: "burst.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("attached in a burst\n"),
    });
    await composer.press("Enter");
    await composer.fill("Burst 3");
    await composer.press("Enter");
    await composer.fill("Burst 4");
    await composer.press("Enter");
    const want = ["Burst 1", "Burst 2 with a file", "Burst 3", "Burst 4"];
    await expect(page.locator(OUTGOING)).toHaveText(want);
    await expect(page.locator(".task-chat-pending")).toHaveCount(0);
    await expect(page.locator(OUTGOING)).toHaveText(want);
    await expect(page.getByRole("link", { name: /burst\.txt/ })).toBeVisible();
    await bubblesInOrder(page);
    await expect.poll(() => storedHumanBodies(page, taskId)).toEqual(want);
  } finally {
    await release(page, taskId);
    await cleanup();
  }
});

test("two tabs on one session: one posts while the other polls, neither regresses", async ({
  page,
  context,
}) => {
  test.setTimeout(90_000);
  const { sessionId, taskId, cleanup } = await openSession(page, "Two tabs");
  const other = await context.newPage();
  try {
    await other.goto(`/session/${sessionId}`);
    await expect(
      other.getByRole("heading", { name: "Two tabs", exact: true }),
    ).toBeVisible();
    const seen = [];
    const sample = async () => {
      const texts = await other.locator(OUTGOING).allTextContents();
      seen.push(texts);
    };
    const composer = page.locator(COMPOSER);
    const want = [];
    for (let i = 1; i <= 4; i += 1) {
      await composer.fill(`Tab A ${i}`);
      await composer.press("Enter");
      want.push(`Tab A ${i}`);
      await sample();
    }
    await expect(page.locator(OUTGOING)).toHaveText(want);
    for (let i = 0; i < 20; i += 1) {
      await sample();
      if (seen.at(-1).length === want.length) break;
      await other.waitForTimeout(500);
    }
    await expect(other.locator(OUTGOING)).toHaveText(want, { timeout: 20_000 });
    for (let i = 1; i < seen.length; i += 1) {
      expect(seen[i].length).toBeGreaterThanOrEqual(seen[i - 1].length);
      expect(seen[i].slice(0, seen[i - 1].length)).toEqual(seen[i - 1]);
    }
    // A reply from the other tab shows up in the first, after A's.
    await other.locator(COMPOSER).fill("Tab B reply");
    await other.locator(COMPOSER).press("Enter");
    await expect(page.locator(OUTGOING)).toHaveText([...want, "Tab B reply"], {
      timeout: 20_000,
    });
  } finally {
    await other.close();
    await release(page, taskId);
    await cleanup();
  }
});

test("keyboard: Enter sends, Shift+Enter breaks a line, focus stays in the composer", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const { taskId, cleanup } = await openSession(page, "Keyboard");
  try {
    const composer = page.locator(COMPOSER);
    await expect(composer).toHaveAttribute("role", "textbox");
    await expect(composer).toHaveAttribute("aria-label", "Message");
    await composer.click();
    await page.keyboard.type("line one");
    await page.keyboard.press("Shift+Enter");
    await page.keyboard.type("line two");
    await expect(page.locator(OUTGOING)).toHaveCount(0);
    await page.keyboard.press("Enter");
    await expect(page.locator(OUTGOING).first()).toContainText("line one");
    await expect(page.locator(OUTGOING).first()).toContainText("line two");
    await expect(composer).toBeFocused();
    await expect(composer).toHaveText("");
    await page.keyboard.type("typed straight after");
    await page.keyboard.press("Enter");
    await expect(page.locator(OUTGOING)).toHaveCount(2);
    await expect(composer).toBeFocused();
    await expect
      .poll(() => storedHumanBodies(page, taskId))
      .toEqual(["line one\nline two", "typed straight after"]);
  } finally {
    await release(page, taskId);
    await cleanup();
  }
});
