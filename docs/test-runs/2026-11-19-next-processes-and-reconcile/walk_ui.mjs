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

// Real-daemon Playwright walkthrough for docs/test-plans/2026-11-19.md §4:
//   1. Templates & firing (Task priority 1e3 -> 1000, clear -> 0/default 5,
//      Interactive switch, "New task from this template" from inspector and
//      edit dialog -> Launch modal with live-chat note vs New task sheet
//      carrying priority; submit and verify created task flags).
//   2. Schedules (Network access, Reads, two Writes targeting different
//      branches of the same repository acme/widgets@main & @release/v1,
//      declared Custom fields, Configuration pane Admission/Hosting/Keep
//      running beside Model, draft composition preview vs actual fired task,
//      single-field change-only PATCH, unsaved-edits guard on Escape/Cancel/
//      scrim/rail click).
//   3. Suites & stacked dialog (searchable multi-select Templates combobox,
//      inline "+ New template" over inert suite dialog, Escape closes top
//      dialog only & restores focus, saving new template selects it).
//   4. Scoped RBAC refusals on the real daemon via verified IAP assertions
//      (Bob with suites:write on acme/widgets only editing suite whose 2nd
//      template targets acme/gadgets -> Save disabled + tooltip names
//      acme/gadgets; Carol without templates:write -> "+ New template"
//      disabled in place without layout shift + tooltip).
//   5. Rail Processes count badge accuracy + /next/agents Details scroll
//      stability during live task state transitions + 1440px & 390px,
//      light & dark screenshots, zero horizontal overflow, and axe audit.

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(
  path.resolve(process.cwd(), "ui-ng/package.json"),
);
const { chromium } = require("playwright");
const AxeBuilder = require("@axe-core/playwright").default;

const BASE = process.env.DAEMON_URL || "http://127.0.0.1:4290";
const CODE = process.env.ACCESS_CODE || "";
const BOB_JWT = process.env.BOB_JWT || "";
const CAROL_JWT = process.env.CAROL_JWT || "";
const STORE_LABEL = process.env.STORE || "sqlite";
const OUT_DIR = process.env.OUT_DIR || `/tmp/walk-proc-${STORE_LABEL}/ui`;

fs.mkdirSync(OUT_DIR, { recursive: true });
fs.mkdirSync(path.join(OUT_DIR, "screens"), { recursive: true });

const report = {
  store: STORE_LABEL,
  base: BASE,
  chromiumVersion: "",
  steps: [],
  findings: [],
  pageErrors: [],
  consoleErrors: [],
  http5xx: [],
  sweeps: [],
};

function record(name, pass, details = {}) {
  report.steps.push({ name, pass, details });
  const tag = pass ? "PASS" : "FAIL";
  console.log(`[${STORE_LABEL}] [${tag}] ${name}: ${JSON.stringify(details)}`);
  if (!pass) {
    report.findings.push({ name, details });
  }
}

async function apiReq(method, p, body, headers = {}) {
  const res = await fetch(`${BASE}${p}`, {
    method,
    headers: {
      ...(headers.Authorization || headers["X-Goog-IAP-JWT-Assertion"]
        ? {}
        : { Authorization: `Bearer ${CODE}` }),
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...headers,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = null;
  }
  return { status: res.status, text, json };
}

async function signInPage(page) {
  await page.goto(`${BASE}/`, { waitUntil: "domcontentloaded" });
  const res = await page.request.post(`${BASE}/api/auth/access-code`, {
    data: { code: CODE, name: "alice@example.com" },
  });
  if (!res.ok()) {
    throw new Error(`access-code sign-in failed: ${res.status()} ${await res.text()}`);
  }
}

function attachListeners(page, label) {
  page.on("pageerror", (err) => {
    report.pageErrors.push({ label, message: String(err) });
  });
  page.on("console", (msg) => {
    if (msg.type() === "error") {
      report.consoleErrors.push({ label, text: msg.text() });
    }
  });
  page.on("response", (res) => {
    if (res.status() >= 500) {
      report.http5xx.push({
        label,
        url: res.url(),
        status: res.status(),
        method: res.request().method(),
      });
    }
  });
}

async function setTheme(page, theme) {
  await page.evaluate((t) => {
    localStorage.setItem("grain.themeMode", t);
    document.documentElement.setAttribute("data-theme", t);
  }, theme);
  await page.waitForTimeout(100);
}

async function pickPill(page, testid, value) {
  await page.getByTestId(testid).click();
  await page.getByTestId(`${testid}-opt-${value}`).click();
}

async function runAxe(page) {
  const res = await new AxeBuilder({ page }).analyze();
  const severe = res.violations.filter((v) =>
    ["critical", "serious"].includes(v.impact || ""),
  );
  return {
    total: res.violations.length,
    severeCount: severe.length,
    severe: severe.map((v) => ({
      id: v.id,
      impact: v.impact,
      help: v.help,
      nodes: v.nodes.slice(0, 3).map((n) => n.html),
    })),
  };
}

async function checkOverflow(page) {
  return await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
    overflowPx: document.documentElement.scrollWidth - window.innerWidth,
  }));
}

async function main() {
  const browser = await chromium.launch({ headless: true });
  report.chromiumVersion = browser.version();
  console.log(`[${STORE_LABEL}] Chromium ${report.chromiumVersion}`);

  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  attachListeners(page, "alice-desktop");
  await signInPage(page);

  // ---------------------------------------------------------------------------
  // 0. Baseline rail Processes badge
  // ---------------------------------------------------------------------------
  await page.goto(`${BASE}/next/inbox`, { waitUntil: "networkidle" });
  const [s0, t0, st0] = await Promise.all([
    apiReq("GET", "/api/schedules"),
    apiReq("GET", "/api/templates"),
    apiReq("GET", "/api/suites"),
  ]);
  const initialCount =
    (s0.json?.length || 0) + (t0.json?.length || 0) + (st0.json?.length || 0);
  const railBadge = page.getByTestId("rail-processes-count");
  await railBadge.waitFor({ state: "visible", timeout: 10000 });
  const initialBadgeText = (await railBadge.textContent())?.trim();
  record("rail_badge_initial", initialBadgeText === String(initialCount), {
    initialCount,
    initialBadgeText,
  });

  // ---------------------------------------------------------------------------
  // 1. Templates: Task priority 1e3 -> 1000, clear -> 0 (default 5),
  //    Interactive switch, "New task from this template" from inspector &
  //    edit dialog -> Launch modal live-chat note & New task sheet priority.
  // ---------------------------------------------------------------------------
  await page.getByTestId("rail-processes").click();
  await page.getByTestId("proc-tab-templates").click();
  await page.getByTestId("proc-new-template-btn").waitFor({ state: "visible" });
  const hasMenuOnTemplates = (await page.getByTestId("proc-new-menu-btn").count()) > 0;
  record("templates_tab_direct_new_button", !hasMenuOnTemplates, {
    hasMenuOnTemplates,
  });

  // 1a. Create interactive template WITH input fields and Task priority "1e3"
  await page.getByTestId("proc-new-template-btn").click();
  const tmplDlg = page.getByTestId("template-dialog");
  await tmplDlg.waitFor({ state: "visible" });
  await tmplDlg.getByTestId("tmpl-name-input").fill("walk-int-fields");
  await tmplDlg.getByTestId("tmpl-title-input").fill("chat: triage {{area}}");
  await tmplDlg.getByTestId("tmpl-desc-input").fill("Interactive triage for {{area}}");
  await tmplDlg.getByTestId("tmpl-repo-input").fill("acme/widgets");
  await tmplDlg.getByTestId("tmpl-params-input").fill("area");
  await tmplDlg.getByTestId("tmpl-interactive").click();
  await tmplDlg.getByTestId("tmpl-priority-input").fill("1e3");

  const [createTmplReq, createTmplRes] = await Promise.all([
    page.waitForRequest(
      (r) => r.method() === "POST" && /\/api\/templates$/.test(r.url()),
    ),
    page.waitForResponse(
      (r) => r.request().method() === "POST" && /\/api\/templates$/.test(r.url()),
    ),
    tmplDlg.getByTestId("tmpl-save-btn").click(),
  ]);
  const createTmplReqBody = JSON.parse(createTmplReq.postData() || "{}");
  const createdTmplFields = await createTmplRes.json();
  await tmplDlg.waitFor({ state: "hidden" });

  // Verify persisted via API (GET /api/templates) and inspector Configuration pane
  const allTmpls1 = (await apiReq("GET", "/api/templates")).json || [];
  const fetchedTmpl1 = allTmpls1.find((t) => t.id === createdTmplFields.id);
  const tmplCfgText = await page.getByTestId("tmpl-config-pane").textContent();
  record(
    "template_create_priority_1e3_and_interactive",
    createTmplRes.status() < 300 &&
      createTmplReqBody.priority === 1000 &&
      fetchedTmpl1?.priority === 1000 &&
      fetchedTmpl1?.interactive === true &&
      (fetchedTmpl1?.fields || []).length === 1 &&
      tmplCfgText?.includes("1000") &&
      tmplCfgText?.includes("Interactive"),
    {
      reqPriority: createTmplReqBody.priority,
      storedPriority: fetchedTmpl1?.priority,
      storedInteractive: fetchedTmpl1?.interactive,
      fieldsCount: (fetchedTmpl1?.fields || []).length,
      cfgHas1000: tmplCfgText?.includes("1000"),
      cfgHasInteractive: tmplCfgText?.includes("Interactive"),
    },
  );

  // 1b. Launch modal from inspector AND from edit dialog -> verify live-chat note, submit & verify task
  await page.getByTestId("tmpl-new-task-btn").click();
  const launchDlg = page.getByTestId("launch-template-dialog");
  await launchDlg.waitFor({ state: "visible" });
  const liveChatNoteVisibleInspector = await launchDlg
    .getByTestId("launch-interactive-note")
    .isVisible();
  const liveChatNoteText = (
    await launchDlg.getByTestId("launch-interactive-note").textContent()
  )?.trim();
  await page.keyboard.press("Escape");
  await launchDlg.waitFor({ state: "hidden" });

  // From edit dialog -> "New task from this template"
  await page.getByTestId("tmpl-edit-btn").click();
  await tmplDlg.waitFor({ state: "visible" });
  await tmplDlg.getByTestId("tmpl-dialog-new-task-btn").click();
  await tmplDlg.waitFor({ state: "hidden" });
  await launchDlg.waitFor({ state: "visible" });
  const liveChatNoteVisibleEdit = await launchDlg
    .getByTestId("launch-interactive-note")
    .isVisible();

  // Fill parameter and submit Launch modal!
  const fieldInput = launchDlg.locator('input[data-testid^="launch-field-"]').first();
  await fieldInput.fill("billing");
  const [launchTaskRes] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === "POST" && /\/api\/tasks$/.test(r.url()),
    ),
    launchDlg.getByTestId("launch-template-submit").click(),
  ]);
  const launchedTask = await launchTaskRes.json();
  const fetchedLaunchedTask = (await apiReq("GET", `/api/tasks/${launchedTask.id}`)).json;
  record(
    "template_fields_launch_modal_and_submit",
    liveChatNoteVisibleInspector &&
      liveChatNoteVisibleEdit &&
      launchTaskRes.status() < 300 &&
      fetchedLaunchedTask?.priority === 1000 &&
      fetchedLaunchedTask?.interactive === true,
    {
      liveChatNoteVisibleInspector,
      liveChatNoteVisibleEdit,
      liveChatNoteText,
      taskId: launchedTask?.id,
      taskPriority: fetchedLaunchedTask?.priority,
      taskInteractive: fetchedLaunchedTask?.interactive,
      taskTitle: fetchedLaunchedTask?.title,
    },
  );

  // 1c. Edit template and clear priority -> verify PATCH sends 0 and resets to default 5
  await page.goto(`${BASE}/next/processes/templates/${createdTmplFields.id}`, {
    waitUntil: "networkidle",
  });
  await page.getByTestId("tmpl-edit-btn").click();
  await tmplDlg.waitFor({ state: "visible" });
  await tmplDlg.getByTestId("tmpl-priority-input").fill("");
  const [clearPriReq, clearPriRes] = await Promise.all([
    page.waitForRequest(
      (r) => r.method() === "PATCH" && r.url().includes(`/api/templates/${createdTmplFields.id}`),
    ),
    page.waitForResponse(
      (r) => r.request().method() === "PATCH" && r.url().includes(`/api/templates/${createdTmplFields.id}`),
    ),
    tmplDlg.getByTestId("tmpl-save-btn").click(),
  ]);
  const clearPriReqBody = JSON.parse(clearPriReq.postData() || "{}");
  const allTmplsCleared = (await apiReq("GET", "/api/templates")).json || [];
  const clearedTmpl = allTmplsCleared.find((t) => t.id === createdTmplFields.id);
  await tmplDlg.waitFor({ state: "hidden" });

  // Launch again after clearing priority -> verify created task gets default priority 5!
  await page.getByTestId("tmpl-new-task-btn").click();
  await launchDlg.waitFor({ state: "visible" });
  await launchDlg.locator('input[data-testid^="launch-field-"]').first().fill("payments");
  const [launchResetRes] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === "POST" && /\/api\/tasks$/.test(r.url()),
    ),
    launchDlg.getByTestId("launch-template-submit").click(),
  ]);
  const launchedResetTask = await launchResetRes.json();
  const fetchedResetTask = (await apiReq("GET", `/api/tasks/${launchedResetTask.id}`)).json;
  record(
    "template_clear_priority_sends_zero_resets_to_5",
    clearPriRes.status() < 300 &&
      clearPriReqBody.priority === 0 &&
      (!clearedTmpl?.priority || clearedTmpl.priority === 0) &&
      fetchedResetTask?.priority === 5 &&
      fetchedResetTask?.interactive === true,
    {
      patchPriority: clearPriReqBody.priority,
      storedPriority: clearedTmpl?.priority ?? 0,
      firedTaskPriority: fetchedResetTask?.priority,
      firedTaskInteractive: fetchedResetTask?.interactive,
    },
  );

  // 1d. Create interactive template WITHOUT input fields and priority 1e3 ->
  //     "New task from this template" from inspector AND edit dialog opens New task sheet
  //     carrying priority 1000; submit and verify created task carries exact priority & interactive flags!
  await page.goto(`${BASE}/next/processes/templates`, { waitUntil: "networkidle" });
  await page.getByTestId("proc-new-template-btn").click();
  await tmplDlg.waitFor({ state: "visible" });
  await tmplDlg.getByTestId("tmpl-name-input").fill("walk-int-nofields");
  await tmplDlg.getByTestId("tmpl-title-input").fill("chat: no-field quick session");
  await tmplDlg.getByTestId("tmpl-desc-input").fill("Interactive template without fields");
  await tmplDlg.getByTestId("tmpl-repo-input").fill("acme/widgets");
  await tmplDlg.getByTestId("tmpl-interactive").click();
  await tmplDlg.getByTestId("tmpl-priority-input").fill("1e3");
  const [createNoFieldsRes] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === "POST" && /\/api\/templates$/.test(r.url()),
    ),
    tmplDlg.getByTestId("tmpl-save-btn").click(),
  ]);
  const tmplNoFields = await createNoFieldsRes.json();
  await tmplDlg.waitFor({ state: "hidden" });

  // From inspector -> New task sheet carries priority 1000
  await page.getByTestId("tmpl-new-task-btn").click();
  const sheet = page.getByTestId("new-task-sheet");
  await sheet.waitFor({ state: "visible" });
  const priFromInspector = await sheet.getByTestId("nt-priority").inputValue();
  await page.keyboard.press("Escape");
  if (await page.getByTestId("confirm-accept").isVisible().catch(() => false)) {
    await page.getByTestId("confirm-accept").click();
  }
  await sheet.waitFor({ state: "hidden" });

  // From edit dialog -> New task sheet carries priority 1000; submit!
  await page.getByTestId("tmpl-edit-btn").click();
  await tmplDlg.waitFor({ state: "visible" });
  await tmplDlg.getByTestId("tmpl-dialog-new-task-btn").click();
  await tmplDlg.waitFor({ state: "hidden" });
  await sheet.waitFor({ state: "visible" });
  const priFromEditDialog = await sheet.getByTestId("nt-priority").inputValue();
  const [sheetTaskReq, sheetTaskRes] = await Promise.all([
    page.waitForRequest(
      (r) => r.method() === "POST" && /\/api\/tasks$/.test(r.url()),
    ),
    page.waitForResponse(
      (r) => r.request().method() === "POST" && /\/api\/tasks$/.test(r.url()),
    ),
    sheet.getByTestId("new-task-submit").click(),
  ]);
  const sheetTaskReqBody = JSON.parse(sheetTaskReq.postData() || "{}");
  const sheetTask = await sheetTaskRes.json();
  const fetchedSheetTask = (await apiReq("GET", `/api/tasks/${sheetTask.id}`)).json;
  record(
    "template_nofields_new_task_sheet_priority_and_interactive",
    priFromInspector === "1000" &&
      priFromEditDialog === "1000" &&
      sheetTaskRes.status() < 300 &&
      fetchedSheetTask?.priority === 1000 &&
      fetchedSheetTask?.interactive === true,
    {
      priFromInspector,
      priFromEditDialog,
      reqBody: {
        templateId: sheetTaskReqBody.templateId,
        priority: sheetTaskReqBody.priority,
        interactive: sheetTaskReqBody.interactive,
      },
      taskId: sheetTask?.id,
      taskPriority: fetchedSheetTask?.priority,
      taskInteractive: fetchedSheetTask?.interactive,
    },
  );

  // ---------------------------------------------------------------------------
  // 2. Schedules: Network access, Reads, TWO Writes targeting different branches
  //    of same repo (acme/widgets@main and acme/widgets@release/v1), declared
  //    Custom fields, Configuration pane (Admission/Hosting/Keep running beside
  //    Model), draft composition preview vs actual fired task, single-field
  //    change-only PATCH, unsaved-edits guard on Escape/Cancel/scrim/rail click.
  // ---------------------------------------------------------------------------
  await page.goto(`${BASE}/next/processes/schedules`, { waitUntil: "networkidle" });
  await page.getByTestId("proc-new-menu-btn").click();
  await page.getByTestId("proc-new-schedule").click();
  const schedDlg = page.getByTestId("schedule-dialog");
  await schedDlg.waitFor({ state: "visible" });

  await schedDlg.getByTestId("sched-title-input").fill("Walk multi-branch audit");
  await schedDlg
    .getByTestId("sched-desc-input")
    .fill("Audit widgets across main and release/v1 from gadgets.");
  await schedDlg.getByTestId("sched-repo-input").fill("acme/gadgets");
  await schedDlg.getByTestId("sched-base-input").fill("main");
  await schedDlg.getByTestId("sched-keep-running").click();
  await pickPill(page, "sched-admission", "bypass");
  await pickPill(page, "sched-hosting", "console");
  await pickPill(page, "sched-network", "none");

  // Select explicit Model so Configuration pane shows Admission, Hosting, and Keep running beside Model!
  await schedDlg.getByTestId("sched-model").click();
  await schedDlg.getByTestId("sched-model-panel").waitFor({ state: "visible" });
  await schedDlg.getByTestId("sched-model-opt").first().click();

  // Reads
  await schedDlg.getByTestId("sched-reads-input").fill("acme/docs");
  await schedDlg.getByTestId("sched-reads-add").click();

  // TWO Writes targeting DIFFERENT branches of the SAME repository!
  await schedDlg.getByTestId("sched-writes-repo-input").fill("acme/widgets");
  await schedDlg.getByTestId("sched-writes-base-input").fill("main");
  await schedDlg.getByTestId("sched-writes-add").click();

  await schedDlg.getByTestId("sched-writes-repo-input").fill("acme/widgets");
  await schedDlg.getByTestId("sched-writes-base-input").fill("release/v1");
  await schedDlg.getByTestId("sched-writes-add").click();
  await schedDlg
    .getByTestId("sched-writes-chips")
    .getByText("acme/widgets @ release/v1")
    .waitFor({ state: "visible" });

  const writesChipsText = (
    await schedDlg.getByTestId("sched-writes-chips").textContent()
  )?.trim();

  // Declared Custom fields (enum, text, bool pill, multi-text)
  await pickPill(page, "sched-cf-priority", "high");
  await schedDlg.getByTestId("sched-cf-requested-by").fill("PlatOps");
  await pickPill(page, "sched-cf-urgent", "true");
  const tagsInput = schedDlg.getByTestId("sched-cf-input-tags");
  if ((await tagsInput.count()) > 0) {
    await tagsInput.fill("ops");
    await schedDlg.getByTestId("sched-cf-add-tags").click();
    await tagsInput.fill("nightly");
    await schedDlg.getByTestId("sched-cf-add-tags").click();
  }

  // Draft composition preview
  const [previewReq, previewRes] = await Promise.all([
    page.waitForRequest(
      (r) =>
        r.method() === "POST" && /\/api\/schedules\/composition$/.test(r.url()),
    ),
    page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        /\/api\/schedules\/composition$/.test(r.url()),
    ),
    schedDlg.getByTestId("sched-preview-composition-btn").click(),
  ]);
  const previewReqBody = JSON.parse(previewReq.postData() || "{}");
  const previewJson = await previewRes.json();
  await schedDlg.getByTestId("composition-preview").waitFor({ state: "visible" });
  const previewFields = Object.fromEntries(
    (previewJson.tasks?.[0]?.fields || []).map((f) => [f.name, f.values || []]),
  );

  // Save schedule
  const [schedCreateRes] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === "POST" && /\/api\/schedules$/.test(r.url()),
    ),
    schedDlg.getByTestId("sched-save-btn").click(),
  ]);
  const createdSched = await schedCreateRes.json();
  await schedDlg.waitFor({ state: "hidden" });

  // Verify Configuration pane shows Admission, Hosting, Keep running beside Model, plus Network, Reads, Writes
  const cfgPane = page.getByTestId("sched-config-pane");
  await cfgPane.waitFor({ state: "visible" });
  const cfgValues = {
    model: (await cfgPane.getByTestId("sched-model-val").textContent())?.trim(),
    admission: (await cfgPane.getByTestId("sched-admission-val").textContent())?.trim(),
    hosting: (await cfgPane.getByTestId("sched-hosting-val").textContent())?.trim(),
    keepRunning: (await cfgPane.getByTestId("sched-keep-running-val").textContent())?.trim(),
    network: (await cfgPane.getByTestId("sched-network-val").textContent())?.trim(),
    reads: (await cfgPane.getByTestId("sched-reads-val").textContent())?.trim(),
    writes: (await cfgPane.getByTestId("sched-writes-val").textContent())?.trim(),
  };
  record(
    "schedule_create_multibranch_writes_and_config_pane",
    schedCreateRes.status() < 300 &&
      writesChipsText?.includes("acme/widgets @ main") &&
      writesChipsText?.includes("acme/widgets @ release/v1") &&
      Boolean(cfgValues.model) &&
      cfgValues.admission === "Bypass queue" &&
      cfgValues.hosting === "Interactive console" &&
      cfgValues.keepRunning === "on" &&
      cfgValues.network?.includes("None") &&
      cfgValues.reads === "acme/docs" &&
      cfgValues.writes?.includes("acme/widgets @ main") &&
      cfgValues.writes?.includes("acme/widgets @ release/v1"),
    { writesChipsText, cfgValues, previewWrites: previewFields.writes, previewReqWrites: previewReqBody.writes },
  );

  // Fire the schedule NOW via UI ("Run now") and verify actual fired task matches draft composition preview!
  const [fireRes] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().includes(`/api/schedules/${createdSched.id}/run`),
    ),
    page.getByTestId("sched-run-now-btn").click(),
  ]);
  const fireJson = await fireRes.json();
  const firedTaskId = fireJson.taskId || fireJson.id || fireJson.task?.id;
  const firedTask = (await apiReq("GET", `/api/tasks/${firedTaskId}`)).json;

  const firedWritesFormatted = (firedTask?.writes || []).map((w) =>
    w.base ? `${w.repo} (${w.base})` : w.repo,
  );
  const previewMatchesFired =
    fireRes.status() < 300 &&
    firedTask?.title === previewFields.title?.[0] &&
    firedTask?.repo === previewFields.repo?.[0] &&
    firedTask?.base === previewFields.base?.[0] &&
    JSON.stringify(firedTask?.reads || []) === JSON.stringify(previewFields.reads || []) &&
    JSON.stringify(firedWritesFormatted) === JSON.stringify(previewFields.writes || []) &&
    firedTask?.networkAccess === previewFields.network?.[0] &&
    firedTask?.admission === previewFields.admission?.[0] &&
    firedTask?.hosting === previewFields.hosting?.[0] &&
    Boolean(firedTask?.keepRunning) === (previewFields.keepRunning?.[0] === "on") &&
    firedTask?.customFields?.priority === "high" &&
    firedTask?.customFields?.["requested-by"] === "PlatOps" &&
    firedTask?.customFields?.urgent === "true";

  record("schedule_draft_preview_matches_actual_fired_task", previewMatchesFired, {
    firedTaskId,
    previewFields,
    firedTaskSubset: {
      title: firedTask?.title,
      repo: firedTask?.repo,
      base: firedTask?.base,
      reads: firedTask?.reads,
      writes: firedTask?.writes,
      networkAccess: firedTask?.networkAccess,
      admission: firedTask?.admission,
      hosting: firedTask?.hosting,
      keepRunning: firedTask?.keepRunning,
      customFields: firedTask?.customFields,
    },
  });

  // Single-field edit sends a change-only PATCH!
  await page.getByTestId("sched-edit-btn").click();
  await schedDlg.waitFor({ state: "visible" });
  await pickPill(page, "sched-network", "open");
  const [patchReq, patchRes] = await Promise.all([
    page.waitForRequest(
      (r) =>
        r.method() === "PATCH" && r.url().includes(`/api/schedules/${createdSched.id}`),
    ),
    page.waitForResponse(
      (r) =>
        r.request().method() === "PATCH" &&
        r.url().includes(`/api/schedules/${createdSched.id}`),
    ),
    schedDlg.getByTestId("sched-save-btn").click(),
  ]);
  const patchBody = JSON.parse(patchReq.postData() || "{}");
  await schedDlg.waitFor({ state: "hidden" });
  const unchangedOmitted = [
    "admission",
    "hosting",
    "keepRunning",
    "reads",
    "writes",
    "customFields",
    "capabilities",
    "model",
  ].every((k) => !(k in patchBody));
  record(
    "schedule_single_field_edit_change_only_patch",
    patchRes.status() < 300 &&
      patchBody.networkAccess === "open" &&
      unchangedOmitted,
    { patchKeys: Object.keys(patchBody), patchBody },
  );

  // Unsaved-edits guard prompts ONCE on Escape, Cancel, scrim click, and rail click!
  await page.getByTestId("sched-edit-btn").click();
  await schedDlg.waitFor({ state: "visible" });
  await pickPill(page, "sched-network", "none");

  const confirmDlg = page.getByTestId("confirm-dialog");
  const cancelConfirm = async () => {
    await confirmDlg.waitFor({ state: "visible", timeout: 4000 });
    const count = await confirmDlg.count();
    await confirmDlg.getByRole("button", { name: /Keep editing|Cancel/i }).first().click();
    await confirmDlg.waitFor({ state: "hidden", timeout: 4000 });
    return count;
  };

  // (a) Escape
  await page.keyboard.press("Escape");
  const escPrompts = await cancelConfirm();
  const stillOpenAfterEsc = await schedDlg.isVisible();

  // (b) Cancel button
  await schedDlg.getByRole("button", { name: "Cancel" }).click();
  const cancelPrompts = await cancelConfirm();
  const stillOpenAfterCancel = await schedDlg.isVisible();

  // (c) Scrim click (click left edge of scrim outside the right-hand drawer)
  const scrimBox = await page.locator(".scrim").first().boundingBox();
  await page.mouse.click(Math.max(10, (scrimBox?.x || 0) + 120), (scrimBox?.y || 0) + 120);
  const scrimPrompts = await cancelConfirm();
  const stillOpenAfterScrim = await schedDlg.isVisible();

  // (d) Route navigation via Back & rail click (canDeactivate guard)
  let railResult = { clicked: false, prompted: false, count: 0, navigatedAfterDiscard: false, note: "" };
  try {
    // Click the rail link directly via DOM event (scrim covers pointer events on the rail,
    // which triggers dismissScheduleDialog; browser Back / SPA navigation exercises canDeactivate).
    await page.getByTestId("rail-inbox").evaluate((el) => el.click());
    await confirmDlg.waitFor({ state: "visible", timeout: 3000 });
    railResult.clicked = true;
    railResult.prompted = true;
    railResult.count = await confirmDlg.count();
    await page.getByTestId("confirm-accept").click();
    await page.waitForURL(/\/next\/inbox/, { timeout: 5000 });
    railResult.urlAfterDiscard = page.url();
    railResult.dialogHiddenAfterDiscard = await schedDlg.isHidden();
    railResult.navigatedAfterDiscard = page.url().includes("/next/inbox");
  } catch (err) {
    railResult.note = String(err);
  }

  record(
    "schedule_unsaved_guard_escape_cancel_scrim_rail",
    escPrompts === 1 &&
      stillOpenAfterEsc &&
      cancelPrompts === 1 &&
      stillOpenAfterCancel &&
      scrimPrompts === 1 &&
      stillOpenAfterScrim &&
      railResult.prompted &&
      railResult.count === 1 &&
      railResult.dialogHiddenAfterDiscard,
    {
      escPrompts,
      stillOpenAfterEsc,
      cancelPrompts,
      stillOpenAfterCancel,
      scrimPrompts,
      stillOpenAfterScrim,
      railResult,
    },
  );

  // ---------------------------------------------------------------------------
  // 3. Suites & stacked dialog:
  //    Create bound templates for repo A (acme/widgets) and repo B (acme/gadgets),
  //    use searchable multi-select Templates combobox, open inline "+ New template"
  //    stacked over inert suite dialog, Escape closes ONLY top dialog & returns
  //    focus, saving new template immediately adds and selects it in the suite.
  // ---------------------------------------------------------------------------
  const tmplRepoA = (
    await apiReq("POST", "/api/templates", {
      name: "walk-bound-widgets",
      title: "chore: widgets check",
      description: "Bound to acme/widgets",
      repo: "acme/widgets",
    })
  ).json;
  const tmplRepoB = (
    await apiReq("POST", "/api/templates", {
      name: "walk-bound-gadgets",
      title: "chore: gadgets check",
      description: "Bound to acme/gadgets",
      repo: "acme/gadgets",
    })
  ).json;

  await page.goto(`${BASE}/next/processes/suites`, { waitUntil: "networkidle" });
  await page.getByTestId("proc-new-suite-btn").waitFor({ state: "visible" });
  await page.getByTestId("proc-new-suite-btn").click();
  const suiteDlg = page.getByTestId("suite-dialog");
  await suiteDlg.waitFor({ state: "visible" });

  // Clear any auto-selected initial template so we explicitly select repo A first,
  // then repo B second via the searchable multi-select combobox!
  const initialRemoves = await suiteDlg
    .locator('[data-testid^="suite-remove-tmpl-"]')
    .evaluateAll((els) => els.map((e) => e.getAttribute("data-testid")));
  for (const tid of initialRemoves) {
    const btn = suiteDlg.getByTestId(tid);
    await btn.click();
    await btn.waitFor({ state: "detached" });
  }

  // Searchable multi-select combobox: select repo A (acme/widgets) first, then repo B (acme/gadgets) second!
  const combo = suiteDlg.getByTestId("suite-templates-combobox");
  await combo.fill("walk-bound-widgets");
  await suiteDlg.getByTestId("suite-templates-listbox").waitFor({ state: "visible" });
  await suiteDlg
    .getByTestId(`suite-tmpl-opt-${tmplRepoA.id}`)
    .click();

  await combo.fill("walk-bound-gadgets");
  await suiteDlg
    .getByTestId(`suite-tmpl-opt-${tmplRepoB.id}`)
    .click();

  // Escape closes listbox only, leaving suite dialog open
  await page.keyboard.press("Escape");
  let listboxClosedOnEsc = false;
  try {
    await suiteDlg.getByTestId("suite-templates-listbox").waitFor({ state: "hidden", timeout: 2000 });
    listboxClosedOnEsc = true;
  } catch {
    listboxClosedOnEsc = false;
  }
  const suiteStillOpenAfterListboxEsc = await suiteDlg.isVisible();

  // Open inline "+ New template" stacked over suite dialog
  const inlineNewBtn = suiteDlg.getByTestId("suite-new-template-btn");
  await inlineNewBtn.click();
  await tmplDlg.waitFor({ state: "visible" });
  const suiteInertWhenStacked =
    (await suiteDlg.getAttribute("inert")) !== null;

  // Escape closes ONLY top template dialog and returns focus cleanly inside suite dialog
  await page.keyboard.press("Escape");
  await tmplDlg.waitFor({ state: "hidden" });
  const suiteOpenAfterTopEsc = await suiteDlg.isVisible();
  const suiteNotInertAfterTopEsc =
    (await suiteDlg.getAttribute("inert")) === null;
  const focusAfterTopEsc = await page.evaluate(() => {
    const el = document.activeElement;
    const inSuite = Boolean(el?.closest('[data-testid="suite-dialog"]'));
    return {
      inSuite,
      tag: el?.tagName,
      testid: el?.getAttribute?.("data-testid") || null,
    };
  });

  // Re-open inline "+ New template", save a new template -> immediately selected in suite!
  await inlineNewBtn.click();
  await tmplDlg.waitFor({ state: "visible" });
  await tmplDlg.getByTestId("tmpl-name-input").fill("walk-inline-created");
  await tmplDlg.getByTestId("tmpl-title-input").fill("chore: inline created");
  await tmplDlg.getByTestId("tmpl-desc-input").fill("Created inline from suite");
  await tmplDlg.getByTestId("tmpl-repo-input").fill("acme/widgets");
  const [inlineTmplRes] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === "POST" && /\/api\/templates$/.test(r.url()),
    ),
    tmplDlg.getByTestId("tmpl-save-btn").click(),
  ]);
  const inlineTmpl = await inlineTmplRes.json();
  await tmplDlg.waitFor({ state: "hidden" });
  const selectedTextAfterInline = (
    await suiteDlg.getByTestId("suite-selected-templates").textContent()
  )?.trim();

  // Save the suite (order: walk-bound-widgets, walk-bound-gadgets, walk-inline-created)
  await suiteDlg.getByTestId("suite-name-input").fill("Walk multi-repo suite");
  const [suiteCreateRes] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === "POST" && /\/api\/suites$/.test(r.url()),
    ),
    suiteDlg.getByTestId("suite-save-btn").click(),
  ]);
  const createdSuite = await suiteCreateRes.json();
  await suiteDlg.waitFor({ state: "hidden" });

  record(
    "suite_combobox_stacked_dialog_inert_escape_focus_and_select",
    listboxClosedOnEsc &&
      suiteStillOpenAfterListboxEsc &&
      suiteInertWhenStacked &&
      suiteOpenAfterTopEsc &&
      suiteNotInertAfterTopEsc &&
      focusAfterTopEsc.inSuite &&
      selectedTextAfterInline?.includes("walk-inline-created") &&
      suiteCreateRes.status() < 300,
    {
      listboxClosedOnEsc,
      suiteInertWhenStacked,
      suiteOpenAfterTopEsc,
      suiteNotInertAfterTopEsc,
      focusAfterTopEsc,
      selectedTextAfterInline,
      createdSuiteId: createdSuite?.id,
      inlineTmplId: inlineTmpl?.id,
      suiteTemplateIds: createdSuite?.templateIds,
    },
  );

  // Measure Alice's allowed button bounding boxes on Edit suite for zero-shift comparison!
  await page.goto(`${BASE}/next/processes/suites/${createdSuite.id}?edit=1`, {
    waitUntil: "networkidle",
  });
  await suiteDlg.waitFor({ state: "visible" });
  const aliceBoxes = {
    newTmpl: await suiteDlg.getByTestId("suite-new-template-btn").boundingBox(),
    save: await suiteDlg.getByTestId("suite-save-btn").boundingBox(),
    newTmplDisabled: await suiteDlg.getByTestId("suite-new-template-btn").isDisabled(),
    saveDisabled: await suiteDlg.getByTestId("suite-save-btn").isDisabled(),
  };

  // ---------------------------------------------------------------------------
  // 4. Real Scoped RBAC Refusals on the real daemon (Bob & Carol via IAP JWTs)
  // ---------------------------------------------------------------------------
  // 4a. Bob: suites:write on acme/widgets ONLY, NOT acme/gadgets.
  //     Suite's 1st template is acme/widgets, 2nd template is acme/gadgets!
  const bobCtx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    extraHTTPHeaders: { "X-Goog-IAP-JWT-Assertion": BOB_JWT },
  });
  const bobPage = await bobCtx.newPage();
  attachListeners(bobPage, "bob-rbac");
  await bobPage.goto(`${BASE}/next/processes/suites/${createdSuite.id}?edit=1`, {
    waitUntil: "networkidle",
  });
  const bobSuiteDlg = bobPage.getByTestId("suite-dialog");
  await bobSuiteDlg.waitFor({ state: "visible" });
  const bobSaveBtn = bobSuiteDlg.getByTestId("suite-save-btn");
  const bobSaveDisabled = await bobSaveBtn.isDisabled();
  const bobConfig = (
    await apiReq("GET", "/api/config", undefined, {
      "X-Goog-IAP-JWT-Assertion": BOB_JWT,
    })
  ).json;
  // Hover over Save button (force: true on disabled button so we actually test real tooltip!)
  const bobSaveBox = await bobSaveBtn.boundingBox();
  await bobPage.mouse.move(
    bobSaveBox.x + bobSaveBox.width / 2,
    bobSaveBox.y + bobSaveBox.height / 2,
  );
  await bobPage.waitForTimeout(550);
  const bobTipState = await bobPage.evaluate(() => {
    const tip = document.getElementById("g-tip");
    return {
      exists: Boolean(tip),
      hidden: tip ? tip.hidden : null,
      text: tip ? tip.textContent?.trim() : null,
    };
  });
  // Also check if dispatching mouseenter on the button shows it vs real pointer hover
  const bobSaveAttrs = await bobSaveBtn.evaluate((el) => ({
    disabled: el.hasAttribute("disabled"),
    ariaDisabled: el.getAttribute("aria-disabled"),
    title: el.getAttribute("title"),
    ariaDescribedBy: el.getAttribute("aria-describedby"),
  }));
  // Check server-side 403 enforcement for Bob too
  const bobPatchRes = await apiReq(
    "PATCH",
    `/api/suites/${createdSuite.id}`,
    { name: "Bob unauthorized edit" },
    { "X-Goog-IAP-JWT-Assertion": BOB_JWT },
  );
  record(
    "rbac_bob_scoped_suites_write_refusal_second_template_repo_b",
    bobSaveDisabled &&
      bobPatchRes.status === 403 &&
      Boolean(bobTipState.exists && !bobTipState.hidden && bobTipState.text?.includes("acme/gadgets")),
    {
      bobRole: bobConfig?.access?.role,
      bobRepoLimits: bobConfig?.access?.repoLimits,
      bobSaveDisabled,
      bobTipState,
      bobSaveAttrs,
      bobPatchStatus: bobPatchRes.status,
      bobPatchBody: bobPatchRes.text?.slice(0, 200),
    },
  );
  await bobCtx.close();

  // 4b. Carol: suites:write everywhere, NO templates:write ->
  //     "+ New template" disabled in place with zero layout shift + refusal tooltip!
  const carolCtx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    extraHTTPHeaders: { "X-Goog-IAP-JWT-Assertion": CAROL_JWT },
  });
  const carolPage = await carolCtx.newPage();
  attachListeners(carolPage, "carol-rbac");
  await carolPage.goto(`${BASE}/next/processes/suites/${createdSuite.id}?edit=1`, {
    waitUntil: "networkidle",
  });
  const carolSuiteDlg = carolPage.getByTestId("suite-dialog");
  await carolSuiteDlg.waitFor({ state: "visible" });
  const carolNewTmplBtn = carolSuiteDlg.getByTestId("suite-new-template-btn");
  const carolSaveBtn = carolSuiteDlg.getByTestId("suite-save-btn");
  const carolBoxes = {
    newTmpl: await carolNewTmplBtn.boundingBox(),
    save: await carolSaveBtn.boundingBox(),
    newTmplDisabled: await carolNewTmplBtn.isDisabled(),
    saveDisabled: await carolSaveBtn.isDisabled(),
  };
  await carolPage.mouse.move(
    carolBoxes.newTmpl.x + carolBoxes.newTmpl.width / 2,
    carolBoxes.newTmpl.y + carolBoxes.newTmpl.height / 2,
  );
  await carolPage.waitForTimeout(550);
  const carolTipState = await carolPage.evaluate(() => {
    const tip = document.getElementById("g-tip");
    return {
      exists: Boolean(tip),
      hidden: tip ? tip.hidden : null,
      text: tip ? tip.textContent?.trim() : null,
    };
  });
  const carolShift = {
    dx: Math.abs(carolBoxes.newTmpl.x - aliceBoxes.newTmpl.x),
    dy: Math.abs(carolBoxes.newTmpl.y - aliceBoxes.newTmpl.y),
    saveDy: Math.abs(carolBoxes.save.y - aliceBoxes.save.y),
  };
  record(
    "rbac_carol_no_templates_write_zero_shift_and_tooltip",
    carolBoxes.newTmplDisabled &&
      !carolBoxes.saveDisabled &&
      carolShift.dx < 1.5 &&
      carolShift.dy < 1.5 &&
      carolShift.saveDy < 1.5 &&
      Boolean(carolTipState.exists && !carolTipState.hidden && carolTipState.text),
    {
      aliceBoxes,
      carolBoxes,
      carolShift,
      carolTipState,
    },
  );
  await carolCtx.close();

  // ---------------------------------------------------------------------------
  // 5. Rail Processes count badge update check & /next/agents Details scroll
  //    stability while tasks change state!
  // ---------------------------------------------------------------------------
  await page.goto(`${BASE}/next/inbox`, { waitUntil: "networkidle" });
  const [s1, t1, st1] = await Promise.all([
    apiReq("GET", "/api/schedules"),
    apiReq("GET", "/api/templates"),
    apiReq("GET", "/api/suites"),
  ]);
  const updatedExpected =
    (s1.json?.length || 0) + (t1.json?.length || 0) + (st1.json?.length || 0);
  // Wait briefly or reload Processes so shell count reflects new items
  await page.goto(`${BASE}/next/processes`, { waitUntil: "networkidle" });
  const badgeAfterCreate = (await railBadge.textContent())?.trim();

  // Create a long-body interactive task so /next/agents Details tab has scrollable content,
  // and keep it running via fake-agy sleep so it appears on /next/agents!
  const longDesc =
    "Long task description for scroll stability test.\n\n" +
    Array.from({ length: 60 }, (_, i) => `- Verification line ${i + 1}: checking scroll containment while tasks transition states.`).join("\n");
  const actionPath = path.join(path.dirname(OUT_DIR), "action-default.json");
  fs.writeFileSync(
    actionPath,
    JSON.stringify({ kind: "plain_reply", response: "holding agent slot", sleep_sec: 12 }),
  );
  const longTask = (
    await apiReq("POST", "/api/tasks", {
      title: "Scroll stability anchor agent",
      description: longDesc,
      repo: "acme/widgets",
      interactive: true,
      approved: true,
    })
  ).json;
  // Wait for it to enter running state
  let runningOk = false;
  for (let i = 0; i < 40; i++) {
    const st = (await apiReq("GET", `/api/tasks/${longTask.id}`)).json?.state;
    if (st === "running") {
      runningOk = true;
      break;
    }
    await page.waitForTimeout(250);
  }
  await page.goto(`${BASE}/next/agents?task=${longTask.id}`, {
    waitUntil: "networkidle",
  });
  let scrollStability = { runningOk, detailsScrolled: false, before: 0, after: 0, minDuring: 0, maxDuring: 0 };
  if (await page.getByTestId("task-tab-details").isVisible().catch(() => false)) {
    await page.getByTestId("task-tab-details").click();
    const scroller = page.locator('[data-testid="agents-pane"] .task-tab-body');
    await scroller.waitFor({ state: "visible" });
    await page.waitForTimeout(200);
    const dims = await scroller.evaluate((el) => {
      el.scrollTop = Math.max(0, Math.min(300, el.scrollHeight - el.clientHeight));
      return {
        scrollTop: el.scrollTop,
        scrollHeight: el.scrollHeight,
        clientHeight: el.clientHeight,
      };
    });
    scrollStability.before = dims.scrollTop;
    scrollStability.dims = dims;
    scrollStability.detailsScrolled = dims.scrollTop > 20;

    // Trigger rapid task state transitions in the background while sampling scrollTop!
    const samples = [];
    for (let i = 0; i < 3; i++) {
      const churn = (
        await apiReq("POST", "/api/tasks", {
          title: `Churn task ${i}`,
          description: "State transition churn",
          repo: "acme/widgets",
          approved: false,
        })
      ).json;
      await apiReq("POST", `/api/tasks/${churn.id}/approve`, {});
      await apiReq("POST", `/api/tasks/${churn.id}/close`, { reason: "churn done" });
      await page.waitForTimeout(300);
      const curTop = await scroller.evaluate((el) => el.scrollTop);
      samples.push(curTop);
    }
    scrollStability.samples = samples;
    scrollStability.after = samples[samples.length - 1];
    scrollStability.minDuring = Math.min(...samples);
    scrollStability.maxDuring = Math.max(...samples);
  }
  // Reset default action back to fast
  fs.writeFileSync(
    actionPath,
    JSON.stringify({ kind: "plain_reply", response: "fast reply", sleep_sec: 0 }),
  );
  record(
    "rail_badge_and_agents_details_scroll_stability",
    badgeAfterCreate === String(updatedExpected) &&
      scrollStability.detailsScrolled &&
      Math.abs(scrollStability.after - scrollStability.before) <= 2 &&
      Math.abs(scrollStability.minDuring - scrollStability.before) <= 2,
    {
      updatedExpected,
      badgeAfterCreate,
      scrollStability,
    },
  );

  // ---------------------------------------------------------------------------
  // 6. Responsive & Theme Browser Sweep:
  //    1440x900 and 390x844, light and dark across:
  //    - /next/processes/schedules
  //    - /next/processes/templates
  //    - /next/processes/suites
  //    - stacked template-over-suite dialog
  //    - /next/agents
  //    Check zero horizontal overflow + axe severe violations = 0 + screenshots.
  // ---------------------------------------------------------------------------
  const targets = [
    { id: "schedules", url: `${BASE}/next/processes/schedules` },
    { id: "templates", url: `${BASE}/next/processes/templates` },
    { id: "suites", url: `${BASE}/next/processes/suites` },
    { id: "stacked-dialog", url: `${BASE}/next/processes/suites/${createdSuite.id}?edit=1`, stack: true },
    { id: "agents", url: `${BASE}/next/agents` },
  ];
  const viewports = [
    { name: "desktop-1440", width: 1440, height: 900 },
    { name: "phone-390", width: 390, height: 844 },
  ];
  const themes = ["light", "dark"];

  let sweepAllPass = true;
  for (const vp of viewports) {
    const sCtx = await browser.newContext({
      viewport: { width: vp.width, height: vp.height },
      hasTouch: vp.width <= 480,
      isMobile: vp.width <= 480,
    });
    const sPage = await sCtx.newPage();
    attachListeners(sPage, `sweep-${vp.name}`);
    await signInPage(sPage);

    for (const theme of themes) {
      for (const t of targets) {
        await sPage.goto(t.url, { waitUntil: "networkidle" });
        await setTheme(sPage, theme);
        if (t.stack) {
          const sd = sPage.getByTestId("suite-dialog");
          await sd.waitFor({ state: "visible" });
          await sd.getByTestId("suite-new-template-btn").click();
          await sPage.getByTestId("template-dialog").waitFor({ state: "visible" });
        }
        await sPage.waitForTimeout(200);
        const overflow = await checkOverflow(sPage);
        const axeRes = await runAxe(sPage);
        const shotName = `${t.id}-${vp.name}-${theme}.png`;
        await sPage.screenshot({
          path: path.join(OUT_DIR, "screens", shotName),
          fullPage: false,
        });
        const ok = overflow.overflowPx <= 0 && axeRes.severeCount === 0;
        if (!ok) sweepAllPass = false;
        report.sweeps.push({
          target: t.id,
          viewport: vp.name,
          theme,
          overflow,
          axeSevereCount: axeRes.severeCount,
          axeSevere: axeRes.severe,
          screenshot: shotName,
          ok,
        });
      }
    }
    await sCtx.close();
  }
  record("browser_sweep_1440_390_light_dark_overflow_axe", sweepAllPass, {
    totalChecks: report.sweeps.length,
    failedChecks: report.sweeps.filter((s) => !s.ok),
  });

  // Delete created schedule/suite/templates and verify rail badge decreases cleanly
  await apiReq("DELETE", `/api/schedules/${createdSched.id}`);
  await apiReq("DELETE", `/api/suites/${createdSuite.id}`);
  await apiReq("DELETE", `/api/templates/${createdTmplFields.id}`);
  await apiReq("DELETE", `/api/templates/${tmplNoFields.id}`);
  await apiReq("DELETE", `/api/templates/${tmplRepoA.id}`);
  await apiReq("DELETE", `/api/templates/${tmplRepoB.id}`);
  await apiReq("DELETE", `/api/templates/${inlineTmpl.id}`);

  await page.goto(`${BASE}/next/processes`, { waitUntil: "networkidle" });
  await page.waitForTimeout(400);
  const badgeAfterDelete = (await railBadge.textContent())?.trim();
  record(
    "rail_badge_returns_to_initial_after_deletes",
    badgeAfterDelete === String(initialCount),
    { initialCount, badgeAfterDelete },
  );

  await ctx.close();
  await browser.close();

  fs.writeFileSync(
    path.join(OUT_DIR, "ui-report.json"),
    JSON.stringify(report, null, 2),
  );
  console.log(`[${STORE_LABEL}] Wrote ${path.join(OUT_DIR, "ui-report.json")}`);
  console.log(
    `[${STORE_LABEL}] Summary: ${report.steps.filter((s) => s.pass).length}/${report.steps.length} passed, findings=${report.findings.length}, pageErrors=${report.pageErrors.length}, http5xx=${report.http5xx.length}`,
  );
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
