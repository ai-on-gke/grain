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

// §4 item 1: the Director settings pane on a live daemon, at 1440x900 and
// 390x844. Signs in with the access code, opens /session/director's
// capabilities section and reports, per width: the network select's value,
// whether the non-isolated warning is shown, the adopt banner, and that
// both sit inside the viewport with no horizontal overflow.
//
//   cd ui && node ../docs/test-runs/2026-11-07-director-agent-api/ui_director_layout.mjs URL CODE
import { chromium } from "@playwright/test";
import { signIn } from "../../../ui/e2e/accesscode.js";

const [url, code] = process.argv.slice(2);
const browser = await chromium.launch();
const out = [];
try {
  for (const [name, width, height] of [
    ["desktop", 1440, 900],
    ["phone", 390, 844],
  ]) {
    const context = await browser.newContext({ viewport: { width, height } });
    await context.addCookies((await signIn(url, code)).cookies);
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(`${url}/session/director#director-capabilities-content`);
    const select = page.locator('select[name="networkAccess"]');
    const toggle = page.getByRole("button", { name: /Director capabilities/ });
    await toggle.first().waitFor({ timeout: 15000 });
    if (!(await select.isVisible())) await toggle.first().click();
    await select.waitFor({ timeout: 15000 });
    await select.scrollIntoViewIfNeeded();
    const warning = page.getByTestId("session-network-not-isolated");
    const banner = page.getByTestId("adopt-shipped-defaults-banner");
    const box = async (loc) =>
      (await loc.count()) ? await loc.first().boundingBox() : null;
    const sel = await box(select);
    const warn = await box(warning);
    const scrollWidth = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    out.push({
      width: name,
      networkSelect: await select.inputValue(),
      warningShown: (await warning.count()) > 0,
      warningText: (await warning.count())
        ? (await warning.first().innerText()).trim()
        : "",
      warningBelowSelect: warn && sel ? warn.y >= sel.y + sel.height - 1 : null,
      warningInsideViewport: warn
        ? warn.x >= 0 && warn.x + warn.width <= width
        : null,
      adoptBannerShown: (await banner.count()) > 0,
      horizontalOverflow: scrollWidth > width,
      pageErrors: errors,
    });
    if (process.env.SHOT_DIR) {
      await (warn ? warning.first() : select).scrollIntoViewIfNeeded();
      await page.screenshot({
        path: `${process.env.SHOT_DIR}/director-network-${name}.png`,
      });
    }
    await context.close();
  }
} finally {
  await browser.close();
}
console.log(JSON.stringify(out, null, 2));
