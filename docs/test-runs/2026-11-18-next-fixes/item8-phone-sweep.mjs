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

// Item 8: the §8 pages at 390 px in light and dark, checked in the DOM
// alongside make ui-screenshots' contact sheet: no horizontal page
// overflow, no visible element poking past the viewport outside a
// scroll container, no text clipped to zero width, no page error.
import { chromium, session, watch } from "./lib.mjs";

const base = process.env.BASE, code = process.env.CODE;
const pages = (process.env.PAGES || "/next/processes/schedules,/next/fleet,/next/inbox,/next/tasks?task=3,/next/workstreams,/next/agents,/next/task/3,/next/processes/quality").split(",");
const browser = await chromium.launch();
let fails = 0;
for (const theme of ["light", "dark"]) {
  const ctx = await session(browser, base, code, { width: 390, height: 844, theme });
  for (const url of pages) {
    const page = await ctx.newPage();
    const log = watch(page);
    await page.goto(`${base}${url}`);
    await page.waitForTimeout(2500);
    const r = await page.evaluate(() => {
      const vw = document.documentElement.clientWidth;
      const scrolls = (el) => { for (let p = el.parentElement; p; p = p.parentElement) { const o = getComputedStyle(p).overflowX; if (o === "auto" || o === "scroll" || o === "hidden" || o === "clip") return p !== document.body && p !== document.documentElement; } return false; };
      const out = [];
      for (const el of document.querySelectorAll("body *")) {
        const rect = el.getBoundingClientRect();
        if (!rect.width || !rect.height || getComputedStyle(el).visibility === "hidden") continue;
        if (el.closest(".sr-only,[aria-hidden=true]")) continue;
        if (rect.right > vw + 1 && !scrolls(el)) out.push(`${el.tagName.toLowerCase()}.${String(el.className).split(" ")[0]} right=${Math.round(rect.right)}`);
      }
      const theme = document.documentElement.getAttribute("data-theme") || document.documentElement.className;
      return { scrollW: document.scrollingElement.scrollWidth, vw, offenders: out.slice(0, 5), count: out.length, theme, bg: getComputedStyle(document.body).backgroundColor };
    });
    const ok = r.scrollW <= r.vw + 1 && r.count === 0 && log.pageErrors.length === 0;
    if (!ok) fails++;
    console.log(`${ok ? "PASS" : "FAIL"} ${theme} ${url}: scrollWidth=${r.scrollW}/${r.vw} bg=${r.bg} offenders=${r.count} ${JSON.stringify(r.offenders)} pageErrors=${JSON.stringify(log.pageErrors)} http=${JSON.stringify(log.http)}`);
    await page.close();
  }
  await ctx.close();
}
console.log(`summary: ${fails} failing`);
await browser.close();
