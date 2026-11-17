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


// docs/test-plans/2026-11-16.md §7 item 5 and the Agents inspector:
//   PART=processes NOIAP=1 DAEMON=http://127.0.0.1:4290 WORK_DIR=/var/tmp/walk-rt node item5-processes-agents.mjs
//     (the publishing-held harness: acme/grain carries this checkout's own .grain/)
//   PART=agents node item5-processes-agents.mjs   (the §7 daemon, with a long run going)
import fs from "node:fs";
import path from "node:path";
import { chromium, api, as, note, info, flush, shot, seeded, DESKTOP, PHONE } from "./lib.mjs";

const ROOT = process.env.ROOT || "/home/debian/work";
const browser = await chromium.launch();
const part = process.env.PART || "processes";
const titles = (page) =>
  page.locator(".proc-list .proc-row").evaluateAll((els) =>
    els.map((e) => e.innerText.split("\n").map((l) => l.trim()).filter(Boolean)[1] ?? ""),
  );

if (part === "processes") {
  // Declared triggers listed match the repository's .grain/triggers/*.
  const declared = fs.readdirSync(path.join(ROOT, ".grain/triggers")).filter((f) => f.endsWith(".yml")).map((f) => f.replace(/\.yml$/, "")).sort();
  const a = await as(browser, "alice@example.com", DESKTOP);
  const b = await as(browser, "bob@example.com", DESKTOP);
  await a.page.goto("processes/schedules");
  await a.page.evaluate(() => localStorage.removeItem("grain.list-order.processes"));
  await a.page.reload();
  const trig = a.page.locator('[data-testid^="trigger-row-"]');
  await trig.first().waitFor({ timeout: 20000 });
  await a.page.waitForTimeout(1500);
  const listed = (await trig.evaluateAll((els) => els.map((e) => e.getAttribute("data-testid").replace(/^trigger-row-/, "")))).sort();
  info("5 declared on disk", JSON.stringify(declared));
  info("5 trigger rows", JSON.stringify(listed));
  note("5 declared triggers listed match .grain/triggers/*", declared.every((d) => listed.some((l) => l.includes(d))) && listed.length === declared.length, "");
  const before = await titles(a.page);
  info("5 order before", JSON.stringify(before));
  const first = a.page.locator(".proc-list .proc-row").first();
  await first.focus();
  await a.page.keyboard.press("Alt+ArrowDown");
  const moveNote = await a.page.getByTestId("proc-move-note").innerText().catch(() => "");
  info("5 move note", JSON.stringify(moveNote));
  await a.page.waitForTimeout(500);
  const moved = await titles(a.page);
  note("5 Alt+ArrowDown moves the first row down", moved[0] === before[1] && moved[1] === before[0], JSON.stringify(moved.slice(0, 3)));
  await a.page.reload();
  await trig.first().waitFor({ timeout: 20000 });
  await a.page.waitForTimeout(1500);
  const reloaded = await titles(a.page);
  note("5 custom order persists across reload", JSON.stringify(reloaded) === JSON.stringify(moved), JSON.stringify(reloaded.slice(0, 3)));
  const stored = await a.page.evaluate(() => localStorage.getItem("grain.list-order.processes"));
  info("5 where the order lives", `localStorage grain.list-order.processes = ${String(stored).slice(0, 160)}`);
  await shot(a.page, "5-desktop-processes-custom-order");
  // A second browser (bob, a fresh context) sees the default order.
  await b.page.goto("processes/schedules");
  await b.page.locator('[data-testid^="trigger-row-"]').first().waitFor({ timeout: 20000 });
  await b.page.waitForTimeout(1500);
  const other = await titles(b.page);
  info("5 second browser order", JSON.stringify(other.slice(0, 3)));
  note("5 second browser: order is per browser (localStorage), as on classic", JSON.stringify(other) === JSON.stringify(before), JSON.stringify(other) === JSON.stringify(moved) ? "carried over" : "default order");
  // Sort, ?q= and the New menu.
  await a.page.goto("processes/schedules?sort=name");
  await a.page.waitForTimeout(1500);
  const sorted = await titles(a.page);
  const isSorted = sorted.every((t, i) => i === 0 || sorted[i - 1].localeCompare(t, undefined, { sensitivity: "base" }) <= 0);
  note("5 ?sort=name sorts by name", isSorted, JSON.stringify(sorted.slice(0, 4)));
  await a.page.goto("processes?q=release");
  await a.page.waitForTimeout(1500);
  // ?q= also matches a schedule's template and suite names, which the row
  // does not print (ui-walkthrough.yml runs suite release-ui-walkthrough).
  const q = await a.page.locator(".proc-list .proc-row").allInnerTexts();
  const all = await (async () => { await a.page.goto("processes"); await a.page.waitForTimeout(1500); return a.page.locator(".proc-list .proc-row").count(); })();
  note("5 ?q=release narrows", q.length > 0 && q.length < all, `${q.length} of ${all} rows; ${q.filter((t) => !/release/i.test(t)).length} match only on a template or suite name`);
  await a.page.goto("processes");
  await a.page.getByRole("button", { name: "New ▾" }).click();
  await a.page.waitForTimeout(400);
  const menuEl = a.page.locator('[role="menu"], .pill-menu, .menu-pop, [data-testid$="new-menu"]').first();
  const menu = (await menuEl.innerText().catch(() => "")).split("\n").map((t) => t.trim()).filter(Boolean);
  info("5 New menu", JSON.stringify(menu));
  note("5 New menu offers schedule/template/suite", menu.length >= 2, "");
  await shot(a.page, "5-desktop-processes-new-menu");
  // Phone width.
  const p = await as(browser, "alice@example.com", PHONE);
  await p.page.goto("processes/schedules");
  await p.page.locator('[data-testid^="trigger-row-"]').first().waitFor({ timeout: 20000 }).catch(() => {});
  const overflow = await p.page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  note("5 phone: no horizontal overflow", overflow <= 0, `scrollWidth - innerWidth = ${overflow}`);
  await shot(p.page, "5-phone-processes");
  for (const [n, c] of [["a", a], ["b", b], ["phone", p]]) note(`5 ${n} no page errors`, c.page.errors.length === 0, JSON.stringify(c.page.errors));
}

if (part === "agents") {
  const id = seeded.LONG;
  const { page } = await as(browser, "alice@example.com", DESKTOP);
  await page.goto("agents");
  const card = page.getByTestId(`agent-${id}`);
  await card.waitFor({ timeout: 20000 });
  const pane = page.getByTestId("agents-pane");
  info("5k start", `pane open: ${await pane.count()}`);
  // Keyboard only from here: Tab until the card has focus.
  let reached = false;
  for (let i = 0; i < 60; i++) {
    await page.keyboard.press("Tab");
    if (await card.evaluate((el) => el === document.activeElement || el.contains(document.activeElement))) {
      reached = true;
      break;
    }
  }
  const focusedWhat = await page.evaluate(() => `${document.activeElement?.tagName} ${document.activeElement?.getAttribute("data-testid") ?? ""} ${document.activeElement?.getAttribute("aria-label") ?? ""}`);
  note("5k Tab reaches the agent card", reached, focusedWhat);
  await page.keyboard.press("Enter");
  const opened = await pane.waitFor({ timeout: 5000 }).then(() => true).catch(() => false);
  note("5k Enter opens the inspector", opened, page.url().replace(/^.*\/next/, "/next"));
  await shot(page, "5k-desktop-agents-inspector-open");
  await page.keyboard.press("Escape");
  await page.waitForTimeout(500);
  const closed = (await pane.count()) === 0;
  const back = await card.evaluate((el) => el === document.activeElement || el.contains(document.activeElement));
  note("5k Escape closes it to the full-width cards", closed, page.url().replace(/^.*\/next/, "/next"));
  note("5k focus returns to the card", back, await page.evaluate(() => `${document.activeElement?.tagName} ${document.activeElement?.getAttribute("data-testid") ?? ""}`));
  await page.keyboard.press("Enter");
  await pane.waitFor({ timeout: 5000 }).catch(() => {});
  // Tab forward to the bar's Close inspector button and press it.
  let closeReached = false;
  for (let i = 0; i < 80; i++) {
    await page.keyboard.press("Tab");
    const tid = await page.evaluate(() => document.activeElement?.getAttribute("data-testid"));
    if (tid === "agents-pane-close" || tid === "pane-close") {
      closeReached = tid;
      break;
    }
  }
  note("5k Tab reaches a Close inspector button", !!closeReached, String(closeReached));
  if (closeReached) {
    await page.keyboard.press("Enter");
    await page.waitForTimeout(500);
    note("5k Enter on Close closes the inspector", (await pane.count()) === 0, "");
    const after = await page.evaluate(() => `${document.activeElement?.tagName} ${document.activeElement?.getAttribute("data-testid") ?? ""}`);
    note("5k focus lands on the card after Close", after.includes(`agent-${id}`), after);
  }
  await shot(page, "5k-desktop-agents-cards");
  note("5k no page errors", page.errors.length === 0, JSON.stringify(page.errors));
}

flush(`item5-${part}`);
await browser.close();
