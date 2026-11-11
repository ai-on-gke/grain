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

// Playwright Chromium walk of the Registry Phase 2 ui-registry screens for
// the 2026-11-08 §1 walkthrough, against a running `grain-registry demo`:
// Stop/Start confirmation modals (Escape, focus return, confirm), lifecycle
// badges, the auto-wake banner, /grants, /admin#delegating-apps and the admin
// recovery panels, plus a screenshot + horizontal-overflow sweep at 1440x900
// and 390x844 in light and dark.
//
//   cd ui-registry && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-08-grain-registry-phase2/ui-walk.mjs /tmp/gr2/shots http://127.0.0.1:4280
//
// Run it from ui-registry/ so @playwright/test resolves.

import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const [out, base] = process.argv.slice(2);
await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch();
let problems = 0;
const ok = (cond, what) => {
  console.log(`${cond ? "PASS" : "FAIL"} ${what}`);
  if (!cond) problems++;
};
const VIEWPORTS = { desktop: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };

async function newPage(vp, theme) {
  const ctx = await browser.newContext({ viewport: VIEWPORTS[vp], colorScheme: theme });
  await ctx.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
  const page = await ctx.newPage();
  page.on("pageerror", (e) => {
    console.log(`FAIL pageerror ${vp}/${theme}: ${e.message}`);
    problems++;
  });
  return page;
}

async function overflow(page) {
  return page.evaluate(() => {
    const vw = document.documentElement.clientWidth;
    const doc = document.documentElement.scrollWidth > vw + 1;
    const bad = [];
    for (const el of document.querySelectorAll("body *")) {
      const r = el.getBoundingClientRect();
      if (!r.width || !r.height || r.right <= vw + 1) continue;
      let p = el.parentElement;
      let scrolls = false;
      while (p && p !== document.body) {
        const s = getComputedStyle(p);
        if (/(auto|scroll|hidden)/.test(s.overflowX)) {
          scrolls = true;
          break;
        }
        p = p.parentElement;
      }
      if (!scrolls) bad.push(`${el.tagName.toLowerCase()}.${el.className}`.slice(0, 80));
    }
    return { doc, bad: bad.slice(0, 5) };
  });
}

// 1. Interactions, at both widths (light) so the phone modal is exercised too.
// Phone goes first: desktop then confirms Stop and Start, changing demo state.
for (const vp of ["phone", "desktop"]) {
  const page = await newPage(vp, "light");
  await page.goto(`${base}/`);
  const stopBtn = page.getByRole("button", { name: "Stop platform-vm" });
  await stopBtn.waitFor();
  await stopBtn.click();
  const dialog = page.getByRole("dialog");
  await dialog.waitFor();
  ok((await dialog.textContent()).includes("Stop platform-vm?"), `${vp}: Stop opens a confirmation naming the entry`);
  const box = await dialog.boundingBox();
  ok(box && box.x >= 0 && box.x + box.width <= VIEWPORTS[vp].width + 1, `${vp}: Stop dialog fits the viewport (${box && Math.round(box.width)}px)`);
  await page.screenshot({ path: path.join(out, `stop-modal-${vp}.png`) });
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "hidden" });
  ok(true, `${vp}: Escape closes the Stop confirmation`);
  const focused = await page.evaluate(() => document.activeElement?.getAttribute("aria-label") || document.activeElement?.textContent);
  ok(focused === "Stop platform-vm", `${vp}: focus returns to the Stop button after Escape (got ${JSON.stringify(focused)})`);
  if (vp === "desktop") {
    await stopBtn.click();
    await page.getByRole("button", { name: "Stop instance" }).click();
    await dialog.waitFor({ state: "hidden" });
    const row = page.locator("tr, li, article").filter({ hasText: "platform-vm" }).first();
    await page.waitForTimeout(500);
    const rowText = (await row.textContent()) || "";
    ok(/stopp(ing|ed)/i.test(rowText), `desktop: confirmed Stop shows a stopping/stopped badge (${rowText.match(/stopp\w+/i)?.[0]})`);
    await page.screenshot({ path: path.join(out, `my-entries-after-stop-${vp}.png`) });
  }
  await page.goto(`${base}/entries/docs-preview`);
  await page.getByText(/stopped/i).first().waitFor();
  const banner = page.getByTestId("stopped-banner");
  await banner.waitFor();
  const bannerText = (await banner.textContent()) || "";
  ok(/wake/i.test(bannerText), `${vp}: stopped entry detail shows the auto-wake banner ("${bannerText.slice(-48)}")`);
  const start = page.getByRole("button", { name: /^Start/ }).first();
  ok(await start.isVisible(), `${vp}: stopped entry detail offers Start`);
  await page.screenshot({ path: path.join(out, `entry-stopped-${vp}.png`), fullPage: true });
  if (vp === "desktop") {
    await start.click();
    const d = page.getByRole("dialog");
    if (await d.isVisible().catch(() => false)) {
      await page.keyboard.press("Escape");
      await d.waitFor({ state: "hidden" });
      ok(true, "desktop: Escape closes the Start confirmation");
      await start.click();
      await d.getByRole("button", { name: /Start/ }).last().click();
    }
    await page.waitForTimeout(800);
    const after = (await page.textContent("body")) || "";
    ok(!/\bstopped\b/i.test(after.slice(0, 2000)) || /creating|starting|running/i.test(after), "desktop: Start moves docs-preview out of stopped");
    await page.screenshot({ path: path.join(out, `entry-after-start-${vp}.png`), fullPage: true });
  }
  await page.goto(`${base}/grants`);
  await page.getByText("Apps acting for you").first().waitFor();
  ok(true, `${vp}: /grants shows "Apps acting for you"`);
  await page.goto(`${base}/admin#delegating-apps`);
  await page.waitForTimeout(800);
  const admin = (await page.textContent("body")) || "";
  ok(/delegating app/i.test(admin), `${vp}: /admin#delegating-apps renders the delegating apps panel`);
  for (const [label, re] of [
    ["quotas", /quota/i],
    ["scope conflicts", /conflict/i],
    ["SQL pool drain", /drain/i],
    ["orphan VMs", /orphan/i],
    ["tunnel disconnect", /tunnel/i],
    ["reconciler lease", /lease/i],
  ]) {
    ok(re.test(admin), `${vp}: /admin shows the ${label} recovery panel`);
  }
  await page.context().close();
}

// 2. Screenshot + overflow sweep.
const pages = ["/", "/grants", "/admin", "/admin#delegating-apps", "/entries/docs-preview", "/entries/platform-vm"];
for (const p of pages) {
  for (const vp of Object.keys(VIEWPORTS)) {
    for (const theme of ["light", "dark"]) {
      const page = await newPage(vp, theme);
      await page.goto(`${base}${p}`);
      await page.waitForLoadState("networkidle");
      await page.waitForTimeout(300);
      const o = await overflow(page);
      ok(!o.doc && o.bad.length === 0, `overflow ${p} ${vp}/${theme}${o.bad.length ? ": " + o.bad.join(", ") : ""}`);
      const name = `${p.replace(/[/#]+/g, "_").replace(/^_|_$/g, "") || "home"}-${vp}-${theme}.png`;
      await page.screenshot({ path: path.join(out, name), fullPage: true });
      await page.context().close();
    }
  }
}
await browser.close();
console.log(`${problems} problem(s)`);
process.exit(problems ? 1 : 0);
