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


// docs/test-plans/2026-11-16.md §7 item 4, second half: a real release
// failure gating publishing on /next/fleet, over the
// 2026-11-15-release-train-publishing-held harness started with
// PAUSE_FOR_SCREENSHOTS=1 (it stops while publishing is held and goes on
// when $WORK_DIR/continue appears, closing the triage task in its 2a).
//   NOIAP=1 DAEMON=http://127.0.0.1:4290 WORK_DIR=/var/tmp/walk-rt node item4b-publishing.mjs
import fs from "node:fs";
import path from "node:path";
import { chromium, api, as, note, info, flush, shot, work, DESKTOP, PHONE } from "./lib.mjs";

const browser = await chromium.launch();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const pub = async () => (await api("GET", "/api/sandboxes")).baseBranchPublishing ?? [];
const finding = (await pub()).find((p) => p.gated);
note("4b publishing gated by the failed releases", !!finding, JSON.stringify(finding && { repo: finding.repo, branch: finding.branch, failures: finding.failures, triage: finding.triageTaskID }));
const triage = finding?.triageTaskID;

const views = [];
for (const [vp, label, scheme] of [[DESKTOP, "desktop", "light"], [PHONE, "phone", "light"], [DESKTOP, "desktop-dark", "dark"]]) {
  const { ctx, page } = await as(browser, "alice@example.com", vp, scheme);
  await page.goto("fleet");
  const banner = page.getByTestId("fleet-publishing").first();
  await banner.getByText(/Publishing held/).first().waitFor({ timeout: 20000 }).catch(() => {});
  const text = (await banner.innerText().catch(() => "")).replace(/\s+/g, " ");
  info(`4b ${label} banner`, JSON.stringify(text.slice(0, 400)));
  note(`4b ${label} banner says Publishing held`, /Publishing held/.test(text), "");
  const link = banner.getByRole("link", { name: new RegExp(`#${triage}\\b`) });
  note(`4b ${label} banner links the triage task`, (await link.count()) > 0, `triage #${triage} -> ${await link.first().getAttribute("href").catch(() => null)}`);
  const box = await banner.boundingBox().catch(() => null);
  if (box) note(`4b ${label} banner inside the viewport`, box.x >= 0 && box.x + box.width <= vp.width + 0.5, JSON.stringify(box));
  await shot(page, `4b-${label}-publishing-held`);
  views.push({ ctx, page, label });
}
// The held schedule says why and what its skips became.
{
  const { page } = views[0];
  await page.goto("processes?q=release-train");
  await sleep(2500);
  await shot(page, "4b-desktop-processes-release-train");
  const rt = page.getByRole("link", { name: /release.train/i }).first();
  if (await rt.count()) {
    await rt.click();
    await sleep(2500);
    const held = (await page.locator("main").innerText()).replace(/\s+/g, " ");
    const m = /(Held[^.]*\.[^.]*\.)/.exec(held);
    info("4b held schedule detail", JSON.stringify(m ? m[1] : held.slice(0, 300)));
    note("4b held schedule says why", /held/i.test(held) && /publish/i.test(held), page.url().replace(/^.*\/next/, "/next"));
    await shot(page, "4b-desktop-schedule-held-detail");
  } else {
    info("4b held schedule", "no release-train link on /next/processes?q=release-train");
  }
  await page.goto("fleet");
}
// Let the harness close the triage task (its step 2a), then watch the
// banner clear on the open page.
fs.writeFileSync(path.join(work, "continue"), "");
const t0 = Date.now();
let cleared = null;
for (let i = 0; i < 120; i++) {
  if (!(await pub()).some((p) => p.gated)) {
    cleared = (Date.now() - t0) / 1000;
    break;
  }
  await sleep(500);
}
note("4b closing the triage task releases the gate", cleared !== null, `after ${cleared}s`);
const { page } = views[0];
let gone = null;
for (let i = 0; i < 60; i++) {
  if (!/Publishing held/.test(await page.getByTestId("fleet-publishing").first().innerText().catch(() => ""))) {
    gone = (Date.now() - t0) / 1000;
    break;
  }
  await sleep(500);
}
note("4b banner clears on the open page", gone !== null, `after ${gone}s; now ${JSON.stringify((await page.getByTestId("fleet-publishing").first().innerText().catch(() => "(no banner)")).replace(/\s+/g, " ").slice(0, 200))}`);
await shot(page, "4b-desktop-publishing-cleared");
for (const v of views) note(`4b ${v.label} no page errors`, v.page.errors.length === 0, JSON.stringify(v.page.errors));
flush("item4b");
await browser.close();
