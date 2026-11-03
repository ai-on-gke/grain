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

// Measures WCAG AA text contrast with axe-core's color-contrast rule
// over every page §11 of docs/test-plans/2026-11-03.md names, in light
// and dark, at 1440×900, against a running `grain demo`.
//
//   cd ui && node ../docs/test-runs/2026-11-03-ui-fixes/contrast.mjs \
//     --url http://127.0.0.1:18080 --code <access code> --out <file.json>
//
// Writes one row per failing text node: page, theme, ratio, the colours
// axe resolved, the font size and weight, and the node's selector and
// text. Exits 0 either way; the JSON is the result.
import fs from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";

// Bare imports resolve from ui/, where this is run, not from docs/.
const require = createRequire(path.resolve("package.json"));
const { chromium } = require("playwright");
const AxeBuilder = require("@axe-core/playwright").default;
const { signIn } = await import(
  pathToFileURL(path.resolve("e2e/accesscode.js")).href
);

const args = Object.fromEntries(
  process.argv
    .slice(2)
    .join(" ")
    .split("--")
    .filter(Boolean)
    .map((a) => a.trim().split(/\s+/)),
);
const url = args.url;
const code = args.code;
const out = args.out || "contrast.json";
const pages = (args.pages || "").split(",").filter(Boolean).length
  ? args.pages.split(",")
  : [
      "/inbox",
      "/boards/board-1",
      "/boards/board-3",
      "/tasks/8",
      "/system/health",
      "/settings/models",
      "/settings/sandboxes",
      "/settings/credentials",
      "/settings/access",
      "/settings/hostds",
      "/settings/preferences",
      "/settings/upgrade",
      "/settings/caches",
      "/settings/engine",
      "/settings/config-repo",
      "/settings/general",
      "/settings/git",
      "/settings/repositories",
      "/settings/tasks",
      "/settings/agents",
      "/settings/capabilities",
      "/settings/ci",
      "/settings/memory",
    ];

const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const rows = [];
const scanned = [];
const undetermined = [];
try {
  for (const theme of ["light", "dark"]) {
    for (const p of pages) {
      const context = await browser.newContext({
        viewport: { width: 1440, height: 900 },
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
      await tab.evaluate(() => document.fonts.ready);
      // Let MUI transitions finish so axe reads settled colours.
      await tab.waitForTimeout(600);
      const result = await new AxeBuilder({ page: tab })
        .withRules(["color-contrast"])
        .analyze();
      const checked =
        result.passes.reduce((n, r) => n + r.nodes.length, 0) +
        result.violations.reduce((n, r) => n + r.nodes.length, 0);
      const incomplete = result.incomplete.reduce(
        (n, r) => n + r.nodes.length,
        0,
      );
      let failing = 0;
      for (const v of result.violations) {
        for (const node of v.nodes) {
          const d = node.any[0]?.data || {};
          failing++;
          rows.push({
            page: p,
            theme,
            ratio: d.contrastRatio,
            expected: d.expectedContrastRatio,
            fg: d.fgColor,
            bg: d.bgColor,
            fontSize: d.fontSize,
            fontWeight: d.fontWeight,
            target: node.target.join(" "),
            html: node.html.slice(0, 160),
          });
        }
      }
      // Nodes axe could not decide (a background it cannot resolve:
      // overlapping elements, an image, a pseudo-element), by its reason,
      // with the ratio where it still computed one.
      const reasons = {};
      for (const r of result.incomplete) {
        for (const node of r.nodes) {
          const d = node.any[0]?.data || {};
          const key = d.messageKey || "unknown";
          reasons[key] = (reasons[key] || 0) + 1;
          if (d.contrastRatio && d.contrastRatio < 4.5) {
            undetermined.push({
              page: p,
              theme,
              reason: key,
              ratio: d.contrastRatio,
              fg: d.fgColor,
              bg: d.bgColor,
              target: node.target.join(" "),
              html: node.html.slice(0, 160),
            });
          }
        }
      }
      // Measure those with getComputedStyle instead: the text colour over
      // the element's own ancestors' backgrounds composited in order, a
      // gradient read as its first colour stop.
      const targets = result.incomplete.flatMap((r) =>
        r.nodes.map((n) => n.target[0]),
      );
      const computed = await tab.evaluate((selectors) => {
        const parse = (c) => {
          const m = c.match(/rgba?\(([^)]+)\)/);
          if (!m) return null;
          const [r, g, b, a = 1] = m[1]
            .split(/[ ,/]+/)
            .filter(Boolean)
            .map(Number);
          return [r, g, b, a];
        };
        const over = (top, under) => {
          const a = top[3];
          return [0, 1, 2]
            .map((i) => top[i] * a + under[i] * (1 - a))
            .concat(1);
        };
        const lum = ([r, g, b]) =>
          [r, g, b]
            .map((v) => v / 255)
            .map((c) =>
              c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4,
            )
            .reduce((s, c, i) => s + c * [0.2126, 0.7152, 0.0722][i], 0);
        const out = [];
        for (const sel of selectors) {
          const el =
            typeof sel === "string" ? document.querySelector(sel) : null;
          if (!el || !el.textContent.trim()) continue;
          const layers = [];
          for (let a = el; a; a = a.parentElement) {
            const cs = getComputedStyle(a);
            let bg = parse(cs.backgroundColor);
            if (
              (!bg || bg[3] === 0) &&
              cs.backgroundImage.includes("gradient")
            ) {
              bg = parse(cs.backgroundImage);
            }
            if (bg && bg[3] > 0) layers.push(bg);
            if (bg && bg[3] === 1) break;
          }
          let bg = [255, 255, 255, 1];
          if (getComputedStyle(document.body).backgroundColor) {
            const b = parse(getComputedStyle(document.body).backgroundColor);
            if (b && b[3] === 1) bg = b;
          }
          for (const l of layers.reverse()) bg = over(l, bg);
          const fgRaw = parse(getComputedStyle(el).color);
          let opacity = 1;
          for (let a = el; a; a = a.parentElement)
            opacity *= Number(getComputedStyle(a).opacity);
          const fg = over(
            [fgRaw[0], fgRaw[1], fgRaw[2], fgRaw[3] * opacity],
            bg,
          );
          const [hi, lo] = [lum(fg), lum(bg)].sort((x, y) => y - x);
          const ratio = Math.round(((hi + 0.05) / (lo + 0.05)) * 100) / 100;
          const size = parseFloat(getComputedStyle(el).fontSize);
          const weight = Number(getComputedStyle(el).fontWeight);
          const large = size >= 24 || (size >= 18.66 && weight >= 700);
          out.push({
            target: sel,
            ratio,
            needs: large ? 3 : 4.5,
            fg: `rgb(${fg.slice(0, 3).map(Math.round).join(",")})`,
            bg: `rgb(${bg.slice(0, 3).map(Math.round).join(",")})`,
            text: el.textContent.trim().slice(0, 80),
          });
        }
        return out;
      }, targets);
      for (const c of computed) {
        if (c.ratio < c.needs)
          undetermined.push({ page: p, theme, reason: "computed", ...c });
      }
      scanned.push({
        page: p,
        theme,
        checked,
        failing,
        incomplete,
        reasons,
        computedChecked: computed.length,
        computedUnderAA: computed.filter((c) => c.ratio < c.needs).length,
      });
      console.log(
        `${theme} ${p}: ${checked} checked, ${failing} under AA, ${incomplete} undetermined`,
      );
      await context.close();
    }
  }
} finally {
  await browser.close();
}
await fs.writeFile(
  out,
  JSON.stringify({ scanned, failing: rows, undetermined }, null, 2) + "\n",
);
console.log(`${rows.length} failing nodes -> ${out}`);
