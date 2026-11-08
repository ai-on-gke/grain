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

// node e2e/screenshot.mjs [--pages /,/directory,/new,/entries/alice-workstation,/admin,/status] [--out screenshots/after]
//
// Captures Playwright screenshots of the real grain-registry UI served by
// `grain-registry demo` across desktop (1440x900) and phone (390x844) in light
// and dark themes, verifies zero page errors or CSP violations, and stitches
// a combined contact-sheet.png.

import { chromium } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  tablet: { width: 820, height: 1180 },
  phone: { width: 390, height: 844 },
};
const THEMES = ["light", "dark"];
const THEME_KEY = "grain.themeMode";
const DEFAULT_PAGES =
  "/,/directory,/new,/grants,/entries/alice-workstation,/admin,/admin#delegating-apps,/status";

function parseArgs(argv) {
  const opts = {
    pages: process.env.PAGES,
    out: process.env.OUT,
    widths: process.env.WIDTHS,
    themes: process.env.THEMES,
    url: process.env.URL,
    bin: process.env.BIN,
    fullPage: false,
  };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--full-page") {
      opts.fullPage = true;
      continue;
    }
    const m = arg.match(/^--(pages|out|widths|themes|url|bin)(?:=(.*))?$/);
    if (!m) throw new Error(`unknown argument ${arg}`);
    opts[m[1]] = m[2] ?? argv[++i];
  }
  const list = (s, def) =>
    (s || def)
      .split(/[,\s]+/)
      .map((x) => x.trim())
      .filter(Boolean);
  opts.pages = list(opts.pages, DEFAULT_PAGES).map((p) =>
    p.startsWith("/") ? p : `/${p}`,
  );
  opts.widths = list(opts.widths, "desktop,phone");
  opts.themes = list(opts.themes, "light,dark");
  opts.out = path.resolve(opts.out || "screenshots/after");
  for (const w of opts.widths) {
    if (!VIEWPORTS[w]) throw new Error(`unknown width ${w}`);
  }
  for (const t of opts.themes) {
    if (!THEMES.includes(t)) throw new Error(`unknown theme ${t}`);
  }
  return opts;
}

export async function startRegistryDemo(tmpDir, bin) {
  const uiDir = fileURLToPath(new URL("..", import.meta.url));
  const root = fileURLToPath(new URL("../..", import.meta.url));
  if (!bin) {
    console.error("building ui-registry into pkg/registry/server/static ...");
    execFileSync("npm", ["run", "build"], { cwd: uiDir, stdio: "inherit" });
    bin = path.join(tmpDir, "grain-registry");
    console.error("building grain-registry (./cmd/grain-registry) ...");
    execFileSync("go", ["build", "-o", bin, "./cmd/grain-registry"], {
      cwd: root,
      stdio: "inherit",
    });
  }
  const proc = spawn(bin, ["demo", "-addr", "127.0.0.1:0"], {
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, TMPDIR: tmpDir },
  });
  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () =>
        reject(
          new Error(`grain-registry demo did not start in 60s:\n${output}`),
        ),
      60_000,
    );
    const read = (chunk) => {
      output += chunk;
      const at = output.match(
        /listening on (http:\/\/(?:127\.0\.0\.1|\[::\]):\d+)/,
      );
      if (at) {
        clearTimeout(timer);
        resolve(at[1]);
      }
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("exit", (c) => {
      clearTimeout(timer);
      reject(
        new Error(
          `grain-registry demo exited (${c}) before listening:\n${output}`,
        ),
      );
    });
  });
  return { url, proc };
}

async function contactSheet(browser, shots, file) {
  const cells = await Promise.all(
    shots.map(async (shot) => {
      const data = (await fs.readFile(shot.file)).toString("base64");
      return `<figure><img src="data:image/png;base64,${data}"><figcaption>${shot.name}</figcaption></figure>`;
    }),
  );
  const html = `<!doctype html><style>
    body { margin: 0; padding: 12px; background: #888; font: 13px sans-serif;
      display: flex; flex-wrap: wrap; gap: 12px; align-items: flex-start; }
    figure { margin: 0; }
    img { display: block; max-width: 480px; max-height: 600px; border: 1px solid #333; }
    figcaption { padding-top: 4px; color: #111; font-weight: 600; }
  </style>${cells.join("")}`;
  const page = await browser.newPage({
    viewport: { width: 2000, height: 800 },
  });
  await page.setContent(html);
  await page.screenshot({ path: file, fullPage: true });
  await page.close();
}

function slug(page) {
  const s = page.replace(/^\/+|\/+$/g, "").replace(/[^A-Za-z0-9]+/g, "_");
  return s || "my_entries";
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  const tmpDir = await fs.mkdtemp(
    path.join(os.tmpdir(), "grain-registry-shots-"),
  );
  let demo;
  try {
    let { url } = opts;
    if (!url) {
      demo = await startRegistryDemo(tmpDir, opts.bin);
      ({ url } = demo);
    }
    await fs.mkdir(opts.out, { recursive: true });
    const browser = await chromium.launch();
    let failed = false;
    const shots = [];
    try {
      for (const page of opts.pages) {
        for (const width of opts.widths) {
          for (const theme of opts.themes) {
            const context = await browser.newContext({
              viewport: VIEWPORTS[width],
              colorScheme: theme,
              isMobile: width === "phone",
              hasTouch: width !== "desktop",
            });
            await context.addInitScript(
              ([key, value]) => localStorage.setItem(key, value),
              [THEME_KEY, theme],
            );
            const tab = await context.newPage();
            const errors = [];
            tab.on("pageerror", (err) => errors.push(err.message));
            tab.on("console", (msg) => {
              if (
                msg.type() === "error" &&
                msg.text().includes("Content Security Policy")
              ) {
                errors.push(`CSP error: ${msg.text()}`);
              }
            });
            await tab.goto(new URL(page, url).href);
            await tab
              .waitForLoadState("networkidle", { timeout: 10_000 })
              .catch(() => {});
            await tab.evaluate(() => document.fonts.ready);
            const hashIdx = page.indexOf("#");
            if (hashIdx >= 0) {
              const anchorId = page.slice(hashIdx + 1);
              if (anchorId) {
                await tab.evaluate((id) => {
                  const el = document.getElementById(id);
                  if (el) el.scrollIntoView({ block: "start" });
                }, anchorId);
              }
            }
            await tab.waitForTimeout(300);
            const file = path.join(
              opts.out,
              `${slug(page)}-${width}-${theme}.png`,
            );
            await tab.screenshot({ path: file, fullPage: opts.fullPage });
            shots.push({ file, name: path.basename(file, ".png") });
            console.log(file);
            for (const e of errors) {
              failed = true;
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
    if (failed) process.exit(1);
  } finally {
    demo?.proc.kill();
    await fs.rm(tmpDir, { recursive: true, force: true });
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((err) => {
    console.error(err.message || err);
    process.exit(1);
  });
}
