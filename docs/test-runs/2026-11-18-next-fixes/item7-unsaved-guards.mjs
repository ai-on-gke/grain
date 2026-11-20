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

// Item 7: the schedule, template and suite dialogs against a real daemon.
// A save that succeeds never asks "Discard unsaved changes?"; a save the
// server refuses keeps the draft and still guards Escape.
import { chromium, session, watch } from "./lib.mjs";

const base = process.env.BASE, code = process.env.CODE;
const browser = await chromium.launch();
const ctx = await session(browser, base, code);
const page = await ctx.newPage();
const log = watch(page);
const discard = page.getByRole("dialog", { name: "Discard unsaved changes?" });
const results = [];
const check = (name, ok, detail = "") => { results.push({ name, ok, detail }); console.log(`${ok ? "PASS" : "FAIL"} ${name} ${detail}`); };

const cases = [
  { kind: "schedule", url: "/next/processes/schedule/sched-2", edit: "sched-edit-btn", dlg: "schedule-dialog", input: "sched-base-input", save: "sched-save-btn", route: /\/api\/schedules\/sched-2$/, api: "/api/schedules", id: "sched-2", field: "base", value: "release" },
  { kind: "template", url: "/next/processes/templates/template-2", edit: "tmpl-edit-btn", dlg: "template-dialog", input: "tmpl-title-input", save: "tmpl-save-btn", route: /\/api\/templates\/template-2$/, api: "/api/templates", id: "template-2", field: "title", value: "Template bound to default branch (saved)" },
  { kind: "suite", url: "/next/processes/suites/suite-1", edit: "suite-edit-btn", dlg: "suite-dialog", input: "suite-name-input", save: "suite-save-btn", route: /\/api\/suites\/suite-1$/, api: "/api/suites", id: "suite-1", field: "name", value: "Walk suite (saved)" },
];

for (const c of cases) {
  await page.goto(`${base}${c.url}`);
  const dlg = page.getByTestId(c.dlg);
  // A real save.
  await page.getByTestId(c.edit).click();
  await dlg.getByTestId(c.input).fill(c.value);
  await dlg.getByTestId(c.save).click();
  await dlg.waitFor({ state: "hidden", timeout: 10000 }).catch(() => {});
  await page.waitForTimeout(500);
  check(`${c.kind}: successful save closes the dialog`, !(await dlg.isVisible()));
  check(`${c.kind}: successful save never asks to discard`, (await discard.count()) === 0);
  const res = await fetch(`${base}${c.api}`, { headers: { Authorization: `Bearer ${code}` } });
  const body = await res.json();
  const row = (Array.isArray(body) ? body : body[c.kind === "suite" ? "suites" : `${c.kind}s`]).find((r) => r.id === c.id);
  check(`${c.kind}: server holds the saved ${c.field}`, row?.[c.field] === c.value, JSON.stringify(row?.[c.field]));
  // A failing save.
  await page.route(c.route, (route) => (route.request().method() === "GET" ? route.continue() : route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "walk: store refused the write" }) })));
  await page.getByTestId(c.edit).click();
  await dlg.getByTestId(c.input).fill(`${c.value} again`);
  await dlg.getByTestId(c.save).click();
  await page.waitForTimeout(1200);
  check(`${c.kind}: failed save keeps the dialog open`, await dlg.isVisible());
  check(`${c.kind}: failed save keeps the draft`, (await dlg.getByTestId(c.input).inputValue().catch(() => "")) === `${c.value} again`);
  const errText = (await dlg.innerText().catch(() => "")).match(/walk: store refused the write|error|failed/i)?.[0] ?? "";
  check(`${c.kind}: failed save says why`, !!errText, JSON.stringify(errText));
  check(`${c.kind}: failed save asks nothing yet`, (await discard.count()) === 0);
  await page.keyboard.press("Escape");
  await page.waitForTimeout(400);
  check(`${c.kind}: Escape after a failed save asks once`, (await discard.count()) === 1);
  await page.screenshot({ path: `/tmp/item7-${c.kind}-guard.png` });
  await discard.getByRole("button", { name: "Discard" }).click();
  await page.waitForTimeout(300);
  check(`${c.kind}: Discard closes both`, !(await dlg.isVisible()) && (await discard.count()) === 0);
  await page.unroute(c.route);
}
console.log(`errors=${JSON.stringify(log)}`);
console.log(`summary: ${results.filter((r) => r.ok).length}/${results.length} passed`);
await browser.close();
