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

// Playwright Chromium walk for 2026-11-09 §4 items 3-4:
// Exercises every confirmation dialog in ui-registry (Stop, Delete, Revoke
// credential, Revoke consent grant, Hard purge — plus verifies Start activates
// directly without a modal) at 1440x900 and 390x844 in light and dark, checks
// Cancel initial focus, Tab + Shift+Tab cycle, Escape cancel with no mutation,
// focus return to the opener, viewport fit and zero horizontal overflow, and
// captures open-dialog screenshots + contact-sheet.png.
//
//   cd ui-registry && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-09-registry-walk-fixes/dialogs-walk.mjs \
//     /tmp/gr-shots/dialogs-after /home/debian/bin/grain-registry

import { createRequire } from "node:module";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { startRegistryDemo } from "../../../ui-registry/e2e/screenshot.mjs";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const [outDir, binPath] = process.argv.slice(2);
if (!outDir) {
  throw new Error("usage: node dialogs-walk.mjs <out-dir> [<grain-registry-bin>]");
}
await fs.mkdir(outDir, { recursive: true });

const tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "grain-dialogs-walk-"));
const demo = await startRegistryDemo(tmpDir, binPath);
const browser = await chromium.launch();

let checks = 0;
let failures = 0;
function ok(cond, msg) {
  checks++;
  console.log(`${cond ? "PASS" : "FAIL"} ${msg}`);
  if (!cond) failures++;
}

const VIEWPORTS = {
  desktop: { width: 1440, height: 900, isMobile: false },
  phone: { width: 390, height: 844, isMobile: true },
};
const THEMES = ["light", "dark"];

async function checkOverflow(page) {
  return page.evaluate(() => {
    const vw = document.documentElement.clientWidth;
    return {
      docOverflow: document.documentElement.scrollWidth > vw + 1,
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: vw,
    };
  });
}

async function stitchContactSheet(shots, outFile) {
  const cells = await Promise.all(
    shots.map(async (s) => {
      const b64 = (await fs.readFile(s.file)).toString("base64");
      return `<figure><img src="data:image/png;base64,${b64}"><figcaption>${s.name}</figcaption></figure>`;
    }),
  );
  const html = `<!doctype html><style>
    body { margin: 0; padding: 12px; background: #888; font: 13px sans-serif;
      display: flex; flex-wrap: wrap; gap: 12px; align-items: flex-start; }
    figure { margin: 0; }
    img { display: block; max-width: 480px; max-height: 600px; border: 1px solid #333; }
    figcaption { padding-top: 4px; color: #111; font-weight: 600; }
  </style>${cells.join("")}`;
  const p = await browser.newPage({ viewport: { width: 2000, height: 900 } });
  await p.setContent(html);
  await p.screenshot({ path: outFile, fullPage: true });
  await p.close();
}

const shots = [];

try {
  for (const [vpName, vp] of Object.entries(VIEWPORTS)) {
    for (const theme of THEMES) {
      const ctx = await browser.newContext({
        viewport: { width: vp.width, height: vp.height },
        colorScheme: theme,
        isMobile: vp.isMobile,
        hasTouch: vp.isMobile,
      });
      await ctx.addInitScript(
        ([k, v]) => localStorage.setItem(k, v),
        ["grain.themeMode", theme],
      );
      const page = await ctx.newPage();
      page.on("pageerror", (e) => ok(false, `pageerror ${vpName}/${theme}: ${e.message}`));

      const verifyDialog = async (slug, opener, expectedTitle) => {
        await opener.scrollIntoViewIfNeeded();
        await opener.focus();
        await page.evaluate(() => document.activeElement?.setAttribute("data-walk-opener", "1"));
        await opener.click();

        const dialog = page.getByRole("dialog");
        await dialog.waitFor({ state: "visible", timeout: 5000 });
        const text = (await dialog.textContent()) || "";
        ok(text.includes(expectedTitle), `${vpName}/${theme} ${slug}: opens "${expectedTitle}"`);

        const focusedLabel = await page.evaluate(() => document.activeElement?.textContent?.trim() || "");
        ok(focusedLabel === "Cancel", `${vpName}/${theme} ${slug}: initial focus on Cancel (got "${focusedLabel}")`);

        const box = await dialog.boundingBox();
        ok(
          Boolean(box) && box.x >= 0 && box.x + box.width <= vp.width + 1,
          `${vpName}/${theme} ${slug}: dialog fits viewport (${box ? Math.round(box.width) : 0}px <= ${vp.width}px)`,
        );

        const ov = await checkOverflow(page);
        ok(!ov.docOverflow, `${vpName}/${theme} ${slug}: zero horizontal overflow (${ov.scrollWidth}/${ov.clientWidth})`);

        // Screenshot each confirmation in desktop/light and phone/dark + all 4 for Purge & Revoke & Creating
        const file = path.join(outDir, `${slug}-${vpName}-${theme}.png`);
        await page.screenshot({ path: file });
        if ((vpName === "desktop" && theme === "light") || (vpName === "phone" && theme === "dark")) {
          shots.push({ name: `${slug} (${vpName} ${theme})`, file });
        }

        // Cycle Tab and Shift+Tab inside the dialog.
        let trapped = true;
        const focusSeq = [];
        for (const key of ["Tab", "Tab", "Tab", "Shift+Tab", "Shift+Tab"]) {
          await page.keyboard.press(key);
          const state = await dialog.evaluate((el) => ({
            inside: el.contains(document.activeElement),
            tag: document.activeElement?.tagName,
            txt: document.activeElement?.textContent?.trim() || document.activeElement?.getAttribute("id") || "",
          }));
          focusSeq.push(`${state.tag}:${state.txt}`);
          if (!state.inside) trapped = false;
        }
        ok(trapped, `${vpName}/${theme} ${slug}: Tab/Shift+Tab stays inside dialog (${focusSeq.join(" -> ")})`);

        await page.keyboard.press("Escape");
        await dialog.waitFor({ state: "hidden", timeout: 5000 });
        const restored = await page.evaluate(() => {
          const match = document.activeElement?.getAttribute("data-walk-opener") === "1";
          document.querySelectorAll("[data-walk-opener]").forEach((el) => el.removeAttribute("data-walk-opener"));
          return match;
        });
        ok(restored, `${vpName}/${theme} ${slug}: Escape closes and restores focus to opener`);
      };

      // 1. My Entries (/): Stop & Delete
      await page.goto(demo.url);
      await page.waitForLoadState("networkidle");
      await verifyDialog("my-entries-stop", page.getByRole("button", { name: "Stop platform-vm" }), "Stop platform-vm?");
      const row = page.locator("tr", { hasText: "platform-vm" }).first();
      await verifyDialog("my-entries-delete", row.getByRole("button", { name: "Delete" }), "Delete platform-vm?");

      // 2. Entry Detail (/entries/platform-vm): Stop instance & Delete entry...
      await page.goto(`${demo.url}/entries/platform-vm`);
      await page.waitForLoadState("networkidle");
      await verifyDialog("detail-stop", page.getByRole("button", { name: "Stop instance" }), "Stop platform-vm?");
      await verifyDialog("detail-delete", page.getByRole("button", { name: "Delete entry..." }), "Delete platform-vm?");

      // 3. Entry Detail (/entries/alice-workstation): Revoke registration credential
      await page.goto(`${demo.url}/entries/alice-workstation`);
      await page.waitForLoadState("networkidle");
      await verifyDialog("detail-revoke-cred", page.getByRole("button", { name: "Revoke" }).first(), "Revoke registration credential?");

      // 4. Apps acting for you (/grants): Revoke consent grant
      await page.goto(`${demo.url}/grants`);
      await page.waitForLoadState("networkidle");
      const grantsSec = page.getByTestId("person-grants-section");
      await verifyDialog("grants-revoke", grantsSec.getByRole("button", { name: "Revoke" }).first(), "Revoke consent for");

      // 5. Admin (/admin): Hard purge
      await page.goto(`${demo.url}/admin`);
      await page.waitForLoadState("networkidle");
      await verifyDialog("admin-purge", page.getByRole("button", { name: "Hard purge" }).first(), "Hard purge alice-sandbox-old?");

      await ctx.close();
    }
  }

  // Also verify Start has no confirmation dialog (starts directly, matching auto_wake / #878)
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  await page.goto(`${demo.url}/entries/docs-preview`);
  await page.getByRole("button", { name: "Start instance" }).click();
  await page.getByText("Requested start for docs-preview.").waitFor({ timeout: 5000 });
  ok((await page.getByRole("dialog").count()) === 0, "Start instance starts directly without confirmation modal (#878)");
  await ctx.close();

  await stitchContactSheet(shots, path.join(outDir, "contact-sheet.png"));
  console.log(`Completed ${checks} checks with ${failures} failure(s).`);
} finally {
  await browser.close();
  demo.proc.kill();
  await fs.rm(tmpDir, { recursive: true, force: true });
}
process.exit(failures ? 1 : 0);
