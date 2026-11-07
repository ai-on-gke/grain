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

// page-text.mjs URL CODE PATH...: signs in with the access code, loads each
// path at 1440 and 390 px, and prints the lines of its rendered text that
// mention a deprecation, a removal or a retired name -- what the
// screenshots of §8 item 5 show, in a form a report can quote.
//
// Copy it into ui/e2e first (it imports ./accesscode.js from there):
//   cp docs/test-runs/2026-11-04-capabilities/page-text.mjs ui/e2e/ && cd ui &&
//     PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node e2e/page-text.mjs \
//     http://127.0.0.1:7812 "$(cat .../local-api-token)" /settings/capabilities /settings/models
import { chromium } from "playwright";
import { signIn } from "./accesscode.js";

const [url, code, ...paths] = process.argv.slice(2);
const pattern =
  /deprecat|retired|removed|alias|custom command|in.process|one release|replace/i;
const browser = await chromium.launch();
for (const width of [1440, 390]) {
  const context = await browser.newContext({
    viewport: { width, height: 900 },
  });
  const { cookies } = await signIn(url, code);
  await context.addCookies(cookies);
  const page = await context.newPage();
  for (const path of paths) {
    await page.goto(`${url}${path}`);
    await page.waitForTimeout(4000); // the UI polls, so the network is never idle
    const text = await page.locator("body").innerText();
    const lines = [
      ...new Set(
        text
          .split("\n")
          .map((l) => l.trim())
          .filter((l) => l && pattern.test(l)),
      ),
    ];
    console.log(`== ${path} @ ${width}px: ${lines.length} matching line(s)`);
    for (const l of lines) console.log(`   ${l.slice(0, 400)}`);
  }
  await context.close();
}
await browser.close();
