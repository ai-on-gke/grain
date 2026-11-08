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

// Screenshots of the live dispatch-down condition in the UI (§8 item 4):
// the deployment alert and System -> Health/Sandboxes, against the real
// daemon on :4293 while its git proxy port is held.
//   cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-07-cli-contexts-gitlogin-liveness/ui-dispatch-blocked.mjs <out-dir> <access-code> <label>
import { chromium } from "playwright";

const [outDir, code, label] = process.argv.slice(2);
const base = "http://127.0.0.1:4293";
const browser = await chromium.launch();
const results = {};
for (const [name, path] of [["home", "/"], ["system", "/system"], ["sandboxes", "/system/sandboxes"]]) {
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
    extraHTTPHeaders: { Authorization: `Bearer ${code}` },
  });
  await page.goto(base + path, { waitUntil: "load" });
  await page.waitForTimeout(4000);
  const text = await page.locator("body").innerText();
  results[name] = {
    dispatchBlocked: /dispatch(ing)? (is )?blocked/i.test(text),
    gitProxy: /git proxy/i.test(text),
    redacted: /redacted/i.test(text),
    leaksSecret: text.includes("127.0.0.1:24576"),
  };
  await page.screenshot({ path: `${outDir}/${label}-${name}.png`, fullPage: false });
  await page.close();
}
console.log(JSON.stringify(results));
await browser.close();
