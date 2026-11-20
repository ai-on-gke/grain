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

// docs/test-plans/2026-11-14.md §3 item 1 in the browser: the /next/
// dispatch banner over a real paused daemon (pause.sh setup), across
// /api/config 304s, a second pause, an extension and an early lift.
// The browser runs in America/Los_Angeles; the team zone is Asia/Tokyo.
//
//   STORE=sqlite ./pause.sh setup
//   NODE_PATH=ui-ng/node_modules node banner.mjs
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(join(process.cwd(), "ui-ng", "package.json"));
const { chromium } = require("@playwright/test");
const here = dirname(fileURLToPath(import.meta.url));
const store = process.env.STORE ?? "sqlite";
const work = process.env.WORK ?? `/tmp/heavy-pause-${store}`;
const base = `http://127.0.0.1:${process.env.PORT ?? 4282}`;
const token = () => readFileSync(`${work}/data/local-api-token`, "utf8").trim();
const step = (cmd) => execFileSync(join(here, "pause.sh"), [cmd], { stdio: "inherit", env: process.env });

const browser = await chromium.launch();
const ctx = await browser.newContext({
  timezoneId: "America/Los_Angeles",
  extraHTTPHeaders: { Authorization: `Bearer ${token()}` },
  viewport: { width: 1440, height: 900 },
});
const page = await ctx.newPage();
await page.clock.install();
const configs = [];
const errors = [];
page.on("response", (r) => {
  if (new URL(r.url()).pathname === "/api/config") configs.push(r.status());
  else if (r.status() >= 400) errors.push(`${r.status()} ${r.url()}`);
});
page.on("console", (m) => m.type() === "error" && errors.push(`console: ${m.text()}`));

const banner = async () =>
  (await page.locator(".dispatch-banner").first().innerText({ timeout: 5_000 }).catch(() => "<none>"))
    .replace(/\s+/g, " ")
    .trim();
const report = async (label) => console.log(`${label}: [config ${configs.join(",")}] ${await banner()}`);

await page.goto(`${base}/next/inbox`);
await page.waitForSelector(".dispatch-banner", { timeout: 20_000 });
await report("initial");
// Ten minutes later: the poll is answered 304 with the body the server
// sent ten minutes ago (secondsRemaining ten minutes stale).
await page.clock.fastForward("10:00");
await page.waitForTimeout(1_000);
await report("+10m (polls answered from the ETag)");

step("second");
await page.clock.fastForward("01:01");
await page.waitForTimeout(1_000);
await report("second pause, one poll later");

step("extend");
await page.clock.fastForward("01:01");
await page.waitForTimeout(1_000);
await report("extended, one poll later");

execFileSync("curl", ["-fsS", "-o", "/dev/null", "-X", "DELETE", "-H", `Authorization: Bearer ${token()}`, `${base}/api/pause`]);
await page.clock.fastForward("01:01");
await page.waitForTimeout(1_000);
await report("lifted, one poll later");
console.log(`4xx/console errors: ${errors.length ? errors.join("\n  ") : "none"}`);
await browser.close();
