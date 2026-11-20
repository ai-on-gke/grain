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

import { test, expect } from "./fixtures.js";

// One pane, one scroller (task 5626). With a long thread, the Conversation
// tab of the task page and of the Tasks and Agents inspectors fills its tab
// body: the thread is the only thing that scrolls, the composer sits pinned
// below it inside the pane, and wheeling the thread never moves the tab body
// or the page around it. When an inline editor opens on a task page and leaves
// the tabs under 320px, the page scrolls so the editor's Save stays reachable.
// On a phone the tab lays out flat in the scrolling page, the thread keeps its
// inline window, and the composer can be reached. The Workstreams inspector is
// the one scroller for "In this stream" plus the task, which lays out flat
// inside it; a CI log runs at full length in the run inspector with a sticky
// .ci-log-bar; the Inbox live transcript grows inside the thread rather than
// scrolling on its own; and stacked Context scrolls a picked document's detail
// into view. Fixtures are served via page.route so the shared demo store is
// left alone.

const EXTRA = 40;

async function apiJson(baseURL, code, p) {
  const res = await fetch(`${baseURL}${p}`, {
    headers: { Authorization: `Bearer ${code}` },
  });
  if (!res.ok) throw new Error(`GET ${p}: ${res.status} ${await res.text()}`);
  return res.json();
}

// longThread answers GET /api/tasks/{id} with EXTRA more comments ahead of
// the task's own. If-None-Match is dropped so the server sends a body.
async function longThread(page, id) {
  await page.route(new RegExp(`/api/tasks/${id}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const res = await route.fetch({ headers });
    const body = await res.json();
    const extra = [];
    for (let i = 1; i <= EXTRA; i++) {
      extra.push({
        id: 900000 + i,
        author: "grain",
        authorKind: "automation",
        body: `Progress note ${i}: ${"the step went as planned and the next one is under way. ".repeat(3)}`,
        createdAt: new Date(Date.UTC(2026, 9, 1, 0, i)).toISOString(),
      });
    }
    body.comments = [...extra, ...(body.comments ?? [])];
    await route.fulfill({ response: res, json: body });
  });
}

// scrolling lists the elements among sel that scroll and have something to
// scroll, by class name.
async function scrolling(page, selectors) {
  return page.evaluate((sels) => {
    const out = [];
    for (const s of sels) {
      for (const el of document.querySelectorAll(s)) {
        const oy = getComputedStyle(el).overflowY;
        if (
          (oy === "auto" || oy === "scroll") &&
          el.scrollHeight > el.clientHeight + 1
        ) {
          out.push(s);
        }
      }
    }
    return out;
  }, selectors);
}

async function openTask(baseURL, code) {
  const tasks = await apiJson(baseURL, code, "/api/tasks");
  const list = Array.isArray(tasks) ? tasks : tasks.tasks;
  const open = list.find(
    (t) => t.state !== "closed" && !t.consoleSession && !t.interactive,
  );
  expect(open, "grain demo seeds an open task").toBeTruthy();
  return open;
}

const CANDIDATES = [".task-page", ".task-tab-body", ".convo-thread"];

async function expectThreadIsTheScroller(page, root) {
  const thread = page.locator(`${root} .convo-thread`);
  await expect(thread).toBeVisible();
  await expect
    .poll(() =>
      scrolling(
        page,
        CANDIDATES.map((c) => `${root} ${c}`),
      ),
    )
    .toEqual([`${root} .convo-thread`]);

  const box = async (s) => page.locator(`${root} ${s}`).first().boundingBox();
  const body = await box(".task-tab-body");
  const composer = await box(".convo-composer");
  const threadBox = await box(".convo-thread");
  const viewport = page.viewportSize();
  expect(composer, "the composer is shown").toBeTruthy();
  expect(composer.y + composer.height).toBeLessThanOrEqual(
    body.y + body.height + 0.5,
  );
  expect(body.y + body.height).toBeLessThanOrEqual(viewport.height + 0.5);
  expect(Math.abs(threadBox.y + threadBox.height - composer.y)).toBeLessThan(2);
  // The conversation fills the tab body rather than leaving a gap below.
  expect(body.y + body.height - (composer.y + composer.height)).toBeLessThan(
    40,
  );

  // Wheeling the thread scrolls it and nothing around it.
  const before = await thread.evaluate((el) => el.scrollTop);
  expect(before, "the thread opens at its latest message").toBeGreaterThan(0);
  await thread.hover();
  await page.mouse.wheel(0, -1500);
  await expect
    .poll(() => thread.evaluate((el) => el.scrollTop))
    .toBeLessThan(before);
  const outer = await page.evaluate(
    (r) =>
      [".task-page", ".task-tab-body"].map(
        (s) => document.querySelector(`${r} ${s}`).scrollTop,
      ),
    root,
  );
  expect(outer).toEqual([0, 0]);
}

test.describe("task conversation has one scroller", () => {
  test.beforeEach(({}, testInfo) => {
    test.skip(
      testInfo.project.name === "phone",
      "sets its own viewports, phone included",
    );
  });

  for (const vp of [
    { width: 1440, height: 900 },
    { width: 1280, height: 800 },
  ]) {
    test(`task page at ${vp.width}x${vp.height}: only the thread scrolls and the composer stays in view`, async ({
      page,
      demoServer,
      accessCode,
    }) => {
      const t = await openTask(demoServer, accessCode);
      await longThread(page, t.id);
      await page.setViewportSize(vp);
      await page.goto(`/next/tasks/${t.id}?tab=conversation`);
      await expectThreadIsTheScroller(page, "g-task-page");
    });
  }

  test("Tasks inspector at 1440x900: only the thread scrolls and the composer stays in the pane", async ({
    page,
    demoServer,
    accessCode,
  }) => {
    const t = await openTask(demoServer, accessCode);
    await longThread(page, t.id);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`/next/tasks?task=${t.id}`);
    const pane = page.getByTestId("tasks-pane");
    await expect(pane).toContainText(`#${t.id}`);
    await pane.getByTestId("task-tab-conversation").click();
    await expectThreadIsTheScroller(page, '[data-testid="tasks-pane"]');
    const paneBox = await pane.boundingBox();
    const composer = await pane.locator(".convo-composer").boundingBox();
    expect(composer.y + composer.height).toBeLessThanOrEqual(
      paneBox.y + paneBox.height + 0.5,
    );
  });

  test("phone task page: the tab lays out flat, only the thread scrolls in it, and the composer can be reached", async ({
    page,
    demoServer,
    accessCode,
  }) => {
    const t = await openTask(demoServer, accessCode);
    await longThread(page, t.id);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`/next/tasks/${t.id}?tab=conversation`);
    await expect(page.locator(".task-tab-body .convo-thread")).toBeVisible();
    // The thread scrolls; the tab body around it does not.
    await expect
      .poll(() => scrolling(page, [".task-tab-body", ".convo-thread"]))
      .toEqual([".convo-thread"]);
    // Scrolled to its end, the task shows the composer inside it.
    const end = await page.evaluate(() => {
      const p = document.querySelector(".task-page");
      p.scrollTop = p.scrollHeight;
      const c = document.querySelector(".convo-composer");
      return {
        composerBottom: c.getBoundingClientRect().bottom,
        pageBottom: p.getBoundingClientRect().bottom,
      };
    });
    expect(end.composerBottom).toBeLessThanOrEqual(end.pageBottom + 0.5);
  });

  test("task page 320px floor: opening the inline editor lets the page scroll so Save stays reachable", async ({
    page,
    demoServer,
    accessCode,
  }) => {
    const t = await openTask(demoServer, accessCode);
    await longThread(page, t.id);
    await page.setViewportSize({ width: 1280, height: 700 });
    await page.goto(`/next/tasks/${t.id}?tab=conversation`);
    await expectThreadIsTheScroller(page, "g-task-page");

    await page.getByTestId("act-edit").click();
    await expect(page.getByTestId("task-edit-panel")).toBeVisible();
    await expect
      .poll(() => scrolling(page, ["g-task-page .task-page"]))
      .toEqual(["g-task-page .task-page"]);

    const save = page.getByTestId("edit-save-btn");
    await save.scrollIntoViewIfNeeded();
    const [pageBox, saveBox] = await Promise.all([
      page.locator("g-task-page .task-page").boundingBox(),
      save.boundingBox(),
    ]);
    expect(saveBox.y).toBeGreaterThanOrEqual(pageBox.y - 0.5);
    expect(saveBox.y + saveBox.height).toBeLessThanOrEqual(
      pageBox.y + pageBox.height + 0.5,
    );
  });

  test("Agents inspector at 1440x900: only the thread scrolls and the composer stays in the pane", async ({
    page,
    demoServer,
    accessCode,
  }) => {
    const tasks = await apiJson(demoServer, accessCode, "/api/tasks");
    const list = Array.isArray(tasks) ? tasks : tasks.tasks;
    const running = list.find(
      (t) => t.state === "running" && !t.consoleSession,
    );
    expect(running, "grain demo seeds a running task").toBeTruthy();
    await longThread(page, running.id);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`/next/agents?task=${running.id}`);
    const pane = page.getByTestId("agents-pane");
    await expect(pane).toBeVisible();
    await pane.getByTestId("task-tab-conversation").click();
    await expectThreadIsTheScroller(page, '[data-testid="agents-pane"]');
    const paneBox = await pane.boundingBox();
    const composer = await pane.locator(".convo-composer").boundingBox();
    expect(composer.y + composer.height).toBeLessThanOrEqual(
      paneBox.y + paneBox.height + 0.5,
    );
  });

  test("Workstreams inspector at 1440x900: the inspector is the one scroller and the task lays out flat in it", async ({
    page,
  }) => {
    await longThread(page, "\\d+");
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/next/workstreams");
    const row = page.locator('[data-testid^="ws-row-"]').first();
    await expect(row).toBeVisible();
    await row.click();
    const insp = page.getByTestId("ws-inspector");
    await expect(insp.locator("g-task-detail")).toBeVisible();
    const root = '[data-testid="ws-inspector"]';
    // On Details, .ws-insp is the sole vertical scroller in the inspector.
    await expect
      .poll(() =>
        scrolling(page, [
          root,
          `${root} .task-page`,
          `${root} .task-tab-body`,
          `${root} .task-aside`,
        ]),
      )
      .toEqual([root]);
    const tab = insp.getByTestId("task-tab-conversation");
    if (await tab.count()) await tab.click();
    await expect(insp.locator(".convo-thread")).toBeVisible();
    // Neither the task nor its tab body is a scroller inside the inspector.
    await expect
      .poll(() =>
        scrolling(page, [root, `${root} .task-page`, `${root} .task-tab-body`]),
      )
      .toEqual([root]);
    const styles = await page.evaluate(
      (r) =>
        [".task-page", ".task-tab-body"].map(
          (s) =>
            getComputedStyle(document.querySelector(`${r} ${s}`)).overflowY,
        ),
      root,
    );
    expect(styles).toEqual(["visible", "visible"]);
    expect(await insp.evaluate((el) => getComputedStyle(el).overflowY)).toBe(
      "auto",
    );
    // Scrolled to its end, the inspector shows the composer inside it.
    const end = await insp.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
      const c = el.querySelector(".convo-composer");
      return {
        composerBottom: c.getBoundingClientRect().bottom,
        inspBottom: el.getBoundingClientRect().bottom,
      };
    });
    expect(end.composerBottom).toBeLessThanOrEqual(end.inspBottom + 0.5);
  });

  test("CI run inspector at 1440x900: log runs at full length in the inspector and .ci-log-bar stays in view", async ({
    page,
  }) => {
    const logLines = Array.from(
      { length: 140 },
      (_, i) =>
        `2026-11-17T00:00:${String(i % 60).padStart(2, "0")}Z step line ${i + 1}: checking package invariants`,
    ).join("\n");
    const run = {
      id: "ci-scroll-1",
      provider: "grain",
      repo: "acme/widgets",
      ref: "refs/heads/grain/task-101",
      commit: "aaa111122223333",
      event: "pull_request",
      requestedBy: "101",
      conclusion: "failure",
      startedAt: "2026-11-17T00:00:00Z",
      finishedAt: "2026-11-17T00:02:00Z",
      jobs: [
        {
          jobId: "unit",
          name: "Go unit suite",
          workflow: "CI",
          conclusion: "failure",
          startedAt: "2026-11-17T00:00:00Z",
          finishedAt: "2026-11-17T00:02:00Z",
          steps: [
            {
              name: "go test ./...",
              conclusion: "failure",
              exitCode: 1,
              output: logLines,
              startedAt: "2026-11-17T00:00:05Z",
              finishedAt: "2026-11-17T00:01:55Z",
            },
          ],
        },
      ],
    };
    await page.route(/\/api\/ci\/runs(\?.*)?$/, (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ json: { runs: [run] } })
        : route.continue(),
    );
    await page.route(/\/api\/ci\/runs\/ci-scroll-1(\?.*)?$/, (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ json: run })
        : route.continue(),
    );

    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/next/ci");
    const insp = page.getByTestId("ci-inspector");
    const viewer = insp.getByTestId("ci-log-viewer");
    await expect(viewer).toBeVisible();

    // Only .ci-inspector scrolls; the log well and lines run at full length.
    await expect
      .poll(() =>
        scrolling(page, [
          ".ci-inspector",
          ".ci-job-card",
          ".ci-log-well",
          ".ci-log-lines",
        ]),
      )
      .toEqual([".ci-inspector"]);

    // Scroll halfway down the log and verify .ci-log-bar sticks at the top of .ci-inspector.
    await insp.evaluate((el) => {
      const well = el.querySelector(".ci-log-well");
      const offset =
        well.getBoundingClientRect().top -
        el.getBoundingClientRect().top +
        el.scrollTop;
      el.scrollTop = offset + 500;
    });
    const [inspBox, wellBox, barBox] = await Promise.all([
      insp.boundingBox(),
      viewer.boundingBox(),
      viewer.locator(".ci-log-bar").boundingBox(),
    ]);
    expect(wellBox.y).toBeLessThan(inspBox.y - 200);
    expect(wellBox.y + wellBox.height).toBeGreaterThan(inspBox.y + 200);
    expect(Math.abs(barBox.y - inspBox.y)).toBeLessThan(2);
    expect(barBox.y + barBox.height).toBeLessThanOrEqual(
      inspBox.y + inspBox.height + 0.5,
    );
  });

  test("Inbox live transcript at 1440x900: grows inside the thread without a nested scroller", async ({
    page,
  }) => {
    const transcriptLines = Array.from(
      { length: 60 },
      (_, i) => `[step ${i + 1}] verifying cluster rollout health...`,
    ).join("\n");
    const task99001 = {
      id: "99001",
      title: "Run cluster deploy verification",
      state: "running",
      repo: "acme/widgets",
      createdAt: "2026-11-17T00:00:00Z",
      updatedAt: "2026-11-17T00:05:00Z",
      attempts: [{ number: 1, startedAt: "2026-11-17T00:01:00Z" }],
      comments: [],
    };
    await page.route(/\/api\/local-script-approvals(\?.*)?$/, (route) =>
      route.request().method() === "GET"
        ? route.fulfill({
            json: [
              {
                id: "lsa-scroll-1",
                taskId: "99001",
                script: "verify-rollout",
                args: ["--prod"],
                requestedAt: "2026-11-17T00:02:00Z",
              },
            ],
          })
        : route.continue(),
    );
    await page.route(/\/api\/tasks\/99001(\?.*)?$/, (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ json: task99001 })
        : route.continue(),
    );
    await page.route(
      /\/api\/tasks\/99001\/attempts\/1\/transcript(\?.*)?$/,
      (route) =>
        route.request().method() === "GET"
          ? route.fulfill({ json: { transcript: transcriptLines } })
          : route.continue(),
    );

    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/next/inbox");
    const row = page
      .getByTestId("queue-row")
      .filter({ hasText: "verify-rollout" });
    await expect(row).toBeVisible();
    await row.click();

    const insp = page.getByTestId("next-inbox-inspector");
    const live = insp.getByTestId("inbox-live-transcript");
    await expect(live).toBeVisible();
    expect(await live.evaluate((el) => getComputedStyle(el).overflowY)).toBe(
      "visible",
    );
    await expect
      .poll(() =>
        scrolling(page, [
          '[data-testid="next-inbox-inspector"] .convo-thread',
          '[data-testid="next-inbox-inspector"] .inbox-live-transcript',
        ]),
      )
      .toEqual(['[data-testid="next-inbox-inspector"] .convo-thread']);
  });

  test("stacked Context at 390x844: picking a document brings its detail into view", async ({
    page,
  }) => {
    const docs = [];
    for (let i = 0; i < 30; i++) {
      docs.push({
        name: `notes-${String(i).padStart(2, "0")}`,
        summary: `Notes on part ${i} of the system`,
        text: `# Part ${i}\n\nWhat we know about part ${i}.`,
        length: 40,
      });
    }
    await page.route(/\/api\/memory\/documents(\?.*)?$/, (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ json: { documents: docs } })
        : route.continue(),
    );
    await page.route(/\/api\/memory\/documents\/[^/?]+(\?.*)?$/, (route) => {
      if (route.request().method() !== "GET") return route.continue();
      const name = decodeURIComponent(
        new URL(route.request().url()).pathname.split("/").pop(),
      );
      return route.fulfill({
        json: docs.find((d) => d.name === name) ?? docs[0],
      });
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/next/context?tab=documents");
    const split = page.getByTestId("context-split");
    const detail = page.locator(".ctx-detail-pane");
    await expect(split).toBeVisible();
    await expect(
      page.locator('[data-testid="doc-row-notes-03"]'),
    ).toBeVisible();

    // Only .ctx-split scrolls, and before selection .ctx-detail-pane sits below the viewport.
    await expect
      .poll(() =>
        scrolling(page, [
          ".ctx-split",
          ".ctx-list-pane",
          ".ctx-list",
          ".ctx-detail-pane",
        ]),
      )
      .toEqual([".ctx-split"]);
    expect((await detail.boundingBox()).y).toBeGreaterThanOrEqual(844);

    await page.locator('[data-testid="doc-row-notes-03"] .ctx-row-btn').click();
    await expect
      .poll(async () => {
        const [s, d] = await Promise.all([
          split.boundingBox(),
          detail.boundingBox(),
        ]);
        return d.y >= 0 && d.y < 844 && Math.abs(d.y - s.y) < 2;
      })
      .toBe(true);

    // Tapping + New pack from the top of the list also reveals the detail pane.
    await split.evaluate((el) => {
      el.scrollTop = 0;
    });
    expect((await detail.boundingBox()).y).toBeGreaterThanOrEqual(844);
    await page.getByTestId("doc-new-btn").click();
    await expect
      .poll(async () => {
        const [s, d] = await Promise.all([
          split.boundingBox(),
          detail.boundingBox(),
        ]);
        return d.y >= 0 && d.y < 844 && Math.abs(d.y - s.y) < 2;
      })
      .toBe(true);
  });
});
