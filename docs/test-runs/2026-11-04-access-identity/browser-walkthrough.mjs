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

// Playwright browser walkthrough for docs/test-plans/2026-11-04.md §3:
// drives Chromium against the live `grain daemon` behind `tests/iap/iapsim.go`
// (and `grain demo` if DEMO_URL is set), verifies UI behaviors for items 1, 2,
// 4, 5 and 6, checks desktop (1440x900) and phone (390x844) layouts in light
// and dark themes for horizontal overflow and console errors, and captures PNG
// screenshots.

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(
  new URL("../../../ui/package.json", import.meta.url),
);
const { chromium } = require("@playwright/test");

const env = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const outDir = env.outDir;
const shotDir = path.join(outDir, "screenshots");
fs.mkdirSync(shotDir, { recursive: true });

const results = [];
let failures = 0;

function record(id, desc, expected, got, pass) {
  if (!pass) failures++;
  results.push({ id, desc, expected, got: String(got), pass });
  const tag = pass ? "PASS" : "FAIL";
  console.log(`${tag}  [${id}] ${desc}: ${got}`);
}

const VIEWPORTS = {
  1440: { width: 1440, height: 900 },
  390: { width: 390, height: 844 },
};

async function withPage(browser, { width = 1440, theme = "light", headers = {} }, fn) {
  const context = await browser.newContext({
    viewport: VIEWPORTS[width],
    colorScheme: theme,
    extraHTTPHeaders: headers,
  });
  await context.addInitScript((mode) => {
    try {
      window.localStorage.setItem("grain.themeMode", mode);
    } catch {
      // Ignore opaque origins or 503 static error pages.
    }
  }, theme);
  const page = await context.newPage();
  const pageErrors = [];
  page.on("pageerror", (err) => pageErrors.push(err.message || String(err)));
  try {
    await fn(page, pageErrors);
  } finally {
    await context.close();
  }
}

async function assertNoOverflow(page, label) {
  const dims = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  record(
    `layout-${label}`,
    `${label} has no horizontal overflow`,
    `scrollWidth <= clientWidth + 1`,
    `${dims.scrollWidth} <= ${dims.clientWidth}`,
    dims.scrollWidth <= dims.clientWidth + 1,
  );
}

const mode = process.argv[3] || "main";
const browser = await chromium.launch();
try {
  const aliceHeaders = { "X-Goog-IAP-JWT-Assertion": env.aliceJWT };
  const frankHeaders = { "X-Goog-IAP-JWT-Assertion": env.frankJWT };
  const auditorHeaders = { "X-Goog-IAP-JWT-Assertion": env.auditorJWT };
  const codeHeaders = { Authorization: `Bearer ${env.accessCode}` };

  if (mode === "verifier-down") {
    await withPage(browser, { width: 1440, theme: "light", headers: aliceHeaders }, async (page) => {
      const resp = await page.goto(`${env.verifierDownURL}/`, { waitUntil: "domcontentloaded" });
      const status = resp.status();
      const retryAfter = resp.headers()["retry-after"];
      const h1 = await page.locator("h1").innerText();
      const body = await page.locator("main").innerText();
      record(
        "UI-6a",
        "Browser receives 503 Retry-After: 5 and usable starting-up message when verifier JWKS is down",
        "503 Retry-After=5 'Sign-in verification is starting up'",
        `${status} Retry-After=${retryAfter} h1=${h1}`,
        status === 503 &&
          retryAfter === "5" &&
          h1.includes("Sign-in verification is starting up") &&
          body.includes("Reload the page in a few seconds") &&
          !body.includes("Sign-in could not be verified"),
      );
      await page.screenshot({ path: path.join(shotDir, "verifier-down-1440-light.png") });
    });
    fs.writeFileSync(path.join(outDir, "browser-verifier-down.json"), JSON.stringify(results, null, 2));
    if (failures > 0) process.exit(1);
    console.log(`Verifier-down browser check passed (${results.length} check).`);
    process.exit(0);
  }

  // --- 1. Live UI allowlist add & remove (Item 1) + UI SA reason check (Item 2) ---
  await withPage(browser, { width: 1440, theme: "light", headers: aliceHeaders }, async (page, pageErrors) => {
    await page.goto(`${env.daemonURL}/settings/access`);
    await page.getByTestId("allowed-principals-table").waitFor({ state: "visible" });

    // Check dropped -ui-allowed-principals banner from one-time migration (Item 2).
    const droppedBanner = page.getByTestId("dropped-flag-entries-warning");
    const droppedVisible = await droppedBanner.isVisible();
    const droppedText = droppedVisible ? await droppedBanner.innerText() : "";
    record(
      "UI-2a",
      "Settings -> Access warns about dropped serviceAccount: and domain: entries from -ui-allowed-principals",
      "dropped-ct@my-proj.iam.gserviceaccount.com & domain:example.com",
      droppedText.replace(/\s+/g, " ").slice(0, 140),
      droppedVisible &&
        droppedText.includes("serviceAccount:dropped-ci@my-proj.iam.gserviceaccount.com") &&
        droppedText.includes("domain:example.com"),
    );

    // Check signed-in identity & role in Settings -> Access.
    const thisBrowserText = await page.locator("#this-browser").innerText();
    const ownRoleText = await page.getByTestId("access-own-role").innerText();
    record(
      "UI-1a",
      "Settings -> Access displays verified IAP identity and grain/operator role",
      "Signed in through IAP as alice@example.com + grain/operator",
      `${thisBrowserText.replace(/\s+/g, " ").slice(0, 90)} | ${ownRoleText.replace(/\s+/g, " ")}`,
      thisBrowserText.includes("Signed in through IAP as alice@example.com") &&
        ownRoleText.includes("grain/operator"),
    );

    // Item 1: Before UI add, frank@example.com is refused with 403 pointing to Settings -> Access.
    const beforeRes = await page.request.get(`${env.daemonURL}/api/whoami`, {
      headers: frankHeaders,
    });
    const beforeJson = await beforeRes.json();
    const beforeErr = String(beforeJson.error || "");
    record(
      "UI-1b",
      "frank@example.com refused before UI allowlist add, pointing to Settings -> Access",
      "403 mentioning Settings -> Access",
      `${beforeRes.status()} ${beforeErr.slice(0, 110)}`,
      beforeRes.status() === 403 && beforeErr.includes("Settings -> Access"),
    );

    // Select non-dangerous role grain/contributor via MUI Select combobox so IAP operator alice can add frank live from UI.
    const entryInput = page.getByLabel("User, group or service account");
    const roleSelect = page.locator("#add-allowed-principal").getByRole("combobox");
    const reasonInput = page.getByTestId("access-reason-input");
    const addBtn = page.getByRole("button", { name: "Add allowed principal" });

    await roleSelect.click();
    await page.getByRole("option", { name: "grain/contributor" }).click();
    await entryInput.fill("user:frank@example.com");
    await reasonInput.fill("UI live allowlist check");
    await addBtn.click();
    await page.getByTestId("principal-row-user:frank@example.com").waitFor({ state: "visible" });

    const midRes = await page.request.get(`${env.daemonURL}/api/whoami`, {
      headers: frankHeaders,
    });
    const midJson = await midRes.json();
    record(
      "UI-1c",
      "UI allowlist add admits frank@example.com immediately without restart",
      "200 allowed=true verified=true",
      `${midRes.status()} allowed=${midJson.allowed} verified=${midJson.verified} label=${midJson.actor?.label}`,
      midRes.status() === 200 && midJson.allowed === true && midJson.verified === true,
    );

    // Remove frank from UI and confirm immediate refusal.
    await page
      .getByTestId("principal-row-user:frank@example.com")
      .getByRole("button", { name: "Remove user:frank@example.com" })
      .click();
    await page.getByTestId("principal-row-user:frank@example.com").waitFor({ state: "detached" });

    const afterRes = await page.request.get(`${env.daemonURL}/api/whoami`, {
      headers: frankHeaders,
    });
    record(
      "UI-1d",
      "UI allowlist remove refuses frank@example.com immediately without restart",
      "403",
      afterRes.status(),
      afterRes.status() === 403,
    );

    // Item 2: Attempt to add a serviceAccount: from UI with NO reason -> refused in UI!
    await roleSelect.click();
    await page.getByRole("option", { name: "grain/contributor" }).click();
    await entryInput.fill("serviceAccount:no-reason-ui@my-proj.iam.gserviceaccount.com");
    await reasonInput.fill("");
    await addBtn.click();
    // Either button is disabled or an alert / helper error appears, and row is NOT created.
    await page.waitForTimeout(300);
    const saRowCount = await page
      .getByTestId("principal-row-serviceAccount:no-reason-ui@my-proj.iam.gserviceaccount.com")
      .count();
    const pageText = await page.locator("#allowed-principals").innerText();
    record(
      "UI-2b",
      "UI refuses serviceAccount: entry with empty reason",
      "row count 0 and reason prompt shown",
      `rows=${saRowCount}, mentions reason=${/reason/i.test(pageText)}`,
      saRowCount === 0 && /reason/i.test(pageText),
    );

    // Confirm release-bot serviceAccount sits in its own Service accounts group with who/when/why.
    const saSection = page.getByTestId("allowed-service-accounts");
    const saSectionText = await saSection.innerText();
    record(
      "UI-2c",
      "Service accounts section shows admitted SA separately with role and reason",
      "release-bot@my-proj.iam.gserviceaccount.com + release pipeline files tasks through IAP",
      saSectionText.replace(/\s+/g, " ").slice(0, 140),
      saSectionText.includes("serviceAccount:release-bot@my-proj.iam.gserviceaccount.com") &&
        saSectionText.includes("release pipeline files tasks through IAP"),
    );

    record("UI-1e", "Settings -> Access has zero uncaught page errors", "0", pageErrors.length, pageErrors.length === 0);
  });

  // --- 2. Item 5: Settings -> Audit with partial scopes (settings:read only) ---
  for (const [tag, hdrs] of [
    ["iap-custom-role", auditorHeaders],
    ["gma-settings-read", { Authorization: `Bearer ${env.auditOnlyToken}` }],
  ]) {
    await withPage(browser, { width: 1440, theme: "light", headers: hdrs }, async (page, pageErrors) => {
      await page.goto(`${env.daemonURL}/settings/audit`);
      await page.getByTestId("config-audit-table").waitFor({ state: "visible" });
      await page.getByTestId("team-audit-table-error").waitFor({ state: "visible" });
      await page.getByTestId("secret-audit-table-error").waitFor({ state: "visible" });

      const cfgText = await page.getByTestId("config-audit-table").innerText();
      const teamErr = await page.getByTestId("team-audit-table-error").innerText();
      const secErr = await page.getByTestId("secret-audit-table-error").innerText();

      record(
        `UI-5-${tag}`,
        `Settings -> Audit (${tag}, settings:read only) renders Configuration changes and clean refusal warnings on Credential access & Secret resolutions`,
        "config-audit-table populated + 2 clean refusal alerts + 0 pageErrors",
        `cfgHasReason=${cfgText.includes("release pipeline")}, teamErr=${teamErr.slice(0, 55)}, secErr=${secErr.slice(0, 55)}, errs=${pageErrors.length}`,
        cfgText.includes("release pipeline") &&
          teamErr.includes("could not be read") &&
          secErr.includes("could not be read") &&
          pageErrors.length === 0,
      );
      if (tag === "iap-custom-role") {
        await assertNoOverflow(page, "audit-partial-1440-light");
        await page.screenshot({ path: path.join(shotDir, "audit-partial-1440-light.png") });
      }
    });
  }

  // --- 3. Item 4: Labels on task page, timeline, comments, pause banner, and chat ---
  await withPage(browser, { width: 1440, theme: "light", headers: aliceHeaders }, async (page, pageErrors) => {
    await page.goto(`${env.daemonURL}/tasks/${env.labelTaskID}`);
    await page.getByTestId("filed-by").waitFor({ state: "visible" });

    const filedBy = await page.getByTestId("filed-by").innerText();
    const detailText = await page.locator("body").innerText();

    const expectedLabels = [
      "alice@example.com",
      "operator (access code)",
      "external agent triage-bot for alice@example.com",
      "grain",
      "Paused by operator (access code)",
    ];
    const missing = expectedLabels.filter((w) => !detailText.includes(w));
    const hasRawPair =
      detailText.includes("{human, operator}") ||
      detailText.includes("human, operator") ||
      detailText.includes("operator (human)");

    record(
      "UI-4a",
      "Task detail page shows consistent PrincipalLabel across Filed by, comments, timeline, and pause banner with no raw {human, operator}",
      "missing=[] hasRawPair=false",
      `filedBy=${filedBy}, missing=${JSON.stringify(missing)}, hasRawPair=${hasRawPair}`,
      filedBy.trim() === "alice@example.com" && missing.length === 0 && !hasRawPair && pageErrors.length === 0,
    );

    // Also inspect interactive chat view on the interactive chat task!
    await page.goto(`${env.daemonURL}/tasks/${env.chatTaskID}`);
    await page.getByText("Follow-up in chat from external agent.").first().waitFor({ state: "visible" });
    const chatText = await page.locator("body").innerText();
    const chatHasLabels =
      chatText.includes("alice@example.com") &&
      chatText.includes("operator (access code)") &&
      chatText.includes("external agent triage-bot for alice@example.com");
    const chatRaw =
      chatText.includes("{human, operator}") ||
      chatText.includes("human, operator") ||
      chatText.includes("operator (human)");
    record(
      "UI-4b",
      "Interactive chat view shows identical PrincipalLabel senders and no raw operator",
      "chatHasLabels=true chatRaw=false",
      `chatHasLabels=${chatHasLabels} chatRaw=${chatRaw}`,
      chatHasLabels && !chatRaw,
    );
  });

  // --- 4. Item 6: Verifier down browser experience (503 starting-up page) ---
  if (env.verifierDownURL) {
    await withPage(browser, { width: 1440, theme: "light", headers: aliceHeaders }, async (page) => {
      const resp = await page.goto(`${env.verifierDownURL}/`, { waitUntil: "domcontentloaded" });
      const status = resp.status();
      const retryAfter = resp.headers()["retry-after"];
      const h1 = await page.locator("h1").innerText();
      const body = await page.locator("main").innerText();
      record(
        "UI-6a",
        "Browser receives 503 Retry-After: 5 and usable starting-up message when verifier JWKS is down",
        "503 Retry-After=5 'Sign-in verification is starting up'",
        `${status} Retry-After=${retryAfter} h1=${h1}`,
        status === 503 &&
          retryAfter === "5" &&
          h1.includes("Sign-in verification is starting up") &&
          body.includes("Reload the page in a few seconds") &&
          !body.includes("Sign-in could not be verified"),
      );
      await page.screenshot({ path: path.join(shotDir, "verifier-down-1440-light.png") });
    });
  }

  // --- 5. Prompt Item 6: Full screenshot matrix (Settings -> Access, Settings -> Audit, Task page, Sidebar)
  //        at 1440 & 390 px, light & dark, checking overflow & uncaught errors on every shot ---
  for (const width of [1440, 390]) {
    for (const theme of ["light", "dark"]) {
      // (a) Settings -> Access (signed in as alice@example.com via IAP)
      await withPage(browser, { width, theme, headers: aliceHeaders }, async (page, pageErrors) => {
        await page.goto(`${env.daemonURL}/settings/access`);
        await page.getByTestId("allowed-principals-table").waitFor({ state: "visible" });
        await assertNoOverflow(page, `access-${width}-${theme}`);
        await page.screenshot({ path: path.join(shotDir, `access-${width}-${theme}.png`) });
        if (pageErrors.length > 0) {
          record(`err-access-${width}-${theme}`, "No page errors", "0", pageErrors.join("; "), false);
        }
      });

      // (b) Settings -> Audit (full operator view via access code so all 3 cards + config_audit reasons show)
      await withPage(browser, { width, theme, headers: codeHeaders }, async (page, pageErrors) => {
        await page.goto(`${env.daemonURL}/settings/audit`);
        await page.getByTestId("config-audit-table").waitFor({ state: "visible" });
        if (width === 1440 && theme === "light") {
          const auditText = await page.getByTestId("config-audit-table").innerText();
          const codeTileTitle = await page.getByTestId("user-tile").getAttribute("title");
          const accessPageRes = await page.request.get(`${env.daemonURL}/api/whoami`, { headers: codeHeaders });
          const codeWho = await accessPageRes.json();
          record(
            "UI-3-audit",
            "Settings -> Audit displays config_audit reasons and actor labels (and access-code sidebar user-tile shows operator (access code))",
            "reasons & actor labels visible + tile=operator (access code)",
            `tile=${codeTileTitle} who=${codeWho.actor?.label} | ${auditText.replace(/\s+/g, " ").slice(0, 120)}`,
            auditText.includes("release pipeline files tasks through IAP") &&
              auditText.includes("enable telemetry check from task run") &&
              auditText.includes("operator (access code)") &&
              codeTileTitle === "operator (access code)",
          );
        }
        await assertNoOverflow(page, `audit-${width}-${theme}`);
        await page.screenshot({ path: path.join(shotDir, `audit-${width}-${theme}.png`) });
        if (pageErrors.length > 0) {
          record(`err-audit-${width}-${theme}`, "No page errors", "0", pageErrors.join("; "), false);
        }
      });

      // (c) Task page (/tasks/<labelTaskID>)
      await withPage(browser, { width, theme, headers: aliceHeaders }, async (page, pageErrors) => {
        await page.goto(`${env.daemonURL}/tasks/${env.labelTaskID}`);
        await page.getByTestId("filed-by").waitFor({ state: "visible" });
        await assertNoOverflow(page, `task-${width}-${theme}`);
        await page.screenshot({ path: path.join(shotDir, `task-${width}-${theme}.png`) });
        if (pageErrors.length > 0) {
          record(`err-task-${width}-${theme}`, "No page errors", "0", pageErrors.join("; "), false);
        }
      });

      // (d) Sidebar (desktop unfolded rail with user-tile; phone navigation drawer open showing phone-signed-in row)
      await withPage(browser, { width, theme, headers: aliceHeaders }, async (page, pageErrors) => {
        await page.goto(`${env.daemonURL}/`);
        if (width === 390) {
          await page.getByRole("button", { name: "Open navigation" }).click();
          const drawer = page.getByRole("dialog", { name: "Navigation" });
          await drawer.getByTestId("phone-signed-in").waitFor({ state: "visible" });
        } else {
          await page.getByTestId("user-tile").waitFor({ state: "visible" });
        }
        await assertNoOverflow(page, `sidebar-${width}-${theme}`);
        await page.screenshot({ path: path.join(shotDir, `sidebar-${width}-${theme}.png`) });
        if (pageErrors.length > 0) {
          record(`err-sidebar-${width}-${theme}`, "No page errors", "0", pageErrors.join("; "), false);
        }
      });
    }
  }
} finally {
  await browser.close();
}

fs.writeFileSync(path.join(outDir, "browser-results.json"), JSON.stringify(results, null, 2));
if (failures > 0) {
  console.error(`Browser walkthrough had ${failures} failure(s)`);
  process.exit(1);
}
console.log(`Browser walkthrough passed (${results.length} checks).`);
