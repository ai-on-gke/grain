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

// stale_alert.mjs checks the stale-schedules alert on the real daemon
// env.sh started (§10 item 2): System → Health and the CI pane at
// 1440×900 and 390×844, light and dark, signed in with the access code,
// and the CI pane for a reader the server answers 403 on
// GET /api/sandboxes (no deployment:read), which must show no banner.
//
//   cd ui && URL=http://127.0.0.1:4261 CODE=$(cat .../local-api-token) \
//     WANT=2 node ../docs/test-runs/2026-11-11-schedule-groups/stale_alert.mjs
//
// READER_TOKEN, when set, is a gma_ token without deployment:read whose
// 403 on the stale route is replayed into the reader's page.
// Run from ui/, whose node_modules and e2e helpers it borrows.
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";

const uiRequire = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = uiRequire("@playwright/test");
const { signIn } = await import(
  pathToFileURL(path.join(process.cwd(), "e2e/accesscode.js")).href
);

const url = process.env.URL || "http://127.0.0.1:4261";
const code = process.env.CODE;
const want = Number(process.env.WANT ?? "2");
const results = [];
const fail = (msg) => {
  results.push(`FAIL ${msg}`);
  process.exitCode = 1;
};

const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
for (const [name, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  for (const theme of ["light", "dark"]) {
    const ctx = await browser.newContext({ viewport, colorScheme: theme });
    await ctx.addCookies(cookies);
    await ctx.addInitScript(
      (t) => localStorage.setItem("grain.themeMode", t),
      theme,
    );
    const page = await ctx.newPage();
    for (const path of ["/system/health", "/ci"]) {
      await page.goto(url + path);
      const alert = page.getByTestId("stale-schedules");
      const label = `${path} ${name} ${theme}`;
      if (want === 0) {
        await page.waitForTimeout(1500);
        if (await alert.count()) fail(`${label}: alert shown, want none`);
        else results.push(`ok   ${label}: no alert`);
        continue;
      }
      try {
        await alert.waitFor({ timeout: 15000 });
      } catch {
        fail(`${label}: no alert`);
        continue;
      }
      const rows = await alert.getByTestId("stale-schedule").count();
      const box = await alert.boundingBox();
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      let contained = box.x >= 0 && box.x + box.width <= viewport.width + 0.5;
      for (const row of await alert.getByTestId("stale-schedule").all()) {
        const r = await row.boundingBox();
        contained &&= r.x >= box.x && r.x + r.width <= box.x + box.width + 0.5;
      }
      const text = (await alert.innerText()).replace(/\s+/g, " ");
      const line = `${label}: rows=${rows} contained=${contained} overflow=${overflow}`;
      if (rows !== want || !contained || overflow > 0) fail(line);
      else results.push(`ok   ${line}`);
      results.push(`     ${text.slice(0, 400)}`);
    }
    await ctx.close();
  }
}

// A reader without deployment:read: the server refuses the stale route,
// and the CI pane shows no banner.
if (process.env.READER_TOKEN) {
  const refused = await fetch(`${url}/api/sandboxes?section=staleSchedules`, {
    headers: { Authorization: `Bearer ${process.env.READER_TOKEN}` },
  });
  results.push(`reader GET /api/sandboxes?section=staleSchedules -> ${refused.status}`);
  const body = await refused.text();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await ctx.addCookies(cookies);
  const page = await ctx.newPage();
  await page.route(
    (u) => u.pathname === "/api/sandboxes",
    (route) => route.fulfill({ status: refused.status, body, contentType: "application/json" }),
  );
  await page.goto(url + "/ci");
  await page.waitForTimeout(2000);
  if (await page.getByTestId("stale-schedules").count()) fail("reader /ci: banner shown");
  else results.push("ok   reader /ci: no banner");
  await ctx.close();
}
await browser.close();
console.log(results.join("\n"));
