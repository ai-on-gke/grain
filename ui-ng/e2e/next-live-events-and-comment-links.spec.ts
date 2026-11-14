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

// /next/inbox answers GET /api/events at once, and /next/task/<id>#comment=
// pages back to an old comment in 500-comment reads and lands on it
// (docs/test-plans/2026-11-13-afternoon.md §2; task 5484). The inbox's own
// timer is 60 s, so a row inside 2 s came from the event stream.

import type { Page, Request } from "@playwright/test";
import { expect, test } from "./fixtures";

const LIVE_MS = 2000;

async function api<T = unknown>(
  base: string,
  code: string,
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const res = await fetch(`${base}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return (text ? JSON.parse(text) : null) as T;
}

const row = (page: Page, key: string) =>
  page.locator(`[data-testid="queue-row"][data-key="${key}"]`);

test("/next/inbox shows, updates and clears notification and host-script rows from /api/events within 2 s @phone @dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(90_000);
  const call = <T = unknown>(method: string, path: string, body?: unknown) =>
    api<T>(demoServer, accessCode, method, path, body);
  const stamp = Date.now().toString(36);
  const task = await call<{ id: string }>("POST", "/api/tasks", {
    title: `live inbox e2e ${stamp}`,
    body: "e2e",
    repo: "acme/widgets",
    capabilities: ["local-scripts:e2e-live"],
  });
  const L = `/api/tasks/${task.id}/local-script-approvals`;
  let noteId: number | null = null;
  try {
    await page.goto("inbox");
    await expect(page.getByTestId("next-inbox-queue")).toBeVisible();

    const raised = await call<{ notification: { id: number } }>(
      "POST",
      "/api/notifications",
      {
        subject: `e2e.live.${stamp}`,
        title: `e2e ${stamp} red`,
        severity: "warning",
        task: task.id,
      },
    );
    noteId = raised.notification.id;
    const note = row(page, `notification:${noteId}`);
    await expect(note).toContainText(`e2e ${stamp} red`, { timeout: LIVE_MS });

    await call("POST", "/api/notifications", {
      subject: `e2e.live.${stamp}`,
      title: `e2e ${stamp} very red`,
      severity: "critical",
      task: task.id,
    });
    await expect(note).toContainText("very red", { timeout: LIVE_MS });

    const reread = page.waitForRequest(
      (r: Request) =>
        r.method() === "GET" &&
        new URL(r.url()).pathname === "/api/notifications",
      { timeout: LIVE_MS },
    );
    await call("POST", `/api/notifications/${noteId}/ack`, {});
    await reread;

    await call("POST", `/api/notifications/${noteId}/clear`, {});
    await expect(note).toHaveCount(0, { timeout: LIVE_MS });
    noteId = null;

    const ask = (script: string, secs: number) =>
      call<{ id: string }>("POST", L, {
        tool: "host_script",
        script,
        args: ["--e2e", stamp],
        deadline: new Date(Date.now() + secs * 1000).toISOString(),
      });
    const settle = async (
      script: string,
      secs: number,
      answer: (id: string) => Promise<unknown>,
    ) => {
      const a = await ask(script, secs);
      const r = row(page, `local-script:${task.id}:${a.id}`);
      await expect(r).toBeVisible({ timeout: LIVE_MS });
      await answer(a.id);
      await expect(r).toHaveCount(0, { timeout: LIVE_MS });
    };
    await settle("deploy.sh", 120, (id) =>
      call("POST", `${L}/${id}/answer`, { approve: true }),
    );
    await settle("rollback.sh", 120, (id) =>
      call("POST", `${L}/${id}/answer`, { approve: false }),
    );
    await settle("migrate.sh", 120, (id) =>
      call("POST", `${L}/${id}/expire`, {}),
    );
    // Past its 2 s deadline, a read of the task sweeps it to expired, and
    // the event that sweep emits clears the row.
    await settle("slow.sh", 2, async () => {
      await new Promise((r) => setTimeout(r, 2200));
      await call("GET", `/api/tasks/${task.id}`);
    });
  } finally {
    if (noteId !== null) {
      await call("POST", `/api/notifications/${noteId}/clear`, {}).catch(
        () => undefined,
      );
    }
    await call("POST", `/api/tasks/${task.id}/close`, {}).catch(
      () => undefined,
    );
  }
});

test("/next/task/<id>#comment= pages back 500 at a time, renders once, and lands on the comment", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(180_000);
  const call = <T = unknown>(method: string, path: string, body?: unknown) =>
    api<T>(demoServer, accessCode, method, path, body);
  const task = await call<{ id: string }>("POST", "/api/tasks", {
    title: `deep link e2e ${Date.now().toString(36)}`,
    body: "e2e",
    repo: "acme/widgets",
  });
  try {
    const COUNT = 1100;
    for (let i = 1; i <= COUNT; i++) {
      await call("POST", `/api/tasks/${task.id}/comments`, {
        body: `e2e comment ${i}`,
      });
    }
    type Page_ = {
      comments?: { id: number; body?: string }[];
      olderComments?: number;
    };
    const all: { id: number; body?: string }[] = [];
    let p = await call<Page_>(
      "GET",
      `/api/tasks/${task.id}?comments_limit=500`,
    );
    for (;;) {
      all.unshift(...(p.comments ?? []));
      if (!p.olderComments || !p.comments?.length) break;
      p = await call<Page_>(
        "GET",
        `/api/tasks/${task.id}?comments_limit=500&comments_before=${p.comments[0].id}`,
      );
    }
    const mine = all.filter((c) => /^e2e comment \d+$/.test(c.body ?? ""));
    expect(mine).toHaveLength(COUNT);
    const tenth = mine[9].id;

    const reads: string[] = [];
    page.on("request", (r) => {
      const u = new URL(r.url());
      if (
        r.method() === "GET" &&
        u.pathname === `/api/tasks/${task.id}` &&
        /comments_(limit|before)/.test(u.search)
      ) {
        reads.push(u.search);
      }
    });
    await page.addInitScript(() => {
      const w = window as unknown as { __counts: number[] };
      w.__counts = [];
      const tick = () => {
        const n = document.querySelectorAll(
          "article.msg[data-comment-id]",
        ).length;
        if (n && w.__counts.at(-1) !== n) w.__counts.push(n);
        requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
    });

    // /next/tasks/<id> redirects to /next/task/<id> keeping the fragment.
    await page.goto(`tasks/${task.id}#comment=${tenth}`);
    const target = page.locator(`article.msg[data-comment-id="${tenth}"]`);
    await expect(target).toHaveClass(/comment-highlight/, { timeout: 30_000 });
    await expect(page).toHaveURL(
      new RegExp(`/next/task/${task.id}#comment=${tenth}$`),
    );
    await expect(target).toBeInViewport();
    await expect(target).toBeFocused();
    // The newest 500, then 500 and 100 older: three reads, one render.
    expect(reads.length).toBeLessThanOrEqual(Math.ceil(COUNT / 500));
    expect(
      await page.evaluate(
        () => (window as unknown as { __counts: number[] }).__counts,
      ),
    ).toEqual([all.length]);

    // A link followed on the open page lands too; a missing one says so.
    const twentieth = mine[19].id;
    await page.evaluate((id) => (location.hash = `#comment=${id}`), twentieth);
    await expect(
      page.locator(`article.msg[data-comment-id="${twentieth}"]`),
    ).toHaveClass(/comment-highlight/, { timeout: 10_000 });
    await page.evaluate(() => (location.hash = "#comment=999999999"));
    await expect(page.getByTestId("snackbar")).toContainText(
      "is not in this task's conversation",
      { timeout: 10_000 },
    );
  } finally {
    await call("POST", `/api/tasks/${task.id}/close`, {}).catch(
      () => undefined,
    );
  }
});
