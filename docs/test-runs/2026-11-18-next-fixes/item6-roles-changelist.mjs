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

// Item 6: a real grain/viewer (bob, signed in through grain_headerverifier's
// X-Walk-User) on /next/task/<id> and /next/inbox, and a repository-less
// task (CL) in the Inbox and on its Changes tab, against a real daemon.
import { chromium, watch } from "./lib.mjs";

const base = process.env.BASE, CL = process.env.CL || "12", REPO_TASK = process.env.REPO_TASK || "2";
const browser = await chromium.launch();
const out = [];
const say = (s) => { out.push(s); console.log(s); };

async function as(who) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, extraHTTPHeaders: { "X-Walk-User": who } });
  const page = await ctx.newPage();
  const log = watch(page);
  const previews = [];
  const writes = [];
  page.on("request", (r) => {
    if (r.url().includes("/diff-preview")) previews.push(new URL(r.url()).pathname);
    if (r.method() !== "GET" && r.method() !== "HEAD") writes.push(`${r.method()} ${new URL(r.url()).pathname}`);
  });
  return { ctx, page, log, previews, writes };
}

// The viewer.
{
  const { page, log, writes } = await as("bob@example.com");
  for (const id of [REPO_TASK, CL]) {
    await page.goto(`${base}/next/task/${id}`);
    await page.getByRole("heading", { name: new RegExp(`#${id}`) }).first().waitFor({ timeout: 15000 });
    await page.waitForTimeout(1000);
    const composer = page.getByPlaceholder(/Comment on this task/);
    const visible = await composer.isVisible().catch(() => false);
    const disabled = visible ? await composer.isDisabled() : null;
    const blocked = await page.getByTestId("reply-blocked").allInnerTexts();
    const send = page.getByTestId("act-send");
    say(`viewer task/${id}: composer visible=${visible} disabled=${disabled} send=${(await send.count()) ? (await send.isDisabled() ? "disabled" : "ENABLED") : "absent"} blocked=${JSON.stringify(blocked)}`);
    const buttons = (await page.getByRole("button").allInnerTexts()).map((s) => s.trim()).filter(Boolean);
    say(`viewer task/${id}: buttons=${JSON.stringify(buttons.slice(0, 40))}`);
    await page.screenshot({ path: `/tmp/item6-viewer-task-${id}.png`, fullPage: true });
  }
  await page.goto(`${base}/next/inbox`);
  await page.waitForTimeout(2500);
  const groupSelects = await page.locator("[data-testid^=group-select-]").count();
  const bulk = await page.locator("[data-testid^=bulk-]").count();
  say(`viewer inbox: group-select controls=${groupSelects} bulk buttons=${bulk}`);
  const rows = page.locator("li").filter({ hasText: /#1[23]\b/ });
  say(`viewer inbox: rows for #12/#13=${await rows.count()}`);
  if (await rows.count()) {
    await rows.first().click();
    await page.waitForTimeout(1200);
    const actions = (await page.locator(".action-btn-group button").allInnerTexts()).map((s) => s.trim());
    const enabled = [];
    for (const b of await page.locator(".action-btn-group button").all()) if (!(await b.isDisabled())) enabled.push((await b.innerText()).trim());
    say(`viewer inbox selected: actions=${JSON.stringify(actions)} enabled=${JSON.stringify(enabled)} blocked=${JSON.stringify(await page.getByTestId("reply-blocked").allInnerTexts())}`);
  }
  await page.screenshot({ path: "/tmp/item6-viewer-inbox.png", fullPage: true });
  say(`viewer writes sent=${JSON.stringify(writes)} errors=${JSON.stringify(log)}`);
}

// The owner, on the changelist task.
{
  const { page, log, previews } = await as("alice@example.com");
  await page.goto(`${base}/next/inbox`);
  await page.waitForTimeout(2000);
  const row = page.locator("li").filter({ hasText: new RegExp(`#${CL}\\b`) }).first();
  say(`owner inbox: CL row present=${(await row.count()) > 0}`);
  if (await row.count()) { await row.click(); await page.waitForTimeout(1500); }
  await page.goto(`${base}/next/task/${CL}`);
  await page.getByTestId("task-tab-changes").click();
  await page.waitForTimeout(1500);
  const tab = await page.locator("main").innerText();
  say(`owner task/${CL} Changes: ${JSON.stringify((tab.match(/[^\n]*(changelist|No branch changes|repository branch)[^\n]*/gi) || []).slice(0, 3))}`);
  await page.screenshot({ path: `/tmp/item6-owner-cl-changes.png`, fullPage: true });
  say(`owner diff-preview requests=${JSON.stringify(previews)} errors=${JSON.stringify(log)}`);
}
await browser.close();
