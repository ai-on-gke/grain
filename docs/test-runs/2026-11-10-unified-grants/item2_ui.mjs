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

// item2_ui.mjs: Exercises §3 item 2 (repository read/write lines end to end
// across all 5 surfaces in Chromium, focus return to "Add grant…", fixed
// #target line, read-only push: refs on a declared-template task, and absence
// of CI-only repo#read where reads are edited) and the browser check of §3
// item 5 (RepoPage surfacing repo_declaration_sync.last_error).
import { createRequire } from "node:module";
import fs from "node:fs";

const { chromium } = createRequire(`${process.cwd()}/`)("playwright");

const [mode, base, token, argsJson, outFile] = process.argv.slice(2);
const args = JSON.parse(argsJson);

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

const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  extraHTTPHeaders: { Authorization: `Bearer ${token}` },
});
const page = await context.newPage();
const pageErrors = [];
page.on("pageerror", (e) => pageErrors.push(String(e)));

if (mode === "item5-error") {
  await page.goto(`${base}/repos/acme/widgets#advanced`);
  await page.waitForTimeout(1000);
  const advancedText = (await page.locator("main").textContent())
    .replace(/\s+/g, " ")
    .trim();
  const stillRunning = await page
    .locator(".declared-still-running")
    .allTextContents();
  fs.writeFileSync(
    outFile,
    JSON.stringify({ advancedText, stillRunning, pageErrors }, null, 2),
  );
  await browser.close();
  process.exit(0);
}

async function checkMenuHasNoCIRepoRead(scope) {
  await scope.getByRole("button", { name: "Add grant…" }).first().click();
  const menu = page.getByRole("menu", { name: "Grants to add" });
  await menu.waitFor();
  await page.getByLabel("Search grants").fill("repo#read");
  await page.waitForTimeout(300);
  const ciMethodCount = await menu
    .locator('[data-method="https://grain.dev/capabilities/repo#read"]')
    .count();
  const repoReadRowCount = await menu
    .locator('[data-repo-grant="read"]')
    .count();
  await page.keyboard.press("Escape");
  await page.waitForTimeout(200);
  return { ciMethodCount, repoReadRowCount };
}

async function addRepoGrantAndCheckFocus(scope, kind, repoName) {
  await scope.getByRole("button", { name: "Add grant…" }).first().click();
  const menu = page.getByRole("menu", { name: "Grants to add" });
  await menu.waitFor();
  await menu
    .getByRole("menuitem", {
      name: kind === "read" ? /Read a repository/ : /Write a repository/,
    })
    .click();
  const inputLabel = kind === "read" ? "Repo to read" : "Repo to write";
  const box = scope.getByRole("textbox", { name: inputLabel, exact: true });
  await box.waitFor();
  await box.fill(repoName);
  await page
    .getByRole("menuitem", { name: new RegExp(repoName.replace("/", "\\/")) })
    .first()
    .click();
  await page.waitForTimeout(150);
  const activeFocusInfo = await page.evaluate(() => {
    const el = document.activeElement;
    return {
      tag: el?.tagName,
      text: el?.textContent?.trim(),
      ariaLabel: el?.getAttribute("aria-label"),
    };
  });
  const focusedAddGrant = activeFocusInfo.text === "Add grant…";
  return { focusedAddGrant, activeFocusInfo };
}

async function setWriteBase(scope, repoName, branchName) {
  const branchInput = scope.getByLabel(`Base branch in ${repoName}`);
  await branchInput.waitFor();
  await branchInput.fill(branchName);
  await branchInput.press("Tab");
  await page.waitForTimeout(500);
}

async function checkTargetUnremovable(scope) {
  const target = scope.locator(
    '[data-grant="https://grain.dev/capabilities/repo#target"]',
  );
  const count = await target.count();
  const buttonCount = count > 0 ? await target.locator("button").count() : -1;
  const text =
    count > 0 ? (await target.textContent()).replace(/\s+/g, " ").trim() : null;
  return { present: count === 1, buttonCount, removable: buttonCount > 0, text };
}

const results = { surfaces: {}, pushCheck: null, ciPageHasRepoRead: null, pageErrors };

// --- 1. New task overlay ---
await page.goto(`${base}/`);
await page.getByRole("button", { name: "+ New task" }).click();
const dialog = page.getByRole("dialog");
const ntScope = dialog.getByTestId("new-task-grants");
await ntScope.waitFor();
await dialog.getByLabel("Title").fill("Item 2 New Task");
const repoInput = dialog.getByLabel(/^Repos/);
if ((await repoInput.inputValue()) === "") {
  await repoInput.click();
  await page.getByRole("option", { name: /acme\/widgets/ }).first().click();
}

const ntTarget = await checkTargetUnremovable(ntScope);
const ntMenu = await checkMenuHasNoCIRepoRead(ntScope);
const ntReadFocus = await addRepoGrantAndCheckFocus(ntScope, "read", "acme/lib");
const ntWriteFocus = await addRepoGrantAndCheckFocus(ntScope, "write", "acme/other");
await setWriteBase(ntScope, "acme/other", "staging");
await dialog.getByLabel("Title").click();

const createdResp = page.waitForResponse(
  (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
);
await dialog.getByRole("button", { name: "Create task" }).click();
const filedTask = await (await createdResp).json();
const storedNewTask = await apiGet(`/api/tasks/${filedTask.id}`);

results.surfaces.newTaskOverlay = {
  target: ntTarget,
  menu: ntMenu,
  readFocus: ntReadFocus,
  writeFocus: ntWriteFocus,
  storedAfterCreate: {
    id: storedNewTask.id,
    reads: storedNewTask.reads || [],
    writes: storedNewTask.writes || [],
  },
};

// --- 2. Task pane (using the task just created, then removing & re-adding) ---
await page.goto(`${base}/tasks/${filedTask.id}`);
const tpScope = page.getByTestId(`task-${filedTask.id}-grants`);
await tpScope.waitFor();

const tpTarget = await checkTargetUnremovable(tpScope);
const tpMenu = await checkMenuHasNoCIRepoRead(tpScope);

// Remove the read & write that came from New task, reload, verify empty.
await tpScope.getByRole("button", { name: "Remove acme/lib · read" }).click();
await page.waitForTimeout(400);
await tpScope
  .getByRole("button", { name: "Remove acme/other · staging · write" })
  .click();
await page.waitForTimeout(400);
await page.reload();
await tpScope.waitFor();
const tpAfterFirstRemove = await apiGet(`/api/tasks/${filedTask.id}`);

// Add read & write (with non-default base staging) directly on task pane, reload, verify stored!
const tpReadFocus = await addRepoGrantAndCheckFocus(tpScope, "read", "acme/lib");
await page.waitForTimeout(400);
const tpWriteFocus = await addRepoGrantAndCheckFocus(tpScope, "write", "acme/other");
await page.waitForTimeout(400);
await setWriteBase(tpScope, "acme/other", "staging");
await page.reload();
await tpScope.waitFor();
const tpAfterAdd = await apiGet(`/api/tasks/${filedTask.id}`);

// Remove again and verify cleared.
await tpScope.getByRole("button", { name: "Remove acme/lib · read" }).click();
await page.waitForTimeout(400);
await tpScope
  .getByRole("button", { name: "Remove acme/other · staging · write" })
  .click();
await page.waitForTimeout(400);
await page.reload();
await tpScope.waitFor();
const tpAfterSecondRemove = await apiGet(`/api/tasks/${filedTask.id}`);

results.surfaces.taskPane = {
  target: tpTarget,
  menu: tpMenu,
  readFocus: tpReadFocus,
  writeFocus: tpWriteFocus,
  afterInitialRemove: {
    reads: tpAfterFirstRemove.reads || [],
    writes: tpAfterFirstRemove.writes || [],
  },
  afterAddReload: {
    reads: tpAfterAdd.reads || [],
    writes: tpAfterAdd.writes || [],
  },
  afterFinalRemoveReload: {
    reads: tpAfterSecondRemove.reads || [],
    writes: tpAfterSecondRemove.writes || [],
    grantEvents: (tpAfterSecondRemove.events || []).filter((e) =>
      /^grant-/.test(e.kind),
    ),
  },
};

// --- 3. Template editor ---
await page.goto(`${base}/processes/templates/${args.templateId}`);
const tplScope = page.getByTestId("template-grants");
await tplScope.waitFor();

const tplTarget = await checkTargetUnremovable(tplScope);
const tplMenu = await checkMenuHasNoCIRepoRead(tplScope);
const tplReadFocus = await addRepoGrantAndCheckFocus(tplScope, "read", "acme/lib");
const tplWriteFocus = await addRepoGrantAndCheckFocus(tplScope, "write", "acme/other");
await setWriteBase(tplScope, "acme/other", "staging");
await page.getByRole("button", { name: "Save", exact: true }).click();
await page.waitForTimeout(700);

await page.goto(`${base}/processes/templates/${args.templateId}`);
await tplScope.waitFor();
const tplList1 = await apiGet("/api/templates");
const tplAfterAdd = (Array.isArray(tplList1) ? tplList1 : tplList1.items).find(
  (t) => t.id === args.templateId,
);

await tplScope.getByRole("button", { name: "Remove acme/lib · read" }).click();
await tplScope
  .getByRole("button", { name: "Remove acme/other · staging · write" })
  .click();
await page.getByRole("button", { name: "Save", exact: true }).click();
await page.waitForTimeout(700);
await page.goto(`${base}/processes/templates/${args.templateId}`);
await tplScope.waitFor();
const tplList2 = await apiGet("/api/templates");
const tplAfterRemove = (Array.isArray(tplList2) ? tplList2 : tplList2.items).find(
  (t) => t.id === args.templateId,
);

results.surfaces.templateEditor = {
  target: tplTarget,
  menu: tplMenu,
  readFocus: tplReadFocus,
  writeFocus: tplWriteFocus,
  afterAddReload: {
    reads: tplAfterAdd.reads || [],
    writes: tplAfterAdd.writes || [],
  },
  afterRemoveReload: {
    reads: tplAfterRemove.reads || [],
    writes: tplAfterRemove.writes || [],
  },
};

// --- 4. Schedule editor ---
await page.goto(`${base}/processes/schedules/${args.scheduleId}`);
const schScope = page.getByTestId("schedule-grants");
await schScope.waitFor();

const schTarget = await checkTargetUnremovable(schScope);
const schMenu = await checkMenuHasNoCIRepoRead(schScope);
const schReadFocus = await addRepoGrantAndCheckFocus(schScope, "read", "acme/lib");
const schWriteFocus = await addRepoGrantAndCheckFocus(schScope, "write", "acme/other");
await setWriteBase(schScope, "acme/other", "staging");
await page.getByRole("button", { name: "Save", exact: true }).click();
await page.waitForTimeout(700);

await page.goto(`${base}/processes/schedules/${args.scheduleId}`);
await schScope.waitFor();
const schList1 = await apiGet("/api/schedules");
const schAfterAdd = (Array.isArray(schList1) ? schList1 : schList1.items).find(
  (s) => s.id === args.scheduleId,
);

await schScope.getByRole("button", { name: "Remove acme/lib · read" }).click();
await schScope
  .getByRole("button", { name: "Remove acme/other · staging · write" })
  .click();
await page.getByRole("button", { name: "Save", exact: true }).click();
await page.waitForTimeout(700);
await page.goto(`${base}/processes/schedules/${args.scheduleId}`);
await schScope.waitFor();
const schList2 = await apiGet("/api/schedules");
const schAfterRemove = (Array.isArray(schList2) ? schList2 : schList2.items).find(
  (s) => s.id === args.scheduleId,
);

results.surfaces.scheduleEditor = {
  target: schTarget,
  menu: schMenu,
  readFocus: schReadFocus,
  writeFocus: schWriteFocus,
  afterAddReload: {
    reads: schAfterAdd.reads || [],
    writes: schAfterAdd.writes || [],
  },
  afterRemoveReload: {
    reads: schAfterRemove.reads || [],
    writes: schAfterRemove.writes || [],
  },
};

// --- 5. Console session page (ops with readsAll=false, plus check director with readsAll=true) ---
await page.goto(`${base}/session/ops`);
await page.getByRole("button", { name: "Edit Ops capabilities" }).click();
const csScope = page.getByTestId("session-ops-grants");
await csScope.waitFor();

const csTarget = await checkTargetUnremovable(csScope);
const csMenu = await checkMenuHasNoCIRepoRead(csScope);
const csReadFocus = await addRepoGrantAndCheckFocus(csScope, "read", "acme/lib");
await page.waitForTimeout(500);
const csWriteFocus = await addRepoGrantAndCheckFocus(csScope, "write", "acme/other");
await page.waitForTimeout(500);
await setWriteBase(csScope, "acme/other", "staging");
await page.reload();
await page.getByRole("button", { name: "Edit Ops capabilities" }).click();
await csScope.waitFor();
const csSessions1 = await apiGet("/api/console-sessions");
const opsAfterAdd = csSessions1.sessions.find((s) => s.id === "ops");

await csScope.getByRole("button", { name: "Remove acme/lib · read" }).click();
await page.waitForTimeout(500);
await csScope
  .getByRole("button", { name: "Remove acme/other · staging · write" })
  .click();
await page.waitForTimeout(500);
await page.reload();
await page.getByRole("button", { name: "Edit Ops capabilities" }).click();
await csScope.waitFor();
const csSessions2 = await apiGet("/api/console-sessions");
const opsAfterRemove = csSessions2.sessions.find((s) => s.id === "ops");

// Also check Director (readsAll=true): check what options "Read a repository" offers!
await page.goto(`${base}/session/director`);
await page.getByRole("button", { name: "Edit Director capabilities" }).click();
const dirScope = page.getByTestId("session-director-grants");
await dirScope.waitFor();
await dirScope.getByRole("button", { name: "Add grant…" }).first().click();
await page.getByRole("menuitem", { name: /Read a repository/ }).click();
const dirReadBox = dirScope.getByRole("textbox", { name: "Repo to read", exact: true });
await dirReadBox.click();
await page.waitForTimeout(200);
const dirOfferedAutoReadOptions = await page
  .getByRole("menuitem")
  .evaluateAll((els) => els.map((e) => e.textContent.trim()));

results.surfaces.consoleSession = {
  target: csTarget,
  menu: csMenu,
  readFocus: csReadFocus,
  writeFocus: csWriteFocus,
  afterAddReload: {
    reads: opsAfterAdd.reads || [],
    writes: opsAfterAdd.writes || [],
  },
  afterRemoveReload: {
    reads: opsAfterRemove.reads || [],
    writes: opsAfterRemove.writes || [],
  },
  directorOfferedReadOptionsWhenReadsAllTrue: dirOfferedAutoReadOptions,
};

// --- 6. Declared template push: refs on a template-filed task ---
await page.goto(`${base}/tasks/${args.pushTaskId}`);
const pushTaskScope = page.getByTestId(`task-${args.pushTaskId}-grants`);
await pushTaskScope.waitFor();
const pushLines = await pushTaskScope
  .locator('[data-grant-kind="repo-push"]')
  .evaluateAll((els) =>
    els.map((e) => ({
      grant: e.dataset.grant,
      text: e.textContent.replace(/\s+/g, " ").trim(),
      buttons: e.querySelectorAll("button").length,
    })),
  );
results.pushCheck = { pushLines };

// --- 7. Verify CI page (where reads are NOT edited) DOES offer repo#read ---
await page.goto(`${base}/repos/acme/widgets#ci`);
const ciScope = page.getByTestId("repo-ci-grants");
await ciScope.waitFor();
results.ciPageHasRepoRead = await checkMenuHasNoCIRepoRead(ciScope);

fs.writeFileSync(outFile, JSON.stringify(results, null, 2));
await browser.close();
