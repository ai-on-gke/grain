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

// Throwaway walkthrough for docs/test-runs/2026-10-07-daemon-demo-move.md;
// not part of the e2e suite (it needs docker and a local registry:2).
import { chromium } from "@playwright/test";
import fs from "node:fs";

const base = process.env.DEMO_URL;
const statusFile = process.env.STATUS_FILE;
const fail = (m) => {
  console.log("FAIL:", m);
  process.exitCode = 1;
};

const browser = await chromium.launch();
const page = await browser.newPage();
// Loopback is trusted as the demo's own actor; naming it skips the
// first-visit username dialog.
await page.addInitScript(() => localStorage.setItem("grain.username", "debian"));
let loads = 0;
page.on("load", () => loads++);
const posts = [];
page.on("response", async (r) => {
  if (r.url().endsWith("/api/upgrade") && r.request().method() === "POST") {
    posts.push({ body: r.request().postData(), code: r.status(), text: await r.text() });
  }
});

await page.goto(base + "/");
await page.getByText("Admin", { exact: true }).click();
await page.getByRole("tab", { name: "Upgrade" }).click();
const box = page.getByRole("combobox", { name: "Branch" });
await box.click();
await page.getByRole("listbox").waitFor();
const opts = page.getByRole("option");
await page.waitForFunction(() => document.querySelectorAll('[role="option"]').length > 0);
const seen = [];
for (let i = 0; i < (await opts.count()); i++) {
  const o = opts.nth(i);
  const name = (await o.locator("span").first().textContent()).trim();
  seen.push(`${name}:${(await o.getAttribute("aria-disabled")) === "true" ? "disabled" : "enabled"}`);
}
console.log("picker options:", seen.join(" "));
for (const n of ["latest", "candidate", "main", "v1.7.0"]) {
  if (!seen.includes(`${n}:enabled`)) fail(`${n} not offered as selectable`);
}
if (seen.some((s) => s.startsWith("grain-task-1:enabled"))) fail("grain-task-1 offered as selectable");
const helper = await page.locator(`[id="${await box.getAttribute("aria-describedby")}"]`).textContent();
console.log("helper text:", helper);
await page.screenshot({ path: "/tmp/upgrade-picker.png" });
await page.keyboard.press("Escape");

// grain-task-1 typed by hand: refused by the allow-list.
async function request(branch) {
  await box.fill(branch);
  await page.getByRole("button", { name: "Upgrade", exact: true }).click();
  const dlg = page.getByRole("dialog", { name: "Confirm upgrade" });
  if (await dlg.isVisible().catch(() => false) || (await dlg.waitFor({ timeout: 2000 }).then(() => true, () => false))) {
    await dlg.getByRole("button", { name: "Upgrade" }).click();
  }
}
await request("grain-task-1");
await page.waitForTimeout(1500);
console.log("POST grain-task-1:", JSON.stringify(posts.at(-1)));
if (!posts.length || posts.at(-1).code !== 400) fail("grain-task-1 was not refused with 400");
const alertText = await page.getByRole("alert").allTextContents();
console.log("alerts after refusal:", JSON.stringify(alertText));
await page.screenshot({ path: "/tmp/upgrade-refused.png" });

// v1.7.0: a real pull, health check and ref-file rewrite, with no restart.
const phases = [];
const fileStates = [];
let stop = false;
const poll = (async () => {
  while (!stop) {
    const s = await (await fetch(base + "/api/upgrade")).json();
    const key = `${s.phase}|${s.detail}`;
    if (phases.at(-1) !== key) phases.push(key);
    try {
      const f = fs.readFileSync(statusFile, "utf8");
      if (fileStates.at(-1) !== f) fileStates.push(f);
    } catch {}
    await new Promise((r) => setTimeout(r, 100));
  }
})();
await request("v1.7.0");
const status = page.locator("dl.upgrade-status .upgrade-phase");
await status.waitFor();
await page.waitForFunction(
  () => {
    const p = document.querySelector("dl.upgrade-status .upgrade-phase")?.textContent;
    return p && p !== "running";
  },
  null,
  { timeout: 120000 },
);
await page.waitForTimeout(1000);
stop = true;
await poll;
console.log("POST v1.7.0:", JSON.stringify(posts.at(-1)));
console.log("api phases:", JSON.stringify(phases, null, 1));
console.log("status file states:", fileStates.length);
for (const f of fileStates) console.log("  ", f.replace(/\s+/g, " ").slice(0, 400));
console.log("final UI phase:", await status.textContent());
console.log("upgrade-status dl:", (await page.locator("dl.upgrade-status").textContent()).replace(/\s+/g, " "));
console.log("page loads:", loads);
await page.screenshot({ path: "/tmp/upgrade-done.png", fullPage: true });
await browser.close();
