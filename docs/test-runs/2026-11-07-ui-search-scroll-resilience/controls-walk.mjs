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

// 2026-11-07 §10 items 2-5 over `grain demo`: sidebar New task, collapsed
// rail and default-hidden CI (#726); the Repos add-in-progress lock
// (#711); "No tasks" vs "could not be loaded" on a cold and a mid-session
// /api/tasks outage (#801); and the closed `origin:` vocabulary beside
// owner, externalRef and humanReview conditions (#794). Usage:
//   GRAIN_SERVER=http://127.0.0.1:4390 GRAIN_TOKEN=... node controls-walk.mjs [label]
// ONLY=sidebar|repos|failed|origin runs one section.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const label = process.argv[2] || "run";
const only = process.env.ONLY || "";
const out = new URL(".", import.meta.url).pathname;
const result = { label, steps: [] };
const step = (name, data) => {
  result.steps.push({ name, ...data });
  console.log(name, JSON.stringify(data));
};
const api = async (method, path, body) => {
  const res = await fetch(S + path, {
    method,
    headers: { Authorization: `Bearer ${T}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: res.status, body: await res.json().catch(() => null) };
};
const browser = await chromium.launch();
const boardIds = [];
let lastData = null;
const desktop = { width: 1440, height: 900 };
const phone = { width: 390, height: 844 };
async function context(viewport, colorScheme = "light") {
  const ctx = await browser.newContext({ baseURL: S, viewport, colorScheme });
  await ctx.request.post("/api/auth/access-code", { data: { code: T } });
  return ctx;
}
const safe = async (fn, dflt = null) => {
  try {
    return await fn();
  } catch (e) {
    return dflt ?? `error: ${String(e.message || e).split("\n")[0]}`;
  }
};

async function sidebarWalk(vp, scheme) {
  const ctx = await context(vp, scheme);
  const page = await ctx.newPage();
  const tag = `${vp.width}-${scheme}`;
  await page.goto("/");
  await page.waitForLoadState("networkidle").catch(() => {});
  // On a phone the rail is a drawer behind the top bar's "Open
  // navigation"; the top bar carries its own "New task".
  const aside = vp.width < 600 ? page.locator("aside, .MuiDrawer-paper").last() : page.locator("aside");
  const openNav = async () => {
    if (vp.width >= 600) return;
    const toggle = page.getByRole("button", { name: "Open navigation" });
    if (await toggle.isVisible().catch(() => false)) await toggle.click();
    await page.waitForTimeout(600);
  };
  const data = {};
  lastData = data;
  if (vp.width < 600) {
    const nt = page.getByRole("button", { name: "New task", exact: true }).first();
    const box = await nt.boundingBox().catch(() => null);
    data.phoneTopBarNewTask = box ? { x: Math.round(box.x), right: Math.round(box.x + box.width), w: Math.round(box.width), h: Math.round(box.height) } : null;
    data.phoneTopBarNewTaskInViewport = !!box && box.x >= 0 && box.x + box.width <= vp.width;
    if (box) {
      await nt.click();
      await page.waitForTimeout(800);
      data.phoneNewTaskUrl = new URL(page.url()).pathname;
      await page.keyboard.press("Escape");
      await page.waitForTimeout(400);
      await page.goto("/");
      await page.waitForTimeout(800);
    }
    await openNav();
    data.drawerOpen = await aside.isVisible().catch(() => false);
  }
  data.ciInNav = await aside.getByRole("button", { name: /^CI\b/ }).count();
  const newTask = aside.getByRole("button", { name: "+ New task" });
  data.newTaskVisible = await newTask.isVisible().catch(() => false);
  const asideBox = await aside.boundingBox().catch(() => null);
  const ntBox = await newTask.boundingBox().catch(() => null);
  data.newTaskInsideRail =
    !!asideBox && !!ntBox && ntBox.x >= asideBox.x && ntBox.x + ntBox.width <= asideBox.x + asideBox.width + 0.5;
  await page.screenshot({ path: `${out}/sidebar-${tag}.png` });
  if (data.newTaskVisible) {
    await newTask.click();
    await page.waitForTimeout(800);
    data.newTaskOpens = await page
      .getByRole("dialog")
      .or(page.locator('[aria-label="New task"]'))
      .first()
      .isVisible()
      .catch(() => false);
    data.urlAfterNewTask = new URL(page.url()).pathname;
    await page.keyboard.press("Escape");
    await page.waitForTimeout(400);
  }
  if (vp.width >= 600) {
    // Top-rail: every page-header control centred on one line.
    const header = page.getByTestId("page-header");
    const boxes = await header.getByRole("button").evaluateAll((els) =>
      els
        .filter((e) => e.offsetParent)
        .map((e) => {
          const r = e.getBoundingClientRect();
          return { name: e.getAttribute("aria-label") || e.textContent.trim(), cy: r.y + r.height / 2, h: r.height };
        }),
    );
    const railTop = await aside.getByRole("button", { name: "+ New task" }).evaluate((e) => {
      const r = e.getBoundingClientRect();
      return r.y + r.height / 2;
    });
    const cys = boxes.map((b) => b.cy);
    data.headerControls = boxes.map((b) => `${b.name}:${Math.round(b.cy)}/${Math.round(b.h)}`);
    data.headerCentreSpread = cys.length ? Math.round((Math.max(...cys) - Math.min(...cys)) * 10) / 10 : null;
    data.newTaskVsHeaderCentre = cys.length ? Math.round((railTop - cys[0]) * 10) / 10 : null;
    // Collapse: tooltips and keyboard order on the rail.
    await aside.getByRole("button", { name: "Collapse sidebar" }).click();
    await page.waitForTimeout(400);
    data.collapsed = await aside.getAttribute("data-collapsed");
    const create = aside.getByRole("button", { name: "Create" });
    data.createVisibleCollapsed = await create.isVisible().catch(() => false);
    await create.hover();
    await page.waitForTimeout(900);
    data.createTooltip = await page.getByRole("tooltip").first().textContent().catch(() => null);
    await create.focus();
    const order = [];
    for (let i = 0; i < 14; i++) {
      order.push(
        await page.evaluate(() => {
          const e = document.activeElement;
          const inRail = !!e.closest("aside");
          return `${inRail ? "" : "[out]"}${e.getAttribute("aria-label") || e.textContent.trim().slice(0, 20)}`;
        }),
      );
      await page.keyboard.press("Tab");
    }
    data.collapsedFocusOrder = order;
    const rail = await aside.boundingBox();
    const icons = await aside.getByRole("button").evaluateAll((els) =>
      els.filter((e) => e.offsetParent).map((e) => {
        const r = e.getBoundingClientRect();
        return { cx: r.x + r.width / 2, w: r.width, name: e.getAttribute("aria-label") || e.textContent.trim().slice(0, 16) };
      }),
    );
    const railCx = rail.x + rail.width / 2;
    data.collapsedRailWidth = rail.width;
    data.collapsedIconsOffCentre = icons.filter((i) => Math.abs(i.cx - railCx) > 3).map((i) => `${i.name}@${Math.round(i.cx - railCx)}`);
    await page.screenshot({ path: `${out}/sidebar-collapsed-${tag}.png` });
    const axe = await new AxeBuilder({ page }).include("aside").analyze();
    data.axeCollapsedRail = axe.violations.map((v) => `${v.id}(${v.nodes.length})`);
    await page.getByTestId("page-header").getByRole("button", { name: "Expand sidebar" }).click();
    await page.waitForTimeout(400);
  }
  // Customize sidebar: turn CI on, then survive a reload.
  if (vp.width < 600 && !(await aside.isVisible().catch(() => false))) await openNav();
  await aside.getByRole("button", { name: "Customize sidebar" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("switch", { name: "Show CI" }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  await page.waitForTimeout(400);
  if (vp.width < 600 && !(await aside.isVisible().catch(() => false))) await openNav();
  data.ciAfterCustomize = await aside.getByRole("button", { name: /^CI\b/ }).count();
  await page.reload();
  await page.waitForTimeout(1200);
  await openNav();
  data.ciAfterReload = await aside.getByRole("button", { name: /^CI\b/ }).count();
  await ctx.close();
  // Hidden CI is still a route.
  const ctx2 = await context(vp, scheme);
  const p2 = await ctx2.newPage();
  await p2.goto("/ci");
  await p2.waitForTimeout(1500);
  data.directCiPath = new URL(p2.url()).pathname;
  data.directCiHeading = await p2.getByRole("heading", { name: /^CI/ }).first().isVisible().catch(() => false);
  data.directCiNavHidden = (await p2.locator("aside").getByRole("button", { name: /^CI\b/ }).count()) === 0;
  await p2.screenshot({ path: `${out}/ci-direct-${tag}.png` });
  await ctx2.close();
  step(`sidebar (${tag})`, data);
}

async function reposWalk(vp) {
  const ctx = await context(vp);
  const page = await ctx.newPage();
  page.on("dialog", (d) => d.accept());
  await page.goto("/repos");
  const field = page.getByLabel("Repo to add");
  const add = page.getByRole("button", { name: "Add repo", exact: true });
  await field.waitFor();
  const tag = `${vp.width}`;
  for (const status of [500, 409, 200]) {
    let posts = 0;
    let release;
    await page.route(/\/api\/repos$/, async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      posts++;
      await new Promise((r) => {
        release = r;
        setTimeout(r, 2000);
      });
      return route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(status === 200 ? { ok: true } : { error: `synthetic ${status}` }),
      });
    });
    const typed = `acme/walk-${status}`;
    await field.fill(typed);
    await page.keyboard.press("Escape"); // close the suggestion list
    await add.dblclick();
    await field.press("Enter").catch(() => {});
    await field.press("Enter").catch(() => {});
    await page.waitForTimeout(400);
    const during = {
      fieldDisabled: await field.isDisabled(),
      buttonBusy: await add.getAttribute("aria-busy"),
      spinner: await add.getByRole("progressbar").count(),
      caption: await safe(() => page.getByText(/Adding acme\/walk/).first().textContent()),
    };
    if (status === 500) await page.screenshot({ path: `${out}/repos-adding-${tag}.png` });
    await page.waitForTimeout(2300);
    const after = {
      fieldValue: await field.inputValue(),
      fieldDisabled: await field.isDisabled(),
      buttonBusy: await add.getAttribute("aria-busy"),
      errorShown: await safe(() => page.locator(".error-banner, [role=alert]").first().textContent()),
    };
    step(`repos add ${status} (${tag})`, { posts, during, after, preserved: after.fieldValue === typed });
    await page.unroute(/\/api\/repos$/);
    await page.waitForTimeout(5500); // let the error banner fade
  }
  await ctx.close();
}

async function failedWalk(vp) {
  const tag = `${vp.width}`;
  const failAll = (page, how) =>
    page.route(/\/api\/tasks(\?.*)?$/, (route) => {
      if (route.request().method() !== "GET") return route.continue();
      if (how === "abort") return route.abort("connectionrefused");
      return route.fulfill({ status: 502, contentType: "application/json", body: JSON.stringify({ error: "synthetic 502" }) });
    });
  const views = [
    ["list", "/"],
    ...boardIds.map((b) => [b.kind, `/boards/${b.id}`]),
  ];
  const read = async (page) => ({
    failedText: await page.getByText(/could not be loaded/i).count(),
    retry: await page.getByRole("button", { name: /retry/i }).count(),
    noTasksClaim: await page
      .getByText(/^(No tasks yet\.|Nothing here right now\.|No tasks in any of these columns\.|Nothing here\.|No tasks in this state\.)$/)
      .count(),
    rail: (await safe(() => page.locator("aside").getByRole("button", { name: /^Tasks/ }).first().innerText())).replace(/\s+/g, " "),
    rows: await page.locator(".task-row, .board-card, .task-tree-row").count(),
  });
  // Cold start.
  for (const [name, path] of views) {
    const ctx = await context(vp);
    const page = await ctx.newPage();
    await failAll(page, "502");
    await page.goto(path);
    await page.waitForTimeout(6500); // past the banner's fade
    const cold = await read(page);
    await page.screenshot({ path: `${out}/failed-cold-${name}-${tag}.png` });
    await page.unroute(/\/api\/tasks(\?.*)?$/);
    await page.waitForTimeout(7000); // the next poll(s)
    const recovered = await read(page);
    step(`cold outage ${name} (${tag})`, { cold, recoveredWithoutClick: recovered });
    await ctx.close();
  }
  // Mid-session: tasks already drawn, then the server goes away.
  for (const [name, path] of views) {
    const ctx = await context(vp);
    const page = await ctx.newPage();
    await page.goto(path);
    await page.waitForTimeout(3000);
    const before = await read(page);
    await failAll(page, "abort");
    await page.waitForTimeout(9000);
    const during = await read(page);
    await page.screenshot({ path: `${out}/failed-mid-${name}-${tag}.png` });
    await page.unroute(/\/api\/tasks(\?.*)?$/);
    await page.waitForTimeout(7000);
    const recovered = await read(page);
    step(`mid-session outage ${name} (${tag})`, { before, during, recovered });
    await ctx.close();
  }
}

async function originWalk(vp) {
  const tag = `${vp.width}`;
  const ctx = await context(vp);
  const page = await ctx.newPage();
  const queries = [
    "origin:any:imported",
    "origin:any:schedule",
    "origin:any:proposed",
    "origin:any:external",
    "origin:any:hand|imported|proposed|schedule|suite|fix|review|external",
    "origin:any:bogus",
    "origin:any:mcp",
    "owner:any:me",
    "externalRef:any:CA-9012",
    "externalRef:filled:",
    "externalRef:any:CA-100",
    "owner:any:alice@example.com",
    "humanReview:filled:",
    "humanReview:any:security",
    "humanReview:any:product_decision",
    "admission:any:durable",
  ];
  for (const q of queries) {
    await page.goto(`/?f=${q}`);
    await page.waitForTimeout(1200);
    // NarrowingBar draws a condition naming a value its attribute does
    // not have, or an attribute there is not, as a warning chip.
    const warn = page.locator(".narrowing-chips .MuiChip-colorWarning");
    const chips = await page.locator(".narrowing-chips .MuiChip-root").allInnerTexts();
    step(`narrowing ${q} (${tag})`, {
      chips,
      warningChips: await warn.count(),
      warningTitles: await warn.evaluateAll((els) => els.map((e) => e.getAttribute("title"))),
      rows: await page.locator(".task-row").count(),
    });
  }
  // Grouped boards by origin.
  const res = await api("POST", "/api/boards", {
    name: `walk origin ${Date.now()}`,
    kind: "list",
    query: "f=origin:any:imported|schedule|proposed|external|hand&group=origin",
  });
  boardIds.push({ kind: "origin-list", id: res.body?.id, temp: true });
  await page.goto(`/boards/${res.body?.id}`);
  await page.waitForTimeout(1500);
  step(`saved board grouped by origin (${tag})`, {
    status: res.status,
    warningChips: await page.locator(".narrowing-chips .MuiChip-colorWarning").count(),
    rows: await page.locator(".task-row").count(),
  });
  await page.screenshot({ path: `${out}/origin-board-${tag}.png` });
  await ctx.close();
}

try {
  if (!only || only === "failed") {
    for (const [kind, extra] of [
      ["kanban", {}],
      ["tree", {}],
      ["panels", { panels: [{ id: "p-states", kind: "states", title: "States", span: 12 }, { id: "p-gantt", kind: "gantt", title: "Timeline", span: 12, view: { range: "7d", lanes: "state" } }] }],
    ]) {
      const res = await api("POST", "/api/boards", { name: `walk ${kind} ${Date.now()}`, kind, ...extra });
      boardIds.push({ kind, id: res.body.id });
    }
  }
  for (const vp of [desktop, phone]) {
    if (!only || only === "sidebar" || (only === "sidebar-phone" && vp.width < 600)) for (const scheme of ["light", "dark"]) await safe(() => sidebarWalk(vp, scheme)).then((r) => r && step(`sidebar error ${vp.width}-${scheme}`, { r, partial: lastData }));
    if (!only || only === "repos") await safe(() => reposWalk(vp)).then((r) => r && step(`repos error ${vp.width}`, { r }));
    if (!only || only === "failed") await safe(() => failedWalk(vp)).then((r) => r && step(`failed error ${vp.width}`, { r }));
    if (!only || only === "origin") await safe(() => originWalk(vp)).then((r) => r && step(`origin error ${vp.width}`, { r }));
  }
} catch (err) {
  step("error", { message: String(err?.stack || err) });
} finally {
  for (const b of boardIds) if (b.id) await api("DELETE", `/api/boards/${b.id}`);
  await browser.close();
  await fs.writeFile(`${out}/controls-${label}${only ? `-${only}` : ""}.json`, JSON.stringify(result, null, 2));
}
