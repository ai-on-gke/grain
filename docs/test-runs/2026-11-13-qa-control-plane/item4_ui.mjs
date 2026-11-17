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

// Playwright driver for Item 4 (§2): verifies `/next/settings/tasks` QA card
// agrees with `grain qa status`, enforces inline rollout bounds without input
// stomp, enforces read-only viewer UI + 403 forced POST with a real
// `grain/viewer` bearer token, and verifies two concurrent tabs do not stomp
// each other's edits.

import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../..",
);
const { chromium } = await import(
  path.join(root, "ui-ng/node_modules/@playwright/test/index.mjs")
);
const { signIn } = await import(path.join(root, "ui/e2e/accesscode.js"));

const daemonUrl = process.env.DAEMON || "http://127.0.0.1:4281";
const accessCode = process.env.CODE;
const grainBin = process.env.GRAIN || "/home/debian/bin/grain";
const shotDir = process.env.SHOT_DIR || "";
if (shotDir) {
  fs.mkdirSync(shotDir, { recursive: true });
}

if (!accessCode) {
  throw new Error("CODE env var (daemon access code) is required");
}

function qaStatusJson() {
  const raw = execFileSync(
    grainBin,
    ["-server", daemonUrl, "-token", accessCode, "--json", "qa", "status"],
    { encoding: "utf-8" },
  );
  return JSON.parse(raw);
}

function runQaCli(args) {
  execFileSync(
    grainBin,
    ["-server", daemonUrl, "-token", accessCode, "qa", ...args],
    { encoding: "utf-8" },
  );
}

async function apiJson(method, path, token, body) {
  const res = await fetch(`${daemonUrl}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    // non-JSON error text
  }
  return { status: res.status, ok: res.ok, text, json };
}

async function signInContext(browser) {
  const { cookies } = await signIn(daemonUrl, accessCode);
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
  });
  await ctx.addCookies(cookies);
  return ctx;
}

const browser = await chromium.launch();
try {
  // 1. Fresh state: CLI and UI card agree (OFF, golden-v1-minimal, 0%, shadow=true)
  const s0 = qaStatusJson();
  assert.equal(s0.enabled, false);
  assert.equal(s0.flowId, "golden-v1-minimal");
  assert.equal(s0.rolloutPct, 0);
  assert.equal(s0.shadowOnControl, true);

  const ctxA = await signInContext(browser);
  const pageA = await ctxA.newPage();
  await pageA.goto(`${daemonUrl}/next/settings/tasks`, { waitUntil: "networkidle" });

  const enableA = pageA.getByTestId("switch-qa-enabled");
  const flowA = pageA.getByTestId("pill-qa-flow");
  const rolloutA = pageA.getByTestId("input-qa-rollout-pct");
  const shadowA = pageA.getByTestId("switch-qa-shadow-control");

  await enableA.waitFor({ state: "visible" });
  assert.equal(await enableA.getAttribute("aria-checked"), "false");
  assert.equal(await flowA.isDisabled(), true);
  assert.equal(await rolloutA.isDisabled(), true);
  assert.equal(await shadowA.isDisabled(), true);
  assert.equal(await rolloutA.inputValue(), "0");
  console.log("  [4.1a] Fresh OFF state matches between `grain qa status` and `/next/settings/tasks`");

  // 2. CLI change -> UI reflects it after reload
  runQaCli(["enable", "-flow", "repo-custom", "-rollout", "42", "-shadow-control=false"]);
  await pageA.reload({ waitUntil: "networkidle" });
  assert.equal(await enableA.getAttribute("aria-checked"), "true");
  assert.equal((await flowA.textContent()).includes("repo-custom"), true);
  assert.equal(await rolloutA.inputValue(), "42");
  assert.equal(await shadowA.getAttribute("aria-checked"), "false");
  console.log("  [4.1b] CLI enable (repo-custom, 42%, shadow=false) reflected accurately in UI card");

  // 3. Inline boundary refusals & raw input preservation + server 400 check
  for (const bad of ["", "101", "-5", "12.5"]) {
    await rolloutA.fill(bad);
    assert.equal(await rolloutA.inputValue(), bad, `input stomped while typing '${bad}'`);
    await pageA.getByTestId("error-qa-rollout-pct").waitFor({ state: "visible" });
    assert.equal(await rolloutA.getAttribute("aria-invalid"), "true");
    assert.equal(await pageA.getByTestId("settings-save-btn").isDisabled(), true);
  }
  const badPost = await apiJson("POST", "/api/settings", accessCode, { qaRolloutPct: 101 });
  assert.equal(badPost.status, 400);
  assert.equal(badPost.text.includes("rolloutPct"), true);
  await pageA.getByTestId("settings-discard-btn").click();
  await pageA.getByTestId("settings-save-bar").waitFor({ state: "hidden" });
  assert.equal(await rolloutA.inputValue(), "42");
  console.log("  [4.2] Out-of-range rollout values ('', '101', '-5', '12.5') refused inline without stomp; forced invalid POST=400");

  // 4. UI edit + Save -> CLI `grain qa status` agrees
  await flowA.click();
  await pageA.locator("#pill-qa-flow-menu button", { hasText: "deterministic-s3-only" }).click();
  await rolloutA.fill("68");
  await shadowA.click();
  await pageA.getByTestId("settings-save-btn").click();
  await pageA.getByTestId("settings-save-bar").waitFor({ state: "hidden" });

  const sAfterUi = qaStatusJson();
  assert.equal(sAfterUi.enabled, true);
  assert.equal(sAfterUi.flowId, "deterministic-s3-only");
  assert.equal(sAfterUi.rolloutPct, 68);
  assert.equal(sAfterUi.shadowOnControl, true);
  console.log("  [4.1c] UI save (deterministic-s3-only, 68%, shadow=true) matches `grain qa status`");

  // Optional screenshots on live daemon when SHOT_DIR is provided (first store pass)
  if (shotDir) {
    await pageA.setViewportSize({ width: 1440, height: 900 });
    await pageA.evaluate(() => localStorage.setItem("grain.themeMode", "light"));
    await pageA.reload({ waitUntil: "networkidle" });
    await pageA.screenshot({ path: `${shotDir}/daemon-tasks-desktop-light.png` });

    await pageA.setViewportSize({ width: 390, height: 844 });
    await pageA.waitForTimeout(200);
    await pageA.screenshot({ path: `${shotDir}/daemon-tasks-phone-light.png` });

    await pageA.evaluate(() => localStorage.setItem("grain.themeMode", "dark"));
    await pageA.reload({ waitUntil: "networkidle" });
    await pageA.setViewportSize({ width: 1440, height: 900 });
    await pageA.waitForTimeout(200);
    await pageA.screenshot({ path: `${shotDir}/daemon-tasks-desktop-dark.png` });

    await pageA.setViewportSize({ width: 390, height: 844 });
    await pageA.waitForTimeout(200);
    await pageA.screenshot({ path: `${shotDir}/daemon-tasks-phone-dark.png` });

    await pageA.evaluate(() => localStorage.setItem("grain.themeMode", "light"));
    await pageA.setViewportSize({ width: 1440, height: 900 });
    await pageA.reload({ waitUntil: "networkidle" });
  }

  // 5. Real `grain/viewer` token against real daemon: read-only banner + disabled controls + 403 on forced POST/PUT/plan
  const enableExtApi = await apiJson("POST", "/api/settings", accessCode, {
    externalAgentApi: true,
  });
  assert.equal(enableExtApi.ok, true, `enabling externalAgentApi failed: ${enableExtApi.status}`);
  const tokRes = await apiJson("POST", "/api/external-agents/tokens", accessCode, {
    name: "qa-walk-viewer",
    scopeSet: "grain/viewer",
  });
  assert.equal(tokRes.ok, true, `creating viewer token failed: ${tokRes.status} ${tokRes.text}`);
  const viewerToken = tokRes.json.token;
  assert.equal(typeof viewerToken === "string" && viewerToken.startsWith("gma_"), true);

  const ctxViewer = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    extraHTTPHeaders: { Authorization: `Bearer ${viewerToken}` },
  });
  const pageViewer = await ctxViewer.newPage();
  await pageViewer.goto(`${daemonUrl}/next/settings/tasks`, { waitUntil: "networkidle" });

  await pageViewer.getByTestId("section-role-readonly-pill").waitFor({ state: "visible" });
  await pageViewer.getByTestId("settings-role-readonly").waitFor({ state: "visible" });
  const bannerText = await pageViewer.getByTestId("settings-role-readonly").textContent();
  assert.match(bannerText, /settings:write|grain\/viewer/i);
  assert.equal(await pageViewer.getByTestId("switch-qa-enabled").isDisabled(), true);
  assert.equal(await pageViewer.getByTestId("pill-qa-flow").isDisabled(), true);
  assert.equal(await pageViewer.getByTestId("input-qa-rollout-pct").isDisabled(), true);
  assert.equal(await pageViewer.getByTestId("switch-qa-shadow-control").isDisabled(), true);

  if (shotDir) {
    await pageViewer.screenshot({ path: `${shotDir}/daemon-tasks-viewer-readonly.png` });
  }

  // Forced POST /api/settings, PUT /api/settings, and POST /api/settings/plan from the viewer page & API
  const viewerForcedPost = await pageViewer.evaluate(async () => {
    const r = await fetch("/api/settings", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ qaEnabled: false, qaRolloutPct: 0 }),
    });
    return { status: r.status, body: await r.text() };
  });
  assert.equal(viewerForcedPost.status, 403);
  assert.match(viewerForcedPost.body, /settings:write/);

  const viewerForcedPut = await apiJson("PUT", "/api/settings", viewerToken, { qaRolloutPct: 10 });
  assert.equal(viewerForcedPut.status, 403);
  assert.match(viewerForcedPut.text, /settings:write/);
  await ctxViewer.close();

  // Confirm viewer's forced POST did not mutate stored settings
  assert.equal(qaStatusJson().rolloutPct, 68);
  console.log("  [4.3] Real grain/viewer session renders read-only banner, disables QA controls, and forced POST/PUT return 403");

  // 6. Two tabs editing at once ("non-stomp" check)
  const ctxB = await signInContext(browser);
  const pageB = await ctxB.newPage();
  await pageA.reload({ waitUntil: "networkidle" });
  await pageB.goto(`${daemonUrl}/next/settings/tasks`, { waitUntil: "networkidle" });

  // Both Tab A and Tab B start from (deterministic-s3-only, 68%, shadow=true, maxAutoSubmitDepth=3).
  // Tab A edits flow -> golden-v1-minimal and saves; Tab B (without reloading) edits rollout -> 25 and saves.
  await flowA.click();
  await pageA.locator("#pill-qa-flow-menu button", { hasText: "golden-v1-minimal" }).click();
  await pageA.getByTestId("settings-save-btn").click();
  await pageA.getByTestId("settings-save-bar").waitFor({ state: "hidden" });

  await pageB.getByTestId("input-qa-rollout-pct").fill("25");
  await pageB.getByTestId("settings-save-btn").click();
  await pageB.getByTestId("settings-save-bar").waitFor({ state: "hidden" });

  const sAfterTwoTabs = qaStatusJson();
  assert.equal(sAfterTwoTabs.flowId, "golden-v1-minimal", "Tab B stomped Tab A's qaFlowId");
  assert.equal(sAfterTwoTabs.rolloutPct, 25, "Tab B's qaRolloutPct did not save");
  assert.equal((await pageB.getByTestId("pill-qa-flow").textContent()).includes("golden-v1-minimal"), true);

  // Cross-card non-stomp: Tab A edits maxAutoSubmitDepth -> 7 while Tab B flips shadowOnControl -> false
  await pageA.reload({ waitUntil: "networkidle" });
  await pageB.reload({ waitUntil: "networkidle" });
  await pageA.getByTestId("input-max-autosubmit-depth").fill("7");
  await pageA.getByTestId("settings-save-btn").click();
  await pageA.getByTestId("settings-save-bar").waitFor({ state: "hidden" });

  await pageB.getByTestId("switch-qa-shadow-control").click();
  await pageB.getByTestId("settings-save-btn").click();
  await pageB.getByTestId("settings-save-bar").waitFor({ state: "hidden" });

  const finalSettings = (await apiJson("GET", "/api/settings", accessCode)).json;
  assert.equal(finalSettings.maxAutoSubmitDepth, 7, "Tab B stomped Tab A's maxAutoSubmitDepth");
  assert.equal(finalSettings.qaFlowId, "golden-v1-minimal");
  assert.equal(finalSettings.qaRolloutPct, 25);
  assert.equal(finalSettings.qaShadowOnControl, false);
  console.log("  [4.4] Two concurrent tabs saved independent QA and Tasks fields without stomping each other");

  await ctxB.close();
  await ctxA.close();
} finally {
  await browser.close();
}
