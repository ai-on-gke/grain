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

// §13 item 1: /next/ (ui-ng) against /inbox (React) over a running
// `grain demo -github-sim -fake-ci`. Run from ui/ (it borrows ui's
// @playwright/test):
//   URL=http://127.0.0.1:4290 CODE=<access code> \
//     PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-11-new-ui-surfaces/next-walk.mjs [step...]
// Steps: counts (default), gate, deeplinks, layout, viewer, act, live.
import { chromium } from "@playwright/test";

const URL_ = process.env.URL || "http://127.0.0.1:4290";
const CODE = process.env.CODE;
const steps = process.argv.slice(2).length ? process.argv.slice(2) : ["counts"];

async function signIn() {
  const res = await fetch(`${URL_}/api/auth/access-code`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code: CODE, name: "alice@example.com" }),
  });
  if (!res.ok) throw new Error(`sign in: ${res.status} ${await res.text()}`);
  const { hostname } = new URL(URL_);
  return res.headers.getSetCookie().map((c) => {
    const [nv] = c.split(";");
    const i = nv.indexOf("=");
    return {
      name: nv.slice(0, i),
      value: nv.slice(i + 1),
      domain: hostname,
      path: "/",
    };
  });
}

const browser = await chromium.launch();
const cookies = await signIn();
async function newPage(opts = {}) {
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    ...opts,
  });
  await ctx.addCookies(cookies);
  const page = await ctx.newPage();
  page.on("pageerror", (e) => console.log(`  pageerror: ${e.message}`));
  return page;
}

async function reactGroups(page) {
  await page.goto(`${URL_}/inbox`);
  await page.waitForSelector(".inbox-group-header h3", { timeout: 15000 });
  await page.waitForTimeout(1500);
  return page.$$eval("section.inbox-group", (els) =>
    els.map((s) => ({
      id: s.dataset.sectionId,
      title: s.querySelector(".inbox-group-header h3")?.textContent.trim(),
      count: Number(s.querySelector(".inbox-group-header .count")?.textContent),
    })),
  );
}

async function nextGroups(page) {
  await page.goto(`${URL_}/next/inbox`);
  await page.waitForSelector("[data-testid=next-inbox-queue]", {
    timeout: 15000,
  });
  await page.waitForTimeout(1500);
  const groups = await page.$$eval("[data-testid^=inbox-group-]", (els) =>
    els.map((s) => ({
      id: s.dataset.testid.replace("inbox-group-", ""),
      title: s.querySelector("h2")?.childNodes[0]?.textContent.trim(),
      count: Number(s.querySelector(".group-count")?.textContent),
      rows: s.querySelectorAll("[data-testid=queue-row]").length,
    })),
  );
  const filters = await page.$$eval("[data-testid^=filter-]", (els) =>
    Object.fromEntries(
      els.map((e) => [
        e.dataset.testid.replace("filter-", ""),
        Number(e.querySelector(".tabular")?.textContent),
      ]),
    ),
  );
  return { groups, filters };
}

for (const step of steps) {
  console.log(`=== ${step}`);
  if (step === "counts") {
    const page = await newPage();
    const r = await reactGroups(page);
    const n = await nextGroups(page);
    console.log("  React /inbox groups:");
    for (const g of r) console.log(`    ${g.count}\t${g.id}\t${g.title}`);
    console.log("  /next/inbox groups:");
    for (const g of n.groups)
      console.log(`    ${g.count} (${g.rows} rows)\t${g.id}\t${g.title}`);
    console.log(`  /next/ filter counts: ${JSON.stringify(n.filters)}`);
    console.log(
      `  totals: React ${r.reduce((a, g) => a + g.count, 0)}, next ${n.groups.reduce((a, g) => a + g.count, 0)}`,
    );
  }
  if (step === "gate") {
    for (const p of ["/next/", "/next/inbox", "/next/api/x", "/next/main.js"]) {
      const res = await fetch(`${URL_}${p}`, { redirect: "manual" });
      console.log(
        `  no code: GET ${p} -> ${res.status} ${res.headers.get("location") || ""}`,
      );
    }
    const cookie = cookies.map((c) => `${c.name}=${c.value}`).join("; ");
    const hdr = await fetch(`${URL_}/next/`, { headers: { Cookie: cookie } });
    console.log(`  with code: GET /next/ -> ${hdr.status}`);
    for (const h of [
      "content-security-policy",
      "x-frame-options",
      "x-content-type-options",
      "referrer-policy",
    ])
      console.log(`    ${h}: ${(hdr.headers.get(h) || "(none)").slice(0, 90)}`);
    // A browser-shaped cross-site POST with the session cookie: refused.
    for (const [label, headers] of [
      [
        "cross-site",
        { "Sec-Fetch-Site": "cross-site", Origin: "https://evil.example" },
      ],
      ["foreign Origin", { Origin: "https://evil.example" }],
      ["same-origin", { "Sec-Fetch-Site": "same-origin", Origin: URL_ }],
    ]) {
      const res = await fetch(`${URL_}/api/notifications/ack-all`, {
        method: "POST",
        headers: {
          Cookie: cookie,
          "Content-Type": "application/json",
          ...headers,
        },
        body: "{}",
      });
      console.log(
        `  POST /api/notifications/ack-all (${label}) -> ${res.status}`,
      );
    }
  }
  if (step === "deeplinks") {
    const page = await newPage();
    for (const p of [
      "/next/",
      "/next/inbox",
      "/next/inbox?filter=proposals",
      "/next/nowhere/deep",
      "/next/tasks/1",
    ]) {
      const res = await page.goto(`${URL_}${p}`);
      await page.waitForTimeout(800);
      const h = await page.locator("[data-testid=next-inbox-headline]").count();
      console.log(
        `  ${p} -> HTTP ${res.status()} lands on ${new URL(page.url()).pathname}${new URL(page.url()).search} inbox=${h > 0}`,
      );
    }
  }
  if (step === "layout") {
    for (const [w, h] of [
      [1440, 900],
      [390, 844],
    ]) {
      for (const scheme of ["light", "dark"]) {
        const page = await newPage({
          viewport: { width: w, height: h },
          colorScheme: scheme,
          isMobile: w < 500,
          hasTouch: w < 500,
        });
        await page.goto(`${URL_}/next/inbox`);
        await page.waitForSelector("[data-testid=next-inbox-queue]");
        await page.waitForTimeout(800);
        const audit = await page.evaluate(() => {
          const doc = document.documentElement;
          const over = [];
          for (const el of document.querySelectorAll("body *")) {
            const r = el.getBoundingClientRect();
            if (r.width && r.right > doc.clientWidth + 1)
              over.push(
                `${el.tagName.toLowerCase()}.${[...el.classList].join(".")}`,
              );
          }
          return {
            scrollW: doc.scrollWidth,
            clientW: doc.clientWidth,
            theme:
              doc.dataset.theme || getComputedStyle(document.body).colorScheme,
            bg: getComputedStyle(document.body).backgroundColor,
            overflow: over.slice(0, 6),
          };
        });
        console.log(`  ${w}x${h} ${scheme}: ${JSON.stringify(audit)}`);
        await page.context().close();
      }
    }
  }
  if (step === "viewer") {
    const page = await newPage();
    const viewer = {
      role: "grain/viewer",
      roles: ["grain/viewer"],
      full: false,
      scopes: ["tasks:read", "repos:read", "settings:read"],
    };
    await page.route("**/api/config", async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      await route.fulfill({ response: res, json: { ...body, access: viewer } });
    });
    await nextGroups(page);
    const rows = page.locator("[data-testid=queue-row]");
    const n = await rows.count();
    const seen = new Set();
    for (let i = 0; i < n; i++) {
      await rows.nth(i).click();
      await page.waitForTimeout(150);
      const btns = await page.$$eval(
        "[data-testid=resolution-dock] button:not([disabled]), [data-testid^=action-]:not([disabled])",
        (els) => els.map((e) => e.textContent.trim()),
      );
      const denied = await page.locator("[data-testid=rbac-denied]").count();
      const title = await page
        .locator("[data-testid=inspector-title]")
        .textContent();
      const line = `${title.trim().slice(0, 40)} enabled=[${btns.join(",")}] rbacNote=${denied}`;
      if (!seen.has(line)) console.log(`    ${line}`);
      seen.add(line);
    }
    const bulk = await page.$$eval("[data-testid^=bulk-]", (e) => e.length);
    console.log(`  viewer: ${n} rows, bulk buttons ${bulk}`);
  }
  if (step === "live") {
    const page = await newPage();
    const events = [];
    page.on("request", (r) => {
      if (r.url().includes("/api/events")) events.push(r.url());
    });
    const before = await nextGroups(page);
    const total = (g) => g.groups.reduce((a, x) => a + x.count, 0);
    console.log(
      `  /api/events requests: ${events.length}; total before ${total(before)}`,
    );
    // Raise an alert out of band; it should appear without a reload.
    const res = await fetch(`${URL_}/api/tasks`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${CODE}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        title: "Live-update probe proposal",
        description: "filed by next-walk.mjs live",
        repo: "acme/widgets",
        approved: false,
      }),
    });
    console.log(`  POST /api/tasks (unapproved) -> ${res.status}`);
    const t0 = Date.now();
    let found = false;
    while (Date.now() - t0 < 15000) {
      found = (await page.getByText("Live-update probe proposal").count()) > 0;
      if (found) break;
      await page.waitForTimeout(250);
    }
    console.log(
      `  probe proposal appeared without reload: ${found} after ${Date.now() - t0}ms`,
    );
  }
  if (step === "act") {
    const page = await newPage();
    await nextGroups(page);
    const act = async (group, fn) => {
      const row = page
        .locator(`[data-testid=inbox-group-${group}] [data-testid=queue-row]`)
        .first();
      if (!(await row.count())) {
        console.log(`  ${group}: no row`);
        return;
      }
      const title = (await row.textContent()).trim().slice(0, 60);
      await row.click();
      const before = await page
        .locator(`[data-testid=inbox-group-${group}] [data-testid=queue-row]`)
        .count();
      const resp = page.waitForResponse(
        (r) => r.request().method() !== "GET" && r.url().includes("/api/"),
        { timeout: 10000 },
      );
      await fn();
      const r = await resp.catch((e) => null);
      await page.waitForTimeout(1500);
      const after = await page
        .locator(`[data-testid=inbox-group-${group}] [data-testid=queue-row]`)
        .count();
      const err = await page.locator("[data-testid=action-error]").count();
      console.log(
        `  ${group}: "${title}" -> ${r ? `${r.request().method()} ${new URL(r.url()).pathname} ${r.status()}` : "no request"}; rows ${before} -> ${after}; error banner ${err}`,
      );
    };
    await act("reply", async () => {
      await page.getByTestId("reply-input").fill("Answer from /next/ walk");
      await page.getByTestId("send-reply").click();
    });
    await act("access", async () => {
      await page.getByTestId("grant-access").click();
      if (await page.getByTestId("confirm-accept").count())
        await page.getByTestId("confirm-accept").click();
    });
    await act("proposal", async () => {
      const b = page
        .locator("[data-testid^=action-]", { hasText: /Approve/ })
        .first();
      await b.click();
      if (await page.getByTestId("confirm-accept").count())
        await page.getByTestId("confirm-accept").click();
    });
  }
}
await browser.close();
