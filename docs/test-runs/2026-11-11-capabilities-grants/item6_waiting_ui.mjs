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

// item6_waiting_ui.mjs URL CODE OUTDIR: §6 item 6. Over a running `grain
// demo`, lays a waiting capability server (status.state "needs-config",
// status.waiting set, as protocol.Waiting reports it) over the demo's
// gchat server in GET /api/capability-servers -- the route answers an
// array, which screenshot.mjs's --merge cannot lay fields over -- and
// shoots /settings/capabilities and that server's page at 1440x900 and
// 390x844, light and dark. Asserts the chip and note read "waiting for
// ...", the chip stays inside its row, and no page scrolls sideways.
// Run from ui/: node ../docs/test-runs/2026-11-11-capabilities-grants/item6_waiting_ui.mjs ...
import { chromium } from "@playwright/test";
import fs from "node:fs/promises";
import path from "node:path";

const [url, code, out] = process.argv.slice(2);
const WAITING = "the host's gcloud credential to be renewed (run the host's login command)";
const ID = "https://grain.dev/capabilities/gchat";
const pages = ["/settings/capabilities", `/settings/capabilities/${encodeURIComponent(ID)}`];
const viewports = { desktop: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };
await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch();
const results = [];
let failed = false;
for (const [wname, vp] of Object.entries(viewports)) {
  for (const theme of ["light", "dark"]) {
    const ctx = await browser.newContext({ viewport: vp, colorScheme: theme, isMobile: wname === "phone",
      hasTouch: wname === "phone", extraHTTPHeaders: { Authorization: `Bearer ${code}` } });
    await ctx.addInitScript(([k, v]) => localStorage.setItem(k, v), ["grain.themeMode", theme]);
    await ctx.route((u) => u.pathname === "/api/capability-servers", async (route) => {
      const response = await route.fetch();
      const json = await response.json();
      for (const s of json) {
        if ((s.id || s.capabilityId) === ID || s.status?.capabilityId === ID || JSON.stringify(s).includes('"' + ID + '"')) {
          s.status = { ...(s.status || {}), state: "needs-config", waiting: WAITING };
        }
      }
      await route.fulfill({ response, json });
    });
    for (const p of pages) {
      const tab = await ctx.newPage();
      const errors = [];
      tab.on("pageerror", (e) => errors.push(e.message));
      await tab.goto(new URL(p, url).href);
      await tab.waitForLoadState("networkidle", { timeout: 10000 }).catch(() => {});
      await tab.getByText(/waiting for/i).first().waitFor({ timeout: 10000 }).catch(() => {});
      const name = `${p === pages[0] ? "list" : "server"}-${wname}-${theme}.png`;
      await tab.screenshot({ path: path.join(out, name), fullPage: true });
      const r = await tab.evaluate((waiting) => {
        const all = [...document.querySelectorAll("body *")].filter((e) => e.children.length === 0 && /waiting for/i.test(e.textContent || ""));
        const chip = all.find((e) => (e.textContent || "").includes(waiting));
        let inside = null;
        if (chip) {
          const b = chip.getBoundingClientRect();
          let row = chip.parentElement;
          while (row && row.getBoundingClientRect().width <= b.width + 1) row = row.parentElement;
          const rb = row ? row.getBoundingClientRect() : null;
          inside = rb ? b.left >= rb.left - 1 && b.right <= rb.right + 1 : null;
        }
        return {
          waitingTexts: all.map((e) => (e.textContent || "").trim().slice(0, 140)),
          chipInsideRow: inside,
          horizontalScroll: document.documentElement.scrollWidth > window.innerWidth + 1,
        };
      }, WAITING);
      const ok = r.waitingTexts.length > 0 && !r.horizontalScroll && r.chipInsideRow !== false && errors.length === 0;
      failed ||= !ok;
      results.push({ page: p, width: wname, theme, ok, errors, ...r });
      await tab.close();
    }
    await ctx.close();
  }
}
await browser.close();
await fs.writeFile(path.join(out, "item6-results.json"), JSON.stringify(results, null, 2));
console.log(JSON.stringify(results.map((r) => ({ page: r.page.slice(0, 40), width: r.width, theme: r.theme, ok: r.ok, hs: r.horizontalScroll, inside: r.chipInsideRow, n: r.waitingTexts.length }))));
process.exit(failed ? 1 : 0);
