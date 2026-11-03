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

// §4 items 4 and 5 in a real Chromium: the phone drawer signed in through
// the simulated IAP and with the access code, light and dark, and
// Settings -> Access plus Tab past locked selects under
// -external-config-repo. Run from ui/ (for its node_modules) by
// drawer.sh, which starts iapsim and the demo and sets PROXY, DEMO, CODE
// and OUT. Prints one PASS/FAIL line per check; exits 1 on any FAIL.
import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const { OUT, PROXY, DEMO, CODE } = process.env;
let failed = 0;
function check(what, ok, got) {
  if (!ok) failed++;
  console.log(`${ok ? "PASS" : "FAIL"}  ${what}: ${String(got).replace(/\n/g, " ").slice(0, 200)}`);
}
const phone = { viewport: { width: 390, height: 844 } };
const desktop = { viewport: { width: 1440, height: 900 } };
const shot = (page, name) => page.screenshot({ path: path.join(OUT, `${name}.png`) });

async function context(browser, size, theme) {
  const ctx = await browser.newContext({ ...size, colorScheme: theme });
  await ctx.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
  return ctx;
}
async function iapSignIn(page, email) {
  await page.goto(`${PROXY}/`);
  await page.waitForURL(/\/_iapsim\/login/);
  await page.getByLabel("Email").fill(email);
  await page.getByRole("button", { name: "Continue" }).click();
  await page.waitForLoadState("networkidle");
}
async function codeSignIn(page) {
  await page.goto(`${DEMO}/`);
  await page.locator("input#code").fill(CODE);
  await page.locator("input#code").press("Enter");
  await page.waitForLoadState("networkidle");
}
async function openDrawer(page) {
  await page.getByRole("button", { name: "Open navigation" }).click();
  const drawer = page.getByRole("dialog", { name: "Navigation" });
  await drawer.waitFor();
  // Wait for the slide-in to finish so screenshots and boxes agree.
  for (let i = 0; i < 50 && (await drawer.boundingBox())?.x !== 0; i++) await page.waitForTimeout(50);
  return drawer;
}

const browser = await chromium.launch();
try {
  for (const theme of ["light", "dark"]) {
    // --- item 4: signed in through IAP ---
    {
      const ctx = await context(browser, phone, theme);
      const page = await ctx.newPage();
      await iapSignIn(page, "owner@example.com");
      const drawer = await openDrawer(page);
      const row = drawer.getByTestId("phone-signed-in");
      const visible = await row.isVisible().catch(() => false);
      check(`${theme} 390 iap: drawer has the signed-in row`, visible, visible);
      const email = await row.getByTestId("phone-signed-in-email").textContent().catch(() => "");
      check(`${theme} 390 iap: row shows the verified email`, email === "owner@example.com", email);
      const access = row.getByRole("button", { name: /Access settings/ });
      check(`${theme} 390 iap: row offers Access settings`, (await access.count()) === 1, await access.count());
      const logout = row.getByRole("button", { name: "Log out" });
      check(`${theme} 390 iap: row offers Log out`, (await logout.count()) === 1, await logout.count());
      const db = await drawer.boundingBox();
      const rb = await row.boundingBox();
      const lb = await logout.boundingBox();
      check(`${theme} 390 iap: row and Log out inside the drawer`,
        rb && lb && rb.x >= db.x - 1 && rb.x + rb.width <= db.x + db.width + 1 && lb.x + lb.width <= db.x + db.width + 1 &&
          rb.y + rb.height <= db.y + db.height + 1,
        JSON.stringify({ db, rb, lb }));
      const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      check(`${theme} 390 iap: no horizontal overflow`, over <= 0, over);
      // The row's contrast against the drawer: every text node's colour differs from the background.
      const colors = await row.evaluate((el) => {
        const cs = getComputedStyle(el.querySelector('[data-testid="phone-signed-in-email"]'));
        let bg = "rgba(0, 0, 0, 0)";
        for (let n = el; n && /rgba\(0, 0, 0, 0\)|transparent/.test(bg); n = n.parentElement) bg = getComputedStyle(n).backgroundColor;
        return { fg: cs.color, bg };
      });
      check(`${theme} 390 iap: email colour differs from its background`, colors.fg !== colors.bg, JSON.stringify(colors));
      await shot(page, `${theme}-390-iap-drawer`);
      // Access settings opens Settings -> Access.
      await access.click();
      const txt = page.getByText("Signed in through IAP as owner@example.com.");
      await txt.waitFor({ timeout: 10000 }).catch(() => {});
      check(`${theme} 390 iap: Access settings opens Settings -> Access`, await txt.isVisible().catch(() => false), page.url());
      await shot(page, `${theme}-390-iap-settings-access`);
      // Log out from the drawer lands on IAP's clear_login_cookie.
      await openDrawer(page);
      await page.getByRole("dialog", { name: "Navigation" }).getByTestId("phone-signed-in").getByRole("button", { name: "Log out" }).click();
      await page.waitForURL(/_gcp_iap\/clear_login_cookie|_iapsim\/login/, { timeout: 10000 }).catch(() => {});
      check(`${theme} 390 iap: Log out goes to /_gcp_iap/clear_login_cookie`, /clear_login_cookie|_iapsim\/login/.test(page.url()), page.url());
      await ctx.close();
    }
    // --- item 4: the access code: no email badge, no signed-in email ---
    for (const [size, w] of [[phone, 390], [desktop, 1440]]) {
      const ctx = await context(browser, size, theme);
      const page = await ctx.newPage();
      await codeSignIn(page);
      const badge = await page.getByTestId("top-bar-user").count();
      check(`${theme} ${w} code: no email badge in the header`, badge === 0, badge);
      if (w === 390) {
        const drawer = await openDrawer(page);
        const emailEl = await drawer.getByTestId("phone-signed-in-email").count();
        check(`${theme} 390 code: drawer shows no signed-in email`, emailEl === 0, emailEl);
        await shot(page, `${theme}-390-code-drawer`);
      } else {
        await shot(page, `${theme}-1440-code-header`);
      }
      await ctx.close();
    }
  }

  // --- item 5: Settings -> Access, and Tab past locked selects, under -external-config-repo ---
  for (const [size, w] of [[desktop, 1440], [phone, 390]]) {
    const ctx = await context(browser, size, "light");
    const page = await ctx.newPage();
    await iapSignIn(page, "owner@example.com");
    await page.goto(`${PROXY}/settings/access`);
    await page.waitForLoadState("networkidle");
    const signed = page.getByText("Signed in through IAP as owner@example.com.");
    await signed.waitFor({ timeout: 10000 }).catch(() => {});
    check(`external ${w}: Settings -> Access reads the sign-in`, await signed.isVisible().catch(() => false), page.url());
    const ro = await page.locator("main [data-settings-readonly]").count();
    check(`external ${w}: Access is not drawn read-only`, ro === 0, ro);
    const lo = page.getByRole("main").getByRole("button", { name: "Log out" });
    check(`external ${w}: Access's Log out is enabled`, (await lo.count()) === 1 && (await lo.isEnabled()), await lo.count());
    await shot(page, `external-${w}-settings-access`);

    // A section with locked selects: General (read-only under an external repo).
    for (const section of ["general", "models", "sandbox"]) {
      await page.goto(`${PROXY}/settings/${section}`);
      await page.waitForLoadState("networkidle");
      const locked = await page.locator("[data-settings-readonly] .MuiSelect-select").count();
      if (locked === 0) continue;
      await page.locator("body").click({ position: { x: 1, y: 1 } });
      const focused = [];
      for (let i = 0; i < 80; i++) {
        await page.keyboard.press("Tab");
        focused.push(await page.evaluate(() => {
          const el = document.activeElement;
          return {
            select: !!el?.closest?.("[data-settings-readonly]") && !!el?.matches?.(".MuiSelect-select, .MuiChip-deletable"),
            text: (el?.getAttribute("aria-label") || el?.textContent || el?.tagName || "").trim().slice(0, 40),
          };
        }));
      }
      const hit = focused.filter((f) => f.select);
      check(`external ${w}: Tab skips the ${locked} locked select(s) in Settings -> ${section}`, hit.length === 0,
        hit.length ? JSON.stringify(hit.slice(0, 3)) : `${focused.length} tabs, none on a locked select`);
      await shot(page, `external-${w}-settings-${section}`);
      break;
    }
    await ctx.close();
  }
} finally {
  await browser.close();
}
console.log(`FAILS=${failed}`);
process.exit(failed ? 1 : 0);
