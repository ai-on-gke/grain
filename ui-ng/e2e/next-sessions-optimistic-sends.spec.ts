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

import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

interface SessionJson {
  id: string;
  title?: string;
  taskId?: string;
}

interface CommentJson {
  id?: number;
  authorKind?: string;
  body?: string;
}

async function apiJson<T>(
  demoServer: string,
  accessCode: string,
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const res = await fetch(`${demoServer}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${accessCode}`,
      "Content-Type": "application/json",
      ...(init.headers ?? {}),
    },
  });
  if (!res.ok) {
    throw new Error(`${init.method ?? "GET"} ${path}: ${res.status}`);
  }
  const text = await res.text();
  return (text ? JSON.parse(text) : {}) as T;
}

async function createStandingSession(
  demoServer: string,
  accessCode: string,
  title: string,
): Promise<{
  sessionId: string;
  taskId: string;
  cleanup: () => Promise<void>;
}> {
  const created = await apiJson<{
    created?: string;
    sessions?: SessionJson[];
  }>(demoServer, accessCode, "/api/console-sessions", {
    method: "POST",
    body: JSON.stringify({ title }),
  });
  const sessionId = created.created || created.sessions?.at(-1)?.id || "";
  expect(sessionId).toBeTruthy();

  const cleanup = async () => {
    await fetch(
      `${demoServer}/api/console-sessions/${encodeURIComponent(sessionId)}`,
      {
        method: "DELETE",
        headers: { Authorization: `Bearer ${accessCode}` },
      },
    ).catch(() => undefined);
  };

  let taskId = "";
  await expect
    .poll(
      async () => {
        const list = await apiJson<{ sessions?: SessionJson[] }>(
          demoServer,
          accessCode,
          "/api/console-sessions",
        );
        taskId = list.sessions?.find((s) => s.id === sessionId)?.taskId ?? "";
        return taskId;
      },
      { timeout: 15_000 },
    )
    .toBeTruthy();

  return { sessionId, taskId, cleanup };
}

async function humanBodiesOnServer(
  demoServer: string,
  accessCode: string,
  taskId: string,
): Promise<string[]> {
  const detail = await apiJson<{ comments?: CommentJson[] }>(
    demoServer,
    accessCode,
    `/api/tasks/${encodeURIComponent(taskId)}?comments_limit=50`,
  );
  return (detail.comments ?? [])
    .filter((c) => c.authorKind === "human")
    .map((c) => c.body ?? "");
}

async function holdTaskComments(
  page: Page,
  taskIdPattern: string,
  opts: { failCount?: number } = {},
) {
  const releases: (() => void)[] = [];
  const bodies: string[] = [];
  let seen = 0;
  await page.route(`**/api/tasks/${taskIdPattern}/comments`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    const n = ++seen;
    try {
      const parsed = route.request().postDataJSON() as { body?: string };
      bodies.push(parsed?.body ?? "");
    } catch {
      bodies.push("");
    }
    await new Promise<void>((resolve) => releases.push(resolve));
    if (n <= (opts.failCount ?? 0)) {
      await route
        .fulfill({
          status: 500,
          contentType: "application/json",
          body: JSON.stringify({ error: "synthetic send failure" }),
        })
        .catch(() => undefined);
      return;
    }
    await route.continue().catch(() => undefined);
  });
  return {
    heldCount: () => releases.length,
    seenBodies: () => [...bodies],
    releaseNext: () => releases.shift()?.(),
    releaseAll: () => {
      while (releases.length > 0) releases.shift()?.();
    },
  };
}

test(
  "Sessions shows optimistic Sending and Queued bubbles, ignores double-Enter, and delivers queued sends in order",
  { tag: ["@phone", "@dark"] },
  async ({ page, demoServer, accessCode, axe }) => {
    test.setTimeout(90_000);
    const title = `Optimistic queue ${Date.now()}`;
    const { sessionId, taskId, cleanup } = await createStandingSession(
      demoServer,
      accessCode,
      title,
    );
    const gate = await holdTaskComments(page, taskId);

    try {
      await page.goto(`sessions/${sessionId}`);
      await expect(
        page.getByRole("heading", { level: 2, name: title, exact: true }),
      ).toBeVisible();

      const log = page.getByRole("log", { name: "Conversation" });
      const composer = page.getByRole("textbox", {
        name: `Message ${title}`,
      });
      await expect(composer).toBeVisible();

      // Type first message and press Enter twice rapidly: clears once, posts once.
      await composer.fill("First message held in flight");
      await composer.press("Enter");
      await composer.press("Enter");
      await expect(composer).toHaveValue("");
      await expect(composer).toBeFocused();

      // Type second message while POST #1 is still held: enters Queued state.
      await composer.fill("Second message queued behind first");
      await composer.press("Enter");
      await expect(composer).toHaveValue("");

      // Type a third unsent thought: stays untouched in composer.
      await composer.fill("Third draft kept in composer");

      const articles = log.getByRole("article");
      const firstBubble = articles.filter({
        hasText: "First message held in flight",
      });
      const secondBubble = articles.filter({
        hasText: "Second message queued behind first",
      });

      await expect(firstBubble).toBeVisible();
      await expect(firstBubble).toHaveAttribute("aria-busy", "true");
      await expect(firstBubble).toContainText("Sending…");

      await expect(secondBubble).toBeVisible();
      await expect(secondBubble).toHaveAttribute("aria-busy", "true");
      await expect(secondBubble).toContainText("Queued");

      // Empty state does not show alongside pending bubbles, and polite status announces count.
      await expect(log.getByText("No messages yet.")).toHaveCount(0);
      await expect(
        page.getByRole("status").filter({ hasText: "Sending 2 messages…" }),
      ).toHaveCount(1);

      // Only POST #1 has hit the network while held; double-Enter did not duplicate it.
      await expect.poll(gate.heldCount).toBe(1);
      expect(gate.seenBodies()).toEqual(["First message held in flight"]);

      // Assert bounding-box order, scroll containment above composer, and no horizontal overflow.
      const viewport = page.viewportSize()!;
      const logBox = (await log.boundingBox())!;
      const composerBox = (await composer.boundingBox())!;
      const firstBefore = (await firstBubble.boundingBox())!;
      const secondBefore = (await secondBubble.boundingBox())!;

      expect(firstBefore.x).toBeGreaterThanOrEqual(0);
      expect(firstBefore.x + firstBefore.width).toBeLessThanOrEqual(
        viewport.width + 1,
      );
      expect(secondBefore.x).toBeGreaterThanOrEqual(0);
      expect(secondBefore.x + secondBefore.width).toBeLessThanOrEqual(
        viewport.width + 1,
      );
      expect(firstBefore.y).toBeLessThan(secondBefore.y);
      expect(secondBefore.y).toBeGreaterThanOrEqual(logBox.y - 1);
      expect(secondBefore.y + secondBefore.height).toBeLessThanOrEqual(
        composerBox.y + 1,
      );

      // Sending/Queued status pill stays vertically centred on its actor header even on phone.
      const firstPillBox = (await firstBubble
        .getByText("Sending…", { exact: true })
        .boundingBox())!;
      const firstActorBox = (await firstBubble
        .getByTestId("actor-chip")
        .boundingBox())!;
      expect(
        Math.abs(
          firstPillBox.y +
            firstPillBox.height / 2 -
            (firstActorBox.y + firstActorBox.height / 2),
        ),
      ).toBeLessThanOrEqual(2);

      expect(await axe()).toEqual([]);

      // Release POST #1 while keeping POST #2 held: second bubble transitions Queued -> Sending….
      gate.releaseNext();
      await expect.poll(gate.heldCount).toBe(1);
      expect(gate.seenBodies()).toEqual([
        "First message held in flight",
        "Second message queued behind first",
      ]);
      await expect(
        secondBubble.getByText("Sending…", { exact: true }),
      ).toBeVisible();
      await expect(
        secondBubble.getByText("Queued", { exact: true }),
      ).toHaveCount(0);

      // Release POST #2: both reconcile cleanly with server copies, zero duplicates, no horizontal jump.
      gate.releaseNext();
      await expect(log.getByText("Sending…", { exact: true })).toHaveCount(0);
      await expect(log.getByText("Queued", { exact: true })).toHaveCount(0);
      await expect(firstBubble).toHaveCount(1);
      await expect(secondBubble).toHaveCount(1);
      await expect(composer).toHaveValue("Third draft kept in composer");

      const firstAfter = (await firstBubble.boundingBox())!;
      expect(Math.abs(firstAfter.x - firstBefore.x)).toBeLessThanOrEqual(2);
      expect(
        Math.abs(firstAfter.width - firstBefore.width),
      ).toBeLessThanOrEqual(2);

      await expect
        .poll(() => humanBodiesOnServer(demoServer, accessCode, taskId))
        .toEqual([
          "First message held in flight",
          "Second message queued behind first",
        ]);
    } finally {
      gate.releaseAll();
      await page.unrouteAll({ behavior: "ignoreErrors" });
      await cleanup();
    }
  },
);

test(
  "a failed send stays marked Not sent with Retry and Discard, without losing text or clobbering the composer",
  { tag: ["@phone", "@dark"] },
  async ({ page, demoServer, accessCode, axe }) => {
    test.setTimeout(90_000);
    const title = `Retryable send ${Date.now()}`;
    const { sessionId, taskId, cleanup } = await createStandingSession(
      demoServer,
      accessCode,
      title,
    );
    const gate = await holdTaskComments(page, taskId, { failCount: 1 });

    try {
      await page.goto(`sessions/${sessionId}`);
      const log = page.getByRole("log", { name: "Conversation" });
      const composer = page.getByRole("textbox", {
        name: `Message ${title}`,
      });

      await composer.fill("Needs retry after failure");
      await composer.press("Enter");
      await composer.fill("Draft typed meanwhile");

      await expect.poll(gate.heldCount).toBe(1);
      gate.releaseNext();

      const failedBubble = log
        .getByRole("article")
        .filter({ hasText: "Needs retry after failure" });
      await expect(failedBubble).toBeVisible();
      await expect(failedBubble).toContainText("Not sent");
      await expect(failedBubble).toContainText("synthetic send failure");
      await expect(
        failedBubble.getByRole("button", { name: "Retry", exact: true }),
      ).toBeVisible();
      await expect(
        failedBubble.getByRole("button", { name: "Discard", exact: true }),
      ).toBeVisible();
      await expect(composer).toHaveValue("Draft typed meanwhile");

      expect(await axe()).toEqual([]);

      // Click Retry: the second POST succeeds once released and reconciles cleanly.
      await failedBubble
        .getByRole("button", { name: "Retry", exact: true })
        .click();
      await expect(failedBubble).toContainText("Sending…");
      await expect.poll(gate.heldCount).toBe(1);
      gate.releaseNext();

      await expect(log.getByText("Not sent")).toHaveCount(0);
      await expect(log.getByText("Sending…")).toHaveCount(0);
      await expect(failedBubble).toHaveCount(1);
      await expect(composer).toHaveValue("Draft typed meanwhile");

      await expect
        .poll(() => humanBodiesOnServer(demoServer, accessCode, taskId))
        .toEqual(["Needs retry after failure"]);
    } finally {
      gate.releaseAll();
      await page.unrouteAll({ behavior: "ignoreErrors" });
      await cleanup();
    }
  },
);

test(
  "New session dialog carries queued follow-ups and an in-progress draft into the opened session",
  { tag: ["@phone", "@dark"] },
  async ({ page, demoServer, accessCode, axe }) => {
    test.setTimeout(90_000);
    const title = `New session handoff ${Date.now()}`;
    let releaseCreate!: () => void;
    const createHeld = new Promise<void>((r) => {
      releaseCreate = r;
    });

    await page.route("**/api/console-sessions", async (route) => {
      if (route.request().method() === "POST") {
        await createHeld;
      }
      await route.continue().catch(() => undefined);
    });
    const commentGate = await holdTaskComments(page, "*");

    let createdSessionId = "";
    try {
      await page.goto("sessions");
      await page
        .getByRole("region", { name: "Sessions" })
        .getByRole("button", { name: "New session" })
        .click();

      const dialog = page.getByRole("dialog", { name: "New session" });
      await expect(dialog).toBeVisible();
      await dialog.getByLabel("Name").fill(title);
      const msgBox = dialog.getByLabel("First message (optional)");
      await msgBox.fill("First message creating session");
      await dialog
        .getByRole("button", { name: "Start session", exact: true })
        .click();

      // While POST /api/console-sessions is held: first message shows Sending… and box clears.
      await expect(msgBox).toHaveValue("");
      const dialogLog = dialog.getByRole("log", { name: "Queued messages" });
      await expect(
        dialogLog
          .getByRole("article")
          .filter({ hasText: "First message creating session" }),
      ).toContainText("Sending…");

      // Queue a second message via Enter and type a third unsent draft while create is still held.
      await msgBox.fill("Second message queued in dialog");
      await msgBox.press("Enter");
      await expect(msgBox).toHaveValue("");
      await expect(
        dialogLog
          .getByRole("article")
          .filter({ hasText: "Second message queued in dialog" }),
      ).toContainText("Queued");

      await msgBox.fill("Third draft carried over");
      expect(await axe()).toEqual([]);

      // Release session creation while keeping the follow-up comment POST held.
      releaseCreate();
      await expect(dialog).toHaveCount(0);
      await expect(page).toHaveURL(/\/next\/sessions\/([^/?#]+)$/);
      createdSessionId =
        /\/next\/sessions\/([^/?#]+)$/.exec(page.url())?.[1] ?? "";
      expect(createdSessionId).toBeTruthy();

      const sessionLog = page.getByRole("log", { name: "Conversation" });
      const composer = page.getByRole("textbox", {
        name: `Message ${title}`,
      });
      await expect(composer).toBeFocused();
      await expect(composer).toHaveValue("Third draft carried over");

      await expect(
        sessionLog
          .getByRole("article")
          .filter({ hasText: "First message creating session" }),
      ).toBeVisible();
      const carriedSecond = sessionLog
        .getByRole("article")
        .filter({ hasText: "Second message queued in dialog" });
      await expect(carriedSecond).toBeVisible();
      await expect(carriedSecond).toContainText("Sending…");

      await expect.poll(commentGate.heldCount).toBe(1);
      commentGate.releaseNext();
      await expect(sessionLog.getByText("Sending…")).toHaveCount(0);

      const list = await apiJson<{ sessions?: SessionJson[] }>(
        demoServer,
        accessCode,
        "/api/console-sessions",
      );
      const taskId =
        list.sessions?.find((s) => s.id === createdSessionId)?.taskId ?? "";
      expect(taskId).toBeTruthy();
      await expect
        .poll(() => humanBodiesOnServer(demoServer, accessCode, taskId))
        .toEqual([
          "First message creating session",
          "Second message queued in dialog",
        ]);
    } finally {
      releaseCreate();
      commentGate.releaseAll();
      await page.unrouteAll({ behavior: "ignoreErrors" });
      if (createdSessionId) {
        await fetch(
          `${demoServer}/api/console-sessions/${encodeURIComponent(createdSessionId)}`,
          {
            method: "DELETE",
            headers: { Authorization: `Bearer ${accessCode}` },
          },
        ).catch(() => undefined);
      }
    }
  },
);
