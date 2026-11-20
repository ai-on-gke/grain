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

// Dumps the rendered text of /next/ pages on a live daemon, for the §7
// walkthrough of docs/test-plans/2026-11-15.md, where nobody can look at
// the screenshots. Copy it into ui/e2e/ (for @playwright/test and
// accesscode.js) and run: URL=... CODE=... PAGES=/next/inbox,... node e2e/dom-dump.mjs
import { chromium } from "@playwright/test";
import { signIn } from "./accesscode.js";

const url = process.env.URL;
const { cookies } = await signIn(url, process.env.CODE);
const browser = await chromium.launch();
for (const width of (process.env.WIDTHS || "1440").split(",")) {
  const context = await browser.newContext({ viewport: { width: Number(width), height: 1000 } });
  await context.addCookies(cookies);
  const page = await context.newPage();
  for (const p of process.env.PAGES.split(",")) {
    await page.goto(url + p, { waitUntil: "load" });
    await page.waitForTimeout(4000);
    console.log(`===== ${p} @ ${width}px`);
    console.log(await page.locator("body").innerText());
    if (process.env.SELECTORS) {
      for (const sel of process.env.SELECTORS.split("|")) {
        const n = await page.locator(sel).count();
        const texts = n ? await page.locator(sel).allInnerTexts() : [];
        console.log(`-- ${sel}: ${n} ${JSON.stringify(texts.slice(0, 12))}`);
      }
    }
  }
  await context.close();
}
await browser.close();
