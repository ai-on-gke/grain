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

// The browser half of docs/test-plans/2026-11-02.md §1 ("not proved" 4
// and 5), against a real `grain daemon` behind the iapstub stand-in for
// IAP. Not a CI spec: it needs that daemon running.
//
//   UI=/path/to/checkout/ui DAEMON=http://127.0.0.1:4300 CODE=<access code> \
//   SHORT=<jwt for a short allowed email> LONG=<jwt for a long allowed email> \
//   LONG_EMAIL=<that email> SHORT_EMAIL=<that email> \
//   PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright node browser.mjs
//
// MODE=noverifier checks the daemon started without -ui-iap-audience
// instead: no badge, and the access-code login still works.
import { createRequire } from "node:module";
import http from "node:http";

const require = createRequire(`${process.env.UI}/package.json`);
const { chromium } = require("@playwright/test");

const DAEMON = process.env.DAEMON;
const CODE = process.env.CODE;
const ATTACKER_PORT = 18090;
const ATTACKER = `http://localhost:${ATTACKER_PORT}`; // another site: localhost vs 127.0.0.1
const H = "X-Goog-IAP-JWT-Assertion";
const rows = [];
const row = (step, result, ok) => {
  rows.push({ step, result, ok });
  console.log(`| ${step} | ${result} | ${ok ? "pass" : "FAIL"} |`);
};

async function api(method, path, body) {
  const res = await fetch(DAEMON + path, {
    method,
    headers: {
      Authorization: `Bearer ${CODE}`,
      "Content-Type": "application/json",
    },
    body: body && JSON.stringify(body),
  });
  return res.json();
}

async function commentCount(id, marker) {
  const t = await api("GET", `/api/tasks/${id}`);
  return JSON.stringify(t).split(marker).length - 1;
}

function attackerServer(taskID) {
  const target = `${DAEMON}/api/tasks/${taskID}/comments`;
  const pages = {
    // A text/plain form whose body happens to be JSON: {"body":"csrf-form","x":"="}
    "/form": `<form id=f method=POST action="${target}" enctype="text/plain">
      <input name='{"body":"csrf-form","x":"' value='"}'></form><script>f.submit()</script>`,
    "/fetch": `<script>fetch(${JSON.stringify(target)}, {method: "POST", mode: "no-cors",
      headers: {"Content-Type": "text/plain"}, body: JSON.stringify({body: "csrf-fetch"})})
      .finally(() => document.title = "sent")</script>`,
    "/link": `<a id=a href="${DAEMON}/">go</a><script>a.click()</script>`,
  };
  const srv = http.createServer((req, res) => {
    res.setHeader("Content-Type", "text/html");
    res.end(pages[req.url] ?? "not found");
  });
  return new Promise((resolve) =>
    srv.listen(ATTACKER_PORT, "127.0.0.1", () => resolve(srv)),
  );
}

// postsTo records the status of every POST the context sends to the daemon.
function postsTo(context) {
  const seen = [];
  context.on("response", (res) => {
    const req = res.request();
    if (req.method() === "POST" && req.url().startsWith(DAEMON))
      seen.push({ url: req.url(), status: res.status() });
  });
  return seen;
}

async function csrf(browser, label, contextOpts, taskID) {
  const context = await browser.newContext(contextOpts);
  const posts = postsTo(context);
  const page = await context.newPage();
  await page.goto(DAEMON + "/");
  await page
    .waitForLoadState("networkidle", { timeout: 10_000 })
    .catch(() => {});

  // Same-origin POST from the UI's own origin, as the UI's fetches are sent.
  const same = await page.evaluate(
    async ([id, body]) => {
      const r = await fetch(`/api/tasks/${id}/comments`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ body }),
      });
      return r.status;
    },
    [taskID, `same-origin-${label}`],
  );
  row(
    `${label}: same-origin POST /api/tasks/${taskID}/comments from the UI origin`,
    `${same}`,
    same === 200,
  );

  // The UI's own comment composer.
  await page.goto(`${DAEMON}/tasks/${taskID}`);
  const box = page.getByPlaceholder("Reply...");
  try {
    await box.fill(`composer-${label}`, { timeout: 10_000 });
    const before = posts.length;
    await page.getByRole("button", { name: "Comment", exact: true }).click();
    await page.waitForTimeout(1500);
    const composerPosts = posts
      .slice(before)
      .filter((p) => p.url.endsWith("/comments"));
    const n = await commentCount(taskID, `composer-${label}`);
    row(
      `${label}: comment through the task page's composer`,
      `POST ${composerPosts.map((p) => p.status).join(",") || "none"}; stored ${n}`,
      n === 1,
    );
  } catch (e) {
    row(
      `${label}: comment through the task page's composer`,
      `could not drive the composer: ${e.message.split("\n")[0]}`,
      false,
    );
  }

  for (const kind of ["form", "fetch"]) {
    const before = posts.length;
    const marker = `csrf-${kind}`;
    const was = await commentCount(taskID, marker);
    await page.goto(`${ATTACKER}/${kind}`);
    await page.waitForTimeout(1500);
    const status =
      posts
        .slice(before)
        .map((p) => p.status)
        .join(",") || "none";
    const now = await commentCount(taskID, marker);
    row(
      `${label}: cross-site ${kind} POST from ${ATTACKER}`,
      `daemon answered ${status}; comment stored: ${now > was}`,
      now === was && !/^2/.test(status),
    );
  }

  // GET from another site is a plain navigation and is unaffected.
  const nav = await page.goto(`${ATTACKER}/link`).then(() =>
    page.waitForURL(`${DAEMON}/`, { timeout: 10_000 }).then(
      () => true,
      () => false,
    ),
  );
  const title = await page.title();
  row(
    `${label}: cross-site link (GET ${DAEMON}/)`,
    `navigated: ${nav}; title "${title}"`,
    nav,
  );
  await context.close();
}

async function layout(browser, token, email, out) {
  for (const [width, viewport] of [
    ["1440", { width: 1440, height: 900 }],
    ["390", { width: 390, height: 844 }],
  ]) {
    for (const theme of ["light", "dark"]) {
      const context = await browser.newContext({
        viewport,
        colorScheme: theme,
        isMobile: width === "390",
        hasTouch: width === "390",
        extraHTTPHeaders: { [H]: token },
      });
      await context.addInitScript(
        (t) => localStorage.setItem("grain.themeMode", t),
        theme,
      );
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      let cleared = false;
      await page.route("**/_gcp_iap/clear_login_cookie", (r) => {
        cleared = true;
        return r.fulfill({
          status: 200,
          contentType: "text/plain",
          body: "IAP logged out",
        });
      });
      await page.goto(DAEMON + "/");
      const badge = page.getByTestId("top-bar-user");
      const label = `${email.length > 30 ? "long" : "short"} ${width}px ${theme}`;
      try {
        await badge.waitFor({ state: "visible", timeout: 10_000 });
      } catch {
        const drawer = await page
          .getByRole("button", { name: /menu|navigation/i })
          .count();
        row(
          `${label}: badge`,
          `no signed-in badge on the page (top-bar-user hidden); PhoneNav menu buttons: ${drawer}`,
          false,
        );
        // What a phone user has instead: Settings -> Access, straight.
        await page.goto(DAEMON + "/settings/access");
        const shows = await page
          .getByText(`Signed in through IAP as ${email}.`)
          .waitFor({ timeout: 10_000 })
          .then(
            () => true,
            () => false,
          );
        const overflow = await page.evaluate(
          () => document.documentElement.scrollWidth - window.innerWidth,
        );
        row(
          `${label}: Settings → Access`,
          `says "Signed in through IAP as …": ${shows}; page horizontal overflow ${overflow}px`,
          shows && overflow <= 0,
        );
        await page.screenshot({
          path: `${out}/access-${email.length > 30 ? "long" : "short"}-${width}-${theme}.png`,
        });
        const logout = page.getByRole("button", { name: "Log out" });
        if (await logout.count()) {
          await logout.click();
          await page
            .waitForURL("**/_gcp_iap/clear_login_cookie", { timeout: 10_000 })
            .catch(() => {});
        }
        row(
          `${label}: Log out (from Settings → Access)`,
          `landed on ${new URL(page.url()).pathname}; IAP clear-cookie fetched: ${cleared}`,
          cleared,
        );
        await context.close();
        continue;
      }
      const geo = await page.evaluate(() => {
        const box = (el) => el && el.getBoundingClientRect().toJSON();
        const b = document.querySelector('[data-testid="top-bar-user"]');
        const g = document.querySelector('[data-testid="page-header-globals"]');
        const header = b.closest("header") || g?.parentElement;
        return {
          badge: box(b),
          globals: box(g),
          header: box(header),
          text: b.textContent,
          title: b.getAttribute("title") || b.getAttribute("aria-label"),
          clipped: b.scrollWidth > b.clientWidth,
          docOverflow: document.documentElement.scrollWidth - window.innerWidth,
        };
      });
      const inGlobals =
        geo.globals &&
        geo.badge.left >= geo.globals.left - 1 &&
        geo.badge.right <= geo.globals.right + 1 &&
        geo.badge.top >= geo.globals.top - 1 &&
        geo.badge.bottom <= geo.globals.bottom + 1;
      const inViewport =
        geo.badge.left >= 0 && geo.badge.right <= viewport.width;
      row(
        `${label}: badge geometry`,
        `badge ${Math.round(geo.badge.left)}..${Math.round(geo.badge.right)} x ${Math.round(geo.badge.top)}..${Math.round(geo.badge.bottom)}, ` +
          `globals ${geo.globals ? `${Math.round(geo.globals.left)}..${Math.round(geo.globals.right)} x ${Math.round(geo.globals.top)}..${Math.round(geo.globals.bottom)}` : "none"}; ` +
          `text "${geo.text}"${geo.clipped ? " (ellipsized" + (geo.title ? `, full address in title/aria-label` : ", NO title") + ")" : ""}; page horizontal overflow ${geo.docOverflow}px`,
        inGlobals &&
          inViewport &&
          geo.docOverflow <= 0 &&
          (!geo.clipped || geo.title?.includes(email)),
      );
      await page.screenshot({
        path: `${out}/home-${email.length > 30 ? "long" : "short"}-${width}-${theme}.png`,
      });

      await badge.click();
      const menu = page.getByRole("menu");
      await menu.waitFor({ state: "visible", timeout: 5_000 }).catch(() => {});
      const mbox = await menu.boundingBox().catch(() => null);
      const items = await page.getByRole("menuitem").allTextContents();
      row(
        `${label}: menu`,
        mbox
          ? `menu ${Math.round(mbox.x)}..${Math.round(mbox.x + mbox.width)} x ${Math.round(mbox.y)}..${Math.round(mbox.y + mbox.height)}; items ${JSON.stringify(items)}`
          : "no menu",
        !!mbox && mbox.x >= 0 && mbox.x + mbox.width <= viewport.width + 1,
      );
      await page.screenshot({
        path: `${out}/menu-${email.length > 30 ? "long" : "short"}-${width}-${theme}.png`,
      });

      await page.getByRole("menuitem", { name: "Access settings" }).click();
      const signedIn = page.getByText(`Signed in through IAP as ${email}.`);
      const shows = await signedIn.waitFor({ timeout: 10_000 }).then(
        () => true,
        () => false,
      );
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      row(
        `${label}: Settings → Access`,
        `says "Signed in through IAP as …": ${shows}; page horizontal overflow ${overflow}px`,
        shows && overflow <= 0,
      );
      await page.screenshot({
        path: `${out}/access-${email.length > 30 ? "long" : "short"}-${width}-${theme}.png`,
      });

      await page.getByRole("button", { name: "Log out" }).click();
      await page
        .waitForURL("**/_gcp_iap/clear_login_cookie", { timeout: 10_000 })
        .catch(() => {});
      row(
        `${label}: Log out`,
        `landed on ${new URL(page.url()).pathname}; IAP clear-cookie fetched: ${cleared}`,
        cleared && page.url().endsWith("/_gcp_iap/clear_login_cookie"),
      );
      if (errors.length) row(`${label}: page errors`, errors.join("; "), false);
      await context.close();
    }
  }
}

async function noVerifier(browser) {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
  });
  const page = await context.newPage();
  await page.goto(DAEMON + "/");
  const input = page.locator('input[name="code"]');
  const hasForm = await input.waitFor({ timeout: 10_000 }).then(
    () => true,
    () => false,
  );
  row(
    "no verifier: unauthenticated / shows the access-code page",
    `${hasForm}`,
    hasForm,
  );
  await input.fill(CODE);
  await input.press("Enter");
  const header = page.getByTestId("page-header-globals");
  const inUI = await header.waitFor({ timeout: 15_000 }).then(
    () => true,
    () => false,
  );
  const badges = await page.getByTestId("top-bar-user").count();
  row(
    "no verifier: access-code login reaches the UI; signed-in badge absent",
    `in UI: ${inUI}; badge count ${badges}`,
    inUI && badges === 0,
  );
  const id = (
    await api("POST", "/api/tasks", {
      title: "noverifier",
      body: "b",
      noRepo: true,
    })
  ).id;
  const status = await page.evaluate(
    async (id) =>
      (
        await fetch(`/api/tasks/${id}/comments`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: '{"body":"x"}',
        })
      ).status,
    id,
  );
  row(
    "no verifier: same-origin POST with the access session",
    `${status}`,
    status === 200,
  );
  await context.close();
}

const browser = await chromium.launch();
let attacker;
try {
  if (process.env.MODE === "noverifier") {
    await noVerifier(browser);
  } else {
    const id = (
      await api("POST", "/api/tasks", {
        title: "csrf walkthrough",
        body: "b",
        noRepo: true,
      })
    ).id;
    attacker = await attackerServer(id);
    await csrf(
      browser,
      "IAP assertion",
      { extraHTTPHeaders: { [H]: process.env.SHORT } },
      id,
    );
    // The access-code path: sign in on the page, then the same requests.
    const ctx = await browser.newContext();
    const p = await ctx.newPage();
    await p.goto(DAEMON + "/");
    await p.locator('input[name="code"]').fill(CODE);
    await p.locator('input[name="code"]').press("Enter");
    await p.getByTestId("page-header-globals").waitFor({ timeout: 15_000 });
    const cookies = await ctx.cookies();
    await ctx.close();
    await csrf(
      browser,
      "access code",
      { storageState: { cookies, origins: [] } },
      id,
    );
    const out = process.env.OUT || "/tmp";
    await layout(browser, process.env.SHORT, process.env.SHORT_EMAIL, out);
    await layout(browser, process.env.LONG, process.env.LONG_EMAIL, out);
  }
} finally {
  await browser.close();
  attacker?.close();
}
const failed = rows.filter((r) => !r.ok);
console.log(`\n${rows.length - failed.length}/${rows.length} passed`);
process.exit(failed.length ? 1 : 0);
