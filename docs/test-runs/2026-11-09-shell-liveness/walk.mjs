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

// docs/test-plans/2026-11-09.md §5 ("Shell liveness"), items 1-5, in
// Playwright against a real `grain demo` process that this script
// SIGSTOPs/SIGCONTs, and against proxies in front of it (proxy.mjs and
// socat). See run.sh for how the demo and the proxies are started.
//
//   cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-09-shell-liveness/walk.mjs \
//     <stop|proxies|slow|mutations|phone|shots> DEMO_URL DEMO_PID CODE OUT_DIR
import { createRequire } from "node:module";
import { execSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";

const require = createRequire(`${process.cwd()}/`);
const { chromium, webkit } = require("@playwright/test");

const [mode, demo, pidArg, code, outDir] = process.argv.slice(2);
const pid = Number(pidArg);
mkdirSync(outDir, { recursive: true });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const results = [];
const check = (id, desc, ok, got) => {
  results.push({ id, desc, ok: Boolean(ok), got });
  console.log(`${ok ? "PASS" : "FAIL"}\t${id}\t${desc}\t${JSON.stringify(got)}`);
};
const note = (id, desc, got) => {
  results.push({ id, desc, note: true, got });
  console.log(`NOTE\t${id}\t${desc}\t${JSON.stringify(got)}`);
};

const rest = (method, path, body) =>
  fetch(`${demo}${path}`, {
    method,
    headers: { Authorization: `Bearer ${code}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  }).then(async (r) => ({ status: r.status, body: await r.json().catch(() => null) }));

async function cookies() {
  const res = await fetch(`${demo}/api/auth/access-code`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code }),
  });
  return res.headers.getSetCookie().map((h) => {
    const [pair] = h.split(";");
    const at = pair.indexOf("=");
    return {
      name: pair.slice(0, at).trim(),
      value: pair.slice(at + 1).trim(),
      domain: "127.0.0.1",
      path: "/",
      expires: -1,
      httpOnly: true,
      secure: false,
      sameSite: "Strict",
    };
  });
}

async function newContext(browserType = chromium, opts = {}) {
  const browser = await browserType.launch();
  const context = await browser.newContext(opts);
  await context.addCookies(await cookies());
  return { browser, context };
}

// track counts GET /api/config (and every /api/ request) in flight, and
// the most of each at once since the last reset().
function track(page) {
  const t = { config: 0, api: 0, maxConfig: 0, maxApi: 0, configSent: 0, apiByPath: {} };
  const isConfig = (req) => req.method() === "GET" && /\/api\/config$/.test(new URL(req.url()).pathname);
  const isApi = (req) => new URL(req.url()).pathname.includes("/api/");
  page.on("request", (req) => {
    if (isConfig(req)) {
      t.config += 1;
      t.configSent += 1;
      t.maxConfig = Math.max(t.maxConfig, t.config);
    }
    if (isApi(req)) {
      t.api += 1;
      t.maxApi = Math.max(t.maxApi, t.api);
      const p = `${req.method()} ${new URL(req.url()).pathname}`;
      t.apiByPath[p] = (t.apiByPath[p] || 0) + 1;
    }
  });
  const done = (req) => {
    if (isConfig(req)) t.config -= 1;
    if (isApi(req)) t.api -= 1;
  };
  page.on("requestfinished", done);
  page.on("requestfailed", done);
  t.reset = () => {
    t.maxConfig = t.config;
    t.maxApi = t.api;
    t.configSent = 0;
    t.apiByPath = {};
  };
  return t;
}

const banner = (page) => page.getByTestId("disconnected-banner");

// One silent-daemon round on one page: stop(), time the banner, keep it
// stopped `hold` ms more counting requests, cont(), time the clear.
async function silentRound(id, page, url, stop, cont, { hold = 20_000, path } = {}) {
  const t = track(page);
  await page.goto(url);
  await page.waitForLoadState("networkidle").catch(() => {});
  await sleep(4000);
  await page.evaluate(() => (window.__noReload = 1));
  check(`${id}.before`, `${path}: no banner before the stop`, (await banner(page).count()) === 0, null);
  t.reset();
  await stop();
  const t0 = Date.now();
  let up = null;
  try {
    await banner(page).waitFor({ state: "visible", timeout: 70_000 });
    up = Date.now() - t0;
  } catch {}
  check(`${id}.banner`, `${path}: banner rises within 10 s timeout x2 + poll ticks (<= 35 s)`, up !== null && up <= 35_000, { ms: up });
  const text = up !== null ? await banner(page).innerText() : null;
  note(`${id}.text`, `${path}: banner text`, text);
  await sleep(hold);
  check(`${id}.oneConfig`, `${path}: at most one GET /api/config in flight at a time while stopped`, t.maxConfig <= 1, {
    maxConfig: t.maxConfig,
    configSent: t.configSent,
  });
  note(`${id}.pileup`, `${path}: most /api/ requests in flight at once while stopped, and what was sent`, {
    maxApi: t.maxApi,
    inFlightAtCont: t.api,
    sent: t.apiByPath,
  });
  await page.screenshot({ path: `${outDir}/${id}-stopped.png` });
  await cont();
  const t1 = Date.now();
  let clear = null;
  try {
    await banner(page).waitFor({ state: "detached", timeout: 60_000 });
    clear = Date.now() - t1;
  } catch {}
  check(`${id}.clear`, `${path}: banner clears with no reload after CONT`, clear !== null, { ms: clear });
  check(`${id}.noReload`, `${path}: page was not reloaded`, (await page.evaluate(() => window.__noReload)) === 1, null);
  await sleep(3000);
  note(`${id}.after`, `${path}: /api/ still in flight 3 s after the clear`, { api: t.api, config: t.config });
}

const signal = (sig) => () => process.kill(pid, sig);

async function stopMode() {
  const { browser, context } = await newContext(chromium, { viewport: { width: 1440, height: 900 } });
  for (const [i, path] of ["/", "/tasks", "/tasks/1", "/session/director"].entries()) {
    const page = await context.newPage();
    await silentRound(`stop${i + 1}`, page, `${demo}${path}`, signal("SIGSTOP"), signal("SIGCONT"), { path });
    await page.close();
  }
  await browser.close();
}

const setProxy = (port, body) =>
  fetch(`http://127.0.0.1:${port}/_proxy/mode`, { method: "POST", body: JSON.stringify(body) }).then((r) => r.json());

async function proxiesMode() {
  const { browser, context } = await newContext(chromium, { viewport: { width: 1440, height: 900 } });
  // socat in front (run.sh: 4292), SIGSTOPped: the kernel still completes
  // the handshake into the listen backlog, and nothing is ever read.
  let page = await context.newPage();
  await silentRound(
    "socat",
    page,
    "http://127.0.0.1:4292/tasks",
    () => execSync("pkill -STOP -x socat"),
    () => execSync("pkill -CONT -x socat"),
    { path: "socat stopped" },
  );
  await page.close();
  for (const [id, m] of [
    ["stall", "stall"],
    ["headers", "headers"],
  ]) {
    page = await context.newPage();
    await silentRound(
      id,
      page,
      "http://127.0.0.1:4291/tasks",
      () => setProxy(4291, { mode: m }),
      () => setProxy(4291, { mode: "forward" }),
      { path: `proxy ${m}` },
    );
    await page.close();
  }
  // Sub-path: proxy.mjs -prefix /grain on 4293 (strict: nothing outside
  // /grain is forwarded).
  page = await context.newPage();
  const seen = [];
  page.on("request", (r) => seen.push(new URL(r.url()).pathname));
  await page.goto("http://127.0.0.1:4293/grain/tasks");
  await sleep(5000);
  const outside = [...new Set(seen.filter((p) => !p.startsWith("/grain")))];
  const loaded = await page.locator(".app-shell").count();
  note("prefix.strict", "UI under /grain/ behind a strict path-prefix proxy: shell mounted? requests outside the prefix", {
    shellMounted: loaded > 0,
    outside: outside.slice(0, 10),
  });
  await page.screenshot({ path: `${outDir}/prefix-strict.png` });
  await page.close();
  // With -passthrough (4294): the prefix strips, and root paths pass, as
  // a proxy that mounts grain at /grain and also routes /api and /assets.
  page = await context.newPage();
  await silentRound(
    "prefix",
    page,
    "http://127.0.0.1:4294/grain/tasks",
    () => setProxy(4294, { mode: "stall" }),
    () => setProxy(4294, { mode: "forward" }),
    { path: "prefix proxy stall" },
  );
  await page.close();
  await browser.close();
}

// recover: the proxy stalls, then starts forwarding new requests again
// but never lets go of the ones it held (half-open backend connections).
// Over HTTP/1.1 a browser opens at most six connections to one host, so
// whether the shell recovers depends on whether its held reads are ever
// given up on.
async function recoverMode() {
  const { browser, context } = await newContext(chromium, { viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  const t = track(page);
  await page.goto("http://127.0.0.1:4291/tasks");
  await sleep(4000);
  await setProxy(4291, { mode: "stall" });
  t.reset();
  const t0 = Date.now();
  await banner(page).waitFor({ state: "visible", timeout: 70_000 });
  note("recover.banner", "banner up after the stall", { ms: Date.now() - t0 });
  await sleep(Number(process.env.HOLD_MS || 30_000));
  const held = await setProxy(4291, { mode: "recover" });
  note("recover.held", "requests the proxy is still holding when new ones start being answered", {
    held: held.held,
    browserInFlight: t.api,
    sent: t.apiByPath,
  });
  const t1 = Date.now();
  let clear = null;
  try {
    await banner(page).waitFor({ state: "detached", timeout: 90_000 });
    clear = Date.now() - t1;
  } catch {}
  check("recover.clear", "banner clears once the proxy answers new requests, with its old ones still held", clear !== null, {
    ms: clear,
    browserInFlight: t.api,
    configInFlight: t.config,
  });
  await page.screenshot({ path: `${outDir}/recover-${clear === null ? "stuck" : "cleared"}.png` });
  await setProxy(4291, { mode: "forward" });
  await browser.close();
}

async function slowMode() {
  const { browser, context } = await newContext(chromium, { viewport: { width: 1440, height: 900 } });
  for (const delayMs of [8000, 9500]) {
    const page = await context.newPage();
    const t = track(page);
    await page.goto("http://127.0.0.1:4291/tasks");
    await sleep(4000);
    await setProxy(4291, { mode: "delay", delayMs });
    t.reset();
    const flips = [];
    let last = false;
    const end = Date.now() + 120_000;
    while (Date.now() < end) {
      const now = (await banner(page).count()) > 0;
      if (now !== last) flips.push({ at: Math.round((120_000 - (end - Date.now())) / 1000), up: now });
      last = now;
      await sleep(500);
    }
    check(`slow${delayMs}`, `every /api/ answer delayed ${delayMs} ms: banner never rises over 120 s`, flips.length === 0, {
      flips,
      maxConfig: t.maxConfig,
      configSent: t.configSent,
      maxApi: t.maxApi,
    });
    await setProxy(4291, { mode: "forward" });
    await page.close();
  }
  await browser.close();
}

// visible is what a reader of the page sees of a mutation's fate: alerts
// and toasts, the New task dialog's submit button, the composer.
const visible = async (p) => ({
  alerts: (await p.locator('[role="alert"], .MuiSnackbar-root, .error-banner').allInnerTexts()).map((t) => t.slice(0, 200)),
  createButton: await p
    .getByRole("dialog")
    .getByRole("button", { name: /Create task|Creating/ })
    .evaluateAll((els) => els.map((e) => ({ text: e.innerText, disabled: e.disabled }))),
  composer: await p.locator(".cs-message-input__content-editor").evaluateAll((els) => els.map((e) => e.innerText)),
});

async function mutationsMode() {
  const { browser, context } = await newContext(chromium, { viewport: { width: 1440, height: 900 } });
  const stamp = Date.now();
  const title = `Stopped-daemon task ${stamp}`;
  const message = `stopped-daemon message ${stamp}`;
  // A: New task form, filled.
  const a = await context.newPage();
  await a.goto(`${demo}/tasks`);
  await a.getByRole("button", { name: "+ New task" }).first().click();
  const dialog = a.getByRole("dialog");
  await dialog.getByLabel(/^Title/).fill(title);
  // Repos -> "none" (ui/e2e/searchSelect.js's chooseOption, inlined).
  const repos = dialog.getByLabel(/^Repos/);
  if ((await repos.getAttribute("aria-expanded")) !== "true") await repos.click();
  const list = `[id="${await repos.getAttribute("aria-controls")}"] [role="option"]`;
  await a.locator(`${list}[data-value="none"], ${list}[data-label="none"]`).first().click();
  await dialog.getByRole("button", { name: "Create task" }).waitFor();
  note("mut.createEnabled", "Create task enabled before the stop", await dialog.getByRole("button", { name: "Create task" }).isEnabled());
  // B: the director session's composer, filled.
  const b = await context.newPage();
  await b.goto(`${demo}/session/director`);
  const composer = b.locator(".cs-message-input__content-editor");
  await composer.waitFor();
  await composer.fill(message);
  // C: Settings -> General -> Environment name, saved with Enter.
  const c = await context.newPage();
  await c.goto(`${demo}/settings/general`);
  await sleep(3000);
  const before = (await rest("GET", "/api/config")).body;
  const fieldName = process.env.SETTINGS_FIELD || "Environment name";
  const field = c.getByLabel(fieldName, { exact: false }).first();
  const fieldValue = `walk-${stamp}`;
  let haveField = (await field.count()) > 0;
  if (haveField) await field.fill(fieldValue);
  note("mut.field", "Settings field used", { fieldName, haveField });

  process.kill(pid, "SIGSTOP");
  const posts = [];
  context.on("request", (r) => {
    if (r.method() !== "GET") posts.push(`${r.method()} ${new URL(r.url()).pathname}`);
  });
  await dialog.getByRole("button", { name: "Create task" }).click();
  await composer.press("Enter");
  if (haveField) {
    const save = c.getByRole("button", { name: /^Save/ }).first();
    if (await save.count()) await save.click();
    else await field.press("Enter");
  }
  await sleep(40_000);
  for (const [n, p] of [
    ["newtask", a],
    ["session", b],
    ["settings", c],
  ]) {
    await p.screenshot({ path: `${outDir}/mut-${n}-stopped.png` });
    note(`mut.${n}.stopped`, `${n}: banner up while stopped? and the busy controls`, {
      banner: (await banner(p).count()) > 0 ? await banner(p).innerText() : null,
      busy: await p.locator("[aria-busy=true], .MuiCircularProgress-root, button:disabled").count(),
      ...(await visible(p)),
    });
  }
  note("mut.sent", "non-GET requests sent while stopped", posts);
  process.kill(pid, "SIGCONT");
  await sleep(20_000);
  for (const [n, p] of [
    ["newtask", a],
    ["session", b],
    ["settings", c],
  ]) {
    await p.screenshot({ path: `${outDir}/mut-${n}-after.png` });
    note(`mut.${n}.after`, `${n}: what is on screen 20 s after CONT`, await visible(p));
  }
  const tasks = (await rest("GET", "/api/tasks")).body;
  const filed = tasks.filter((t) => t.title === title);
  check("mut.newtask", "New task created exactly once after CONT", filed.length === 1, { count: filed.length, ids: filed.map((t) => t.id) });
  note("mut.newtask.dialog", "New task dialog still open after CONT?", { open: await dialog.count() });
  // The composer posts to the session's own task (POST /api/tasks/<id>/comments).
  const sessionTask = (posts.find((p) => /\/comments$/.test(p)) || "").match(/\/api\/tasks\/(\d+)\//)?.[1];
  const sess = sessionTask ? (await rest("GET", `/api/tasks/${sessionTask}`)).body : null;
  const hits = (sess?.comments || []).filter((cm) => (cm.body || cm.text || "").includes(message)).length;
  check("mut.session", "session message delivered exactly once after CONT", hits === 1, { occurrences: hits });
  const posted = posts.filter((p) => /\/api\/(tasks|console-sessions)/.test(p));
  note("mut.posted", "mutations sent (each should be once)", posted);
  if (haveField) {
    const after = (await rest("GET", "/api/config")).body;
    note("mut.settings", "config after CONT differs from before?", {
      changed: JSON.stringify(after) !== JSON.stringify(before),
      hasValue: JSON.stringify(after).includes(fieldValue),
    });
  }
  await browser.close();
}

// First-frame shell sampler: every animation frame, compare <html
// data-phone> with the media query, PhoneNav's AppBar, the desktop rail
// and the page-header gutter, and keep every frame they disagree in.
const SAMPLER = () => {
  const Q = "(max-width: 599.95px), (max-height: 480px) and (orientation: landscape)";
  window.__frames = { n: 0, bad: [], first: null };
  const tick = () => {
    const de = document.documentElement;
    // Only frames with a nav in them: the loading splash inside
    // .app-shell has neither PhoneNav's AppBar nor the rail.
    const shell = document.querySelector(".app-shell header.MuiAppBar-root, .app-body > aside");
    if (shell) {
      const attr = de.dataset.phone === "true";
      const mq = matchMedia(Q).matches;
      const appBar = !!document.querySelector(".app-shell header.MuiAppBar-root");
      const aside = document.querySelector(".app-body > aside, .app-shell aside:not(.MuiDrawer-paper *)");
      const rail = aside ? aside.getBoundingClientRect() : null;
      const railShown = !!rail && rail.width > 40 && rail.height > 100 && !aside.closest(".MuiDrawer-root");
      // The page's left gutter: where the first task row starts.
      const row = document.querySelector(".task-row");
      const gutter = row ? Math.round(row.getBoundingClientRect().left) : null;
      const s = {
        w: innerWidth,
        h: innerHeight,
        attr,
        mq,
        appBar,
        railShown,
        gutter,
        theme: de.dataset.theme,
        sidebarWidth: de.style.getPropertyValue("--sidebar-width"),
      };
      window.__frames.n += 1;
      if (!window.__frames.first) window.__frames.first = s;
      if (!window.__frames.firstRow && gutter !== null) window.__frames.firstRow = s;
      if (attr !== mq || appBar !== attr || railShown === attr) window.__frames.bad.push(s);
      window.__frames.last = s;
    }
    requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
};

async function phoneMode() {
  for (const [name, type] of [
    ["chromium", chromium],
    ["webkit", webkit],
  ]) {
    const { browser, context } = await newContext(type, { viewport: { width: 1440, height: 900 } });
    await context.addInitScript(SAMPLER);
    const page = await context.newPage();
    const sizes = [];
    for (let i = 0; i < 5; i++) sizes.push([1440, 900], [390, 844]);
    for (let i = 0; i < 5; i++) sizes.push([844, 390], [390, 844]);
    const firstBad = [];
    const steady = {};
    // Reload at each width, both themes.
    for (const theme of ["light", "dark"]) {
      await page.goto(`${demo}/tasks`);
      await page.evaluate((t) => localStorage.setItem("grain.themeMode", t), theme);
      for (const [w, h] of sizes) {
        await page.setViewportSize({ width: w, height: h });
        await page.reload();
        await page.locator(".task-row").first().waitFor();
        await sleep(300);
        const f = await page.evaluate(() => window.__frames);
        if (f.bad.length) firstBad.push({ theme, w, h, bad: f.bad.slice(0, 3) });
        if (!f.first) firstBad.push({ theme, w, h, noFrames: f.n });
        else if (f.first.theme !== theme) firstBad.push({ theme, w, h, firstTheme: f.first.theme });
        const key = `${w}x${h}`;
        steady[key] = steady[key] || f.last.gutter;
        const firstRow = f.firstRow;
        if (firstRow && firstRow.gutter !== f.last.gutter) firstBad.push({ theme, w, h, firstGutter: firstRow.gutter, last: f.last.gutter });
      }
    }
    check(`phone.${name}.reload`, `${name}: every frame after a reload at each width (x20, light+dark) has data-phone == media query == AppBar == !rail, theme and gutter right`, firstBad.length === 0, { bad: firstBad.slice(0, 5), steady });
    // Live resizes with no reload: the sampler keeps running.
    await page.evaluate(() => (window.__frames.bad = []));
    const live = [];
    for (const [w, h] of sizes) {
      await page.setViewportSize({ width: w, height: h });
      await sleep(250);
      const f = await page.evaluate(() => window.__frames);
      if (f.bad.length) live.push({ w, h, bad: f.bad.slice(0, 3) });
      await page.evaluate(() => (window.__frames.bad = []));
    }
    // Bounding boxes at the end of each orientation.
    for (const [w, h] of [
      [390, 844],
      [844, 390],
      [1440, 900],
    ]) {
      await page.setViewportSize({ width: w, height: h });
      await sleep(300);
      const box = await page.evaluate(() => {
        const bar = document.querySelector(".app-shell header.MuiAppBar-root");
        const b = bar ? bar.getBoundingClientRect() : null;
        const aside = document.querySelector(".app-body > aside");
        const a = aside ? aside.getBoundingClientRect() : null;
        return { phone: document.documentElement.dataset.phone, bar: b && { x: b.x, y: b.y, w: b.width }, rail: a && { x: a.x, w: a.width } };
      });
      note(`phone.${name}.box.${w}x${h}`, `${name} ${w}x${h}: AppBar and rail boxes`, box);
    }
    check(`phone.${name}.live`, `${name}: no frame disagrees during 20 live resizes/rotations`, live.length === 0, live.slice(0, 5));
    await browser.close();
  }
}

// Screenshots of the banner with the demo stopped.
async function shotsMode() {
  const pages = [
    ["home", "/"],
    ["tasks", "/tasks"],
    ["task", "/tasks/1"],
    ["session", "/session/director"],
  ];
  const combos = [];
  for (const theme of ["light", "dark"])
    for (const [vw, vh] of [
      [1440, 900],
      [390, 844],
    ])
      combos.push({ theme, vw, vh });
  const { browser, context } = await newContext(chromium);
  const opened = [];
  for (const { theme, vw, vh } of combos) {
    for (const [n, p] of pages) {
      const page = await context.newPage();
      await page.setViewportSize({ width: vw, height: vh });
      await page.goto(`${demo}${p}`);
      await page.evaluate((t) => localStorage.setItem("grain.themeMode", t), theme);
      await page.reload();
      await page.locator(".app-shell").waitFor();
      opened.push({ page, name: `${n}-${vw}-${theme}`, vw, vh });
    }
  }
  await sleep(3000);
  process.kill(pid, "SIGSTOP");
  try {
    for (const o of opened) await banner(o.page).waitFor({ state: "visible", timeout: 90_000 });
    for (const o of opened) {
      await o.page.screenshot({ path: `${outDir}/banner-${o.name}.png` });
      const geo = await o.page.evaluate(() => {
        const r = (el) => (el ? el.getBoundingClientRect().toJSON() : null);
        return {
          banner: r(document.querySelector('[data-testid="disconnected-banner"]')),
          appBar: r(document.querySelector(".app-shell header.MuiAppBar-root")),
          composer: r(document.querySelector(".cs-message-input")),
          scrollW: document.documentElement.scrollWidth,
        };
      });
      const overlap = (x, y) => x && y && x.left < y.right && y.left < x.right && x.top < y.bottom && y.top < x.bottom;
      check(`shot.${o.name}`, `${o.name}: banner covers neither the phone nav nor the composer, no horizontal overflow`,
        !overlap(geo.banner, geo.appBar) && !overlap(geo.banner, geo.composer) && geo.scrollW <= o.vw,
        geo);
    }
  } finally {
    process.kill(pid, "SIGCONT");
  }
  await browser.close();
}

const modes = { stop: stopMode, proxies: proxiesMode, recover: recoverMode, slow: slowMode, mutations: mutationsMode, phone: phoneMode, shots: shotsMode };
try {
  await modes[mode]();
} finally {
  try {
    process.kill(pid, "SIGCONT");
  } catch {}
  writeFileSync(`${outDir}/${mode}-results.json`, JSON.stringify(results, null, 2));
}
