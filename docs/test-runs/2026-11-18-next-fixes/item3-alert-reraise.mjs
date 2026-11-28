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

// Item 3: a real alert (pipeline.not-publishing, raised by the daemon's
// base-branch-release automation) selected in /next/inbox, re-raised with
// a different body while every GET /api/notifications?family= read fails.
// RERAISE is a shell command that changes the finding (the walk's sqlite3
// UPDATE of base_branch_release).
import { execSync } from "node:child_process";
import { chromium, session, watch } from "./lib.mjs";

const base = process.env.BASE, code = process.env.CODE;
const browser = await chromium.launch();
const ctx = await session(browser, base, code);
const page = await ctx.newPage();
const log = watch(page);
await page.goto(`${base}/next/inbox`);
const title = page.locator("li", { hasText: "Nothing is publishing from acme/widgets release" }).first();
await title.waitFor({ timeout: 15000 });
await title.click();
await page.waitForTimeout(1500);
const bodyOf = async () => (await page.locator("body").innerText()).match(/Nothing has published from acme\/widgets release since .*?\d+ releases/g) ?? [];
console.log(`before: ${JSON.stringify(await bodyOf())}`);
let blocked = 0;
await page.route((u) => u.pathname === "/api/notifications" && u.searchParams.has("family"), (r) => { blocked++; return r.fulfill({ status: 503, contentType: "application/json", body: '{"error":"walk: family reads blocked"}' }); });
execSync(process.env.RERAISE, { stdio: "inherit" });
let after = [];
for (let i = 0; i < 30; i++) {
  await page.waitForTimeout(3000);
  after = await bodyOf();
  if (after.some((b) => b.includes(process.env.WANT))) break;
}
console.log(`after: ${JSON.stringify(after)} family reads blocked=${blocked}`);
console.log(`${after.some((b) => b.includes(process.env.WANT)) && !after.some((b) => b.includes(process.env.OLD)) ? "PASS" : "FAIL"} the selected alert shows the re-raised body only`);
await page.screenshot({ path: "/tmp/item3-after.png", fullPage: true });
console.log(`errors=${JSON.stringify(log)}`);
await browser.close();
