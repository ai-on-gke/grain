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

// item1_ui_check.mjs: Browser verification for §3 item 1 against an upgraded
// schema 129 -> 131 store (run for both SQLite and PostgreSQL).
//
// 1. Opens every UI form (task pane, template editor, schedule editor,
//    Settings -> Tasks, repo defaults, repo CI capabilities, console session
//    page, and New task overlay) and asserts one line per grant with zero
//    duplicates.
// 2. Snapshots GET endpoints before and after editing an unrelated grant on
//    each form one at a time, recording the exact per-object diff so we verify
//    canonical writes apply only to what was touched and never drop or
//    duplicate grants.
//
// Run from ui/:
//   node ../docs/test-runs/2026-11-10-unified-grants/item1_ui_check.mjs \
//     <baseUrl> <token> <seededIdsJson> <outJson>
import { createRequire } from "node:module";
import fs from "node:fs";

const { chromium } = createRequire(`${process.cwd()}/`)("playwright");

const [base, token, idsFile, outFile] = process.argv.slice(2);
const ids = JSON.parse(fs.readFileSync(idsFile, "utf8"));

const headers = {
  Authorization: `Bearer ${token}`,
  "Content-Type": "application/json",
};

async function apiGet(path) {
  const res = await fetch(`${base}${path}`, { headers });
  if (!res.ok) {
    throw new Error(`GET ${path} -> ${res.status}: ${await res.text()}`);
  }
  return res.json();
}

async function snapshotAll() {
  const [task1, templates, schedules, settings, repoCaps, repoLibCaps, sessions] =
    await Promise.all([
      apiGet(`/api/tasks/${ids.task1}`),
      apiGet("/api/templates"),
      apiGet("/api/schedules"),
      apiGet("/api/settings"),
      apiGet("/api/repos/acme/widgets/capabilities"),
      apiGet("/api/repos/acme/lib/capabilities"),
      apiGet("/api/console-sessions"),
    ]);
  const tplList = Array.isArray(templates)
    ? templates
    : templates.items || templates.templates || [];
  const schList = Array.isArray(schedules)
    ? schedules
    : schedules.items || schedules.schedules || [];
  const sessList = sessions.sessions || [];
  const ops = sessList.find((s) => s.id === "ops");
  const director = sessList.find((s) => s.id === "director");
  const opsTask = ops?.taskId ? await apiGet(`/api/tasks/${ops.taskId}`) : null;
  return {
    task1: {
      capabilities: task1.capabilities || [],
      reads: task1.reads || [],
      writes: task1.writes || [],
    },
    template: (() => {
      const t = tplList.find((x) => x.id === ids.template);
      return {
        capabilities: t?.capabilities || [],
        reads: t?.reads || [],
        writes: t?.writes || [],
      };
    })(),
    schedule: (() => {
      const s = schList.find((x) => x.id === ids.schedule);
      return {
        capabilities: s?.capabilities || [],
        reads: s?.reads || [],
        writes: s?.writes || [],
      };
    })(),
    settings: {
      defaultCapabilities: settings.defaultCapabilities || [],
    },
    repoWidgets: {
      defaultCapabilities: repoCaps.defaultCapabilities || [],
      ciCapabilities: repoCaps.ciCapabilities || [],
    },
    repoLib: {
      ciCapabilities: repoLibCaps.ciCapabilities || [],
    },
    opsSession: {
      capabilities: ops?.capabilities || [],
      reads: ops?.reads || [],
      taskCapabilities: opsTask?.capabilities || [],
    },
    directorSession: {
      capabilities: director?.capabilities || [],
    },
  };
}

function diffSnapshots(before, after) {
  const changed = {};
  for (const key of Object.keys(after)) {
    const b = JSON.stringify(before[key]);
    const a = JSON.stringify(after[key]);
    if (b !== a) {
      changed[key] = { before: before[key], after: after[key] };
    }
  }
  return changed;
}

const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  extraHTTPHeaders: { Authorization: `Bearer ${token}` },
});
const page = await context.newPage();
const pageErrors = [];
page.on("pageerror", (e) => pageErrors.push(String(e)));

async function readHeld(group) {
  await group.waitFor({ timeout: 15000 });
  // Wait for checkGrants / capabilityMethods to settle.
  await page.waitForTimeout(500);
  const items = await group
    .locator(".grants-held [data-grant]")
    .evaluateAll((els) =>
      els.map((e) => ({
        grant: e.dataset.grant,
        kind: e.dataset.grantKind || "section",
        text: e.textContent.replace(/\s+/g, " ").trim(),
      })),
    );
  const grants = items.map((i) => i.grant);
  const unique = new Set(grants);
  return {
    items,
    count: items.length,
    hasDuplicates: unique.size !== grants.length,
  };
}

async function toggleApiScope(group, scopeShortName) {
  await group.getByRole("button", { name: "Add grant…" }).first().click();
  const menu = page.getByRole("menu", { name: "Grants to add" });
  await menu.waitFor();
  await page.getByLabel("Search grants").fill(scopeShortName);
  const item = menu.getByRole("menuitemcheckbox", {
    name: scopeShortName,
    exact: true,
  });
  const was = await item.getAttribute("aria-checked");
  await item.click();
  await page
    .waitForFunction(
      ([n, w]) => {
        const el = [
          ...document.querySelectorAll('[role="menuitemcheckbox"]'),
        ].find((e) => e.getAttribute("aria-label") === n);
        return el && el.getAttribute("aria-checked") !== w;
      },
      [scopeShortName, was],
      { timeout: 5000 },
    )
    .catch(() => {});
  await page.keyboard.press("Escape");
  await page.waitForTimeout(500);
}

const report = {
  initialForms: {},
  edits: [],
  newTaskAfterPartialDefaultsEdit: null,
  pageErrors,
};

// 1. Inspect every form right after upgrade (before any edit).
await page.goto(`${base}/tasks/${ids.task1}`);
report.initialForms.taskPane = await readHeld(
  page.getByTestId(`task-${ids.task1}-grants`),
);

await page.goto(`${base}/processes/templates/${ids.template}`);
report.initialForms.templateEditor = await readHeld(
  page.getByTestId("template-grants"),
);

await page.goto(`${base}/processes/schedules/${ids.schedule}`);
report.initialForms.scheduleEditor = await readHeld(
  page.getByTestId("schedule-grants"),
);

await page.goto(`${base}/settings/tasks`);
report.initialForms.settingsTasks = await readHeld(
  page.getByTestId("settings-default-grants"),
);

await page.goto(`${base}/repos/acme/widgets#agents`);
report.initialForms.repoDefaults = await readHeld(
  page.getByTestId("repo-default-grants"),
);

await page.goto(`${base}/repos/acme/widgets#ci`);
const ciSelectText = await page
  .locator("#repo-ci-capabilities-label")
  .locator("..")
  .textContent();
report.initialForms.repoCICapabilities = {
  selectText: ciSelectText.replace(/\s+/g, " ").trim(),
  grantsPicker: await readHeld(page.getByTestId("repo-ci-grants")),
};

await page.goto(`${base}/session/ops`);
await page.getByRole("button", { name: "Edit Ops capabilities" }).click();
report.initialForms.consoleSessionOps = await readHeld(
  page.getByTestId("session-ops-grants"),
);

await page.goto(`${base}/`);
await page.getByRole("button", { name: "+ New task" }).click();
report.initialForms.newTaskOverlay = await readHeld(
  page.getByTestId("new-task-grants"),
);
await page.keyboard.press("Escape");

// 2. Partial edits one form at a time, diffing GET snapshots before & after!
let snap = await snapshotAll();
report.beforeAllEdits = snap;

// 2a. Task pane: add unrelated api#tasks:write -> only task1's new grant is written;
//     existing legacy api:tasks:read@acme/widgets and bare self-debug on task1 stay untouched,
//     and no other object changes!
await page.goto(`${base}/tasks/${ids.task1}`);
await toggleApiScope(page.getByTestId(`task-${ids.task1}-grants`), "tasks:write");
await page.waitForTimeout(600);
let nextSnap = await snapshotAll();
report.edits.push({
  surface: "taskPane (add tasks:write)",
  diff: diffSnapshots(snap, nextSnap),
  uiAfter: await readHeld(page.getByTestId(`task-${ids.task1}-grants`)),
});
snap = nextSnap;

// 2b. Settings -> Tasks: add unrelated tasks:write -> canonicalizes settings.defaultCapabilities
//     ONLY, leaving repoWidgets.defaultCapabilities in legacy spelling!
await page.goto(`${base}/settings/tasks`);
await toggleApiScope(page.getByTestId("settings-default-grants"), "tasks:write");
await page.waitForTimeout(600);
nextSnap = await snapshotAll();
report.edits.push({
  surface: "settingsTasks (add tasks:write)",
  diff: diffSnapshots(snap, nextSnap),
  uiAfter: await readHeld(page.getByTestId("settings-default-grants")),
});
snap = nextSnap;

// CRITICAL CHECK: Now settings.defaultCapabilities is canonical, while
// repoWidgets.defaultCapabilities is STILL legacy! Open New task overlay for
// acme/widgets and check whether unionCapabilities produces duplicate lines!
await page.goto(`${base}/`);
await page.getByRole("button", { name: "+ New task" }).click();
report.newTaskAfterPartialDefaultsEdit = await readHeld(
  page.getByTestId("new-task-grants"),
);
await page.keyboard.press("Escape");

// 2c. Template editor: add unrelated tasks:write and Save.
await page.goto(`${base}/processes/templates/${ids.template}`);
await toggleApiScope(page.getByTestId("template-grants"), "tasks:write");
await page.getByRole("button", { name: "Save", exact: true }).click();
await page.waitForTimeout(800);
await page.goto(`${base}/processes/templates/${ids.template}`);
nextSnap = await snapshotAll();
report.edits.push({
  surface: "templateEditor (add tasks:write + Save)",
  diff: diffSnapshots(snap, nextSnap),
  uiAfter: await readHeld(page.getByTestId("template-grants")),
});
snap = nextSnap;

// 2d. Schedule editor: add unrelated tasks:write and Save.
await page.goto(`${base}/processes/schedules/${ids.schedule}`);
await toggleApiScope(page.getByTestId("schedule-grants"), "tasks:write");
await page.getByRole("button", { name: "Save", exact: true }).click();
await page.waitForTimeout(800);
await page.goto(`${base}/processes/schedules/${ids.schedule}`);
nextSnap = await snapshotAll();
report.edits.push({
  surface: "scheduleEditor (add tasks:write + Save)",
  diff: diffSnapshots(snap, nextSnap),
  uiAfter: await readHeld(page.getByTestId("schedule-grants")),
});
snap = nextSnap;

// 2e. Console session ops: add unrelated tasks:write; verify declined api:ci-read stays off!
await page.goto(`${base}/session/ops`);
await page.getByRole("button", { name: "Edit Ops capabilities" }).click();
await toggleApiScope(page.getByTestId("session-ops-grants"), "tasks:write");
await page.waitForTimeout(800);
nextSnap = await snapshotAll();
report.edits.push({
  surface: "consoleSessionOps (add tasks:write)",
  diff: diffSnapshots(snap, nextSnap),
  uiAfter: await readHeld(page.getByTestId("session-ops-grants")),
});
snap = nextSnap;

// 2f. Repo defaults (acme/widgets #agents): add unrelated deployment:read.
await page.goto(`${base}/repos/acme/widgets#agents`);
await toggleApiScope(page.getByTestId("repo-default-grants"), "deployment:read");
await page.waitForTimeout(800);
nextSnap = await snapshotAll();
report.edits.push({
  surface: "repoDefaults (add deployment:read)",
  diff: diffSnapshots(snap, nextSnap),
  uiAfter: await readHeld(page.getByTestId("repo-default-grants")),
});
snap = nextSnap;

// 2g. Repo CI capabilities (acme/widgets #ci): add unrelated repo#read for acme/other.
await page.goto(`${base}/repos/acme/widgets#ci`);
const ciGroup = page.getByTestId("repo-ci-grants");
await ciGroup.getByRole("button", { name: "Add grant…" }).click();
const ciMenu = page.getByRole("menu", { name: "Grants to add" });
await ciMenu.waitFor();
await page.getByLabel("Search grants").fill("repo#read");
await ciMenu.locator('[data-method="https://grain.dev/capabilities/repo#read"]').click();
const repoArg = ciGroup.locator('[data-grant-draft] input[data-arg="repo"]');
await repoArg.waitFor();
await repoArg.fill("acme/other");
await repoArg.blur();
await page.waitForTimeout(1000);
nextSnap = await snapshotAll();
report.edits.push({
  surface: "repoCICapabilities (add repo#read acme/other)",
  diff: diffSnapshots(snap, nextSnap),
  uiAfter: await readHeld(ciGroup),
});
snap = nextSnap;

// 2h. Repo CI capabilities on acme/lib (which holds unmigrated bare self-debug + repo-read:acme/widgets):
//     try adding repo#read for acme/other and check whether the save succeeds or errors!
await page.goto(`${base}/repos/acme/lib#ci`);
const ciLibSelectText = await page
  .locator("#repo-ci-capabilities-label")
  .locator("..")
  .textContent();
const ciLibGroup = page.getByTestId("repo-ci-grants");
const ciLibBefore = await readHeld(ciLibGroup);
const ciLibPut = page.waitForResponse(
  (r) =>
    r.url().includes("/api/repos/acme/lib/ci-capabilities") &&
    r.request().method() === "PUT",
  { timeout: 8000 },
);
await ciLibGroup.getByRole("button", { name: "Add grant…" }).click();
const ciLibMenu = page.getByRole("menu", { name: "Grants to add" });
await ciLibMenu.waitFor();
await page.getByLabel("Search grants").fill("repo#read");
await ciLibMenu.locator('[data-method="https://grain.dev/capabilities/repo#read"]').click();
const libRepoArg = ciLibGroup.locator('[data-grant-draft] input[data-arg="repo"]');
await libRepoArg.waitFor();
await libRepoArg.fill("acme/other");
await libRepoArg.blur();
const libPutRes = await ciLibPut.catch(() => null);
await page.waitForTimeout(800);
nextSnap = await snapshotAll();
report.repoLibCIEdit = {
  selectTextBefore: ciLibSelectText.replace(/\s+/g, " ").trim(),
  grantsPickerBefore: ciLibBefore,
  putStatus: libPutRes ? libPutRes.status() : null,
  putBody: libPutRes ? await libPutRes.text() : null,
  diff: diffSnapshots(snap, nextSnap),
};

fs.writeFileSync(outFile, JSON.stringify(report, null, 2));
await browser.close();
