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

// pane_text.mjs URL CODE PATH ROW: open PATH on a running grain, click the
// /next/ processes row whose text holds ROW, and print the detail pane's
// rendered text, so the repository access it shows can be compared with
// the read and write tables. Run from ui/ (it needs ui's playwright).
import { chromium } from "playwright";

const [url, code, path, row] = process.argv.slice(2);
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
await page.goto(new URL("/", url).href);
await page.evaluate((c) => localStorage.setItem("grain.accessCode", c), code);
await page.context().setExtraHTTPHeaders({ Authorization: `Bearer ${code}` });
await page.goto(new URL(path, url).href);
await page.locator("button.proc-row", { hasText: row }).first().click();
await page.waitForTimeout(1500);
const text = await page.locator("main").innerText();
console.log(text.replace(/\n{2,}/g, "\n"));
await browser.close();
