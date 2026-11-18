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

// shot.mjs renders /next/settings/upgrade (or PAGE) on the walk's daemon
// at 1440x900 and 390x844 and, with CLICK set to a data-testid, presses
// that button at 1440 and prints every /api/upgrade request it sent and
// the banner text before and after.
//
//   DAEMON=http://127.0.0.1:4280 TOKEN=$CODE OUT=/tmp/shots/pg-scheduled \
//     [CLICK=cancel-upgrade-btn] [PAGE=/next/settings/upgrade] node shot.mjs
//
// TOKEN is a bearer the daemon takes (the data dir's local-api-token).
// For the viewer pass set EMAIL=viewer@example.com and point DAEMON at
// tests/iap/iapsim.go's proxy, which signs that person in.
import { chromium } from "playwright";

const daemon = process.env.DAEMON ?? "http://127.0.0.1:4280";
const page_ = process.env.PAGE ?? "/next/settings/upgrade";
const out = process.env.OUT ?? "/tmp/shots/upgrade";
const click = process.env.CLICK ?? "";
const scheme = process.env.SCHEME ?? "light";

const browser = await chromium.launch();
console.log(`chromium ${browser.version()}`);
for (const [w, h] of [
  [1440, 900],
  [390, 844],
]) {
  const ctx = await browser.newContext({
    viewport: { width: w, height: h },
    colorScheme: scheme,
    // EMAIL signs in through tests/iap/iapsim.go's proxy instead.
    extraHTTPHeaders: process.env.EMAIL
      ? { "X-Iapsim-Email": process.env.EMAIL }
      : { Authorization: `Bearer ${process.env.TOKEN}` },
  });
  const page = await ctx.newPage();
  const sent = [];
  page.on("request", (r) => {
    if (r.url().includes("/api/upgrade") && r.method() !== "GET") {
      sent.push(`${r.method()} ${new URL(r.url()).pathname}${new URL(r.url()).search}`);
    }
  });
  page.on("response", async (r) => {
    if (r.url().includes("/api/upgrade") && r.request().method() !== "GET") {
      console.log(`  ${w}px response ${r.status()} ${r.request().method()} ${new URL(r.url()).pathname}`);
    }
  });
  // "load", not "networkidle": with the store's SELECT revoked (item 4)
  // the page's failing polls never let the network go idle.
  await page.goto(daemon + page_, { waitUntil: "load" });
  await page.waitForTimeout(4000);
  const banners = async () =>
    (await page.locator('[data-testid$="upgrade-banner"], .s-banner').allInnerTexts()).map((t) => t.replace(/\s+/g, " ").trim());
  console.log(`${w}px banners: ${JSON.stringify(await banners())}`);
  for (const id of ["cancel-upgrade-btn", "cancel-scheduled-upgrade-btn"]) {
    const b = page.getByTestId(id);
    if (await b.count()) {
      const box = await b.boundingBox();
      console.log(
        `${w}px ${id}: visible=${await b.isVisible()} enabled=${await b.isEnabled()} box=${JSON.stringify(box)} inViewportX=${box ? box.x >= 0 && box.x + box.width <= w : "n/a"}`,
      );
    }
  }
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  console.log(`${w}px horizontal overflow: ${overflow}px`);
  await page.screenshot({ path: `${out}-${w}.png`, fullPage: false });
  if (click && w === 1440) {
    const b = page.getByTestId(click);
    await b.scrollIntoViewIfNeeded();
    await b.click();
    await page.waitForTimeout(2500);
    console.log(`${w}px after click banners: ${JSON.stringify(await banners())}`);
    const toasts = await page.locator('[role="status"], [role="alert"], .toast, g-toast').allInnerTexts();
    console.log(`${w}px after click toasts: ${JSON.stringify(toasts.map((t) => t.trim()).filter(Boolean))}`);
    await page.screenshot({ path: `${out}-${w}-after-click.png`, fullPage: false });
  }
  console.log(`${w}px sent: ${JSON.stringify(sent)}`);
  await ctx.close();
}
await browser.close();
