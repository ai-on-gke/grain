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

// state-ui.mjs: Settings -> State (the "config repo" tab, /settings/config-repo) on the walkthrough daemon, with the
// secrets key wrapped by the long-named fake KMS key, at 1440x900 and
// 390x844 in light and dark. For each it screenshots the page and checks
// that the protection, the key name and the last unwrap are shown, that
// the key name wraps inside its container and the page does not scroll
// sideways. Run from ui/ (for @playwright/test):
//
//   DAEMON=http://127.0.0.1:4271 CODE=<access code> KMS_KEY=<key name> OUT=dir node state-ui.mjs
import { chromium } from "@playwright/test";

const { DAEMON, CODE, KMS_KEY, OUT } = process.env;
const browser = await chromium.launch();
const results = [];
for (const [w, h] of [[1440, 900], [390, 844]]) {
  for (const theme of ["light", "dark"]) {
    const context = await browser.newContext({
      viewport: { width: w, height: h },
      colorScheme: theme,
      extraHTTPHeaders: { Authorization: `Bearer ${CODE}` },
    });
    const page = await context.newPage();
    await page.goto(`${DAEMON}/settings/config-repo`);
    await page.waitForLoadState("networkidle", { timeout: 15_000 }).catch(() => {});
    await page.getByText(KMS_KEY, { exact: false }).first().waitFor({ timeout: 10_000 }).catch(() => {});
    const key = page.getByText(KMS_KEY, { exact: false }).first();
    const found = await key.count();
    let box = null;
    let container = null;
    if (found) {
      await key.scrollIntoViewIfNeeded();
      box = await key.boundingBox();
      container = await key.evaluate((el) => {
        const p = el.parentElement.getBoundingClientRect();
        return { left: p.left, right: p.right };
      });
    }
    const page_ = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
      text: document.body.innerText,
    }));
    const r = {
      size: `${w}x${h}`,
      theme,
      keyNameShown: found > 0,
      keyNameInsideViewport: box ? box.x >= 0 && box.x + box.width <= w + 0.5 : null,
      keyNameInsideContainer: box && container ? box.x + box.width <= container.right + 0.5 : null,
      keyNameLines: box ? Math.round(box.height / 16) : null,
      noSideScroll: page_.scrollWidth <= page_.innerWidth,
      mentionsKMS: /kms/i.test(page_.text),
      mentionsUnwrap: /unwrap/i.test(page_.text),
    };
    results.push(r);
    await page.screenshot({ path: `${OUT}/state-${w}-${theme}.png`, fullPage: true });
    if (found) await key.screenshot({ path: `${OUT}/state-key-${w}-${theme}.png` }).catch(() => {});
    await context.close();
  }
}
await browser.close();
console.log(JSON.stringify(results, null, 1));
