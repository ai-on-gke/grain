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

// The browser half of tests/iap/walkthrough.sh: a real Chromium driving the
// visible UI of `grain demo` behind iapsim's proxy. Run from ui/ (for its
// node_modules) by walkthrough.sh, which sets MODE and the addresses; it
// prints result rows in walkthrough.sh's table format, saves screenshots to
// OUT, and exits 1 if a check failed.
import { createRequire } from "node:module";
import http from "node:http";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const { MODE, OUT, PROXY, DEMO, NET, CODE } = process.env;
const ATTACKER_PORT = 18091;
const ATTACKER = `http://localhost:${ATTACKER_PORT}`; // a different site from 127.0.0.1
let failed = 0;
const caseOf = {
  iap: "1",
  csrf: "9",
  bad: "3",
  allowlist: "2",
  attribution: "8",
  noiap: "6",
  requireiap: "7",
};
// check records one row; otherwise, as in walkthrough.sh's check, is what a
// mismatch is recorded as instead of FAIL (N/A or DIFFERS), uncounted.
function check(c, what, ok, got, otherwise) {
  if (!ok && !otherwise) failed++;
  const g = String(got).replace(/\|/g, "\\|").replace(/\n/g, " ").slice(0, 160);
  console.log(`| ${caseOf[c] || c} | browser: ${what} | | \`${g}\` | ${ok ? "PASS" : otherwise || "FAIL"} |`);
}
const shot = (page, name) => page.screenshot({ path: path.join(OUT, `${name}.png`), fullPage: false });

// signIn walks the simulated Google sign-in iapsim's proxy shows a browser
// that has not signed in, and lands on the UI.
async function signIn(page, email) {
  await page.goto(`${PROXY}/`);
  await page.waitForURL(/\/_iapsim\/login/);
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Continue" }).click();
  await page.waitForLoadState("networkidle");
}

const browser = await chromium.launch();
const desktop = { viewport: { width: 1440, height: 900 } };
try {
  if (MODE === "iap") {
    const ctx = await browser.newContext(desktop);
    const page = await ctx.newPage();
    await page.goto(`${PROXY}/`);
    await page.waitForURL(/\/_iapsim\/login/);
    await shot(page, "01-iap-simulated-google-signin");
    await page.getByLabel("Email").fill("owner@example.com");
    await page.getByRole("button", { name: "Continue" }).click();
    const tile = page.getByTestId("top-bar-user");
    await tile.waitFor({ timeout: 20000 }).catch(() => {});
    const tileText = (await tile.textContent().catch(() => "")) || "";
    check("iap", "UI loads with no access code; user tile", tileText.trim() === "owner@example.com", tileText);
    const body = await page.locator("body").innerText();
    check("iap", "no 'Enter access code' page", !/Enter access code/.test(body), page.url());
    const cookies = await ctx.cookies();
    check("iap", "no grain_access cookie is set", !cookies.some((c) => c.name === "grain_access"), cookies.map((c) => c.name).join(","));
    await shot(page, "02-iap-ui-signed-in-owner");
    const st = await page.evaluate(async () => {
      const r = await fetch("/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title: "filed from the UI's origin" }),
      });
      return `${r.status} ${(await r.json()).author}`;
    });
    check("iap", "same-origin POST from the page", /^201 owner@example.com$/.test(st), st);
    await tile.click();
    await shot(page, "03-iap-user-menu");
    await page.getByRole("menuitem", { name: "Log out" }).click();
    await page.waitForURL(/clear_login_cookie/, { timeout: 10000 }).catch(() => {});
    check("iap", "Log out goes to IAP's clear_login_cookie", /\/_gcp_iap\/clear_login_cookie/.test(page.url()), page.url());
    await ctx.close();

    // 9. CSRF from another site. The IAP cookie travels with a cross-site
    // request in real IAP; here the context sends the person's identity on
    // every request, the attacker's included, to the same effect.
    const attacker = http
      .createServer((req, res) => {
        res.setHeader("Content-Type", "text/html");
        if (req.url === "/form") {
          res.end(`<form id=f method=post enctype="text/plain" action="${PROXY}/api/tasks">
<input name='{"title":"csrf-browser-form","x":"' value='"}'></form><script>f.submit()</script>`);
        } else {
          res.end(`<script>fetch("${PROXY}/api/tasks",{method:"POST",mode:"no-cors",credentials:"include",
headers:{"Content-Type":"text/plain"},body:JSON.stringify({title:"csrf-browser-fetch"})}).then(()=>document.title="sent")</script>`);
        }
      })
      .listen(ATTACKER_PORT, "127.0.0.1");
    const victim = await browser.newContext({ ...desktop, extraHTTPHeaders: { "X-Iapsim-Email": "owner@example.com" } });
    const vp = await victim.newPage();
    const formResp = vp.waitForResponse((r) => r.url().endsWith("/api/tasks"), { timeout: 10000 });
    await vp.goto(`${ATTACKER}/form`);
    const form = await formResp;
    check("csrf", "cross-site form POST with a valid assertion", form.status() === 403, `${form.status()} ${(await form.text()).slice(0, 100)}`);
    await shot(vp, "04-csrf-cross-site-form-refused");
    const fetched = vp.waitForResponse((r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST", { timeout: 10000 });
    await vp.goto(`${ATTACKER}/fetch`);
    const fr = await fetched;
    check("csrf", "cross-site no-cors fetch POST with a valid assertion", fr.status() === 403, fr.status());
    const titles = await (await victim.request.get(`${PROXY}/api/tasks`)).json();
    const list = Array.isArray(titles) ? titles : titles.tasks || [];
    check("csrf", "neither cross-site write was stored", !list.some((t) => /^csrf-browser/.test(t.title)), list.length);
    attacker.close();
    await victim.close();

    // 3. A browser reaching grain with an assertion that does not verify
    // (expired here) is refused with a page, not the API's JSON.
    for (const [w, h, name] of [[1440, 900, "11-bad-assertion-page"], [390, 844, "11b-bad-assertion-page-phone"]]) {
      const bctx = await browser.newContext({ viewport: { width: w, height: h }, extraHTTPHeaders: { "X-Goog-IAP-JWT-Assertion": process.env.BADTOK } });
      const bp = await bctx.newPage();
      const r = await bp.goto(`${DEMO}/`);
      const h1 = (await bp.locator("h1").first().innerText().catch(() => "")) || "";
      const ow = await bp.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      check("bad", `expired assertion, browser at ${w}px: 401 page, fits`, r.status() === 401 && /Sign-in could not be verified/.test(h1) && ow <= 0, `${r.status()} ${h1} overflow ${ow}px`);
      await shot(bp, name);
      await bctx.close();
    }
  }

  if (MODE === "allowlist") {
    const ctx = await browser.newContext(desktop);
    const page = await ctx.newPage();
    await signIn(page, "bob@example.com");
    const who = (await page.locator("#refused-principal").innerText().catch(() => "")) || "";
    check("allowlist", "refusal page names bob", /bob@example\.com/.test(who), who);
    const advice = (await page.locator("#refused-advice").innerText().catch(() => "")) || "";
    check("allowlist", "refusal page says how the owner adds bob", /user:bob@example\.com/.test(advice) && /Settings/.test(advice), advice);
    const form = await page.locator("input#code").count();
    const prec = (await page.locator("#refused-precedence").innerText().catch(() => "")) || "";
    check("allowlist", "refusal page offers the access-code form", form === 1, form ? "form shown" : `no form; says: ${prec}`,
      "DIFFERS: by design for IAP (identity beats the code), the page explains how to use the code instead");
    await shot(page, "05-refusal-page-bob");
    await page.setViewportSize({ width: 390, height: 844 });
    await shot(page, "05b-refusal-page-bob-phone");
    const ow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    check("allowlist", "refusal page fits a 390px phone", ow <= 0, `overflow ${ow}px`);
    await ctx.close();

    const actx = await browser.newContext(desktop);
    const ap = await actx.newPage();
    await signIn(ap, "alice@example.com");
    const t = ((await ap.getByTestId("top-bar-user").textContent({ timeout: 20000 }).catch(() => "")) || "").trim();
    check("allowlist", "alice (user:) gets in; tile", t === "alice@example.com", t);
    await ap.goto(`${PROXY}/tasks/${process.env.ATTR_TASK}`);
    await ap.waitForLoadState("networkidle");
    await ap.getByText("a comment from alice").first().waitFor({ timeout: 15000 }).catch(() => {});
    const txt = await ap.locator("body").innerText();
    const n = (txt.match(/alice@example\.com/g) || []).length;
    check("attribution", "task page names alice as filer/commenter/closer", n >= 2, `${n} mentions`);
    check("attribution", "task page shows 'operator (access code)' nowhere for alice's task", !/operator \(access code\)/.test(txt), "-");
    await shot(ap, "06-attribution-task-page-alice");
    await actx.close();
  }

  if (MODE === "noiap") {
    const ctx = await browser.newContext(desktop);
    const page = await ctx.newPage();
    await page.goto(`${DEMO}/`);
    const h1 = (await page.locator("h1").first().innerText().catch(() => "")) || "";
    check("noiap", "login page shown", /Enter access code/.test(h1), h1);
    await shot(page, "07-noiap-access-code-page");
    await page.locator("input#code").fill(CODE);
    await page.getByRole("button", { name: "Continue" }).click();
    await page.waitForLoadState("networkidle");
    const cookie = (await ctx.cookies()).find((c) => c.name === "grain_access");
    check("noiap", "grain_access cookie set, SameSite=Strict, HttpOnly", cookie && cookie.sameSite === "Strict" && cookie.httpOnly, JSON.stringify(cookie && { sameSite: cookie.sameSite, httpOnly: cookie.httpOnly }));
    const h = await page.locator("body").innerText();
    check("noiap", "the UI loads after the code", !/Enter access code/.test(h) && h.length > 200, page.url());
    const tile = await page.getByTestId("top-bar-user").count();
    check("noiap", "no signed-in email tile with the access code", tile === 0, tile);
    await shot(page, "08-noiap-ui-after-code");
    await ctx.close();
  }

  if (MODE === "requireiap") {
    const ctx = await browser.newContext(desktop);
    const page = await ctx.newPage();
    await signIn(page, "owner@example.com");
    const t = ((await page.getByTestId("top-bar-user").textContent({ timeout: 20000 }).catch(() => "")) || "").trim();
    check("requireiap", "through IAP: UI with no code; tile", t === "owner@example.com", t);
    await shot(page, "09-require-iap-ui-through-iap");
    const net = await browser.newContext(desktop);
    const np = await net.newPage();
    const resp = await np.goto(`${NET}/`);
    const body = await np.locator("body").innerText();
    check("requireiap", "over the network without IAP: no access-code form", (await np.locator("input#code").count()) === 0, `${resp.status()} ${body.slice(0, 120)}`);
    await shot(np, "10-require-iap-network-no-login");
    await net.close();
    await ctx.close();
  }
} catch (e) {
  failed++;
  console.log(`| ${caseOf[MODE] || MODE} | browser: ${MODE} threw | | \`${String(e).slice(0, 160).replace(/\|/g, "\\|").replace(/\n/g, " ")}\` | FAIL |`);
} finally {
  await browser.close();
}
process.exit(failed ? 1 : 0);
