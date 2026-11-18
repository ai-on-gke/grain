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

// Item 2: /next/fleet with two stacked "Publishing held" banners (real
// findings on acme/widgets@main and @release, each gated by an open
// release triage task). Over a minute, snapshot every live region's ARIA
// tree and record each change inside one -- what a screen reader would
// announce -- and the banners' full text, whose age moves outside them.
import { chromium, session, watch } from "./lib.mjs";

const base = process.env.BASE, code = process.env.CODE, secs = Number(process.env.SECS || 70);
const browser = await chromium.launch();
const ctx = await session(browser, base, code, { width: Number(process.env.WIDTH || 1440), height: 900 });
const page = await ctx.newPage();
const log = watch(page);
await page.goto(`${base}/next/fleet`);
await page.getByTestId("fleet-publishing").first().waitFor({ timeout: 20000 });
await page.evaluate(() => {
  window.__liveChanges = [];
  const seen = new WeakSet();
  const observe = () => {
    for (const el of document.querySelectorAll('[role=status],[role=alert],[aria-live]')) {
      if (seen.has(el)) continue;
      seen.add(el);
      new MutationObserver(() => window.__liveChanges.push({ at: Date.now(), role: el.getAttribute("role") || el.getAttribute("aria-live"), text: el.textContent.replace(/\s+/g, " ").trim().slice(0, 300) }))
        .observe(el, { subtree: true, childList: true, characterData: true });
    }
  };
  observe();
  new MutationObserver(observe).observe(document.body, { subtree: true, childList: true });
});
const snaps = [];
for (let t = 0; t <= secs; t += 10) {
  const regions = page.locator("[role=status],[aria-live]");
  const n = await regions.count();
  const s = [];
  for (let i = 0; i < n; i++) {
    const r = regions.nth(i);
    if (!(await r.isVisible().catch(() => false))) continue;
    s.push((await r.ariaSnapshot()).replace(/\n\s*/g, " ⏎ "));
  }
  const full = (await page.getByTestId("fleet-publishing").allInnerTexts()).map((b) => b.replace(/\s+/g, " "));
  const banners = full.map((b) => `${b.length}:${(b.match(/\d+[mhd] ago/) || [""])[0]}`);
  snaps.push({ t, regions: s, bannerLengths: banners });
  console.log(`t=${t}s regions=${JSON.stringify(s)} bannerChars=${JSON.stringify(banners)}`);
  if (t < secs) await page.waitForTimeout(10000);
}
const changes = await page.evaluate(() => window.__liveChanges);
console.log(`live-region changes in ${secs}s: ${changes.length}`);
for (const c of changes.slice(0, 20)) console.log(`  change ${c.role}: ${JSON.stringify(c.text)}`);
const longest = Math.max(0, ...changes.map((c) => c.text.length));
const statusTexts = (await page.getByTestId("fleet-publishing-status").allInnerTexts()).map((s) => s.replace(/\s+/g, " "));
console.log(`fleet-publishing-status=${JSON.stringify(statusTexts)}`);
console.log(`${statusTexts.length === 2 && statusTexts.every((s) => s.length < 60) && longest < 120 ? "PASS" : "CHECK"}: live regions hold only the titles (longest change ${longest} chars)`);
await page.screenshot({ path: `/tmp/item2-fleet.png`, fullPage: true });
console.log(`errors=${JSON.stringify(log)}`);
await browser.close();
