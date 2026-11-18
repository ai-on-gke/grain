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

// Walkthrough for docs/test-plans/2026-11-16.md §6 item 1: posts the
// hostile Mermaid corpus (corpus.json) to a task on a running `grain demo`
// and drives /next/task/<id> (and the classic /tasks/<id> as a cross-check)
// at 1440 and 390 px, light and dark, with a live theme switch. Every
// console message, page error, dialog and CSP violation is recorded; any
// dialog or a `window.__pwned` flag set by a diagram is a script that ran.
//
// GRAIN_URL=http://127.0.0.1:4231 GRAIN_CODE=... OUT=/tmp/md-shots \
//   node mermaid-walk.mjs
// (run from ui-ng/ so @playwright/test resolves).

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const BASE = process.env.GRAIN_URL;
const CODE = process.env.GRAIN_CODE;
const OUT = process.env.OUT || "/tmp/md-shots";
const HERE = path.dirname(new URL(import.meta.url).pathname);
fs.mkdirSync(OUT, { recursive: true });

async function api(method, p, body) {
  const res = await fetch(BASE + p, {
    method,
    headers: {
      Authorization: `Bearer ${CODE}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const txt = await res.text();
  if (!res.ok) throw new Error(`${method} ${p}: ${res.status} ${txt}`);
  return txt ? JSON.parse(txt) : null;
}

const corpus = JSON.parse(fs.readFileSync(path.join(HERE, "corpus.json")));
const task = process.env.TASK_ID
  ? { id: process.env.TASK_ID }
  : await api("POST", "/api/tasks", {
      title: "Mermaid hostile corpus walkthrough",
    });
if (!process.env.TASK_ID) {
  for (const [name, body] of Object.entries(corpus)) {
    await api("POST", `/api/tasks/${task.id}/comments`, { body });
    console.log("posted", name);
  }
}

const browser = await chromium.launch();
console.log("chromium", browser.version());
const results = [];
const pages = (process.env.PAGES || `next/task/${task.id},tasks/${task.id}`)
  .split(",")
  .filter(Boolean);
for (const width of [1440, 390]) {
  for (const theme of ["light", "dark"]) {
    for (const route of pages) {
      const ctx = await browser.newContext({
        viewport: { width, height: width > 600 ? 900 : 844 },
        colorScheme: theme,
      });
      await ctx.addInitScript(
        ([t]) => {
          localStorage.setItem("grain.themeMode", t);
          window.__pwned = 0;
          window.alert =
            window.confirm =
            window.prompt =
              () => {
                window.__pwned++;
              };
          document.addEventListener("securitypolicyviolation", (e) =>
            console.log(`CSP ${e.violatedDirective} ${e.blockedURI}`),
          );
        },
        [theme],
      );
      // Sign in the way the shared e2e harness does: with the access code.
      await ctx.setExtraHTTPHeaders({ Authorization: `Bearer ${CODE}` });
      const page = await ctx.newPage();
      const log = [];
      page.on("console", (m) => log.push(`${m.type()}: ${m.text()}`));
      page.on("pageerror", (e) => log.push(`pageerror: ${e.message}`));
      page.on("dialog", (d) => {
        log.push(`DIALOG: ${d.message()}`);
        d.dismiss();
      });
      const t0 = Date.now();
      await page.goto(`${BASE}/${route}`);
      const svgSel = route.startsWith("next/")
        ? ".mermaid-canvas svg"
        : "svg[id^='mermaid'], .mermaid svg, [class*=ermaid] svg";
      await page
        .waitForFunction(
          (sel) =>
            document.querySelectorAll(sel).length >= 7 ||
            (document.querySelectorAll(sel).length >= 6 &&
              /could not be rendered|Syntax error|Parse error/i.test(
                document.body.innerText,
              )),
          svgSel,
          { timeout: 60_000 },
        )
        .catch(() => log.push("timeout waiting for diagrams"));
      const renderMs = Date.now() - t0;
      // Hover and click every node: click callbacks must not fire.
      for (const n of await page.locator(`${svgSel} .node`).all()) {
        await n.click({ timeout: 1000, force: true }).catch(() => {});
      }
      const probe = await page.evaluate((sel) => {
        const svgs = Array.from(document.querySelectorAll(sel));
        const bad = svgs.flatMap((s) =>
          Array.from(
            s.querySelectorAll(
              "script, foreignObject, image, iframe, [onclick], [onerror], [onload]",
            ),
          ).map((e) => e.outerHTML.slice(0, 120)),
        );
        const links = svgs.flatMap((s) =>
          Array.from(s.querySelectorAll("a")).map(
            (a) => a.getAttribute("href") || a.getAttribute("xlink:href"),
          ),
        );
        return {
          svgs: svgs.length,
          bad,
          links,
          pwned: window.__pwned,
          bodyVisible: getComputedStyle(document.body).display !== "none",
          overflow: document.documentElement.scrollWidth - window.innerWidth,
          errors: Array.from(document.querySelectorAll(".mermaid-error")).map(
            (e) => e.textContent,
          ),
          strayBody: Array.from(document.body.children)
            .filter((c) => /mermaid/.test(c.id))
            .map((c) => c.id),
        };
      }, svgSel);
      const slug = `${route.replace(/\W+/g, "_")}-${width}-${theme}`;
      await page.screenshot({ path: `${OUT}/${slug}.png`, fullPage: false });
      // Live theme switch on /next/: diagrams redraw in the other palette.
      let themeSwitch = null;
      if (route.startsWith("next/")) {
        const fillOf = () =>
          page
            .locator(".mermaid-canvas svg .node rect")
            .first()
            .evaluate((el) => getComputedStyle(el).fill)
            .catch(() => null);
        const before = await fillOf();
        await page.locator("body").press("t");
        await page.waitForTimeout(3000);
        const after = await fillOf();
        themeSwitch = { before, after, changed: before !== after };
        await page.screenshot({ path: `${OUT}/${slug}-switched.png` });
      }
      results.push({
        route,
        width,
        theme,
        renderMs,
        ...probe,
        themeSwitch,
        log,
      });
      await ctx.close();
    }
  }
}
await browser.close();
fs.writeFileSync(
  `${OUT}/results.json`,
  JSON.stringify({ task: task.id, results }, null, 1),
);
for (const r of results) {
  console.log(
    `${r.route} ${r.width} ${r.theme}: svgs=${r.svgs} bad=${r.bad.length} pwned=${r.pwned} links=${JSON.stringify(r.links)} overflow=${r.overflow} ms=${r.renderMs} theme=${JSON.stringify(r.themeSwitch)} errors=${JSON.stringify(r.errors)} log=${r.log.filter((l) => !/^(debug|info|log): (?!CSP)/.test(l)).length}`,
  );
}
