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

// Reads each review task's page on /next/tasks/<id> at 1440x900 and reports
// how the description rendered: the <pre> blocks holding the brief, and every
// heading whose text names a grain review section -- so a fenced brief that
// rendered as live headings shows up as a heading here.
//
// It resolves @playwright/test from ui/node_modules, so run a copy from ui/e2e:
//
//   cd ui && sed 's#"../../../ui-shared/e2e/accesscode.js"#"./accesscode.js"#' \
//     ../docs/test-runs/2026-11-15-review-brief/brief_dom.mjs >e2e/_brief_dom.mjs &&
//   PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node e2e/_brief_dom.mjs URL CODE ID...;
//   rm e2e/_brief_dom.mjs

import { chromium } from "@playwright/test";
import { signIn } from "../../../ui-shared/e2e/accesscode.js";

const [url, code, ...ids] = process.argv.slice(2);
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const out = [];
try {
  for (const id of ids) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    await context.addCookies(cookies);
    const page = await context.newPage();
    await page.goto(`${url}/next/tasks/${id}`, { waitUntil: "networkidle" });
    await page.getByText("The change under review").first().waitFor({ timeout: 20000 });
    const report = await page.evaluate(() => {
      const names = [
        "The change under review",
        "Earlier reviews of this pull request",
        "Parallel reviews of this head",
        "Requirements",
      ];
      const headings = [...document.querySelectorAll("h1,h2,h3,h4,h5,h6")]
        .filter((h) => names.some((n) => h.textContent.includes(n)))
        .map((h) => `${h.tagName}: ${h.textContent.trim().slice(0, 60)}`);
      const pres = [...document.querySelectorAll("pre")].map((p) => ({
        lines: p.textContent.split("\n").length,
        first: p.textContent.split("\n")[0].slice(0, 60),
        quotesEarlier: p.textContent.includes("Earlier reviews of this pull request"),
        quotesIncremental: p.textContent.includes("This is an incremental review"),
        width: Math.round(p.getBoundingClientRect().width),
        overflowX: getComputedStyle(p).overflowX,
      }));
      return { headings, pres, docScrollWidth: document.documentElement.scrollWidth };
    });
    out.push({ id, ...report });
    await context.close();
  }
} finally {
  await browser.close();
}
console.log(JSON.stringify(out, null, 1));
