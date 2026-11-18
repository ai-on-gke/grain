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

// Item 4: a task comment and a memory document carrying a mermaid diagram
// whose %%{init}%% tries to pin theme, themeVariables, themeCSS, fonts and
// a loose securityLevel, with an onerror label and a javascript: click.
// In light and dark, the diagram must take the page's palette and no
// script may run.
import { chromium, session, watch } from "./lib.mjs";

const base = process.env.BASE, code = process.env.CODE, tid = process.env.TID;
const browser = await chromium.launch();
let fails = 0;
for (const theme of ["light", "dark"]) {
  for (const where of ["comment", "doc"]) {
    const ctx = await session(browser, base, code, { theme });
    const page = await ctx.newPage();
    const log = watch(page);
    if (where === "comment") {
      await page.goto(`${base}/next/task/${tid}`);
    } else {
      await page.goto(`${base}/next/context?tab=documents`);
      await page.getByText("Memory Documents (Packs)").first().click().catch(() => {});
      await page.getByTestId("doc-row-hostile-mermaid").click();
    }
    const svg = page.locator("[data-mermaid-canvas] svg").filter({ hasText: "click me" }).first();
    await svg.waitFor({ timeout: 20000 });
    await svg.scrollIntoViewIfNeeded();
    // Try the click handler too.
    await svg.getByText("click me").click({ force: true }).catch(() => {});
    await page.waitForTimeout(500);
    const r = await svg.evaluate((el) => {
      const node = el.querySelector(".node rect, .node polygon, rect.basic");
      const text = el.querySelector("text");
      const bg = getComputedStyle(el).backgroundColor;
      return {
        nodeFill: node ? getComputedStyle(node).fill : null,
        textFill: text ? getComputedStyle(text).fill : null,
        svgBackground: bg,
        font: text ? getComputedStyle(text).fontFamily : null,
        foreignObjects: el.querySelectorAll("foreignObject").length,
        imgs: el.querySelectorAll("img, image").length,
        scripts: el.querySelectorAll("script").length,
        onAttrs: [...el.querySelectorAll("*")].flatMap((n) => [...n.attributes].filter((a) => /^on/i.test(a.name) || /javascript:/i.test(a.value)).map((a) => `${n.tagName}.${a.name}`)),
        styleHasHostile: [...el.querySelectorAll("style")].some((s) => /#ff00ff|#00ff00|Comic Sans/i.test(s.textContent)),
        pwned: window.__pwned ?? null,
      };
    });
    const pageBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    const hostile = /255, 0, 255|0, 255, 0/.test(`${r.nodeFill} ${r.textFill} ${r.svgBackground}`) || /Comic Sans/i.test(r.font || "") || r.styleHasHostile;
    const ok = !hostile && r.pwned === null && r.scripts === 0 && r.onAttrs.length === 0 && r.foreignObjects === 0 && r.imgs === 0;
    if (!ok) fails++;
    console.log(`${ok ? "PASS" : "FAIL"} ${theme} ${where}: ${JSON.stringify({ ...r, pageBg })} errors=${JSON.stringify(log.pageErrors)}`);
    await page.screenshot({ path: `/tmp/item4-${theme}-${where}.png` });
    await svg.screenshot({ path: `/tmp/item4-${theme}-${where}-svg.png` });
    await ctx.close();
  }
}
console.log(`summary: ${fails} failing`);
await browser.close();
