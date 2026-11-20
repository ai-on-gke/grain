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

// walk_playwright.mjs executes §2 items 4 & 5 of docs/test-plans/2026-11-16.md
// against a live grain daemon seeded with ~5,000 tasks (265+ open):
//   - Logs /api/tasks, /api/tasks/summaries, and /api/tasks/{id} requests on
//     cold loads of /next/inbox, /next/agents, /next/tasks,
//     /next/tasks?group=workstream, and /next/workstreams (+ workstream detail).
//   - Injects a failing /api/tasks/summaries chunk via page.route (both cold
//     partial failure and warm refresh failure) and checks cached rows stay.
//   - Spot-checks classic / lists the same 265-task open set as /next/tasks.
//
// Usage:
//   PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//   node docs/test-runs/2026-11-16-task-list-cost/walk_playwright.mjs \
//     --url http://127.0.0.1:4290 --token <access-code> [--label postgres]

import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../../..");
const require = createRequire(path.join(repoRoot, "ui-ng/package.json"));
const { chromium } = require("@playwright/test");

function parseArgs(argv) {
  const out = { url: "http://127.0.0.1:4290", token: "", label: "daemon" };
  for (let i = 2; i < argv.length; i++) {
    if (argv[i] === "--url") out.url = argv[++i];
    else if (argv[i] === "--token") out.token = argv[++i];
    else if (argv[i] === "--label") out.label = argv[++i];
  }
  if (!out.token) throw new Error("--token is required");
  return out;
}

function classifyTaskReq(rawUrl) {
  const u = new URL(rawUrl);
  const p = u.pathname;
  if (p === "/api/tasks") {
    return { kind: "list", query: u.search };
  }
  if (p === "/api/tasks/summaries") {
    const ids = (u.searchParams.get("ids") || "").split(",").filter(Boolean);
    return {
      kind: "summaries",
      idCount: ids.length,
      ids,
      commentsLimit: u.searchParams.get("comments_limit"),
      attemptsLimit: u.searchParams.get("attempts_limit"),
    };
  }
  const m = p.match(/^\/api\/tasks\/([^/]+)$/);
  if (m) {
    return { kind: "single", id: m[1], query: u.search };
  }
  return null;
}

async function createAuthedContext(browser, baseURL, token) {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
  });
  const res = await context.request.post(`${baseURL}/api/auth/access-code`, {
    data: { code: token, name: "admin@example.com" },
  });
  if (!res.ok()) {
    throw new Error(
      `POST /api/auth/access-code failed: ${res.status()} ${await res.text()}`,
    );
  }
  return context;
}

function attachRecorder(page) {
  const records = [];
  const consoleErrors = [];
  const pageErrors = [];
  page.on("response", (resp) => {
    const req = resp.request();
    const info = classifyTaskReq(req.url());
    if (info) {
      records.push({
        method: req.method(),
        status: resp.status(),
        url: req.url(),
        ...info,
      });
    }
  });
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("pageerror", (err) => pageErrors.push(String(err)));
  return { records, consoleErrors, pageErrors };
}

function summarizeRecords(records) {
  const lists = records.filter((r) => r.kind === "list");
  const summaries = records.filter((r) => r.kind === "summaries");
  const singles = records.filter((r) => r.kind === "single");
  const distinctSingleIds = [...new Set(singles.map((r) => r.id))];
  return {
    listCount: lists.length,
    listQueries: lists.map((r) => `${r.query || "(open)"}:${r.status}`),
    summaryCount: summaries.length,
    summaryBatches: summaries.map(
      (r) =>
        `${r.idCount}ids(cl=${r.commentsLimit},al=${r.attemptsLimit}):${r.status}`,
    ),
    singleCount: singles.length,
    distinctSingleIds,
    singles: singles.map((r) => `${r.id}:${r.status}`),
  };
}

async function waitQuiet(page, ms = 2500) {
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(ms);
}

const RUNNING_TITLE_RE =
  /Running agent task \d+|Running PR repair \d+|Rolled-over task \d+/g;

async function main() {
  const args = parseArgs(process.argv);
  console.log(`=== Playwright Walkthrough Items 4 & 5 (${args.label}) ===`);

  const browser = await chromium.launch({ headless: true });
  try {
    // 1. Cold load: /next/inbox
    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/next/inbox`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2500);
      const queueRows = await page.getByTestId("queue-row").count();
      const sum = summarizeRecords(rec.records);
      console.log(
        `[cold /next/inbox] queueRows=${queueRows} summaryCalls=${sum.summaryCount} ${JSON.stringify(sum.summaryBatches)} singleCalls=${sum.singleCount} distinctSingleIds=${JSON.stringify(sum.distinctSingleIds)} listCalls=${sum.listCount} pageErrors=${rec.pageErrors.length}`,
      );
      if (queueRows < 20) {
        throw new Error(
          `/next/inbox expected >=20 queue rows, got ${queueRows}`,
        );
      }
      if (sum.summaryCount === 0 || sum.summaryCount > 4) {
        throw new Error(
          `/next/inbox unexpected summaryCalls=${sum.summaryCount}`,
        );
      }
      if (sum.distinctSingleIds.length > 1) {
        throw new Error(
          `/next/inbox per-task fan-out detected: distinctSingleIds=${JSON.stringify(sum.distinctSingleIds)}`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(`/next/inbox pageErrors: ${rec.pageErrors.join("; ")}`);
      }
      await ctx.close();
    }

    // 2. Cold load: /next/agents (65 running tasks -> 2 summary chunks: 50 + 15)
    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/next/agents`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2500);
      const bodyText = await page.locator("body").innerText();
      const runMatches = (bodyText.match(RUNNING_TITLE_RE) || []).length;
      const sum = summarizeRecords(rec.records);
      console.log(
        `[cold /next/agents] visibleRunningTitles=${runMatches} summaryCalls=${sum.summaryCount} ${JSON.stringify(sum.summaryBatches)} singleCalls=${sum.singleCount} listCalls=${sum.listCount} pageErrors=${rec.pageErrors.length}`,
      );
      if (runMatches < 65) {
        throw new Error(
          `/next/agents expected 65 running titles visible, got ${runMatches}`,
        );
      }
      if (sum.summaryCount < 2 || sum.summaryCount > 4) {
        throw new Error(
          `/next/agents expected 2 summary chunks (50+15), got ${sum.summaryCount}: ${JSON.stringify(sum.summaryBatches)}`,
        );
      }
      if (sum.singleCount !== 0) {
        throw new Error(
          `/next/agents per-task fan-out: singleCount=${sum.singleCount}`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(
          `/next/agents pageErrors: ${rec.pageErrors.join("; ")}`,
        );
      }
      await ctx.close();
    }

    // 3. Cold load: /next/tasks (65 awaiting_submit -> 2 summary chunks: 50 + 15)
    //    Also verify tab counts & collect open work task IDs polled by /next/tasks.
    let nextPolledWorkIds = [];
    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      page.on("response", async (resp) => {
        const u = new URL(resp.url());
        if (
          u.pathname === "/api/tasks" &&
          u.search === "" &&
          resp.status() === 200
        ) {
          try {
            const rows = await resp.json();
            nextPolledWorkIds = rows
              .filter((t) => !t.consoleSession && !t.interactive)
              .map((t) => String(t.id))
              .sort((a, b) => Number(a) - Number(b));
          } catch {}
        }
      });
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/next/tasks`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2500);
      const bodyText = await page.locator("body").innerText();
      const tabLinks = await page
        .locator("a[href*='/next/tasks?tab=']")
        .evaluateAll((els) =>
          els.map((e) => (e.textContent || "").replace(/\s+/g, " ").trim()),
        );
      const sum = summarizeRecords(rec.records);
      console.log(
        `[cold /next/tasks] polledWorkTasks=${nextPolledWorkIds.length} tabs=${JSON.stringify(tabLinks)} summaryCalls=${sum.summaryCount} ${JSON.stringify(sum.summaryBatches)} distinctSingleIds=${JSON.stringify(sum.distinctSingleIds)} listCalls=${sum.listCount} pageErrors=${rec.pageErrors.length}`,
      );
      if (nextPolledWorkIds.length < 260) {
        throw new Error(
          `/next/tasks expected >=260 polled open work tasks, got ${nextPolledWorkIds.length}`,
        );
      }
      if (sum.summaryCount < 2 || sum.summaryCount > 4) {
        throw new Error(
          `/next/tasks expected 2 summary chunks (50+15), got ${sum.summaryCount}: ${JSON.stringify(sum.summaryBatches)}`,
        );
      }
      if (sum.distinctSingleIds.length > 1) {
        throw new Error(
          `/next/tasks per-task fan-out: distinctSingleIds=${JSON.stringify(sum.distinctSingleIds)}`,
        );
      }
      // Spot-check Submit & land tab renders the 65 awaiting_submit rows cleanly
      await page
        .locator("a[href*='/next/tasks?tab=']", { hasText: "Submit & land" })
        .first()
        .click();
      await waitQuiet(page, 1500);
      const submitText = await page.locator("body").innerText();
      const submitMatches = (
        submitText.match(/Awaiting submit PR \d+/g) || []
      ).length;
      console.log(
        `[tab Submit & land (${page.url()})] visibleAwaitingSubmitTitles=${submitMatches}`,
      );
      if (submitMatches < 65) {
        throw new Error(
          `Submit & land tab expected 65 awaiting_submit titles, got ${submitMatches}`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(`/next/tasks pageErrors: ${rec.pageErrors.join("; ")}`);
      }
      await ctx.close();
    }

    // 4. Cold load: /next/tasks?group=workstream AND /next/workstreams (+ /next/workstreams/300)
    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/next/tasks?group=workstream`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2500);
      const sum = summarizeRecords(rec.records);
      const bodyText = await page.locator("body").innerText();
      const hasWsHeading = bodyText.includes("Group by") && bodyText.includes("Workstream");
      console.log(
        `[cold /next/tasks?group=workstream] hasWsGroup=${hasWsHeading} summaryCalls=${sum.summaryCount} ${JSON.stringify(sum.summaryBatches)} distinctSingleIds=${JSON.stringify(sum.distinctSingleIds)} listCalls=${sum.listCount} pageErrors=${rec.pageErrors.length}`,
      );
      if (!hasWsHeading) {
        throw new Error("/next/tasks?group=workstream did not activate Workstream grouping");
      }
      if (sum.distinctSingleIds.length > 1) {
        throw new Error(
          `/next/tasks?group=workstream per-task fan-out: ${JSON.stringify(sum.distinctSingleIds)}`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(
          `/next/tasks?group=workstream pageErrors: ${rec.pageErrors.join("; ")}`,
        );
      }
      await ctx.close();
    }

    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/next/workstreams`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2000);
      const wsListText = await page.locator("body").innerText();
      const streamsShown = (wsListText.match(/(\d+) streams shown/) || [])[1] || "0";
      // Open a specific workstream detail to trigger WorkstreamsStore.ensureRootDetail -> /api/tasks/summaries
      await page.goto(`${args.url}/next/workstreams/task:300`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2000);
      const sum = summarizeRecords(rec.records);
      const wsText = await page.locator("body").innerText();
      const hasRoot300 = wsText.includes("Workstream root 300");
      console.log(
        `[cold /next/workstreams + /task:300] streamsShown=${streamsShown} hasRoot300=${hasRoot300} summaryCalls=${sum.summaryCount} ${JSON.stringify(sum.summaryBatches)} distinctSingleIds=${JSON.stringify(sum.distinctSingleIds)} listCalls=${sum.listCount} pageErrors=${rec.pageErrors.length}`,
      );
      if (Number(streamsShown) < 60) {
        throw new Error(`/next/workstreams expected >=60 streams shown, got ${streamsShown}`);
      }
      if (!hasRoot300) {
        throw new Error("/next/workstreams/task:300 did not render Workstream root 300");
      }
      if (sum.summaryCount < 1) {
        throw new Error("/next/workstreams/task:300 did not call /api/tasks/summaries for root hydration");
      }
      if (sum.distinctSingleIds.length > 1) {
        throw new Error(
          `/next/workstreams per-task fan-out: ${JSON.stringify(sum.distinctSingleIds)}`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(
          `/next/workstreams pageErrors: ${rec.pageErrors.join("; ")}`,
        );
      }
      await ctx.close();
    }

    // 5. Failing chunk injection via page.route:
    //    5a. /next/agents cold load with 2nd /api/tasks/summaries chunk -> 500
    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      let seen = 0;
      let failed = 0;
      let ok = 0;
      await page.route("**/api/tasks/summaries*", async (route) => {
        seen++;
        if (seen === 2) {
          failed++;
          await route.fulfill({
            status: 500,
            contentType: "application/json",
            body: JSON.stringify({ error: "injected chunk 2 failure" }),
          });
          return;
        }
        ok++;
        await route.continue();
      });
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/next/agents`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2500);
      const bodyText = await page.locator("body").innerText();
      const titles = (bodyText.match(RUNNING_TITLE_RE) || []).length;
      console.log(
        `[fault /next/agents cold chunk-2 500] okChunks=${ok} failedChunks=${failed} visibleRunningTitles=${titles} pageErrors=${rec.pageErrors.length}`,
      );
      if (failed < 1 || ok < 1) {
        throw new Error(
          `/next/agents cold fault did not hit both ok (${ok}) and failed (${failed}) chunks`,
        );
      }
      if (titles < 65) {
        throw new Error(
          `/next/agents cold chunk-2 500 lost rows: ${titles} < 65`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(
          `/next/agents cold fault pageErrors: ${rec.pageErrors.join("; ")}`,
        );
      }
      await ctx.close();
    }

    //    5b. /next/agents warm load -> fail subsequent summaries on poll -> cached rows & details stay
    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      let failAll = false;
      let intercepted500 = 0;
      await page.route("**/api/tasks/summaries*", async (route) => {
        if (failAll) {
          intercepted500++;
          await route.fulfill({
            status: 500,
            contentType: "application/json",
            body: JSON.stringify({ error: "injected refresh chunk failure" }),
          });
          return;
        }
        await route.continue();
      });
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/next/agents`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2000);
      const beforeText = await page.locator("body").innerText();
      const beforeTitles = (beforeText.match(RUNNING_TITLE_RE) || []).length;
      const beforeActivity = (
        beforeText.match(/executing walkthrough check/gi) || []
      ).length;

      failAll = true;
      await page.evaluate(() => {
        window.dispatchEvent(new Event("focus"));
        document.dispatchEvent(new Event("visibilitychange"));
      });
      const deadline = Date.now() + 20000;
      while (intercepted500 === 0 && Date.now() < deadline) {
        await page.waitForTimeout(500);
      }
      await page.waitForTimeout(1000);
      const afterText = await page.locator("body").innerText();
      const afterTitles = (afterText.match(RUNNING_TITLE_RE) || []).length;
      const afterActivity = (
        afterText.match(/executing walkthrough check/gi) || []
      ).length;
      console.log(
        `[fault /next/agents warm poll 500] intercepted500=${intercepted500} titles=${beforeTitles}->${afterTitles} cachedActivityText=${beforeActivity}->${afterActivity} pageErrors=${rec.pageErrors.length}`,
      );
      if (intercepted500 === 0) {
        throw new Error(
          "/next/agents warm fault did not observe a subsequent summary poll within 20s",
        );
      }
      if (afterTitles < beforeTitles) {
        throw new Error(
          `/next/agents warm fault lost rows: ${beforeTitles} -> ${afterTitles}`,
        );
      }
      if (beforeActivity > 0 && afterActivity < beforeActivity) {
        throw new Error(
          `/next/agents warm fault lost cached summary details: ${beforeActivity} -> ${afterActivity}`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(
          `/next/agents warm fault pageErrors: ${rec.pageErrors.join("; ")}`,
        );
      }
      await ctx.close();
    }

    //    5c. /next/tasks?tab=submit: cold load with chunk 2 -> 500 AND warm load then chunk failure
    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      let seen = 0;
      let ok = 0;
      let failed = 0;
      await page.route("**/api/tasks/summaries*", async (route) => {
        seen++;
        if (seen === 2) {
          failed++;
          await route.fulfill({
            status: 500,
            contentType: "application/json",
            body: JSON.stringify({ error: "injected tasks chunk 2 failure" }),
          });
          return;
        }
        ok++;
        await route.continue();
      });
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/next/tasks`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2500);
      await page
        .locator("a[href*='/next/tasks?tab=']", { hasText: "Submit & land" })
        .first()
        .click();
      await waitQuiet(page, 1000);
      const bodyText = await page.locator("body").innerText();
      const submitMatches = (
        bodyText.match(/Awaiting submit PR \d+/g) || []
      ).length;
      console.log(
        `[fault /next/tasks Submit & land cold chunk-2 500] okChunks=${ok} failedChunks=${failed} visibleAwaitingSubmitTitles=${submitMatches} pageErrors=${rec.pageErrors.length}`,
      );
      if (failed < 1 || ok < 1) {
        throw new Error(
          `/next/tasks cold fault did not hit both ok (${ok}) and failed (${failed}) chunks`,
        );
      }
      if (submitMatches < 65) {
        throw new Error(
          `/next/tasks cold chunk-2 500 lost rows: ${submitMatches}`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(
          `/next/tasks cold fault pageErrors: ${rec.pageErrors.join("; ")}`,
        );
      }
      await ctx.close();
    }

    //    5d. /next/inbox warm load -> fail summaries -> cached queue rows stay
    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      let failAll = false;
      let intercepted500 = 0;
      await page.route("**/api/tasks/summaries*", async (route) => {
        if (failAll) {
          intercepted500++;
          await route.fulfill({
            status: 500,
            contentType: "application/json",
            body: JSON.stringify({ error: "injected inbox summaries failure" }),
          });
          return;
        }
        await route.continue();
      });
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/next/inbox`, {
        waitUntil: "domcontentloaded",
      });
      await waitQuiet(page, 2000);
      const beforeRows = await page.getByTestId("queue-row").count();
      failAll = true;
      await page.evaluate(() => {
        window.dispatchEvent(new Event("focus"));
        document.dispatchEvent(new Event("visibilitychange"));
      });
      await page.waitForTimeout(3000);
      const afterRows = await page.getByTestId("queue-row").count();
      console.log(
        `[fault /next/inbox warm summaries 500] intercepted500=${intercepted500} queueRows=${beforeRows}->${afterRows} pageErrors=${rec.pageErrors.length}`,
      );
      if (afterRows < beforeRows) {
        throw new Error(
          `/next/inbox warm fault lost rows: ${beforeRows} -> ${afterRows}`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(
          `/next/inbox warm fault pageErrors: ${rec.pageErrors.join("; ")}`,
        );
      }
      await ctx.close();
    }

    // 6. Item 5: Classic / spot-check against /next/tasks open set
    {
      const ctx = await createAuthedContext(browser, args.url, args.token);
      const page = await ctx.newPage();
      let classicPolledWorkIds = [];
      page.on("response", async (resp) => {
        const u = new URL(resp.url());
        if (
          u.pathname === "/api/tasks" &&
          u.search === "" &&
          resp.status() === 200
        ) {
          try {
            const rows = await resp.json();
            classicPolledWorkIds = rows
              .filter((t) => !t.consoleSession && !t.interactive)
              .map((t) => String(t.id))
              .sort((a, b) => Number(a) - Number(b));
          } catch {}
        }
      });
      const rec = attachRecorder(page);
      await page.goto(`${args.url}/`, { waitUntil: "domcontentloaded" });
      await waitQuiet(page, 2500);
      const classicRenderedIds = await page.evaluate(() => {
        const ids = new Set();
        document.querySelectorAll("a[href^='/tasks/']").forEach((el) => {
          const m = (el.getAttribute("href") || "").match(/^\/tasks\/(\d+)$/);
          if (m) ids.add(m[1]);
        });
        return [...ids].sort((a, b) => Number(a) - Number(b));
      });
      const renderedSet = new Set(classicRenderedIds);
      const missingOpenInClassicDOM = nextPolledWorkIds.filter(
        (id) => !renderedSet.has(id),
      );
      const polledMismatch =
        classicPolledWorkIds.length !== nextPolledWorkIds.length ||
        classicPolledWorkIds.some((id, i) => id !== nextPolledWorkIds[i]);
      const sum = summarizeRecords(rec.records);
      console.log(
        `[classic /] polledOpenWork=${classicPolledWorkIds.length} nextOpenWork=${nextPolledWorkIds.length} classicDOMLinks=${classicRenderedIds.length} missingOpenInClassicDOM=${missingOpenInClassicDOM.length} listCalls=${sum.listCount} pageErrors=${rec.pageErrors.length}`,
      );
      if (polledMismatch) {
        throw new Error(
          `classic / polled open work set (${classicPolledWorkIds.length}) != /next/tasks (${nextPolledWorkIds.length})`,
        );
      }
      if (missingOpenInClassicDOM.length > 0) {
        throw new Error(
          `classic / DOM missed ${missingOpenInClassicDOM.length} open task(s): ${missingOpenInClassicDOM.slice(0, 10).join(",")}`,
        );
      }
      if (rec.pageErrors.length > 0) {
        throw new Error(`classic / pageErrors: ${rec.pageErrors.join("; ")}`);
      }
      await ctx.close();
    }

    console.log(
      `PASS: Playwright walkthrough items 4 & 5 succeeded on ${args.label}`,
    );
  } finally {
    await browser.close();
  }
}

main().catch((err) => {
  console.error("FAIL:", err);
  process.exit(1);
});
