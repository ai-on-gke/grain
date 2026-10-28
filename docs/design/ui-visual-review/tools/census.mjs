// Style census of the rendered UI: for every screen, the visible text
// nodes' computed font sizes, weights, colours and effective backgrounds,
// with WCAG contrast ratios, plus a few layout measurements. This is how
// the review's critique was grounded in a sandbox with no way to view a
// PNG. Output is JSON on stdout.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { SCREENS } from "./shoot.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const uiDir = path.resolve(here, "../../../../ui");
const require = createRequire(path.join(uiDir, "package.json"));
const { chromium } = require("@playwright/test");
const { signIn } = await import(path.join(uiDir, "e2e/accesscode.js"));

const url = "http://127.0.0.1:4290";
const code = (await fs.readFile("/home/debian/demo/code.txt", "utf8")).trim();
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();

import { census } from "./censusFn.mjs";
const out = {};
const screens = process.argv[2] ? process.argv[2].split(",") : Object.keys(SCREENS);
for (const name of screens) {
  for (const theme of ["light", "dark"]) {
    for (const width of ["desktop", "phone"]) {
      const context = await browser.newContext({
        viewport: width === "desktop" ? { width: 1440, height: 900 } : { width: 390, height: 844 },
        colorScheme: theme,
        isMobile: width === "phone",
        reducedMotion: "reduce",
      });
      await context.addCookies(cookies);
      await context.addInitScript(([k, v]) => localStorage.setItem(k, v), ["grain.themeMode", theme]);
      const page = await context.newPage();
      await page.goto(url + SCREENS[name].path);
      await page.waitForLoadState("networkidle", { timeout: 10000 }).catch(() => {});
      await page.waitForTimeout(600);
      out[`${name}-${width}-${theme}`] = await page.evaluate(census);
      await context.close();
    }
  }
}
await browser.close();
console.log(JSON.stringify(out, null, 1));
