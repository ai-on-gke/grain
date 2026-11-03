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

// A layout sweep standing in for looking at each screenshot: for every
// page §11 names, at 1440×900 and 390×844 in light and dark, records
// whether the document scrolls sideways and every visible text element
// or control that is cut off by the window's left or right edge.
//
//   cd ui && node ../docs/test-runs/2026-11-03-ui-fixes/layout.mjs \
//     --url http://127.0.0.1:18080 --code <access code> --pages <a,b> --out <file.json>
import fs from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";

const require = createRequire(path.resolve("package.json"));
const { chromium } = require("playwright");
const { signIn } = await import(
  pathToFileURL(path.resolve("e2e/accesscode.js")).href
);

const arg = (name) => {
  const i = process.argv.indexOf(`--${name}`);
  return i < 0 ? undefined : process.argv[i + 1];
};
const url = arg("url");
const pages = arg("pages").split(",");
const out = arg("out") || "layout.json";

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: {
    viewport: { width: 390, height: 844 },
    isMobile: true,
    hasTouch: true,
  },
};

const { cookies } = await signIn(url, arg("code"));
const browser = await chromium.launch();
const rows = [];
try {
  for (const p of pages) {
    for (const [name, opts] of Object.entries(sizes)) {
      for (const theme of ["light", "dark"]) {
        const context = await browser.newContext({
          ...opts,
          colorScheme: theme,
        });
        await context.addCookies(cookies);
        await context.addInitScript(
          (t) => localStorage.setItem("grain.themeMode", t),
          theme,
        );
        const tab = await context.newPage();
        await tab.goto(new URL(p, url).href);
        await tab
          .waitForLoadState("networkidle", { timeout: 10_000 })
          .catch(() => {});
        await tab.waitForTimeout(500);
        const found = await tab.evaluate(() => {
          const W = window.innerWidth;
          const cut = [];
          for (const el of document.querySelectorAll(
            "button, a, input, [role=button], h1, h2, h3, p, span, td, th, label",
          )) {
            const r = el.getBoundingClientRect();
            if (!r.width || !r.height) continue;
            const cs = getComputedStyle(el);
            if (cs.visibility === "hidden" || Number(cs.opacity) === 0)
              continue;
            // Inside something that scrolls sideways on purpose (a
            // table, a tab strip, the gantt) is not cut off.
            let scroller = false;
            for (let a = el.parentElement; a; a = a.parentElement) {
              const o = getComputedStyle(a).overflowX;
              if (
                (o === "auto" || o === "scroll") &&
                a.scrollWidth > a.clientWidth
              ) {
                scroller = true;
                break;
              }
              if (o === "hidden" || o === "clip") {
                const ar = a.getBoundingClientRect();
                if (r.right > ar.right + 1 || r.left < ar.left - 1) {
                  scroller = true;
                  break;
                }
              }
            }
            if (scroller) continue;
            if (r.left < -1 || r.right > W + 1) {
              cut.push({
                tag: el.tagName.toLowerCase(),
                text: (el.getAttribute("aria-label") || el.textContent || "")
                  .trim()
                  .slice(0, 60),
                left: Math.round(r.left),
                right: Math.round(r.right),
              });
            }
          }
          return {
            sideways: document.documentElement.scrollWidth > W + 1,
            cut: cut.slice(0, 10),
          };
        });
        rows.push({ page: p, width: name, theme, ...found });
        if (found.sideways || found.cut.length) {
          console.log(
            `${p} ${name} ${theme}: sideways=${found.sideways} cut=${found.cut.length}`,
          );
        }
        await context.close();
      }
    }
  }
} finally {
  await browser.close();
}
await fs.writeFile(out, JSON.stringify(rows, null, 2) + "\n");
console.log(
  `${rows.length} shots, ${rows.filter((r) => r.sideways || r.cut.length).length} with something cut off -> ${out}`,
);
