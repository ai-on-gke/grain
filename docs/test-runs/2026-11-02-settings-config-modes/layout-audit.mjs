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

// What this run's agent could not do by eye -- its file reader shows a
// PNG as bytes and the sandbox has no Gemini key for LOOK=1 -- done from
// the DOM instead, over the pages make ui-screenshots shot: for each
// page at 1440 and 390 px, light and dark, it reports
//   - the page scrolling sideways (document wider than the viewport),
//   - visible elements running off the right edge, outside any
//     container that scrolls sideways on purpose,
//   - text clipped by its own box without an ellipsis,
//   - text whose contrast against the background behind it is below 3:1.
// (../2026-11-02-settings-config-modes.md, "What is NOT proved" 4.)
//
//   node layout-audit.mjs <base-url> <bearer-token> <out.json> <path>...
import { chromium } from "playwright";
import fs from "node:fs";

const [BASE, TOK, OUT, ...paths] = process.argv.slice(2);
const browser = await chromium.launch();
const findings = [];
for (const [width, height] of [
  [1440, 900],
  [390, 844],
]) {
  for (const scheme of ["light", "dark"]) {
    const ctx = await browser.newContext({
      extraHTTPHeaders: { Authorization: `Bearer ${TOK}` },
      viewport: { width, height },
      colorScheme: scheme,
    });
    const page = await ctx.newPage();
    for (const path of paths) {
      await page.goto(BASE + path);
      await page
        .waitForLoadState("networkidle", { timeout: 5000 })
        .catch(() => {});
      await page.waitForTimeout(1200);
      const found = await page.evaluate(() => {
        const vw = document.documentElement.clientWidth;
        const out = [];
        const name = (el) =>
          `${el.tagName.toLowerCase()}${el.getAttribute("data-testid") ? `[${el.getAttribute("data-testid")}]` : ""}${
            el.className && typeof el.className === "string"
              ? `.${el.className
                  .split(" ")
                  .filter((c) => !c.startsWith("css-"))
                  .slice(0, 2)
                  .join(".")}`
              : ""
          } "${(el.innerText || "").trim().slice(0, 40)}"`;
        if (document.documentElement.scrollWidth > vw + 1) {
          out.push({
            kind: "page-scrolls-sideways",
            detail: `${document.documentElement.scrollWidth} > ${vw}`,
          });
        }
        const scrollsX = (el) => {
          for (let p = el.parentElement; p; p = p.parentElement) {
            const s = getComputedStyle(p);
            if (
              /(auto|scroll|hidden)/.test(s.overflowX) &&
              p !== document.body &&
              p !== document.documentElement
            )
              return true;
          }
          return false;
        };
        const parse = (c) => {
          const m = c.match(/rgba?\(([^)]+)\)/);
          if (!m) return null;
          const [r, g, b, a = 1] = m[1]
            .split(/[ ,/]+/)
            .filter(Boolean)
            .map(Number);
          return { r, g, b, a };
        };
        const lum = ({ r, g, b }) => {
          const f = (v) => {
            v /= 255;
            return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
          };
          return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
        };
        const bgOf = (el) => {
          for (let p = el; p; p = p.parentElement) {
            const c = parse(getComputedStyle(p).backgroundColor);
            if (c && c.a > 0.9) return c;
          }
          return (
            parse(getComputedStyle(document.body).backgroundColor) || {
              r: 255,
              g: 255,
              b: 255,
            }
          );
        };
        for (const el of document.querySelectorAll("body *")) {
          const r = el.getBoundingClientRect();
          if (r.width === 0 || r.height === 0) continue;
          const s = getComputedStyle(el);
          if (
            s.visibility === "hidden" ||
            (s.position === "fixed" && r.left >= vw)
          )
            continue;
          if (
            el.closest(
              "[aria-hidden=true], .MuiTooltip-popper, .MuiPopover-root",
            )
          )
            continue;
          if (r.right > vw + 1 && r.left < vw && !scrollsX(el)) {
            out.push({
              kind: "off-right-edge",
              detail: `${name(el)} right=${Math.round(r.right)}`,
            });
          }
          const ownText = [...el.childNodes].some(
            (n) => n.nodeType === 3 && n.textContent.trim(),
          );
          if (!ownText) continue;
          if (
            s.overflowX === "hidden" &&
            el.scrollWidth > el.clientWidth + 1 &&
            s.textOverflow !== "ellipsis" &&
            s.whiteSpace === "nowrap"
          ) {
            out.push({
              kind: "clipped-text",
              detail: `${name(el)} ${el.scrollWidth}>${el.clientWidth}`,
            });
          }
          const fg = parse(s.color);
          if (
            fg &&
            fg.a > 0.5 &&
            s.opacity !== "0" &&
            !el.closest(":disabled, .Mui-disabled, [aria-disabled=true]")
          ) {
            const bg = bgOf(el);
            const [l1, l2] = [lum(fg), lum(bg)].sort((a, b) => b - a);
            const ratio = (l1 + 0.05) / (l2 + 0.05);
            if (ratio < 3)
              out.push({
                kind: "low-contrast",
                detail: `${name(el)} ${ratio.toFixed(2)}:1`,
              });
          }
        }
        return out;
      });
      // One line per distinct finding: a list of fifty rows that all
      // overflow the same way is one finding.
      const seen = new Set();
      for (const f of found) {
        const key = `${f.kind} ${f.detail.replace(/\d+/g, "#")}`;
        if (seen.has(key)) continue;
        seen.add(key);
        findings.push({ path, width, scheme, ...f });
      }
      console.log(`${path} ${width} ${scheme}: ${seen.size} distinct`);
    }
    await ctx.close();
  }
}
await browser.close();
fs.writeFileSync(OUT, JSON.stringify(findings, null, 1) + "\n");
