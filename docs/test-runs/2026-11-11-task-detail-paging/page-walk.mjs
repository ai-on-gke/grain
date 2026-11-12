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

// page-walk.mjs: §2.3 against a real daemon's task page, in Chromium.
//
//   UI_DIR=work/ui BASE=http://127.0.0.1:4311 TOKEN=$(cat sq/local-api-token) \
//   TASK=2 FIRST=<oldest comment id> COMPACTED=<task id> CT=<compactedThrough> \
//   OUT=<dir> PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node page-walk.mjs
//
// Checks, at 1440x900 and 390x844 in light and dark:
//  1. the page renders the newest 50 comments with a "Show earlier" button;
//  2. "Show earlier" adds a strictly older slice (every new id < the old
//     first id, no duplicates);
//  3. a comment posted through the API shows on the next conditional poll
//     and the earlier slice is still loaded;
//  4. #comment=FIRST pages back to the oldest comment, highlights it, and
//     leaves it focused and on screen;
//  5. COMPACTED (compactedThrough=CT): after Show earlier, no comment at or
//     before CT is visible until the collapsed group is opened.
// and screenshots each state into OUT.
import { createRequire } from "node:module";
const require = createRequire(`${process.env.UI_DIR}/package.json`);
const { chromium } = require("@playwright/test");

const { BASE, TOKEN, TASK, FIRST, COMPACTED, CT, OUT } = process.env;
const ids = (page) =>
  page.$$eval("[data-comment-id]", (els) => [
    ...new Set(els.map((e) => Number(e.getAttribute("data-comment-id")))),
  ]);
const visibleIds = (page) =>
  page.$$eval("[data-comment-id]", (els) =>
    els.filter((e) => e.offsetParent !== null).map((e) => Number(e.getAttribute("data-comment-id"))));
const showEarlier = (page) =>
  page.getByRole("button", { name: /^Show earlier/ }).first();
let failures = 0;
const check = (ok, what) => {
  console.log(`${ok ? "PASS" : "FAIL"} ${what}`);
  if (!ok) failures++;
};
async function post(path, body) {
  const r = await fetch(BASE + path, {
    method: "POST",
    headers: { Authorization: `Bearer ${TOKEN}`, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`${path}: ${r.status}`);
}

const browser = await chromium.launch();
for (const [vw, vh] of [[1440, 900], [390, 844]]) {
  for (const scheme of ["light", "dark"]) {
    const tag = `${vw}-${scheme}`;
    const ctx = await browser.newContext({
      viewport: { width: vw, height: vh },
      colorScheme: scheme,
      extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
    });
    const page = await ctx.newPage();
    const pagedGets = [];
    page.on("request", (r) => {
      const u = new URL(r.url());
      if (u.pathname === `/api/tasks/${TASK}`) pagedGets.push(u.search);
    });

    let t0 = Date.now();
    await page.goto(`${BASE}/tasks/${TASK}`);
    await page.waitForSelector("[data-comment-id]");
    const head = await ids(page);
    check(head.length === 50 && (await showEarlier(page).isVisible()),
      `${tag}: head page renders 50 comments and Show earlier (${head.length}, ${Date.now() - t0} ms)`);
    await page.screenshot({ path: `${OUT}/${tag}-head.png` });

    await showEarlier(page).click();
    await page.waitForFunction((n) => document.querySelectorAll("[data-comment-id]").length > n, head.length);
    const after = await ids(page);
    const added = after.filter((i) => !head.includes(i));
    check(added.length > 0 && added.every((i) => i < Math.min(...head)) && after.length === new Set(after).size,
      `${tag}: Show earlier adds ${added.length} strictly older comments`);

    const body = `poll probe ${tag}`;
    await post(`/api/tasks/${TASK}/comments`, { body });
    t0 = Date.now();
    await page.getByText(body).first().waitFor({ timeout: 30000 });
    const polled = await ids(page);
    check(added.every((i) => polled.includes(i)),
      `${tag}: new comment shown by the poll in ${Date.now() - t0} ms; earlier slice kept (${polled.length} comments)`);

    // The highlight fades after a moment, so it is caught by an observer
    // installed before the link is followed; focus stays on the comment.
    await page.evaluate((id) => {
      window.__highlighted = false;
      new MutationObserver(() => {
        const el = document.querySelector(`[data-comment-id="${id}"]`);
        if (el && el.classList.contains("comment-highlight")) window.__highlighted = true;
      }).observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["class"] });
    }, FIRST);
    t0 = Date.now();
    await page.evaluate((id) => { location.hash = `comment=${id}`; }, FIRST);
    await page.waitForFunction(() => window.__highlighted, null, { timeout: 120000 });
    const ms = Date.now() - t0;
    const first = page.locator(`[data-comment-id="${FIRST}"]`).first();
    await page.waitForTimeout(500);
    const focused = await first.evaluate((el) => document.activeElement === el);
    const box = await first.boundingBox();
    check(focused && box && box.y < vh && box.y + box.height > 0,
      `${tag}: deep link to comment ${FIRST} loaded back and highlighted in ${ms} ms, focused and on screen (${(await ids(page)).length} comments in the DOM, ${pagedGets.filter((s) => s.includes("comments_before")).length} earlier-page fetches in total)`);
    await page.screenshot({ path: `${OUT}/${tag}-deeplink.png` });

    if (COMPACTED) {
      await page.goto(`${BASE}/tasks/${COMPACTED}`);
      await page.waitForSelector("[data-comment-id]");
      await showEarlier(page).click();
      await page.waitForFunction(() => document.querySelectorAll("[data-comment-id]").length > 50);
      await page.waitForTimeout(500);
      const all = await ids(page);
      const vis = await visibleIds(page);
      const hiddenCompacted = all.filter((i) => i <= Number(CT)).length;
      const visibleCompacted = vis.filter((i) => i <= Number(CT)).length;
      check(hiddenCompacted > 0 && visibleCompacted === 0,
        `${tag}: compacted task loads ${hiddenCompacted} comments at or before compactedThrough=${CT}, ${visibleCompacted} of them visible (visible ${Math.min(...vis)}..${Math.max(...vis)})`);
      await page.screenshot({ path: `${OUT}/${tag}-compacted.png` });
    }
    await ctx.close();
  }
}
await browser.close();
process.exit(failures ? 1 : 0);
