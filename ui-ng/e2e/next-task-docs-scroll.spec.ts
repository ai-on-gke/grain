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

import { test, expect, type Locator, type Page } from "./fixtures.js";

test.use({ demoArgs: ["-as", "demo-operator"] });

const LONG_PROMPT = [
  "# Task Specification",
  "",
  'Implement container-responsive **task detail** layout, the `Docs` tab, and `<dialog class="doc-reader-modal">`.',
  "",
  ...Array.from(
    { length: 28 },
    (_, i) =>
      `## Requirement Section ${i + 1}\n\nVerify scroll containment, clean \`--md-sys-color-surface\` reading surface, and responsive collapse for section ${i + 1}.`,
  ),
].join("\n");

const LONG_PR_DOC_DIFF = [
  "diff --git a/design/design/ui-design-book.design.md b/design/design/ui-design-book.design.md",
  "new file mode 100644",
  "--- /dev/null",
  "+++ b/design/design/ui-design-book.design.md",
  "@@ -0,0 +1,42 @@",
  "+# Design Book Addendum",
  "+",
  "+Container query `@container task-page (max-width: 1060px)` collapses `.task-aside` when narrow.",
  ...Array.from(
    { length: 38 },
    (_, i) =>
      `+- Rule ${i + 1}: Keep independent vertical scroll on readers and modals.`,
  ),
].join("\n");

const LONG_REPORT_COMMENT = [
  "## Verification Report",
  "",
  ...Array.from(
    { length: 34 },
    (_, i) =>
      `- Check ${i + 1}: Verified layout and scroll at desktop and phone.`,
  ),
].join("\n");

const LONG_COMMENTS = [
  ...Array.from({ length: 12 }, (_, i) => ({
    id: 100 + i,
    author: i % 2 === 0 ? "demo-operator" : "agent",
    authorKind: i % 2 === 0 ? "human" : "agent",
    authorLabel: i % 2 === 0 ? "Demo Operator" : "Agent",
    createdAt: `2026-11-16T10:${String(i).padStart(2, "0")}:00Z`,
    body: `Progress update ${i + 1}: checking **scroll containment** across all task panes and verifying \`scrollTop > 0\`.`,
  })),
  {
    id: 200,
    author: "agent",
    authorKind: "agent",
    authorLabel: "Agent",
    summary: "Verification Report",
    createdAt: "2026-11-16T10:30:00Z",
    body: LONG_REPORT_COMMENT,
  },
];

async function apiJson<T>(
  baseURL: string,
  code: string,
  path: string,
): Promise<T> {
  const res = await fetch(`${baseURL}${path}`, {
    headers: { Authorization: `Bearer ${code}` },
  });
  if (!res.ok)
    throw new Error(`GET ${path}: ${res.status} ${await res.text()}`);
  return (await res.json()) as T;
}

async function setTheme(page: Page, mode: "light" | "dark"): Promise<void> {
  await page.evaluate((m) => {
    localStorage.setItem("grain.themeMode", m);
    document.documentElement.setAttribute("data-theme", m);
  }, mode);
  await expect(page.locator("html")).toHaveAttribute("data-theme", mode);
}

async function advanceScroll(locator: Locator): Promise<number> {
  await expect(locator).toBeVisible();
  const box = await locator.boundingBox();
  expect(box).toBeTruthy();
  const initial = await locator.evaluate((el) => ({
    scrollHeight: el.scrollHeight,
    clientHeight: el.clientHeight,
    scrollTop: el.scrollTop,
  }));
  expect(
    initial.scrollHeight,
    `expected overflow on element (scrollHeight=${initial.scrollHeight}, clientHeight=${initial.clientHeight})`,
  ).toBeGreaterThan(initial.clientHeight + 10);

  if (initial.scrollTop > 0) {
    await locator.evaluate((el) => {
      el.scrollTop = 0;
    });
  }
  await locator
    .page()
    .mouse.move(
      box!.x + Math.min(box!.width / 2, 120),
      box!.y + Math.min(box!.height / 2, 120),
    );
  await locator.page().mouse.wheel(0, 400);
  await expect
    .poll(() => locator.evaluate((el) => el.scrollTop))
    .toBeGreaterThan(0);
  return locator.evaluate((el) => el.scrollTop);
}

async function routeRichTask(page: Page, taskId: string): Promise<void> {
  await page.route(
    new RegExp(`/api/tasks/${taskId}(\\?.*)?$`),
    async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      const headers = { ...route.request().headers() };
      delete headers["if-none-match"];
      const res = await route.fetch({ headers });
      const text = await res.text();
      if (!text) return route.fulfill({ response: res });
      const body = JSON.parse(text) as Record<string, unknown>;
      body["description"] = LONG_PROMPT;
      body["repo"] = (body["repo"] as string) || "acme/web";
      body["branch"] = "grain/task-docs";
      body["base"] = "main";
      body["state"] =
        body["state"] === "proposed" ? "awaiting_submit" : body["state"];
      body["comments"] = LONG_COMMENTS;
      await route.fulfill({ response: res, json: body });
    },
  );

  await page.route(
    /\/api\/repos\/[^/]+\/[^/]+\/(github-)?branches(\?.*)?$/,
    async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ branches: ["main", "grain/task-docs"] }),
      });
    },
  );

  await page.route(
    /\/api\/repos\/[^/]+\/[^/]+\/compare(\?.*)?$/,
    async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          base: "main",
          head: "grain/task-docs",
          diff: LONG_PR_DOC_DIFF,
        }),
      });
    },
  );
}

test("Standalone Task: container collapse on Director open, always-visible Details, Docs tab, doc-reader-modal, and real scrollTop > 0 @dark", async ({
  page,
  demoServer,
  accessCode,
  axe,
}) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 1440, height: 900 });

  const tasks = await apiJson<unknown>(demoServer, accessCode, "/api/tasks");
  const list = Array.isArray(tasks)
    ? (tasks as { id: string }[])
    : ((tasks as { tasks?: { id: string }[] }).tasks ?? []);
  const targetId = list[0]?.id;
  expect(targetId).toBeTruthy();

  await routeRichTask(page, targetId!);

  for (const theme of ["light", "dark"] as const) {
    await page.addInitScript(
      (t) => localStorage.setItem("grain.themeMode", t),
      theme,
    );
    await page.goto(`/next/task/${targetId}`);
    await setTheme(page, theme);
    await expect(page.getByTestId("task-tab-details")).toBeVisible();

    // Before Director: .task-page > 1060px and .task-aside is visible.
    const pageBoxBefore = await page.locator(".task-page").boundingBox();
    expect(pageBoxBefore!.width).toBeGreaterThan(1060);
    await expect(page.locator(".task-aside")).toBeVisible();

    // Open Director drawer: .task-page narrows <= 1060px and .task-aside collapses via @container.
    await page.getByTestId("director-trigger").click();
    await expect(page.getByTestId("director-drawer")).toBeVisible();
    await expect
      .poll(
        async () =>
          (await page.locator(".task-page").boundingBox())?.width ?? 9999,
      )
      .toBeLessThanOrEqual(1060);
    await expect(page.locator(".task-aside")).toBeHidden();
    const mainBox = await page.locator(".task-main").boundingBox();
    const layoutBox = await page.locator(".task-layout").boundingBox();
    expect(mainBox!.width).toBeGreaterThanOrEqual(layoutBox!.width - 2);

    // Close Director drawer: .task-aside returns.
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("director-drawer")).toHaveCount(0);
    await expect(page.locator(".task-aside")).toBeVisible();

    // Conversation thread scrolls (scrollTop > 0).
    await advanceScroll(page.locator(".task-tab-body .convo-thread"));

    // Details tab stays visible above 1080px, does not redirect, and scrolls.
    await page.getByTestId("task-tab-details").click();
    await expect(page).toHaveURL(new RegExp(`tab=details`));
    await expect(page.getByTestId("task-tab-details")).toHaveAttribute(
      "aria-current",
      "page",
    );
    await expect(
      page
        .locator('[data-testid="task-details"] .description')
        .filter({ visible: true }),
    ).toBeVisible();
    await advanceScroll(page.locator(".task-tab-body"));

    // Open Docs from Prompt pane button.
    await page.getByTestId("prompt-open-docs").click();
    await expect(page).toHaveURL(new RegExp(`tab=docs`));
    await expect(page.getByTestId("task-docs-shell")).toBeVisible();
    await expect(page.getByTestId("doc-item-prompt")).toBeVisible();
    await expect(
      page.getByTestId("doc-item-pr:design/design/ui-design-book.design.md"),
    ).toBeVisible();
    await expect(page.getByTestId("doc-item-comment-200")).toBeVisible();

    // Clean surface background (not gray sidebar) and ~76ch measure.
    const readerMetrics = await page.evaluate(() => {
      const reader = document.querySelector(
        '[data-testid="docs-reader-scroll"]',
      )!;
      const article = document.querySelector(
        '[data-testid="docs-reader-body"]',
      )!;
      const surfaceProbe = document.createElement("span");
      surfaceProbe.style.backgroundColor = "var(--md-sys-color-surface)";
      document.body.appendChild(surfaceProbe);
      const expectedBg = getComputedStyle(surfaceProbe).backgroundColor;
      surfaceProbe.remove();
      return {
        readerBg: getComputedStyle(reader).backgroundColor,
        expectedBg,
        articleWidth: article.getBoundingClientRect().width,
      };
    });
    expect(readerMetrics.readerBg).toBe(readerMetrics.expectedBg);
    expect(readerMetrics.articleWidth).toBeGreaterThan(300);
    expect(readerMetrics.articleWidth).toBeLessThanOrEqual(900);

    // Docs reader advances scrollTop > 0 independently.
    await advanceScroll(page.getByTestId("docs-reader-scroll"));

    // Switch to PR doc and scroll independently.
    await page
      .getByTestId("doc-item-pr:design/design/ui-design-book.design.md")
      .click();
    await expect(page.getByTestId("docs-reader-title")).toContainText(
      "ui-design-book.design.md",
    );
    await advanceScroll(page.getByTestId("docs-reader-scroll"));

    expect(await axe()).toEqual([]);

    // Open native full-screen modal from Docs tab.
    await page.getByTestId("docs-fullscreen").click();
    const modal = page.getByTestId("doc-reader-modal");
    await expect(modal).toBeVisible();
    await expect(modal).toHaveAttribute("aria-modal", "true");
    await expect(page.getByTestId("doc-modal-pos")).toHaveText("2 / 3");

    // Modal advances scrollTop > 0.
    await advanceScroll(page.getByTestId("doc-modal-scroll"));

    // Prev / Next navigation between documents.
    await page.getByTestId("doc-modal-next").click();
    await expect(page.getByTestId("doc-modal-pos")).toHaveText("3 / 3");
    await expect(page.getByTestId("doc-modal-body")).toContainText(
      "Verification Report",
    );
    await page.getByTestId("doc-modal-prev").click();
    await page.getByTestId("doc-modal-prev").click();
    await expect(page.getByTestId("doc-modal-pos")).toHaveText("1 / 3");
    await expect(page.getByTestId("doc-modal-body")).toContainText(
      "Task Specification",
    );

    expect(await axe()).toEqual([]);

    // Close via ×, reopen from Prompt ⤢, dismiss via backdrop, reopen and dismiss via Escape.
    await page.getByTestId("doc-modal-close").click();
    await expect(modal).not.toHaveAttribute("open", "");

    await page.getByTestId("task-tab-details").click();
    await page.getByTestId("prompt-fullscreen").click();
    await expect(modal).toHaveAttribute("open", "");
    await page.mouse.click(8, 8);
    await expect(modal).not.toHaveAttribute("open", "");

    await page.getByTestId("prompt-fullscreen").click();
    await expect(modal).toHaveAttribute("open", "");
    await page.keyboard.press("Escape");
    await expect(modal).not.toHaveAttribute("open", "");
  }
});

test("Phone 390x844: Docs tab, Details scroll, and full-screen doc-reader-modal in light & dark @phone", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(90_000);
  await page.setViewportSize({ width: 390, height: 844 });

  const tasks = await apiJson<unknown>(demoServer, accessCode, "/api/tasks");
  const list = Array.isArray(tasks)
    ? (tasks as { id: string }[])
    : ((tasks as { tasks?: { id: string }[] }).tasks ?? []);
  const targetId = list[0]?.id;
  expect(targetId).toBeTruthy();

  await routeRichTask(page, targetId!);

  for (const theme of ["light", "dark"] as const) {
    await page.goto(`/next/task/${targetId}?tab=docs`);
    await setTheme(page, theme);

    await expect(page.locator(".task-aside")).toBeHidden();
    await expect(page.getByTestId("task-tab-details")).toBeVisible();
    await expect(page.getByTestId("task-docs-shell")).toBeVisible();

    const sideways = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(sideways).toBeLessThanOrEqual(0);

    await advanceScroll(page.getByTestId("docs-reader-scroll"));

    // Open modal on phone and verify width fits 390px and scrollTop > 0.
    await page.getByTestId("docs-fullscreen").click();
    const modal = page.getByTestId("doc-reader-modal");
    await expect(modal).toHaveAttribute("open", "");
    const mBox = await modal.boundingBox();
    expect(mBox!.width).toBeLessThanOrEqual(390.5);
    await advanceScroll(page.getByTestId("doc-modal-scroll"));
    await page.getByTestId("doc-modal-next").click();
    await expect(page.getByTestId("doc-modal-pos")).toHaveText("2 / 3");
    await page.keyboard.press("Escape");
    await expect(modal).not.toHaveAttribute("open", "");

    await page.getByTestId("task-tab-details").click();
    await advanceScroll(page.locator(".task-tab-body"));
  }
});

test("Agents inspector Details and Workstream inspector Conversation scroll cleanly (scrollTop > 0) @dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(90_000);
  await page.setViewportSize({ width: 1440, height: 900 });

  const tasks = await apiJson<unknown>(demoServer, accessCode, "/api/tasks");
  const list = Array.isArray(tasks)
    ? (tasks as {
        id: string;
        state: string;
        consoleSession?: unknown;
        origin?: string;
      }[])
    : ((
        tasks as {
          tasks?: {
            id: string;
            state: string;
            consoleSession?: unknown;
            origin?: string;
          }[];
        }
      ).tasks ?? []);
  const running = list.find((t) => t.state === "running" && !t.consoleSession);
  expect(running).toBeTruthy();

  await routeRichTask(page, running!.id);

  // 1. Agents inspector Details tab scroll.
  await page.goto(`/next/agents?task=${running!.id}`);
  await expect(page.getByTestId("agents-pane")).toBeVisible();
  await page.getByTestId("task-tab-details").click();
  await expect(
    page.locator('[data-testid="agents-pane"] [data-testid="task-details"]'),
  ).toBeVisible();
  const agentScroller = page.locator(
    '[data-testid="agents-pane"] .task-tab-body',
  );
  await advanceScroll(agentScroller);

  // 2. Workstream inspector Conversation scroll.
  await routeRichTask(page, "\\d+");
  await page.goto("/next/workstreams");
  const wsRow = page.locator('[data-testid^="ws-row-"]').first();
  await expect(wsRow).toBeVisible();
  await wsRow.click();
  await expect(page.getByTestId("ws-inspector")).toBeVisible();
  await expect(
    page.locator('[data-testid="ws-inspector"] g-task-detail'),
  ).toBeVisible();
  await advanceScroll(
    page.locator('[data-testid="ws-inspector"] .convo-thread'),
  );
});

test("Inbox renders Markdown formatting in access/release/question summaries cleanly @phone @dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(60_000);

  const tasks = await apiJson<unknown>(demoServer, accessCode, "/api/tasks");
  const list = Array.isArray(tasks)
    ? (tasks as {
        id: string;
        title: string;
        state: string;
        consoleSession?: unknown;
      }[])
    : ((
        tasks as {
          tasks?: {
            id: string;
            title: string;
            state: string;
            consoleSession?: unknown;
          }[];
        }
      ).tasks ?? []);
  const target =
    list.find((t) => !t.consoleSession && t.state !== "closed") ?? list[0];
  expect(target).toBeTruthy();

  await page.route(/\/api\/tasks(\?.*)?$/, async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const res = await route.fetch({ headers });
    const body = await res.json();
    const arr = Array.isArray(body) ? body : body.tasks;
    const hit = arr.find((t: { id: string }) => t.id === target!.id);
    if (hit) {
      hit.state = "awaiting_reply";
      hit.awaitingAccess = true;
      hit.turnStopped = false;
      hit.pendingSecret = undefined;
      hit.questionSummary = "Grant **acme/web** `api:ci-read` access";
    }
    await route.fulfill({ response: res, json: body });
  });

  await page.route(
    new RegExp(`/api/tasks/${target!.id}(\\?.*)?$`),
    async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      const headers = { ...route.request().headers() };
      delete headers["if-none-match"];
      const res = await route.fetch({ headers });
      const body = await res.json();
      body.state = "awaiting_reply";
      body.awaitingAccess = true;
      body.turnStopped = false;
      body.pendingSecret = undefined;
      body.pendingAccess = {
        summary: "Grant **acme/web** `api:ci-read` access",
        reason: "Needs `api:ci-read` to inspect workflow runs.",
        createdAt: "2026-11-16T10:00:00Z",
        items: [
          {
            kind: "capability",
            grant: "api:ci-read@acme/web",
            requiresPerson: true,
          },
        ],
      };
      await route.fulfill({ response: res, json: body });
    },
  );

  await page.goto("/next/inbox");
  const row = page
    .getByTestId("queue-row")
    .filter({ hasText: target!.title })
    .first();
  await expect(row).toBeVisible();
  await expect(row).not.toContainText("**acme/web**");
  await row.locator(".row-main").click();

  const accessSummary = page.getByTestId("access-summary");
  await expect(accessSummary).toBeVisible();
  await expect(accessSummary.locator("strong")).toHaveText("acme/web");
  await expect(accessSummary.locator("code")).toHaveText("api:ci-read");
  await expect(accessSummary).not.toContainText("**acme/web**");
});
