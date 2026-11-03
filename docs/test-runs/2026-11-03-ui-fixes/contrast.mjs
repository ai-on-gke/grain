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
      scanned.push({ page: p, theme, checked, failing, incomplete });
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
  JSON.stringify({ scanned, failing: rows }, null, 2) + "\n",
);
console.log(`${rows.length} failing nodes -> ${out}`);
