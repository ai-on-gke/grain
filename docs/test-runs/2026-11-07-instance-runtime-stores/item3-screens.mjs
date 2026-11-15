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

// §2 item 3 screens: Settings -> General (Updates card) and Settings ->
// Upgrade (external-management notice + read-only Database Backend) on
// the live -disable-self-upgrade daemon item3-self-upgrade.sh leaves on
// 127.0.0.1:4300, at 1440x900 and 390x844, light and dark.
//
//   cd ui && node ../docs/test-runs/2026-11-07-instance-runtime-stores/item3-screens.mjs
import { chromium } from "playwright";
import fs from "node:fs";

const W = process.env.W || "/tmp/walk";
const BASE = "http://127.0.0.1:4300";
const OUT = process.env.OUT || `${W}/screens`;
const MGMT = "https://registry.example.test/instances/acme";
const TOK = fs.readFileSync(`${W}/i3/local-api-token`, "utf8").trim();
fs.mkdirSync(OUT, { recursive: true });

let failures = 0;
const check = (ok, what) => {
  console.log(`${ok ? "PASS" : "FAIL"} ${what}`);
  if (!ok) failures++;
};

const browser = await chromium.launch();
for (const [vname, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({
      viewport,
      colorScheme: scheme,
      extraHTTPHeaders: { Authorization: `Bearer ${TOK}` },
    });
    const page = await ctx.newPage();
    const tag = `${vname}-${scheme}`;

    await page.goto(`${BASE}/settings/general#updates`);
    const updates = page.getByTestId("updates-externally-managed");
    await updates.waitFor({ timeout: 20000 });
    await updates.scrollIntoViewIfNeeded();
    await page.waitForTimeout(500);
    check(
      (await page.getByTestId("updates-management-link").getAttribute("href")) === MGMT,
      `${tag} general: Updates card links to ${MGMT}`,
    );
    check(
      (await page.locator('[data-setting-id="autoUpgradeOn"]').count()) === 0,
      `${tag} general: no automatic-upgrade fields`,
    );
    let overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    check(overflow <= 0, `${tag} general: no horizontal overflow (${overflow}px)`);
    await page.screenshot({ path: `${OUT}/general-${tag}.png` });

    await page.goto(`${BASE}/settings/upgrade`);
    const notice = page.getByTestId("upgrade-externally-managed");
    await notice.waitFor({ timeout: 20000 });
    check(
      (await page.getByTestId("upgrade-management-link").getAttribute("href")) === MGMT,
      `${tag} upgrade: notice links to ${MGMT}`,
    );
    check(
      (await page.getByTestId("upgrade-section").getByRole("button", { name: /^Upgrade$/ }).count()) === 0,
      `${tag} upgrade: no Upgrade button in the upgrade section (the settings nav entry is a link-button named Upgrade)`,
    );
    const db = page.locator(".database-backend-section");
    await db.waitFor({ timeout: 20000 });
    const dbText = await db.innerText();
    check(/PostgreSQL/.test(dbText), `${tag} upgrade: Database Backend says PostgreSQL`);
    check(!dbText.includes("pgpass-walk"), `${tag} upgrade: Database Backend DSN carries no password`);
    check(
      (await db.getByRole("button").count()) === 0 && (await db.getByRole("textbox").count()) === 0,
      `${tag} upgrade: Database Backend is read-only (no buttons or fields)`,
    );
    const nb = await notice.boundingBox();
    const lb = await page.getByTestId("upgrade-management-link").boundingBox();
    check(lb.x >= nb.x && lb.x + lb.width <= nb.x + nb.width + 1, `${tag} upgrade: link stays inside the notice`);
    overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    check(overflow <= 0, `${tag} upgrade: no horizontal overflow (${overflow}px)`);
    console.log(`  ${tag} database card: ${dbText.replace(/\s+/g, " ").trim()}`);
    await page.screenshot({ path: `${OUT}/upgrade-${tag}.png`, fullPage: true });
    await ctx.close();
  }
}
await browser.close();
console.log(failures ? `${failures} check(s) failed` : "all checks passed");
process.exit(failures ? 1 : 0);
