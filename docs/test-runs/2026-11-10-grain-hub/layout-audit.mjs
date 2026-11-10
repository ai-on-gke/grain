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

// Read-only geometry audit beside the §7 screenshots (the sandbox cannot
// look at a PNG): for each page at 1440 and 390 px in light and dark, the
// page must not scroll sideways, and the My Entries delete dialog (opened,
// measured and cancelled, never confirmed) must sit inside the viewport with
// its explanatory paragraph unclipped. Run from ui-hub/ like ui-walk.mjs.
import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const pages = [
  ["http://127.0.0.1:4281", "/"],
  ["http://127.0.0.1:4281", "/new"],
  ["http://127.0.0.1:4281", "/new?kind=connected"],
  ["http://127.0.0.1:4281", "/entries/walk-creating"],
  ["http://127.0.0.1:4284", "/admin"],
  ["http://127.0.0.1:4235", "/new"],
];
const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};
let failures = 0;
const browser = await chromium.launch();
try {
  for (const [width, viewport] of Object.entries(VIEWPORTS)) {
    for (const theme of ["light", "dark"]) {
      const context = await browser.newContext({
        viewport,
        colorScheme: theme,
        isMobile: width === "phone",
        hasTouch: width === "phone",
      });
      await context.addInitScript(
        (t) => localStorage.setItem("grain.themeMode", t),
        theme,
      );
      const page = await context.newPage();
      for (const [base, route] of pages) {
        await page.goto(base + route);
        await page
          .waitForLoadState("networkidle", { timeout: 10_000 })
          .catch(() => {});
        const m = await page.evaluate(() => ({
          sw: document.documentElement.scrollWidth,
          cw: document.documentElement.clientWidth,
          bg: getComputedStyle(document.body).backgroundColor,
        }));
        const ok = m.sw <= m.cw;
        if (!ok) failures++;
        console.log(
          `${ok ? "PASS" : "FAIL"} ${width}/${theme} ${base.slice(-4)}${route} scrollWidth=${m.sw} clientWidth=${m.cw} body=${m.bg}`,
        );
      }
      await page.goto("http://127.0.0.1:4281/");
      const row = page.getByRole("row", { name: /act-plain/ });
      await row
        .getByRole("button", { name: /^Delete/ })
        .click({ timeout: 10_000 });
      const dialog = page.getByRole("dialog");
      await dialog.waitFor();
      const box = await dialog.boundingBox();
      const para = await dialog
        .locator("p")
        .first()
        .evaluate((el) => ({
          sh: el.scrollHeight,
          ch: el.clientHeight,
          sw: el.scrollWidth,
          cw: el.clientWidth,
        }));
      const inside =
        box &&
        box.x >= 0 &&
        box.y >= 0 &&
        box.x + box.width <= viewport.width &&
        box.y + box.height <= viewport.height;
      const unclipped = para.sw <= para.cw && para.sh <= para.ch + 1;
      if (!inside || !unclipped) failures++;
      console.log(
        `${inside && unclipped ? "PASS" : "FAIL"} ${width}/${theme} delete dialog box=${Math.round(box.x)},${Math.round(box.y)} ${Math.round(box.width)}x${Math.round(box.height)} paragraph scroll=${para.sw}x${para.sh} client=${para.cw}x${para.ch}`,
      );
      await page.keyboard.press("Escape");
      await context.close();
    }
  }
} finally {
  await browser.close();
}
console.log(
  failures ? `${failures} check(s) FAILED` : "all layout checks passed",
);
process.exit(failures ? 1 : 0);
