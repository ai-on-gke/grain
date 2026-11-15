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

// Scenario spec for docs/test-plans/2026-11-13.md §1 items 3, 4, 7, 8, and 9
// over `grain demo -github-sim -fake-ci`: Director drawer context + compact
// Tab focus trap + in-place task shaping, Sessions next-turn model + 390px
// Artifact/Memory canvas zero-overflow, Inbox diff preview on proposals &
// truncated fallback link, conditional ETag polling (304), and single-h1 invariant.

import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { signIn } from "../../ui-shared/e2e/accesscode.js";
import { test, expect } from "./fixtures.js";

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

function findDemoBareRepo(tmpRoot) {
  for (const entry of fs.readdirSync(tmpRoot)) {
    const candidate = path.join(
      tmpRoot,
      entry,
      "githubsim",
      "acme",
      "widgets.git",
    );
    if (fs.existsSync(candidate)) return candidate;
  }
  throw new Error(`could not find acme/widgets.git under ${tmpRoot}`);
}

function pushCommitToBareBranch(tmpRoot, bareRepo, branch, filesMap, message) {
  const work = fs.mkdtempSync(path.join(tmpRoot, "gitpush-"));
  try {
    execFileSync("git", ["clone", "-q", bareRepo, work]);
    execFileSync("git", ["-C", work, "checkout", "-qb", branch, "origin/main"]);
    for (const [rel, content] of Object.entries(filesMap)) {
      const full = path.join(work, rel);
      fs.mkdirSync(path.dirname(full), { recursive: true });
      fs.writeFileSync(full, content);
    }
    execFileSync("git", ["-C", work, "add", "."]);
    execFileSync("git", [
      "-C",
      work,
      "-c",
      "user.name=Walk Seed",
      "-c",
      "user.email=seed@example.com",
      "commit",
      "-qm",
      message,
    ]);
    execFileSync("git", ["-C", work, "push", "-q", "-f", "origin", branch]);
  } finally {
    fs.rmSync(work, { recursive: true, force: true });
  }
}

async function apiJson(demoUrl, code, urlPath, opts = {}) {
  const res = await fetch(`${demoUrl}${urlPath}`, {
    ...opts,
    headers: {
      Authorization: `Bearer ${code}`,
      ...(opts.headers || {}),
    },
  });
  const text = await res.text();
  return { status: res.status, body: text ? JSON.parse(text) : null };
}

test("demo walkthrough: Director focus trap & in-place shaping, Sessions 390px canvas & next-turn model, Inbox proposal diff preview, 304 polling, and single h1", async ({
  browser,
}) => {
  test.setTimeout(150_000);
  const grainBin = process.env.GRAIN_E2E_DEMO_BIN;
  expect(grainBin).toBeTruthy();

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "grain-e2e-walkdemo-"));
  const codeFile = path.join(tmpDir, "access-code");
  let output = "";
  let demoUrl = "";

  const demoProc = spawn(
    grainBin,
    [
      "demo",
      "-github-sim",
      "-fake-ci",
      "-human-review",
      "-reconcile",
      "2s",
      "-addr",
      "127.0.0.1:0",
      "-access-code-file",
      codeFile,
    ],
    {
      env: { ...process.env, TMPDIR: tmpDir },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );

  try {
    demoUrl = await new Promise((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error(`grain demo start timed out:\n${output}`)),
        30_000,
      );
      const onChunk = (buf) => {
        output += buf.toString();
        const m = output.match(/at http:\/\/127\.0\.0\.1:(\d+)/);
        if (m) {
          clearTimeout(timer);
          resolve(`http://127.0.0.1:${m[1]}`);
        }
      };
      demoProc.stdout.on("data", onChunk);
      demoProc.stderr.on("data", onChunk);
      demoProc.on("exit", (c) =>
        reject(new Error(`grain demo exited (${c}):\n${output}`)),
      );
    });

    const code = fs.readFileSync(codeFile, "utf8").trim();
    const storageState = await signIn(demoUrl, code, "alice@example.com");
    const bareWidgets = findDemoBareRepo(tmpDir);

    const ctx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      storageState,
    });
    const page = await ctx.newPage();

    // 1. Director drawer: cross-page context, compact Tab trap, Escape focus restore, and in-place shaping
    await page.goto(`${demoUrl}/next/tasks`, { waitUntil: "domcontentloaded" });
    await page.waitForSelector(".main-stage > *:not(router-outlet)");
    expect(await page.locator("h1").count()).toBe(1);

    await page.getByTestId("director-trigger").click();
    const drawer = page.getByTestId("director-drawer");
    await expect(drawer).toBeVisible();
    await page.locator('[data-testid^="open-"]').first().click();
    await expect(page.getByTestId("director-context-toggle")).toContainText(
      "Task #",
    );

    await page.locator('a[href="/next/ci"]').first().click();
    await page.waitForURL("**/next/ci**");
    await expect(drawer).toBeVisible();

    await page.keyboard.press("Escape");
    await expect(drawer).toHaveCount(0);

    // Compact modal Director (< 1080px) traps Tab inside drawer and restores focus on Escape
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByTestId("director-trigger").click();
    await expect(drawer).toBeVisible();
    for (let i = 0; i < 8; i++) {
      await page.keyboard.press("Tab");
      const inside = await page.evaluate(() =>
        Boolean(
          document.activeElement?.closest('[data-testid="director-drawer"]'),
        ),
      );
      expect(inside).toBe(true);
    }
    await page.keyboard.press("Escape");
    await expect(drawer).toHaveCount(0);
    await expect(page.getByTestId("director-trigger")).toBeFocused();

    // Shape a task in place and confirm visibility on /next/task/<id> and classic /tasks/<id>
    await page.setViewportSize({ width: 1440, height: 900 });
    const shapeTarget = (
      await apiJson(demoUrl, code, "/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: "Rough task brief",
          description: "Initial notes",
          repo: "acme/widgets",
          approved: false,
        }),
      })
    ).body;
    const sessionsList = (await apiJson(demoUrl, code, "/api/console-sessions"))
      .body;
    const dirTaskId =
      sessionsList.sessions.find((s) => s.id === "director")?.taskId || "1";
    const shapedTitle = "Shaped in-place: bounded exponential retry";
    await apiJson(demoUrl, code, `/api/tasks/${dirTaskId}/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        summary: "Shaped draft",
        body:
          "```grain-draft\n" +
          JSON.stringify({
            title: shapedTitle,
            description: "Cap retries at 5 with jitter.",
            priority: 8,
          }) +
          "\n```",
      }),
    });

    await page.goto(`${demoUrl}/next/task/${shapeTarget.id}`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("act-edit").click();
    await page.getByTestId("edit-shape-director").click();
    await page.getByTestId("shaper-apply-save").click();
    await expect(page.locator("#task-title")).toContainText(shapedTitle);

    await page.goto(`${demoUrl}/tasks/${shapeTarget.id}`, {
      waitUntil: "domcontentloaded",
    });
    await expect(page.locator("body")).toContainText(shapedTitle);

    // 2. Sessions next-turn model change & 390px Artifact + Memory canvas without overflow
    await apiJson(demoUrl, code, `/api/tasks/${dirTaskId}/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        summary: "Canvas doc",
        body: "```markdown\n# Mobile Spec\n\nToken: `https://ghe.example.com/acme/widgets/pull/9999/files#diff-0123456789abcdef0123456789abcdef`\n```",
      }),
    });
    await apiJson(demoUrl, code, "/api/memory/documents", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        repo: "",
        path: "ops.md",
        content:
          "# Ops Memory\n\n`long_memory_identifier_without_spaces_0123456789abcdef0123456789`\n",
      }),
    });

    await page.goto(`${demoUrl}/next/sessions/director`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("chat-head-model").click();
    await page
      .getByTestId("chat-head-model-menu")
      .getByRole("option", { name: "gemini-2.5-flash" })
      .click();
    await page.getByTestId("composer").fill("Next turn model check.");
    await page.getByTestId("composer").press("Control+Enter");
    await expect
      .poll(
        async () =>
          (await apiJson(demoUrl, code, `/api/tasks/${dirTaskId}`)).body.model,
      )
      .toBe("gemini-2.5-flash");

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${demoUrl}/next/sessions/director`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("session-canvas-toggle").click();
    await page.getByTestId("canvas-tab-docs").click();
    const docsOverflow = await page.evaluate(() => {
      const cb = document.querySelector(".canvas-body");
      const cp = document.querySelector(".chat-pane");
      return {
        docW: document.documentElement.scrollWidth,
        cbScroll: cb ? cb.scrollWidth : 0,
        cbClient: cb ? cb.clientWidth : 0,
        chatW: cp ? Math.round(cp.getBoundingClientRect().width) : 0,
      };
    });
    expect(docsOverflow.docW).toBeLessThanOrEqual(390);
    expect(docsOverflow.cbScroll).toBeLessThanOrEqual(docsOverflow.cbClient);
    expect(docsOverflow.chatW).toBeGreaterThanOrEqual(370);

    await page.getByTestId("canvas-tab-memory").click();
    const memOverflow = await page.evaluate(() => {
      const cb = document.querySelector(".canvas-body");
      return {
        docW: document.documentElement.scrollWidth,
        cbScroll: cb ? cb.scrollWidth : 0,
        cbClient: cb ? cb.clientWidth : 0,
      };
    });
    expect(memOverflow.docW).toBeLessThanOrEqual(390);
    expect(memOverflow.cbScroll).toBeLessThanOrEqual(memOverflow.cbClient);

    // 3. Inbox diff preview: proposal with branch, huge truncated diff with link, and proposal without branch
    await page.setViewportSize({ width: 1440, height: 900 });
    const propHuge = (
      await apiJson(demoUrl, code, "/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: "Proposal twenty-file truncated diff",
          description: "Check truncation link.",
          repo: "acme/widgets",
          approved: false,
        }),
      })
    ).body;
    const propHugeDet = (
      await apiJson(demoUrl, code, `/api/tasks/${propHuge.id}`)
    ).body;
    const files20 = {};
    for (let i = 1; i <= 18; i++) {
      files20[`pkg/m${i}/x.go`] = `package m${i}\nconst N = ${i}\n`;
    }
    pushCommitToBareBranch(
      tmpDir,
      bareWidgets,
      propHugeDet.branch,
      files20,
      "feat: 18 files",
    );

    const propEmpty = (
      await apiJson(demoUrl, code, "/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: "Proposal unpushed branch",
          description: "No branch pushed.",
          repo: "acme/widgets",
          approved: false,
        }),
      })
    ).body;

    await page.goto(`${demoUrl}/next/inbox`, { waitUntil: "domcontentloaded" });
    await page
      .getByTestId("queue-row")
      .filter({ hasText: propHuge.title })
      .first()
      .click();
    await expect(page.getByTestId("inspector-diff")).toBeVisible();
    await expect(
      page.getByTestId("diff-truncated").locator("a[href]"),
    ).toBeVisible();

    await page
      .getByTestId("queue-row")
      .filter({ hasText: propEmpty.title })
      .first()
      .click();
    await sleep(300);
    await expect(page.getByTestId("inspector-diff")).toHaveCount(0);

    // 4. Conditional polling sends If-None-Match and receives 304 while keeping cached rows
    let count304 = 0;
    page.on("response", (resp) => {
      if (
        resp.url().includes("/api/tasks") &&
        resp.request().headers()["if-none-match"] &&
        resp.status() === 304
      ) {
        count304++;
      }
    });
    await page.goto(`${demoUrl}/next/tasks`, { waitUntil: "domcontentloaded" });
    await expect.poll(() => count304, { timeout: 12_000 }).toBeGreaterThan(0);
    await expect(page.locator('[data-testid^="open-"]').first()).toBeVisible();

    await ctx.close();
  } finally {
    demoProc.kill("SIGTERM");
    await sleep(300);
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
});
