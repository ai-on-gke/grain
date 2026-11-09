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

// 2026-11-08 §7 item 3b: a cross-surface sweep over `grain demo` of
// #903, #928, #931, #933, #934, #935, #936, #938 -- a New Task draft
// meeting a template with its own priority and console hosting plus a
// malformed fork head, a template-backed schedule's hosting preview,
// viewer / developer / operator gating of move handles, /boards load
// failure and retry, and axe on each page. Usage:
//   GRAIN_SERVER=http://127.0.0.1:<port> GRAIN_TOKEN=... node cross-walk.mjs [label]
import { chromium, AxeBuilder, VIEWPORTS, THEMES, recorder, signedIn, api, out, tag } from "./lib.mjs";

const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const label = process.argv[2] || "run";
const rec = recorder(label);
const FAST = process.env.FAST === "1";
const call = (m, p, b) => api(S, T, m, p, b);

const ROLES = {
  viewer: {
    role: "grain/viewer",
    roles: ["grain/viewer"],
    full: false,
    scopes: ["boards:read", "ci:read", "repos:read", "schedules:read", "settings:read", "suites:read", "tasks:read", "templates:read"],
  },
  developer: {
    role: "grain/developer",
    roles: ["grain/developer"],
    full: false,
    scopes: [
      "boards:read", "boards:write", "ci:read", "repos:read", "schedules:read", "settings:read",
      "suites:read", "tasks:read", "tasks:write", "templates:read", "merge:write",
    ],
  },
  operator: null, // the access code itself: unlimited
};

async function asRole(ctx, access) {
  if (!access) return;
  await ctx.route(
    (u) => u.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      await route.fulfill({ response, json: { ...(await response.json()), access } });
    },
  );
}

async function axe(page, name) {
  const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).disableRules(["color-contrast"]).analyze();
  rec.check(`axe ${name}`, r.violations.length === 0, { violations: r.violations.map((v) => `${v.id}:${v.nodes.length}`) });
}

const browser = await chromium.launch();

// A template with its own priority and console hosting, and a schedule
// filed from it.
const tmpl = await call("POST", "/api/templates", {
  name: "Walk console template",
  title: "Console template task",
  repo: "acme/widgets",
  priority: 8,
  hosting: "console",
});
rec.step("template", { status: tmpl.status, id: tmpl.body?.id, priority: tmpl.body?.priority, hosting: tmpl.body?.hosting });
const sched = await call("POST", "/api/schedules", {
  name: "Walk template schedule",
  recurrence: { kind: "everyNHours", everyNHours: 24 },
  templateId: tmpl.body?.id,
});
rec.step("schedule", { status: sched.status, id: sched.body?.id, body: sched.status >= 300 ? JSON.stringify(sched.body) : undefined });

async function draftWalk(vp, theme) {
  const t = tag(vp, theme);
  const ctx = await signedIn(browser, S, T, vp, theme);
  const page = await ctx.newPage();
  page.on("dialog", (d) => d.accept());
  try {
    await page.goto("/new");
    const title = page.getByLabel("Title", { exact: true }).first();
    await title.waitFor({ timeout: 20000 });
    await title.fill("Draft walk title");
    await page.keyboard.press("Escape");
    await page.waitForTimeout(500);
    await page.goto(`/processes/templates/${tmpl.body.id}`);
    const from = page.getByRole("button", { name: /New task from this template/i }).first();
    await from.waitFor({ timeout: 15000 });
    await from.click();
    await page.getByLabel("Title", { exact: true }).first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(500);
    const prio = await page.locator('input[name="priority"]').first().inputValue().catch((e) => `? ${e}`.slice(0, 80));
    const prioHelp = await page.locator('input[name="priority"]').first()
      .evaluate((el) => el.closest(".MuiFormControl-root")?.innerText || "").catch(() => "");
    const kept = await page.getByLabel("Title", { exact: true }).first().inputValue();
    const hostingText = await page.locator("body").innerText();
    rec.check(`draft over template keeps the template's priority ${t}`, prio === "8", { priority: prio, help: prioHelp.replace(/\s+/g, " "), title: kept });
    rec.check(`template's console hosting shown ${t}`, /console/i.test(hostingText), {});
    const base = page.getByLabel("Base branch", { exact: true }).first();
    const create = page.getByRole("button", { name: "Create task", exact: true });
    if (await base.count()) {
      await base.fill("bad::head");
      await page.waitForTimeout(400);
      const heldBad = await create.isDisabled();
      const msg = await page.locator(".MuiFormHelperText-root.Mui-error").allInnerTexts();
      await base.fill("forkuser:feat");
      await page.waitForTimeout(400);
      const okFixed = await create.isEnabled();
      rec.check(`Create task held on bad::head, released on forkuser:feat ${t}`, heldBad && okFixed, { heldBad, okFixed, msg: msg.slice(0, 2) });
      await base.fill("bad::head");
      await page.waitForTimeout(300);
      if (theme === "light" || vp.width === 390) await page.screenshot({ path: `${out}cross-forkhead-${t}.png` });
    } else {
      rec.check(`Base branch field present ${t}`, false, {});
    }
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    rec.check(`New Task no overflow ${t}`, overflow <= 0, { overflow });
    if (vp.width === 1440 && theme === "light") await axe(page, `new task ${t}`);
    // Discard the draft so later walks start clean.
    const discard = page.getByRole("button", { name: /Discard draft/i });
    if (await discard.count()) await discard.first().click();
  } catch (err) {
    rec.check(`draft walk ran ${t}`, false, { error: String(err).slice(0, 300) });
  } finally {
    await ctx.close();
  }
}

async function scheduleWalk(vp, theme) {
  const t = tag(vp, theme);
  if (!sched.body?.id) return rec.check(`schedule created ${t}`, false, { status: sched.status });
  const ctx = await signedIn(browser, S, T, vp, theme);
  const page = await ctx.newPage();
  try {
    await page.goto(`/processes/schedules/${sched.body.id}`);
    await page.getByRole("heading", { name: "Edit schedule" }).waitFor({ timeout: 15000 });
    await page.waitForTimeout(500);
    // The "What this files" preview (CompositionPanel): its Hosting row.
    await page.locator("dl dt", { hasText: /^Hosting$/ }).first().waitFor({ timeout: 10000 }).catch(() => {});
    const preview = await page.locator("dl > div", { has: page.locator("dt", { hasText: /^Hosting$/ }) })
      .allInnerTexts().then((xs) => xs.join(" | ").replace(/\s+/g, " "));
    rec.check(`template-backed schedule previews console hosting ${t}`, /console/i.test(preview), { preview: preview.slice(0, 240) });
    if (theme === "light" || vp.width === 390) await page.screenshot({ path: `${out}cross-schedule-${t}.png` });
    if (vp.width === 1440 && theme === "light") await axe(page, `schedule pane ${t}`);
  } catch (err) {
    rec.check(`schedule walk ran ${t}`, false, { error: String(err).slice(0, 300) });
  } finally {
    await ctx.close();
  }
}

async function roleWalk(vp, theme) {
  const t = tag(vp, theme);
  const board = await call("POST", "/api/boards", { name: `Walk kanban ${t}`, kind: "kanban", query: "" });
  try {
    for (const [role, access] of Object.entries(ROLES)) {
      const ctx = await signedIn(browser, S, T, vp, theme);
      await asRole(ctx, access);
      const page = await ctx.newPage();
      await page.goto("/");
      await page.locator(".task-row").first().waitFor({ timeout: 20000 });
      await page.waitForTimeout(800);
      const listMoves = await page.getByRole("button", { name: /^Move task / }).evaluateAll((els) =>
        els.map((e) => ({ name: e.getAttribute("aria-label") || e.textContent, disabled: e.disabled })));
      await page.goto(`/boards/${board.body.id}`);
      await page.waitForTimeout(2500);
      const kanbanMoves = await page.getByRole("button", { name: /Move task|Move #|Pick up/i }).evaluateAll((els) =>
        els.map((e) => ({ name: e.getAttribute("aria-label") || e.textContent, disabled: e.disabled })));
      const enabled = (xs) => xs.filter((x) => !x.disabled).length;
      const allHaveId = [...listMoves, ...kanbanMoves].every((x) => /#?\d+/.test(x.name || ""));
      const expectMove = role !== "viewer";
      rec.check(`${role} move handles ${t}`,
        (expectMove ? enabled(listMoves) > 0 : enabled(listMoves) === 0 && enabled(kanbanMoves) === 0) && allHaveId, {
          list: listMoves.length,
          listEnabled: enabled(listMoves),
          kanban: kanbanMoves.length,
          kanbanEnabled: enabled(kanbanMoves),
          sample: [listMoves[0]?.name, kanbanMoves[0]?.name],
        });
      if (theme === "light" && vp.width === 1440) await page.screenshot({ path: `${out}cross-kanban-${role}-${t}.png` });
      await ctx.close();
    }
  } finally {
    await call("DELETE", `/api/boards/${board.body?.id}`);
  }
}

async function boardsWalk(vp, theme) {
  const t = tag(vp, theme);
  const ctx = await signedIn(browser, S, T, vp, theme);
  const page = await ctx.newPage();
  let fail = true;
  await page.route((u) => u.pathname === "/api/boards", (route) =>
    fail && route.request().method() === "GET"
      ? route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"boards store unavailable"}' })
      : route.fallback());
  try {
    await page.goto("/boards");
    const alert = page.locator(".tasks-load-error");
    await alert.first().waitFor({ timeout: 15000 }).catch(() => {});
    const text = await alert.first().innerText().catch(() => "");
    const noBoardsShown = await page.getByText(/No boards yet/i).count();
    rec.check(`/boards load failure says so, not "No boards yet" ${t}`, /could not be loaded/.test(text) && noBoardsShown === 0, { text });
    if (theme === "light" || vp.width === 390) await page.screenshot({ path: `${out}cross-boards-failed-${t}.png` });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    rec.check(`/boards failure no overflow ${t}`, overflow <= 0, { overflow });
    fail = false;
    await page.getByRole("button", { name: "Retry" }).click();
    await page.waitForTimeout(1500);
    rec.check(`/boards Retry recovers ${t}`, (await alert.count()) === 0, {
      rows: await page.locator(".template-row").count(),
    });
    if (vp.width === 1440 && theme === "light") await axe(page, `boards ${t}`);
  } catch (err) {
    rec.check(`boards walk ran ${t}`, false, { error: String(err).slice(0, 300) });
  } finally {
    await ctx.close();
  }
}

const only = process.env.ONLY || "";
for (const vp of FAST ? VIEWPORTS.slice(0, 1) : VIEWPORTS) {
  for (const theme of FAST ? THEMES.slice(0, 1) : THEMES) {
    if (!only || only === "draft") await draftWalk(vp, theme);
    if (!only || only === "schedule") await scheduleWalk(vp, theme);
    if (!only || only === "role") await roleWalk(vp, theme);
    if (!only || only === "boards") await boardsWalk(vp, theme);
  }
}
if (sched.body?.id) await call("DELETE", `/api/schedules/${sched.body.id}`);
if (tmpl.body?.id) await call("DELETE", `/api/templates/${tmpl.body.id}`);
await rec.save(`cross-${label}.json`);
await browser.close();
console.log(`failures: ${rec.result.failures.length}`, rec.result.failures);
