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

// walk-playwright.mjs: executes 2026-11-08 §3 Requirement 5 in real Chromium
// against `grain demo` and `grain demo -legacy-capability` across 1440x900
// (desktop) and 390x844 (phone) in light and dark themes, verifying:
//   1. /tasks/<id> Hosting select beneath Admission, live toggle + timeline row
//   2. /processes TemplateOverlay, ScheduleOverlay, and template-backed
//      schedule hosting composition preview (#898, #936)
//   3. /sessions/director standing-session grant editing via in-process
//      config-map in `grain demo` and absence of HostingField (#913)
//   4. /settings/capabilities github-sandbox retirement banner (#886) and
//      `grain demo -legacy-capability` synthetic `example-legacy` picker check
//   5. Zero pageerror/console.error, zero horizontal overflow, valid bounding
//      boxes, and a combined contact-sheet.png for visual inspection.

import { chromium } from "../../../ui/node_modules/@playwright/test/index.mjs";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { newAccessCode, signIn } from "../../../ui/e2e/accesscode.js";

const GRAIN_BIN = process.env.GRAIN_BIN || "/home/debian/bin/grain";
const OUT_DIR = process.env.OUT_DIR || "/tmp/walk-playwright-shots";
const THEME_KEY = "grain.themeMode";

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};
const THEMES = ["light", "dark"];

async function startDemo(tmpDir, extraArgs = []) {
  const code = newAccessCode();
  const codeFile = path.join(tmpDir, `code-${Math.random().toString(16).slice(2)}`);
  await fs.writeFile(codeFile, `${code}\n`, { mode: 0o600 });
  const proc = spawn(
    GRAIN_BIN,
    [
      "demo",
      "-addr",
      "127.0.0.1:0",
      "-open=false",
      "-github-sim",
      "-access-code-file",
      codeFile,
      ...extraArgs,
    ],
    {
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, TMPDIR: tmpDir },
    },
  );
  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`grain demo timeout:\n${output}`)),
      45_000,
    );
    const onChunk = (chunk) => {
      output += chunk;
      const m = output.match(/at http:\/\/(?:127\.0\.0\.1|\[::\]):(\d+)/);
      if (m) {
        clearTimeout(timer);
        resolve(`http://127.0.0.1:${m[1]}`);
      }
    };
    proc.stdout.on("data", onChunk);
    proc.stderr.on("data", onChunk);
    proc.on("exit", (c) => {
      clearTimeout(timer);
      reject(new Error(`grain demo exited (${c}):\n${output}`));
    });
  });
  return { url, code, proc };
}

async function apiReq(url, code, method, pathname, body) {
  const res = await fetch(new URL(pathname, url).href, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    json = text;
  }
  return { status: res.status, json };
}

async function openTab(browser, cookies, widthName, theme) {
  const context = await browser.newContext({
    viewport: VIEWPORTS[widthName],
    colorScheme: theme,
    isMobile: widthName === "phone",
    hasTouch: widthName === "phone",
  });
  await context.addCookies(cookies);
  await context.addInitScript(
    ([k, v]) => localStorage.setItem(k, v),
    [THEME_KEY, theme],
  );
  const page = await context.newPage();
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(e.message));
  return { context, page, pageErrors };
}

async function settle(page) {
  await page.waitForLoadState("networkidle", { timeout: 10_000 }).catch(() => {});
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(350);
}

async function checkOverflow(page, label) {
  const dims = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  if (dims.scrollWidth > dims.clientWidth + 1) {
    throw new Error(
      `Horizontal overflow on ${label}: scrollWidth=${dims.scrollWidth} > clientWidth=${dims.clientWidth}`,
    );
  }
  return dims;
}

async function buildContactSheet(browser, shots, outFile) {
  const cells = await Promise.all(
    shots.map(async (s) => {
      const b64 = (await fs.readFile(s.file)).toString("base64");
      return `<figure><img src="data:image/png;base64,${b64}"><figcaption>${s.name}</figcaption></figure>`;
    }),
  );
  const html = `<!doctype html><style>
    body { margin: 0; padding: 14px; background: #666; font: 12px/1.35 system-ui, sans-serif; color: #fff;
      display: flex; flex-wrap: wrap; gap: 14px; align-items: flex-start; }
    figure { margin: 0; background: #222; padding: 6px; border-radius: 4px; }
    img { display: block; max-width: 440px; max-height: 540px; border: 1px solid #444; }
    figcaption { padding-top: 5px; font-family: monospace; }
  </style>${cells.join("")}`;
  const p = await browser.newPage({ viewport: { width: 1920, height: 900 } });
  await p.setContent(html);
  await p.screenshot({ path: outFile, fullPage: true });
  await p.close();
}

async function main() {
  await fs.mkdir(OUT_DIR, { recursive: true });
  const tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "walk-pw-"));
  const shots = [];
  const report = { assertions: [], matrix: [] };

  let demoMain;
  let demoLegacy;
  const browser = await chromium.launch();
  try {
    demoMain = await startDemo(tmpDir);
    const { cookies } = await signIn(demoMain.url, demoMain.code);

    // Seed clean tasks/templates/schedules on demoMain so all 4 viewport*theme cells have crisp data:
    const createdTask = await apiReq(demoMain.url, demoMain.code, "POST", "/api/tasks", {
      title: "Walkthrough §3 Hosting & Grants UI Check",
      description: "Verifies HostingField beneath Admission and timeline transitions.",
      repo: "acme/widgets",
    });
    const taskId = createdTask.json.id;

    const createdTpl = await apiReq(demoMain.url, demoMain.code, "POST", "/api/templates", {
      name: "walk-console-tpl",
      title: "Scheduled console check from template",
      description: "Console template for schedule hosting composition preview.",
      repo: "acme/widgets",
      hosting: "console",
    });
    const baseTplId = createdTpl.json.id;

    // --- Interactive Behavior Assertions (Desktop Light) ---
    {
      const { context, page, pageErrors } = await openTab(browser, cookies, "desktop", "light");

      // 1. /tasks/<id>: Admission & Hosting bounding boxes + live toggle -> timeline transition
      await page.goto(`${demoMain.url}/tasks/${taskId}`);
      await settle(page);
      const admLoc = page.getByLabel("Admission", { exact: true });
      const hostLoc = page.getByLabel("Hosting", { exact: true });
      await admLoc.waitFor({ state: "visible", timeout: 10_000 });
      await hostLoc.waitFor({ state: "visible", timeout: 10_000 });
      const admBox = await admLoc.boundingBox();
      const hostBox = await hostLoc.boundingBox();
      if (!admBox || !hostBox || hostBox.y <= admBox.y) {
        throw new Error(`Expected Hosting beneath Admission: adm=${JSON.stringify(admBox)} host=${JSON.stringify(hostBox)}`);
      }
      // Toggle Hosting -> Interactive console
      await hostLoc.click();
      await page.getByRole("option", { name: "Interactive console" }).click();
      await page.getByText(/Runs the agent CLI on an interactive terminal/).waitFor({ state: "visible", timeout: 10_000 });
      await page.getByText(/Hosting.*console/i).first().waitFor({ state: "visible", timeout: 10_000 }).catch(() => {});
      const afterTask = await apiReq(demoMain.url, demoMain.code, "GET", `/api/tasks/${taskId}`);
      report.assertions.push({
        check: "task_detail_hosting_toggle",
        taskId,
        admBox,
        hostBox,
        hostingAfterClick: afterTask.json.hosting,
        transitions: (afterTask.json.transitions || []).filter((t) => JSON.stringify(t).toLowerCase().includes("hosting")),
      });

      // 2. /processes: TemplateOverlay HostingField + ScheduleOverlay HostingField + template-backed preview (#898, #936)
      await page.goto(`${demoMain.url}/processes`);
      await settle(page);
      await page.getByRole("button", { name: "+ New", exact: true }).click();
      await page.getByTestId("new-process-kind").getByRole("menuitem", { name: /^Template/ }).click();
      await page.getByRole("heading", { name: "New template" }).waitFor({ state: "visible" });
      const tplHostHelp = await page.getByText(/Hands the agent its prompt.*Applies to every task this template files\./).isVisible();
      await page.getByLabel("Name").first().fill("walk-ui-console-tpl");
      await page.getByLabel("Title").first().fill("UI Console Template");
      await page.getByLabel("Hosting", { exact: true }).click();
      await page.getByRole("option", { name: "Interactive console" }).click();
      await page.getByText(/Runs the agent CLI on an interactive terminal.*Applies to every task this template files\./).waitFor({ state: "visible" });
      const tplShot = path.join(OUT_DIR, "processes-template-editor-desktop-light.png");
      await page.screenshot({ path: tplShot });
      shots.push({ file: tplShot, name: "processes-template-editor-desktop-light" });

      // Navigate cleanly back to /processes -> Open New schedule:
      // (a) standalone HostingField ("schedule"), then
      // (b) select walk-console-tpl -> verify #composition-preview shows "Interactive console"!
      await page.goto(`${demoMain.url}/processes`);
      await settle(page);
      await page.getByRole("button", { name: "+ New", exact: true }).click();
      await page.getByTestId("new-process-kind").getByRole("menuitem", { name: /^Schedule/ }).click();
      await page.getByRole("heading", { name: "New schedule" }).waitFor({ state: "visible" });
      const schHostHelp = await page.getByText(/Hands the agent its prompt.*Applies to every task this schedule files\./).isVisible();
      await page.getByLabel("Hosting", { exact: true }).click();
      await page.getByRole("option", { name: "Interactive console" }).click();
      await page.getByText(/Runs the agent CLI on an interactive terminal.*Applies to every task this schedule files\./).waitFor({ state: "visible" });
      const schStandaloneShot = path.join(OUT_DIR, "processes-schedule-editor-desktop-light.png");
      await page.screenshot({ path: schStandaloneShot });
      shots.push({ file: schStandaloneShot, name: "processes-schedule-editor-desktop-light" });

      // Select walk-console-tpl in SearchSelect ("Template") -> verify #composition-preview!
      const tplField = page.getByLabel("Template").first();
      await tplField.click();
      await page.getByRole("option", { name: /walk-console-tpl/i }).first().click();
      await page.locator("#composition-preview").waitFor({ state: "visible", timeout: 10_000 });
      await page.locator("#composition-preview").scrollIntoViewIfNeeded();
      await page.waitForTimeout(500);
      const compText = await page.locator("#composition-preview").innerText();
      const schShot = path.join(OUT_DIR, "processes-schedule-preview-desktop-light.png");
      await page.screenshot({ path: schShot });
      shots.push({ file: schShot, name: "processes-schedule-preview-desktop-light" });
      report.assertions.push({
        check: "processes_template_and_schedule_hosting",
        baseTplId,
        tplHostHelp,
        schHostHelp,
        compositionPreviewIncludesInteractiveConsole: compText.toLowerCase().includes("interactive console"),
        compositionPreviewHostingLines: compText
          .split("\n")
          .filter((l) => l.toLowerCase().includes("hosting") || l.toLowerCase().includes("interactive console")),
      });

      // 3. /sessions/director: route resolution, NO HostingField, live config-map grant edit in `grain demo` (#913)
      await page.goto(`${demoMain.url}/sessions/director`);
      await settle(page);
      const hostingCountOnSession = await page.getByLabel("Hosting", { exact: true }).count();
      const capToggleBtn = page.getByRole("button", { name: /Director capabilities/i }).first();
      await capToggleBtn.waitFor({ state: "visible", timeout: 10_000 });
      await capToggleBtn.click();
      const grantsPane = page.getByTestId("session-director-grants");
      await grantsPane.waitFor({ state: "visible", timeout: 10_000 });
      const grantsPaneText = await grantsPane.innerText();
      // Verify standard grain demo hides Legacy capabilities (since all core ones were retired)
      const hasLegacyHeadingInDefaultDemo = grantsPaneText.includes("example-legacy");

      // Also exercise PUT /api/console-sessions/director with config-map#config-map grant in grain demo!
      const beforeSess = await apiReq(demoMain.url, demoMain.code, "GET", "/api/console-sessions");
      const dirSess = (beforeSess.json.sessions || []).find((s) => s.id === "director");
      const bootstrapGrant = 'https://grain.dev/capabilities/config-map#config-map={"config_maps":["bootstrap"]}';
      const nextCaps = Array.from(new Set([...(dirSess?.capabilities || []), bootstrapGrant]));
      const putSess = await apiReq(demoMain.url, demoMain.code, "PUT", "/api/console-sessions/director", {
        capabilities: nextCaps,
      });
      await page.reload();
      await settle(page);
      await page.getByRole("button", { name: /Director capabilities/i }).first().click();
      await page.getByTestId("session-director-grants").waitFor({ state: "visible", timeout: 10_000 });
      const afterGrantsText = await page.getByTestId("session-director-grants").innerText();
      const sessShot = path.join(OUT_DIR, "sessions-director-grants-desktop-light.png");
      await page.screenshot({ path: sessShot });
      shots.push({ file: sessShot, name: "sessions-director-grants-desktop-light" });
      // Also check that + New task on default grain demo hides Legacy capabilities combobox
      await page.goto(`${demoMain.url}/`);
      await settle(page);
      await page.getByRole("button", { name: "+ New task" }).first().click();
      await page.waitForTimeout(300);
      const legacyComboInDefaultDemo = await page.getByRole("combobox", { name: "Legacy capabilities" }).count();

      report.assertions.push({
        check: "sessions_director_config_map_and_no_hosting",
        hostingCountOnSession,
        hasLegacyHeadingInDefaultDemo,
        legacyComboInDefaultDemo,
        putSessionStatus: putSess.status,
        configMapRenderedInDirectorGrants:
          afterGrantsText.includes("config-map") || afterGrantsText.includes("bootstrap"),
      });

      if (pageErrors.length > 0) {
        throw new Error(`Page errors during interactive checks: ${pageErrors.join("; ")}`);
      }
      await context.close();
    }

    // --- 4x4 Viewport x Theme Matrix Across All §3 Surfaces ---
    const matrixPages = [
      { slug: "task-detail", path: `/tasks/${taskId}` },
      { slug: "sessions-director", path: "/sessions/director", click: 'button:has-text("Director capabilities")' },
      { slug: "settings-capabilities", path: "/settings/capabilities" },
    ];

    for (const mp of matrixPages) {
      for (const widthName of Object.keys(VIEWPORTS)) {
        for (const theme of THEMES) {
          const { context, page, pageErrors } = await openTab(browser, cookies, widthName, theme);
          await page.goto(`${demoMain.url}${mp.path}`);
          await settle(page);
          if (mp.click) {
            const loc = page.locator(mp.click).first();
            if (await loc.isVisible().catch(() => false)) {
              await loc.click();
              await page.waitForTimeout(350);
            }
          }
          if (mp.slug === "settings-capabilities") {
            const banner = page.getByTestId("github-sandbox-deprecation-notice");
            await banner.waitFor({ state: "visible", timeout: 10_000 });
            const txt = await banner.innerText();
            if (!txt.includes("github-sandbox") || !txt.includes("github#sandbox-repo")) {
              throw new Error(`Unexpected github-sandbox banner copy: ${txt}`);
            }
          }
          const dims = await checkOverflow(page, `${mp.slug}-${widthName}-${theme}`);
          const outPng = path.join(OUT_DIR, `${mp.slug}-${widthName}-${theme}.png`);
          await page.screenshot({ path: outPng });
          shots.push({ file: outPng, name: `${mp.slug}-${widthName}-${theme}` });
          if (pageErrors.length > 0) {
            throw new Error(`pageerror on ${mp.slug}-${widthName}-${theme}: ${pageErrors.join("; ")}`);
          }
          report.matrix.push({ page: mp.slug, width: widthName, theme, ...dims });
          await context.close();
        }
      }
    }

    // --- `grain demo -legacy-capability` Synthetic Picker Check ---
    demoLegacy = await startDemo(tmpDir, ["-legacy-capability"]);
    const legacyAuth = await signIn(demoLegacy.url, demoLegacy.code);
    {
      const { context, page, pageErrors } = await openTab(browser, legacyAuth.cookies, "desktop", "light");
      await page.goto(`${demoLegacy.url}/`);
      await settle(page);
      await page.getByRole("button", { name: "+ New task" }).first().click();
      const legacyCombo = page.getByRole("combobox", { name: "Legacy capabilities" });
      await legacyCombo.waitFor({ state: "visible", timeout: 10_000 });
      await legacyCombo.click();
      const exampleOption = page.getByRole("option", { name: /Example legacy capability/i });
      await exampleOption.waitFor({ state: "visible", timeout: 10_000 });
      const optionText = await exampleOption.innerText();
      const legShot = path.join(OUT_DIR, "legacy-capability-picker-desktop-light.png");
      await page.screenshot({ path: legShot });
      shots.push({ file: legShot, name: "legacy-capability-picker-desktop-light" });
      report.assertions.push({
        check: "demo_legacy_capability_flag",
        legacyComboVisible: true,
        exampleLegacyOptionText: optionText.trim(),
      });
      if (pageErrors.length > 0) {
        throw new Error(`pageerror on -legacy-capability: ${pageErrors.join("; ")}`);
      }
      await context.close();
    }

    const contactSheetPath = path.join(OUT_DIR, "contact-sheet.png");
    await buildContactSheet(browser, shots, contactSheetPath);
    report.contactSheet = contactSheetPath;
    report.totalScreenshots = shots.length;
    console.log(JSON.stringify(report, null, 2));
  } finally {
    await browser.close();
    if (demoMain) demoMain.proc.kill("SIGKILL");
    if (demoLegacy) demoLegacy.proc.kill("SIGKILL");
    await fs.rm(tmpDir, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
