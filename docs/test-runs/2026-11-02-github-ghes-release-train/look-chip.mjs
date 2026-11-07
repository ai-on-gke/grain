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

// Measures the external PR chip on /tasks/<id> at 1440/390 px, light and dark, and saves header crops.
// Usage (from ui/): node ../docs/test-runs/2026-11-02-github-ghes-release-train/look-chip.mjs URL TOKEN ID TAG
import { chromium } from "playwright";
const [url, code, id, tag] = process.argv.slice(2);
const browser = await chromium.launch();
const lum = (c) => { const [r,g,b] = c.match(/\d+(\.\d+)?/g).slice(0,3).map(Number).map(v=>v/255).map(v=> v<=0.03928? v/12.92 : ((v+0.055)/1.055)**2.4); return 0.2126*r+0.7152*g+0.0722*b; };
for (const [w,h,name] of [[1440,900,"desktop"],[390,844,"phone"]]) for (const theme of ["light","dark"]) {
  const ctx = await browser.newContext({ viewport:{width:w,height:h}, colorScheme: theme, extraHTTPHeaders: { Authorization: `Bearer ${code}` } });
  const page = await ctx.newPage();
  await page.goto(`${url}/tasks/${id}`); await page.waitForSelector(".freshness");
  await page.waitForTimeout(500);
  const chip = page.getByTestId("external-pr-chip");
  const n = await chip.count();
  const fr = page.locator(".freshness").first();
  const fb = await fr.boundingBox();
  let info = { n };
  if (n) {
    const cb = await chip.boundingBox();
    info = await chip.evaluate((el) => { const l = el.querySelector(".MuiChip-label"); let bg = getComputedStyle(el).backgroundColor; let p = el; while ((bg === "rgba(0, 0, 0, 0)" || bg === "transparent") && p.parentElement) { p = p.parentElement; bg = getComputedStyle(p).backgroundColor; } return { n:1, text: l.textContent, color: getComputedStyle(l).color, bg, border: getComputedStyle(el).borderColor, href: el.getAttribute("href") }; });
    const L1 = lum(info.color), L2 = lum(info.bg); info.contrast = +(((Math.max(L1,L2)+0.05)/(Math.min(L1,L2)+0.05)).toFixed(2));
    info.chip = cb; 
  }
  info.freshness = fb; info.freshnessText = (await fr.innerText()).replace(/\s+/g," ").slice(0,120);
  info.overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  await page.screenshot({ path: `screenshots/crops/${tag}-${name}-${theme}.jpg`, type: "jpeg", quality: 55, clip: { x: Math.max(0, fb.x-8), y: Math.max(0, fb.y-40), width: Math.min(w, fb.width+16, 700), height: fb.height+80 } });
  console.log(tag, name, theme, JSON.stringify(info));
  await ctx.close();
}
await browser.close();
