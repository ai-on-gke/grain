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

// §4 item 1 (UI half of Adopt shipped defaults) on a live daemon, at
// 1440x900 and 390x844: save a pre-#806 Director row over REST, open the
// session pane, check the banner and its button sit inside the viewport,
// click Adopt shipped defaults, and read the row back.
//
//   cd ui && node ../docs/test-runs/2026-11-07-director-agent-api/ui_adopt.mjs URL CODE
import { chromium } from "@playwright/test";
import { signIn } from "../../../ui/e2e/accesscode.js";

const [url, code] = process.argv.slice(2);
const headers = {
  "X-Grain-Access-Code": code,
  "Content-Type": "application/json",
};
const OLD = [
  "api:ci-read",
  "api:deployment:read",
  "api:self-debug",
  'https://grain.dev/capabilities/config-map#config-map={"config_maps":["bootstrap"]}',
  "https://grain.dev/capabilities/grain#health={}",
  "https://grain.dev/capabilities/grain#journal={}",
  "https://grain.dev/capabilities/grain#source={}",
  "https://grain.dev/capabilities/github#read-issues={}",
];
const director = async () =>
  (
    await (await fetch(`${url}/api/console-sessions`, { headers })).json()
  ).sessions.find((s) => s.id === "director");

const browser = await chromium.launch();
const out = [];
try {
  for (const [name, width, height] of [
    ["desktop", 1440, 900],
    ["phone", 390, 844],
  ]) {
    await fetch(`${url}/api/console-sessions/director`, {
      method: "PUT",
      headers,
      body: JSON.stringify({ capabilities: OLD }),
    });
    const before = await director();
    const context = await browser.newContext({ viewport: { width, height } });
    await context.addCookies((await signIn(url, code)).cookies);
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(`${url}/session/director#director-capabilities-content`);
    const toggle = page.getByRole("button", { name: /Director capabilities/ });
    await toggle.first().waitFor({ timeout: 15000 });
    const banner = page.getByTestId("adopt-shipped-defaults-banner");
    if (!(await banner.isVisible())) await toggle.first().click();
    await banner.waitFor({ timeout: 15000 });
    await banner.scrollIntoViewIfNeeded();
    const button = banner.getByRole("button", {
      name: "Adopt shipped defaults",
    });
    const bb = await banner.boundingBox();
    const btn = await button.boundingBox();
    const inside = (b) => b && b.x >= 0 && b.x + b.width <= width + 0.5;
    if (process.env.SHOT_DIR) {
      await page.screenshot({
        path: `${process.env.SHOT_DIR}/adopt-banner-${name}.png`,
      });
    }
    await button.click();
    await banner.waitFor({ state: "detached", timeout: 15000 });
    const after = await director();
    out.push({
      width: name,
      missingBefore: before.missingShippedDefaults?.length ?? 0,
      bannerInsideViewport: inside(bb),
      buttonInsideBanner:
        !!bb &&
        !!btn &&
        btn.x >= bb.x &&
        btn.x + btn.width <= bb.x + bb.width + 0.5,
      missingAfter: after.missingShippedDefaults?.length ?? 0,
      customExtraKept: after.capabilities.includes(
        "https://grain.dev/capabilities/github#read-issues={}",
      ),
      heldAfter: after.capabilities.length,
      horizontalOverflow:
        (await page.evaluate(() => document.documentElement.scrollWidth)) >
        width,
      pageErrors: errors,
    });
    await context.close();
  }
} finally {
  await browser.close();
}
console.log(JSON.stringify(out, null, 2));
