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

// pagetext.mjs prints what a page says, as text, for a walkthrough that has
// to read a screenshot it cannot see. Run from ui/ (for its Playwright):
//   URL=http://127.0.0.1:4290 CODE=<access code> node \
//     ../docs/test-runs/2026-11-15-release-train-publishing-held/pagetext.mjs \
//     /next/processes/schedules '[data-testid="sched-row-<id>"]'
// A second argument is a selector clicked before the text is read.
import { chromium } from "playwright";
import { signIn } from "../../../ui/e2e/accesscode.js";

const [page, click] = process.argv.slice(2);
const url = process.env.URL;
const { cookies } = await signIn(url, process.env.CODE);
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
await ctx.addCookies(cookies);
const tab = await ctx.newPage();
await tab.goto(new URL(page, url).href);
await tab.waitForTimeout(3000); // pages that poll never go network-idle
if (click) {
  await tab.locator(click).first().click({ timeout: 10_000 });
  await tab.waitForTimeout(3000); // pages that poll never go network-idle
}
console.log(await tab.locator("body").innerText());
await browser.close();
