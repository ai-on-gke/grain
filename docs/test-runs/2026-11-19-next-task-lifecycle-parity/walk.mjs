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

// End-to-end walkthrough driver for docs/test-plans/2026-11-19.md §5 against a
// real `grain daemon` + `ghsim` + `iapsim` (SQLite or PostgreSQL).
// Drives only `/next/` routes in real Playwright Chromium across unlimited
// operator (alice), scoped operator (bob, dave), and viewer (carol) sessions.

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { execFileSync } from "node:child_process";

const require = createRequire(
  path.resolve("/home/debian/work/ui-ng/package.json"),
);
const { chromium } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;

const PROXY = process.env.PROXY || "http://127.0.0.1:4293";
const DAEMON = process.env.DAEMON || "http://127.0.0.1:4291";
const GHSIM = process.env.GHSIM || "127.0.0.1:4294";
const WORK_DIR = process.env.WORK_DIR || "/tmp/walk-s5-sqlite";
const STORE = process.env.STORE || "sqlite";
const PG_DSN =
  process.env.PG_DSN ||
  "postgres://grain:grain@127.0.0.1:5432/grain_walk_s5?sslmode=disable";
const SHOT_DIR = process.env.SHOT_DIR || `/tmp/walk-s5-shots-${STORE}`;
const CODE = fs
  .readFileSync(path.join(WORK_DIR, "data/local-api-token"), "utf8")
  .trim();

fs.mkdirSync(SHOT_DIR, { recursive: true });

const results = [];
const findings = [];

function record(area, name, pass, detail) {
  results.push({ area, name, pass, detail });
  const tag = pass ? "PASS" : "FAIL";
  console.log(`[${tag}] [${area}] ${name}: ${detail}`);
  if (!pass) findings.push({ area, name, detail });
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

async function api(method, p, body, email = null) {
  const base = email ? PROXY : DAEMON;
  const headers = email
    ? { "X-Iapsim-Email": email }
    : { Authorization: `Bearer ${CODE}` };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(`${base}${p}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = text;
  }
  return { status: res.status, ok: res.ok, body: json, raw: text };
}

async function ghsim(p) {
  const res = await fetch(`http://${GHSIM}${p}`);
  return { status: res.status, text: await res.text() };
}

async function waitFor(fn, timeoutMs = 60000, intervalMs = 300) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    last = await fn();
    if (last) return last;
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  throw new Error(`waitFor timed out after ${timeoutMs}ms (last=${JSON.stringify(last)})`);
}

function writeAction(taskId, action) {
  fs.writeFileSync(
    path.join(WORK_DIR, `action-${taskId}.json`),
    JSON.stringify(action),
  );
}

async function newUserContext(browser, email, opts = {}) {
  const ctx = await browser.newContext({
    viewport: opts.viewport || { width: 1440, height: 900 },
    colorScheme: opts.colorScheme || "light",
    timezoneId: opts.timezoneId || "UTC",
  });
  // Set iapsim_email cookie so all navigations, fetches, and SSE EventSource
  // authenticate as this real IAP user through iapsim proxy.
  await ctx.addCookies([
    {
      name: "iapsim_email",
      value: email,
      url: PROXY,
    },
  ]);
  const page = await ctx.newPage();
  const pageErrors = [];
  const consoleErrors = [];
  page.on("pageerror", (err) => pageErrors.push(String(err)));
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  return { ctx, page, pageErrors, consoleErrors };
}

const SEVERE_IMPACTS = new Set(["serious", "critical"]);

async function checkAxeAndOverflow(page, label, exclude = []) {
  let builder = new AxeBuilder({ page });
  for (const sel of exclude) builder = builder.exclude(sel);
  const axeRes = await builder.analyze();
  const severe = axeRes.violations
    .filter((v) => SEVERE_IMPACTS.has(v.impact ?? ""))
    .map((v) => ({
      id: v.id,
      impact: v.impact,
      help: v.help,
      nodes: v.nodes.map((n) => ({ target: n.target, html: n.html.slice(0, 120) })),
    }));
  const moderate = axeRes.violations
    .filter((v) => !SEVERE_IMPACTS.has(v.impact ?? ""))
    .map((v) => `${v.id}(${v.impact})`);
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  record(
    "A11y/Layout",
    label,
    severe.length === 0 && overflow <= 0,
    `overflow=${overflow}px, severe_axe=${severe.length}, non_severe=${JSON.stringify(moderate)}` +
      (severe.length ? ` ${JSON.stringify(severe)}` : ""),
  );
  return { violations: severe, overflow };
}

function seedBaseRepairTag(taskId) {
  // Attach grain's own base-ci-repair tag (without fixes-base-branch) so
  // model.GrainBaseRepair(task) is true.
  const tag = "base-ci-repair:acme/widgets:main";
  if (STORE === "sqlite") {
    const db = path.join(WORK_DIR, "data/store/grain.db");
    execFileSync("sqlite3", [
      db,
      `INSERT OR IGNORE INTO task_tag (task_id, tag) VALUES ('${taskId}', '${tag}');`,
    ]);
  } else {
    execFileSync("psql", [
      PG_DSN,
      "-c",
      `INSERT INTO task_tag (task_id, tag) VALUES ('${taskId}', '${tag}') ON CONFLICT DO NOTHING;`,
    ]);
  }
}

async function run() {
  const browser = await chromium.launch({ headless: true });
  const version = browser.version();
  console.log(`Chromium version: ${version}, STORE=${STORE}`);

  // =========================================================================
  // AREA 1: Edit panel & change-only PATCH /api/tasks/{id} + Audit + Locks
  // =========================================================================
  {
    const { ctx, page, pageErrors } = await newUserContext(
      browser,
      "alice@example.com",
    );
    try {
      // Create a proposed task with autoMerge: true first, then turn autoMerge off
      // externally to test change-only PATCH preserving auto_merge: false.
      const createRes = await api("POST", "/api/tasks", {
        title: "Area 1 edit target",
        description: "Walkthrough task for change-only PATCH and edit fields.",
        repo: "acme/widgets",
        approve: false,
        autoMerge: true,
      }, "alice@example.com");
      assert(createRes.ok, `create area1 task failed: ${createRes.raw}`);
      const tid = createRes.body.id;

      // Open Edit panel while task still has autoMerge: true on screen
      await page.goto(`${PROXY}/next/task/${tid}`);
      await page.getByTestId("act-edit").waitFor({ state: "visible" });
      await page.getByTestId("act-edit").click();
      const panel = page.getByTestId("task-edit-panel");
      await panel.waitFor({ state: "visible" });

      // Verify inline priority validation: 0, -3, 2.5 refused and Save disabled
      const prioInput = panel.getByTestId("edit-priority");
      const saveBtn = panel.getByTestId("edit-save-btn");
      let badRefusedAll = true;
      for (const bad of ["0", "-3", "2.5"]) {
        await prioInput.fill(bad);
        const help = await panel.getByTestId("edit-priority-help").textContent();
        const dis = await saveBtn.isDisabled();
        const inv = await prioInput.getAttribute("aria-invalid");
        if (!dis || inv !== "true" || !help.includes("whole number, 1 or more")) {
          badRefusedAll = false;
        }
      }
      record(
        "Area 1",
        "Edit priority inline refusal (0, -3, 2.5)",
        badRefusedAll,
        `refused all invalid values and disabled Save=${badRefusedAll}`,
      );

      // Now turn auto_merge OFF externally (simulating a hold or another tab)
      // BEFORE clicking Save! First set External ref and priority 1e3.
      await panel.getByTestId("edit-external-ref").fill("EXT-101");
      await prioInput.fill("1e3");
      const saveEnabled = await saveBtn.isEnabled();
      record(
        "Area 1",
        "Edit priority 1e3 accepted",
        saveEnabled,
        `Save enabled for 1e3=${saveEnabled}`,
      );

      // Flip autoMerge=false out-of-band via API (another tab)
      const offRes = await api(
        "PATCH",
        `/api/tasks/${tid}`,
        { autoMerge: false },
        "alice@example.com",
      );
      assert(offRes.ok, `flip autoMerge false failed: ${offRes.raw}`);

      // Wait 500ms so SSE event reaches the browser — let's ALSO check whether
      // background SSE/poll clobbers our unsaved External ref / Priority draft!
      await page.waitForTimeout(800);
      const refAfterSSE = await panel.getByTestId("edit-external-ref").inputValue();
      const prioAfterSSE = await prioInput.inputValue();
      record(
        "Area 1",
        "Open Edit form survives background SSE update without clobbering draft",
        refAfterSSE === "EXT-101" && prioAfterSSE === "1e3",
        `after external PATCH+SSE: externalRef=${JSON.stringify(refAfterSSE)}, priority=${JSON.stringify(prioAfterSSE)}`,
      );

      // Re-fill if clobbered so we still test change-only PATCH itself
      if (refAfterSSE !== "EXT-101") {
        await panel.getByTestId("edit-external-ref").fill("EXT-101");
      }
      if (prioAfterSSE !== "1e3") {
        await prioInput.fill("1e3");
      }

      // Intercept the outgoing PATCH to inspect exact keys sent
      let patchBody = null;
      const patchWait = page.waitForRequest(
        (r) =>
          r.method() === "PATCH" &&
          new URL(r.url()).pathname === `/api/tasks/${tid}`,
      );
      await saveBtn.click();
      const patchReq = await patchWait;
      patchBody = patchReq.postDataJSON();
      await panel.waitFor({ state: "hidden" });

      const after1 = (await api("GET", `/api/tasks/${tid}`, undefined, "alice@example.com")).body;
      const omittedAutoMerge = !("autoMerge" in patchBody) && !("auto_merge" in patchBody);
      const omittedOwner = !("owner" in patchBody);
      const omittedReviewers = !("reviewers" in patchBody);
      const omittedAdmission = !("admission" in patchBody);
      const omittedHosting = !("hosting" in patchBody);
      const omittedKeepRunning = !("keepRunning" in patchBody);
      const omittedFixesBase = !("fixesBaseBranch" in patchBody);
      const noOwnerAudit = !(after1.transitions || []).some((tr) => tr.state === "owner_changed");
      record(
        "Area 1",
        "Change-only PATCH omits untouched fields & preserves auto_merge=false",
        patchBody.priority === 1000 &&
          patchBody.externalRef === "EXT-101" &&
          omittedAutoMerge &&
          omittedOwner &&
          omittedReviewers &&
          omittedAdmission &&
          omittedHosting &&
          omittedKeepRunning &&
          omittedFixesBase &&
          after1.autoMerge === false &&
          after1.priority === 1000 &&
          after1.externalRef === "EXT-101" &&
          noOwnerAudit,
        `sent keys=${JSON.stringify(Object.keys(patchBody))}, priority=${after1.priority}, externalRef=${after1.externalRef}, autoMerge=${after1.autoMerge}, noOwnerAudit=${noOwnerAudit}`,
      );

      // Now test the second half: change Owner, Reviewers, Admission (Bypass -> Durable),
      // Hosting (Interactive console), Keep running, and "This fixes a red main"
      await page.getByTestId("act-edit").click();
      await panel.waitFor({ state: "visible" });
      await panel.getByTestId("edit-owner").fill("bob@example.com");
      await panel.getByTestId("edit-reviewers").fill("carol, dave");
      await panel.getByTestId("edit-admission").click();
      await panel.getByTestId("edit-admission-opt-bypass").click();
      await panel.getByTestId("edit-keep-running").check();
      await panel.getByTestId("edit-fixes-base").check();

      // Screenshot Edit panel desktop light
      await page.screenshot({ path: path.join(SHOT_DIR, "area1-edit-desktop-light.png") });
      await checkAxeAndOverflow(page, "Area 1 Edit panel desktop light");

      await Promise.all([
        page.waitForResponse(
          (r) =>
            r.request().method() === "PATCH" &&
            new URL(r.url()).pathname === `/api/tasks/${tid}` &&
            r.status() === 200,
        ),
        saveBtn.click(),
      ]);
      await panel.waitFor({ state: "hidden" });

      // Reload & switch Admission to Durable + Hosting to Interactive console
      await page.reload();
      await page.getByTestId("act-edit").click();
      await panel.waitFor({ state: "visible" });
      const bypassPersisted = (await api("GET", `/api/tasks/${tid}`, undefined, "alice@example.com")).body;
      record(
        "Area 1",
        "Edit Owner, Reviewers, Bypass queue, Keep running, Fixes base persisted across reload",
        bypassPersisted.owner === "bob@example.com" &&
          JSON.stringify(bypassPersisted.reviewers) === JSON.stringify(["carol", "dave"]) &&
          bypassPersisted.admission === "bypass" &&
          bypassPersisted.keepRunning === true &&
          bypassPersisted.fixesBaseBranch === true,
        `owner=${bypassPersisted.owner}, reviewers=${JSON.stringify(bypassPersisted.reviewers)}, admission=${bypassPersisted.admission}, keepRunning=${bypassPersisted.keepRunning}, fixesBaseBranch=${bypassPersisted.fixesBaseBranch}`,
      );

      await panel.getByTestId("edit-admission").click();
      await panel.getByTestId("edit-admission-opt-durable").click();
      await panel.getByTestId("edit-hosting").click();
      await panel.getByTestId("edit-hosting-opt-console").click();
      await Promise.all([
        page.waitForResponse(
          (r) =>
            r.request().method() === "PATCH" &&
            new URL(r.url()).pathname === `/api/tasks/${tid}` &&
            r.status() === 200,
        ),
        saveBtn.click(),
      ]);
      await panel.waitFor({ state: "hidden" });

      await page.reload();
      await page.getByTestId("task-state").waitFor({ state: "visible" });
      const durablePersisted = (await api("GET", `/api/tasks/${tid}`, undefined, "alice@example.com")).body;
      record(
        "Area 1",
        "Edit Durable admission & Interactive console hosting persisted across reload",
        durablePersisted.admission === "durable" && durablePersisted.hosting === "console",
        `admission=${durablePersisted.admission}, hosting=${durablePersisted.hosting}`,
      );

      // Check History rendering on the task page for owner_changed / admission_changed / hosting_changed
      const historyText = await page.locator(".history").first().innerText();
      const apiOwnerTr = (durablePersisted.transitions || []).find((tr) => tr.state === "owner_changed");
      const historyHasFromTo =
        historyText.includes("bob@example.com") &&
        historyText.includes("alice@example.com");
      record(
        "Area 1",
        "History shows owner transfer details (oldOwner -> newOwner)",
        !!apiOwnerTr && historyHasFromTo,
        `API transition=${JSON.stringify(apiOwnerTr)}; rendered History text=${JSON.stringify(historyText)}`,
      );

      // Check locked Admission on a real console session task (built-in Director session)
      const csRes = await api("GET", "/api/console-sessions", undefined, "alice@example.com");
      const consoleTaskId = csRes.body?.sessions?.find((s) => s.taskId)?.taskId || null;
      if (consoleTaskId) {
        await page.goto(`${PROXY}/next/task/${consoleTaskId}`);
        await page.getByTestId("act-edit").click();
        await panel.waitFor({ state: "visible" });
        const admBtn = panel.getByTestId("edit-admission");
        const admDisabled = await admBtn.isDisabled();
        record(
          "Area 1",
          "Admission locked on console session task",
          admDisabled,
          `consoleTaskId=${consoleTaskId}, admission disabled=${admDisabled}`,
        );
      } else {
        record(
          "Area 1",
          "Admission locked on console session task",
          false,
          `Could not find consoleSession task (${csRes.status}: ${csRes.raw.slice(0, 200)})`,
        );
      }

      // Check locked "This fixes a red main" on a real grainBaseRepair task
      const repairCreate = await api("POST", "/api/tasks", {
        title: "Grain base repair lock check",
        description: "Simulated base repair task.",
        repo: "acme/widgets",
        approve: false,
      }, "alice@example.com");
      const repairId = repairCreate.body.id;
      seedBaseRepairTag(repairId);
      await page.goto(`${PROXY}/next/task/${repairId}`);
      await page.getByTestId("act-edit").click();
      await panel.waitFor({ state: "visible" });
      const fixesChk = panel.getByTestId("edit-fixes-base");
      const fixesChecked = await fixesChk.isChecked();
      const fixesDisabled = await fixesChk.isDisabled();
      record(
        "Area 1",
        "Fixes-base locked on grain base-repair task",
        fixesChecked && fixesDisabled,
        `repairId=${repairId}, checked=${fixesChecked}, disabled=${fixesDisabled}`,
      );

      // Also probe: as scoped operator bob@example.com, open Edit panel on a task
      // and check whether Keep running / Auto-merge / Admission are gated in Edit!
      const bobEditCtx = await newUserContext(browser, "bob@example.com");
      try {
        const bTask = (await api("POST", "/api/tasks", {
          title: "Bob edit role gating check",
          description: "Check what Edit allows a limited contributor to toggle.",
          repo: "acme/widgets",
          approve: false,
        }, "bob@example.com")).body;
        await bobEditCtx.page.goto(`${PROXY}/next/task/${bTask.id}`);
        await bobEditCtx.page.getByTestId("act-edit").click();
        const bPanel = bobEditCtx.page.getByTestId("task-edit-panel");
        await bPanel.waitFor({ state: "visible" });
        const krDisabled = await bPanel.getByTestId("edit-keep-running").isDisabled();
        const amDisabled = await bPanel.getByTestId("edit-auto-merge").isDisabled();
        let krPatchStatus = null;
        if (!krDisabled) {
          await bPanel.getByTestId("edit-keep-running").check();
          const [resp] = await Promise.all([
            bobEditCtx.page.waitForResponse(
              (r) =>
                r.request().method() === "PATCH" &&
                new URL(r.url()).pathname === `/api/tasks/${bTask.id}`,
            ),
            bPanel.getByTestId("edit-save-btn").click(),
          ]);
          krPatchStatus = resp.status();
        }
        record(
          "Area 1",
          "Scoped operator (grain/contributor) Edit keepRunning & autoMerge gating",
          krDisabled && amDisabled,
          `edit-keep-running disabled=${krDisabled}, edit-auto-merge disabled=${amDisabled}, attempted PATCH status=${krPatchStatus}`,
        );
        await api("POST", `/api/tasks/${bTask.id}/close`, {}, "alice@example.com");
      } finally {
        await bobEditCtx.ctx.close();
      }

      // Clean up Area 1 tasks
      await api("POST", `/api/tasks/${tid}/close`, {}, "alice@example.com");
      await api("POST", `/api/tasks/${repairId}/close`, {}, "alice@example.com");
      record("Area 1", "Zero uncaught page errors", pageErrors.length === 0, `pageErrors=${JSON.stringify(pageErrors)}`);
    } finally {
      await ctx.close();
    }
  }

  // =========================================================================
  // AREA 2: New task sheet — No repo, Fork-head validation, Role gating
  // =========================================================================
  {
    const { ctx, page, pageErrors } = await newUserContext(
      browser,
      "alice@example.com",
    );
    try {
      await page.goto(`${PROXY}/next/tasks`);
      await page.getByTestId("tab-backlog").waitFor({ state: "visible" });
      await page.locator("body").press("c");
      const sheet = page.getByTestId("new-task-sheet");
      await sheet.waitFor({ state: "visible" });

      // 2a. No repository task with Owner + Keep running + priority 25
      await sheet.getByTestId("nt-title").fill("Area 2 no-repo walkthrough task");
      await sheet.getByTestId("nt-repo").click();
      await sheet.getByTestId("nt-repo-opt-__none__").click();
      await sheet.getByTestId("nt-base").waitFor({ state: "detached", timeout: 5000 }).catch(() => {});

      const baseHidden = (await sheet.getByTestId("nt-base").count()) === 0;
      const newBrHidden = (await sheet.getByTestId("nt-new-branch").count()) === 0;
      const autoMergeHidden = (await sheet.getByTestId("nt-auto-merge").count()) === 0;
      await sheet.getByTestId("nt-owner").fill("bob@example.com");
      await sheet.getByTestId("nt-keep-running").check();
      await sheet.getByTestId("nt-priority").fill("25");
      // Uncheck approve so fake-agy doesn't spin in a keep-running loop during walkthrough
      const approveSwitch = sheet.getByTestId("approved-switch");
      if ((await approveSwitch.getAttribute("aria-checked")) === "true") {
        await approveSwitch.click();
      }

      await page.screenshot({ path: path.join(SHOT_DIR, "area2-newtask-norepo-desktop-light.png") });
      await checkAxeAndOverflow(page, "Area 2 New task sheet (No repo) desktop light");

      const [noRepoReq, noRepoRes] = await Promise.all([
        page.waitForRequest(
          (r) => r.method() === "POST" && new URL(r.url()).pathname === "/api/tasks",
        ),
        page.waitForResponse(
          (r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/tasks",
        ),
        sheet.getByTestId("new-task-submit").click(),
      ]);
      const nrPayload = noRepoReq.postDataJSON();
      const nrCreated = await noRepoRes.json();
      const nrStored = (await api("GET", `/api/tasks/${nrCreated.id}`, undefined, "alice@example.com")).body;
      record(
        "Area 2",
        "New task No repository hides repo controls & persists Owner + Keep running + priority 25",
        baseHidden &&
          newBrHidden &&
          autoMergeHidden &&
          noRepoRes.status() < 300 &&
          nrPayload.noRepo === true &&
          nrPayload.repo === "" &&
          nrPayload.base === "" &&
          !nrPayload.autoMerge &&
          nrStored.owner === "bob@example.com" &&
          nrStored.keepRunning === true &&
          nrStored.priority === 25 &&
          !nrStored.repo,
        `hidden=(base:${baseHidden},newBr:${newBrHidden},am:${autoMergeHidden}), status=${noRepoRes.status()}, stored={id:${nrStored.id},repo:${JSON.stringify(nrStored.repo)},owner:${nrStored.owner},keepRunning:${nrStored.keepRunning},priority:${nrStored.priority}}`,
      );
      await api("POST", `/api/tasks/${nrCreated.id}/close`, {}, "alice@example.com");

      // 2b. Repo task with <fork-owner>:<branch> validation and real ghsim resolution
      await page.locator("body").press("c");
      await sheet.waitFor({ state: "visible" });
      await sheet.getByTestId("nt-title").fill("Area 2 fork-head walkthrough task");
      const newBrChk = sheet.getByTestId("nt-new-branch");
      if (await newBrChk.isChecked()) await newBrChk.uncheck();
      if ((await approveSwitch.getAttribute("aria-checked")) === "true") {
        await approveSwitch.click();
      }

      const baseInput = sheet.getByTestId("nt-base");
      const submitBtn = sheet.getByTestId("new-task-submit");
      let invalidForkRejected = true;
      for (const bad of ["bad::label", "-bad:x", "alice:a..b"]) {
        await baseInput.fill(bad);
        const errVis = await sheet.getByTestId("nt-fork-head-error").isVisible();
        const inv = await baseInput.getAttribute("aria-invalid");
        const dis = await submitBtn.isDisabled();
        if (!errVis || inv !== "true" || !dis) invalidForkRejected = false;
      }
      await baseInput.fill("alice:fix-branch");
      const helpText = await sheet.getByTestId("nt-fork-head-help").textContent();
      const errGone = (await sheet.getByTestId("nt-fork-head-error").count()) === 0;
      const enabledNow = await submitBtn.isEnabled();

      const [forkRes] = await Promise.all([
        page.waitForResponse(
          (r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/tasks",
        ),
        submitBtn.click(),
      ]);
      const forkCreated = await forkRes.json();
      const forkStored = forkRes.ok()
        ? (await api("GET", `/api/tasks/${forkCreated.id}`, undefined, "alice@example.com")).body
        : null;
      record(
        "Area 2",
        "New task fork-head inline rejection + real ghsim resolution (alice:fix-branch)",
        invalidForkRejected &&
          errGone &&
          enabledNow &&
          helpText.includes("branch fix-branch on alice's fork") &&
          forkRes.ok() &&
          forkStored?.base === "main",
        `invalidRejected=${invalidForkRejected}, help=${JSON.stringify(helpText)}, POST status=${forkRes.status()}, stored={id:${forkStored?.id},base:${forkStored?.base},pr:${forkStored?.pullRequest}}`,
      );
      if (forkStored?.id) {
        await api("POST", `/api/tasks/${forkStored.id}/close`, {}, "alice@example.com");
      }
      record("Area 2", "Zero uncaught page errors (alice)", pageErrors.length === 0, `pageErrors=${JSON.stringify(pageErrors)}`);
    } finally {
      await ctx.close();
    }

    // 2c. Scoped operator (bob: grain/contributor) & dispatcher without approve (dave: walk-dispatcher)
    const bobCtx = await newUserContext(browser, "bob@example.com");
    const daveCtx = await newUserContext(browser, "dave@example.com");
    try {
      await bobCtx.page.goto(`${PROXY}/next/tasks`);
      await bobCtx.page.getByTestId("tab-backlog").waitFor({ state: "visible" });
      await bobCtx.page.locator("body").press("c");
      const bSheet = bobCtx.page.getByTestId("new-task-sheet");
      await bSheet.waitFor({ state: "visible" });
      const bobKrDisabled = await bSheet.getByTestId("nt-keep-running").isDisabled();
      const bobApproveDisabled = await bSheet.getByTestId("approved-switch").isDisabled();
      const bobOwnerEnabled = await bSheet.getByTestId("nt-owner").isEnabled();
      await bSheet.getByTestId("nt-priority").fill("25");
      const bobHighPrioDisabled = await bSheet.getByTestId("new-task-submit").isDisabled();
      await bSheet.getByTestId("nt-priority").fill("5");
      await bSheet.getByTestId("nt-title").fill("Bob proposed task for run-now gating");
      const [bobPost] = await Promise.all([
        bobCtx.page.waitForResponse(
          (r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/tasks",
        ),
        bSheet.getByTestId("new-task-submit").click(),
      ]);
      const bobTask = await bobPost.json();
      // Also verify backend refuses keepRunning=true from bob directly
      const bobApiRefuse = await api("POST", "/api/tasks", {
        title: "Bob direct keepRunning attempt",
        noRepo: true,
        keepRunning: true,
      }, "bob@example.com");
      record(
        "Area 2",
        "Scoped operator (bob) Keep running disabled in UI & refused by API; Approve disabled",
        bobKrDisabled &&
          bobApproveDisabled &&
          bobOwnerEnabled &&
          bobHighPrioDisabled &&
          bobPost.ok() &&
          bobTask.state === "proposed" &&
          bobApiRefuse.status === 403,
        `krDisabled=${bobKrDisabled}, approveDisabled=${bobApproveDisabled}, ownerEnabled=${bobOwnerEnabled}, highPrioRefused=${bobHighPrioDisabled}, createdState=${bobTask.state}, directKeepRunningHTTP=${bobApiRefuse.status}`,
      );

      // Now dave (has dispatch:write + tasks:write, NO approve/tasks:admin) visits bob's unapproved proposed task:
      // Unapproved Run now MUST be hidden!
      await daveCtx.page.goto(`${PROXY}/next/task/${bobTask.id}`);
      await daveCtx.page.getByTestId("task-state").waitFor({ state: "visible" });
      const daveRunNowUnapproved = await daveCtx.page.getByTestId("act-run-now").count();
      const daveApproveUnapproved = await daveCtx.page.getByTestId("act-approve").count();
      // Direct API run-now on unapproved task must also return 403
      const daveRunNowApiUnapproved = await api("POST", `/api/tasks/${bobTask.id}/run-now`, {}, "dave@example.com");

      // Block bob's task on a proposed dep so approving it keeps it in queued (not dispatched)
      const blocker = (await api("POST", "/api/tasks", {
        title: "Dep blocker for dave run-now check",
        noRepo: true,
        approve: false,
      }, "alice@example.com")).body;
      await api("POST", `/api/tasks/${bobTask.id}/depends-on`, { id: blocker.id, attach: true }, "alice@example.com");
      await api("POST", `/api/tasks/${bobTask.id}/approve`, {}, "alice@example.com");

      // Revisit as dave now that task is approved & queued: Run now SHOULD appear!
      await daveCtx.page.reload();
      await daveCtx.page.getByTestId("task-state").waitFor({ state: "visible" });
      const daveRunNowApproved = await daveCtx.page.getByTestId("act-run-now").count();
      record(
        "Area 2",
        "Unapproved Run now gated on approve (dave has dispatch:write without tasks:admin)",
        daveRunNowUnapproved === 0 &&
          daveApproveUnapproved === 0 &&
          daveRunNowApiUnapproved.status === 403 &&
          daveRunNowApproved === 1,
        `unapproved: act-run-now=${daveRunNowUnapproved}, act-approve=${daveApproveUnapproved}, API=${daveRunNowApiUnapproved.status}; approved queued: act-run-now=${daveRunNowApproved}`,
      );
      await api("POST", `/api/tasks/${bobTask.id}/close`, {}, "alice@example.com");
      await api("POST", `/api/tasks/${blocker.id}/close`, {}, "alice@example.com");
    } finally {
      await bobCtx.ctx.close();
      await daveCtx.ctx.close();
    }
  }

  // =========================================================================
  // AREA 3: Reversible close reasons + Undo + Running close + Reopen History
  // =========================================================================
  {
    const { ctx, page, pageErrors } = await newUserContext(
      browser,
      "alice@example.com",
    );
    try {
      // 3a. Queued task (blocked on a proposed dep so it stays queued!)
      const depBlocker = (await api("POST", "/api/tasks", {
        title: "Area 3 dep blocker",
        noRepo: true,
        approve: false,
      }, "alice@example.com")).body;
      const queuedTask = (await api("POST", "/api/tasks", {
        title: "Area 3 queued close reason check",
        description: "Stays queued behind depBlocker.",
        noRepo: true,
        approved: true,
        dependsOn: [depBlocker.id],
      }, "alice@example.com")).body;

      await page.goto(`${PROXY}/next/task/${queuedTask.id}`);
      await page.getByTestId("act-close").waitFor({ state: "visible" });
      await page.getByTestId("act-close").click();
      const confirm = page.getByTestId("close-confirm");
      await confirm.waitFor({ state: "visible" });
      const queuedAlertCount = await confirm.locator('[role="alert"]').count();
      const cancelLabel = (await confirm.getByTestId("close-keep").textContent()).trim();
      await page.screenshot({ path: path.join(SHOT_DIR, "area3-close-queued-desktop-light.png") });
      await checkAxeAndOverflow(page, "Area 3 inline close row (queued) desktop light");

      // Escape cancels cleanly
      await confirm.getByTestId("close-reason").press("Escape");
      const dismissedOnEsc = (await confirm.count()) === 0;
      const closeBtnFocusedAfterEsc = await page.getByTestId("act-close").evaluate((el) => document.activeElement === el);

      // Re-open close row, leave blank, press Enter -> closes cleanly with Undo snackbar
      await page.getByTestId("act-close").click();
      await confirm.waitFor({ state: "visible" });
      await confirm.getByTestId("close-reason").press("Enter");
      await page.getByTestId("act-reopen").waitFor({ state: "visible" });
      const undoText = (await page.getByTestId("snackbar-action").textContent()).trim();
      await page.getByTestId("snackbar-action").click();
      await page.getByTestId("act-close").waitFor({ state: "visible" });
      const afterUndo = (await api("GET", `/api/tasks/${queuedTask.id}`, undefined, "alice@example.com")).body;

      // Close again with an explicit reason
      await page.getByTestId("act-close").click();
      await confirm.waitFor({ state: "visible" });
      await confirm.getByTestId("close-reason").fill("queued close reason walkthrough");
      await confirm.getByTestId("close-reason").press("Enter");
      await page.getByTestId("act-reopen").waitFor({ state: "visible" });
      await page.reload();
      await page.getByTestId("act-reopen").waitFor({ state: "visible" });
      const histQueued = await page.locator(".history").first().innerText();
      record(
        "Area 3",
        "Queued close inline row: no warning, Escape cancels, blank Enter closes, Undo restores, reason persists",
        queuedAlertCount === 0 &&
          cancelLabel === "Cancel" &&
          dismissedOnEsc &&
          undoText === "Undo" &&
          afterUndo.state === "queued" &&
          histQueued.includes("queued close reason walkthrough"),
        `alertCount=${queuedAlertCount}, cancelLabel=${cancelLabel}, escDismissed=${dismissedOnEsc}, escFocusBack=${closeBtnFocusedAfterEsc}, undoText=${undoText}, stateAfterUndo=${afterUndo.state}, historyHasReason=${histQueued.includes("queued close reason walkthrough")}`,
      );

      // 3b. Real running task closed with a reason -> irreversible warning, task stops, no Undo,
      // then reopen with a reason and verify both reasons persist across reload!
      const runTask = (await api("POST", "/api/tasks", {
        title: "Area 3 live running close test",
        description: "Fake agy waits on release file.",
        repo: "acme/widgets",
        approve: false,
      }, "alice@example.com")).body;
      writeAction(runTask.id, {
        kind: "mcp_sequence",
        calls: [
          {
            tool: "run_command",
            args: {
              command: `while [ ! -f ${WORK_DIR}/release-area3 ]; do sleep 0.5; done; echo released`,
              description: "waiting for area 3 close test",
              timeout: 600000,
            },
          },
          { tool: "nothing_needed", args: { reason: "released" } },
        ],
      });
      await api("POST", `/api/tasks/${runTask.id}/approve`, {}, "alice@example.com");
      await waitFor(async () => {
        const d = (await api("GET", `/api/tasks/${runTask.id}`, undefined, "alice@example.com")).body;
        return d.state === "running" ? d : null;
      }, 45000, 400);

      await page.goto(`${PROXY}/next/task/${runTask.id}`);
      await page.getByTestId("act-close").waitFor({ state: "visible" });
      await page.getByTestId("act-close").click();
      await confirm.waitFor({ state: "visible" });
      const runAlertText = await confirm.locator('[role="alert"]').textContent();
      const keepLabel = (await confirm.getByTestId("close-keep").textContent()).trim();
      await confirm.getByTestId("close-reason").fill("stopping live run reason");
      await confirm.getByTestId("close-yes").click();
      await page.getByTestId("act-reopen").waitFor({ state: "visible", timeout: 20000 });
      const undoOnRunning = await page.getByTestId("snackbar-action").count();

      // Verify task stopped & closed in store
      const closedRun = (await api("GET", `/api/tasks/${runTask.id}`, undefined, "alice@example.com")).body;
      const closedTr = (closedRun.transitions || []).find((tr) => tr.state === "closed" && tr.reason === "stopping live run reason");
      record(
        "Area 3",
        "Running close shows irreversible warning, stops run, records reason, no Undo",
        runAlertText.includes("stops the run") &&
          keepLabel === "Keep" &&
          undoOnRunning === 0 &&
          closedRun.state === "closed" &&
          !!closedTr,
        `alert=${JSON.stringify(runAlertText)}, keepLabel=${keepLabel}, undoCount=${undoOnRunning}, state=${closedRun.state}, closedTr=${JSON.stringify(closedTr)}, topLevelClosedReason=${JSON.stringify(closedRun.closedReason)}`,
      );

      // Reopen with a reason (via POST /api/tasks/{id}/reopen {reason} while blocked on depBlocker so it doesn't re-run)
      // Also note: check whether UI Reopen button has a reason input (we already know from code it's a direct click).
      await api("POST", `/api/tasks/${runTask.id}/depends-on`, { id: depBlocker.id, attach: true }, "alice@example.com");
      const reopenRes = await api(
        "POST",
        `/api/tasks/${runTask.id}/reopen`,
        { reason: "reopened after verifying stop" },
        "alice@example.com",
      );
      assert(reopenRes.ok, `reopen failed: ${reopenRes.raw}`);
      await page.reload();
      await page.getByTestId("task-state").waitFor({ state: "visible" });
      const histAfterReopen = await page.locator(".history").first().innerText();
      record(
        "Area 3",
        "Close reason and reopen reason both persist in History across reload",
        histAfterReopen.includes("stopping live run reason") &&
          histAfterReopen.includes("reopened after verifying stop"),
        `History=${JSON.stringify(histAfterReopen)}`,
      );

      // Clean up
      fs.writeFileSync(path.join(WORK_DIR, "release-area3"), "1");
      await api("POST", `/api/tasks/${runTask.id}/close`, {}, "alice@example.com");
      await api("POST", `/api/tasks/${depBlocker.id}/close`, {}, "alice@example.com");
      record("Area 3", "Zero uncaught page errors", pageErrors.length === 0, `pageErrors=${JSON.stringify(pageErrors)}`);
    } finally {
      await ctx.close();
    }
  }

  // =========================================================================
  // AREA 4: Batch `Defer until…` with non-UTC timezones + live orchestrator + dark theme
  // =========================================================================
  {
    // Occupy max-workers=2 with 2 busy tasks so 3 approved queued Backlog tasks
    // sit waiting in Backlog without being dispatched while we select & defer them.
    const busyIds = [];
    for (const n of [1, 2]) {
      const bt = (await api("POST", "/api/tasks", {
        title: `Area 4 busy worker ${n}`,
        description: "Holds worker slot during batch defer test.",
        repo: "acme/widgets",
        approve: false,
      }, "alice@example.com")).body;
      writeAction(bt.id, {
        kind: "mcp_sequence",
        calls: [
          {
            tool: "run_command",
            args: {
              command: `while [ ! -f ${WORK_DIR}/release-area4 ]; do sleep 0.5; done; echo done`,
              description: `holding worker slot ${n}`,
              timeout: 600000,
            },
          },
          { tool: "nothing_needed", args: { reason: "area 4 worker slot released" } },
        ],
      });
      await api("POST", `/api/tasks/${bt.id}/approve`, {}, "alice@example.com");
      busyIds.push(bt.id);
    }
    await waitFor(async () => {
      const states = await Promise.all(
        busyIds.map(async (id) => (await api("GET", `/api/tasks/${id}`, undefined, "alice@example.com")).body.state),
      );
      return states.every((s) => s === "running") ? states : null;
    }, 45000, 400);

    // Create 3 Backlog (approved queued) tasks for America/Los_Angeles
    const laIds = [];
    for (const n of [1, 2, 3]) {
      const t = (await api("POST", "/api/tasks", {
        title: `Area 4 LA backlog ${n}`,
        description: "Will be batch deferred under America/Los_Angeles.",
        noRepo: true,
        approved: true,
      }, "alice@example.com")).body;
      laIds.push(t.id);
    }
    // Create 3 Needs approval (proposed) tasks for Asia/Tokyo
    const tokyoIds = [];
    for (const n of [1, 2, 3]) {
      const t = (await api("POST", "/api/tasks", {
        title: `Area 4 Tokyo proposed ${n}`,
        description: "Will be batch deferred under Asia/Tokyo.",
        noRepo: true,
        approve: false,
      }, "alice@example.com")).body;
      tokyoIds.push(t.id);
    }

    // 4a. America/Los_Angeles on Backlog (light + dark theme check)
    const laCtx = await newUserContext(browser, "alice@example.com", {
      timezoneId: "America/Los_Angeles",
      colorScheme: "dark",
    });
    try {
      await laCtx.page.goto(`${PROXY}/next/tasks?tab=backlog`);
      for (const id of laIds) {
        await laCtx.page.getByLabel(`Select #${id}`, { exact: true }).check();
      }
      const bar = laCtx.page.getByTestId("batch-bar");
      await bar.waitFor({ state: "visible" });
      await bar.getByTestId("batch-defer-until-open").click();
      const dateInput = bar.getByTestId("batch-defer-date");
      await dateInput.waitFor({ state: "visible" });

      // Verify dark-theme inverted color-scheme and bar background + axe
      const darkScheme = await dateInput.evaluate((el) => getComputedStyle(el).colorScheme);
      const darkBarBg = await bar.evaluate((el) => getComputedStyle(el).backgroundColor);
      await laCtx.page.screenshot({ path: path.join(SHOT_DIR, "area4-batch-defer-desktop-dark.png") });
      await checkAxeAndOverflow(laCtx.page, "Area 4 batch defer bar desktop dark");

      // Also check light theme color-scheme
      await laCtx.page.emulateMedia({ colorScheme: "light" });
      await laCtx.page.waitForTimeout(150);
      const lightScheme = await dateInput.evaluate((el) => getComputedStyle(el).colorScheme);
      const lightBarBg = await bar.evaluate((el) => getComputedStyle(el).backgroundColor);
      await laCtx.page.screenshot({ path: path.join(SHOT_DIR, "area4-batch-defer-desktop-light.png") });
      await checkAxeAndOverflow(laCtx.page, "Area 4 batch defer bar desktop light");

      record(
        "Area 4",
        "Dark/light inverted --inverse-color-scheme on batch Defer until date input",
        darkScheme === "light" &&
          darkBarBg === "rgb(228, 230, 235)" &&
          lightScheme === "dark" &&
          lightBarBg === "rgb(40, 44, 53)",
        `dark:(scheme=${darkScheme},bg=${darkBarBg}), light:(scheme=${lightScheme},bg=${lightBarBg})`,
      );

      // Pick a fixed future date in PST (UTC-8): 2026-12-15 -> 2026-12-15T08:00:00Z
      const laDay = "2026-12-15";
      const laExpected = "2026-12-15T08:00:00Z";
      await dateInput.fill(laDay);
      await bar.getByTestId("batch-defer-until-yes").click();
      await bar.waitFor({ state: "hidden" });

      // Check all 3 tasks stored exact LA start-of-day RFC 3339 timestamp
      const laStored = await Promise.all(
        laIds.map(async (id) => (await api("GET", `/api/tasks/${id}`, undefined, "alice@example.com")).body),
      );
      const allLaExact = laStored.every(
        (t) => t.state === "deferred" && Date.parse(t.deferredUntil) === Date.parse(laExpected),
      );
      record(
        "Area 4",
        "Batch Defer until under America/Los_Angeles stores exact PST start-of-day RFC 3339",
        allLaExact,
        `expected=${laExpected}, got=${JSON.stringify(laStored.map((t) => ({ id: t.id, state: t.state, deferredUntil: t.deferredUntil })))}`,
      );
    } finally {
      await laCtx.ctx.close();
    }

    // 4b. Asia/Tokyo on Needs approval (proposed) + verify selection/input resets
    const tokyoCtx = await newUserContext(browser, "alice@example.com", {
      timezoneId: "Asia/Tokyo",
      colorScheme: "light",
    });
    try {
      await tokyoCtx.page.goto(`${PROXY}/next/tasks?tab=proposed`);
      for (const id of tokyoIds) {
        await tokyoCtx.page.getByLabel(`Select #${id}`, { exact: true }).check();
      }
      const bar = tokyoCtx.page.getByTestId("batch-bar");
      await bar.waitFor({ state: "visible" });
      await bar.getByTestId("batch-defer-until-open").click();
      const dateInput = bar.getByTestId("batch-defer-date");
      // Pick 2026-12-15 in JST (UTC+9) -> 2026-12-14T15:00:00Z
      const tokyoDay = "2026-12-15";
      const tokyoExpected = "2026-12-14T15:00:00Z";
      await dateInput.fill(tokyoDay);
      await bar.getByTestId("batch-defer-until-yes").click();
      await bar.waitFor({ state: "hidden" });

      const tokyoStored = await Promise.all(
        tokyoIds.map(async (id) => (await api("GET", `/api/tasks/${id}`, undefined, "alice@example.com")).body),
      );
      const allTokyoExact = tokyoStored.every(
        (t) => t.state === "deferred" && Date.parse(t.deferredUntil) === Date.parse(tokyoExpected),
      );

      // Verify selection & date input reset: create 1 extra proposed task, select it,
      // open Defer until… and confirm the date input is empty (not "2026-12-15")!
      const extraProp = (await api("POST", "/api/tasks", {
        title: "Area 4 reset check",
        noRepo: true,
        approve: false,
      }, "alice@example.com")).body;
      await tokyoCtx.page.getByLabel(`Select #${extraProp.id}`, { exact: true }).waitFor({ state: "visible", timeout: 10000 });
      await tokyoCtx.page.getByLabel(`Select #${extraProp.id}`, { exact: true }).check();
      await bar.getByTestId("batch-defer-until-open").click();
      const resetVal = await bar.getByTestId("batch-defer-date").inputValue();
      await api("POST", `/api/tasks/${extraProp.id}/close`, {}, "alice@example.com");

      record(
        "Area 4",
        "Batch Defer until under Asia/Tokyo stores exact JST start-of-day RFC 3339 & resets input",
        allTokyoExact && resetVal === "",
        `expected=${tokyoExpected}, resetVal=${JSON.stringify(resetVal)}, got=${JSON.stringify(tokyoStored.map((t) => ({ id: t.id, state: t.state, deferredUntil: t.deferredUntil })))}`,
      );
    } finally {
      await tokyoCtx.ctx.close();
    }

    // Release the 2 busy tasks so both workers are FREE and let live orchestrator run 5+ poll cycles!
    fs.writeFileSync(path.join(WORK_DIR, "release-area4"), "1");
    await waitFor(async () => {
      const states = await Promise.all(
        busyIds.map(async (id) => (await api("GET", `/api/tasks/${id}`, undefined, "alice@example.com")).body.state),
      );
      return states.every((s) => s === "closed") ? states : null;
    }, 30000, 400);
    await new Promise((r) => setTimeout(r, 2500));

    const postFree = await Promise.all(
      [...laIds, ...tokyoIds].map(async (id) => (await api("GET", `/api/tasks/${id}`, undefined, "alice@example.com")).body),
    );
    const allStillDeferred = postFree.every((t) => t.state === "deferred" && (t.attempts || []).length === 0);
    record(
      "Area 4",
      "Live orchestrator with free workers respects all 6 deferred tasks (zero dispatches)",
      allStillDeferred,
      `states=${JSON.stringify(postFree.map((t) => ({ id: t.id, state: t.state, attempts: (t.attempts || []).length })))}`,
    );
    for (const id of [...laIds, ...tokyoIds]) {
      await api("POST", `/api/tasks/${id}/close`, {}, "alice@example.com");
    }
  }

  // =========================================================================
  // AREA 5: #comment= deep links on >120-comment thread with live polling
  // =========================================================================
  {
    // Seed task B with 125 real comments!
    const taskB = (await api("POST", "/api/tasks", {
      title: "Area 5 multi-page conversation B",
      description: "Seeded with >120 real comments for deep-link walkthrough.",
      noRepo: true,
      approve: false,
    }, "alice@example.com")).body;
    const commentIds = [];
    for (let i = 1; i <= 124; i++) {
      const c = await api(
        "POST",
        `/api/tasks/${taskB.id}/comments`,
        { body: `Seeded comment #${i} on task B.` },
        "alice@example.com",
      );
      assert(c.ok, `comment ${i} failed: ${c.raw}`);
      commentIds.push(c.body.id ?? c.body.comment?.id);
    }
    // Read back exact comment IDs (in case POST /comments returns task or comment)
    const detailFirst = (await api("GET", `/api/tasks/${taskB.id}?comments_limit=500`, undefined, "alice@example.com")).body;
    const allIds = detailFirst.comments.map((c) => c.id);
    const oldId1 = allIds[0];
    const oldId2 = allIds[1];
    // Post comment #125 with bare #comment= links + #50%off link
    await api(
      "POST",
      `/api/tasks/${taskB.id}/comments`,
      {
        body:
          `Links: [old one](#comment=${oldId1}) · [old two](#comment=${oldId2}) · [bad pct](#50%off)`,
      },
      "alice@example.com",
    );

    // Seed task A with cross-task pane link to task B's oldId1
    const taskA = (await api("POST", "/api/tasks", {
      title: "Area 5 cross-task link A",
      description: "Holds a cross-task link into task B.",
      noRepo: true,
      approve: false,
    }, "alice@example.com")).body;
    await api(
      "POST",
      `/api/tasks/${taskA.id}/comments`,
      { body: `Cross-task link: [jump to B old](/next/tasks?tab=proposed&task=${taskB.id}#comment=${oldId1})` },
      "alice@example.com",
    );

    const { ctx, page, pageErrors, consoleErrors } = await newUserContext(
      browser,
      "alice@example.com",
    );
    try {
      // 5a. Direct visit /next/task/<B>#comment=<oldId1> + focus ring outlasts fill + live polling seam
      await page.goto(`${PROXY}/next/task/${taskB.id}#comment=${oldId1}`);
      const c1 = page.locator(`article.msg[data-comment-id="${oldId1}"]`);
      await c1.waitFor({ state: "visible", timeout: 15000 });
      await page.waitForTimeout(300);
      const focusedImmediately = await c1.evaluate((el) => document.activeElement === el);
      const hasHighlightImmediately = await c1.evaluate((el) => el.classList.contains("comment-highlight"));
      const inView = await c1.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const th = el.closest('[role="log"]').getBoundingClientRect();
        return r.top >= th.top - 2 && r.bottom <= th.bottom + 2 && r.top >= 0 && r.bottom <= window.innerHeight;
      });
      await page.screenshot({ path: path.join(SHOT_DIR, "area5-comment-deeplink-desktop-light.png") });
      await checkAxeAndOverflow(page, "Area 5 comment deep-link desktop light");

      // Post 2 new comments while paged back and let live polling run (also crosses 4.5s HIGHLIGHT_MS!)
      await api("POST", `/api/tasks/${taskB.id}/comments`, { body: "Live poll seam comment 126" }, "alice@example.com");
      await api("POST", `/api/tasks/${taskB.id}/comments`, { body: "Live poll seam comment 127" }, "alice@example.com");

      // Wait 4.8s so both live polling fires AND HIGHLIGHT_MS (4000ms) expires!
      await page.waitForTimeout(4800);
      const afterFillFadeHighlight = await c1.evaluate((el) => el.classList.contains("comment-highlight"));
      const afterFillFadeFocused = await c1.evaluate((el) => document.activeElement === el);
      const afterFillFadeOutline = await c1.evaluate((el) => getComputedStyle(el).outlineStyle);
      const afterFillFadeTabindex = await c1.getAttribute("tabindex");

      record(
        "Area 5",
        "Direct #comment= lands in view, focuses, highlights; focus ring outlasts fill",
        focusedImmediately &&
          hasHighlightImmediately &&
          inView &&
          !afterFillFadeHighlight &&
          afterFillFadeFocused &&
          afterFillFadeOutline !== "none",
        `immediate:(focused=${focusedImmediately},highlight=${hasHighlightImmediately},inView=${inView}); after 4.8s:(highlight=${afterFillFadeHighlight},focused=${afterFillFadeFocused},outline=${afterFillFadeOutline},tabindex=${JSON.stringify(afterFillFadeTabindex)})`,
      );

      // Check zero comments dropped at the seam after live polling!
      const renderedIds = await page.locator("article.msg[data-comment-id]").evaluateAll((els) =>
        els.map((e) => Number(e.getAttribute("data-comment-id"))),
      );
      const latestDetail = (await api("GET", `/api/tasks/${taskB.id}?comments_limit=500`, undefined, "alice@example.com")).body;
      const expectedAllIds = latestDetail.comments.map((c) => c.id);
      const missingAtSeam = expectedAllIds.filter((id) => !renderedIds.includes(id));
      record(
        "Area 5",
        "Live polling while paged back drops zero comments at the seam (>120 comments)",
        expectedAllIds.length >= 127 && missingAtSeam.length === 0,
        `expected=${expectedAllIds.length}, rendered=${renderedIds.length}, missing=${JSON.stringify(missingAtSeam)}`,
      );

      // 5b. Cross-task pane link /next/tasks?tab=proposed&task=<B>#comment=<oldId1> from task A + Back/Forward
      await page.goto(`${PROXY}/next/tasks?tab=proposed&task=${taskA.id}&pane=conversation`);
      await page.getByRole("link", { name: "jump to B old" }).waitFor({ state: "visible", timeout: 15000 });
      await page.getByRole("link", { name: "jump to B old" }).click();
      await page.waitForURL(new RegExp(`task=${taskB.id}#comment=${oldId1}`));
      const c1Pane = page.locator(`article.msg[data-comment-id="${oldId1}"]`);
      await c1Pane.waitFor({ state: "visible", timeout: 15000 });
      await page.waitForTimeout(300);
      const crossFocused = await c1Pane.evaluate((el) => document.activeElement === el);

      await page.goBack();
      await page.waitForURL(new RegExp(`task=${taskA.id}`));
      let backSawLink = false;
      try {
        await page.getByRole("link", { name: "jump to B old" }).waitFor({ state: "visible", timeout: 10000 });
        backSawLink = true;
      } catch {}
      await page.goForward();
      await page.waitForURL(new RegExp(`task=${taskB.id}#comment=${oldId1}`));
      await c1Pane.waitFor({ state: "visible", timeout: 15000 });
      await page.waitForTimeout(300);
      const fwdFocused = await c1Pane.evaluate((el) => document.activeElement === el);
      const missingToastCount = await page.getByText(/is not in this task's conversation/).count();
      record(
        "Area 5",
        "Cross-task pane link + Back/Forward lands on target comment",
        crossFocused && backSawLink && fwdFocused && missingToastCount === 0,
        `crossFocused=${crossFocused}, backSawLink=${backSawLink}, fwdFocused=${fwdFocused}, missingToast=${missingToastCount}`,
      );

      // 5c. Rapid two #comment= clicks (in-flight coalescing) + #50%off no throw
      let pageBackCalls = 0;
      await page.route(new RegExp(`/api/tasks/${taskB.id}\\?`), async (route) => {
        const u = new URL(route.request().url());
        if (u.searchParams.has("comments_before")) {
          pageBackCalls++;
          await new Promise((r) => setTimeout(r, 500));
        }
        await route.continue();
      });
      await page.goto(`${PROXY}/next/task/${taskB.id}`);
      await page.getByRole("link", { name: "old one" }).waitFor({ state: "visible", timeout: 15000 });
      // Rapidly click old one, then 80ms later click old two while page-back is in flight!
      await page.getByRole("link", { name: "old one" }).click();
      await page.waitForTimeout(80);
      await page.getByRole("link", { name: "old two" }).click();
      const c2 = page.locator(`article.msg[data-comment-id="${oldId2}"]`);
      await c2.waitFor({ state: "visible", timeout: 15000 });
      await page.waitForTimeout(700);
      const c2Focused = await c2.evaluate((el) => document.activeElement === el);
      const finalHash = await page.evaluate(() => location.hash);
      await page.unroute(new RegExp(`/api/tasks/${taskB.id}\\?`));
      record(
        "Area 5",
        "Rapid two #comment= link clicks coalesce in-flight page-back cleanly",
        c2Focused && finalHash === `#comment=${oldId2}` && pageBackCalls === 1,
        `c2Focused=${c2Focused}, finalHash=${finalHash}, comments_before_calls=${pageBackCalls}`,
      );

      // Click #50%off link (must not throw)
      const errorsBeforePct = pageErrors.length;
      await page.getByRole("link", { name: "bad pct" }).click();
      await page.waitForTimeout(300);
      record(
        "Area 5",
        "#50%off bare link does not throw",
        pageErrors.length === errorsBeforePct,
        `newPageErrors=${pageErrors.length - errorsBeforePct}, hash=${await page.evaluate(() => location.hash)}`,
      );

      // 5d. Intercept page-back fetch with 503 -> shows error, never "not in this task's conversation"
      // Need a task with >500 comments OR intercept comments_before on task B when initial load is 100 comments
      // (open /next/task/<B> first so only newest 100 are loaded, then click [old one] which triggers comments_before!)
      await page.goto(`${PROXY}/next/task/${taskB.id}`);
      await page.getByRole("link", { name: "old one" }).waitFor({ state: "visible", timeout: 15000 });
      await page.route(new RegExp(`/api/tasks/${taskB.id}\\?`), async (route) => {
        const u = new URL(route.request().url());
        if (u.searchParams.has("comments_before")) {
          return route.fulfill({
            status: 503,
            contentType: "application/json",
            body: JSON.stringify({ error: "database busy on page-back" }),
          });
        }
        await route.continue();
      });
      await page.getByRole("link", { name: "old one" }).click();
      await page.getByText("database busy on page-back").waitFor({ state: "visible", timeout: 10000 });
      const falseMissing503 = await page.getByText(/is not in this task's conversation/).count();
      await page.unroute(new RegExp(`/api/tasks/${taskB.id}\\?`));
      record(
        "Area 5",
        "503 page-back error shows error banner and never 'not in this task's conversation'",
        falseMissing503 === 0,
        `falseMissingCount=${falseMissing503}`,
      );
    } finally {
      await ctx.close();
    }

    // 5e. Phone (390px): /next/tasks?tab=proposed&task=<B>#comment=<oldId1> opens full page /next/task/<B>#comment=<oldId1>
    const phoneCtx = await newUserContext(browser, "alice@example.com", {
      viewport: { width: 390, height: 844 },
    });
    try {
      await phoneCtx.page.goto(`${PROXY}/next/tasks?tab=proposed&task=${taskB.id}#comment=${oldId1}`);
      await phoneCtx.page.waitForURL(new RegExp(`/next/task/${taskB.id}.*#comment=${oldId1}`), { timeout: 15000 });
      const pc1 = phoneCtx.page.locator(`article.msg[data-comment-id="${oldId1}"]`);
      await pc1.waitFor({ state: "visible", timeout: 15000 });
      await phoneCtx.page.waitForTimeout(300);
      const phoneFocused = await pc1.evaluate((el) => document.activeElement === el);
      await phoneCtx.page.screenshot({ path: path.join(SHOT_DIR, "area5-comment-deeplink-phone-light.png") });
      await checkAxeAndOverflow(phoneCtx.page, "Area 5 comment deep-link phone light");
      record(
        "Area 5",
        "Phone opens pane #comment= link as full task page & focuses comment",
        phoneFocused,
        `url=${phoneCtx.page.url()}, focused=${phoneFocused}`,
      );
    } finally {
      await phoneCtx.ctx.close();
    }

    await api("POST", `/api/tasks/${taskA.id}/close`, {}, "alice@example.com");
    await api("POST", `/api/tasks/${taskB.id}/close`, {}, "alice@example.com");
  }

  // =========================================================================
  // AREA 6: Primary `Retry merge` header action on a real `merge_failed` task
  // =========================================================================
  {
    // Instruct ghsim to refuse merges so the real merge queue produces merge_failed!
    await ghsim("/_sim/refuse-merges?value=Branch+protection+rule+refused+merge");

    // Create a parent task so our child task belongs to a real workstream for /next/workstreams!
    const wsParent = (await api("POST", "/api/tasks", {
      title: "Area 6 workstream root",
      description: "Workstream root for Retry merge walkthrough.",
      repo: "acme/widgets",
      approve: false,
    }, "alice@example.com")).body;
    const mergeTask = (await api("POST", "/api/tasks", {
      title: "Area 6 merge_failed walkthrough",
      description: "Pushes a real branch to ghsim and hits merge refusal.",
      repo: "acme/widgets",
      origin: `task:${wsParent.id}`,
      autoMerge: true,
      approve: false,
    }, "alice@example.com")).body;

    writeAction(mergeTask.id, {
      kind: "mcp_sequence",
      calls: [
        {
          tool: "run_command",
          args: {
            command: `cd work && echo "merge test ${mergeTask.id}" > feature-${mergeTask.id}.txt && git add feature-${mergeTask.id}.txt && git commit -m "feat: add feature-${mergeTask.id}\n\nWalkthrough change for Retry merge." && git push origin HEAD`,
            description: "committing and pushing walkthrough branch",
            timeout: 120000,
          },
        },
        {
          tool: "comment_on_issue",
          args: { comment: "Pushed branch for Retry merge walkthrough." },
        },
      ],
    });
    await api("POST", `/api/tasks/${mergeTask.id}/approve`, {}, "alice@example.com");

    // Wait for real daemon to run agent, open PR, run CI, attempt merge, and land in merge_failed!
    const failedDetail = await waitFor(async () => {
      const d = (await api("GET", `/api/tasks/${mergeTask.id}`, undefined, "alice@example.com")).body;
      if (d.state === "completed" && d.mergeQueueState === "merge_failed") return d;
      return null;
    }, 90000, 500);

    record(
      "Area 6",
      "Real daemon + ghsim produced genuine merge_failed task",
      failedDetail.state === "completed" && failedDetail.mergeQueueState === "merge_failed",
      `id=${mergeTask.id}, pr=${failedDetail.pullRequest}, state=${failedDetail.state}, mergeQueueState=${failedDetail.mergeQueueState}, reason=${JSON.stringify(failedDetail.reason)}`,
    );

    const aliceCtx = await newUserContext(browser, "alice@example.com");
    const alicePhoneCtx = await newUserContext(browser, "alice@example.com", {
      viewport: { width: 390, height: 844 },
      colorScheme: "dark",
    });
    const carolCtx = await newUserContext(browser, "carol@example.com");

    try {
      // 6a. /next/task/<id> desktop
      await aliceCtx.page.goto(`${PROXY}/next/task/${mergeTask.id}`);
      const actions = aliceCtx.page.getByRole("group", { name: "Task actions" });
      await actions.getByTestId("act-retry-merge").waitFor({ state: "visible" });
      const taskStateTxt = (await aliceCtx.page.getByTestId("task-state").textContent()).trim();
      const firstBtnTxt = (await actions.getByRole("button").first().textContent()).trim();
      const isPrimary = await actions.getByTestId("act-retry-merge").evaluate((el) => el.classList.contains("btn-primary"));
      const runNowInBar = await actions.locator('> button[data-testid="act-run-now"]').count();
      await actions.getByTestId("act-more").click();
      await aliceCtx.page.getByTestId("act-more-menu").waitFor({ state: "visible" });
      const runNowInMore = await aliceCtx.page.getByTestId("act-more-menu").getByTestId("act-run-now").isVisible();
      await aliceCtx.page.screenshot({ path: path.join(SHOT_DIR, "area6-retry-merge-desktop-light.png") });
      await aliceCtx.page.keyboard.press("Escape");
      await checkAxeAndOverflow(aliceCtx.page, "Area 6 Retry merge task page desktop light");

      // Phone dark screenshot of Retry merge header + More menu
      await alicePhoneCtx.page.goto(`${PROXY}/next/task/${mergeTask.id}`);
      await alicePhoneCtx.page.getByTestId("act-retry-merge").waitFor({ state: "visible" });
      await alicePhoneCtx.page.getByTestId("act-more").click();
      await alicePhoneCtx.page.getByTestId("act-more-menu").waitFor({ state: "visible" });
      await alicePhoneCtx.page.screenshot({ path: path.join(SHOT_DIR, "area6-retry-merge-phone-dark.png") });
      await checkAxeAndOverflow(alicePhoneCtx.page, "Area 6 Retry merge phone dark");

      // 6b. /next/tasks?tab=merging&task=<id>
      await aliceCtx.page.goto(`${PROXY}/next/tasks?tab=merging&task=${mergeTask.id}`);
      const paneRetry = aliceCtx.page.getByTestId("tasks-pane").getByTestId("act-retry-merge");
      await paneRetry.waitFor({ state: "visible" });
      const paneRunNowInBar = await aliceCtx.page.getByTestId("tasks-pane").locator('.task-actions > button[data-testid="act-run-now"]').count();

      // 6c. /next/workstreams
      await aliceCtx.page.goto(`${PROXY}/next/workstreams/task:${wsParent.id}?node=${mergeTask.id}`);
      const wsRetry = aliceCtx.page.getByTestId("act-retry-merge");
      await wsRetry.waitFor({ state: "visible", timeout: 15000 });
      const wsRunNowInBar = await aliceCtx.page.locator('.task-actions > button[data-testid="act-run-now"]').count();

      record(
        "Area 6",
        "Primary Retry merge on /next/task, /next/tasks?tab=merging, and /next/workstreams with Run now in More",
        taskStateTxt === "Merge failed" &&
          firstBtnTxt === "Retry merge" &&
          isPrimary &&
          runNowInBar === 0 &&
          runNowInMore &&
          paneRunNowInBar === 0 &&
          wsRunNowInBar === 0,
        `state=${taskStateTxt}, firstBtn=${firstBtnTxt}, isPrimary=${isPrimary}, runNowInBar=${runNowInBar}, runNowInMore=${runNowInMore}, paneVisible=true, wsVisible=true`,
      );

      // 6d. Pausing swaps Retry merge for Resume; Resuming brings Retry merge back
      await aliceCtx.page.goto(`${PROXY}/next/task/${mergeTask.id}`);
      await aliceCtx.page.getByTestId("act-pause").click();
      await aliceCtx.page.getByTestId("act-resume-pause").waitFor({ state: "visible" });
      const retryWhilePaused = await aliceCtx.page.getByTestId("act-retry-merge").count();
      await aliceCtx.page.getByTestId("act-resume-pause").click();
      await aliceCtx.page.getByTestId("act-retry-merge").waitFor({ state: "visible" });
      record(
        "Area 6",
        "Pausing swaps Retry merge for Resume; Resuming restores Retry merge",
        retryWhilePaused === 0,
        `retryWhilePaused=${retryWhilePaused}, restoredAfterResume=true`,
      );

      // 6e. Viewer (carol, no merge:write) cannot see/click Retry merge (and API returns 403)
      await carolCtx.page.goto(`${PROXY}/next/task/${mergeTask.id}`);
      await carolCtx.page.getByTestId("task-state").waitFor({ state: "visible" });
      const carolRetryCount = await carolCtx.page.getByTestId("act-retry-merge").count();
      const carolRetryApi = await api("POST", `/api/tasks/${mergeTask.id}/retry-merge`, {}, "carol@example.com");
      record(
        "Area 6",
        "Viewer (carol, no merge:write) cannot click Retry merge & API refuses 403",
        carolRetryCount === 0 && carolRetryApi.status === 403,
        `carolRetryCount=${carolRetryCount}, carolAPIStatus=${carolRetryApi.status}`,
      );

      // 6f. Clear merge refusal on ghsim, click Retry merge -> live transition to Merging without reload!
      await ghsim("/_sim/refuse-merges?value=");
      let navs = 0;
      aliceCtx.page.on("framenavigated", (frame) => {
        if (frame === aliceCtx.page.mainFrame()) navs++;
      });
      const retryPost = aliceCtx.page.waitForResponse(
        (r) =>
          r.request().method() === "POST" &&
          new URL(r.url()).pathname === `/api/tasks/${mergeTask.id}/retry-merge`,
      );
      await aliceCtx.page.getByTestId("act-retry-merge").click();
      const retryResp = await retryPost;
      await aliceCtx.page.getByTestId("act-retry-merge").waitFor({ state: "hidden" });
      const liveState = (await aliceCtx.page.getByTestId("task-state").textContent()).trim();
      const liveHasError = await aliceCtx.page.getByTestId("task-state").evaluate((el) => el.classList.contains("error"));

      // Wait for real merge queue to actually merge the PR on ghsim and close the task!
      const mergedFinal = await waitFor(async () => {
        const d = (await api("GET", `/api/tasks/${mergeTask.id}`, undefined, "alice@example.com")).body;
        return d.state === "closed" ? d : null;
      }, 90000, 500);

      record(
        "Area 6",
        "Clicking Retry merge transitions live to Merging without reload and lands PR on ghsim",
        retryResp.status() === 200 &&
          navs === 0 &&
          (liveState === "Merging" || liveState === "Closed") &&
          !liveHasError &&
          mergedFinal.state === "closed",
        `POST=${retryResp.status()}, navs=${navs}, liveState=${liveState}, liveHasError=${liveHasError}, finalState=${mergedFinal.state}`,
      );
    } finally {
      await aliceCtx.ctx.close();
      await alicePhoneCtx.ctx.close();
      await carolCtx.ctx.close();
      await api("POST", `/api/tasks/${wsParent.id}/close`, {}, "alice@example.com");
    }
  }

  // =========================================================================
  // AREA 7: Additional Responsive Phone (390px) Light/Dark screenshots + axe
  // =========================================================================
  {
    const tEdit = (await api("POST", "/api/tasks", {
      title: "Area 7 responsive visual check",
      description: "Used for phone light/dark screenshots.",
      repo: "acme/widgets",
      approve: false,
    }, "alice@example.com")).body;
    for (const scheme of ["light", "dark"]) {
      const pCtx = await newUserContext(browser, "alice@example.com", {
        viewport: { width: 390, height: 844 },
        colorScheme: scheme,
      });
      try {
        // Edit panel on phone
        await pCtx.page.goto(`${PROXY}/next/task/${tEdit.id}`);
        await pCtx.page.getByTestId("act-edit").click();
        await pCtx.page.getByTestId("task-edit-panel").waitFor({ state: "visible" });
        await pCtx.page.screenshot({ path: path.join(SHOT_DIR, `area7-edit-phone-${scheme}.png`) });
        await checkAxeAndOverflow(pCtx.page, `Area 7 Edit panel phone ${scheme}`);

        // Inline close row on phone
        await pCtx.page.getByTestId("edit-close-btn").click();
        await pCtx.page.getByTestId("act-close").click();
        await pCtx.page.getByTestId("close-confirm").waitFor({ state: "visible" });
        await pCtx.page.screenshot({ path: path.join(SHOT_DIR, `area7-close-phone-${scheme}.png`) });
        await checkAxeAndOverflow(pCtx.page, `Area 7 close row phone ${scheme}`);

        // New task sheet on phone
        await pCtx.page.goto(`${PROXY}/next/tasks`);
        await pCtx.page.getByTestId("tab-backlog").waitFor({ state: "visible" });
        await pCtx.page.locator("body").press("c");
        await pCtx.page.getByTestId("new-task-sheet").waitFor({ state: "visible" });
        await pCtx.page.screenshot({ path: path.join(SHOT_DIR, `area7-newtask-phone-${scheme}.png`) });
        await checkAxeAndOverflow(pCtx.page, `Area 7 New task sheet phone ${scheme}`);
        await pCtx.page.keyboard.press("Escape");

        // Batch defer-until on phone (Needs approval tab has tEdit!)
        await pCtx.page.goto(`${PROXY}/next/tasks?tab=proposed`);
        await pCtx.page.getByLabel(`Select #${tEdit.id}`, { exact: true }).check();
        await pCtx.page.getByTestId("batch-defer-until-open").click();
        await pCtx.page.getByTestId("batch-defer-date").waitFor({ state: "visible" });
        await pCtx.page.screenshot({ path: path.join(SHOT_DIR, `area7-batch-defer-phone-${scheme}.png`) });
        await checkAxeAndOverflow(pCtx.page, `Area 7 batch defer phone ${scheme}`);
      } finally {
        await pCtx.ctx.close();
      }
    }
    await api("POST", `/api/tasks/${tEdit.id}/close`, {}, "alice@example.com");
  }

  await browser.close();

  const summaryPath = path.join(SHOT_DIR, "results.json");
  fs.writeFileSync(
    summaryPath,
    JSON.stringify({ store: STORE, chromium: version, results, findings }, null, 2),
  );
  console.log(`\n=== SUMMARY (${STORE}): ${results.filter((r) => r.pass).length}/${results.length} passed, ${findings.length} findings ===`);
  if (findings.length) {
    console.log("FINDINGS:", JSON.stringify(findings, null, 2));
  }
}

run().catch((err) => {
  console.error("WALKTHROUGH ERROR:", err);
  process.exit(1);
});
