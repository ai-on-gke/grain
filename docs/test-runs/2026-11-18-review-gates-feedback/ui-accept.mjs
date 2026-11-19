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

// Accepts a targeted review's findings through a real browser on a live
// daemon (docs/test-plans/2026-11-18.md §3 item 1), or shoots the owner's
// Inbox (item 4). Prints one JSON line of what it saw.
//
//   GRAIN_URL=... GRAIN_CODE=... OUT=... MODE=next|classic|inbox \
//   WIDTH=1440|390 TASK=<id> REASON=... node ui-accept.mjs   (from ui-ng/)

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const { GRAIN_URL: BASE, GRAIN_CODE: CODE, OUT, MODE, TASK } = process.env;
const WIDTH = Number(process.env.WIDTH || 1440);
const REASON = process.env.REASON || "merge as-is: the amount is validated upstream";
fs.mkdirSync(OUT, { recursive: true });

const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: { width: WIDTH, height: WIDTH > 600 ? 900 : 844 },
});
await ctx.setExtraHTTPHeaders({ Authorization: `Bearer ${CODE}` });
const page = await ctx.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
const out = { mode: MODE, width: WIDTH, task: TASK };
const shot = (name) =>
  page.screenshot({ path: path.join(OUT, `${MODE}-${WIDTH}-${TASK}-${name}.png`), fullPage: false });

try {
  if (MODE === "inbox") {
    await page.goto(`${BASE}/next/inbox`);
    await page.waitForLoadState("networkidle");
    const item = page.getByText("Review needs your decision", { exact: false }).first();
    await item.waitFor({ timeout: 20000 });
    out.inboxShowsDecision = await item.isVisible();
    await item.click().catch(() => {});
    await page.waitForTimeout(1000);
    out.inboxText = (await page.locator("body").innerText()).slice(0, 4000);
    await shot("inbox");
  } else {
    const next = MODE === "next";
    await page.goto(next ? `${BASE}/next/task/${TASK}?tab=changes` : `${BASE}/tasks/${TASK}`);
    const button = page.getByTestId("accept-findings-targeted-review");
    await button.waitFor({ timeout: 30000 });
    await button.scrollIntoViewIfNeeded();
    const b = await button.boundingBox();
    out.buttonBox = b;
    out.buttonOnScreen = b.x >= 0 && b.x + b.width <= WIDTH + 1;
    await shot("before");
    await button.click();
    const dialog = page.getByTestId("accept-findings-dialog");
    await dialog.waitFor();
    const d = await dialog.boundingBox();
    out.dialogFits = d.x >= 0 && d.x + d.width <= WIDTH + 1;
    out.dialogText = (await dialog.innerText()).slice(0, 1500);
    await dialog.getByTestId("accept-findings-reason").fill(REASON);
    await shot("dialog");
    const confirm = dialog.getByTestId(next ? "accept-findings-confirm" : "confirm-accept-findings");
    await confirm.click();
    await dialog.waitFor({ state: "hidden", timeout: 15000 });
    await expectGone(button);
    out.buttonGoneAfter = true;
    await page.waitForTimeout(800);
    await shot("after");
  }
} catch (e) {
  out.error = String(e).slice(0, 800);
  await shot("error").catch(() => {});
}
out.pageErrors = errors;
console.log(JSON.stringify(out));
await browser.close();

async function expectGone(loc) {
  for (let i = 0; i < 40; i++) {
    if ((await loc.count()) === 0) return;
    await page.waitForTimeout(500);
  }
  throw new Error("Accept findings button still shown after accepting");
}
