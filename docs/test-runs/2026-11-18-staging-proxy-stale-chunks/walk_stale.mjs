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

// Browser half of docs/test-plans/2026-11-18.md §7 (items 1, 3, 5) against
// the env.sh harness: https://127.0.0.1:4292 in front of a real daemon.
//
//   item1  planted cookies never reach the browser's jar; the operator's
//          session survives /staging/preview/plant
//   item5  with navigator.connection.saveData (CDP Emulation.
//          setDataSaverOverride) no lazy chunk loads before a navigation
//   item3  a real upgrade under an open /next/ tab: binary A (build A's
//          chunk hashes) swapped for binary B and restarted, then
//          unpreloaded routes and @defer blocks clicked, at 1440 and 390 px;
//          classic / walked across the same swap
//
// Run from ui-ng/ (for its playwright):
//   PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-18-staging-proxy-stale-chunks/walk_stale.mjs
// Env: WORK_DIR (default /tmp/walk-stale-sqlite), OUT (screenshots + JSON).
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("playwright");
const HERE = path.dirname(fileURLToPath(import.meta.url));
const WORK = process.env.WORK_DIR || "/tmp/walk-stale-sqlite";
const OUT = process.env.OUT || "/tmp/walk-stale-shots";
const BASE = "https://127.0.0.1:4292";
fs.mkdirSync(OUT, { recursive: true });

const results = [];
function record(item, name, ok, detail) {
  results.push({ item, name, ok, detail });
  console.log(`${ok ? "PASS" : "FAIL"} [${item}] ${name}: ${JSON.stringify(detail)}`);
}

// swap installs binary A or B and restarts the daemon (and with it the
// identity-aware proxy) on the same data dir, as an upgrade does.
function swap(which) {
  // Copy then rename over the running binary, as an in-place upgrade does.
  const bin = path.join(WORK, "bin", "grain-slowverifier");
  fs.copyFileSync(`${bin}-${which}`, `${bin}.new`);
  fs.renameSync(`${bin}.new`, bin);
  execFileSync("bash", ["-c", `source "${HERE}/env.sh"; STORE=\${STORE:-sqlite}; start_walk_daemon "${WORK}"; wait_staging`], {
    stdio: ["ignore", "ignore", "inherit"],
    timeout: 120000,
  });
}

const isChunk = (u) => /\/next\/(chunk|main)-[A-Z0-9]+\.js/.test(u);

async function newPage(browser, width, { saveData = false } = {}) {
  const ctx = await browser.newContext({
    ignoreHTTPSErrors: true,
    viewport: { width, height: width > 600 ? 900 : 844 },
    deviceScaleFactor: 1,
  });
  const page = await ctx.newPage();
  if (saveData) {
    const cdp = await ctx.newCDPSession(page);
    await cdp.send("Emulation.setDataSaverOverride", { dataSaverEnabled: true });
  }
  const log = { chunks: [], failed: [], pageErrors: [], consoleErrors: [] };
  page.on("response", (r) => {
    if (isChunk(r.url())) log.chunks.push({ url: r.url().replace(BASE, ""), status: r.status(), at: Date.now() });
  });
  page.on("pageerror", (e) => log.pageErrors.push(String(e).slice(0, 300)));
  page.on("console", (m) => {
    if (m.type() === "error") log.consoleErrors.push(m.text().slice(0, 300));
  });
  return { ctx, page, log };
}

async function openInbox(page) {
  await page.goto(`${BASE}/next/inbox`, { waitUntil: "load", timeout: 60000 });
  await page.locator('[data-testid="help-trigger"]').first().waitFor({ state: "attached", timeout: 60000 });
}

// clickRoute clicks the shell's own link to route (rail, phone bar, or the
// More menu), so the navigation is the router's, not a page load.
async function clickRoute(page, route) {
  const sel = `a[href="/next${route}"]`;
  let link = page.locator(`${sel}:visible`).first();
  if (!(await link.count())) {
    const more = page.locator('[data-testid="rail-more"]:visible, [data-testid="compact-more"]:visible').first();
    if (await more.count()) await more.click();
    link = page.locator(`${sel}:visible`).first();
    await link.waitFor({ state: "visible", timeout: 5000 }).catch(async () => {
      await shot(page, `debug-no-link-${route.slice(1)}-${page.viewportSize().width}`);
    });
  }
  if (!(await link.count())) throw new Error(`no visible link to ${route}`);
  await link.click();
}

async function snackbar(page, timeout = 8000) {
  const s = page.locator('[data-testid="snackbar"]');
  try {
    await s.first().waitFor({ state: "visible", timeout });
    return (await s.first().textContent()).trim().replace(/\s+/g, " ");
  } catch {
    return null;
  }
}

const unhandled = (log) =>
  log.pageErrors.concat(log.consoleErrors.filter((t) => /Uncaught|unhandled|ChunkLoadError|Failed to fetch dynamically/i.test(t)));

async function item1(browser) {
  const { ctx, page } = await newPage(browser, 1440);
  await openInbox(page);
  await ctx.addCookies([
    { name: "GCP_IAP_UID", value: "secret-edge-uid", url: BASE },
    { name: "__Host-GCP_IAP_AUTH_TOKEN_0123", value: "secret-edge-token", url: BASE, secure: true },
  ]);
  const before = await ctx.cookies(BASE);
  const session = before.find((c) => c.name === "__Host-grain_verified");
  await page.goto(`${BASE}/staging/preview/plant`);
  const after = await ctx.cookies(`${BASE}/staging/preview/x`);
  const planted = after.filter((c) => c.value.startsWith("planted-"));
  const bad = planted.filter((c) => /^(__secure-)?(grain_|gcp_iap|gcp_iaap)|^__host-/i.test(c.name) || !c.path.startsWith("/staging/preview"));
  const sessionAfter = (await ctx.cookies(BASE)).find((c) => c.name === "__Host-grain_verified");
  record("1", "planted cookies in the jar", bad.length === 0, planted.map((c) => `${c.name} path=${c.path} domain=${c.domain}`));
  record("1", "operator session survives", !!session && !!sessionAfter && sessionAfter.path === "/", {
    before: !!session,
    after: !!sessionAfter,
    reissuedByGrain: session?.value !== sessionAfter?.value,
  });
  const echo = await page.goto(`${BASE}/staging/preview/echo`).then((r) => r.json());
  const sent = echo.cookieLines.join("; ");
  record("1", "browser echo cookies", !/grain_|gcp_ia|__host-/i.test(sent), sent);
  const names = echo.headers.map(([k]) => k);
  record("1", "browser echo header names", !names.some((n) => /assertion|authorization|x-goog|x-grain-(access|tunnel)/i.test(n)), names);
  fs.writeFileSync(path.join(OUT, "item1-browser-echo.json"), JSON.stringify(echo, null, 1).replace(/secret-[\w.-]*/g, "secret-REDACTED"));
  // The inbox still works with the same session after the staging visit.
  await openInbox(page);
  record("1", "grain still signed in after staging", true, page.url().replace(BASE, ""));
  await ctx.close();
}

async function item5(browser) {
  for (const saveData of [false, true]) {
    const { ctx, page, log } = await newPage(browser, 1440, { saveData });
    await openInbox(page);
    // The shell renders before the routed page's own chunk resolves: count
    // from the inbox page itself, or its chunk and imports read as "late".
    await page.locator("g-inbox-page").first().waitFor({ state: "attached", timeout: 60000 });
    await page.waitForTimeout(250);
    const loadedAt = log.chunks.length;
    await page.waitForTimeout(6000);
    const late = log.chunks.slice(loadedAt).map((c) => c.url);
    const sd = await page.evaluate(() => navigator.connection?.saveData);
    if (saveData) record("5", "Save-Data: lazy chunks fetched in 6s idle", late.length === 0 && sd === true, { saveData: sd, late });
    else record("5", "baseline: lazy chunks fetched in 6s idle", late.length > 0, { saveData: sd, count: late.length });
    await ctx.close();
  }
}

async function shot(page, name) {
  await page.screenshot({ path: path.join(OUT, `${name}.png`) });
}

async function item3(browser, width) {
  const tag = `${width}`;
  // 3a. unpreloaded routes: Save-Data keeps them unpreloaded across the swap.
  swap("A");
  {
    const { ctx, page, log } = await newPage(browser, width, { saveData: true });
    await openInbox(page);
    swap("B");
    for (const route of ["/tasks", "/agents", "/processes"]) {
      const before = page.url();
      await clickRoute(page, route);
      const text = await snackbar(page);
      const stayed = page.url() === before;
      await shot(page, `item3-${tag}-stale-${route.slice(1)}`);
      if (text) {
        const vp = page.viewportSize();
        const box = await page.locator('[data-testid="snackbar"]').first().boundingBox();
        const act = await page.locator('[data-testid="snackbar-action"]').first().boundingBox();
        const inside = (b) => b && b.x >= 0 && b.y >= 0 && b.x + b.width <= vp.width && b.y + b.height <= vp.height;
        record("3", `${tag}px snackbar ${route} inside viewport, Reload inside snackbar`, inside(box) && inside(act) && act.x >= box.x && act.x + act.width <= box.x + box.width, { box, act });
      }
      record("3", `${tag}px stale route ${route}: snackbar, stays put`, !!text && stayed, { text, url: page.url().replace(BASE, "") });
      if (route === "/processes") {
        const action = page.locator('[data-testid="snackbar-action"]').first();
        await Promise.all([page.waitForURL(`**/next${route}**`, { timeout: 30000 }), action.click()]);
        await page.locator('[data-testid="help-trigger"]').first().waitFor({ state: "attached", timeout: 60000 });
        await page.waitForTimeout(1500);
        await shot(page, `item3-${tag}-reloaded-processes`);
        record("3", `${tag}px Reload lands on ${route}`, page.url().includes(`/next${route}`), page.url().replace(BASE, ""));
      } else {
        await page.locator('[data-testid="snackbar"]').first().waitFor({ state: "hidden", timeout: 15000 }).catch(() => {});
      }
    }
    record("3", `${tag}px no unhandled rejection (routes)`, unhandled(log).length === 0, {
      unhandled: unhandled(log),
      notFound: log.chunks.filter((c) => c.status === 404).map((c) => c.url),
    });
    await ctx.close();
  }
  // 3b. @defer: Help has no prefetch, New task prefetches only when idle and
  // not on Save-Data -- walk both, after a swap.
  swap("A");
  for (const saveData of [false, true]) {
    const { ctx, page, log } = await newPage(browser, width, { saveData });
    await openInbox(page);
    await page.waitForTimeout(5000); // let the idle preloads run on A
    swap("B");
    await page.locator('[data-testid="help-trigger"]:visible').first().click();
    const helpText = await snackbar(page);
    await shot(page, `item3-${tag}-defer-help${saveData ? "-savedata" : ""}`);
    record("3", `${tag}px @defer Help after swap${saveData ? " (Save-Data)" : ""}`, !!helpText && /could not load/i.test(helpText), helpText);
    await page.keyboard.press("Escape");
    await page.locator('[data-testid="snackbar"]').first().waitFor({ state: "hidden", timeout: 15000 }).catch(() => {});
    const fab = page.locator('[data-testid="rail-new-task"]:visible, [data-testid="phone-new-task"]:visible').first();
    await fab.click();
    const sheetText = await snackbar(page, 5000);
    const sheet = await page.locator("g-new-task-sheet").count();
    await shot(page, `item3-${tag}-defer-newtask${saveData ? "-savedata" : ""}`);
    if (saveData) {
      record("3", `${tag}px @defer New task after swap (Save-Data, not prefetched)`, !!sheetText && /could not load/i.test(sheetText), sheetText);
    } else {
      record("3", `${tag}px @defer New task after swap (idle-prefetched on A)`, sheet > 0, { sheet, snackbar: sheetText });
    }
    if (sheetText) {
      const action = page.locator('[data-testid="snackbar-action"]').first();
      await Promise.all([page.waitForEvent("load", { timeout: 30000 }), action.click()]);
      await page.locator('[data-testid="help-trigger"]').first().waitFor({ state: "attached", timeout: 60000 });
      record("3", `${tag}px @defer Reload reloads current page`, page.url().includes("/next/inbox"), page.url().replace(BASE, ""));
    }
    record("3", `${tag}px no unhandled rejection (@defer${saveData ? ", Save-Data" : ""})`, unhandled(log).length === 0, unhandled(log));
    await ctx.close();
    swap("A");
  }
}

async function classic(browser) {
  // Classic / is unchanged between the builds (only ui-ng changed), so the
  // swap must leave it working exactly as before.
  swap("A");
  const { ctx, page, log } = await newPage(browser, 1440);
  await page.goto(`${BASE}/`, { waitUntil: "load", timeout: 60000 });
  await page.waitForTimeout(3000);
  swap("B");
  await page.goto(`${BASE}/tasks`, { waitUntil: "load", timeout: 60000 });
  await page.waitForTimeout(3000);
  await shot(page, "item3-classic-after-swap");
  const failed = log.chunks.filter((c) => c.status >= 400);
  record("3", "classic / across the swap", unhandled(log).length === 0 && failed.length === 0, {
    unhandled: unhandled(log),
    title: await page.title(),
  });
  await ctx.close();
}

const browser = await chromium.launch();
const only = (process.env.ONLY || "1,5,3,classic").split(",");
try {
  if (only.includes("1")) await item1(browser);
  if (only.includes("5")) await item5(browser);
  if (only.includes("3")) for (const w of (process.env.WIDTHS || "1440,390").split(",").map(Number)) await item3(browser, w);
  if (only.includes("classic")) await classic(browser);
} catch (e) {
  record("-", "walk aborted", false, String(e).slice(0, 500));
} finally {
  await browser.close();
  fs.writeFileSync(path.join(OUT, "results.json"), JSON.stringify(results, null, 1));
  swap("A");
}
process.exit(results.some((r) => !r.ok) ? 1 : 0);
