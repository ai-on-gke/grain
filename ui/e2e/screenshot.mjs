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

// node e2e/screenshot.mjs --pages /,/inbox [--out screenshots/after]
//
// Screenshots of the real UI, for a person or an agent to look at: every
// page asked for, at a desktop and a phone width, in light and dark. It
// is what the "UI changes" rule in .grain/settings.yml points agents at
// (`make ui-screenshots PAGES=...` runs it), because a change can pass
// every jsdom and DOM assertion here and still draw a badge off-centre or
// an action floating mid-rail -- which only a rendered page shows.
//
// By default it builds `grain` from this checkout (./cmd/grain, which
// embeds whatever pkg/ui/static holds, so build the frontend first --
// the make target does) and runs `grain demo` over a throwaway store,
// the same server e2e/fixtures.js gives each Playwright worker. Point
// it at a server you already have instead with --url and --code.
//
// Options (each also readable from the environment named beside it):
//
//   --pages  PAGES   comma-separated paths, e.g. "/,/inbox,/repos". "/" if unset.
//                    A path's #anchor is scrolled to the top before the shot.
//   --out    OUT     directory for the PNGs. screenshots/ if unset.
//   --widths WIDTHS  any of desktop, tablet, phone. "desktop,phone" if unset.
//   --themes THEMES  any of light, dark. "light,dark" if unset.
//   --url    URL     a running grain to shoot instead of starting a demo.
//   --code   CODE    that server's access code (with --url).
//   --bin    BIN     a built grain binary to run `demo` from.
//   --demo-args DEMO_ARGS  space-separated extra `grain demo` flags, e.g.
//                    "-host-executor=hostd".
//   --full-page      capture the whole scrollable page, not the viewport.
//   --click  CLICK   a Playwright selector to click once the page has
//                    loaded, before the shot -- to open a menu or expand
//                    a collapsed group. Repeat it to click several in
//                    order; one that matches nothing fails the run.
//   --look[=Q] LOOK  send the screenshots to Gemini and print what it sees:
//                    for an agent whose file reader shows a PNG as bytes.
//                    Needs GEMINI_API_KEY or ~/.gemini-api-key; Q replaces
//                    the default question. GEMINI_MODEL picks the model.
//
// Files are named <page>-<width>-<theme>.png, so a "before" and an
// "after" run into two directories line up file for file, and
// contact-sheet.png puts them all on one image -- one attachment rather
// than one per shot. Uncaught page errors are printed and fail the run:
// a blank pane is not a screenshot.
import { chromium } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { newAccessCode, signIn } from "./accesscode.js";

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  tablet: { width: 820, height: 1180 },
  phone: { width: 390, height: 844 },
};
const THEMES = ["light", "dark"];
// The same localStorage key src/ThemeModeContext.jsx reads, so the app's
// own theme setting is what changes rather than only the media query.
const THEME_KEY = "grain.themeMode";

const DEFAULT_LOOK =
  "These are screenshots of one web app, each labelled with its file name " +
  "(page-width-theme). For each one, list concrete visual defects: " +
  "misalignment (badges or counts off-centre from their labels, baselines " +
  "that do not line up), controls floating away from where they belong, " +
  "text overflowing or clipped, unexpected wrapping, uneven spacing, and " +
  "anything unreadable in that theme. Say 'none seen' for a clean one.";

function parseArgs(argv) {
  const opts = {
    pages: process.env.PAGES,
    out: process.env.OUT,
    widths: process.env.WIDTHS,
    themes: process.env.THEMES,
    url: process.env.URL,
    code: process.env.CODE,
    bin: process.env.BIN,
    demoArgs: process.env.DEMO_ARGS,
    fullPage: false,
    look: process.env.LOOK ? DEFAULT_LOOK : null,
    click: process.env.CLICK ? [process.env.CLICK] : [],
  };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--full-page") {
      opts.fullPage = true;
      continue;
    }
    if (arg === "--look" || arg.startsWith("--look=")) {
      opts.look = arg.slice("--look=".length) || DEFAULT_LOOK;
      continue;
    }
    if (arg === "--click" || arg.startsWith("--click=")) {
      opts.click.push(arg.startsWith("--click=") ? arg.slice(8) : argv[++i]);
      continue;
    }
    const m = arg.match(
      /^--(pages|out|widths|themes|url|code|bin|demo-args)(?:=(.*))?$/,
    );
    if (!m) throw new Error(`unknown argument ${arg}; see this file's header`);
    opts[m[1] === "demo-args" ? "demoArgs" : m[1]] = m[2] ?? argv[++i];
  }
  const list = (s, def) =>
    (s || def)
      .split(/[,\s]+/)
      .map((x) => x.trim())
      .filter(Boolean);
  opts.pages = list(opts.pages, "/").map((p) =>
    p.startsWith("/") ? p : `/${p}`,
  );
  opts.widths = list(opts.widths, "desktop,phone");
  opts.themes = list(opts.themes, "light,dark");
  opts.out = path.resolve(opts.out || "screenshots");
  for (const w of opts.widths) {
    if (!VIEWPORTS[w]) throw new Error(`unknown width ${w}`);
  }
  for (const t of opts.themes) {
    if (!THEMES.includes(t)) throw new Error(`unknown theme ${t}`);
  }
  opts.demoArgs = (opts.demoArgs || "").split(/\s+/).filter(Boolean);
  if (opts.url && !opts.code) throw new Error("--url needs --code");
  return opts;
}

// startDemo runs `grain demo` on a port the kernel picks, with an access
// code of our own, and resolves once it says where it is listening.
async function startDemo(tmpDir, bin, demoArgs = []) {
  if (!bin) {
    const root = fileURLToPath(new URL("../..", import.meta.url));
    bin = path.join(tmpDir, "grain");
    console.error("building grain (./cmd/grain) ...");
    execFileSync("go", ["build", "-o", bin, "./cmd/grain"], {
      cwd: root,
      stdio: "inherit",
    });
  }
  const code = newAccessCode();
  const codeFile = path.join(tmpDir, "access-code");
  await fs.writeFile(codeFile, `${code}\n`, { mode: 0o600 });
  const proc = spawn(
    bin,
    [
      "demo",
      "-addr",
      "127.0.0.1:0",
      "-open=false",
      "-access-code-file",
      codeFile,
      ...demoArgs,
    ],
    {
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, TMPDIR: tmpDir },
    },
  );
  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`grain demo did not start in 60s:\n${output}`)),
      60_000,
    );
    const read = (chunk) => {
      output += chunk;
      const at = output.match(/at http:\/\/(?:127\.0\.0\.1|\[::\]):(\d+)/);
      if (at) {
        clearTimeout(timer);
        resolve(`http://127.0.0.1:${at[1]}`);
      }
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("exit", (c) => {
      clearTimeout(timer);
      reject(
        new Error(`grain demo exited (${c}) before listening:\n${output}`),
      );
    });
  });
  return { url, code, proc };
}

// contactSheet lays every shot out on one page, a row per page and a
// column per width and theme, and screenshots that.
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
    figcaption { padding-top: 4px; }
  </style>${cells.join("")}`;
  const page = await browser.newPage({
    viewport: { width: 2000, height: 800 },
  });
  await page.setContent(html);
  await page.screenshot({ path: file, fullPage: true });
  await page.close();
}

// look asks Gemini about the screenshots and resolves to its answer.
async function look(shots, question) {
  let key = process.env.GEMINI_API_KEY;
  if (!key) {
    key = await fs
      .readFile(path.join(os.homedir(), ".gemini-api-key"), "utf8")
      .then((s) => s.trim())
      .catch(() => "");
  }
  if (!key) {
    throw new Error("--look needs GEMINI_API_KEY or ~/.gemini-api-key");
  }
  const parts = [{ text: question }];
  for (const shot of shots) {
    parts.push({ text: shot.name });
    parts.push({
      inline_data: {
        mime_type: "image/png",
        data: (await fs.readFile(shot.file)).toString("base64"),
      },
    });
  }
  const model = process.env.GEMINI_MODEL || "gemini-3.8-flash";
  const res = await fetch(
    `https://generativelanguage.googleapis.com/v1beta/models/${model}:generateContent`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json", "x-goog-api-key": key },
      body: JSON.stringify({ contents: [{ parts }] }),
    },
  );
  const body = await res.json();
  if (!res.ok) {
    throw new Error(
      `Gemini ${model}: ${res.status} ${JSON.stringify(body.error ?? body)}`,
    );
  }
  return (body.candidates?.[0]?.content?.parts ?? [])
    .map((p) => p.text ?? "")
    .join("");
}

function slug(page) {
  const s = page.replace(/^\/+|\/+$/g, "").replace(/[^A-Za-z0-9]+/g, "_");
  return s || "home";
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  const tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "grain-shots-"));
  let demo;
  try {
    let { url, code } = opts;
    if (!url) {
      demo = await startDemo(tmpDir, opts.bin, opts.demoArgs);
      ({ url, code } = demo);
    }
    const { cookies } = await signIn(url, code);
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
            await context.addCookies(cookies);
            await context.addInitScript(
              ([key, value]) => localStorage.setItem(key, value),
              [THEME_KEY, theme],
            );
            const tab = await context.newPage();
            const errors = [];
            tab.on("pageerror", (err) => errors.push(err.message));
            await tab.goto(new URL(page, url).href);
            // The UI polls, so the network is never idle for long; settle
            // for a quiet moment, then let fonts and transitions finish.
            await tab
              .waitForLoadState("networkidle", { timeout: 10_000 })
              .catch(() => {});
            await tab.evaluate(() => document.fonts.ready);
            for (const selector of opts.click) {
              await tab.locator(selector).first().click({ timeout: 10_000 });
              await tab.waitForTimeout(300);
            }
            // A page given as /path#anchor is shot with that element
            // scrolled to the top -- whatever scrolls it, the window or a
            // pane of its own, which --full-page cannot unroll -- so a
            // card far down a phone-width page is in the shot. Wait
            // briefly for the element first so a debounced section (such
            // as #composition-preview) has time to mount after networkidle.
            const anchor = page.includes("#")
              ? decodeURIComponent(page.slice(page.indexOf("#") + 1))
              : "";
            if (anchor) {
              await tab
                .waitForFunction(
                  (id) => Boolean(document.getElementById(id)),
                  anchor,
                  {
                    timeout: 5_000,
                  },
                )
                .catch(() => {});
            }
            await tab.evaluate(() => {
              const id = decodeURIComponent(location.hash.slice(1));
              if (id)
                document.getElementById(id)?.scrollIntoView({ block: "start" });
            });
            await tab.waitForTimeout(500);
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
    if (opts.look) {
      console.log(`\n--- Gemini on ${shots.length} screenshot(s) ---`);
      console.log(await look(shots, opts.look));
    }
    if (failed) process.exitCode = 1;
  } finally {
    if (demo) demo.proc.kill("SIGKILL");
    await fs.rm(tmpDir, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err.message || err);
  process.exit(1);
});
