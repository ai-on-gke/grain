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

// docs/test-plans/2026-11-10.md §6 item 2: load every list page and Settings
// page of the seeded daemon in Chromium, record each /api/ call the browser
// makes, and flag (a) any request carrying limit= or an X-Grain-Agent-List
// header and (b) any answer the UI read that came back cut short
// (truncated: true) without the UI having asked for a page.
//
// Run from ui/ so Playwright resolves:
//   cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-10-agent-api-paging/ui_requests.mjs \
//     http://127.0.0.1:4281 "$(cat /var/tmp/grain-paging-sqlite/data/local-api-token)" out.json

import { createRequire } from "node:module";
import { writeFileSync } from "node:fs";

// Resolve Playwright from the working directory (ui/), not from this file.
const { chromium } = createRequire(process.cwd() + "/")("playwright");

const [base, code, out] = process.argv.slice(2);
const pages = [
  "/", "/tasks", "/inbox", "/ci", "/templates", "/schedules", "/suites", "/boards",
  "/memory", "/release-notes", "/notifications", "/console",
  "/settings/general", "/settings/access", "/settings/agents", "/settings/capabilities",
  "/settings/capability-servers", "/settings/ci", "/settings/config-repo", "/settings/credentials",
  "/settings/engine", "/settings/executors", "/settings/git", "/settings/hostds", "/settings/hosts",
  "/settings/memory", "/settings/models", "/settings/preferences", "/settings/sandboxes",
  "/settings/secrets", "/settings/tasks", "/settings/upgrade",
];

const browser = await chromium.launch();
const context = await browser.newContext();
const signIn = await context.request.post(`${base}/api/auth/access-code`, {
  form: { code: code.trim(), name: "owner@example.com" },
  maxRedirects: 0,
});
if (![200, 303].includes(signIn.status())) {
  throw new Error(`access-code sign-in answered ${signIn.status()}`);
}

const calls = [];
const page = await context.newPage();
page.on("response", async (resp) => {
  const req = resp.request();
  const url = new URL(req.url());
  if (!url.pathname.startsWith("/api/") || req.method() !== "GET") return;
  const headers = await req.allHeaders();
  const row = {
    page: page.url().replace(base, ""),
    path: url.pathname + url.search,
    status: resp.status(),
    limit: url.searchParams.get("limit"),
    agentListHeader: Object.keys(headers).some((h) => h.toLowerCase() === "x-grain-agent-list"),
    truncated: false,
    items: null,
  };
  try {
    if ((resp.headers()["content-type"] || "").includes("json")) {
      const body = await resp.json();
      if (body && typeof body === "object" && !Array.isArray(body)) {
        row.truncated = body.truncated === true;
        for (const k of ["items", "tasks", "runs", "entries", "sessions", "secrets", "tokens", "sets", "repos", "models", "targets", "events", "workflows", "faults", "batches", "gates", "versions"]) {
          if (Array.isArray(body[k])) { row.items = body[k].length; break; }
        }
      } else if (Array.isArray(body)) {
        row.items = body.length;
      }
    }
  } catch {
    // An event stream or a body already consumed: no list to check.
  }
  calls.push(row);
});

for (const p of pages) {
  // The UI polls, so the network never goes idle: load, then give each
  // page's reads a few seconds to land.
  try {
    await page.goto(base + p, { waitUntil: "load", timeout: 15000 });
  } catch {
    // Recorded calls still count; move on.
  }
  await page.waitForTimeout(4000);
}
await browser.close();

const flagged = calls.filter((c) => c.agentListHeader || c.limit !== null || c.truncated);
writeFileSync(out, JSON.stringify({ pages, calls, flagged }, null, 2));
console.log(`${calls.length} GET /api calls across ${pages.length} pages; ${flagged.length} flagged`);
for (const f of flagged) console.log(JSON.stringify(f));
