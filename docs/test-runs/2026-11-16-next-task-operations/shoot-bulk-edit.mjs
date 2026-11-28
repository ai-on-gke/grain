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


// Shoots the writer's Bulk edit dialog with priority 9 typed, at 1440 and
// 390 px in light and dark, into $SHOTS (before/after the priority cap):
//   SHOTS=/home/debian/work/screenshots/after node shoot-bulk-edit.mjs
import fs from "node:fs";
import path from "node:path";
import { chromium, as, seeded, row, tick, DESKTOP, PHONE } from "./lib.mjs";

const dir = process.env.SHOTS;
fs.mkdirSync(dir, { recursive: true });
const browser = await chromium.launch();
const ids = Object.keys(seeded).filter((k) => /^Q\d+$/.test(k)).sort().map((k) => seeded[k]).slice(-2);
for (const [vp, w] of [[DESKTOP, 1440], [PHONE, 390]]) {
  for (const scheme of ["light", "dark"]) {
    const { ctx, page } = await as(browser, "wendy@example.com", vp, scheme);
    await page.addInitScript((m) => localStorage.setItem("grain.themeMode", m), scheme);
    await page.goto("tasks?tab=backlog");
    await row(page, ids[0]).waitFor({ timeout: 15000 });
    for (const id of ids) await tick(page, id).check();
    await page.getByTestId("batch-bar").getByTestId("batch-more").click();
    await page.getByRole("option", { name: /^Bulk edit/ }).click();
    const form = page.getByTestId("batch-edit-dialog");
    await form.getByTestId("bulk-priority").fill("9");
    await page.waitForTimeout(300);
    const apply = form.getByTestId("batch-edit-apply");
    console.log(`${w} ${scheme}: apply disabled=${await apply.isDisabled()} error=${JSON.stringify(await form.getByTestId("bulk-priority-error").innerText().catch(() => null))}`);
    await page.screenshot({ path: path.join(dir, `next-tasks-bulk-edit-writer-${w}-${scheme}.png`) });
    await ctx.close();
  }
}
await browser.close();
