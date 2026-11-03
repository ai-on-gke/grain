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

// disable-no-reload.mjs <url> <code> <server-id> <out.png>: open Settings ->
// Capabilities over a running grain demo, Disable one server from its
// page, go back, and wait (no reload) for its row to say disabled (#453).
// Then Enable it again. Run from ui/ so playwright resolves.
import { chromium } from "@playwright/test";

const [url, code, id, out] = process.argv.slice(2);
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
await page.setExtraHTTPHeaders({ Authorization: `Bearer ${code}` });
let reloads = 0;
page.on("framenavigated", (f) => { if (f === page.mainFrame()) reloads++; });
await page.goto(`${url}/settings/capabilities#capability-servers`);
const row = page.locator(`[data-capability-server-row="${id}"]`);
await row.waitFor();
console.log("before:", await row.getByTestId("capability-server-state").textContent());
await row.click();
const card = page.locator(`[data-capability-server="${id}"]`);
const started = Date.now();
await card.getByRole("button", { name: "Disable" }).click();
await page.getByTestId("capability-servers-back").click();
await row.getByTestId("capability-server-state").filter({ hasText: "disabled" }).waitFor({ timeout: 20000 });
console.log(`after Disable: ${await row.getByTestId("capability-server-state").textContent()} in ${Date.now() - started} ms, page loads ${reloads}`);
await page.screenshot({ path: out, fullPage: false });
await row.click();
await card.getByRole("button", { name: "Enable" }).click();
await page.getByTestId("capability-servers-back").click();
await row.getByTestId("capability-server-state").filter({ hasNotText: "disabled" }).waitFor({ timeout: 20000 });
console.log("after Enable:", await row.getByTestId("capability-server-state").textContent());
await browser.close();
