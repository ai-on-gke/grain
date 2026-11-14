#!/usr/bin/env node
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

// Walkthrough driver for docs/test-plans/2026-11-13.md §1 items 1, 2, 5, and 6
// (plus real-daemon CLI catalog verification for item 4) over a real
// `grain daemon -sandbox-port-base=0` (SQLite) started and seeded by env.sh.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import { createRequire } from "node:module";

const requireNg = createRequire(
  new URL("../../../ui-ng/package.json", import.meta.url),
);
const { chromium } = requireNg("playwright-core");

const seededPath = process.argv[2] || "/tmp/walk-next-ui/seeded.json";
const seeded = JSON.parse(fs.readFileSync(seededPath, "utf8"));

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

async function gotoNext(page, url) {
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await page.waitForSelector(".main-stage > *:not(router-outlet)", {
    timeout: 15000,
  });
  await sleep(350);
}

async function gotoClassic(page, url) {
  await page.goto(url, { waitUntil: "domcontentloaded" });
  await page.waitForSelector("body", { timeout: 15000 });
  await sleep(600);
}
const baseUrl = seeded.iapProxyUrl;
const results = [];
function record(id, ok, detail) {
  results.push({ id, ok: Boolean(ok), detail });
  const tag = ok ? "PASS" : "FAIL";
  console.log(`[${tag}] ${id}: ${detail}`);
}

async function apiJson(baseUrl, path, opts = {}) {
  const res = await fetch(`${baseUrl}${path}`, opts);
  const text = await res.text();
  let body = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    body = text;
  }
  return { status: res.status, body };
}

async function openSignedInDaemonContext(browser, viewport = { width: 1440, height: 900 }) {
  return openIapContext(browser, "alice@example.com", viewport);
}

async function openIapContext(browser, email, viewport = { width: 1440, height: 900 }) {
  const context = await browser.newContext({ viewport });
  await context.addCookies([
    {
      name: "iapsim_email",
      value: email,
      url: seeded.iapProxyUrl,
    },
  ]);
  const page = await context.newPage();
  return { context, page };
}

async function readPillOptions(page, testid, menuTestid = `${testid}-menu`) {
  const btn = page.getByTestId(testid);
  if ((await btn.count()) === 0) return null;
  await btn.click();
  const menu = page.getByTestId(menuTestid);
  await menu.waitFor({ state: "visible", timeout: 5000 });
  const opts = await menu
    .locator('[role="option"]')
    .evaluateAll((els) => els.map((e) => (e.textContent || "").trim()));
  await page.keyboard.press("Escape");
  return opts;
}

const browser = await chromium.launch({ headless: true });
try {
  // =========================================================================
  // ITEM 1: Every rail destination over a real daemon, classic parity & deep links
  // =========================================================================
  {
    const { context, page } = await openSignedInDaemonContext(browser);
    const consoleErrors = [];
    const pageErrors = [];
    page.on("console", (m) => {
      if (m.type() === "error") consoleErrors.push(`${page.url()}: ${m.text()}`);
    });
    page.on("pageerror", (e) => pageErrors.push(`${page.url()}: ${e.message}`));

    const destinations = [
      "/next/inbox",
      "/next/workstreams",
      "/next/tasks",
      "/next/agents",
      "/next/sessions",
      "/next/ci",
      "/next/processes",
      "/next/telemetry",
      "/next/context",
      "/next/fleet",
      "/next/settings",
    ];
    for (const dest of destinations) {
      await gotoNext(page, `${baseUrl}${dest}`);
    }
    record(
      "item1.rail_destinations_clean",
      consoleErrors.length === 0 && pageErrors.length === 0,
      `visited ${destinations.length} /next/ destinations; consoleErrors=${consoleErrors.length}, pageErrors=${pageErrors.length}` +
        (consoleErrors.length ? ` (${consoleErrors.join(" | ")})` : ""),
    );

    // Compare first-list counts against API / classic UI
    const authHeaders = { Authorization: `Bearer ${seeded.accessCode}` };
    const apiTasks = (await apiJson(seeded.daemonUrl, "/api/tasks", { headers: authHeaders })).body || [];
    const apiSchedules = (await apiJson(seeded.daemonUrl, "/api/schedules", { headers: authHeaders })).body || [];
    const apiRunsRes = (await apiJson(seeded.daemonUrl, "/api/ci/runs", { headers: authHeaders })).body;
    const apiRuns = Array.isArray(apiRunsRes) ? apiRunsRes : apiRunsRes?.runs || [];

    await gotoNext(page, `${baseUrl}/next/tasks`);
    const nextTaskRows = await page.locator('[data-testid^="open-"]').count();
    await gotoClassic(page, `${baseUrl}/tasks`);
    const classicBodyText = await page.locator("body").innerText();
    const classicHasSeeded =
      classicBodyText.includes("Seeded proposal on widgets") &&
      classicBodyText.includes("Seeded queued task on api");

    await gotoNext(page, `${baseUrl}/next/processes`);
    const nextProcText = await page.locator("main").innerText();
    await gotoClassic(page, `${baseUrl}/processes`);
    const classicProcText = await page.locator("body").innerText();
    const procParity =
      nextProcText.includes("Nightly widgets check") &&
      classicProcText.includes("Nightly widgets check") &&
      apiSchedules.length >= 1;

    await gotoNext(page, `${baseUrl}/next/ci`);
    const nextCiText = await page.locator("main").innerText();
    await gotoClassic(page, `${baseUrl}/ci`);
    const classicCiText = await page.locator("body").innerText();
    const ciParity =
      nextCiText.includes("acme/widgets") &&
      classicCiText.includes("acme/widgets") &&
      apiRuns.length >= 1;

    await gotoNext(page, `${baseUrl}/next/fleet`);
    const nextFleetText = await page.locator("main").innerText();
    await gotoClassic(page, `${baseUrl}/fleet`);
    const classicFleetText = await page.locator("body").innerText();
    const fleetParity = nextFleetText.length > 20 && classicFleetText.length > 20;

    record(
      "item1.first_list_parity",
      nextTaskRows >= 4 && classicHasSeeded && procParity && ciParity && fleetParity,
      `apiTasks=${apiTasks.length}, nextTaskRows=${nextTaskRows}, classicHasSeeded=${classicHasSeeded}, procParity=${procParity}, ciParity=${ciParity} (runs=${apiRuns.length}), fleetParity=${fleetParity}`,
    );

    // Deep-link reload: /next/tasks/<id> and /next/settings/repositories
    await gotoNext(page, `${baseUrl}/next/tasks/${seeded.proposalId}`);
    await page.waitForSelector("#task-title");
    const titleBefore = (await page.locator("#task-title").textContent())?.trim() || "";
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.waitForSelector("#task-title");
    await sleep(300);
    const titleAfter = (await page.locator("#task-title").textContent())?.trim() || "";

    await gotoNext(page, `${baseUrl}/next/settings/repositories`);
    await page.waitForSelector("#settings-stage-heading");
    const secBefore = (await page.locator("#settings-stage-heading").textContent())?.trim() || "";
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.waitForSelector("#settings-stage-heading");
    await sleep(300);
    const secAfter = (await page.locator("#settings-stage-heading").textContent())?.trim() || "";

    record(
      "item1.deep_link_reload",
      titleBefore.includes("Seeded proposal on widgets") &&
        titleBefore === titleAfter &&
        secBefore === "Repositories" &&
        secAfter === "Repositories",
      `taskTitle="${titleBefore}" -> "${titleAfter}", settingsHeading="${secBefore}" -> "${secAfter}"`,
    );

    // Real-daemon CLI catalog check for Item 4 (fake-agy.py advertises 4 models, 2 configured)
    const agentModelsRes = (
      await apiJson(seeded.daemonUrl, "/api/agent-models", { headers: authHeaders })
    ).body;
    await gotoNext(page, `${baseUrl}/next/sessions/director`);
    await sleep(400);
    const sessionModelOpts = (await readPillOptions(page, "chat-head-model")) || [];
    const leakedUnconfigured = sessionModelOpts.filter((o) =>
      /unconfigured/i.test(o),
    );
    record(
      "item4.daemon_model_picker_configured_only",
      Boolean(agentModelsRes?.available) &&
        (agentModelsRes?.models?.length || 0) === 4 &&
        sessionModelOpts.length === 3 &&
        leakedUnconfigured.length === 0,
      `agentModelsCatalog=${JSON.stringify((agentModelsRes?.models || []).map((m) => m.id))}, pickerOptions=${JSON.stringify(sessionModelOpts)}`,
    );

    await context.close();
  }

  // =========================================================================
  // ITEM 2: Role gating (viewer + contributor + personal token) & CSRF
  // =========================================================================
  {
    const { context, page } = await openIapContext(browser, "viewer@example.com");
    // Deep-link directly to /next/tasks/<proposalId> first to verify RBAC loads on deep links!
    await gotoNext(page, `${seeded.iapProxyUrl}/next/tasks/${seeded.proposalId}`);
    await page.waitForSelector("#task-title");

    const railNewDisabled = await page.getByTestId("rail-new-task").isDisabled();
    const approveCount = await page.locator('[data-testid="act-approve"]').count();
    const editCount = await page.locator('[data-testid="act-edit"]').count();
    const submitCount = await page.locator('[data-testid="act-submit"]').count();

    // Check Create menu items as viewer
    await page.getByTestId("rail-create-menu").click();
    const createNewTaskBtn = page.getByTestId("create-new-task");
    const createMenuDisabled =
      (await createNewTaskBtn.count()) === 0 || (await createNewTaskBtn.isDisabled());
    await page.keyboard.press("Escape");

    // Check shortcut 'c' does NOT open New Task sheet for viewer
    await page.keyboard.press("c");
    const sheetOpenedForViewer = (await page.getByTestId("new-task-sheet").count()) > 0;

    // Check 390px phone FAB disabled for viewer on /next/inbox
    await page.setViewportSize({ width: 390, height: 844 });
    await gotoNext(page, `${seeded.iapProxyUrl}/next/inbox`);
    const phoneFab = page.getByTestId("phone-new-task");
    const phoneNewDisabled =
      (await phoneFab.count()) === 0 || (await phoneFab.isDisabled());

    // Check Settings saves disabled for viewer
    await page.setViewportSize({ width: 1440, height: 900 });
    await gotoNext(page, `${seeded.iapProxyUrl}/next/settings/tasks`);
    const qaSave = page.locator('[data-testid="qa-save-btn"]');
    const qaSaveBlocked =
      (await qaSave.count()) === 0 || (await qaSave.isDisabled());

    record(
      "item2.viewer_mutating_controls_gated",
      railNewDisabled &&
        phoneNewDisabled &&
        createMenuDisabled &&
        !sheetOpenedForViewer &&
        approveCount === 0 &&
        editCount === 0 &&
        submitCount === 0 &&
        qaSaveBlocked,
      `railNewDisabled=${railNewDisabled}, phoneNewDisabled=${phoneNewDisabled}, createMenuDisabled=${createMenuDisabled}, sheetOpenedForViewer=${sheetOpenedForViewer}, approveCount=${approveCount}, editCount=${editCount}, submitCount=${submitCount}, qaSaveBlocked=${qaSaveBlocked}`,
    );

    // Forced POSTs as viewer must return 403
    const viewerForcedCreate = await page.evaluate(async () => {
      const r = await fetch("/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: "Viewer forced task",
          description: "should 403",
          repo: "acme/widgets",
        }),
      });
      return r.status;
    });
    const viewerForcedApprove = await page.evaluate(async (id) => {
      const r = await fetch(`/api/tasks/${id}/approve`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      });
      return r.status;
    }, seeded.proposalId);
    const viewerForcedSettings = await page.evaluate(async () => {
      const r = await fetch("/api/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ maxWorkers: 2 }),
      });
      return r.status;
    });
    await context.close();

    // Limited personal token forced POST + Contributor role check + Cross-origin CSRF check
    const tokenForced = await apiJson(seeded.daemonUrl, "/api/tasks", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${seeded.limitedToken}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        title: "Token forced",
        description: "should 403",
        repo: "acme/widgets",
      }),
    });

    const { context: contribCtx, page: contribPage } = await openIapContext(
      browser,
      "contributor@example.com",
    );
    await gotoNext(
      contribPage,
      `${seeded.iapProxyUrl}/next/tasks/${seeded.proposalId}`,
    );
    const contribCanNewTask = !(await contribPage
      .getByTestId("rail-new-task")
      .isDisabled());
    const contribApproveHidden =
      (await contribPage.locator('[data-testid="act-approve"]').count()) === 0;
    const contribForcedApprove = await contribPage.evaluate(async (id) => {
      const r = await fetch(`/api/tasks/${id}/approve`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      });
      return r.status;
    }, seeded.proposalId);
    await contribCtx.close();

    const csrfRes = await fetch(`${seeded.iapProxyUrl}/api/tasks`, {
      method: "POST",
      headers: {
        "X-Iapsim-Email": "alice@example.com",
        Origin: "https://evil.example",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        title: "CSRF forced",
        description: "should 403",
        repo: "acme/widgets",
      }),
    });

    record(
      "item2.forced_post_403_and_csrf",
      viewerForcedCreate === 403 &&
        viewerForcedApprove === 403 &&
        viewerForcedSettings === 403 &&
        tokenForced.status === 403 &&
        contribCanNewTask &&
        contribApproveHidden &&
        contribForcedApprove === 403 &&
        csrfRes.status === 403,
      `viewerPOST=${viewerForcedCreate}/${viewerForcedApprove}/${viewerForcedSettings}, tokenPOST=${tokenForced.status}, contribNewTask=${contribCanNewTask}, contribApproveHidden=${contribApproveHidden} (${contribForcedApprove}), csrfStatus=${csrfRes.status}`,
    );
  }

  // =========================================================================
  // ITEM 5: Repo scoping (5 connected repos vs 2 configured instance repos)
  // =========================================================================
  {
    const { context, page } = await openSignedInDaemonContext(browser);
    await gotoNext(page, `${baseUrl}/next/inbox`);

    // 1. New Task operational repo picker
    await page.getByTestId("rail-new-task").click();
    await page.getByTestId("new-task-sheet").waitFor({ state: "visible" });
    await sleep(300);
    if ((await page.getByTestId("nt-repo").count()) === 0) {
      await page.getByTestId("more-options").click();
      await sleep(150);
    }
    const ntRepoOpts = ((await readPillOptions(page, "nt-repo")) || []).filter(
      (r) => !/no repository/i.test(r) && r !== "grain/config",
    );
    await page.getByTestId("new-task-close").click();

    // 2. CI operational repo picker
    await gotoNext(page, `${baseUrl}/next/ci`);
    const ciRepoOptsRaw =
      (await readPillOptions(page, "ci-repo-pill", "ci-repo-menu")) || [];
    const ciRepoOpts = ciRepoOptsRaw.filter((r) => r !== "All repositories");

    // 3. Schedules operational repo picker
    await gotoNext(page, `${baseUrl}/next/processes/schedules`);
    await page.getByTestId("proc-new-schedule-btn").click();
    await page.getByTestId("schedule-dialog").waitFor({ state: "visible" });
    const schedRepoOptsRaw = await readPillOptions(page, "sched-repo-select");
    const schedRepoOpts = (schedRepoOptsRaw || []).filter(
      (r) => !/choose|custom/i.test(r),
    );
    await page.keyboard.press("Escape");

    // 4. Settings -> Repositories shows all 5 connected repos
    await gotoNext(page, `${baseUrl}/next/settings/repositories`);
    await sleep(400);
    const settingsReachableOptsRaw = await readPillOptions(
      page,
      "select-reachable-repo",
    );
    const settingsReachableOpts = (settingsReachableOptsRaw || []).filter(
      (r) => r.startsWith("acme/"),
    );

    const onlyTwo = (arr) =>
      arr.length === 2 &&
      arr.includes("acme/widgets") &&
      arr.includes("acme/api") &&
      !arr.some((x) => /docs|infra|mobile/.test(x));

    record(
      "item5.operational_pickers_two_repos",
      onlyTwo(ntRepoOpts) && onlyTwo(ciRepoOpts) && onlyTwo(schedRepoOpts),
      `ntRepo=${JSON.stringify(ntRepoOpts)}, ciRepo=${JSON.stringify(ciRepoOpts)}, schedRepo=${JSON.stringify(schedRepoOptsRaw)}`,
    );
    record(
      "item5.settings_repositories_all_five",
      settingsReachableOpts.length === 5 &&
        ["acme/api", "acme/docs", "acme/infra", "acme/mobile", "acme/widgets"].every(
          (r) => settingsReachableOpts.includes(r),
        ),
      `settingsReachable=${JSON.stringify(settingsReachableOptsRaw)}`,
    );

    await context.close();
  }

  // =========================================================================
  // ITEM 6: Found in release (v3.6.0 stored in task_link, malformed refused under field, absent from classic)
  // =========================================================================
  {
    const { context, page } = await openSignedInDaemonContext(browser);
    await gotoNext(page, `${baseUrl}/next/tasks`);
    await page.getByTestId("rail-new-task").click();
    const sheet = page.getByTestId("new-task-sheet");
    await sheet.waitFor({ state: "visible" });
    await sleep(300);

    if ((await page.getByTestId("nt-repo").count()) > 0) {
      await page.getByTestId("nt-repo").click();
      await page.getByTestId("nt-repo-opt-acme/widgets").click();
    }
    await page.getByTestId("nt-title").fill("Regression found in v3.6.0");
    await page.getByTestId("nt-description").fill("Reproduce release regression.");
    await page.getByTestId("more-options").click();
    if ((await page.getByTestId("nt-repo").count()) > 0) {
      const repoLabel = (await page.getByTestId("nt-repo").textContent()) || "";
      if (!repoLabel.includes("acme/widgets")) {
        await page.getByTestId("nt-repo").click();
        await page.getByTestId("nt-repo-opt-acme/widgets").click();
      }
    }

    const foundInInput = page.getByTestId("nt-found-in");
    await foundInInput.fill("not-a-semver");
    await page.getByTestId("new-task-submit").click();
    await sleep(250);

    const fieldErrorEl = page.getByTestId("nt-found-in-error");
    const hasUnderFieldError =
      (await fieldErrorEl.count()) > 0 && (await fieldErrorEl.isVisible());
    const foundInAriaInvalid = await foundInInput.getAttribute("aria-invalid");
    const titleAriaInvalid = await page
      .getByTestId("nt-title")
      .getAttribute("aria-invalid");

    record(
      "item6.malformed_refused_under_field",
      hasUnderFieldError &&
        foundInAriaInvalid === "true" &&
        titleAriaInvalid !== "true",
      `hasUnderFieldError=${hasUnderFieldError}, foundInAriaInvalid=${foundInAriaInvalid}, titleAriaInvalid=${titleAriaInvalid}`,
    );

    // Valid v3.6.0 submission -> verify SQLite task_link
    if ((await foundInInput.count()) === 0) {
      await page.getByTestId("more-options").click();
    }
    await foundInInput.fill("v3.6.0");
    const createRespPromise = page.waitForResponse(
      (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
    );
    await page.getByTestId("new-task-submit").click();
    const createResp = await createRespPromise;
    const createdTask = await createResp.json();
    const sqlOut = execFileSync(
      "sqlite3",
      [
        seeded.dbPath,
        `SELECT kind || ':' || target FROM task_link WHERE task_id = '${createdTask.id}';`,
      ],
      { encoding: "utf8" },
    ).trim();

    record(
      "item6.valid_v3_6_0_stored_in_task_link",
      createResp.status() < 300 &&
        createdTask.foundIn === "v3.6.0" &&
        sqlOut.split("\n").includes("found-in:v3.6.0"),
      `status=${createResp.status()}, api.foundIn=${createdTask.foundIn}, sqlite.task_link=${JSON.stringify(sqlOut)}`,
    );

    // Classic UI form check: Found in release is absent from classic form
    await gotoClassic(page, `${baseUrl}/tasks`);
    const classicNewBtn = page
      .locator("button")
      .filter({ hasText: /New task/i })
      .first();
    if ((await classicNewBtn.count()) > 0) {
      await classicNewBtn.click();
      await page.waitForTimeout(300);
    }
    const classicHtml = await page.content();
    const absentFromClassic =
      !classicHtml.includes("Found in release") &&
      !classicHtml.includes("nt-found-in");

    record(
      "item6.absent_from_classic_form",
      absentFromClassic,
      `absentFromClassic=${absentFromClassic}`,
    );

    await context.close();
  }
} finally {
  await browser.close();
}

const failed = results.filter((r) => !r.ok);
console.log(
  `\nSummary: ${results.length - failed.length}/${results.length} checks passed.`,
);
if (failed.length > 0) {
  process.exit(1);
}
