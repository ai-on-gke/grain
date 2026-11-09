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

// 2026-11-08 §7 item 1: server-side closed-work owner / externalRef
// queries and admission narrowing + sorting, over a consolerestserver
// seeded by seed.mjs (+ legacy-admission.sh). Usage:
//   GRAIN_SERVER=http://127.0.0.1:<port> GRAIN_TOKEN=... VIEWER=... node narrowing-walk.mjs [label]
// Writes narrowing-<label>.json and screenshots beside itself.
import { chromium, AxeBuilder, VIEWPORTS, THEMES, recorder, signedIn, api, out, tag } from "./lib.mjs";

const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const VIEWER = process.env.VIEWER || "operator";
const label = process.argv[2] || "run";
const rec = recorder(label);
const seedNo = (s) => {
  const m = /seed (\d{4})/.exec(s || "");
  return m ? Number(m[1]) : null;
};

// track records every GET /api/tasks?state=closed a page makes.
function track(page) {
  const reqs = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (u.pathname !== "/api/tasks" || u.searchParams.get("state") !== "closed") return;
    reqs.push({
      at: Date.now(),
      cursor: u.searchParams.get("cursor") || "",
      search: u.searchParams.get("search") || "",
      owner: u.searchParams.get("owner") || "",
      externalRef: u.searchParams.get("externalRef") || "",
    });
  });
  return reqs;
}

async function rowSeeds(page) {
  const texts = await page.locator(".task-row").allInnerTexts();
  return texts.map(seedNo).filter((n) => n !== null);
}

async function settle(page, ms = 1500) {
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(ms);
}

// expectedSeeds asks the server itself, every page, for what a condition
// should list.
// closedMs is each seed's closedAt to the millisecond: the browser's
// Date holds no finer, so tasks seed.mjs closed within one millisecond
// tie there and may be drawn in either order.
const closedMs = new Map();
async function expectedSeeds(params) {
  const seeds = [];
  let cursor = "";
  for (;;) {
    const r = await api(S, T, "GET", `/api/tasks?state=closed&limit=200&cursor=${encodeURIComponent(cursor)}&${params}`);
    for (const t of r.body.tasks) {
      const n = seedNo(t.title);
      if (n === null) continue;
      seeds.push(n);
      closedMs.set(n, Date.parse(t.closedAt));
    }
    if (!r.body.nextCursor) return seeds;
    cursor = r.body.nextCursor;
  }
}

const CASES = [
  { name: "owner-me", f: ["owner:any:me"], params: `owner=${encodeURIComponent(VIEWER)}`, want: { owner: VIEWER } },
  {
    name: "owner-other",
    f: ["owner:any:other@example.com"],
    params: "owner=other%40example.com",
    want: { owner: "other@example.com" },
  },
  { name: "extref-inc99", f: ["externalRef:any:INC-99"], params: "externalRef=INC-99", want: { externalRef: "INC-99" } },
  {
    name: "owner-me-search",
    f: ["owner:any:me"],
    q: "kestrelmint",
    params: `owner=${encodeURIComponent(VIEWER)}&search=kestrelmint`,
    want: { owner: VIEWER, search: "kestrelmint" },
  },
  {
    name: "owner-me-durable",
    f: ["owner:any:me", "admission:any:durable"],
    params: `owner=${encodeURIComponent(VIEWER)}`,
    filterAdmission: "durable",
    want: { owner: VIEWER },
  },
];

const browser = await chromium.launch();

async function caseWalk(vp, theme, c) {
  const ctx = await signedIn(browser, S, T, vp, theme);
  const page = await ctx.newPage();
  const reqs = track(page);
  const qs = ["f=state:any:closed", ...c.f.map((f) => `f=${encodeURIComponent(f)}`)];
  if (c.q) qs.push(`q=${encodeURIComponent(c.q)}`);
  const t0 = Date.now();
  await page.goto(`/?${qs.join("&")}`);
  await page.locator(".task-row").first().waitFor({ timeout: 30000 });
  await settle(page);
  const firstPaint = Date.now() - t0;
  // Scroll the list's end into view a few times: a targeted query may
  // page its own answer, but must never page the plain newest-closed list.
  await page.mouse.move(vp.width * 0.6, vp.height * 0.6);
  for (let i = 0; i < 4; i++) {
    await page.mouse.wheel(0, 20000);
    await page.waitForTimeout(600);
  }
  await settle(page, 800);
  const targeted = reqs.filter((r) => r.owner || r.externalRef);
  const plainDeep = reqs.filter((r) => !r.owner && !r.externalRef && !r.search && r.cursor);
  const firstTargeted = targeted[0] || {};
  const shown = await rowSeeds(page);
  let expect = await expectedSeeds(c.params);
  if (c.filterAdmission) {
    const ok = new Set();
    let cursor = "";
    for (;;) {
      const r = await api(S, T, "GET", `/api/tasks?state=closed&limit=200&cursor=${encodeURIComponent(cursor)}&${c.params}`);
      r.body.tasks.filter((t) => (t.admission || "standard") === c.filterAdmission).forEach((t) => ok.add(seedNo(t.title)));
      if (!r.body.nextCursor) break;
      cursor = r.body.nextCursor;
    }
    expect = expect.filter((n) => ok.has(n));
  }
  const dupes = shown.length - new Set(shown).size;
  const strays = shown.filter((n) => !expect.includes(n));
  const order = shown.every((n, i) => i === 0 || closedMs.get(shown[i - 1]) >= closedMs.get(n));
  const t = `${c.name} ${tag(vp, theme)}`;
  rec.check(`${t}: one targeted closed request first`, targeted.length >= 1 &&
    Object.entries(c.want).every(([k, v]) => firstTargeted[k] === v), { targeted: targeted.length, firstTargeted, firstPaint });
  rec.check(`${t}: never pages the plain closed list`, plainDeep.length === 0, { plainDeep: plainDeep.length, total: reqs.length });
  rec.check(`${t}: rows match, no duplicates, in place`, dupes === 0 && strays.length === 0 && order && shown.length > 0, {
    shown: shown.length,
    expected: expect.length,
    dupes,
    strays: strays.slice(0, 5),
    order,
  });
  if (c.name === "extref-inc99" || c.name === "owner-me-search") {
    rec.check(`${t}: every match drawn`, shown.length === expect.length, { shown, expect });
  }
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  rec.check(`${t}: no horizontal overflow`, overflow <= 0, { overflow });
  if (theme === "light" || vp.width === 390) await page.screenshot({ path: `${out}narrow-${c.name}-${tag(vp, theme)}.png` });

  // Clear the pinned chips (the last condition first) and the box.
  const before = reqs.length;
  for (const f of [...c.f].reverse()) {
    const chip = page.locator(".narrowing-chips .MuiChip-root").filter({ hasNotText: /^State/ }).last();
    await chip.locator(".MuiChip-deleteIcon").click();
    await page.waitForTimeout(300);
    void f;
  }
  if (c.q) await page.locator('input[type="search"], input[placeholder*="Search" i]').first().fill("");
  await settle(page);
  const cleared = await rowSeeds(page);
  const plainFirst = (await expectedSeeds("limit=200")).slice(0, 200);
  const afterReqs = reqs.slice(before);
  const clearedPrefix = cleared.slice(0, 50).every((n, i) => closedMs.get(n) === closedMs.get(plainFirst[i]));
  const outOfPlace = cleared.filter((n) => !plainFirst.includes(n));
  rec.check(`${t}: chips cleared back to the plain list`, clearedPrefix && outOfPlace.length <= 0 &&
    !afterReqs.some((r) => r.owner || r.externalRef), {
    rows: cleared.length,
    outOfPlace: outOfPlace.slice(0, 5),
    newTargeted: afterReqs.filter((r) => r.owner || r.externalRef).length,
  });
  await ctx.close();
}

// Rapid a -> b switch with a's answer held back: b's rows must win.
async function raceWalk(vp, theme) {
  const ctx = await signedIn(browser, S, T, vp, theme);
  const page = await ctx.newPage();
  const reqs = track(page);
  await page.route(/\/api\/tasks\?.*owner=alice/, async (route) => {
    await new Promise((r) => setTimeout(r, 2500));
    await route.continue();
  });
  await page.goto("/?f=state:any:closed");
  await page.locator(".task-row").first().waitFor({ timeout: 30000 });
  const nav = (url) =>
    page.evaluate((u) => {
      history.pushState({}, "", u);
      dispatchEvent(new PopStateEvent("popstate"));
    }, url);
  await nav("/?f=state:any:closed&f=owner:any:alice%40example.com");
  await page.waitForTimeout(150);
  await nav("/?f=state:any:closed&f=owner:any:bob%40example.com");
  await page.waitForTimeout(4000);
  const shown = await rowSeeds(page);
  // owner = owners[i % 4]: alice is i%4==2, bob i%4==3.
  const alice = shown.filter((n) => n % 4 === 2).length;
  const bob = shown.filter((n) => n % 4 === 3).length;
  const t = `race ${tag(vp, theme)}`;
  rec.check(`${t}: stale owner:any:alice answer never drawn`, alice === 0 && bob > 0, {
    alice,
    bob,
    reqs: reqs.filter((r) => r.owner).map((r) => r.owner),
    url: page.url(),
  });
  await ctx.close();
}

async function admissionWalk(vp, theme) {
  const ctx = await signedIn(browser, S, T, vp, theme);
  const page = await ctx.newPage();
  const t = tag(vp, theme);
  const rank = { standard: 0, "": 0, bypass: 1, durable: 2 };
  const admOf = new Map();
  let cursor = "";
  for (let i = 0; i < 2; i++) {
    const r = await api(S, T, "GET", `/api/tasks?state=closed&limit=200&cursor=${encodeURIComponent(cursor)}`);
    // GET /api/tasks reads a stored "" as standard, so a legacy row is
    // known by its seed index (legacy-admission.sh), not by the answer.
    r.body.tasks.forEach((x) => {
      const n = seedNo(x.title);
      admOf.set(n, Math.floor(n / 4) % 4 === 3 ? "" : x.admission);
    });
    cursor = r.body.nextCursor;
  }
  for (const dir of ["asc", "desc"]) {
    await page.goto(`/?f=state:any:closed&sort=admission:${dir}`);
    await page.locator(".task-row").first().waitFor({ timeout: 30000 });
    await settle(page);
    const seeds = (await rowSeeds(page)).filter((n) => admOf.has(n));
    const ranks = seeds.map((n) => rank[admOf.get(n)]);
    const sorted = ranks.every((r, i) => i === 0 || (dir === "asc" ? ranks[i - 1] <= r : ranks[i - 1] >= r));
    const legacy = seeds.filter((n) => admOf.get(n) === "").length;
    rec.check(`admission sort ${dir} ${t}: standard+"" < bypass < durable`, sorted && legacy > 0, {
      rows: seeds.length,
      legacy,
      firstRanks: ranks.slice(0, 3),
      lastRanks: ranks.slice(-3),
    });
    if (vp.width === 1440 && theme === "light") await page.screenshot({ path: `${out}admission-sort-${dir}-${t}.png` });
  }
  await page.goto("/?f=state:any:closed&f=admission:any:standard");
  await page.locator(".task-row").first().waitFor({ timeout: 30000 });
  await settle(page);
  const std = (await rowSeeds(page)).filter((n) => admOf.has(n));
  rec.check(`admission:any:standard ${t}: keeps legacy "" rows`, std.some((n) => admOf.get(n) === "") &&
    std.every((n) => ["standard", ""].includes(admOf.get(n))), { rows: std.length, legacy: std.filter((n) => admOf.get(n) === "").length });
  await page.goto("/?f=state:any:closed&f=admission:any:bogus");
  const warn = page.locator(".narrowing-chips .MuiChip-colorWarning");
  await warn.first().waitFor({ timeout: 15000 }).catch(() => {});
  rec.check(`admission:any:bogus ${t}: warning chip`, (await warn.count()) === 1, {
    text: await warn.first().innerText().catch(() => ""),
  });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  rec.check(`admission:any:bogus ${t}: no overflow`, overflow <= 0, { overflow });
  await page.screenshot({ path: `${out}admission-bogus-${t}.png` });
  if (theme === "light") {
    const axe = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).disableRules(["color-contrast"]).analyze();
    rec.check(`axe narrowing ${t}`, axe.violations.length === 0, { violations: axe.violations.map((v) => `${v.id}:${v.nodes.length}`) });
  }
  await ctx.close();
}

// A saved Kanban board narrowed by admission (Kanban columns are states,
// model.BoardColumn: there is no attribute group-by to set).
async function boardWalk(vp, theme) {
  const t = tag(vp, theme);
  const made = await api(S, T, "POST", "/api/boards", {
    name: `Durable work ${t}`,
    kind: "kanban",
    query: "f=admission%3Aany%3Adurable&sort=admission%3Adesc",
  });
  const ctx = await signedIn(browser, S, T, vp, theme);
  const page = await ctx.newPage();
  try {
    await page.goto(`/boards/${made.body.id}`);
    await page.waitForTimeout(3000);
    const cards = await page.locator(".kanban-card, [data-kanban-card], .task-card").allInnerTexts();
    const warn = await page.getByText(/Unknown condition/).count();
    rec.check(`saved kanban admission:any:durable ${t}`, made.status < 300 && warn === 0, {
      status: made.status,
      cards: cards.length,
      chips: await page.locator(".narrowing-chips .MuiChip-root").allInnerTexts(),
    });
    if (theme === "light") await page.screenshot({ path: `${out}board-durable-${t}.png` });
  } finally {
    await api(S, T, "DELETE", `/api/boards/${made.body?.id}`);
    await ctx.close();
  }
}

const only = process.env.ONLY || "";
const FAST = process.env.FAST === "1";
for (const vp of FAST ? VIEWPORTS.slice(0, 1) : VIEWPORTS) {
  for (const theme of FAST ? THEMES.slice(0, 1) : THEMES) {
    if (!only || only === "cases") for (const c of CASES) await caseWalk(vp, theme, c);
    if (!only || only === "race") await raceWalk(vp, theme);
    if (!only || only === "admission") await admissionWalk(vp, theme);
    if (!only || only === "board") await boardWalk(vp, theme);
  }
}
await rec.save(`narrowing-${label}.json`);
await browser.close();
console.log(`failures: ${rec.result.failures.length}`, rec.result.failures);
