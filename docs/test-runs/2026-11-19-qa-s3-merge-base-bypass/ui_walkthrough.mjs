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

// Playwright browser & accessibility walkthrough of /next/task/<id> Changes & CI
// tab and <g-bypass-s3-dialog> against the live grain daemon (Item 4a & Item 5
// of docs/test-plans/2026-11-19.md §3).

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(
  path.resolve(process.cwd(), "ui-ng/package.json"),
);
const { chromium } = require("@playwright/test");
const { default: AxeBuilder } = require("@axe-core/playwright");

const stateFile = process.argv[2];
const outDir = process.argv[3];
const mode = process.argv[4] || "all"; // "inspect" (before fix) or "all" (full + happy bypass)

if (!stateFile || !outDir) {
  console.error("Usage: node ui_walkthrough.mjs <ui-state.json> <out-dir> [inspect|all]");
  process.exit(1);
}

const state = JSON.parse(fs.readFileSync(stateFile, "utf8"));
fs.mkdirSync(outDir, { recursive: true });

const results = {
  chromiumVersion: "",
  viewports: [],
  staleHead409: null,
  happyBypass: null,
  qaDiagnosticViews: null,
  failures: [],
};

function recordFail(msg) {
  results.failures.push(msg);
  console.error("  [UI-FAIL]", msg);
}

function recordPass(msg) {
  console.log("  [UI-PASS]", msg);
}

async function getAuthCookies() {
  const res = await fetch(`${state.daemon}/api/auth/access-code`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code: state.code, name: "alice@example.com" }),
  });
  if (!res.ok) {
    throw new Error(`sign-in failed: ${res.status} ${await res.text()}`);
  }
  const { hostname } = new URL(state.daemon);
  return res.headers.getSetCookie().map((header) => {
    const [pair] = header.split(";");
    const at = pair.indexOf("=");
    return {
      name: pair.slice(0, at).trim(),
      value: pair.slice(at + 1).trim(),
      domain: hostname,
      path: "/",
      expires: -1,
      httpOnly: true,
    };
  });
}

async function signIn(ctx) {
  await ctx.addCookies(await getAuthCookies());
}

async function openChangesTab(page, taskId) {
  await page.goto(`${state.daemon}/next/task/${taskId}?tab=changes`, {
    waitUntil: "networkidle",
  });
  const changesTab = page.getByRole("tab", { name: /Changes/i });
  if (await changesTab.isVisible({ timeout: 2000 }).catch(() => false)) {
    await changesTab.click();
  }
  await page.getByTestId("task-qa-section").waitFor({ state: "visible", timeout: 15000 });
}

function pushSecondCommitToStage(bareRepo, stageBranch) {
  const tmp = fs.mkdtempSync("/tmp/ui-stale-");
  try {
    execFileSync("git", ["-C", tmp, "init", "-q"]);
    execFileSync("git", ["-C", tmp, "remote", "add", "origin", bareRepo]);
    execFileSync("git", ["-C", tmp, "fetch", "-q", "origin", stageBranch]);
    execFileSync("git", ["-C", tmp, "checkout", "-q", "-B", "work", "FETCH_HEAD"]);
    fs.mkdirSync(path.join(tmp, "pkg/bypass"), { recursive: true });
    fs.writeFileSync(
      path.join(tmp, "pkg/bypass/stale_second.go"),
      `package bypass\n// stamp ${Date.now()}\nfunc StaleSecond() {}\n`,
    );
    execFileSync("git", ["-C", tmp, "add", "pkg/bypass/stale_second.go"]);
    execFileSync("git", [
      "-C",
      tmp,
      "-c",
      "user.name=Walk Agent",
      "-c",
      "user.email=agent@example.com",
      "commit",
      "-qm",
      "feat: second commit while bypass dialog open",
    ]);
    const sha = execFileSync("git", ["-C", tmp, "rev-parse", "HEAD"], {
      encoding: "utf8",
    }).trim();
    execFileSync("git", [
      "-C",
      tmp,
      "push",
      "-q",
      "-f",
      "origin",
      `HEAD:refs/heads/${stageBranch}`,
    ]);
    return sha;
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

const browser = await chromium.launch({ headless: true });
results.chromiumVersion = browser.version();
console.log("Chromium version:", results.chromiumVersion);

try {
  // 1. Sweep 1440x900 & 390x844 in light and dark on blocked task T4.
  const combos = [
    { name: "desktop-light", width: 1440, height: 900, scheme: "light" },
    { name: "desktop-dark", width: 1440, height: 900, scheme: "dark" },
    { name: "phone-light", width: 390, height: 844, scheme: "light" },
    { name: "phone-dark", width: 390, height: 844, scheme: "dark" },
  ];

  for (const combo of combos) {
    const ctx = await browser.newContext({
      viewport: { width: combo.width, height: combo.height },
      colorScheme: combo.scheme,
    });
    const page = await ctx.newPage();
    await signIn(ctx);
    await openChangesTab(page, state.t4BlockedId);

    const qaSection = page.getByTestId("task-qa-section");
    const blockedBar = page.getByTestId("qa-s3-blocked-bar");
    const bypassBtn = page.getByTestId("qa-bypass-s3-btn");
    await blockedBar.waitFor({ state: "visible", timeout: 10000 });

    const pageOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    const axeChanges = await new AxeBuilder({ page })
      .include('[data-testid="task-qa-section"]')
      .analyze();

    await page.screenshot({
      path: path.join(outDir, `changes-${combo.name}.png`),
      fullPage: false,
    });

    // Click Bypass S3 button to open <g-bypass-s3-dialog>
    await bypassBtn.click();
    const dialog = page.getByTestId("bypass-s3-dialog");
    await dialog.waitFor({ state: "visible", timeout: 5000 });

    // Check initial focus on [data-testid="bypass-s3-reason"]
    const activeTestId = await page.evaluate(
      () => document.activeElement?.getAttribute("data-testid") || document.activeElement?.tagName,
    );
    // Check staging branch & 12-char SHA display
    const dialogText = await dialog.innerText();
    const hasStageBranch = dialogText.includes(`grain-qa-stage/${state.t4BlockedId}`);
    const has12CharSha = /commit [0-9a-f]{12}/.test(dialogText);

    // Check submit disabled while blank AND while whitespace-only
    const confirmBtn = page.getByTestId("bypass-s3-confirm");
    const disabledBlank = await confirmBtn.isDisabled();
    const reasonEl = page.getByTestId("bypass-s3-reason");
    await reasonEl.fill("   \n\t  ");
    const disabledWhitespace = await confirmBtn.isDisabled();
    await reasonEl.fill("");

    // Bounding box containment in viewport
    const box = await dialog.boundingBox();
    const fitsViewport =
      !!box &&
      box.x >= -1 &&
      box.y >= -1 &&
      box.x + box.width <= combo.width + 1 &&
      box.y + box.height <= combo.height + 1;

    const dialogOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    const axeDialog = await new AxeBuilder({ page })
      .include('[data-testid="bypass-s3-dialog"]')
      .analyze();

    await page.screenshot({
      path: path.join(outDir, `dialog-${combo.name}.png`),
      fullPage: false,
    });

    // Escape dismissal & focus return
    await page.keyboard.press("Escape");
    const closedOnEscape = await dialog
      .waitFor({ state: "detached", timeout: 3000 })
      .then(() => true)
      .catch(() => false);
    await page.waitForTimeout(100);
    const focusAfterEscape = await page.evaluate(
      () => document.activeElement?.getAttribute("data-testid") || document.activeElement?.tagName,
    );

    // Cancel button dismissal
    await bypassBtn.click();
    await dialog.waitFor({ state: "visible", timeout: 5000 });
    await page.getByTestId("bypass-s3-cancel").click();
    const closedOnCancel = await dialog
      .waitFor({ state: "detached", timeout: 3000 })
      .then(() => true)
      .catch(() => false);

    const rec = {
      ...combo,
      pageOverflow,
      dialogOverflow,
      activeTestId,
      hasStageBranch,
      has12CharSha,
      disabledBlank,
      disabledWhitespace,
      fitsViewport,
      box,
      closedOnEscape,
      focusAfterEscape,
      closedOnCancel,
      axeChangesViolations: axeChanges.violations.map((v) => ({ id: v.id, impact: v.impact, help: v.help })),
      axeDialogViolations: axeDialog.violations.map((v) => ({ id: v.id, impact: v.impact, help: v.help })),
    };
    results.viewports.push(rec);

    if (activeTestId !== "bypass-s3-reason") recordFail(`${combo.name}: initial focus was ${activeTestId}, want bypass-s3-reason`);
    else recordPass(`${combo.name}: initial focus on bypass-s3-reason`);
    if (!hasStageBranch || !has12CharSha) recordFail(`${combo.name}: missing stageBranch (${hasStageBranch}) or 12-char SHA (${has12CharSha})`);
    else recordPass(`${combo.name}: shows staging branch + 12-char commit SHA`);
    if (!disabledBlank || !disabledWhitespace) recordFail(`${combo.name}: confirm not disabled for blank (${disabledBlank}) / whitespace (${disabledWhitespace})`);
    else recordPass(`${combo.name}: submit disabled while blank and whitespace`);
    if (!fitsViewport || pageOverflow > 0 || dialogOverflow > 0) recordFail(`${combo.name}: overflow/fit failure pageOverflow=${pageOverflow} dialogOverflow=${dialogOverflow} fits=${fitsViewport}`);
    else recordPass(`${combo.name}: zero horizontal overflow and dialog fits viewport`);
    if (rec.axeChangesViolations.length > 0 || rec.axeDialogViolations.length > 0) {
      recordFail(`${combo.name}: axe violations changes=${JSON.stringify(rec.axeChangesViolations)} dialog=${JSON.stringify(rec.axeDialogViolations)}`);
    } else {
      recordPass(`${combo.name}: zero axe violations on Changes QA section and dialog`);
    }
    if (!closedOnEscape || !closedOnCancel) recordFail(`${combo.name}: dismissal failed escape=${closedOnEscape} cancel=${closedOnCancel}`);
    else recordPass(`${combo.name}: Escape and Cancel dismiss dialog (focus returned to ${focusAfterEscape})`);

    await ctx.close();
  }

  // 2. Stale-head 409 error feedback check on T8 (real push while dialog is open!)
  {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: "light" });
    const page = await ctx.newPage();
    await signIn(ctx);
    await openChangesTab(page, state.t8StaleId);
    await page.getByTestId("qa-bypass-s3-btn").click();
    const dialog = page.getByTestId("bypass-s3-dialog");
    await dialog.waitFor({ state: "visible" });

    // Push a second commit onto grain-qa-stage/<T8> AFTER dialog opened!
    const newSha = pushSecondCommitToStage(state.bareRepo, state.t8StageBranch);
    const typedReason = "Reviewed initial commit only; testing stale-head 409 feedback";
    await page.getByTestId("bypass-s3-reason").fill(typedReason);

    const [resp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes(`/api/tasks/${state.t8StaleId}/qa/bypass-s3`)),
      page.getByTestId("bypass-s3-confirm").click(),
    ]);
    await page.waitForTimeout(300);

    const status = resp.status();
    const respBody = await resp.text();
    const stillOpen = await dialog.isVisible();
    const keptReason = stillOpen ? await page.getByTestId("bypass-s3-reason").inputValue() : "";
    const dialogTextAfter409 = stillOpen ? await dialog.innerText() : "";
    const errorElInDialog = await dialog.locator('[role="alert"], [data-testid="bypass-s3-error"]').count();
    const errorVisibleInDialog = errorElInDialog > 0 && await dialog.locator('[role="alert"], [data-testid="bypass-s3-error"]').first().isVisible();
    const bgActionErrorVisible = await page.getByTestId("task-action-error").isVisible().catch(() => false);
    const bgActionErrorInsideInert = await page.evaluate(
      () => !!document.querySelector('[inert] [data-testid="task-action-error"]'),
    );

    await page.screenshot({
      path: path.join(outDir, `stale-409-desktop-light.png`),
      fullPage: false,
    });

    results.staleHead409 = {
      newSha,
      status,
      respBody,
      stillOpen,
      reasonIntact: keptReason === typedReason,
      errorVisibleInDialog,
      dialogTextHasError: /moved|refresh|not|conflict|409/i.test(dialogTextAfter409),
      bgActionErrorVisible,
      bgActionErrorInsideInert,
    };

    if (status !== 409) recordFail(`stale-head: expected HTTP 409, got ${status}: ${respBody}`);
    else recordPass(`stale-head: live daemon returned HTTP 409 Conflict`);
    if (!stillOpen || keptReason !== typedReason) recordFail(`stale-head: dialog closed or lost typed reason (open=${stillOpen}, reason=${keptReason})`);
    else recordPass(`stale-head: dialog stays open keeping typed reason intact`);
    if (!errorVisibleInDialog) {
      recordFail(`stale-head: 409 error is NOT displayed inside <g-bypass-s3-dialog> (bgActionErrorVisible=${bgActionErrorVisible}, bgActionErrorInsideInert=${bgActionErrorInsideInert})`);
    } else {
      recordPass(`stale-head: 409 error feedback visibly rendered inside <g-bypass-s3-dialog>`);
    }

    // Also inspect T2 (qa:base-resolution), T3 (warn-only), and T6 (GitHub Actions CI) in browser
    await page.keyboard.press("Escape");
    await openChangesTab(page, state.t2DeletedBaseId);
    const t2Text = await page.getByTestId("task-qa-section").innerText();
    await page.screenshot({ path: path.join(outDir, `t2-base-resolution-desktop-light.png`) });
    await openChangesTab(page, state.t3WarnId);
    const t3Text = await page.getByTestId("task-qa-section").innerText();
    await page.screenshot({ path: path.join(outDir, `t3-warn-only-desktop-light.png`) });
    await openChangesTab(page, state.t6ActionsId);
    await page.waitForTimeout(500);
    const t6ChangesText = await page.locator(".changes-tab").innerText();
    const t6HasActionsRun = (await page.locator('[data-testid^="ci-run-gha:"]').count()) > 0;
    await page.screenshot({ path: path.join(outDir, `t6-actions-ci-desktop-light.png`) });
    results.qaDiagnosticViews = {
      t2HasBaseResolution: t2Text.includes("qa:base-resolution"),
      t3HasStrictDecoderWarn: t3Text.includes("golden:strict-config-decoder") && t3Text.includes("warn"),
      t6HasActionsRun,
      t6ChangesExcerpt: t6ChangesText.slice(0, 500),
    };
    if (!results.qaDiagnosticViews.t2HasBaseResolution) recordFail("T2 Changes tab missing qa:base-resolution");
    else recordPass("T2 Changes tab displays qa:base-resolution diagnostic check run");
    if (!results.qaDiagnosticViews.t3HasStrictDecoderWarn) recordFail("T3 Changes tab missing golden:strict-config-decoder warn");
    else recordPass("T3 Changes tab displays golden:strict-config-decoder warn");
    if (!t6HasActionsRun) recordFail("T6 Changes & CI tab failed to list GitHub Actions runs (missing provider=github-actions)");
    else recordPass("T6 Changes & CI tab displays GitHub Actions runs");

    await ctx.close();
  }

  // 3. Item 4a: Live Happy Path bypass via <g-bypass-s3-dialog> on T7 (when mode === "all")
  if (mode === "all") {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: "light" });
    const page = await ctx.newPage();
    await signIn(ctx);
    await openChangesTab(page, state.t7HappyId);
    await page.getByTestId("qa-bypass-s3-btn").click();
    const dialog = page.getByTestId("bypass-s3-dialog");
    await dialog.waitFor({ state: "visible" });
    const reason = "Verified diff manually in Playwright walkthrough; overriding S3 test-integrity-guard";
    await page.getByTestId("bypass-s3-reason").fill(reason);
    const [resp] = await Promise.all([
      page.waitForResponse((r) => r.url().includes(`/api/tasks/${state.t7HappyId}/qa/bypass-s3`)),
      page.getByTestId("bypass-s3-confirm").click(),
    ]);
    const status = resp.status();
    const body = await resp.json().catch(() => ({}));
    await page.waitForTimeout(600);
    const dialogClosed = (await dialog.count()) === 0;
    const barGone = (await page.getByTestId("qa-s3-blocked-bar").count()) === 0;
    const qaText = await page.getByTestId("task-qa-section").innerText();
    await page.screenshot({ path: path.join(outDir, `t7-after-bypass-desktop-light.png`) });

    // Query live daemon API to verify all post-bypass invariants
    const t7After = await (await fetch(`${state.daemon}/api/tasks/${state.t7HappyId}`, {
      headers: { Authorization: `Bearer ${state.code}` },
    })).json();
    const t7QaAfter = await (await fetch(`${state.daemon}/api/tasks/${state.t7HappyId}/qa`, {
      headers: { Authorization: `Bearer ${state.code}` },
    })).json();
    const branchesAfter = await (await fetch(`${state.ghsim}/_sim/branches`)).json();

    const autoMergeOffTransition = (t7After.transitions || []).find(
      (tr) => tr.state === "auto_merge_off" && (tr.reason || "").includes("bypassed"),
    );
    const hasApprovalGate = (t7After.mergeGates || []).some(
      (g) => g.id === "approval" && g.state === "needed",
    );
    const hasS5MergeTriage = (t7QaAfter.checkRuns || []).some(
      (r) => r.stage === "S5" && r.checkId === "qa:merge-triage",
    );
    results.happyBypass = {
      status,
      prNumber: body.number,
      dialogClosed,
      barGone,
      uiShowsOperatorBypass: qaText.includes("qa:s3-operator-bypass") && qaText.includes("bypassed"),
      autoMerge: t7After.autoMerge,
      autoMergeOffTransition: autoMergeOffTransition || null,
      triageDecision: t7After.qaSummary?.triageDecision || null,
      hasApprovalGate,
      hasS5MergeTriage,
      pullRequest: t7After.pullRequest,
      s3Blocked: !!t7QaAfter.s3Blocked,
      stageBranchDeleted: !Object.hasOwn(branchesAfter, `grain-qa-stage/${state.t7HappyId}`),
      prBranchCreated: Object.hasOwn(branchesAfter, t7After.branch),
      auditCheckRun: (t7QaAfter.checkRuns || []).find((r) => r.checkId === "qa:s3-operator-bypass"),
      lastComment: (t7After.comments || []).slice(-1)[0]?.body || "",
      mergeGates: t7After.mergeGates || [],
    };

    if (status !== 200 || !body.number) recordFail(`happy-bypass: status=${status} body=${JSON.stringify(body)}`);
    else recordPass(`happy-bypass: dialog promoted staging branch and opened PR #${body.number}`);
    if (t7After.autoMerge !== false || !autoMergeOffTransition) {
      recordFail(`happy-bypass: autoMerge withdrawal missing (autoMerge=${t7After.autoMerge}, transition=${JSON.stringify(autoMergeOffTransition)})`);
    } else {
      recordPass(`happy-bypass: autoMerge withdrawn with auto_merge_off audit transition`);
    }
    if (t7After.qaSummary?.triageDecision !== "require_human" || !hasApprovalGate || !hasS5MergeTriage) {
      recordFail(`happy-bypass: S5 triage / human approval gating not preserved (triage=${t7After.qaSummary?.triageDecision}, approvalGate=${hasApprovalGate}, s5Triage=${hasS5MergeTriage})`);
    } else {
      recordPass(`happy-bypass: S5 triage (require_human + qa:merge-triage) and approval gate preserved`);
    }
    if (!results.happyBypass.stageBranchDeleted || !results.happyBypass.prBranchCreated) {
      recordFail(`happy-bypass: branch state wrong: ${JSON.stringify(branchesAfter)}`);
    } else {
      recordPass(`happy-bypass: grain-qa-stage/${state.t7HappyId} promoted to ${t7After.branch} and deleted`);
    }
    if (!results.happyBypass.uiShowsOperatorBypass || !dialogClosed || !barGone) {
      recordFail(`happy-bypass: UI did not update cleanly (dialogClosed=${dialogClosed}, barGone=${barGone}, showsBypass=${results.happyBypass.uiShowsOperatorBypass})`);
    } else {
      recordPass(`happy-bypass: UI closed dialog, hid blocked bar, and rendered qa:s3-operator-bypass bypassed`);
    }

    await ctx.close();
  }
} finally {
  await browser.close();
}

fs.writeFileSync(path.join(outDir, "ui-results.json"), JSON.stringify(results, null, 2));
console.log("\nUI Walkthrough Summary:", results.failures.length, "failures");
if (results.failures.length > 0) {
  process.exit(1);
}
