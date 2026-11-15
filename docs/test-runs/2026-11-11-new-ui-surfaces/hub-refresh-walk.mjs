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

// §13 item 3: the Grain Hub entry page's auto-refresh, in a real browser
// against the real grain-hub hub-naming.sh (KEEP=1) leaves running, through
// an iapstub front that asserts alice (HUB, default :4235). Run from ui-hub/
// (it borrows ui-hub's @playwright/test):
//   HUB=http://127.0.0.1:4235 PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-11-new-ui-surfaces/hub-refresh-walk.mjs
// ENTRY (default name-walk) is a connected entry whose daemon is up.
import { chromium } from "@playwright/test";

const HUB = process.env.HUB || "http://127.0.0.1:4235";
const ENTRY = process.env.ENTRY || "name-walk";
const API = `/api/entries/${ENTRY}`;

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const polls = [];
let failNext = 0;
let fixture = null; // a JSON patch laid over the entry's real answer
page.on("pageerror", (e) => console.log(`  pageerror: ${e.message}`));
await page.route(`**${API}`, async (route) => {
  if (route.request().method() !== "GET") return route.continue();
  polls.push(Date.now());
  if (failNext > 0) {
    failNext -= 1;
    return route.fulfill({
      status: 503,
      contentType: "application/json",
      body: '{"error":"hub unavailable"}',
    });
  }
  if (!fixture) return route.continue();
  const res = await route.fetch();
  const body = await res.json();
  return route.fulfill({ response: res, json: { ...body, ...fixture } });
});
const gaps = () =>
  polls.slice(1).map((t, i) => Math.round((t - polls[i]) / 100) / 10);
const bar = () => page.getByTestId("entry-refresh-status").innerText();
const hubPatch = async (body) => {
  const res = await fetch(`${HUB}${API}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  return res.status;
};
const setHidden = (hidden) =>
  page.evaluate((h) => {
    Object.defineProperty(document, "hidden", {
      configurable: true,
      get: () => h,
    });
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => (h ? "hidden" : "visible"),
    });
    document.dispatchEvent(new Event("visibilitychange"));
  }, hidden);

await hubPatch({ display_name: "Name Walk" });
await page.goto(`${HUB}/entries/${ENTRY}`);
await page.getByTestId("entry-refresh-status").waitFor();
const real = await (await fetch(`${HUB}${API}`)).json();
console.log(
  `=== real entry: state=${real.state} status=${real.status} health=${real.health}`,
);
console.log(`  bar: ${await bar()}`);

console.log("=== settled poll rate (45s)");
polls.length = 0;
await page.waitForTimeout(45000);
console.log(`  polls ${polls.length}, gaps ${JSON.stringify(gaps())}s`);

console.log("=== transitional (fixture: state=replacing over the real answer)");
fixture = { state: "replacing" };
await page.getByRole("button", { name: "Refresh now" }).click();
polls.length = 0;
await page.waitForTimeout(13000);
console.log(`  bar: ${await bar()}`);
console.log(`  polls ${polls.length}, gaps ${JSON.stringify(gaps())}s`);
fixture = null;
await page.waitForTimeout(4500);
console.log(`  settled again: ${await bar()}`);

console.log(
  "=== hidden-tab pause (document.hidden overridden + visibilitychange)",
);
polls.length = 0;
await setHidden(true);
await page.waitForTimeout(45000);
const whileHidden = polls.length;
await setHidden(false);
await page.waitForTimeout(1500);
console.log(
  `  polls while hidden for 45s: ${whileHidden}; within 1.5s of showing: ${polls.length - whileHidden}`,
);

console.log("=== edit preservation");
const input = page.getByLabel("Display name");
await input.fill("My local edit");
console.log(
  `  PATCH display_name on the hub -> ${await hubPatch({ display_name: "Renamed on the server", purpose: "Purpose set elsewhere" })}`,
);
await page.getByRole("button", { name: "Refresh now" }).click();
await page.waitForTimeout(1500);
const hint = page.getByTestId("server-changed-displayName");
console.log(`  input keeps: "${await input.inputValue()}"`);
console.log(
  `  hint: ${(await hint.count()) ? await hint.innerText() : "(none)"}`,
);
console.log(
  `  untouched Purpose took the server's: "${await page.getByLabel("Purpose").inputValue()}"`,
);
const discard = page.getByRole("button", { name: "Discard my edits" });
console.log(`  "Discard my edits" shown: ${(await discard.count()) > 0}`);
if (await discard.count()) {
  await discard.click();
  console.log(
    `  after Discard: "${await input.inputValue()}", hint ${await hint.count()}`,
  );
}
await input.fill("Renamed on the server2");
await hubPatch({ display_name: "Renamed on the server2" });
await page.getByRole("button", { name: "Refresh now" }).click();
await page.waitForTimeout(1500);
console.log(
  `  edit equal to the server's new value: hint ${await hint.count()}, Discard ${await discard.count()}`,
);

console.log("=== failed poll marks the bar only");
failNext = 1;
await page.getByRole("button", { name: "Refresh now" }).click();
await page.waitForTimeout(1500);
const failed = page.getByTestId("entry-refresh-failed");
console.log(`  bar: ${await bar()}`);
console.log(
  `  failed marker: ${await failed.count()}, alert banners: ${await page.getByRole("alert").count()}, entry heading still shown: ${await page.getByText(ENTRY).first().isVisible()}`,
);
await page.getByRole("button", { name: "Refresh now" }).click();
await page.waitForTimeout(1500);
console.log(`  next good poll clears it: marker ${await failed.count()}`);

for (const [w, h, scheme] of [
  [1440, 900, "light"],
  [1440, 900, "dark"],
  [390, 844, "light"],
  [390, 844, "dark"],
]) {
  const ctx = await browser.newContext({
    viewport: { width: w, height: h },
    colorScheme: scheme,
    isMobile: w < 500,
    hasTouch: w < 500,
  });
  const p = await ctx.newPage();
  await p.goto(`${HUB}/entries/${ENTRY}`);
  await p.getByTestId("entry-refresh-status").waitFor();
  const audit = await p.evaluate(() => {
    const doc = document.documentElement;
    const barEl = document.querySelector("[data-testid=entry-refresh-status]");
    const r = barEl.getBoundingClientRect();
    return {
      scrollW: doc.scrollWidth,
      clientW: doc.clientWidth,
      bar: [Math.round(r.left), Math.round(r.right)],
    };
  });
  console.log(`  layout ${w}x${h} ${scheme}: ${JSON.stringify(audit)}`);
  if (process.env.SHOTS)
    await p.screenshot({
      path: `${process.env.SHOTS}/hub-entry-${w < 500 ? "phone" : "desktop"}-${scheme}.png`,
      fullPage: true,
    });
  await ctx.close();
}
await browser.close();
