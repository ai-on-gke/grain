#!/usr/bin/env node
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

// Walkthrough driver for docs/test-plans/2026-11-13.md §1 items 3, 4, 7, 8, and 9
// over a real `grain demo -github-sim -fake-ci` server on 127.0.0.1:4293.

import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";

import { signIn } from "../../../ui-shared/e2e/accesscode.js";

const requireNg = createRequire(
  new URL("../../../ui-ng/package.json", import.meta.url),
);
const { chromium } = requireNg("playwright-core");

const pollArg = process.argv.find((a) => a.startsWith("--poll-seconds="));
const POLL_SECONDS = pollArg ? Number(pollArg.split("=")[1]) : 60;
const DEMO_PORT = Number(process.env.DEMO_PORT || 4293);
const DEMO_URL = `http://127.0.0.1:${DEMO_PORT}`;
const GRAIN_BIN = process.env.GRAIN || "/home/debian/bin/grain";
const DEMO_TMP = "/tmp/walk-next-demo";

const results = [];
function record(id, ok, detail) {
  results.push({ id, ok: Boolean(ok), detail });
  console.log(`[${ok ? "PASS" : "FAIL"}] ${id}: ${detail}`);
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

async function apiJson(code, urlPath, opts = {}) {
  const headers = {
    Authorization: `Bearer ${code}`,
    ...(opts.headers || {}),
  };
  const res = await fetch(`${DEMO_URL}${urlPath}`, { ...opts, headers });
  const text = await res.text();
  let body = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    body = text;
  }
  return { status: res.status, body };
}

async function openSignedInContext(
  browser,
  code,
  viewport = { width: 1440, height: 900 },
  colorScheme = "light",
) {
  const storageState = await signIn(DEMO_URL, code, "alice@example.com");
  const context = await browser.newContext({
    viewport,
    colorScheme,
    storageState,
  });
  const page = await context.newPage();
  return { context, page };
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

function pushCommitToBareBranch(bareRepo, branch, filesMap, message) {
  const work = fs.mkdtempSync(path.join(DEMO_TMP, "gitpush-"));
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

fs.rmSync(DEMO_TMP, { recursive: true, force: true });
fs.mkdirSync(DEMO_TMP, { recursive: true });
const codeFile = path.join(DEMO_TMP, "access-code");
const logFd = fs.openSync(path.join(DEMO_TMP, "demo.log"), "w");

const demoProc = spawn(
  GRAIN_BIN,
  [
    "demo",
    "-github-sim",
    "-fake-ci",
    "-human-review",
    "-reconcile",
    "2s",
    "-addr",
    `127.0.0.1:${DEMO_PORT}`,
    "-access-code-file",
    codeFile,
  ],
  {
    env: { ...process.env, TMPDIR: DEMO_TMP },
    stdio: ["ignore", logFd, logFd],
  },
);

let browser;
try {
  // Wait for grain demo to start and write access code
  let code = "";
  for (let i = 0; i < 120; i++) {
    if (fs.existsSync(codeFile)) {
      code = fs.readFileSync(codeFile, "utf8").trim();
      if (code) {
        try {
          const r = await apiJson(code, "/api/config");
          if (r.status === 200) break;
        } catch {
          // retry
        }
      }
    }
    await sleep(200);
  }
  if (!code) throw new Error("grain demo did not start in time");

  const bareWidgets = findDemoBareRepo(DEMO_TMP);
  browser = await chromium.launch({ headless: true });

  // =========================================================================
  // ITEM 3: Director drawer (click & shortcut on pages, keeps context, shapes task in place, Escape + focus trap/return)
  // =========================================================================
  {
    const { context, page } = await openSignedInContext(browser, code);

    // 3a. Opens from pages by click & shortcut, keeps open & updates context when navigating
    await page.goto(`${DEMO_URL}/next/inbox`, { waitUntil: "domcontentloaded" });
    await page.getByTestId("director-trigger").click();
    const drawer = page.getByTestId("director-drawer");
    await drawer.waitFor({ state: "visible" });
    const openOnInbox = await drawer.isVisible();

    // Navigate via rail to Tasks, Processes, CI, Settings while drawer stays open
    const ctxSamples = [];
    for (const navRoute of ["/next/tasks", "/next/processes", "/next/ci", "/next/settings"]) {
      await page.evaluate((u) => {
        window.history.pushState({}, "", u);
        window.dispatchEvent(new PopStateEvent("popstate"));
      }, navRoute);
      await sleep(250);
    }
    await page.goto(`${DEMO_URL}/next/tasks`, { waitUntil: "domcontentloaded" });
    await page.getByTestId("director-trigger").focus();
    await page.keyboard.press("d");
    await drawer.waitFor({ state: "visible" });
    const rows = page.locator('[data-testid^="open-"]');
    await rows.first().click();
    const ctxTasks =
      (await page.getByTestId("director-context-toggle").textContent())?.trim() || "";
    ctxSamples.push(ctxTasks);

    // Navigate via rail link (SPA navigation) to CI -> drawer stays open & updates context
    await page.locator('a[href="/next/ci"]').first().click();
    await page.waitForURL("**/next/ci**");
    const stillOpenOnCi = await drawer.isVisible();

    // Escape closes and returns focus to opener (director-trigger)
    await page.keyboard.press("Escape");
    await sleep(100);
    const closedAfterEsc = (await drawer.count()) === 0;
    const focusReturnedToTrigger = await page.evaluate(
      () => document.activeElement?.getAttribute("data-testid") === "director-trigger",
    );

    // Shortcut Alt+D from view-filter opens and Escape restores focus
    const filterInput = page.getByTestId("view-filter");
    await filterInput.focus();
    await page.keyboard.press("Alt+d");
    await drawer.waitFor({ state: "visible" });
    await page.keyboard.press("Escape");
    await sleep(100);
    const closedAfterAltDEsc = (await drawer.count()) === 0;

    // Focus trap inside compact modal Director (< 1080px, e.g. 390x844)
    await page.setViewportSize({ width: 390, height: 844 });
    await page.getByTestId("director-trigger").click();
    await drawer.waitFor({ state: "visible" });
    let stayedInDrawer = true;
    for (let i = 0; i < 8; i++) {
      await page.keyboard.press("Tab");
      const inside = await page.evaluate(() =>
        Boolean(document.activeElement?.closest('[data-testid="director-drawer"]')),
      );
      if (!inside) stayedInDrawer = false;
    }
    await page.keyboard.press("Escape");
    await sleep(100);
    const compactFocusReturned = await page.evaluate(
      () => document.activeElement?.getAttribute("data-testid") === "director-trigger",
    );

    record(
      "item3.drawer_open_navigate_escape_focus",
      openOnInbox &&
        ctxTasks.includes("Task #") &&
        stillOpenOnCi &&
        closedAfterEsc &&
        focusReturnedToTrigger &&
        closedAfterAltDEsc &&
        stayedInDrawer &&
        compactFocusReturned,
      `openOnInbox=${openOnInbox}, ctxTasks="${ctxTasks}", stillOpenOnCi=${stillOpenOnCi}, closedAfterEsc=${closedAfterEsc}, focusReturned=${focusReturnedToTrigger}, compactTrap=${stayedInDrawer}, compactFocusReturned=${compactFocusReturned}`,
    );

    // 3b. Shapes a task in place -> written to task & visible on /next/task/<id> and classic /tasks/<id>
    await page.setViewportSize({ width: 1440, height: 900 });
    const seededTask = (
      await apiJson(code, "/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: "Unshaped draft task",
          description: "Initial rough notes.",
          repo: "acme/widgets",
          approved: false,
        }),
      })
    ).body;

    const sessionsList = (await apiJson(code, "/api/console-sessions")).body;
    const directorSession = (sessionsList?.sessions || []).find(
      (s) => s.id === "director",
    );
    const directorTaskId = directorSession?.taskId || "1";

    // Post structured grain-draft turn to Director's backing task (simulating LLM shaping output)
    const shapedTitle = "Shaped: deterministic retry backoff in widgets";
    const shapedDesc = "Acceptance criteria: cap retries at 5 and add jitter.";
    await apiJson(code, `/api/tasks/${directorTaskId}/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        summary: "Shaped task draft",
        body:
          "Here is the tightened scope:\n\n```grain-draft\n" +
          JSON.stringify({
            title: shapedTitle,
            description: shapedDesc,
            priority: 8,
          }) +
          "\n```",
      }),
    });

    await page.goto(`${DEMO_URL}/next/task/${seededTask.id}`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("act-edit").click();
    await page.getByTestId("task-edit-panel").waitFor({ state: "visible" });
    await page.getByTestId("edit-shape-director").click();
    await page.getByTestId("shaper-diff-card").waitFor({ state: "visible" });
    await page.getByTestId("shaper-apply-save").click();
    await sleep(400);

    const updatedApi = (await apiJson(code, `/api/tasks/${seededTask.id}`)).body;
    const nextPageTitle =
      (await page.locator("#task-title").textContent())?.trim() || "";

    await page.goto(`${DEMO_URL}/tasks/${seededTask.id}`, {
      waitUntil: "domcontentloaded",
    });
    await sleep(600);
    const classicTaskText = await page.locator("body").innerText();

    record(
      "item3.shape_task_in_place_and_classic_visible",
      updatedApi?.title === shapedTitle &&
        updatedApi?.description === shapedDesc &&
        nextPageTitle.includes(shapedTitle) &&
        classicTaskText.includes(shapedTitle),
      `api.title="${updatedApi?.title}", nextVisible=${nextPageTitle.includes(shapedTitle)}, classicVisible=${classicTaskText.includes(shapedTitle)}`,
    );

    await context.close();
  }

  // =========================================================================
  // ITEM 4: Sessions canvas and model picker (configured models, next-turn model, 390px artifact & memory canvas)
  // =========================================================================
  {
    await apiJson(code, "/api/deployment/agent-backends/primary-agy", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        framework: "antigravity",
        models: ["claude-sonnet-4-6", "gemini-2.5-pro"],
      }),
    });
    const cfgBefore = (await apiJson(code, "/api/config")).body;
    const configuredModels = cfgBefore?.models || [];
    const sessionsList = (await apiJson(code, "/api/console-sessions")).body;
    const directorSession = (sessionsList?.sessions || []).find(
      (s) => s.id === "director",
    );
    const directorTaskId = directorSession?.taskId || "1";

    // Seed an artifact message with long unbroken code/tokens + HTML artifact AND a memory document
    await apiJson(code, `/api/tasks/${directorTaskId}/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        summary: "Design doc and HTML mock",
        body:
          "```markdown\n# Mobile Canvas Architecture\n\nLong identifier check: `https://ghe.example.com/acme/widgets/pull/9999/files#diff-0123456789abcdef0123456789abcdef0123456789abcdef`\n\n```ts\nexport const VERY_LONG_CONFIGURATION_CONSTANT_NAME_FOR_MOBILE_OVERFLOW_TEST = 'ok';\n```\n```\n\n```html\n<title>Mock Preview</title><section><h1>Preview</h1></section>\n```",
      }),
    });
    await apiJson(code, "/api/memory/documents", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        repo: "",
        path: "runbook.md",
        content:
          "# Standing Runbook\n\nVerify `grain_config_extremely_long_token_without_spaces_0123456789abcdef` wraps cleanly at 390px.\n",
      }),
    });

    const { context, page } = await openSignedInContext(browser, code);
    await page.goto(`${DEMO_URL}/next/sessions/director`, {
      waitUntil: "domcontentloaded",
    });
    await page.waitForSelector('[data-testid="chat-head-model"]');

    // Check model picker options match configuredModels (plus Deployment default)
    const modelBtn = page.getByTestId("chat-head-model");
    await modelBtn.click();
    const modelMenu = page.getByTestId("chat-head-model-menu");
    await modelMenu.waitFor({ state: "visible" });
    const pickerOptions = await modelMenu
      .locator('[role="option"]')
      .evaluateAll((els) => els.map((e) => (e.textContent || "").trim()));
    const targetModel =
      configuredModels.find((m) => m !== directorSession?.model) ||
      configuredModels[0];
    const optIdx = pickerOptions.findIndex(
      (o) => !o.startsWith("Deployment default") && o.includes(targetModel),
    );
    if (optIdx >= 0) {
      await modelMenu.locator('[role="option"]').nth(optIdx).click();
    } else {
      await page.keyboard.press("Escape");
    }
    await sleep(400);

    // Send next turn message and verify session + backing task hold targetModel
    const composer = page.getByTestId("composer");
    await composer.fill("Verify model on next turn.");
    await composer.press("Control+Enter");
    await sleep(400);

    const sessionsAfter = (await apiJson(code, "/api/console-sessions")).body;
    const dirAfter = (sessionsAfter?.sessions || []).find(
      (s) => s.id === "director",
    );
    const dirTaskAfter = (await apiJson(code, `/api/tasks/${directorTaskId}`)).body;

    record(
      "item4.demo_model_picker_and_next_turn",
      pickerOptions.length === configuredModels.length + 1 &&
        dirAfter?.model === targetModel &&
        dirTaskAfter?.model === targetModel,
      `configured=${JSON.stringify(configuredModels)}, picker=${JSON.stringify(pickerOptions)}, session.model=${dirAfter?.model}, task.model=${dirTaskAfter?.model}`,
    );

    // 390px viewport check: open Artifact doc AND Memory doc in canvas -> zero overflow & no 260px chat squeeze
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(`${DEMO_URL}/next/sessions/director`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("session-canvas-toggle").click();
    const canvas = page.getByTestId("session-canvas");
    await canvas.waitFor({ state: "visible" });

    // Open Docs artifact tab
    await page.getByTestId("canvas-tab-docs").click();
    await sleep(200);
    const docGeom = await page.evaluate(() => {
      const cb = document.querySelector(".canvas-body");
      const cp = document.querySelector(".chat-pane");
      const cv = document.querySelector('[data-testid="session-canvas"]');
      return {
        docScrollW: document.documentElement.scrollWidth,
        canvasBodyScrollW: cb ? cb.scrollWidth : 0,
        canvasBodyClientW: cb ? cb.clientWidth : 0,
        canvasRectW: cv ? Math.round(cv.getBoundingClientRect().width) : 0,
        chatPaneW: cp ? Math.round(cp.getBoundingClientRect().width) : 0,
      };
    });

    // Open Memory document tab
    await page.getByTestId("canvas-tab-memory").click();
    await sleep(250);
    const memGeom = await page.evaluate(() => {
      const cb = document.querySelector(".canvas-body");
      return {
        docScrollW: document.documentElement.scrollWidth,
        canvasBodyScrollW: cb ? cb.scrollWidth : 0,
        canvasBodyClientW: cb ? cb.clientWidth : 0,
      };
    });

    // Check raw hex free background on canvas HTML iframe rule
    await page.getByTestId("canvas-tab-html").click();
    await sleep(150);

    const noDocOverflow =
      docGeom.docScrollW <= 390 &&
      docGeom.canvasBodyScrollW <= docGeom.canvasBodyClientW &&
      docGeom.canvasRectW <= 390 &&
      docGeom.chatPaneW >= 370;
    const noMemOverflow =
      memGeom.docScrollW <= 390 &&
      memGeom.canvasBodyScrollW <= memGeom.canvasBodyClientW;

    record(
      "item4.canvas_390px_artifact_and_memory_no_overflow",
      noDocOverflow && noMemOverflow,
      `docGeom=${JSON.stringify(docGeom)}, memGeom=${JSON.stringify(memGeom)}`,
    );

    await context.close();
  }

  // =========================================================================
  // ITEM 7: Inbox diff preview (Submit decision, proposal with branch, huge diff truncated with link, task with no branch)
  // =========================================================================
  {
    // 1. Ensure seeded submit task's branch exists with a commit in bareWidgets
    const allTasks = (await apiJson(code, "/api/tasks")).body || [];
    const submitTask = allTasks.find((t) => t.state === "awaiting_submit" && t.repo === "acme/widgets");
    if (submitTask) {
      const detail = (await apiJson(code, `/api/tasks/${submitTask.id}`)).body;
      const br = detail?.branch || submitTask.branch;
      if (br) {
        pushCommitToBareBranch(
          bareWidgets,
          br,
          { "src/submit_fix.go": "package widgets\n\nconst Fixed = true\n" },
          "fix: seeded submit diff",
        );
      }
    }

    // 2. Proposal with a pushed branch
    const propWithBranch = (
      await apiJson(code, "/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: "Proposal with pushed branch diff",
          description: "Inspect my diff in Inbox.",
          repo: "acme/widgets",
          approved: false,
        }),
      })
    ).body;
    const propBranchDetail = (
      await apiJson(code, `/api/tasks/${propWithBranch.id}`)
    ).body;
    pushCommitToBareBranch(
      bareWidgets,
      propBranchDetail.branch,
      { "src/proposal_feature.go": "package widgets\n\nfunc ProposalFeature() string { return \"ok\" }\n" },
      "feat: proposal branch commit",
    );

    // 3. Proposal with a HUGE diff (20 files -> totalHunks = 20 > DefaultDiffPreviewHunks 12 -> truncated = true)
    const propHuge = (
      await apiJson(code, "/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: "Proposal with huge truncated diff",
          description: "20 changed files.",
          repo: "acme/widgets",
          approved: false,
        }),
      })
    ).body;
    const propHugeDetail = (await apiJson(code, `/api/tasks/${propHuge.id}`)).body;
    const twentyFiles = {};
    for (let i = 1; i <= 20; i++) {
      twentyFiles[`pkg/mod${String(i).padStart(2, "0")}/file.go`] =
        `package mod${i}\n\nconst V = ${i}\n`;
    }
    pushCommitToBareBranch(
      bareWidgets,
      propHugeDetail.branch,
      twentyFiles,
      "feat: huge 20-file change",
    );

    // 4. Proposal with NO pushed branch
    const propNoBranch = (
      await apiJson(code, "/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: "Proposal without any pushed branch",
          description: "No git branch pushed yet.",
          repo: "acme/widgets",
          approved: false,
        }),
      })
    ).body;

    const { context, page } = await openSignedInContext(browser, code);
    await page.goto(`${DEMO_URL}/next/inbox`, { waitUntil: "domcontentloaded" });
    await page.waitForSelector('[data-testid="queue-row"]');
    await sleep(350);

    // Select Submit decision
    let submitShowsDiff = false;
    if (submitTask) {
      const row = page
        .getByTestId("queue-row")
        .filter({ hasText: submitTask.title })
        .first();
      if ((await row.count()) > 0) {
        await row.click();
        await sleep(450);
        submitShowsDiff = (await page.getByTestId("inspector-diff").count()) > 0;
      }
    }

    // Select Proposal with branch
    await page
      .getByTestId("queue-row")
      .filter({ hasText: "Proposal with pushed branch diff" })
      .first()
      .click();
    await sleep(450);
    const proposalShowsDiff =
      (await page.getByTestId("inspector-diff").count()) > 0 &&
      ((await page.getByTestId("inspector-diff").textContent()) || "").includes(
        "proposal_feature.go",
      );

    // Select Proposal with huge diff -> truncated note + link!
    await page
      .getByTestId("queue-row")
      .filter({ hasText: "Proposal with huge truncated diff" })
      .first()
      .click();
    await sleep(450);
    const truncEl = page.getByTestId("diff-truncated");
    const hugeIsTruncated = (await truncEl.count()) > 0;
    const hugeHasLink =
      hugeIsTruncated && (await truncEl.locator("a[href]").count()) > 0;

    // Select Proposal with NO branch -> no empty diff pane!
    await page
      .getByTestId("queue-row")
      .filter({ hasText: "Proposal without any pushed branch" })
      .first()
      .click();
    await sleep(450);
    const noBranchHasNoPane =
      (await page.getByTestId("inspector-diff").count()) === 0;

    record(
      "item7.inbox_diff_preview_matrix",
      submitShowsDiff && proposalShowsDiff && hugeIsTruncated && hugeHasLink && noBranchHasNoPane,
      `submitShowsDiff=${submitShowsDiff}, proposalShowsDiff=${proposalShowsDiff}, hugeIsTruncated=${hugeIsTruncated}, hugeHasLink=${hugeHasLink}, noBranchHasNoPane=${noBranchHasNoPane}`,
    );

    await context.close();
  }

  // =========================================================================
  // ITEM 8: Conditional polling (If-None-Match & 304 over POLL_SECONDS on Tasks & Inbox vs classic)
  // =========================================================================
  {
    async function measurePagePolling(urlPath) {
      const { context, page } = await openSignedInContext(browser, code);
      const stats = {
        path: urlPath,
        apiGetCount: 0,
        ifNoneMatchCount: 0,
        status304Count: 0,
        status200Count: 0,
        totalBytes: 0,
      };
      // Navigate and let initial load settle before starting the 60s idle poll window
      await page.goto(`${DEMO_URL}${urlPath}`, { waitUntil: "domcontentloaded" });
      await sleep(1000);

      page.on("response", async (resp) => {
        const u = resp.url();
        if (!u.includes("/api/") || u.includes("/api/events")) return;
        if (resp.request().method() !== "GET") return;
        stats.apiGetCount++;
        const reqHeaders = resp.request().headers();
        if (reqHeaders["if-none-match"]) stats.ifNoneMatchCount++;
        if (resp.status() === 304) {
          stats.status304Count++;
        } else if (resp.status() === 200) {
          stats.status200Count++;
          try {
            const buf = await resp.body();
            stats.totalBytes += buf.length;
          } catch {
            // ignore closed body
          }
        }
      });

      await sleep(POLL_SECONDS * 1000);
      const rowTextLen = (await page.locator("body").innerText()).length;
      await context.close();
      return { ...stats, rowTextLen };
    }

    console.log(`Measuring conditional polling for ${POLL_SECONDS}s across 4 views in parallel...`);
    const [nextTasks, classicTasks, nextInbox, classicInbox] = await Promise.all([
      measurePagePolling("/next/tasks"),
      measurePagePolling("/tasks"),
      measurePagePolling("/next/inbox"),
      measurePagePolling("/inbox"),
    ]);

    record(
      "item8.conditional_polling_304_and_cached_body",
      nextTasks.ifNoneMatchCount > 0 &&
        nextTasks.status304Count > 0 &&
        nextInbox.ifNoneMatchCount > 0 &&
        nextInbox.status304Count > 0 &&
        nextTasks.rowTextLen > 100 &&
        nextInbox.rowTextLen > 100,
      `window=${POLL_SECONDS}s | /next/tasks=${JSON.stringify(nextTasks)} | /tasks=${JSON.stringify(classicTasks)} | /next/inbox=${JSON.stringify(nextInbox)} | /inbox=${JSON.stringify(classicInbox)}`,
    );
  }

  // =========================================================================
  // ITEM 9: Visual & Design-Book pass (every rail page at 1440x900 & 390x844, light & dark)
  // =========================================================================
  {
    const railPages = [
      "/next/inbox",
      "/next/workstreams",
      "/next/tasks",
      "/next/agents",
      "/next/sessions",
      "/next/ci",
      "/next/processes",
      "/next/telemetry",
      "/next/context",
      "/next/fleet",
      "/next/settings",
    ];
    const violations = [];
    for (const vp of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      const { context: lightCtx, page: lightPage } = await openSignedInContext(
        browser,
        code,
        vp,
        "light",
      );
      const { context: darkCtx, page: darkPage } = await openSignedInContext(
        browser,
        code,
        vp,
        "dark",
      );
      await darkPage.addInitScript(() => {
        try {
          localStorage.setItem("grain.theme", "dark");
        } catch {}
      });
      await lightPage.addInitScript(() => {
        try {
          localStorage.setItem("grain.theme", "light");
        } catch {}
      });

      for (const route of railPages) {
        await lightPage.goto(`${DEMO_URL}${route}`, { waitUntil: "domcontentloaded" });
        await lightPage.waitForSelector(".main-stage > *:not(router-outlet)");
        await darkPage.goto(`${DEMO_URL}${route}`, { waitUntil: "domcontentloaded" });
        await darkPage.waitForSelector(".main-stage > *:not(router-outlet)");
        await sleep(300);

        const lightAudit = await lightPage.evaluate(() => ({
          scrollW: document.documentElement.scrollWidth,
          innerW: window.innerWidth,
          h1Count: document.querySelectorAll("h1").length,
          nativeSelects: document.querySelectorAll("select").length,
          bg: getComputedStyle(document.body).backgroundColor,
        }));
        const darkAudit = await darkPage.evaluate(() => ({
          scrollW: document.documentElement.scrollWidth,
          innerW: window.innerWidth,
          h1Count: document.querySelectorAll("h1").length,
          nativeSelects: document.querySelectorAll("select").length,
          bg: getComputedStyle(document.body).backgroundColor,
        }));

        if (lightAudit.scrollW > lightAudit.innerW) {
          violations.push(`${route}@${vp.width}px light overflow (${lightAudit.scrollW}>${lightAudit.innerW})`);
        }
        if (darkAudit.scrollW > darkAudit.innerW) {
          violations.push(`${route}@${vp.width}px dark overflow (${darkAudit.scrollW}>${darkAudit.innerW})`);
        }
        if (lightAudit.h1Count !== 1) {
          violations.push(`${route}@${vp.width}px h1Count=${lightAudit.h1Count} (want 1)`);
        }
        if (lightAudit.nativeSelects > 0 || darkAudit.nativeSelects > 0) {
          violations.push(`${route}@${vp.width}px nativeSelects=${lightAudit.nativeSelects}`);
        }
        if (lightAudit.bg === darkAudit.bg) {
          violations.push(`${route}@${vp.width}px light/dark bg identical (${lightAudit.bg})`);
        }
      }
      await lightCtx.close();
      await darkCtx.close();
    }

    record(
      "item9.visual_design_book_invariants",
      violations.length === 0,
      violations.length === 0
        ? `all ${railPages.length} rail destinations clean at 1440x900 & 390x844 in light & dark`
        : `violations (${violations.length}): ${violations.join("; ")}`,
    );
  }
} finally {
  if (browser) await browser.close();
  demoProc.kill("SIGTERM");
  fs.closeSync(logFd);
  fs.rmSync(DEMO_TMP, { recursive: true, force: true });
}

const failed = results.filter((r) => !r.ok);
console.log(
  `\nSummary: ${results.length - failed.length}/${results.length} checks passed.`,
);
if (failed.length > 0) {
  process.exit(1);
}
