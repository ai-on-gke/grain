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

// docs/test-plans/2026-11-09.md §1 items 1–3 (browser half), against the
// real daemon item1_seed.sh seeded. For each form that lost its Legacy
// capabilities picker it lists the grant chips held (one per grant, no
// generic capability-method chip beside an API one), ticks an unrelated
// API scope with GrantsPicker, saves, unticks it, saves, and reloads;
// item1_seed.sh's snapshot then diffs the stored objects. Every request
// the pages make is watched for the removed
// POST /api/capabilities/{id}/check.
//
//   cd ui && node ../docs/test-runs/2026-11-09-access-forms-api-grants/ui_walkthrough.mjs \
//     http://127.0.0.1:4295 ACCESS_CODE IDS_JSON OUT_JSON
import { createRequire } from "node:module";
import fs from "node:fs";

// Resolved from ui/node_modules, the directory this is run from.
const { chromium } = createRequire(`${process.cwd()}/`)("playwright");

const [base, code, idsFile, outFile] = process.argv.slice(2);
const ids = JSON.parse(fs.readFileSync(idsFile, "utf8"));
const results = [];
const checkCalls = [];
const pageErrors = [];
const record = (form, step, data) => {
  results.push({ form, step, ...data });
  console.log(form, step, JSON.stringify(data));
};

const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  extraHTTPHeaders: { Authorization: `Bearer ${code}` },
});
const page = await context.newPage();
page.on("request", (r) => {
  if (/\/api\/capabilities\/[^/]+\/check/.test(r.url()))
    checkCalls.push(`${r.method()} ${r.url()}`);
});
// What each form sends back: the held bare id must be in it, unchanged.
const writes = [];
page.on("request", (r) => {
  if (!["PUT", "POST", "PATCH"].includes(r.method())) return;
  if (
    !/\/api\/(tasks\/[^/]+\/capabilities|templates|schedules|settings|repos\/[^/]+\/[^/]+\/capabilities)/.test(
      r.url(),
    )
  )
    return;
  writes.push({
    method: r.method(),
    url: r.url().replace(base, ""),
    body: r.postData(),
  });
});
page.on("pageerror", (e) => pageErrors.push(String(e)));
page.on("dialog", (d) => d.accept());

async function held(group) {
  return group
    .locator(".grants-held [data-grant]")
    .evaluateAll((els) =>
      els.map((e) => ({ grant: e.dataset.grant, text: e.textContent.trim() })),
    );
}

async function toggle(group, name) {
  await group.getByRole("button", { name: "Add grant…" }).click();
  const menu = page.getByRole("menu", { name: "Grants to add" });
  await menu.waitFor();
  await page.getByLabel("Search grants").fill(name);
  const item = menu.getByRole("menuitemcheckbox", { name, exact: true });
  const was = await item.getAttribute("aria-checked");
  await item.click();
  await page
    .waitForFunction(
      ([n, w]) => {
        const el = [
          ...document.querySelectorAll('[role="menuitemcheckbox"]'),
        ].find(
          (e) =>
            e.getAttribute("aria-label") === n ||
            e.textContent.trim().startsWith(n),
        );
        return el && el.getAttribute("aria-checked") !== w;
      },
      [name, was],
      { timeout: 5000 },
    )
    .catch(() => {});
  await page.keyboard.press("Escape");
  await page.waitForTimeout(600);
}

async function walk(form, url, testId, save) {
  await page.goto(base + url);
  const group = page.getByTestId(testId);
  await group.waitFor({ timeout: 15000 });
  record(form, "loaded", { held: await held(group) });
  await toggle(group, "tasks:write");
  if (save) await save();
  await page.waitForTimeout(800);
  // A Save closes the template and schedule panes: open them again.
  await page.goto(base + url);
  await group.waitFor();
  record(form, "after-add-reload", { held: await held(group) });
  await toggle(group, "tasks:write");
  if (save) await save();
  await page.waitForTimeout(800);
  await page.goto(base + url);
  await group.waitFor();
  record(form, "after-remove-reload", { held: await held(group) });
  const legacy = await page
    .getByText("Legacy capabilities", { exact: false })
    .count();
  record(form, "legacy-heading", { count: legacy });
}

const clickSave = async () => {
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.waitForTimeout(1000);
};

await walk("task-pane", `/tasks/${ids.task}`, `task-${ids.task}-grants`, null);
await walk(
  "template",
  `/processes/templates/${ids.template}`,
  "template-grants",
  clickSave,
);
await walk(
  "schedule",
  `/processes/schedules/${ids.schedule}`,
  "schedule-grants",
  clickSave,
);
await walk(
  "settings-tasks",
  "/settings/tasks",
  "settings-default-grants",
  null,
);
await walk(
  "repo-defaults",
  "/repos/acme/widgets#agents",
  "repo-default-grants",
  null,
);

// The New task overlay: the defaults it pre-ticks, one chip each.
await page.goto(base + "/");
await page.getByRole("button", { name: "+ New task" }).click();
const nt = page.getByTestId("new-task-grants");
await nt.waitFor({ timeout: 15000 });
record("new-task", "loaded", { held: await held(nt) });

// The Console session page: the Director, which env.sh's caller gave
// both spellings through PUT /api/console-sessions/director.
await page.goto(base + "/sessions");
await page
  .getByRole("button", { name: /^Open session Director/ })
  .first()
  .click();
await page.getByRole("button", { name: "Edit Director capabilities" }).click();
const sg = page.getByTestId("session-director-grants");
await sg.waitFor({ timeout: 15000 });
record("console-session", "loaded", { held: await held(sg) });
await page.keyboard.press("Escape");

// Removing a chip removes the stored grant: api:tasks:read, filed as
// https://grain.dev/capabilities/api#tasks:read, off the seeded task.
await page.goto(`${base}/tasks/${ids.task}`);
const tg = page.getByTestId(`task-${ids.task}-grants`);
await tg.waitFor();
await tg.getByRole("button", { name: "Remove api:tasks:read" }).click();
await page.waitForTimeout(1000);
await page.reload();
await tg.waitFor();
record("task-pane", "after-chip-remove-reload", { held: await held(tg) });

// Pages that used to (or might) call the removed check route.
for (const url of [
  "/settings/capabilities",
  "/settings",
  "/setup",
  "/repos/acme/widgets#ci",
]) {
  await page.goto(base + url);
  await page.waitForTimeout(2500);
}
record("network", "capability-check-calls", { calls: checkCalls });
record("network", "grant-writes", { writes });
record("errors", "page-errors", { errors: pageErrors });
fs.writeFileSync(outFile, JSON.stringify(results, null, 2));
await browser.close();
