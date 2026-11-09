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

// 2026-11-07 §10 item 1 (#820): server-side closed-task search and
// scroll-triggered paging, over a server seeded by seed.mjs (~3,000
// closed + ~40 live). Usage:
//   GRAIN_SERVER=http://127.0.0.1:<port> GRAIN_TOKEN=... node search-scroll-walk.mjs [label]
// Writes search-scroll-<label>.json and screenshots beside itself.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const label = process.argv[2] || "run";
const only = process.env.ONLY || "";
const out = new URL(".", import.meta.url).pathname;
const result = { label, steps: [] };
const step = (name, data) => {
  result.steps.push({ name, ...data });
  console.log(name, JSON.stringify(data));
};
const api = async (method, path, body) => {
  const res = await fetch(S + path, {
    method,
    headers: { Authorization: `Bearer ${T}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: res.status, body: await res.json().catch(() => null) };
};
const seedNo = (s) => {
  const m = /seed (\d{4})/.exec(s || "");
  return m ? Number(m[1]) : null;
};

const browser = await chromium.launch();
const boardIds = [];

async function context(viewport, colorScheme = "light") {
  const ctx = await browser.newContext({ baseURL: S, viewport, colorScheme });
  await ctx.request.post("/api/auth/access-code", { data: { code: T } });
  return ctx;
}

// track records every closed-page and search request a page makes.
function track(page) {
  const reqs = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname !== "/api/tasks" || u.searchParams.get("state") !== "closed") return;
    reqs.push({
      at: Date.now(),
      cursor: u.searchParams.get("cursor") || "",
      search: u.searchParams.get("search") || "",
      limit: u.searchParams.get("limit"),
    });
  });
  return reqs;
}

const searchBox = (page) =>
  page.locator('input[type="search"], input[placeholder*="Search" i]').first();

async function rowsWith(page, sel, text) {
  return page.locator(sel, { hasText: text }).count();
}

async function searchWalk(vp) {
  const ctx = await context(vp);
  const page = await ctx.newPage();
  const reqs = track(page);
  await page.goto("/?f=state:any:closed");
  await page.locator(".task-row").first().waitFor({ timeout: 30000 });
  await page.waitForTimeout(1500);
  const baseRows = await page.locator(".task-row").count();
  const tag = `${vp.width}`;
  const deep = (await api("GET", "/api/tasks?state=closed&limit=1&cursor=&search=zephyrquill")).body.tasks[0];

  // Typed at keyboard speed: one debounced request per settled word.
  for (const [what, q, expect] of [
    ["title", "zephyrquill", true],
    ["description", "obsidianlark", true],
    ["externalRef", "CA-9012", true],
    ["owner", "deepowner", true],
    ["id", `#${deep.id}`, true],
  ]) {
    await searchBox(page).fill("");
    await page.waitForTimeout(500);
    const before = reqs.length;
    const t0 = Date.now();
    await searchBox(page).pressSequentially(q, { delay: 40 });
    const found = await page
      .locator(".task-row", { hasText: "zephyrquill" })
      .first()
      .waitFor({ timeout: 6000 })
      .then(() => Date.now() - t0, () => null);
    await page.waitForTimeout(700);
    const sent = reqs.slice(before).filter((r) => r.search);
    step(`search by ${what} (${tag})`, {
      q,
      expectFound: expect,
      foundMs: found,
      rows: await page.locator(".task-row").count(),
      searchRequests: sent.map((r) => r.search),
    });
  }
  await page.screenshot({ path: `${out}/search-found-${tag}.png` });

  // Out of order: the first word's answer is held 2.5 s, the second's is
  // not; the first must never show once the second is typed.
  await searchBox(page).fill("");
  await page.waitForTimeout(600);
  let held = 0;
  await page.route(/\/api\/tasks\?.*search=seed%200013/, async (route) => {
    held++;
    await new Promise((r) => setTimeout(r, 2500));
    await route.continue();
  });
  await searchBox(page).pressSequentially("seed 0013", { delay: 30 });
  await page.waitForTimeout(450); // past the 300 ms debounce: request out
  await searchBox(page).fill("seed 0021");
  await page.waitForTimeout(3500); // held answer arrives late
  const stale = await rowsWith(page, ".task-row", "seed 0013");
  const fresh = await rowsWith(page, ".task-row", "seed 0021");
  step(`out-of-order answers (${tag})`, { held, staleRowsShown: stale, freshRowsShown: fresh });
  await page.unroute(/\/api\/tasks\?.*search=seed%200013/);

  // Clearing: back to the paged view, and nothing the searches merged in
  // drawn out of place below it.
  await searchBox(page).fill("");
  await page.waitForTimeout(1500);
  const titles = await page.locator(".task-row").allInnerTexts();
  const nums = titles.map(seedNo).filter((n) => n !== null);
  const closedNums = nums.filter((n) => n < 3000);
  // Out of place: a closed row far below its neighbours (a gap of more
  // than one page's worth of seeds between consecutive rows).
  const outOfPlace = [];
  for (let i = 1; i < closedNums.length; i++) {
    if (closedNums[i - 1] - closedNums[i] > 250) outOfPlace.push(closedNums[i]);
  }
  step(`search cleared (${tag})`, {
    rowsBefore: baseRows,
    rowsAfter: titles.length,
    outOfPlaceRows: outOfPlace,
  });
  await page.screenshot({ path: `${out}/search-cleared-${tag}.png`, fullPage: false });
  await ctx.close();
}

// scrollWalk scrolls a view to its end with the wheel and checks the pages
// it read: every cursor once, no duplicate or missing rows, no request
// after the last page, and the scroll position never thrown backwards.
async function scrollWalk(vp, name, path, rowSel) {
  const ctx = await context(vp);
  const page = await ctx.newPage();
  const reqs = track(page);
  await page.goto(path);
  await page.locator(rowSel).first().waitFor({ timeout: 30000 });
  await page.waitForTimeout(1500);
  const scroller = async () =>
    page.evaluate((sel) => {
      const row = document.querySelectorAll(sel);
      let el = row[row.length - 1];
      while (el && el !== document.body) {
        const s = getComputedStyle(el);
        if (/(auto|scroll)/.test(s.overflowY) && el.scrollHeight > el.clientHeight + 4) {
          return { top: el.scrollTop, height: el.scrollHeight, client: el.clientHeight, tag: el.className.toString().slice(0, 40) };
        }
        el = el.parentElement;
      }
      const d = document.scrollingElement;
      return { top: d.scrollTop, height: d.scrollHeight, client: d.clientHeight, tag: "document" };
    }, rowSel);
  let last = await scroller();
  const backJumps = [];
  let idle = 0;
  const t0 = Date.now();
  let wheels = 0;
  while (idle < 12 && Date.now() - t0 < 240000) {
    // Over the last row, so the wheel reaches whichever box scrolls.
    const box = await page.locator(rowSel).last().boundingBox().catch(() => null);
    await page.mouse.move(vp.width / 2, Math.min(vp.height - 60, Math.max(120, box ? box.y : vp.height / 2)));
    await page.mouse.wheel(0, 2400);
    wheels++;
    await page.waitForTimeout(250);
    const now = await scroller();
    if (now.top + 4 < last.top) backJumps.push({ from: last.top, to: now.top, wheel: wheels });
    const atEnd = now.top + now.client >= now.height - 8;
    idle = atEnd && now.height === last.height ? idle + 1 : 0;
    last = now;
  }
  const endedAt = reqs.length;
  await page.waitForTimeout(5000);
  const after = reqs.length - endedAt;
  const paging = reqs.filter((r) => !r.search && r.limit === "200");
  const cursors = paging.map((r) => r.cursor).filter((c) => c);
  const texts = await page.locator(rowSel).allInnerTexts();
  const nums = texts.map(seedNo).filter((n) => n !== null);
  const closedNums = nums.filter((n) => n < 3000);
  const uniq = new Set(closedNums);
  const missing = [];
  for (let i = 0; i < 3000; i++) if (!uniq.has(i)) missing.push(i);
  step(`scroll ${name} (${vp.width})`, {
    wheels,
    seconds: Math.round((Date.now() - t0) / 1000),
    pageRequests: paging.length,
    distinctCursors: new Set(cursors).size,
    repeatedCursors: cursors.length - new Set(cursors).size,
    requestsAfterEnd: after,
    rowsDrawn: texts.length,
    closedSeedRows: closedNums.length,
    duplicateRows: closedNums.length - uniq.size,
    missingClosed: missing.length,
    missingSample: missing.slice(0, 10),
    backJumps: backJumps.slice(0, 5),
    backJumpCount: backJumps.length,
    loadMoreLeft: await page.locator(".closed-tasks-more").count(),
    scroller: last.tag,
  });
  await page.screenshot({ path: `${out}/scroll-end-${name}-${vp.width}.png` });
  await ctx.close();
}

try {
  const desktop = { width: 1440, height: 900 };
  const phone = { width: 390, height: 844 };
  const live = (await api("GET", "/api/tasks")).body;
  const totals = (await api("GET", "/api/tasks?state=closed&limit=1&cursor=")).body;
  step("corpus", { live: live.length, closedTotal: totals.total });
  for (const kind of ["kanban", "tree"]) {
    const res = await api("POST", "/api/boards", {
      name: `walk ${kind} ${Date.now()}`,
      kind,
      query: "f=state:any:closed",
      ...(kind === "kanban" ? { columns: [{ name: "Closed", states: ["closed"] }] } : {}),
    });
    boardIds.push({ kind, id: res.body.id });
  }
  for (const vp of [desktop, phone]) {
    if (!only || only === "search") await searchWalk(vp);
    if (!only || only === "scroll") {
      await scrollWalk(vp, "list", "/?f=state:any:closed", ".task-row");
      for (const b of boardIds) {
        await scrollWalk(vp, b.kind, `/boards/${b.id}`, b.kind === "kanban" ? ".board-card" : ".task-tree-row");
      }
    }
  }
} catch (err) {
  step("error", { message: String(err?.stack || err) });
} finally {
  for (const b of boardIds) await api("DELETE", `/api/boards/${b.id}`);
  await browser.close();
  await fs.writeFile(`${out}/search-scroll-${label}.json`, JSON.stringify(result, null, 2));
}
