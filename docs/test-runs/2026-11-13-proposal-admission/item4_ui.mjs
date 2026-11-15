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

// §5 item 4 (and item 2's buttons): drives the real daemon's /next/ and
// classic task panes.
//
//   node item4_ui.mjs approve <url> <code> <outdir> <taskId>...
//     clicks Approve on /next/task/<id> and on classic /tasks/<id> and
//     records the error each page shows.
//   node item4_ui.mjs grants <url> <code> <outdir> <nextTaskId> <classicTaskId>
//     adds api:grain/operator and repo#credential {credential: bot} from
//     Add grant… on each pane, tries an ungrantable repo grant, and
//     prints what GET /api/tasks/<id> stores.
import path from "node:path";
import fs from "node:fs";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
const { chromium } = await import(path.join(root, "ui/node_modules/@playwright/test/index.mjs"));
const { signIn } = await import(path.join(root, "ui/e2e/accesscode.js"));

const [mode, url, code, outdir, ...ids] = process.argv.slice(2);
fs.mkdirSync(outdir, { recursive: true });
const api = async (p, init = {}) =>
  (await fetch(url + p, { ...init, headers: { Authorization: `Bearer ${code}`, "Content-Type": "application/json" } })).json();
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
await context.addCookies(cookies);
const page = await context.newPage();
const out = {};
const shot = (name) => page.screenshot({ path: path.join(outdir, `${name}.png`), fullPage: false });
const step = async (name, fn) => {
  try {
    out[name] = (await fn()) ?? "ok";
  } catch (e) {
    out[name] = `FAILED: ${String(e).split("\n")[0]}`;
    await shot(`${name}-failed`).catch(() => {});
  }
};

if (mode === "approve") {
  for (const id of ids) {
    await step(`next-approve-${id}`, async () => {
      await page.goto(`${url}/next/task/${id}`);
      const btn = page.getByRole("button", { name: /^Approve/ }).first();
      await btn.waitFor({ timeout: 20000 });
      const resp = page.waitForResponse((r) => r.url().endsWith(`/api/tasks/${id}/approve`));
      await btn.click();
      const r = await resp;
      await page.waitForTimeout(800);
      const shown = await page.locator('[role="alert"], .banner-error, .toast, [data-testid*="error"]').allInnerTexts();
      await shot(`next-approve-${id}`);
      return { status: r.status(), shown: shown.filter(Boolean).map((s) => s.slice(0, 300)) };
    });
    await step(`classic-approve-${id}`, async () => {
      await page.goto(`${url}/tasks/${id}`);
      const btn = page.getByRole("button", { name: /^Approve/ }).first();
      await btn.waitFor({ timeout: 20000 });
      const resp = page.waitForResponse((r) => r.url().endsWith(`/api/tasks/${id}/approve`));
      await btn.click();
      const r = await resp;
      await page.waitForTimeout(800);
      const shown = await page.locator('[role="alert"], .error, .toast, [class*="error"]').allInnerTexts();
      await shot(`classic-approve-${id}`);
      return { status: r.status(), shown: shown.filter(Boolean).map((s) => s.slice(0, 300)) };
    });
    out[`state-${id}`] = (await api(`/api/tasks/${id}`)).state;
  }
}

if (mode === "grants") {
  const [nextId, classicId] = ids;
  const T = "task-grants";
  await step("next-open", async () => {
    await page.goto(`${url}/next/task/${nextId}`);
    await page.getByTestId(`${T}-add-open`).click({ timeout: 20000 });
    await page.getByTestId(`${T}-mode-api`).click();
    await page.getByTestId(`${T}-api-set`).click();
    const opts = await page.getByTestId(`${T}-api-set-menu`).locator('[role="option"]').allInnerTexts();
    return { apiOptions: opts.map((o) => o.split(" — ")[0]) };
  });
  await step("next-add-grain-operator", async () => {
    await page.getByTestId(`${T}-api-set-opt-grain/operator`).click();
    await page.getByTestId(`${T}-attach`).click();
    await page.waitForTimeout(1500);
    const err = await page.getByTestId(`${T}-error`).allInnerTexts();
    return { error: err };
  });
  await step("next-add-repo-credential", async () => {
    await page.getByTestId(`${T}-mode-method`).click();
    await page.getByTestId(`${T}-method-ref`).click();
    await page.getByTestId(`${T}-method-ref-opt-https://grain.dev/capabilities/repo#credential`).click();
    await page.getByTestId(`${T}-method-args`).fill('{"credential":"bot"}');
    await page.getByTestId(`${T}-attach`).click();
    await page.waitForTimeout(1500);
    return { error: await page.getByTestId(`${T}-error`).allInnerTexts() };
  });
  await step("next-ungrantable-repo", async () => {
    await page.getByTestId(`${T}-mode-raw`).click();
    await page.getByTestId(`${T}-raw-input`).fill("repo-read:acme/gadgets");
    await page.getByTestId(`${T}-attach`).click();
    await page.waitForTimeout(1500);
    await shot("next-grants");
    return { error: await page.getByTestId(`${T}-error`).allInnerTexts() };
  });
  out.nextStored = (await api(`/api/tasks/${nextId}`)).capabilities;

  const section = page.getByTestId(`task-${classicId}-grants`);
  const menu = page.getByRole("menu", { name: "Grants to add" });
  await step("classic-add-grain-operator", async () => {
    await page.goto(`${url}/tasks/${classicId}`);
    await section.getByRole("button", { name: "Add grant…" }).click({ timeout: 20000 });
    await page.getByLabel("Search grants").fill("grain/operator");
    // Rows are menuitemcheckbox toggles and the menu stays open for more:
    // close it before the next Add grant….
    await menu.locator('[data-api-grant="api:grain/operator"]').click();
    await page.waitForTimeout(1500);
    await page.keyboard.press("Escape");
  });
  await step("classic-add-repo-credential", async () => {
    await section.getByRole("button", { name: "Add grant…" }).click({ timeout: 5000 });
    await page.getByLabel("Search grants").fill("credential");
    await menu.getByText("repo server › credential").click();
    const draft = section.locator("[data-grant-draft]").first();
    await draft.waitFor({ timeout: 5000 });
    // The credential argument is a select over the configured
    // credentials: choosing one grants the draft ("Fill in the arguments
    // marked to grant it").
    await draft.locator("[role=combobox][id$='-credential']").click();
    await page.getByRole("option", { name: "bot" }).click();
    const buttons = await draft.getByRole("button").allInnerTexts();
    await page.waitForTimeout(1500);
    await shot("classic-grants");
    return { draftButtons: buttons };
  });
  out.classicStored = (await api(`/api/tasks/${classicId}`)).capabilities;
}

console.log(JSON.stringify(out, null, 2));
await browser.close();
