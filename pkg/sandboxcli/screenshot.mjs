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

// `grain screenshot`: screenshots of any web app a run is working on --
// the dev server it started in its sandbox, a preview deployment -- at
// several widths and in light and dark, plus one contact sheet of them
// all to attach to the run's closing note, where grain shows it inline.
//
// pkg/sandboxcli/screenshot.go embeds this file, writes it to a temporary
// directory and runs it with node. Playwright is found, in order, in the
// working directory's node_modules (an app with its own @playwright/test),
// in the directories $GRAIN_SCREENSHOT_MODULE_PATHS names (npm's global
// root and grain's own cache), and otherwise this exits 3 so the Go side
// installs playwright-core into its cache and runs it again. Browsers come
// from $PLAYWRIGHT_BROWSERS_PATH, which grain's sandbox images pre-fill.
//
// It is ui/e2e/screenshot.mjs without grain's own demo server and sign-in:
// keep the two in step when the shot itself changes.

import fs from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";

export const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  tablet: { width: 820, height: 1180 },
  phone: { width: 390, height: 844 },
};
export const THEMES = ["light", "dark"];
const MISSING_PLAYWRIGHT = 3;

const USAGE = `grain screenshot --url <base-url> [--pages /,/settings] [flags]

Screenshot a web app at desktop and phone widths, light and dark, and lay
every shot out on one contact sheet. Attach the contact sheet to the run's
closing note (comment_on_issue attachments: [{"path": "<out>/contact-sheet.png"}])
and grain shows it in the task's conversation and the pull request.

  --url URL          the app's base URL, e.g. http://127.0.0.1:5173 (required)
  --pages P          comma-separated paths under --url ("/" if unset); a
                     path's #anchor is scrolled to the top before the shot
  --out DIR          directory for the PNGs (screenshots if unset)
  --widths W         any of desktop, tablet, phone ("desktop,phone")
  --themes T         any of light, dark ("light,dark"); sets
                     prefers-color-scheme
  --theme-key KEY    also set localStorage[KEY] to the theme's name, for an
                     app that keeps its own theme setting
  --storage K=V      a localStorage entry set before the page loads (repeat)
  --cookie N=V       a cookie for --url's host (repeat)
  --header "N: v"    a header sent on every request (repeat)
  --click SEL        a Playwright selector clicked after load (repeat, in order)
  --type TEXT        text typed after the clicks
  --wait-for SEL     wait for this selector to be visible before the shot
  --full-page        capture the whole scrollable page, not the viewport
  --reference FILE   a design image (a Figma export, say) put first on the
                     contact sheet so it sits beside the real render (repeat)
  --allow-errors     do not fail the run on uncaught page errors

Files are <page>-<width>-<theme>.png, so a before and an after run into two
directories line up file for file.
`;

export function parseArgs(argv) {
  const opts = {
    url: "",
    pages: ["/"],
    out: "screenshots",
    widths: ["desktop", "phone"],
    themes: [...THEMES],
    themeKey: "",
    storage: [],
    cookies: [],
    headers: {},
    click: [],
    type: "",
    waitFor: "",
    fullPage: false,
    references: [],
    allowErrors: false,
    help: false,
  };
  const list = (v) =>
    v
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
  const pair = (v, sep, flag) => {
    const i = v.indexOf(sep);
    if (i <= 0) throw new Error(`${flag} takes NAME${sep}VALUE, not ${JSON.stringify(v)}`);
    return [v.slice(0, i).trim(), v.slice(i + sep.length).trim()];
  };
  for (let i = 0; i < argv.length; i++) {
    let arg = argv[i];
    let value;
    if (arg.startsWith("--") && arg.includes("=")) {
      value = arg.slice(arg.indexOf("=") + 1);
      arg = arg.slice(0, arg.indexOf("="));
    }
    const next = () => {
      if (value !== undefined) return value;
      if (i + 1 >= argv.length) throw new Error(`${arg} needs a value`);
      return argv[++i];
    };
    switch (arg) {
      case "-h":
      case "--help":
        opts.help = true;
        break;
      case "--url":
        opts.url = next();
        break;
      case "--pages":
        opts.pages = list(next());
        break;
      case "--out":
        opts.out = next();
        break;
      case "--widths":
        opts.widths = list(next());
        break;
      case "--themes":
        opts.themes = list(next());
        break;
      case "--theme-key":
        opts.themeKey = next();
        break;
      case "--storage":
        opts.storage.push(pair(next(), "=", arg));
        break;
      case "--cookie":
        opts.cookies.push(pair(next(), "=", arg));
        break;
      case "--header": {
        const [k, v] = pair(next(), ":", arg);
        opts.headers[k] = v;
        break;
      }
      case "--click":
        opts.click.push(next());
        break;
      case "--type":
        opts.type = next();
        break;
      case "--wait-for":
        opts.waitFor = next();
        break;
      case "--full-page":
        opts.fullPage = true;
        break;
      case "--reference":
        opts.references.push(next());
        break;
      case "--allow-errors":
        opts.allowErrors = true;
        break;
      default:
        throw new Error(`unknown flag ${JSON.stringify(arg)}`);
    }
  }
  if (opts.help) return opts;
  if (!opts.url) throw new Error("--url is required: the base URL of the app to shoot");
  let base;
  try {
    base = new URL(opts.url);
  } catch {
    throw new Error(`--url ${JSON.stringify(opts.url)} is not a URL`);
  }
  if (base.protocol !== "http:" && base.protocol !== "https:") {
    throw new Error(`--url must be http or https, not ${base.protocol}`);
  }
  for (const w of opts.widths) {
    if (!VIEWPORTS[w]) throw new Error(`unknown width ${JSON.stringify(w)}: use desktop, tablet or phone`);
  }
  for (const t of opts.themes) {
    if (!THEMES.includes(t)) throw new Error(`unknown theme ${JSON.stringify(t)}: use light or dark`);
  }
  if (opts.pages.length === 0) opts.pages = ["/"];
  return opts;
}

// slug is a page's file-name stem: "/" is home, anything else its path
// with every run of other characters folded to "_".
export function slug(page) {
  const s = page
    .replace(/#.*$/, "")
    .replace(/^\/+|\/+$/g, "")
    .replace(/[^A-Za-z0-9]+/g, "_");
  const anchor = page.includes("#") ? "_" + page.slice(page.indexOf("#") + 1).replace(/[^A-Za-z0-9]+/g, "_") : "";
  return (s || "home") + anchor;
}

const escapeHTML = (s) =>
  s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

// loadChromium finds a Playwright module the app or grain's cache has.
async function loadChromium() {
  const roots = [process.cwd(), ...(process.env.GRAIN_SCREENSHOT_MODULE_PATHS || "").split(path.delimiter).filter(Boolean)];
  for (const root of roots) {
    const req = createRequire(path.join(path.resolve(root), "noop.js"));
    for (const name of ["playwright-core", "playwright", "@playwright/test"]) {
      let resolved;
      try {
        resolved = req.resolve(name);
      } catch {
        continue;
      }
      const mod = await import(pathToFileURL(resolved).href);
      const chromium = mod.chromium ?? mod.default?.chromium;
      if (chromium) return chromium;
    }
  }
  return null;
}

async function contactSheet(browser, shots, file) {
  const cells = await Promise.all(
    shots.map(async (shot) => {
      const data = (await fs.readFile(shot.file)).toString("base64");
      const ext = path.extname(shot.file).slice(1).toLowerCase();
      const type = ext === "jpg" ? "jpeg" : ext || "png";
      return `<figure${shot.reference ? ' class="ref"' : ""}><img src="data:image/${type};base64,${data}"><figcaption>${escapeHTML(shot.name)}</figcaption></figure>`;
    }),
  );
  const html = `<!doctype html><style>
    body { margin: 0; padding: 12px; background: #888; font: 13px sans-serif;
      display: flex; flex-wrap: wrap; gap: 12px; align-items: flex-start; }
    figure { margin: 0; }
    figure.ref figcaption { font-weight: bold; }
    img { display: block; max-width: 480px; max-height: 600px; border: 1px solid #333; }
    figure.ref img { border: 2px solid #f2c94c; }
    figcaption { padding-top: 4px; }
  </style>${cells.join("")}`;
  const page = await browser.newPage({ viewport: { width: 2000, height: 800 } });
  await page.setContent(html);
  await page.screenshot({ path: file, fullPage: true });
  await page.close();
}

async function main() {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (err) {
    console.error(`grain screenshot: ${err.message}\n\n${USAGE}`);
    process.exit(2);
  }
  if (opts.help) {
    process.stdout.write(USAGE);
    return;
  }
  const chromium = await loadChromium();
  if (!chromium) {
    console.error("grain screenshot: no Playwright module found (playwright-core, playwright or @playwright/test)");
    process.exit(MISSING_PLAYWRIGHT);
  }
  for (const ref of opts.references) {
    await fs.access(ref).catch(() => {
      throw new Error(`--reference ${ref}: no such file`);
    });
  }
  await fs.mkdir(opts.out, { recursive: true });
  const base = new URL(opts.url);
  const browser = await chromium.launch();
  const shots = opts.references.map((ref) => ({
    file: ref,
    name: `reference: ${path.basename(ref)}`,
    reference: true,
  }));
  let failed = false;
  try {
    for (const page of opts.pages) {
      for (const width of opts.widths) {
        for (const theme of opts.themes) {
          const context = await browser.newContext({
            viewport: VIEWPORTS[width],
            colorScheme: theme,
            isMobile: width === "phone",
            hasTouch: width !== "desktop",
            extraHTTPHeaders: opts.headers,
          });
          if (opts.cookies.length > 0) {
            await context.addCookies(opts.cookies.map(([name, value]) => ({ name, value, url: base.origin })));
          }
          const storage = [...opts.storage];
          if (opts.themeKey) storage.push([opts.themeKey, theme]);
          if (storage.length > 0) {
            await context.addInitScript((entries) => {
              for (const [k, v] of entries) localStorage.setItem(k, v);
            }, storage);
          }
          const tab = await context.newPage();
          const errors = [];
          tab.on("pageerror", (err) => errors.push(err.message));
          const target = new URL(page, base).href;
          const resp = await tab.goto(target);
          if (resp && resp.status() >= 400) {
            errors.push(`${target} answered HTTP ${resp.status()}`);
          }
          // Dev servers keep a socket open for hot reload, so the network
          // is never idle for long: settle for a quiet moment, then let
          // fonts and transitions finish.
          await tab.waitForLoadState("networkidle", { timeout: 10_000 }).catch(() => {});
          await tab.evaluate(() => document.fonts.ready);
          for (const selector of opts.click) {
            await tab.locator(selector).first().click({ timeout: 10_000 });
            await tab.waitForTimeout(300);
          }
          if (opts.type) {
            await tab.keyboard.type(opts.type);
            await tab.waitForTimeout(300);
          }
          if (opts.waitFor) {
            await tab.locator(opts.waitFor).first().waitFor({ state: "visible", timeout: 15_000 });
          }
          await tab.evaluate(() => {
            const id = decodeURIComponent(location.hash.slice(1));
            if (id) document.getElementById(id)?.scrollIntoView({ block: "start" });
          });
          await tab.waitForTimeout(500);
          const file = path.join(opts.out, `${slug(page)}-${width}-${theme}.png`);
          await tab.screenshot({ path: file, fullPage: opts.fullPage });
          shots.push({ file, name: path.basename(file, ".png") });
          console.log(file);
          for (const e of errors) {
            if (!opts.allowErrors) failed = true;
            console.error(`page error on ${page} (${width}, ${theme}): ${e}`);
          }
          await context.close();
        }
      }
    }
    const sheet = path.join(opts.out, "contact-sheet.png");
    await contactSheet(browser, shots, sheet);
    console.log(sheet);
  } finally {
    await browser.close();
  }
  if (failed) {
    console.error("grain screenshot: the page raised errors (--allow-errors to keep the shots anyway)");
    process.exitCode = 1;
  }
}

// Run only as a program, so a test can import parseArgs and slug.
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  main().catch((err) => {
    console.error(`grain screenshot: ${err.message || err}`);
    process.exit(1);
  });
}
