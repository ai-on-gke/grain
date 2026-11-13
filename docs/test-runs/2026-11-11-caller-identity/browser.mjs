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

// Item 1 of docs/test-plans/2026-11-11.md §5 in a real browser: two
// access-code sessions named alice@ and bob@ in separate contexts, a name
// the allowlist refuses, and a v1 (unnamed) cookie, against the daemon
// env.sh starts. Run from ui/ (for its playwright):
//
//   cp browser.mjs ui/.browser.mjs && cd ui && DAEMON=http://127.0.0.1:4301 CODE=... TASK=1 V1=... OUT=/tmp/shots \
//     node .browser.mjs

import { chromium } from "@playwright/test";
import fs from "node:fs";

const daemon = process.env.DAEMON;
const code = process.env.CODE;
const task = process.env.TASK;
const v1 = process.env.V1;
const out = process.env.OUT || "/tmp/caller-identity-shots";
fs.mkdirSync(out, { recursive: true });

const viewports = [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
];
const results = [];
const note = (k, v) => {
  results.push([k, v]);
  console.log(`${k}: ${v}`);
};

async function signIn(browser, name, viewport, scheme) {
  const ctx = await browser.newContext({ viewport, colorScheme: scheme });
  const page = await ctx.newPage();
  await page.goto(daemon + "/");
  await page.fill('input[name="code"]', code);
  await page.fill('input[name="name"]', name);
  await Promise.all([
    page.waitForLoadState("load"),
    page.locator('button[type="submit"], input[type="submit"]').first().click(),
  ]);
  await page.waitForTimeout(1500);
  return { ctx, page };
}

const browser = await chromium.launch();
try {
  // Two named sessions side by side.
  const a = await signIn(browser, "alice@example.com", viewports[0][1], "light");
  const b = await signIn(browser, "bob@example.com", viewports[0][1], "light");
  for (const [who, s] of [["alice", a], ["bob", b]]) {
    const whoami = await s.page.evaluate(async () => (await fetch("/api/whoami")).json());
    note(`${who} whoami`, `${whoami.actor.id} (${whoami.actor.label})`);
  }
  // bob comments on alice's task from the task page's API, as the UI does.
  const posted = await b.page.evaluate(async (id) => {
    const r = await fetch(`/api/tasks/${id}/comments`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ body: "bob, from the browser" }),
    });
    return r.status;
  }, task);
  note("bob comment from browser", posted);
  await a.page.goto(`${daemon}/tasks/${task}`);
  await a.page.waitForLoadState("load");
  await a.page.waitForTimeout(2000);
  const taskText = await a.page.locator("body").innerText();
  note("task page names alice as author", /alice@example\.com/.test(taskText));
  note("task page names bob on his comment", /bob@example\.com/.test(taskText));
  await a.page.screenshot({ path: `${out}/task-desktop-light.png`, fullPage: false });

  // Settings -> Access history, both widths, both schemes.
  for (const [vpName, vp] of viewports) {
    for (const scheme of ["light", "dark"]) {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: scheme });
      await ctx.addCookies(await a.ctx.cookies());
      const page = await ctx.newPage();
      await page.goto(`${daemon}/settings/access`);
      await page.waitForLoadState("load");
      await page.waitForTimeout(2000);
      const text = await page.locator("body").innerText();
      if (vpName === "desktop" && scheme === "light") {
        note("access history shows alice's code-session write", /POST \/api\/tasks by alice@example\.com/.test(text));
        note("access history shows bob's code-session write", /by bob@example\.com/.test(text));
      }
      await page.screenshot({ path: `${out}/settings-access-${vpName}-${scheme}.png`, fullPage: true });
      await ctx.close();
    }
  }

  // A name the allowlist does not admit.
  const c = await signIn(browser, "carol@example.com", viewports[0][1], "light");
  const cText = await c.page.locator("body").innerText();
  note("carol (not on the allowlist) refused", /not on this deployment's allowlist/.test(cText));
  await c.page.screenshot({ path: `${out}/signin-refused-desktop-light.png` });

  // An old v1 cookie still works, unnamed.
  const d = await browser.newContext({ viewport: viewports[1][1], colorScheme: "dark" });
  const host = new URL(daemon).hostname;
  await d.addCookies([{ name: "grain_access", value: v1, domain: host, path: "/" }]);
  const dp = await d.newPage();
  const whoV1 = await dp.request.get(`${daemon}/api/whoami`);
  const w = await whoV1.json();
  note("v1 cookie whoami", `${whoV1.status()} ${w.actor && w.actor.id} (${w.actor && w.actor.label})`);
  await dp.goto(`${daemon}/`);
  await dp.waitForLoadState("load");
  await dp.waitForTimeout(1500);
  await dp.screenshot({ path: `${out}/v1-cookie-phone-dark.png` });
} finally {
  await browser.close();
}
fs.writeFileSync(`${out}/results.json`, JSON.stringify(results, null, 2));
