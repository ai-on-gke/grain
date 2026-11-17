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

// Item 5's Inbox half: with item5_give_up.sh's daemon left running (KEEP=1),
// opens /next/inbox and the classic /inbox at 1440x900 and 390x844 in light
// and dark, waits for the merge queue's give-up item, and records whether
// it names the pull request and the reason. Screenshots go to out/.
//
// Usage: node ui_inbox.mjs <url> <code> <task-id> <pr>
// (needs `npm ci` in ui/ and PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright)
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "../../..");
const { chromium } = await import(path.join(root, "ui/node_modules/@playwright/test/index.mjs"));
const { signIn } = await import(path.join(root, "ui/e2e/accesscode.js"));

const [url, code, taskId, pr] = process.argv.slice(2);
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const results = [];
for (const route of ["/next/inbox", "/inbox"]) {
  for (const [name, viewport] of [
    ["desktop", { width: 1440, height: 900 }],
    ["phone", { width: 390, height: 844 }],
  ]) {
    for (const theme of ["light", "dark"]) {
      const context = await browser.newContext({ viewport, colorScheme: theme, isMobile: name === "phone" });
      await context.addCookies(cookies);
      await context.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e)));
      await page.goto(url + route);
      let found = true;
      try {
        await page.getByText(/gave up on|Merge blocked/).first().waitFor({ timeout: 20000 });
      } catch {
        found = false;
      }
      await page.waitForTimeout(500);
      const r = await page.evaluate(
        ([pr, taskId]) => {
          const txt = document.body.innerText;
          return {
            namesPR: txt.includes(pr),
            namesTask: txt.includes(`task ${taskId}`) || txt.includes(`#${taskId}`),
            namesReason: txt.includes("cla/google"),
            gaveUp: /gave up/.test(txt),
            mergeBlocked: /Merge blocked/i.test(txt),
            retry: /Retry merge/i.test(txt),
            overflowPx: document.documentElement.scrollWidth - window.innerWidth,
          };
        },
        [pr, taskId],
      );
      const file = path.join(here, "out", `inbox${route.replaceAll("/", "-")}-${name}-${theme}.png`);
      await page.screenshot({ path: file, fullPage: false });
      results.push({ route, name, theme, found, ...r, errors: errors.slice(0, 2), file: path.basename(file) });
      await context.close();
    }
  }
}
await browser.close();
console.log(JSON.stringify(results, null, 1));
