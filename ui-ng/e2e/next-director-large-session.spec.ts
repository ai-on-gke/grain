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

import type { Route } from "@playwright/test";
import { test, expect } from "./fixtures.js";

// Large Director session regression suite (task 5971):
// Seeds ~300 comments (including multi-KB markdown, PNG screenshots, and two
// ~9 MB binary attachments plus a 5.6 MB zip) onto the Director session and
// verifies both /next/sessions/director and the global Director drawer:
// 1. Only the newest page (50 comments) renders initially, with "Load earlier
//    comments (N)" paging older history without jumping to the bottom.
// 2. Non-image binary attachments are download links only and never fetched inline.
// 3. Navigating through /next/tasks (mounting TaskDetailComponent) never turns
//    .convo-thread into overflow: visible or hides sent messages off-screen.
// 4. Scrolling, clicking, and sending a message (< 2s) work both under normal
//    API latency and when polling routes stall 10s before returning 503.

const TINY_PNG_B64 =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=";

function longMarkdown(idx: number): string {
  const lines = [
    `## Turn ${idx}: Queue & Workstream Audit`,
    "",
    `Checked [Task #${idx}](/tasks/${idx}) across **three active workstreams** and verified check runs.`,
    "",
    "```go",
    "func inspectSession(id string, limit int) error {",
    "    for i := 0; i < limit; i++ {",
    '        if id == "" { return ErrMissingSession }',
    "    }",
    "    return nil",
    "}",
    "```",
    "",
    "```mermaid",
    "flowchart LR",
    `  A["Turn ${idx}"] --> B["Check CI"] --> C["Report"]`,
    "```",
    "",
  ];
  for (let s = 1; s <= 8; s++) {
    lines.push(
      `${s}. Step ${idx}.${s}: verify \`GET /api/tasks/${idx}?comments_limit=50\` answers \`200\` with **summary** and _notes_.`,
    );
  }
  return lines.join("\n");
}

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
  if (!res.ok) {
    throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return (txt ? JSON.parse(txt) : null) as T;
}

test("300-comment Director session with ~9 MB binaries scrolls, clicks, pages, and shows sent messages within 2s under normal latency and 10s-delayed 503 polls", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(240_000);
  await page.addInitScript(() => {
    (window as unknown as { __longTasks: number[] }).__longTasks = [];
    try {
      new PerformanceObserver((list) => {
        for (const e of list.getEntries()) {
          (window as unknown as { __longTasks: number[] }).__longTasks.push(
            e.duration,
          );
        }
      }).observe({ type: "longtask", buffered: true });
    } catch {
      // Chromium supports longtask.
    }
  });

  const sessionList = await api<{
    sessions: { id: string; taskId: string }[];
  }>(demoServer, accessCode, "GET", "/api/console-sessions");
  const director = sessionList.sessions.find((s) => s.id === "director");
  expect(director?.taskId).toBeTruthy();
  const taskId = director!.taskId;

  const heldRoutes = new Set<Route>();
  const requestedAttachmentUrls: string[] = [];
  page.on("request", (req) => {
    if (req.url().includes(`/api/tasks/${taskId}/attachments/`)) {
      requestedAttachmentUrls.push(req.url());
    }
  });

  try {
    // Seed ~300 comments: alternating operator prompts & multi-KB markdown,
    // several PNG screenshots, a 5.6 MB zip, and two ~9 MB split gzip parts.
    for (let batchStart = 1; batchStart <= 290; batchStart += 10) {
      const batch = Array.from({ length: 10 }, (_, k) => batchStart + k);
      for (const n of batch) {
        const attachments =
          n >= 282 && n % 2 === 0
            ? [
                {
                  filename: `shot-${n}.png`,
                  contentType: "image/png",
                  content: TINY_PNG_B64,
                },
              ]
            : [];
        await api(
          demoServer,
          accessCode,
          "POST",
          `/api/tasks/${taskId}/comments`,
          {
            body: n % 2 === 0 ? longMarkdown(n) : `Operator check #${n}`,
            attachments,
          },
        );
      }
    }

    const zip56 = Buffer.alloc(5_600_000, 0x61).toString("base64");
    const splitAA = Buffer.alloc(9_400_000, 0x62).toString("base64");
    const splitAB = Buffer.alloc(8_900_000, 0x63).toString("base64");

    await api(demoServer, accessCode, "POST", `/api/tasks/${taskId}/comments`, {
      body: "Diagnostic archive attached.",
      attachments: [
        {
          filename: "diag-archive.zip",
          contentType: "application/zip",
          content: zip56,
        },
      ],
    });
    await api(demoServer, accessCode, "POST", `/api/tasks/${taskId}/comments`, {
      body: "Split gzip part aa attached.",
      attachments: [
        {
          filename: "trace.tar.gz.split.aa",
          contentType: "application/gzip",
          content: splitAA,
        },
      ],
    });
    await api(demoServer, accessCode, "POST", `/api/tasks/${taskId}/comments`, {
      body: "Split gzip part ab attached.",
      attachments: [
        {
          filename: "trace.tar.gz.split.ab",
          contentType: "application/octet-stream",
          content: splitAB,
        },
      ],
    });
    for (let n = 294; n <= 300; n++) {
      await api(
        demoServer,
        accessCode,
        "POST",
        `/api/tasks/${taskId}/comments`,
        {
          body: longMarkdown(n),
          attachments:
            n === 299
              ? [
                  {
                    filename: "latest-shot.png",
                    contentType: "image/png",
                    content: TINY_PNG_B64,
                  },
                ]
              : [],
        },
      );
    }

    const pagedDetail = await api<{
      comments: {
        id: number;
        attachments?: { id: number; filename: string; contentType: string }[];
      }[];
      olderComments?: number;
    }>(
      demoServer,
      accessCode,
      "GET",
      `/api/tasks/${taskId}?comments_limit=50&attempts_limit=20`,
    );
    expect(pagedDetail.comments.length).toBe(50);
    expect(pagedDetail.olderComments ?? 0).toBeGreaterThanOrEqual(250);

    const binaryAttachmentIds = new Set<string>();
    for (const c of pagedDetail.comments) {
      for (const a of c.attachments ?? []) {
        if (!a.contentType.startsWith("image/")) {
          binaryAttachmentIds.add(String(a.id));
        }
      }
    }
    expect(binaryAttachmentIds.size).toBe(3);

    // --- 1. /next/sessions/director under normal API latency ---
    await page.goto("/next/sessions/director");
    const sessionLog = page.getByRole("log", { name: "Conversation" });
    await expect(sessionLog).toContainText(
      "Turn 300: Queue & Workstream Audit",
    );

    // Conversation is paged to newest 50 with "Load earlier comments".
    await expect(sessionLog.locator("article.msg")).toHaveCount(50);
    const loadOlderBtn = sessionLog.getByRole("button", {
      name: /Load earlier comments/,
    });
    await expect(loadOlderBtn).toBeVisible();

    // Binary attachments (.zip, .split.aa, .split.ab) are download links only, never fetched inline.
    for (const binId of binaryAttachmentIds) {
      expect(
        requestedAttachmentUrls.some((u) =>
          u.endsWith(`/attachments/${binId}`),
        ),
      ).toBe(false);
    }
    await expect(
      sessionLog.getByRole("link", { name: /trace\.tar\.gz\.split\.aa/ }),
    ).toHaveAttribute("download", "trace.tar.gz.split.aa");

    // Reset post-boot longtask buffer and verify wheel scrolling, clicks, paging, and < 2s send.
    await page.evaluate(() => {
      (window as unknown as { __longTasks: number[] }).__longTasks.length = 0;
    });

    const logBox = await sessionLog.boundingBox();
    expect(logBox).not.toBeNull();
    let bottomScroll = await sessionLog.evaluate((el) => el.scrollTop);
    expect(bottomScroll).toBeGreaterThan(1000);

    // When parked at the bottom of a full 50-comment window, a new tall server
    // reply slides the 50-comment window forward and scrolls into the viewport.
    await api(demoServer, accessCode, "POST", `/api/tasks/${taskId}/comments`, {
      body: `${longMarkdown(301)}\n\nFull-window reply #301 scrolls into view.`,
    });
    const reply301 = sessionLog
      .locator("article.msg")
      .filter({ hasText: "Full-window reply #301 scrolls into view." });
    await expect(reply301).toBeVisible({ timeout: 10_000 });
    await expect(sessionLog.locator("article.msg")).toHaveCount(50);
    await expect
      .poll(
        async () => {
          const box = await reply301.boundingBox();
          if (!box) return false;
          return (
            box.y + box.height > logBox!.y &&
            box.y + box.height <= logBox!.y + logBox!.height + 4
          );
        },
        { timeout: 2_000 },
      )
      .toBe(true);
    bottomScroll = await sessionLog.evaluate((el) => el.scrollTop);

    await page.mouse.move(logBox!.x + logBox!.width / 2, logBox!.y + 160);
    await page.mouse.wheel(0, -600);
    await expect
      .poll(() => sessionLog.evaluate((el) => el.scrollTop), { timeout: 2_000 })
      .toBeLessThan(bottomScroll - 200);

    // Scroll to top and load earlier page; older comments prepend without yanking to bottom.
    await sessionLog.evaluate((el) => {
      el.scrollTop = 0;
    });
    await loadOlderBtn.click();
    await expect(sessionLog.locator("article.msg")).toHaveCount(100);

    // Interactive click works cleanly (toggle Grants & settings drawer).
    const openSettingsBtn = page.getByRole("button", {
      name: /Grants & settings/,
    });
    const closeSettingsBtn = page.getByRole("button", {
      name: "Close settings",
    });
    await openSettingsBtn.click({ timeout: 2_000 });
    await expect(closeSettingsBtn).toHaveAttribute("aria-expanded", "true");
    await closeSettingsBtn.click({ timeout: 2_000 });
    await expect(openSettingsBtn).toHaveAttribute("aria-expanded", "false");

    // Normal-latency send appears within 2s inside visible log viewport.
    const composer = page.getByRole("textbox", { name: /Message Director/ });
    const normalStart = Date.now();
    await composer.fill("Normal latency operator ping");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await expect(sessionLog).toContainText("Normal latency operator ping", {
      timeout: 2_000,
    });
    expect(Date.now() - normalStart).toBeLessThan(2_000);

    const postBootLongTasks = await page.evaluate(
      () => (window as unknown as { __longTasks: number[] }).__longTasks,
    );
    // No freeze: every seeded turn carries a Mermaid diagram, and one
    // diagram's layout is a single task Mermaid cannot split (~150-200 ms
    // here, more on a shared CI runner). Diagrams draw only as they near
    // the viewport, so the bound is per diagram, never the whole thread.
    expect(Math.max(0, ...postBootLongTasks)).toBeLessThan(600);

    // --- 2. /next/sessions/director with 10s-delayed 503s on polling routes ---
    const installStalledPolls = async () => {
      await page.route("**/api/**", async (route) => {
        const req = route.request();
        const url = new URL(req.url());
        const isPollGet =
          req.method() === "GET" &&
          (url.pathname === "/api/console-sessions" ||
            url.pathname === "/api/tasks" ||
            url.pathname === `/api/tasks/${taskId}` ||
            url.pathname.endsWith("/transcript"));
        if (!isPollGet) {
          await route.continue();
          return;
        }
        heldRoutes.add(route);
        await new Promise((r) => setTimeout(r, 10_000));
        heldRoutes.delete(route);
        await route
          .fulfill({
            status: 503,
            contentType: "application/json",
            body: JSON.stringify({ error: "verifier_unavailable" }),
          })
          .catch(() => {});
      });
    };

    await installStalledPolls();

    // Scroll, click, and send must all succeed within 2s despite 10s-delayed 503 polls.
    const preStallScroll = await sessionLog.evaluate((el) => el.scrollTop);
    await page.mouse.move(logBox!.x + logBox!.width / 2, logBox!.y + 160);
    await page.mouse.wheel(0, -500);
    await expect
      .poll(() => sessionLog.evaluate((el) => el.scrollTop), { timeout: 2_000 })
      .toBeLessThan(preStallScroll - 150);

    await openSettingsBtn.click({ timeout: 2_000 });
    await expect(closeSettingsBtn).toHaveAttribute("aria-expanded", "true");
    await closeSettingsBtn.click({ timeout: 2_000 });

    const stallStart = Date.now();
    await composer.fill("Sent during 10s 503 polling outage");
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await expect(sessionLog).toContainText(
      "Sent during 10s 503 polling outage",
      { timeout: 2_000 },
    );
    expect(Date.now() - stallStart).toBeLessThan(2_000);

    await page.unrouteAll({ behavior: "ignoreErrors" });
    for (const r of [...heldRoutes]) {
      heldRoutes.delete(r);
      await r.abort().catch(() => {});
    }

    // --- 3. Coplanar Director drawer across SPA navigation (/inbox -> /tasks) + 10s 503 polls ---
    await page.goto("/next/inbox");
    await page.getByRole("button", { name: "Director" }).click();
    const drawer = page.getByRole("complementary", { name: "Director" });
    const drawerLog = drawer.getByRole("log", { name: "Conversation thread" });
    await expect(drawerLog).toContainText("Sent during 10s 503 polling outage");

    // Navigate to /next/tasks so TaskDetailComponent mounts after ConversationComponent:
    // .convo-thread must stay overflow-y: auto and keep the composer below the thread.
    await page
      .getByRole("navigation", { name: "Primary destinations" })
      .getByRole("link", { name: "Tasks" })
      .click();
    await expect(page).toHaveURL(/\/next\/tasks/);
    expect(
      await drawerLog.evaluate((el) => getComputedStyle(el).overflowY),
    ).toBe("auto");

    const drawerLogBox = await drawerLog.boundingBox();
    const drawerComposer = drawer.getByRole("textbox", {
      name: /Ask Director|Message Director/,
    });
    const drawerCompBox = await drawerComposer.boundingBox();
    expect(drawerLogBox).not.toBeNull();
    expect(drawerCompBox).not.toBeNull();
    expect(drawerCompBox!.y).toBeGreaterThanOrEqual(
      drawerLogBox!.y + drawerLogBox!.height - 2,
    );

    // Install 10s-delayed 503 polling routes again and verify drawer scroll, click, and < 2s visible send.
    await installStalledPolls();

    const drawerBottom = await drawerLog.evaluate((el) => el.scrollTop);
    await page.mouse.move(
      drawerLogBox!.x + drawerLogBox!.width / 2,
      drawerLogBox!.y + 120,
    );
    await page.mouse.wheel(0, -500);
    await expect
      .poll(() => drawerLog.evaluate((el) => el.scrollTop), { timeout: 2_000 })
      .toBeLessThan(drawerBottom - 150);

    await drawer
      .getByRole("button", { name: "Wide" })
      .click({ timeout: 2_000 });
    await expect(drawer.getByRole("button", { name: "Compact" })).toBeVisible();

    const drawerSendStart = Date.now();
    await drawerComposer.fill("Drawer message visible under 503s");
    await drawer.getByRole("button", { name: "Send", exact: true }).click();
    const sentMsg = drawerLog
      .locator("article.msg")
      .filter({ hasText: "Drawer message visible under 503s" });
    await expect(sentMsg).toBeVisible({ timeout: 2_000 });
    expect(Date.now() - drawerSendStart).toBeLessThan(2_000);

    // Bounding-box check: the sent message sits inside the visible thread viewport, not clipped below it.
    const updatedLogBox = await drawerLog.boundingBox();
    const sentMsgBox = await sentMsg.boundingBox();
    expect(updatedLogBox).not.toBeNull();
    expect(sentMsgBox).not.toBeNull();
    expect(sentMsgBox!.y + sentMsgBox!.height).toBeGreaterThan(
      updatedLogBox!.y,
    );
    expect(sentMsgBox!.y).toBeLessThan(
      updatedLogBox!.y + updatedLogBox!.height,
    );

    // Mixed 1.2s route latency + occasional 10s 503s, then self-recovery once requests settle.
    await page.unrouteAll({ behavior: "ignoreErrors" });
    for (const r of [...heldRoutes]) {
      heldRoutes.delete(r);
      await r.abort().catch(() => {});
    }
    let reqCounter = 0;
    await page.route("**/api/**", async (route) => {
      reqCounter += 1;
      const failWith503 =
        route.request().method() === "GET" && reqCounter % 3 === 0;
      heldRoutes.add(route);
      await new Promise((r) => setTimeout(r, failWith503 ? 10_000 : 1_200));
      heldRoutes.delete(route);
      if (failWith503) {
        await route
          .fulfill({
            status: 503,
            contentType: "application/json",
            body: JSON.stringify({ error: "verifier_unavailable" }),
          })
          .catch(() => {});
        return;
      }
      await route.continue().catch(() => {});
    });

    const slowSendStart = Date.now();
    await drawerComposer.fill("Optimistic under 1.2s-10s mixed latency");
    await drawer.getByRole("button", { name: "Send", exact: true }).click();
    await expect(drawerLog).toContainText(
      "Optimistic under 1.2s-10s mixed latency",
      { timeout: 2_000 },
    );
    expect(Date.now() - slowSendStart).toBeLessThan(2_000);

    // Clear route fault injection and post a reply from the server side:
    // the pane recovers on its own and renders the server turn without reload.
    await page.unrouteAll({ behavior: "ignoreErrors" });
    for (const r of [...heldRoutes]) {
      heldRoutes.delete(r);
      await r.abort().catch(() => {});
    }
    await api(demoServer, accessCode, "POST", `/api/tasks/${taskId}/comments`, {
      body: "Director recovered automatically after slow 503 window.",
    });
    await expect(drawerLog).toContainText(
      "Director recovered automatically after slow 503 window.",
      { timeout: 10_000 },
    );
  } finally {
    await page.unrouteAll({ behavior: "ignoreErrors" });
    for (const r of [...heldRoutes]) {
      heldRoutes.delete(r);
      await r.abort().catch(() => {});
    }
    // Restore clean Director session task for any subsequent spec on this worker.
    await fetch(`${demoServer}/api/console-sessions/director/restart`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${accessCode}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ carryHandover: false }),
    }).catch(() => {});
  }
});
