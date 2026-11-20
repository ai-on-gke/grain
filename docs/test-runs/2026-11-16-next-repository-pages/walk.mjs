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

// Walkthrough driver for docs/test-plans/2026-11-16.md §8 against the real
// daemon env.sh starts. Every step logs one line "STEP <name>: <outcome>"
// so the report can quote it, and every save is read back over the API.
//
//   node walk.mjs <phase> [out-dir]
//
// phase: roundtrip | roles | add | gate | layout | managed

import fs from "node:fs";
import { createRequire } from "node:module";

const requireNg = createRequire(
  new URL("../../../ui-ng/package.json", import.meta.url),
);
const { chromium } = requireNg("playwright-core");

const phase = process.argv[2] || "roundtrip";
const out = process.argv[3] || "/tmp/walk-repos/captures";
fs.mkdirSync(out, { recursive: true });
const DAEMON = process.env.DAEMON || "http://127.0.0.1:4292";
const PROXY = process.env.IAP_PROXY || "http://127.0.0.1:4296";
const CODE = process.env.CODE;
const R = "/next/repos/acme/widgets";

const log = (name, outcome) => console.log(`STEP ${name}: ${outcome}`);

async function api(method, path, body) {
  const res = await fetch(`${DAEMON}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${CODE}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = text;
  try {
    json = JSON.parse(text);
  } catch {
    // keep text
  }
  return { status: res.status, body: json };
}

async function as(browser, email, opts = {}) {
  const ctx = await browser.newContext({
    viewport: opts.viewport || { width: 1440, height: 900 },
    colorScheme: opts.colorScheme || "light",
  });
  await ctx.addCookies([{ name: "iapsim_email", value: email, url: PROXY }]);
  const page = await ctx.newPage();
  page.on("pageerror", (e) => log("pageerror", e.message));
  return { ctx, page };
}

async function go(page, path) {
  await page.goto(`${PROXY}${path}`, { waitUntil: "domcontentloaded" });
  await page.waitForLoadState("networkidle").catch(() => {});
}

async function text(locator) {
  return (await locator.innerText().catch(() => "<absent>")).replace(
    /\s+/g,
    " ",
  );
}

async function roundtrip(browser) {
  const { page } = await as(browser, "alice@example.com");
  if (process.env.CLASSIC_ONLY) return classic(page);
  // Work ownership: one item off, then the default off.
  await go(page, `${R}/releases`);
  const own = page.getByTestId("card-repo-work-ownership");
  await own.waitFor();
  log("ownership card", await text(own));
  const triage = own.getByTestId("work-item-triage");
  await triage.getByRole("button", { name: /: off$/ }).click();
  await page.waitForTimeout(800);
  log("ownership triage off api", JSON.stringify((await api("GET", "/api/repos/acme/widgets/work-ownership")).body.items.find((i) => i.item === "triage")));
  const release = own.getByTestId("work-item-release");
  await release.getByRole("button", { name: /: on$/ }).click();
  await page.waitForTimeout(800);
  const sw = own.getByRole("switch", { name: "Standing work on this deployment" });
  await sw.click();
  await page.waitForTimeout(800);
  const wo = (await api("GET", "/api/repos/acme/widgets/work-ownership")).body;
  log("ownership after default off api", JSON.stringify({ default: wo.default, items: wo.items.map((i) => [i.item, i.override, i.runs]) }));
  log("ownership card after", await text(own));

  // Qualification plan: two items, the second depending on the first.
  const plan = page.getByTestId("card-repo-qualification-plan");
  log("plan card initial", await text(plan));
  const addBtn = plan.getByRole("button", { name: "Add item" });
  await addBtn.click();
  await plan.getByRole("button", { name: "Item 1 template" }).click();
  await page.getByRole("option", { name: /^qualify/ }).click();
  await addBtn.click();
  await plan.getByRole("button", { name: "Item 2 template" }).click();
  await page.getByRole("option", { name: /security-audit/ }).click();
  await plan.getByRole("group", { name: "Item 2 depends on" }).getByRole("button", { name: "qualify" }).click();
  await plan.getByTestId("qualification-item-repeat-1").fill("2");
  await plan.getByTestId("save-qualification-plan").click();
  await page.waitForTimeout(1000);
  log("plan api", JSON.stringify((await api("GET", "/api/repos/acme/widgets/qualification-plan")).body));
  await page.screenshot({ path: `${out}/releases-after-save.png`, fullPage: true });

  // CI capabilities.
  await go(page, `${R}/ci`);
  const caps = page.getByTestId("card-repo-ci-capabilities");
  await caps.waitFor();
  await caps.getByRole("button", { name: /Package publish/ }).click();
  await page.waitForTimeout(800);
  log("ci caps api", JSON.stringify((await api("GET", "/api/repos/acme/widgets/ci-capabilities")).body.ciCapabilities));

  // Issue import interval.
  await go(page, R);
  const box = page.getByLabel("Every (minutes)");
  await box.fill("15");
  await page.getByTestId("save-issue-import-btn").click();
  await page.waitForTimeout(800);
  log("issue import api", JSON.stringify((await api("GET", "/api/repos/acme/widgets/settings")).body.issueImport));

  // Advanced tab.
  await go(page, `${R}/advanced`);
  log("advanced tab", (await text(page.locator(".main-stage"))).slice(0, 1500));
  await page.screenshot({ path: `${out}/advanced.png`, fullPage: true });

  await classic(page);
}

// Classic reads the same.
async function classic(page) {
  await page.goto(`${PROXY}/repos/acme/widgets`, { waitUntil: "load" });
  for (const tab of ["overview", "releases", "ci"]) {
    await page.getByRole("tab", { name: new RegExp(`^${tab}$`, "i") }).or(page.getByRole("button", { name: new RegExp(`^${tab}$`, "i") })).first().click();
    await page.waitForTimeout(2500);
    log(`classic repo page ${tab}`, (await text(page.locator("body"))).slice(0, 5000));
    await page.screenshot({ path: `${out}/classic-${tab}.png`, fullPage: true });
  }
}

async function roles(browser) {
  for (const email of (process.env.ROLES || "viewer@example.com,contributor@example.com,writer@example.com").split(",")) {
    const { page, ctx } = await as(browser, email);
    for (const tab of ["", "/releases", "/ci", "/advanced"]) {
      await go(page, `${R}${tab}`);
      await page.waitForTimeout(500);
      const disabled = await page.locator("button:disabled, input:disabled, [aria-disabled=true]").count();
      const enabled = await page.locator(".main-stage button:not(:disabled), .main-stage input:not(:disabled)").count();
      log(`${email} ${tab || "/overview"}`, `disabled=${disabled} enabled=${enabled} :: ${(await text(page.locator(".main-stage"))).slice(0, 900)}`);
      await page.screenshot({ path: `${out}/role-${email.split("@")[0]}${tab.replace("/", "-") || "-overview"}.png`, fullPage: true });
    }
    await go(page, "/next/repos");
    log(`${email} list`, (await text(page.locator(".main-stage"))).slice(0, 900));
    // What the server does with a write from this role, through the proxy.
    for (const [method, path, body] of [
      ["PUT", "/api/repos/acme/widgets/work-ownership", { item: "triage", state: "on" }],
      ["PUT", "/api/repos/acme/widgets/ci-capabilities", { ciCapabilities: [] }],
      ["PUT", "/api/deployment/repos/acme/widgets/config", { ciMode: "grain" }],
      ["POST", "/api/repos", { repo: "acme/docs" }],
    ]) {
      const r = await page.evaluate(
        async ([m, p, b]) => {
          const csrf = document.cookie.match(/grain_csrf=([^;]+)/)?.[1] || "";
          const res = await fetch(p, { method: m, headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf }, body: JSON.stringify(b) });
          return [res.status, (await res.text()).slice(0, 200)];
        },
        [method, path, body],
      );
      log(`${email} ${method} ${path}`, JSON.stringify(r));
    }
    await ctx.close();
  }
}

async function add(browser) {
  const { page } = await as(browser, "alice@example.com");
  await go(page, "/next/repos");
  log("list", (await text(page.locator(".main-stage"))).slice(0, 1200));
  await page.screenshot({ path: `${out}/list.png`, fullPage: true });
  const tryAdd = async (name) => {
    await go(page, "/next/repos");
    const form = page.getByRole("form", { name: "Add repo" });
    await form.getByLabel("Repository to add").fill(name);
    await form.getByRole("button", { name: "Add repo" }).click();
    await page.waitForTimeout(1500);
    const dialog = page.getByTestId("confirm-dialog");
    const confirm = (await dialog.count()) ? ` confirm=${await text(dialog)}` : "";
    log(`add ${JSON.stringify(name)}`, `error=${await text(form.getByRole("alert"))} input-invalid=${await form.getByLabel("Repository to add").getAttribute("aria-invalid")} banner=${await text(page.locator(".banner-error"))}${confirm}`);
    await page.screenshot({ path: `${out}/add-${name.replace(/[^a-z0-9]/gi, "_") || "empty"}.png`, fullPage: true });
  };
  for (const n of ["not a repo", "acme/widgets", "ghe.example.com/acme/legacy", "acme/docs"]) await tryAdd(n);
  log("repos api", JSON.stringify((await api("GET", "/api/repos")).body).slice(0, 600));
}

async function gate(browser) {
  const { page } = await as(browser, "alice@example.com");
  const read = async (label) => {
    await go(page, R);
    await page.waitForTimeout(800);
    const w = page.getByTestId("repo-base-gate-warning");
    log(`gate ${label}`, `warning=${await w.count()} ${await text(w)} :: api=${JSON.stringify((await api("GET", "/api/base-branch-gates?repo=acme/widgets")).body.gates.map((g) => g.reason))}`);
  };
  for (const [label, cfg] of [
    ["none unwatched", { ciMode: "none", watchDefaultBranch: false }],
    ["none watched", { ciMode: "none", watchDefaultBranch: true }],
    ["github watched", { ciMode: "github", watchDefaultBranch: true }],
    ["grain watched", { ciMode: "grain", watchDefaultBranch: true }],
  ]) {
    const r = await api("PUT", "/api/repos/acme/widgets/settings", cfg);
    if (r.status >= 300) log(`gate set ${label}`, `${r.status} ${JSON.stringify(r.body).slice(0, 300)}`);
    await read(label);
  }
}

async function layout(browser) {
  for (const colorScheme of ["light", "dark"]) {
    for (const width of [1440, 390]) {
      const { page, ctx } = await as(browser, "alice@example.com", { viewport: { width, height: width > 500 ? 900 : 844 }, colorScheme });
      for (const tab of ["", "/branches", "/ci", "/releases", "/advanced"]) {
        await go(page, `${R}${tab}`);
        await page.waitForTimeout(600);
        const overflow = await page.evaluate(() => {
          const vw = document.documentElement.clientWidth;
          const bad = [];
          for (const el of document.querySelectorAll(".main-stage *")) {
            const b = el.getBoundingClientRect();
            if (b.width > 0 && b.right > vw + 1 && !el.closest("[class*=scroll], .table-wrap, .s-table-wrap, pre")) bad.push(`${el.tagName.toLowerCase()}.${[...el.classList].join(".")}@${Math.round(b.right)}`);
          }
          return { docW: document.documentElement.scrollWidth, vw, bad: bad.slice(0, 8) };
        });
        log(`layout ${colorScheme} ${width} ${tab || "/overview"}`, JSON.stringify(overflow));
        await page.screenshot({ path: `${out}/layout-${colorScheme}-${width}${tab.replace("/", "-") || "-overview"}.png`, fullPage: true });
      }
      await go(page, "/next/repos");
      await page.screenshot({ path: `${out}/layout-${colorScheme}-${width}-list.png`, fullPage: true });
      await ctx.close();
    }
  }
}

async function managed(browser) {
  const { page } = await as(browser, "alice@example.com");
  for (const tab of ["", "/releases", "/ci", "/advanced"]) {
    await go(page, `${R}${tab}`);
    await page.waitForTimeout(600);
    const enabled = await page.locator(".main-stage button:not(:disabled), .main-stage input:not(:disabled), .main-stage [role=switch]:not([aria-disabled=true]):not(:disabled)").allInnerTexts();
    log(`managed ${tab || "/overview"}`, `${(await text(page.locator(".main-stage"))).slice(0, 700)} :: enabled controls=${JSON.stringify(enabled.map((t) => t.trim()).filter(Boolean).slice(0, 30))}`);
    await page.screenshot({ path: `${out}/managed${tab.replace("/", "-") || "-overview"}.png`, fullPage: true });
  }
  await go(page, "/next/repos");
  log("managed list", (await text(page.locator(".main-stage"))).slice(0, 700));
  for (const [method, path, body] of [
    ["PUT", "/api/repos/acme/widgets/work-ownership", { item: "triage", state: "" }],
    ["PUT", "/api/repos/acme/widgets/ci-capabilities", { ciCapabilities: [] }],
    ["PUT", "/api/repos/acme/widgets/qualification-plan", { items: [] }],
    ["PUT", "/api/repos/acme/widgets/settings", { issueImportIntervalSeconds: 0 }],
    ["POST", "/api/repos", { repo: "acme/docs" }],
  ]) {
    const r = await api(method, path, body);
    log(`managed ${method} ${path}`, `${r.status} ${JSON.stringify(r.body).slice(0, 300)}`);
  }
}

const browser = await chromium.launch();
try {
  await { roundtrip, roles, add, gate, layout, managed }[phase](browser);
} catch (e) {
  log("FAILED", e.stack || String(e));
  process.exitCode = 1;
} finally {
  await browser.close();
}
