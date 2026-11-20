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

// docs/test-plans/2026-11-14.md §3 item 4 in the browser: open several
// /next/settings tabs at once against probes.sh's slow git host, close
// one mid-flight, and report what each tab got from reachable-repos.
// probes.sh's upstream.log counts the listings they caused.
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createRequire } from "node:module";

const require = createRequire(join(process.cwd(), "ui-ng", "package.json"));
const { chromium } = require("@playwright/test");
const base = process.env.BASE ?? "http://127.0.0.1:4288";
const token = readFileSync(process.env.TOKEN_FILE ?? "/tmp/heavy-probes/data/local-api-token", "utf8").trim();
const paths = (process.env.TABS ?? "/next/settings,/next/settings/repositories,/next/settings/code,/next/context").split(",");

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
const tabs = await Promise.all(paths.map(() => ctx.newPage()));
const seen = paths.map(() => []);
const problems = paths.map(() => []);
tabs.forEach((p, i) => {
  p.on("response", (r) => {
    const u = new URL(r.url());
    if (u.pathname === "/api/deployment/reachable-repos") seen[i].push(r.status());
    else if (r.status() >= 400) problems[i].push(`${r.status()} ${u.pathname}`);
  });
  p.on("console", (m) => m.type() === "error" && problems[i].push(`console: ${m.text().slice(0, 160)}`));
});
const t0 = Date.now();
await Promise.all(tabs.map((p, i) => p.goto(base + paths[i], { waitUntil: "domcontentloaded" })));
await tabs[0].waitForTimeout(1_000);
await tabs[1].close(); // cancelled mid-flight
await Promise.all(tabs.filter((_, i) => i !== 1).map((p) => p.waitForLoadState("networkidle", { timeout: 20_000 }).catch(() => {})));
paths.forEach((p, i) => console.log(JSON.stringify({ tab: p, closedMidFlight: i === 1, reachableRepos: seen[i], problems: problems[i] })));
console.log(`settled after ${Date.now() - t0} ms`);
await browser.close();
