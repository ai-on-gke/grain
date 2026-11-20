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

// Walks docs/test-plans/2026-11-15.md §1 items 2-4 on /next/ against a
// running server (a `grain demo` or a real daemon) holding a rollover
// chain A -> B -> C. Prints one JSON line per check.
//
//   URL=http://127.0.0.1:4290 CODE=<access code> A=21 B=22 C=23 \
//     node walk-next.mjs   (run from ui/, which has playwright installed)

import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const { URL: BASE, CODE, A, B, C } = process.env;
const results = [];
function check(name, ok, detail = "") {
  results.push({ name, ok: !!ok, detail });
  console.log(JSON.stringify({ name, ok: !!ok, detail }));
}

async function signIn(context) {
  const res = await context.request.post(`${BASE}/api/auth/access-code`, {
    data: { code: CODE, name: "alice@example.com" },
  });
  if (!res.ok()) throw new Error(`login: ${res.status()}`);
}

async function noSideScroll(page) {
  return page.evaluate(
    () => document.documentElement.scrollWidth <= window.innerWidth,
  );
}

async function run(width) {
  const browser = await chromium.launch();
  const context = await browser.newContext({
    viewport: { width, height: 900 },
  });
  await signIn(context);
  const page = await context.newPage();
  const w = `@${width}`;

  // Item 3: the row pill under tabs, views, search, grouping, selection.
  await page.goto(`${BASE}/next/tasks?tab=closed`);
  await page.getByTestId(`row-${A}`).waitFor();
  check(`closed tab pill on A ${w}`, await page.getByTestId(`rolled-over-${A}`).isVisible(),
    await page.getByTestId(`rolled-over-${A}`).textContent());
  check(`closed tab pill on B ${w}`, await page.getByTestId(`rolled-over-${B}`).isVisible(),
    await page.getByTestId(`rolled-over-${B}`).textContent());
  await page.goto(`${BASE}/next/tasks?tab=closed&q=nightly`);
  await page.getByTestId(`row-${A}`).waitFor();
  check(`?q= search keeps pill ${w}`, await page.getByTestId(`rolled-over-${A}`).isVisible());
  await page.goto(`${BASE}/next/tasks?tab=closed&group=workstream`);
  await page.getByTestId(`row-${A}`).waitFor();
  check(`Workstreams grouping keeps pill ${w}`, await page.getByTestId(`rolled-over-${A}`).isVisible());
  await page.goto(`${BASE}/next/tasks?tab=closed&view=timeline`);
  await page.getByTestId(`row-${A}`).first().waitFor();
  check(`Timeline view shows pill ${w}`,
    (await page.getByTestId(`rolled-over-${A}`).count()) > 0);
  await page.goto(`${BASE}/next/tasks?tab=closed`);
  await page.getByTestId(`row-${A}`).waitFor();
  const box = page.getByTestId(`row-${A}`).locator('input[type="checkbox"]').first();
  if (await box.count()) {
    await box.check();
    check(`selection keeps pill ${w}`, await page.getByTestId(`rolled-over-${A}`).isVisible());
    await box.uncheck();
  } else {
    check(`selection checkbox present ${w}`, false, "no row checkbox");
  }
  check(`tasks list no side scroll ${w}`, await noSideScroll(page));

  // Saved boards: save the closed-tab search as a board, open the plain
  // list, apply the board, and the pill is back.
  await page.goto(`${BASE}/next/tasks?tab=closed&q=nightly`);
  await page.getByTestId(`row-${A}`).waitFor();
  const boardName = `Rolled over ${width}`;
  await page.getByTestId("saved-boards-btn").click();
  await page.getByTestId("board-save-open").click();
  await page.getByTestId("board-name-input").fill(boardName);
  await page.getByTestId("board-save-submit").click();
  await page.goto(`${BASE}/next/tasks`);
  await page.getByTestId("saved-boards-btn").click();
  await page.getByTestId("saved-boards-menu").getByText(boardName).first().click();
  await page.getByTestId(`row-${A}`).waitFor();
  check(`saved board keeps pill ${w}`, await page.getByTestId(`rolled-over-${A}`).isVisible(), page.url());

  // Item 2: banners, both directions, with and without the pane.
  await page.goto(`${BASE}/next/task/${B}`);
  const to = page.getByTestId("task-rolled-over-to");
  const from = page.getByTestId("task-rolled-over-from");
  await to.waitFor();
  check(`B shows both banners ${w}`, (await to.isVisible()) && (await from.isVisible()),
    `${await to.innerText()} | ${await from.innerText()}`);
  check(`B names C, A ${w}`,
    (await to.innerText()).includes(`#${C}`) && (await from.innerText()).includes(`#${A}`));
  check(`B no side scroll ${w}`, await noSideScroll(page));
  for (const b of [to, from]) {
    const bb = await b.boundingBox();
    check(`banner fits ${await b.getAttribute("data-testid")} ${w}`,
      bb.x >= 0 && bb.x + bb.width <= width, JSON.stringify(bb));
  }
  // Keyboard: the successor link is reachable with Tab.
  const link = to.getByRole("link", { name: `#${C}` });
  await link.focus();
  check(`banner link focusable ${w}`, await link.evaluate((el) => el === document.activeElement));
  await page.keyboard.press("Enter");
  await page.waitForURL(new RegExp(`/next/task/${C}`));
  check(`Enter follows to C ${w}`, true, page.url());
  await page.goBack();
  await page.waitForURL(new RegExp(`/next/task/${B}`));
  await to.waitFor();
  check(`back returns to B ${w}`, true, page.url());
  await page.goForward();
  await page.waitForURL(new RegExp(`/next/task/${C}`));
  await from.waitFor();
  check(`forward returns to C, banner shown ${w}`, (await from.innerText()).includes(`#${B}`));
  check(`C has no rolled-over-to ${w}`, (await to.count()) === 0);

  // Pane: /next/tasks?task=A keeps the pane on the successor link. A
  // phone opens rows full-page instead of in a pane, so desktop only.
  if (width >= 1024) {
  await page.goto(`${BASE}/next/tasks?tab=closed&task=${A}&pane=runs`);
  const pto = page.getByTestId("task-rolled-over-to");
  await pto.waitFor();
  await pto.getByRole("link", { name: `#${B}` }).click();
  await page.waitForURL(new RegExp(`task=${B}`));
  const u = new globalThis.URL(page.url());
  check(`pane link keeps tab and pane ${w}`,
    u.pathname.endsWith("/next/tasks") && u.searchParams.get("tab") === "closed" &&
      u.searchParams.get("pane") === "runs", page.url());
  await page.getByTestId("task-rolled-over-from").waitFor();
  await page.getByTestId("task-rolled-over-from").getByRole("link", { name: `#${A}` }).click();
  await page.waitForURL(new RegExp(`task=${A}`));
  check(`pane back-link returns to A ${w}`, true, page.url());
  await page.goBack();
  await page.waitForURL(new RegExp(`task=${B}`));
  check(`pane history back ${w}`, true, page.url());
  }

  // #comment= deep link on the rolled-over task.
  const detail = await (await context.request.get(`${BASE}/api/tasks/${A}`)).json();
  const comments = detail.comments || [];
  if (comments.length) {
    const cid = comments[comments.length - 1].id;
    await page.goto(`${BASE}/next/task/${A}#comment=${cid}`);
    await page.getByTestId("task-rolled-over-to").waitFor();
    const target = page.locator(`[id="comment-${cid}"], [data-comment-id="${cid}"]`).first();
    await page.waitForTimeout(1500);
    const vis = (await target.count()) ? await target.isVisible() : false;
    const inView = vis ? await target.evaluate((el) => {
      const r = el.getBoundingClientRect();
      return r.top < window.innerHeight && r.bottom > 0;
    }) : false;
    check(`#comment=${cid} deep link scrolls to comment ${w}`, inView,
      `count=${await target.count()} visible=${vis}`);
    check(`#comment deep link keeps banner ${w}`,
      await page.getByTestId("task-rolled-over-to").count() === 1);
  } else {
    check(`A has comments for deep link ${w}`, false, "none");
  }

  // Inbox agreement: the closed predecessor is not waiting on anyone.
  await page.goto(`${BASE}/next/inbox`);
  await page.waitForTimeout(1500);
  const inboxText = await page.locator("main").innerText();
  check(`inbox does not list A ${w}`, !new RegExp(`#${A}\\b`).test(inboxText));

  await browser.close();
}

for (const width of [1440, 390]) await run(width);
const failed = results.filter((r) => !r.ok);
console.log(`${results.length - failed.length}/${results.length} checks passed`);
process.exit(failed.length ? 1 : 0);
