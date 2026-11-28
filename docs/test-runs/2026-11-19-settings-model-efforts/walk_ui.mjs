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

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../..",
);
const { chromium } = require(
  path.join(repoRoot, "ui-ng/node_modules/playwright"),
);
const AxeBuilder = require(
  path.join(repoRoot, "ui-ng/node_modules/@axe-core/playwright"),
).default;

const BASE = process.env.WALK_API;
const TOKEN = process.env.WALK_TOKEN;
const SHOT_DIR = process.env.WALK_SHOT_DIR || "";
const DB_KIND = process.env.WALK_DB || "sqlite";

if (!BASE || !TOKEN) {
  console.error("WALK_API and WALK_TOKEN are required");
  process.exit(2);
}
if (SHOT_DIR) fs.mkdirSync(SHOT_DIR, { recursive: true });

const results = {
  db: DB_KIND,
  chromiumVersion: "",
  checks: [],
  findings: [],
};

function record(name, ok, detail) {
  results.checks.push({ name, ok, detail });
  const mark = ok ? "PASS" : "FAIL";
  console.log(`[${DB_KIND}] [${mark}] ${name}: ${detail}`);
}

function finding(id, detail) {
  results.findings.push({ id, detail });
  console.log(`[${DB_KIND}] [FINDING ${id}] ${detail}`);
}

async function api(method, p, body) {
  const res = await fetch(`${BASE}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${TOKEN}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = text;
  }
  if (!res.ok) {
    throw new Error(`${method} ${p} -> ${res.status}: ${text}`);
  }
  return json;
}

async function axeViolations(page, selector) {
  let builder = new AxeBuilder({ page }).withTags([
    "wcag2a",
    "wcag2aa",
    "wcag21a",
    "wcag21aa",
  ]);
  if (selector) builder = builder.include(selector);
  const res = await builder.analyze();
  return res.violations
    .filter((v) => v.impact === "serious" || v.impact === "critical")
    .map((v) => `${v.id}(${v.impact},${v.nodes.length}): ${v.help}`);
}

async function openContext(browser, { width, height, theme }) {
  const ctx = await browser.newContext({
    viewport: { width, height },
    colorScheme: theme,
    extraHTTPHeaders: { Authorization: `Bearer ${TOKEN}` },
  });
  const page = await ctx.newPage();
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(`pageerror: ${e.message}`));
  page.on("console", (msg) => {
    if (msg.type() === "error") {
      pageErrors.push(`console.error: ${msg.text()}`);
    }
  });
  await page.addInitScript((t) => {
    localStorage.setItem("grain.themeMode", t);
    localStorage.setItem("grain-theme-mode", t);
  }, theme);
  return { ctx, page, pageErrors };
}

async function seedConnections() {
  await api("PUT", "/api/deployment/agent-backends/agy-main", {
    id: "agy-main",
    name: "Antigravity main",
    framework: "antigravity",
    authMode: "ambient",
    models: [
      "gemini-3.1-pro",
      "opus-5.5-high",
      "opus-5.5-xhigh",
      "gemini-4-carbon-xhigh",
      "auto",
    ],
  });
  await api("PUT", "/api/deployment/agent-backends/codex-main", {
    id: "codex-main",
    name: "Codex main",
    framework: "codex",
    authMode: "ambient",
    models: ["gpt-5.1-codex", "gpt-5.1-codex-max"],
  });
  await api("PUT", "/api/deployment/agent-backends/claude-main", {
    id: "claude-main",
    name: "Claude main",
    framework: "claude",
    authMode: "ambient",
    models: ["claude-opus-5-5"],
  });
}

async function pickModel(page, testid, model) {
  await page.getByTestId(testid).click();
  const opt = page
    .locator(`[data-testid="${testid}-opt"][data-model="${model}"]`)
    .first();
  await opt.waitFor({ state: "visible", timeout: 10000 });
  await opt.click();
  await page
    .getByTestId(`${testid}-panel`)
    .waitFor({ state: "hidden", timeout: 5000 });
  await page.waitForTimeout(50);
}

async function pickPill(page, testid, value) {
  await page.getByTestId(testid).click();
  const opt = page.getByTestId(`${testid}-opt-${value}`);
  await opt.waitFor({ state: "visible", timeout: 10000 });
  await opt.click();
  await page
    .getByTestId(`${testid}-menu`)
    .waitFor({ state: "hidden", timeout: 5000 });
  await page.waitForTimeout(50);
}

async function main() {
  const browser = await chromium.launch({ headless: true });
  results.chromiumVersion = browser.version();
  try {
    await seedConnections();
    const settingsInit = await api("GET", "/api/settings");
    record(
      "vocabularies-from-daemon",
      Array.isArray(settingsInit.geminiEfforts) &&
        Array.isArray(settingsInit.codexEfforts),
      `geminiEfforts=${JSON.stringify(settingsInit.geminiEfforts)}, codexEfforts=${JSON.stringify(settingsInit.codexEfforts)}`,
    );

    // ------------------------------------------------------------------
    // Scenario 1: Save/reload & deployDraft dirty state on /next/settings/models
    // ------------------------------------------------------------------
    await api("PUT", "/api/deployment", {
      defaultModels: [
        "gemini-3.1-pro",
        "gpt-5.1-codex",
        "claude-opus-5-5",
        "opus-5.5-high",
      ],
      modelSettings: [],
    });
    const depBefore = await api("GET", "/api/deployment");
    record(
      "empty-modelSettings-omitted-on-get",
      !("modelSettings" in depBefore) || depBefore.modelSettings == null,
      `GET /api/deployment modelSettings=${JSON.stringify(depBefore.modelSettings)}`,
    );

    const { ctx, page, pageErrors } = await openContext(browser, {
      width: 1440,
      height: 900,
      theme: "light",
    });

    await page.goto(`${BASE}/next/settings/models`, {
      waitUntil: "networkidle",
    });
    const card = page.getByTestId("card-model-defaults");
    await card.waitFor({ state: "visible", timeout: 15000 });

    // 1a. Verify no-effort models hide picker on fallback rows.
    const fb1HasPicker = await card.getByTestId("effort-fallback-1").count();
    const fb1None = await card
      .getByTestId("effort-fallback-none-1")
      .textContent();
    const fb2HasPicker = await card.getByTestId("effort-fallback-2").count();
    const fb2None = await card
      .getByTestId("effort-fallback-none-2")
      .textContent();
    record(
      "fallback-no-effort-hidden",
      fb1HasPicker === 0 &&
        fb1None?.includes("Claude takes no reasoning effort") &&
        fb2HasPicker === 0 &&
        fb2None?.includes("Effort is part of the model name: high"),
      `claude=${JSON.stringify(fb1None?.trim())}, opus-5.5-high=${JSON.stringify(fb2None?.trim())}`,
    );

    // 1b. Revert to Model default clears "1 unsaved change" save bar cleanly.
    await pickPill(page, "effort-default-model", "high");
    const saveBarText1 = await page
      .getByTestId("settings-save-bar")
      .textContent();
    const barShown = await page.getByTestId("settings-save-bar").isVisible();
    await pickPill(page, "effort-default-model", "");
    await page.waitForTimeout(150);
    const barCleared = !(await page
      .getByTestId("settings-save-bar")
      .isVisible());
    record(
      "revert-unset-effort-clears-save-bar",
      barShown && barCleared,
      `shown=${barShown} (${saveBarText1?.trim()}), clearedAfterRevert=${barCleared}`,
    );

    // 1c. Set effort on default (gemini-3.1-pro -> high) & Codex fallback (gpt-5.1-codex -> xhigh), save & reload.
    await pickPill(page, "effort-default-model", "high");
    await pickPill(page, "effort-fallback-0", "xhigh");
    await page.getByTestId("settings-save-btn").click();
    await page
      .getByTestId("settings-save-bar")
      .waitFor({ state: "hidden", timeout: 10000 });
    await page.reload({ waitUntil: "networkidle" });
    await card.waitFor({ state: "visible" });
    const defAfterReload = await card
      .getByTestId("effort-default-model")
      .textContent();
    const fb0AfterReload = await card
      .getByTestId("effort-fallback-0")
      .textContent();
    const depAfterSave1 = await api("GET", "/api/deployment");
    record(
      "save-and-reload-default-and-codex-effort",
      defAfterReload?.includes("high") &&
        fb0AfterReload?.includes("xhigh") &&
        depAfterSave1.modelSettings?.length === 2,
      `UI default=${JSON.stringify(defAfterReload?.trim())}, fb0=${JSON.stringify(fb0AfterReload?.trim())}, API=${JSON.stringify(depAfterSave1.modelSettings)}`,
    );

    // 1d. Modify an existing stored effort in place (same row count = 2) -> formatSettingValue detects edit.
    await pickPill(page, "effort-default-model", "medium");
    const dirtyOnInPlace = await page
      .getByTestId("settings-save-bar")
      .isVisible();
    const dirtyText = dirtyOnInPlace
      ? (await page.getByTestId("settings-save-bar").textContent())?.trim()
      : "";
    await page.getByTestId("settings-save-btn").click();
    await page
      .getByTestId("settings-save-bar")
      .waitFor({ state: "hidden", timeout: 10000 });
    await page.reload({ waitUntil: "networkidle" });
    await card.waitFor({ state: "visible" });
    const defAfterInPlace = await card
      .getByTestId("effort-default-model")
      .textContent();
    const depAfterInPlace = await api("GET", "/api/deployment");
    record(
      "in-place-same-row-count-edit-detected-and-persisted",
      dirtyOnInPlace &&
        defAfterInPlace?.includes("medium") &&
        depAfterInPlace.modelSettings?.find((r) => r.model === "gemini-3.1-pro")
          ?.effort === "medium",
      `dirtyBar=${dirtyOnInPlace} (${dirtyText}), reloaded=${JSON.stringify(defAfterInPlace?.trim())}, API=${JSON.stringify(depAfterInPlace.modelSettings)}`,
    );

    // ------------------------------------------------------------------
    // Scenario 2: "Other models" adder reconciliation
    // Put gpt-5.1-codex as default so gemini-3.1-pro, gpt-5.1-codex-max,
    // claude-opus-5-5, auto, opus-5.5-high, gemini-4-carbon-xhigh are all
    // available in the "Other models" adder!
    // ------------------------------------------------------------------
    await api("PUT", "/api/deployment", {
      defaultModels: ["gpt-5.1-codex"],
      modelSettings: [],
    });
    await page.reload({ waitUntil: "networkidle" });
    await card.waitFor({ state: "visible" });

    const effortBtn = card.getByTestId("select-model-setting-effort");
    const addBtn = card.getByTestId("add-model-setting-btn");
    const initDisabled =
      (await effortBtn.isDisabled()) && (await addBtn.isDisabled());
    record(
      "adder-initially-disabled",
      initDisabled,
      `effortDisabled=${await effortBtn.isDisabled()}, addDisabled=${await addBtn.isDisabled()}`,
    );

    // Pick Codex gpt-5.1-codex-max, verify no empty "Model default" option, pick "xhigh".
    await pickModel(page, "picker-model-setting-model", "gpt-5.1-codex-max");
    await effortBtn.click();
    const hasEmptyOpt =
      (await page.getByTestId("select-model-setting-effort-opt-").count()) > 0;
    await page.getByTestId("select-model-setting-effort-opt-xhigh").click();
    await page
      .getByTestId("select-model-setting-effort-menu")
      .waitFor({ state: "hidden", timeout: 5000 });
    await page.waitForTimeout(50);
    const codexXhighText = (await effortBtn.textContent())?.trim();

    // Switch to bare Antigravity (gemini-3.1-pro) after picking xhigh.
    // Note: on HEAD, antigravity.Efforts() = ["low","medium","high","xhigh","max"],
    // so "xhigh" IS a valid Antigravity effort; test both xhigh AND Codex-only "minimal"!
    await pickModel(page, "picker-model-setting-model", "gemini-3.1-pro");
    const agyAfterXhigh = (await effortBtn.textContent())?.trim();
    const agyValidAfterXhigh = settingsInit.geminiEfforts.includes(
      agyAfterXhigh ?? "",
    );
    record(
      "adder-codex-xhigh-to-agy",
      !hasEmptyOpt && codexXhighText === "xhigh" && agyValidAfterXhigh,
      `hasEmptyOpt=${hasEmptyOpt}, codexPicked=${codexXhighText}, afterSwitchToGemini=${agyAfterXhigh} (in geminiEfforts=${JSON.stringify(settingsInit.geminiEfforts)})`,
    );

    // Now pick Codex "minimal" (Codex-only, NOT in geminiEfforts) -> switch to gemini-3.1-pro -> must reconcile to "high"!
    await pickModel(page, "picker-model-setting-model", "gpt-5.1-codex-max");
    await pickPill(page, "select-model-setting-effort", "minimal");
    const codexMinText = (await effortBtn.textContent())?.trim();
    await pickModel(page, "picker-model-setting-model", "gemini-3.1-pro");
    const agyAfterMinimal = (await effortBtn.textContent())?.trim();
    record(
      "adder-codex-minimal-reconciles-for-agy",
      codexMinText === "minimal" && agyAfterMinimal === "high",
      `codexPicked=${codexMinText}, afterSwitchToGemini=${agyAfterMinimal}`,
    );

    // Select claude-opus-5-5, auto, opus-5.5-high, gemini-4-carbon-xhigh in adder:
    // verify effort select disables, add button disables, and explanatory hint appears.
    for (const [modelId, expectedHint] of [
      ["claude-opus-5-5", "Claude takes no reasoning effort."],
      ["auto", "The provider chooses the effort."],
      ["opus-5.5-high", "Effort is part of the model name: high."],
      ["gemini-4-carbon-xhigh", "Effort is part of the model name: xhigh."],
    ]) {
      await pickModel(page, "picker-model-setting-model", modelId);
      const effDis = await effortBtn.isDisabled();
      const addDis = await addBtn.isDisabled();
      const hint = (
        await card.getByTestId("model-setting-effort-none").textContent()
      )?.trim();
      record(
        `adder-no-effort-${modelId}`,
        effDis && addDis && hint === expectedHint,
        `effortDisabled=${effDis}, addDisabled=${addDis}, hint=${JSON.stringify(hint)}`,
      );
    }

    // ------------------------------------------------------------------
    // Scenario 3A: Stale stored effort on no-effort models IN "Other models"
    // on /next/settings/models
    // ------------------------------------------------------------------
    await api("PUT", "/api/deployment", {
      defaultModels: ["gemini-3.1-pro", "gpt-5.1-codex"],
      modelSettings: [
        { model: "opus-5.5-high", effort: "low" },
        { model: "auto", effort: "high" },
        { model: "claude-opus-5-5", effort: "high" },
      ],
    });
    await page.reload({ waitUntil: "networkidle" });
    await card.waitFor({ state: "visible" });
    for (const [modelId, expectedSub] of [
      ["opus-5.5-high", "(not applied: Effort is part of the model name: high.)"],
      ["auto", "(not applied: The provider chooses the effort.)"],
      ["claude-opus-5-5", "(not applied: Claude takes no reasoning effort.)"],
    ]) {
      const text = (
        await card.getByTestId(`model-setting-unused-${modelId}`).textContent()
      )?.trim();
      record(
        `next-other-models-stale-${modelId}`,
        text === expectedSub,
        `text=${JSON.stringify(text)}`,
      );
    }

    // ------------------------------------------------------------------
    // Scenario 3B: Stale stored effort on no-effort models IN default/fallback
    // chain on /next/settings/models! (Check for BUG!)
    // ------------------------------------------------------------------
    await api("PUT", "/api/deployment", {
      defaultModels: ["opus-5.5-high", "auto", "claude-opus-5-5", "gpt-5.1-codex"],
      modelSettings: [
        { model: "opus-5.5-high", effort: "low" },
        { model: "auto", effort: "high" },
        { model: "claude-opus-5-5", effort: "high" },
      ],
    });
    await page.reload({ waitUntil: "networkidle" });
    await card.waitFor({ state: "visible" });
    if (SHOT_DIR) {
      await page.screenshot({
        path: path.join(SHOT_DIR, `next-stale-chain-desktop-light.png`),
        fullPage: true,
      });
    }
    const defStaleBtn = (
      await card.getByTestId("effort-default-model").textContent()
    )?.trim();
    const defFieldText = (
      await card.getByTestId("default-model-effort").textContent()
    )?.trim();
    const fb0StaleBtn = (
      await card.getByTestId("effort-fallback-0").textContent()
    )?.trim();
    const fb0RowText = (
      await card.getByTestId("fallback-row-0").textContent()
    )?.trim();
    const fb1StaleBtn = (
      await card.getByTestId("effort-fallback-1").textContent()
    )?.trim();
    const fb1RowText = (
      await card.getByTestId("fallback-row-1").textContent()
    )?.trim();
    const chainShowsNotApplied =
      defStaleBtn?.includes("(not applied)") &&
      fb0StaleBtn?.includes("(not applied)") &&
      fb1StaleBtn?.includes("(not applied)") &&
      defFieldText?.includes("Effort is part of the model name: high") &&
      fb0RowText?.includes("The provider chooses the effort") &&
      fb1RowText?.includes("Claude takes no reasoning effort");
    record(
      "next-chain-stale-no-effort-says-not-applied-with-reason",
      Boolean(chainShowsNotApplied),
      `defaultBtn=${JSON.stringify(defStaleBtn)}, defaultField=${JSON.stringify(defFieldText)}, fb0Btn=${JSON.stringify(fb0StaleBtn)}, fb1Btn=${JSON.stringify(fb1StaleBtn)}`,
    );
    if (!chainShowsNotApplied) {
      finding(
        "F1",
        `On /next/settings/models, stale stored effort on no-effort default/fallback models shows "${defStaleBtn}" / "${fb0StaleBtn}" / "${fb1StaleBtn}" instead of "(not applied)" and omits the explanatory reason (es.reason).`,
      );
    }

    // Also measure if desktop fallback row effort pill overflows 210px column!
    const fb0CellBox = await card
      .getByTestId("fallback-row-0")
      .locator(".mp-chain-effort")
      .boundingBox();
    const fb0BtnBox = await card.getByTestId("effort-fallback-0").boundingBox();
    const fb0ActionsBox = await card
      .getByTestId("fallback-row-0")
      .locator(".mp-chain-actions")
      .boundingBox();
    const pillOverlapsActions =
      fb0BtnBox && fb0ActionsBox
        ? fb0BtnBox.x + fb0BtnBox.width > fb0ActionsBox.x + 1
        : false;
    record(
      "next-stale-fallback-pill-fits-column",
      !pillOverlapsActions,
      `cell=${JSON.stringify(fb0CellBox)}, btn=${JSON.stringify(fb0BtnBox)}, actions=${JSON.stringify(fb0ActionsBox)}`,
    );

    // Clear stale efforts on /next/settings/models (both chain and other-models) and confirm clean save.
    await pickPill(page, "effort-default-model", "");
    await pickPill(page, "effort-fallback-0", "");
    await pickPill(page, "effort-fallback-1", "");
    await page.getByTestId("settings-save-btn").click();
    await page
      .getByTestId("settings-save-bar")
      .waitFor({ state: "hidden", timeout: 10000 });
    const depAfterChainClear = await api("GET", "/api/deployment");
    record(
      "next-chain-stale-clear-persists",
      !depAfterChainClear.modelSettings ||
        depAfterChainClear.modelSettings.length === 0,
      `modelSettings after clear=${JSON.stringify(depAfterChainClear.modelSettings)}`,
    );

    // Re-seed stale in "other models" and clear via Remove buttons.
    await api("PUT", "/api/deployment", {
      defaultModels: ["gemini-3.1-pro", "gpt-5.1-codex"],
      modelSettings: [
        { model: "opus-5.5-high", effort: "low" },
        { model: "auto", effort: "high" },
        { model: "claude-opus-5-5", effort: "high" },
      ],
    });
    await page.reload({ waitUntil: "networkidle" });
    await card.waitFor({ state: "visible" });
    for (const m of ["opus-5.5-high", "auto", "claude-opus-5-5"]) {
      await card.getByRole("button", { name: `Remove effort for ${m}` }).click();
    }
    await page.getByTestId("settings-save-btn").click();
    await page
      .getByTestId("settings-save-bar")
      .waitFor({ state: "hidden", timeout: 10000 });
    const depAfterOtherClear = await api("GET", "/api/deployment");
    record(
      "next-other-models-stale-clear-persists",
      !depAfterOtherClear.modelSettings ||
        depAfterOtherClear.modelSettings.length === 0,
      `modelSettings after clear=${JSON.stringify(depAfterOtherClear.modelSettings)}`,
    );

    // ------------------------------------------------------------------
    // Scenario 3C: Classic /settings/models stale stored effort check!
    // ------------------------------------------------------------------
    await api("PUT", "/api/deployment", {
      defaultModels: ["gemini-3.1-pro", "gpt-5.1-codex"],
      modelSettings: [
        { model: "opus-5.5-high", effort: "low" },
        { model: "auto", effort: "high" },
        { model: "claude-opus-5-5", effort: "high" },
      ],
    });
    await page.goto(`${BASE}/settings/models`, { waitUntil: "domcontentloaded" });
    await page.getByTestId("model-opus-5.5-high").waitFor({
      state: "visible",
      timeout: 15000,
    });
    if (SHOT_DIR) {
      await page.screenshot({
        path: path.join(SHOT_DIR, `classic-stale-desktop-light.png`),
        fullPage: true,
      });
    }
    for (const [modelId, expectedHelper] of [
      [
        "opus-5.5-high",
        'Not applied: Effort is part of the model name: high. Choose "Model default" to clear it.',
      ],
      [
        "auto",
        'Not applied: auto picks the model, and its effort, itself. Choose "Model default" to clear it.',
      ],
      [
        "claude-opus-5-5",
        'Not applied: Claude takes no reasoning effort. Choose "Model default" to clear it.',
      ],
    ]) {
      const box = page.getByTestId(`model-${modelId}`);
      const text = (await box.textContent()) ?? "";
      record(
        `classic-stale-${modelId}-not-applied`,
        text.includes("(not applied)") && text.includes(expectedHelper),
        `hasNotApplied=${text.includes("(not applied)")}, hasHelper=${text.includes(expectedHelper)}`,
      );
      // Clear via MUI select -> "Model default", waiting for the real PUT /api/deployment to settle.
      await box.getByRole("combobox", { name: "Reasoning effort" }).click();
      const putDone = page.waitForResponse(
        (res) =>
          res.request().method() === "PUT" &&
          new URL(res.url()).pathname === "/api/deployment",
        { timeout: 10000 },
      );
      await page.getByRole("option", { name: "Model default" }).click();
      await putDone;
      await page.getByTestId(`model-no-effort-${modelId}`).waitFor({
        state: "visible",
        timeout: 10000,
      });
    }
    const depAfterClassicClear = await api("GET", "/api/deployment");
    record(
      "classic-stale-clear-persists",
      !depAfterClassicClear.modelSettings ||
        depAfterClassicClear.modelSettings.length === 0,
      `modelSettings after classic clear=${JSON.stringify(depAfterClassicClear.modelSettings)}`,
    );

    record(
      "scenario-page-errors",
      pageErrors.length === 0,
      `errors=${JSON.stringify(pageErrors)}`,
    );
    await ctx.close();

    // ------------------------------------------------------------------
    // Scenario 4: Responsive layout, alignment, overflow, menu containment,
    // and axe audit at 1440x900 & 390x844 (plus 1024x860 & 2048x1200)
    // in light & dark on /next/settings/models and classic /settings/models
    // ------------------------------------------------------------------
    await api("PUT", "/api/deployment", {
      defaultModels: [
        "gemini-3.1-pro",
        "gpt-5.1-codex",
        "claude-opus-5-5",
        "opus-5.5-high",
      ],
      modelSettings: [
        { model: "gemini-3.1-pro", effort: "high" },
        { model: "gpt-5.1-codex", effort: "xhigh" },
        { model: "auto", effort: "high" },
        { model: "gemini-4-carbon-xhigh", effort: "low" },
      ],
    });

    const viewports = [
      { name: "desktop-1440", width: 1440, height: 900 },
      { name: "phone-390", width: 390, height: 844 },
      { name: "compact-1024", width: 1024, height: 860 },
      { name: "wide-2048", width: 2048, height: 1200 },
    ];
    for (const vp of viewports) {
      for (const theme of ["light", "dark"]) {
        const run = await openContext(browser, {
          width: vp.width,
          height: vp.height,
          theme,
        });
        await run.page.goto(`${BASE}/next/settings/models`, {
          waitUntil: "networkidle",
        });
        const c = run.page.getByTestId("card-model-defaults");
        await c.waitFor({ state: "visible", timeout: 15000 });

        const overflow = await run.page.evaluate(
          () =>
            document.documentElement.scrollWidth -
            document.documentElement.clientWidth,
        );
        const cardBox = await c.boundingBox();
        const fb0Eff = await c
          .getByTestId("fallback-row-0")
          .locator(".mp-chain-effort")
          .boundingBox();
        const fb1Eff = await c
          .getByTestId("fallback-row-1")
          .locator(".mp-chain-effort")
          .boundingBox();
        const fb2Eff = await c
          .getByTestId("fallback-row-2")
          .locator(".mp-chain-effort")
          .boundingBox();
        const colAligned =
          Math.abs(fb0Eff.x - fb1Eff.x) <= 1 &&
          Math.abs(fb1Eff.x - fb2Eff.x) <= 1;

        let phoneWrapped = true;
        if (vp.width <= 600) {
          const modelBox = await c
            .getByTestId("fallback-row-0")
            .locator(".mp-chain-model")
            .boundingBox();
          phoneWrapped =
            fb0Eff.y >= modelBox.y + modelBox.height - 2 &&
            fb0Eff.x + fb0Eff.width <= cardBox.x + cardBox.width + 1;
        }

        // Open effort menu on fallback-0 and verify viewport containment.
        await c.getByTestId("effort-fallback-0").click();
        const menu = run.page.getByTestId("effort-fallback-0-menu");
        await menu.waitFor({ state: "visible", timeout: 5000 });
        const menuBox = await menu.boundingBox();
        const menuContained =
          menuBox.x >= 0 &&
          menuBox.x + menuBox.width <= vp.width + 1 &&
          menuBox.y >= 0 &&
          menuBox.y + menuBox.height <= vp.height + 1;
        await run.page.keyboard.press("Escape");

        // Open model picker on adder and verify viewport containment.
        await c.getByTestId("picker-model-setting-model").click();
        const panel = run.page.getByTestId("picker-model-setting-model-panel");
        await panel.waitFor({ state: "visible", timeout: 5000 });
        const panelBox = await panel.boundingBox();
        const panelContained =
          panelBox.x >= 0 &&
          panelBox.x + panelBox.width <= vp.width + 1;
        await run.page.keyboard.press("Escape");

        const violations = await axeViolations(
          run.page,
          '[data-testid="card-model-defaults"]',
        );

        if (
          SHOT_DIR &&
          (vp.name === "desktop-1440" || vp.name === "phone-390")
        ) {
          await run.page.screenshot({
            path: path.join(SHOT_DIR, `next-${vp.name}-${theme}.png`),
            fullPage: true,
          });
        }

        record(
          `responsive-next-${vp.name}-${theme}`,
          overflow <= 0 &&
            colAligned &&
            phoneWrapped &&
            menuContained &&
            panelContained &&
            violations.length === 0 &&
            run.pageErrors.length === 0,
          `overflow=${overflow}, colAligned=${colAligned} (x=${fb0Eff.x.toFixed(1)},${fb1Eff.x.toFixed(1)},${fb2Eff.x.toFixed(1)}), phoneWrapped=${phoneWrapped}, menuContained=${menuContained} (${JSON.stringify(menuBox)}), panelContained=${panelContained}, axe=${JSON.stringify(violations)}, pageErrors=${JSON.stringify(run.pageErrors)}`,
        );
        if (!menuContained) {
          finding(
            `F2-${vp.name}-${theme}`,
            `g-pill-select menu overflows viewport (${vp.width}x${vp.height}): menuBox=${JSON.stringify(menuBox)}`,
          );
        }
        await run.ctx.close();
      }
    }

    // Classic /settings/models responsive check at 1440 and 390, light and dark
    for (const vp of [
      { name: "desktop-1440", width: 1440, height: 900 },
      { name: "phone-390", width: 390, height: 844 },
    ]) {
      for (const theme of ["light", "dark"]) {
        const run = await openContext(browser, {
          width: vp.width,
          height: vp.height,
          theme,
        });
        await run.page.goto(`${BASE}/settings/models`, {
          waitUntil: "domcontentloaded",
        });
        await run.page.getByTestId("model-auto").waitFor({
          state: "visible",
          timeout: 15000,
        });
        const overflow = await run.page.evaluate(
          () =>
            document.documentElement.scrollWidth -
            document.documentElement.clientWidth,
        );
        const violations = await axeViolations(run.page);
        if (SHOT_DIR) {
          await run.page.screenshot({
            path: path.join(SHOT_DIR, `classic-${vp.name}-${theme}.png`),
            fullPage: true,
          });
        }
        record(
          `responsive-classic-${vp.name}-${theme}`,
          overflow <= 0 &&
            violations.length === 0 &&
            run.pageErrors.length === 0,
          `overflow=${overflow}, axe=${JSON.stringify(violations)}, pageErrors=${JSON.stringify(run.pageErrors)}`,
        );
        await run.ctx.close();
      }
    }
  } finally {
    await browser.close();
  }

  if (SHOT_DIR) {
    fs.writeFileSync(
      path.join(SHOT_DIR, "ui-results.json"),
      JSON.stringify(results, null, 2),
    );
  }
  const failed = results.checks.filter((c) => !c.ok);
  console.log(
    `[${DB_KIND}] UI summary: ${results.checks.length - failed.length}/${results.checks.length} passed, ${results.findings.length} finding(s)`,
  );
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
