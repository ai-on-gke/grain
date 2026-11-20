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

// Item 1: what /next/processes/schedule/<id> and classic Schedules show as
// a template-bound schedule's base. BASE and CODE name the real daemon.
import { chromium, session, watch } from "./lib.mjs";

const base = process.env.BASE, code = process.env.CODE, out = process.env.OUT || "/tmp/item1";
const browser = await chromium.launch();
const ctx = await session(browser, base, code);
const page = await ctx.newPage();
const log = watch(page);
for (const id of (process.env.IDS || "sched-1,sched-2").split(",")) {
  await page.goto(`${base}/next/processes/schedule/${id}`);
  await page.getByText(/Template bound/).first().waitFor({ timeout: 15000 });
  await page.waitForTimeout(800);
  const baseRow = page.getByTestId("sched-base");
  console.log(`next ${id} sched-base=${(await baseRow.count()) ? JSON.stringify(await baseRow.innerText()) : "(no Base row: default branch)"}`);
  const dl = await page.locator("dl").allInnerTexts();
  console.log(`next ${id} detail=${JSON.stringify(dl.join(" | ").replace(/\s+/g, " ").slice(0, 400))}`);
  const skips = (await page.getByTestId("sched-skip-outcome").allInnerTexts()).map((s) => s.trim());
  if (skips.length) console.log(`next ${id} skips=${JSON.stringify(skips)}`);
  const sup = await page.getByText(/Suppressed —/).allInnerTexts();
  if (sup.length) console.log(`next ${id} suppression=${JSON.stringify(sup)}`);
  await page.screenshot({ path: `${out}-next-${id}.png`, fullPage: true });
}
await page.goto(`${base}/schedules`);
await page.waitForTimeout(2500);
const body = await page.locator("body").innerText();
for (const line of body.split("\n").filter((l) => /release|default|stale|widgets|Template bound/i.test(l))) console.log(`classic: ${line}`);
await page.screenshot({ path: `${out}-classic.png`, fullPage: true });
console.log(`errors=${JSON.stringify(log)}`);
await browser.close();
