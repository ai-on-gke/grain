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

// inbox-approve.mjs URL CODE VERSION OUT_DIR: §2 item 1's Inbox step,
// against the live `grain demo -github-sim` start-demo.sh started. Opens
// the Inbox as the person the access code signs in, screenshots the
// release rows, presses VERSION's Approve, accepts the confirm, and
// writes what the page saw and the sim was sent to OUT_DIR. Run from ui/
// (for @playwright/test) with PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright.
import fs from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { signIn } from "../../../ui/e2e/accesscode.js";

// @playwright/test from ui/node_modules: this file lives outside ui/.
const { chromium } = createRequire(path.join(process.cwd(), "package.json"))(
  "@playwright/test",
);

const [url, code, version, out] = process.argv.slice(2);
const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  storageState: await signIn(url, code),
});
const page = await context.newPage();
await page.goto(`${url}/inbox`);
const section = page.getByTestId("inbox-notifications");
await section.waitFor();
await section.locator(".inbox-group-toggle").click();
const rows = section
  .getByTestId("inbox-notification")
  .filter({ hasText: "is ready to promote" });
await rows.first().waitFor();
const titles = await rows.allInnerTexts();
await page.screenshot({
  path: path.join(out, `inbox-before-approve-${version}.png`),
});
const row = rows.filter({ hasText: `${version} is ready to promote` });
let dialog = "";
page.once("dialog", (d) => {
  dialog = d.message();
  d.accept();
});
const [resp] = await Promise.all([
  page.waitForResponse((r) =>
    r.url().includes(`/pending-releases/${version}/approve`),
  ),
  row.getByRole("button", { name: "Approve", exact: true }).click(),
]);
const body = await resp.json();
await row.waitFor({ state: "detached" });
await page.screenshot({
  path: path.join(out, `inbox-after-approve-${version}.png`),
});
const report = {
  rowsBefore: titles.map((t) => t.split("\n")[0]),
  confirm: dialog,
  approveStatus: resp.status(),
  approveResponse: body,
  rowGoneAfterApprove: (await row.count()) === 0,
};
await fs.writeFile(
  path.join(out, `inbox-approve-${version}.json`),
  JSON.stringify(report, null, 2) + "\n",
);
console.log(JSON.stringify(report, null, 2));
await browser.close();
