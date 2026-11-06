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

// §10 item 1: closed-task paging under change, over a seeded demo.
// Usage (from anywhere):
//   GRAIN_SERVER=http://127.0.0.1:4390 GRAIN_TOKEN=... node paging-walk.mjs
// Writes paging-walk.json and paging.har beside itself.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const out = new URL(".", import.meta.url).pathname;
const result = { steps: [] };
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
const browser = await chromium.launch();
const ctx = await browser.newContext({
  baseURL: S,
  viewport: { width: 1440, height: 900 },
  recordHar: { path: `${out}/paging.har`, content: "omit", urlFilter: /\/api\/tasks/ },
});
await ctx.request.post("/api/auth/access-code", { data: { code: T } });
const boardIds = [];
try {
  // Live tasks from the seed and the ids of the closed ones, oldest last.
  const live = (await api("GET", "/api/tasks")).body;
  const liveSeed = live.filter((t) => t.title.startsWith("seed "));
  const target = liveSeed[0];
  step("live poll", { liveTasks: live.length, liveSeed: liveSeed.length, target: target.id });
  const allClosed = [];
  for (let cursor = "", pages = 0; ; pages++) {
    const p = (await api("GET", `/api/tasks?state=closed&limit=200&cursor=${encodeURIComponent(cursor)}`)).body;
    allClosed.push(...p.tasks.map((t) => String(t.id)));
    if (!p.nextCursor) {
      step("API closed pages", { pages: pages + 1, ids: allClosed.length, unique: new Set(allClosed).size, total: p.total });
      break;
    }
    cursor = p.nextCursor;
  }
  const oldest = allClosed[allClosed.length - 1];

  // Tab A: the closed list. Tab B: the task's own page, where it is closed.
  const a = await ctx.newPage();
  const pageIds = [];
  a.on("response", async (r) => {
    if (r.url().includes("state=closed")) {
      try {
        const j = await r.json();
        pageIds.push(...j.tasks.map((t) => String(t.id)));
      } catch {}
    }
  });
  await a.goto("/?f=state:any:closed");
  await a.locator(".task-row").first().waitFor();
  const rowsAt = async () => a.locator(".task-row").count();
  step("closed list at load", { rows: await rowsAt() });

  const b = await ctx.newPage();
  await b.goto(`/tasks/${target.id}`);
  await b.waitForLoadState("networkidle");
  const t0 = Date.now();
  const closeRes = await b.request.post(`/api/tasks/${target.id}/close`);
  const row = a.locator(".task-row", { hasText: target.title.slice(0, 30) });
  await row.first().waitFor({ timeout: 15000 });
  step("close in tab B -> tab A closed list", { closeStatus: closeRes.status(), ms: Date.now() - t0 });

  const t1 = Date.now();
  const reopenRes = await b.request.post(`/api/tasks/${target.id}/reopen`);
  await row.first().waitFor({ state: "detached", timeout: 15000 });
  const goneMs = Date.now() - t1;
  await a.goto("/?f=state:none:closed");
  await a.locator(".task-row", { hasText: target.title.slice(0, 30) }).first().waitFor({ timeout: 15000 });
  step("reopen in tab B -> left closed list, back in open list", { reopenStatus: reopenRes.status(), goneMs });

  // Load older to the end.
  await a.goto("/?f=state:any:closed");
  await a.locator(".task-row").first().waitFor();
  pageIds.length = 0;
  let clicks = 0;
  const more = a.getByRole("button", { name: "Load older closed tasks" });
  const clickLog = [];
  while (await more.isVisible().catch(() => false)) {
    const box = await more.boundingBox();
    await more.click();
    clicks++;
    clickLog.push({ box, urlAfter: a.url().replace(S, ""), rowsAfter: await a.locator(".task-row").count() });
    // The button reads "Loading closed tasks…" while its page is read
    // (innerText is lower-cased by the theme's text-transform, so match
    // on the accessible name, case-insensitively).
    await a.getByRole("button", { name: /loading closed tasks/i }).waitFor({ state: "hidden", timeout: 30000 });
    if (clicks > 40) break;
  }
  const rowIds = await a.$$eval(".task-row", (rows) =>
    rows.map((r) => r.getAttribute("data-task-id") || r.id || r.textContent.slice(0, 60)),
  );
  step("load older to the end", {
    clicks,
    pageResponses: pageIds.length,
    rowsDrawn: rowIds.length,
    uniqueRows: new Set(rowIds).size,
    buttonGone: !(await more.isVisible().catch(() => false)),
    firstClicks: clickLog.slice(0, 2),
    urlAtEnd: a.url().replace(S, ""),
  });

  // Filters, boards and the tree over the oldest closed task.
  const oldTask = (await api("GET", `/api/tasks/${oldest}`)).body;
  const oldTitle = oldTask.title.slice(0, 30);
  await a.locator('input[type="search"], input[placeholder*="Search" i]').first().fill(oldTitle);
  const filtered = await a
    .locator(".task-row", { hasText: oldTitle })
    .first()
    .waitFor({ timeout: 10000 })
    .then(() => true, () => false);
  step("search filter finds the oldest closed task after paging", { oldest, filtered });

  for (const kind of ["list", "tree", "kanban"]) {
    const res = await api("POST", "/api/boards", {
      name: `walk ${kind} ${Date.now()}`,
      kind,
      query: "f=state:any:closed",
      ...(kind === "kanban" ? { columns: [{ name: "Closed", states: ["closed"] }] } : {}),
    });
    boardIds.push(res.body.id);
    // Navigate inside the SPA so the already-paged closed tasks are kept.
    await a.evaluate((p) => {
      window.history.pushState({}, "", p);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }, `/boards/${res.body.id}`);
    const seen = await a
      .getByText(oldTitle, { exact: false })
      .first()
      .waitFor({ timeout: 10000 })
      .then(() => true, () => false);
    step(`saved ${kind} board shows the oldest closed task`, { board: res.body.id, seen });
  }

  // A deep link to the oldest closed task, in a fresh tab.
  const c = await ctx.newPage();
  await c.goto(`/tasks/${oldest}`);
  const opened = await c
    .getByText(oldTitle, { exact: false })
    .first()
    .waitFor({ timeout: 10000 })
    .then(() => true, () => false);
  await c.screenshot({ path: `${out}/deep-link-oldest-closed.png` });
  step("deep link to the oldest closed task", { oldest, opened });
} finally {
  for (const id of boardIds) await api("DELETE", `/api/boards/${id}`);
  await ctx.close();
  await browser.close();
}
// HAR numbers.
const har = JSON.parse(await fs.readFile(`${out}/paging.har`, "utf8"));
const rows = har.log.entries.map((e) => ({
  url: e.request.url.replace(S, ""),
  status: e.response.status,
  encoding: (e.response.headers.find((h) => h.name.toLowerCase() === "content-encoding") || {}).value || "",
  bodySize: e.response.content.size,
  transfer: e.response._transferSize ?? e.response.bodySize,
}));
const pick = (pred) => rows.filter(pred);
result.har = {
  livePolls: pick((r) => r.url === "/api/tasks").slice(0, 3),
  live304s: pick((r) => r.url === "/api/tasks" && r.status === 304).length,
  closedFirstPage: pick((r) => r.url.includes("state=closed") && r.url.endsWith("cursor=")).slice(0, 2),
  entries: rows.length,
};
console.log(JSON.stringify(result.har, null, 1));
await fs.writeFile(`${out}/paging-walk.json`, JSON.stringify(result, null, 2));
