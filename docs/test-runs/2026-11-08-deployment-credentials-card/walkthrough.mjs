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

// Browser pass of docs/test-plans/2026-11-08-afternoon.md §1 items 1-5,
// in real Chromium against servers started outside it:
//
//   DEMO_A=http://127.0.0.1:4261 CODE_A=<access code>   grain demo -github-sim
//     -fake-ci -secrets-store -legacy-capability
//   DEMO_B=http://127.0.0.1:4262 CODE_B=<access code>   the same with
//     -external-config-repo <bare repo> and no -legacy-capability
//   ROLE_BASE=http://127.0.0.1:18490                      roles.sh's iapsim
//     proxy in front of a real grain daemon
//
// Run from ui/ (for the playwright package):
//   PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node \
//     ../docs/test-runs/2026-11-08-deployment-credentials-card/walkthrough.mjs [items]
//
// Prints one JSON line per observation; items is a comma list (1,2,3,4,5).

import { createRequire } from "node:module";

// Resolve Playwright from ui/node_modules, where `npm ci` puts it.
const require = createRequire(
  new URL("../../../ui/package.json", import.meta.url),
);
const { chromium } = require("@playwright/test");

const A = process.env.DEMO_A;
const B = process.env.DEMO_B;
const ROLE = process.env.ROLE_BASE;
const OUT = process.env.OUT || "/tmp/credentials-card-shots";
const items = new Set((process.argv[2] || "1,2,3,4,5").split(","));

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

function log(item, check, data) {
  console.log(JSON.stringify({ item, check, ...data }));
}

async function signIn(url, code) {
  const res = await fetch(`${url}/api/auth/access-code`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code }),
  });
  if (!res.ok) throw new Error(`sign in to ${url}: ${res.status}`);
  const { hostname } = new URL(url);
  return res.headers.getSetCookie().map((h) => {
    const [pair] = h.split(";");
    const at = pair.indexOf("=");
    return {
      name: pair.slice(0, at).trim(),
      value: pair.slice(at + 1).trim(),
      domain: hostname,
      path: "/",
    };
  });
}

async function contextFor(browser, base, code, vp, scheme, headers) {
  const ctx = await browser.newContext({
    viewport: VIEWPORTS[vp],
    colorScheme: scheme,
    baseURL: base,
    extraHTTPHeaders: headers,
  });
  if (code) await ctx.addCookies(await signIn(base, code));
  return ctx;
}

function watch(page) {
  const errors = [];
  const failures = [];
  page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
  page.on("pageerror", (e) => errors.push(String(e)));
  page.on("response", (r) => {
    if (r.url().includes("/api/") && r.status() >= 400)
      failures.push(
        `${r.request().method()} ${new URL(r.url()).pathname} ${r.status()}`,
      );
  });
  return { errors, failures };
}

// where reports the page's address and where the named element sits.
async function where(page, sel) {
  return page.evaluate((sel) => {
    const el = sel ? document.querySelector(sel) : null;
    const r = el?.getBoundingClientRect();
    const header = document.querySelector(
      "header, .app-header, [data-testid=page-header]",
    );
    return {
      url: location.pathname + location.hash,
      history: history.length,
      target: sel,
      found: Boolean(el),
      top: r ? Math.round(r.top) : null,
      inView: r ? r.top >= 0 && r.top < innerHeight - 40 : false,
      headerBottom: header
        ? Math.round(header.getBoundingClientRect().bottom)
        : null,
      active:
        document.activeElement?.getAttribute("name") ||
        document.activeElement?.id ||
        document.activeElement?.tagName,
      overflowX:
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    };
  }, sel);
}

async function settle(page) {
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(700);
}

async function item1(browser) {
  const entries = [
    ["/settings/credentials", "#credentials"],
    ["/settings/credentials#githubHost", "[data-setting-id=githubHost]"],
    ["/settings/credentials#gcp-settings", "#gcp-settings"],
    ["/settings/secrets", "#other-secrets"],
    ["/settings/secrets#other-secrets", "#other-secrets"],
    ["/settings/legacy-capabilities", "#credentials"],
    ["/settings/legacy-capabilities#credentials", "#credentials"],
    ["/admin/integrations", "#credentials"],
    ["/admin/integrations#githubHost", "[data-setting-id=githubHost]"],
    ["/system/integrations", "#credentials"],
    ["/system/integrations#gcpProject", "[data-setting-id=gcpProject]"],
    ["/settings/capabilities#credentials", "#credentials"],
  ];
  for (const vp of Object.keys(VIEWPORTS)) {
    const ctx = await contextFor(browser, A, process.env.CODE_A, vp, "light");
    const page = await ctx.newPage();
    for (const [path, sel] of entries) {
      await page.goto(path);
      await settle(page);
      log(1, "entry", { vp, path, ...(await where(page, sel)) });
    }
    // Back/forward round a redirect.
    await page.goto("/settings/general");
    await settle(page);
    await page.goto("/settings/credentials#githubHost");
    await settle(page);
    const after = await where(page, "[data-setting-id=githubHost]");
    await page.goBack();
    await settle(page);
    const back = await where(page, null);
    await page.goForward();
    await settle(page);
    const fwd = await where(page, "[data-setting-id=githubHost]");
    log(1, "back-forward", {
      vp,
      after: after.url,
      back: back.url,
      forward: fwd.url,
      fwdInView: fwd.inView,
    });
    // Settings search.
    for (const q of [
      "legacy",
      "legacy capabilities",
      "credentials",
      "secrets",
      "GitHub host",
      "GCP project",
    ]) {
      await page.goto("/settings/general");
      await settle(page);
      if (vp === "phone") {
        // The phone layout opens on the section list, which holds the filter.
        await page.goto("/settings");
        await settle(page);
      }
      const filter = page.getByTestId("settings-filter").locator("input");
      await filter.fill(q);
      await page.waitForTimeout(300);
      const results = await page.getByTestId("settings-result").allInnerTexts();
      let landed = null;
      if (results.length) {
        await page.getByTestId("settings-result").first().click();
        await settle(page);
        landed = await where(page, "#credentials");
      }
      log(1, "search", {
        vp,
        q,
        results: results.map((r) => r.replace(/\s+/g, " ").slice(0, 60)),
        landed: landed?.url,
        credentialsInView: landed?.inView,
      });
    }
    // In-app hint links naming Capabilities.
    await page.goto("/settings/capabilities#credentials");
    await settle(page);
    const otherLink = page.locator("#credentials a[href='#other-secrets']");
    if (await otherLink.count()) {
      await otherLink.first().click();
      await settle(page);
      log(1, "hint-link other-secrets", {
        vp,
        ...(await where(page, "#other-secrets")),
      });
    }
    const serversLink = page.locator(
      "#other-secrets a[href$='#capability-servers']",
    );
    if (await serversLink.count()) {
      await serversLink.first().click();
      await settle(page);
      log(1, "hint-link capability-servers", {
        vp,
        ...(await where(page, "#capability-servers")),
      });
    }
    await ctx.close();
  }
}

async function cardState(page) {
  return page.evaluate(() => {
    const card = document.querySelector("#credentials");
    if (!card) return { card: false };
    const field = (n) => {
      const el = card.querySelector(`input[name=${n}]`);
      return el
        ? el.matches(":disabled")
          ? "disabled"
          : "editable"
        : "absent";
    };
    const buttons = [...card.querySelectorAll("button")];
    const named = (t) => buttons.filter((b) => b.textContent.trim() === t);
    const usable = (bs) => bs.filter((b) => !b.matches(":disabled")).length;
    const chips = [...card.querySelectorAll(".MuiChip-deletable")];
    return {
      card: true,
      githubHost: field("githubHost"),
      gcpProject: field("gcpProject"),
      gcpServiceAccountEmail: field("gcpServiceAccountEmail"),
      secretInput: field("secret"),
      deleteSecret: `${usable(named("Delete secret"))}/${named("Delete secret").length}`,
      setSecret: `${usable(named("Set secret"))}/${named("Set secret").length}`,
      deletableChips: `${chips.filter((c) => c.getAttribute("aria-disabled") !== "true").length}/${chips.length}`,
      cardNote:
        card.querySelector("[data-testid=credentials-read-only]")
          ?.textContent || "",
      pageNote:
        document.querySelector("[data-testid=settings-role-read-only]")
          ?.textContent || "",
      configNotice: /config repo/i.test(card.textContent),
    };
  });
}

async function item2(browser) {
  for (const who of ["reader", "writer", "admin", "capadmin", "secretsonly"]) {
    const ctx = await contextFor(browser, ROLE, null, "desktop", "light", {
      "X-Iapsim-Email": `${who}@example.com`,
    });
    const page = await ctx.newPage();
    const w = watch(page);
    await page.goto("/settings/capabilities#credentials");
    await settle(page);
    const state = await cardState(page);
    // Try the controls the UI offers; the server's answer is what counts.
    let tried = [];
    page.on("dialog", (d) => d.accept());
    const set = page.locator("#credentials button", { hasText: "Set secret" });
    if ((await set.count()) && (await set.isEnabled())) {
      await page
        .locator("#credentials input[name=secret]")
        .fill(`${who}-ui-probe`);
      await page.locator("#credentials input[name=key]").fill("token");
      await page.locator("#credentials input[name=value]").fill("x");
      await set.click();
      await settle(page);
      tried.push("set");
    }
    const host = page.locator("#credentials input[name=githubHost]");
    if ((await host.count()) && (await host.isEnabled())) {
      await host.fill("ghe.example.com");
      await host.blur();
      await settle(page);
      tried.push("githubHost");
    }
    const del = page.locator("#credentials button", {
      hasText: "Delete secret",
    });
    if ((await del.count()) && (await del.first().isEnabled())) {
      tried.push("delete");
      await del.first().click();
      await settle(page);
    }
    log(2, "role", {
      who,
      ...state,
      tried,
      failures: w.failures,
      errors: w.errors.slice(0, 3),
    });
    await ctx.close();
  }
  // Config-locked mode.
  const ctx = await contextFor(
    browser,
    B,
    process.env.CODE_B,
    "desktop",
    "light",
  );
  const page = await ctx.newPage();
  await page.goto("/settings/capabilities#credentials");
  await settle(page);
  log(2, "config-locked", await cardState(page));
  await ctx.close();
}

async function item3(browser) {
  const ctx = await contextFor(
    browser,
    A,
    process.env.CODE_A,
    "desktop",
    "light",
  );
  const page = await ctx.newPage();
  const w = watch(page);
  const bodies = [];
  page.on("response", async (r) => {
    if (
      r.url().includes("/api/secrets") ||
      r.url().includes("/api/settings") ||
      r.url().includes("/api/capability-servers")
    ) {
      try {
        bodies.push(await r.text());
      } catch {}
    }
  });
  page.on("dialog", (d) => d.accept());
  await page.goto("/settings/capabilities#credentials");
  await settle(page);
  const listing = async () =>
    page.evaluate(() => {
      const rows = (sel) =>
        [...document.querySelectorAll(sel)].map(
          (li) => li.querySelector(".secret-name")?.textContent,
        );
      return {
        other: rows("#other-secrets ul.secrets-list:not([aria-label]) li"),
        usedByServers: rows(
          "#other-secrets ul[aria-label='Secrets used by capability servers'] li",
        ),
        restartChips: [
          ...document.querySelectorAll(
            "#credentials [data-testid=settings-restart-chip], #credentials .MuiChip-label",
          ),
        ]
          .map((c) => c.textContent)
          .filter((t) => /restart/i.test(t)),
      };
    });
  log(3, "initial", await listing());
  const form = (s, k, v) =>
    page
      .locator("#credentials input[name=secret]")
      .fill(s)
      .then(() => page.locator("#credentials input[name=key]").fill(k))
      .then(() => page.locator("#credentials input[name=value]").fill(v));
  await form("walk-new", "token", "WALK-VALUE-1");
  await page.locator("#credentials button", { hasText: "Set secret" }).click();
  await settle(page);
  log(3, "after set walk-new/token", await listing());
  await form("walk-new", "second", "WALK-VALUE-2");
  await page.locator("#credentials button", { hasText: "Set secret" }).click();
  await settle(page);
  const chip = page
    .locator("#other-secrets li", { hasText: "walk-new" })
    .locator(".MuiChip-root", { hasText: "second" })
    .locator(".MuiChip-deleteIcon, [title^=delete]");
  await chip.first().click();
  await settle(page);
  log(3, "after delete key walk-new/second", await listing());
  await page
    .locator("#other-secrets li", { hasText: "walk-new" })
    .getByRole("button", { name: "Delete secret" })
    .click();
  await settle(page);
  log(3, "after delete secret walk-new", await listing());
  const dom = await page.content();
  log(3, "leaks", {
    domHasValue: /WALK-VALUE|s3cr3t|SEEDED-VALUE/.test(dom),
    responseHasValue: bodies.some((b) => /WALK-VALUE|SEEDED-VALUE/.test(b)),
    errors: w.errors.slice(0, 5),
    failures: w.failures,
  });
  await ctx.close();
}

async function item4(browser) {
  for (const [label, base, code] of [
    ["legacy", A, process.env.CODE_A],
    ["no-legacy", B, process.env.CODE_B],
  ]) {
    const ctx = await contextFor(browser, base, code, "desktop", "light");
    const page = await ctx.newPage();
    const probe = async (where) => {
      await settle(page);
      const hint = await page
        .getByText("None offered -- grant access under Grants")
        .count();
      const legacy = await page.getByText("example-legacy").count();
      const grants = await page.getByText(/^Grants$/).count();
      log(4, "picker", { label, where, hint, legacy, grantsHeading: grants });
    };
    for (const path of [
      "/settings/tasks",
      "/repos/acme/app",
      "/templates",
      "/schedules",
    ]) {
      await page.goto(path);
      await probe(path);
    }
    await page.goto("/");
    await settle(page);
    const newTask = page.getByRole("button", { name: /new task/i }).first();
    if (await newTask.count()) {
      await newTask.click();
      await probe("new-task overlay");
    }
    await ctx.close();
  }
}

async function item5(browser) {
  for (const vp of Object.keys(VIEWPORTS)) {
    for (const scheme of ["light", "dark"]) {
      const ctx = await contextFor(browser, A, process.env.CODE_A, vp, scheme);
      const page = await ctx.newPage();
      await page.goto("/settings/capabilities#credentials");
      await settle(page);
      const pos = await where(page, "#credentials");
      const counts = await page.evaluate(() => ({
        servers: document.querySelectorAll(
          "[aria-label='Capability servers'] li, [data-testid=capability-server-row]",
        ).length,
        secrets: document.querySelectorAll("#other-secrets li").length,
        overflowing: [...document.querySelectorAll("#credentials *")].filter(
          (e) =>
            e.scrollWidth > e.clientWidth + 1 &&
            getComputedStyle(e).overflowX === "visible" &&
            e.clientWidth > 0,
        ).length,
      }));
      await page.screenshot({
        path: `${OUT}/capabilities-${vp}-${scheme}.png`,
        fullPage: true,
      });
      log(5, "layout", { vp, scheme, ...pos, ...counts });
      await ctx.close();
    }
  }
}

const browser = await chromium.launch();
try {
  if (items.has("1") && A) await item1(browser);
  if (items.has("2") && ROLE) await item2(browser);
  if (items.has("3") && A) await item3(browser);
  if (items.has("4") && A && B) await item4(browser);
  if (items.has("5") && A) await item5(browser);
} finally {
  await browser.close();
}
