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

// page_text.mjs prints the visible text around a held schedule on the
// /next/ schedules page and the classic processes page of a running
// daemon, so the suppressed firing can be checked without an image
// viewer. Run from ui/ (for @playwright/test):
//
//   PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node \
//     ../docs/test-runs/2026-11-13-schedules-templates/page_text.mjs URL CODE
import { chromium } from "@playwright/test";
import { signIn } from "../../../ui/e2e/accesscode.js";

const [url, code] = process.argv.slice(2);
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const context = await browser.newContext();
await context.addCookies(cookies);
for (const path of ["/next/processes/schedules", "/processes"]) {
  const page = await context.newPage();
  await page.goto(new URL(path, url).href);
  await page.waitForTimeout(4000);
  if (path.startsWith("/next/")) {
    // The banner with the detail is in the schedule's expanded row.
    await page.getByText("held", { exact: true }).first().click();
    await page.waitForTimeout(1500);
  }
  const text = await page.locator("body").innerText();
  const lines = text.split("\n").filter((l) => /held|suppress|not firing|publishing/i.test(l));
  console.log(`--- ${path}`);
  for (const l of lines.slice(0, 12)) console.log("  " + l.trim());
  await page.close();
}
await browser.close();
