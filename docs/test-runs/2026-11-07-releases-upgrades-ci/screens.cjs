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

// screens.cjs renders §9's two UI surfaces on live daemons at desktop
// (1440x900) and phone (390x844) widths and writes a contact sheet:
//
//   - Processes -> Schedules on the item-4 daemon (SCHED_URL), with an
//     off-target git_ref schedule showing its Suppressed chip and the
//     schedule overlay's explanation (#744);
//   - Settings -> Upgrade on a signature-verifying daemon (UPGRADE_URL),
//     before and after choosing "Release assets only", which the daemon
//     refuses (#661).
//
// Each page also gets a horizontal-overflow check (no element wider than the
// viewport). Usage (from anywhere; resolves Playwright out of ui/):
//
//   SCHED_URL=http://127.0.0.1:4290 SCHED_CODE=... SCHED_TITLE=... \
//   UPGRADE_URL=http://127.0.0.1:4291 UPGRADE_CODE=... OUT=/tmp/shots \
//   node screens.cjs
"use strict";

const path = require("node:path");
const fs = require("node:fs");
const repo = path.resolve(__dirname, "../../..");
const { chromium } = require(path.join(repo, "ui/node_modules/@playwright/test"));

const VIEWPORTS = [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
];

async function overflow(page) {
  return page.evaluate(() => {
    const vw = document.documentElement.clientWidth;
    return document.documentElement.scrollWidth > vw + 1
      ? `document scrollWidth ${document.documentElement.scrollWidth} > viewport ${vw}`
      : "";
  });
}

async function main() {
  const out = process.env.OUT || "/tmp/item4-shots";
  fs.mkdirSync(out, { recursive: true });
  const browser = await chromium.launch();
  const shots = [];
  const failures = [];
  const shot = async (page, name) => {
    const file = path.join(out, `${name}.png`);
    await page.screenshot({ path: file, fullPage: false });
    const o = await overflow(page);
    if (o) failures.push(`${name}: ${o}`);
    console.log(`${o ? "FAIL" : "PASS"} ${name}${o ? ": " + o : " (no horizontal overflow)"}`);
    shots.push({ name, file });
  };

  for (const [vpName, viewport] of VIEWPORTS) {
    // Processes -> Schedules (#744).
    {
      const ctx = await browser.newContext({
        viewport,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.SCHED_CODE}` },
      });
      const page = await ctx.newPage();
      await page.goto(`${process.env.SCHED_URL}/processes?f=kind:any:schedule`);
      const link = page.getByRole("link", { name: process.env.SCHED_TITLE });
      await link.waitFor({ timeout: 20000 });
      const row = page.locator(".process-row", { has: link });
      const chip = row.locator(".MuiChip-root", { hasText: "Suppressed" });
      const chipOK = await chip.isVisible();
      const hint = (await row.locator(".schedule-overdue.hint").textContent()) || "";
      console.log(`${chipOK ? "PASS" : "FAIL"} ${vpName}: Suppressed chip visible on "${process.env.SCHED_TITLE}"`);
      if (!chipOK) failures.push(`${vpName}: no Suppressed chip`);
      const hintOK = hint.includes("which is not on this deployment's target repo list");
      console.log(`${hintOK ? "PASS" : "FAIL"} ${vpName}: row hint "${hint.trim()}"`);
      if (!hintOK) failures.push(`${vpName}: hint ${hint}`);
      await shot(page, `processes-schedules-${vpName}`);
      await link.click();
      const overlayText = page.getByText(/This schedule is suppressed and is not polling its watched ref/);
      await overlayText.waitFor({ timeout: 10000 });
      console.log(`PASS ${vpName}: schedule overlay explains the suppression`);
      await shot(page, `schedule-overlay-${vpName}`);
      await ctx.close();
    }
    // Settings -> Upgrade on a verifying deployment (#661).
    {
      const ctx = await browser.newContext({
        viewport,
        extraHTTPHeaders: { Authorization: `Bearer ${process.env.UPGRADE_CODE}` },
      });
      const page = await ctx.newPage();
      await page.goto(`${process.env.UPGRADE_URL}/settings/upgrade`);
      const source = page.getByTestId("upgrade-image-source");
      await source.waitFor({ timeout: 20000 });
      await source.scrollIntoViewIfNeeded();
      await shot(page, `settings-upgrade-${vpName}`);
      const putDone = page.waitForResponse(
        (r) => r.url().endsWith("/api/upgrade/source") && r.request().method() === "PUT",
      );
      await source.getByLabel("Source").click();
      await page.getByRole("option", { name: "Release assets only" }).click();
      const put = await putDone;
      const body = await put.text();
      const refused = put.status() === 400 && body.includes("verifies image signatures");
      console.log(`${refused ? "PASS" : "FAIL"} ${vpName}: choosing "Release assets only" -> PUT /api/upgrade/source ${put.status()} ${body.slice(0, 120)}`);
      if (!refused) failures.push(`${vpName}: source PUT ${put.status()} ${body}`);
      // The refusal is shown to the operator, and the source stays put.
      const shown = page.getByText(/verifies image signatures/).first();
      await shown.waitFor({ timeout: 10000 });
      const status = await (await page.request.get(`${process.env.UPGRADE_URL}/api/upgrade`)).json();
      const kept = status.imageSource && status.imageSource.source !== "release-assets";
      console.log(`${kept ? "PASS" : "FAIL"} ${vpName}: refusal shown and image source still ${status.imageSource && status.imageSource.source}`);
      if (!kept) failures.push(`${vpName}: source moved to ${JSON.stringify(status.imageSource)}`);
      await shot(page, `settings-upgrade-refused-${vpName}`);
      await ctx.close();
    }
  }

  // Contact sheet: every shot, labelled, in one PNG.
  const sheet = await browser.newPage({ viewport: { width: 1800, height: 1000 } });
  const cells = shots
    .map(
      (s) =>
        `<figure><img src="data:image/png;base64,${fs.readFileSync(s.file).toString("base64")}"><figcaption>${s.name}</figcaption></figure>`,
    )
    .join("");
  await sheet.setContent(
    `<style>body{margin:12px;font:14px system-ui;background:#eee}main{display:grid;grid-template-columns:repeat(4,1fr);gap:12px;align-items:start}figure{margin:0;background:#fff;padding:6px}img{width:100%;border:1px solid #ccc}figcaption{padding-top:4px}</style><main>${cells}</main>`,
  );
  await sheet.screenshot({ path: path.join(out, "contact-sheet.png"), fullPage: true });
  await browser.close();
  if (failures.length) {
    console.log(`${failures.length} UI CHECK(S) FAILED`);
    process.exit(1);
  }
  console.log("ALL UI CHECKS PASSED");
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
