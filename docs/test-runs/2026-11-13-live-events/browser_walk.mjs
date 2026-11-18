#!/usr/bin/env node
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

// browser_walk.mjs: the browser half of docs/test-plans/2026-11-13-afternoon.md
// §2 against a live daemon (browser_walk.sh starts it and passes its ids).
//
// Item 2 -- /next/inbox live updates. For each of 1440x900 light, 1440x900
// dark, 390x844 light and 390x844 dark, an operator page and a
// tasks:read@acme/widgets viewer page sit on /next/inbox while the walk
// raises, re-raises (critical), acks and clears a notification on the
// widgets task, raises two the viewer may not read (acme/secret,
// deployment-wide), and posts four host-script approvals settled by
// approve, deny, the caller's /expire and a sweep past a 2 s deadline.
// Each step records how long the row took to appear / change / leave and
// which /api reads the page made meanwhile: the inbox's timer is 60 s, so
// a change inside 2 s came from the /api/events stream.
//
// Item 3 -- a 3,000-comment task opened at #comment=<10th comment> on
// /next/task/<id>, /next/tasks/<id> (redirect), an in-page hash change and
// classic /tasks/<id>: comment-page requests, how many times the thread's
// message count changed (1 = rendered once), whether comment 10 is in
// view, focused and highlighted, and how far the thread scrolled after.
//
// Writes summary.json and screenshots to --out; exits 1 on any failed
// check unless --allow-failures.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../../..");
const { chromium } = createRequire(path.join(repoRoot, "ui-ng/package.json"))(
  "@playwright/test",
);

const argv = process.argv.slice(2);
const arg = (name, def) => {
  const i = argv.indexOf(`--${name}`);
  return i >= 0 ? argv[i + 1] : def;
};
const url = arg("url");
const token = arg("token");
const viewerUrl = arg("viewer-url");
const viewerEmail = arg("viewer-email");
const widgetsTask = arg("widgets-task");
const longTask = arg("long-task");
const out = arg("out", path.join(here, "out/browser"));
const allowFailures = argv.includes("--allow-failures");
const LIVE_MS = 2000;
const COMMENTS = 3000;

const failures = [];
const check = (ok, what) => {
  if (!ok) failures.push(what);
  return ok;
};

async function api(tok, method, p, body) {
  const r = await fetch(url + p, {
    method,
    headers: {
      Authorization: `Bearer ${tok}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await r.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    json = text;
  }
  if (!r.ok) throw new Error(`${method} ${p}: ${r.status} ${text}`);
  return json;
}
const deadline = (s) => new Date(Date.now() + s * 1000).toISOString();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// watch records the /api GETs and 4xx/5xx a page makes.
function watch(page) {
  const log = [];
  page.on("request", (req) => {
    const u = new URL(req.url());
    if (u.pathname.startsWith("/api/") && req.method() === "GET") {
      log.push({ at: Date.now(), path: u.pathname, query: u.search });
    }
  });
  const errors = [];
  page.on("response", (res) => {
    const u = new URL(res.url());
    if (u.pathname.startsWith("/api/") && res.status() >= 400) {
      errors.push(`${res.status()} ${u.pathname}${u.search}`);
    }
  });
  return { log, errors };
}

async function rowState(page, key) {
  return page.evaluate((k) => {
    const el = document.querySelector(`[data-testid="queue-row"][data-key="${k}"]`);
    if (!el) return null;
    return (el.textContent || "").replace(/\s+/g, " ").trim();
  }, key);
}

// waitRow polls the page until want(rowText) holds; answers ms taken.
async function waitRow(page, key, want, limit = 10000) {
  const t0 = Date.now();
  while (Date.now() - t0 < limit) {
    if (want(await rowState(page, key))) return Date.now() - t0;
    await sleep(25);
  }
  return null;
}

// openContext opens /next/inbox as the operator (the daemon's local token)
// or, with who = "viewer", as widgets@example.com through iapsim.
async function openContext(browser, who, viewport, scheme) {
  const viewer = who === "viewer";
  const ctx = await browser.newContext({
    viewport,
    colorScheme: scheme,
    extraHTTPHeaders: viewer
      ? { "X-Iapsim-Email": viewerEmail }
      : { Authorization: `Bearer ${token}` },
  });
  const page = await ctx.newPage();
  const w = watch(page);
  await page.goto(`${viewer ? viewerUrl : url}/next/inbox`);
  await page.waitForSelector('[data-testid="next-inbox-queue"], [data-testid="next-inbox-empty"]', {
    timeout: 30000,
  });
  await sleep(1000);
  return { ctx, page, ...w };
}

async function inboxWalk(browser, viewport, scheme, label) {
  const admin = await openContext(browser, "admin", viewport, scheme);
  const viewer = await openContext(browser, "viewer", viewport, scheme);
  const steps = [];
  const L = `/api/tasks/${widgetsTask}/local-script-approvals`;
  const subj = `walk.inbox.${label}`;

  // step: run act(), then wait for want(row) on page; record reads between.
  async function step(name, page, reads, key, act, want) {
    const from = reads.length;
    const t0 = Date.now();
    const ctx = await act();
    const k = typeof key === "function" ? key(ctx) : key;
    const ms = await waitRow(page, k, want);
    const during = reads.slice(from).map((r) => r.path);
    const s = { name, key: k, ms, reads: during.length, readPaths: [...new Set(during)] };
    steps.push(s);
    check(ms !== null && ms <= LIVE_MS, `${label}: ${name} took ${ms ?? "forever"} ms (want <= ${LIVE_MS})`);
    return { ctx, t0 };
  }

  const raise = (title, severity, extra = {}) =>
    api(token, "POST", "/api/notifications", { subject: subj, title, severity, task: widgetsTask, ...extra });
  const n1 = await step(
    "notification raised (warning)",
    admin.page,
    admin.log,
    (r) => `notification:${r.notification.id}`,
    () => raise(`${label} widgets red`, "warning"),
    (t) => !!t?.includes(`${label} widgets red`),
  );
  const nkey = `notification:${n1.ctx.notification.id}`;
  const vms = await waitRow(viewer.page, nkey, (t) => !!t, 3000);
  steps.push({ name: "viewer sees the widgets notification", key: nkey, ms: vms });
  check(vms !== null && vms <= LIVE_MS, `${label}: viewer saw the acme/widgets notification in ${vms} ms`);
  await step(
    "notification re-raised (critical)",
    admin.page,
    admin.log,
    nkey,
    () => raise(`${label} widgets very red`, "critical"),
    (t) => !!t?.includes("very red"),
  );
  // An ack keeps the row; it is live when the page has re-read the list.
  {
    const from = admin.log.length;
    const t0 = Date.now();
    await api(token, "POST", `/api/notifications/${n1.ctx.notification.id}/ack`, {});
    let ms = null;
    while (Date.now() - t0 < 10000) {
      if (admin.log.slice(from).some((r) => r.path === "/api/notifications")) {
        ms = Date.now() - t0;
        break;
      }
      await sleep(25);
    }
    steps.push({ name: "notification acked (list re-read)", key: nkey, ms, reads: admin.log.length - from });
    check(ms !== null && ms <= LIVE_MS, `${label}: ack re-read took ${ms} ms`);
  }
  await step(
    "notification cleared",
    admin.page,
    admin.log,
    nkey,
    () => api(token, "POST", `/api/notifications/${n1.ctx.notification.id}/clear`, {}),
    (t) => t === null,
  );
  // Out of the viewer's scope: both reach the operator, never the viewer.
  const secret = await step(
    "acme/secret notification raised",
    admin.page,
    admin.log,
    (r) => `notification:${r.notification.id}`,
    () => api(token, "POST", "/api/notifications", { subject: `${subj}.secret`, title: `${label} secret red`, repo: "acme/secret" }),
    (t) => !!t,
  );
  const global = await step(
    "deployment-wide notification raised",
    admin.page,
    admin.log,
    (r) => `notification:${r.notification.id}`,
    () => api(token, "POST", "/api/notifications", { subject: `${subj}.hosts`, title: `${label} hosts down`, severity: "critical" }),
    (t) => !!t,
  );
  await sleep(1500);
  const leaked = [];
  for (const k of [`notification:${secret.ctx.notification.id}`, `notification:${global.ctx.notification.id}`]) {
    if (await rowState(viewer.page, k)) leaked.push(k);
  }
  check(leaked.length === 0, `${label}: viewer shows out-of-scope rows ${leaked.join(", ")}`);
  for (const r of [secret, global]) {
    await api(token, "POST", `/api/notifications/${r.ctx.notification.id}/clear`, {});
  }

  const lsKey = (r) => `local-script:${widgetsTask}:${r.id}`;
  const gone = (t) => t === null;
  const post = (script, secs) => () =>
    api(token, "POST", L, { tool: "host_script", script, args: ["--walk", label], deadline: deadline(secs) });
  const a1 = await step("host-script pending (deploy)", admin.page, admin.log, lsKey, post("deploy.sh", 120), (t) => !!t);
  await step("approved", admin.page, admin.log, lsKey(a1.ctx), () => api(token, "POST", `${L}/${a1.ctx.id}/answer`, { approve: true }), gone);
  const a2 = await step("host-script pending (rollback)", admin.page, admin.log, lsKey, post("rollback.sh", 120), (t) => !!t);
  await step("denied", admin.page, admin.log, lsKey(a2.ctx), () => api(token, "POST", `${L}/${a2.ctx.id}/answer`, { approve: false }), gone);
  const a3 = await step("host-script pending (migrate)", admin.page, admin.log, lsKey, post("migrate.sh", 120), (t) => !!t);
  await step("caller /expire", admin.page, admin.log, lsKey(a3.ctx), () => api(token, "POST", `${L}/${a3.ctx.id}/expire`, {}), gone);
  const a4 = await step("host-script pending (slow, 2 s deadline)", admin.page, admin.log, lsKey, post("slow.sh", 2), (t) => !!t);
  // The sweep runs on a read of the queue; nothing reads the task here, so
  // the page itself must learn of the expiry from the event the sweep emits.
  await sleep(2200);
  await step(
    "sweep expiry (a task read sweeps it)",
    admin.page,
    admin.log,
    lsKey(a4.ctx),
    () => api(token, "GET", `/api/tasks/${widgetsTask}`),
    gone,
  );

  for (const [who, side] of [
    ["admin", admin],
    ["viewer", viewer],
  ]) {
    await side.page.screenshot({ path: path.join(out, `inbox-${label}-${who}.png`) });
  }
  const viewerForbidden = viewer.errors.filter((e) => e.startsWith("403"));
  check(viewerForbidden.length === 0, `${label}: viewer page drew 403s: ${viewerForbidden.slice(0, 5).join(", ")}`);
  const adminErrors = admin.errors.filter((e) => !e.includes("/api/whoami"));
  await admin.ctx.close();
  await viewer.ctx.close();
  return { label, steps, viewerForbidden, adminErrors, leaked };
}

// Records, every frame, how many messages the thread holds and its scroll.
const FRAME_PROBE = () => {
  window.__counts = [];
  window.__scrolls = [];
  const tick = () => {
    const n = document.querySelectorAll("article.msg[data-comment-id], [data-comment-id]").length;
    if (window.__counts.at(-1) !== n) window.__counts.push(n);
    const t = document.querySelector(".convo-thread, .chat-log, .task-log");
    if (t && window.__scrolls.at(-1) !== t.scrollTop) window.__scrolls.push(t.scrollTop);
    requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
};

async function deepLink(browser, name, open, targetId, settleMs = 6000) {
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    extraHTTPHeaders: { Authorization: `Bearer ${token}` },
  });
  await ctx.addInitScript(FRAME_PROBE);
  const page = await ctx.newPage();
  const w = watch(page);
  const t0 = Date.now();
  await open(page, w);
  const sel = `[data-comment-id="${targetId}"]`;
  let highlighted = false;
  let found = null;
  while (Date.now() - t0 < 30000) {
    found = await page.evaluate((s) => {
      const el = document.querySelector(s);
      if (!el) return null;
      const r = el.getBoundingClientRect();
      const a = document.activeElement;
      return {
        highlighted: el.classList.contains("comment-highlight"),
        inView: r.bottom > 0 && r.top < window.innerHeight,
        focused: a === el || (a instanceof Element && el.contains(a)),
      };
    }, sel);
    if (found?.highlighted) {
      highlighted = true;
      break;
    }
    await sleep(25);
  }
  const ms = Date.now() - t0;
  const revealedAt = Date.now();
  const scrollAtReveal = await page.evaluate(() => window.__scrolls.at(-1) ?? 0);
  await sleep(1500);
  const after = await page.evaluate(() => ({
    counts: window.__counts.filter((n) => n > 0),
    scrolls: window.__scrolls,
    url: location.pathname + location.hash,
  }));
  // Comment-page reads up to the reveal (the 3 s detail poll keeps
  // reading the newest 100 afterwards, which is not paging).
  const pages = w.log.filter(
    (r) =>
      r.at <= revealedAt &&
      /^\/api\/tasks\/[^/]+$/.test(r.path) &&
      /comments_(limit|before)/.test(r.query),
  );
  const shot = path.join(out, `deeplink-${name}.png`);
  await page.screenshot({ path: shot });
  await ctx.close();
  const res = {
    name,
    ms,
    commentRequests: pages.length,
    pagingRequests: pages.filter((r) => r.query.includes("comments_before=")).length,
    requests: pages.map((r) => r.query),
    renders: after.counts,
    url: after.url,
    highlighted,
    inView: !!found?.inView,
    focused: !!found?.focused,
    scrollAfterReveal: (after.scrolls.at(-1) ?? 0) - scrollAtReveal,
  };
  // ceil(3000/500) = 6 pages of 500 back from the newest comment.
  check(res.pagingRequests <= Math.ceil(COMMENTS / 500), `${name}: ${res.pagingRequests} comments_before pages (want <= 6)`);
  check(highlighted, `${name}: comment ${targetId} never highlighted`);
  check(res.inView, `${name}: comment ${targetId} not in view`);
  return res;
}

async function main() {
  await fs.mkdir(out, { recursive: true });
  const browser = await chromium.launch();
  const summary = { inbox: [], deepLinks: [] };
  for (const [vw, vh, scheme] of [
    [1440, 900, "light"],
    [1440, 900, "dark"],
    [390, 844, "light"],
    [390, 844, "dark"],
  ]) {
    const label = `${vw}-${scheme}`;
    console.error(`inbox walk ${label} ...`);
    summary.inbox.push(await inboxWalk(browser, { width: vw, height: vh }, scheme, label));
  }

  console.error(`seeding ${COMMENTS} comments on task ${longTask} ...`);
  const ids = [];
  for (let i = 1; i <= COMMENTS; i++) {
    const c = await api(token, "POST", `/api/tasks/${longTask}/comments`, { body: `walk comment ${i}` });
    ids.push(c.id ?? c.comment?.id);
  }
  const detail = await api(token, "GET", `/api/tasks/${longTask}?comments_limit=500`);
  const all = [];
  let cursor = null;
  let page = detail;
  for (;;) {
    all.unshift(...(page.comments ?? []));
    if (!page.olderComments) break;
    cursor = page.comments[0].id;
    page = await api(token, "GET", `/api/tasks/${longTask}?comments_limit=500&comments_before=${cursor}`);
  }
  const walkComments = all.filter((c) => /^walk comment \d+$/.test(c.body ?? ""));
  check(walkComments.length === COMMENTS, `seeded ${walkComments.length} walk comments, want ${COMMENTS}`);
  const c10 = walkComments[9].id;
  const c20 = walkComments[19].id;
  summary.comments = { task: longTask, total: all.length, tenth: c10, twentieth: c20 };

  summary.deepLinks.push(
    await deepLink(browser, "next-task", (p) => p.goto(`${url}/next/task/${longTask}#comment=${c10}`), c10),
  );
  const redirect = await deepLink(
    browser,
    "next-tasks-redirect",
    (p) => p.goto(`${url}/next/tasks/${longTask}#comment=${c10}`),
    c10,
  );
  check(redirect.url === `/next/task/${longTask}#comment=${c10}`, `redirect landed on ${redirect.url}`);
  summary.deepLinks.push(redirect);
  summary.deepLinks.push(
    await deepLink(
      browser,
      "next-in-page-hash",
      async (p, w) => {
        await p.goto(`${url}/next/task/${longTask}`);
        await p.waitForSelector("article.msg", { timeout: 30000 });
        await sleep(500);
        w.log.length = 0;
        await p.evaluate(() => (window.__counts = []));
        await p.evaluate((id) => (location.hash = `#comment=${id}`), c20);
      },
      c20,
    ),
  );
  summary.deepLinks.push(
    await deepLink(browser, "classic-tasks", (p) => p.goto(`${url}/tasks/${longTask}#comment=${c10}`), c10),
  );
  for (const d of summary.deepLinks.filter((d) => d.name.startsWith("next-") && d.name !== "next-in-page-hash")) {
    check(d.commentRequests <= Math.ceil(COMMENTS / 500), `${d.name}: ${d.commentRequests} comment reads in all (want <= 6)`);
    check(d.renders.length === 1, `${d.name}: thread rendered ${d.renders.length} times (${d.renders.join(" -> ")})`);
    check(d.focused, `${d.name}: linked comment not focused`);
  }
  await browser.close();
  summary.failures = failures;
  await fs.writeFile(path.join(out, "summary.json"), JSON.stringify(summary, null, 2) + "\n");
  for (const r of summary.inbox) {
    console.log(`== inbox ${r.label}`);
    for (const s of r.steps) console.log(`  ${String(s.ms).padStart(5)} ms  reads=${s.reads ?? "-"}  ${s.name}`);
  }
  for (const d of summary.deepLinks) {
    console.log(
      `== ${d.name}: ${d.commentRequests} comment reads (${d.pagingRequests} comments_before pages), renders ${d.renders.join("->")}, ` +
        `highlighted=${d.highlighted} inView=${d.inView} focused=${d.focused} scrollAfter=${d.scrollAfterReveal} ${d.ms} ms url=${d.url}`,
    );
  }
  console.log(failures.length ? `FAILURES:\n- ${failures.join("\n- ")}` : "all checks passed");
  if (failures.length && !allowFailures) process.exit(1);
}

main().catch((e) => {
  console.error(e);
  process.exit(2);
});
