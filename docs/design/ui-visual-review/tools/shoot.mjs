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

// Throwaway capture tool for the visual review (docs/design/ui-visual-review).
//
//   node docs/design/ui-visual-review/tools/shoot.mjs --out current
//   node docs/design/ui-visual-review/tools/shoot.mjs --out option-1 \
//       --css docs/design/ui-visual-review/tools/option-1.css \
//       --js  docs/design/ui-visual-review/tools/option-1.js
//
// Shoots the real UI served by a running `grain demo` (--url, default
// http://127.0.0.1:4290; --code-file names its access code) at a desktop
// and a phone width, light and dark. --css injects a stylesheet after the
// page has loaded and --js runs a script in the page, which is how the
// redesign options are mocked onto the real UI without touching ui/src.
// It is a cousin of ui/e2e/screenshot.mjs, trimmed to what this review
// needs: a fixed set of named screens, per-screen click steps, and the
// injection hooks. Not wired into any make target or test.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { census } from "./censusFn.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "../../../..");
const uiDir = path.join(root, "ui");
const require = createRequire(path.join(uiDir, "package.json"));
const { chromium } = require("@playwright/test");
const { signIn } = await import(
  path.join(uiDir, "e2e/accesscode.js")
);

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};
const THEME_KEY = "grain.themeMode";

// The screens the brief asks for. `clicks` are run after load; `wait` is
// a selector to wait for before the shot.
export const SCREENS = {
  tasks: { path: "/" },
  board: { path: "/boards/board-1" },
  task: { path: "/tasks/4" },
  "new-task": { path: "/new" },
  ci: { path: "/ci" },
  "merge-queue": { path: "/repos/acme/widgets/settings/merge-queue" },
  inbox: { path: "/inbox" },
  capabilities: { path: "/settings/capabilities" },
  metrics: { path: "/metrics" },
};

function parseArgs(argv) {
  const o = {
    url: "http://127.0.0.1:4290",
    codeFile: "/home/debian/demo/code.txt",
    out: "current",
    css: [],
    js: [],
    screens: Object.keys(SCREENS),
    widths: ["desktop", "phone"],
    themes: ["light", "dark"],
    scale: 1,
    markLight: null,
    markDark: null,
    option: 0,
    census: false,
    icons: null,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const v = () => argv[++i];
    if (a === "--url") o.url = v();
    else if (a === "--code-file") o.codeFile = v();
    else if (a === "--out") o.out = v();
    else if (a === "--css") o.css.push(v());
    else if (a === "--js") o.js.push(v());
    else if (a === "--screens") o.screens = v().split(",");
    else if (a === "--widths") o.widths = v().split(",");
    else if (a === "--themes") o.themes = v().split(",");
    else if (a === "--scale") o.scale = Number(v());
    else if (a === "--mark-light") o.markLight = v();
    else if (a === "--option") o.option = Number(v());
    else if (a === "--census") o.census = true;
    else if (a === "--icons") o.icons = v();
    else if (a === "--mark-dark") o.markDark = v();
    else throw new Error(`unknown argument ${a}`);
  }
  return o;
}

async function main() {
  const o = parseArgs(process.argv.slice(2));
  const outDir = path.isAbsolute(o.out) ? o.out : path.join(here, "..", o.out);
  await fs.mkdir(outDir, { recursive: true });
  const code = (await fs.readFile(o.codeFile, "utf8")).trim();
  const { cookies } = await signIn(o.url, code);
  // One style tag per file, so a sheet that starts with @import keeps
  // it first (an @import after other rules is ignored).
  const css = await Promise.all(o.css.map((f) => fs.readFile(f, "utf8")));
  const js = (await Promise.all(o.js.map((f) => fs.readFile(f, "utf8")))).join(
    "\n;\n",
  );
  const marks = {
    light: o.markLight ? await fs.readFile(o.markLight, "utf8") : null,
    dark: o.markDark ? await fs.readFile(o.markDark, "utf8") : null,
  };
  const icons = o.icons ? JSON.parse(await fs.readFile(o.icons, "utf8")) : null;
  const browser = await chromium.launch();
  const censusOut = {};
  try {
    for (const name of o.screens) {
      const screen = SCREENS[name];
      if (!screen) throw new Error(`unknown screen ${name}`);
      for (const width of o.widths) {
        for (const theme of o.themes) {
          const context = await browser.newContext({
            viewport: VIEWPORTS[width],
            deviceScaleFactor: o.scale,
            colorScheme: theme,
            isMobile: width === "phone",
            hasTouch: width === "phone",
            reducedMotion: "reduce",
            // The demo's CSP (style-src 'self') would refuse the web
            // fonts the options load; the mock is the one place that
            // is fine.
            bypassCSP: true,
          });
          await context.addCookies(cookies);
          await context.addInitScript(
            ([key, value]) => localStorage.setItem(key, value),
            [THEME_KEY, theme],
          );
          const tab = await context.newPage();
          const errors = [];
          tab.on("pageerror", (err) => errors.push(err.message));
          await tab.goto(new URL(screen.path, o.url).href);
          await tab
            .waitForLoadState("networkidle", { timeout: 10_000 })
            .catch(() => {});
          await tab.evaluate(() => document.fonts.ready);
          for (const sel of screen.clicks || []) {
            await tab.locator(sel).first().click({ timeout: 10_000 });
            await tab.waitForTimeout(300);
          }
          if (screen.wait) {
            await tab.locator(screen.wait).first().waitFor({ timeout: 10_000 });
          }
          for (const sheet of css) await tab.addStyleTag({ content: sheet });
          if (js) {
            await tab.evaluate(
              ({ src, theme, width, name, mark, option, icons }) => {
                window.__mock = { theme, width, screen: name, mark, option, icons };
                new Function(src)();
              },
              { src: js, theme, width, name, mark: marks[theme], option: o.option, icons },
            );
          }
          await tab.evaluate(() => document.fonts.ready);
          await tab.waitForTimeout(700);
          const file = path.join(outDir, `${name}-${width}-${theme}.png`);
          await tab.screenshot({ path: file });
          console.log(path.relative(root, file));
          if (o.census) censusOut[`${name}-${width}-${theme}`] = await tab.evaluate(census);
          for (const e of errors) console.error(`page error on ${name}: ${e}`);
          await context.close();
        }
      }
    }
  } finally {
    await browser.close();
  }
  if (o.census) {
    const f = path.join(outDir, "census.json");
    await fs.writeFile(f, JSON.stringify(censusOut, null, 1));
    console.log(path.relative(root, f));
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((err) => {
    console.error(err.stack || err);
    process.exit(1);
  });
}
