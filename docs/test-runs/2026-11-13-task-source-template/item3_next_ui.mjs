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

// §1 item 3: on a live daemon's /next/, files one task from a repository's
// declared template, one from a deployment template and one from scratch
// through the New-task sheet (`c`), then renders /next/tasks and each
// /next/task/<id> at 1440x900 and 390x844 and reads templateId back from
// GET /api/tasks and GET /api/tasks/<id>.
//
//   node item3_next_ui.mjs <url> <access-code> <outdir> <declTemplateId> <deployTemplateId>

import path from "node:path";
import fs from "node:fs";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
const { chromium } = await import(path.join(root, "ui/node_modules/@playwright/test/index.mjs"));
const { signIn } = await import(path.join(root, "ui/e2e/accesscode.js"));

const [url, code, outdir, declTmpl, deployTmpl] = process.argv.slice(2);
fs.mkdirSync(outdir, { recursive: true });
const apiGet = async (p) => {
  const r = await fetch(url + p, { headers: { Authorization: `Bearer ${code}` } });
  if (!r.ok) throw new Error(`GET ${p}: ${r.status} ${await r.text()}`);
  return r.json();
};

const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
await context.addCookies(cookies);
const page = await context.newPage();
const pageErrors = [];
page.on("pageerror", (e) => pageErrors.push(String(e)));
const sheet = page.getByTestId("new-task-sheet");

async function file(label, fill) {
  await page.goto(`${url}/next/tasks`, { waitUntil: "networkidle" });
  await page.keyboard.press("c");
  await sheet.waitFor({ state: "visible", timeout: 15000 });
  await fill();
  const resp = page.waitForResponse(
    (r) => r.request().method() === "POST" && /\/api\/tasks(\/from-template)?$/.test(new URL(r.url()).pathname),
  );
  await page.getByTestId("new-task-submit").click();
  const r = await resp;
  const route = new URL(r.url()).pathname;
  if (!r.ok()) {
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(outdir, `${label}-failed.png`) });
    throw new Error(`${label}: ${route} answered ${r.status()} ${(await r.text()).trim()}`);
  }
  await sheet.waitFor({ state: "hidden", timeout: 10000 });
  return { id: (await r.json()).id, route };
}
const pickTemplate = (id) => async () => {
  await page.getByTestId("nt-template").click();
  await page.getByTestId(`nt-template-opt-${id}`).click();
  await page.getByTestId("nt-composition-preview").waitFor({ state: "visible", timeout: 10000 });
};

const decl = await file("declared-template", pickTemplate(declTmpl));
const deploy = await file("deployment-template", pickTemplate(deployTmpl));
const scratch = await file("scratch", async () => {
  await page.getByTestId("nt-title").fill("Scratch task from the /next/ sheet");
  await page.getByTestId("nt-description").fill("Filed without a template.");
});

const layout = [];
for (const vp of [
  { name: "desktop-1440", width: 1440, height: 900 },
  { name: "phone-390", width: 390, height: 844 },
]) {
  await page.setViewportSize({ width: vp.width, height: vp.height });
  for (const r of ["/next/tasks", `/next/task/${decl.id}`, `/next/task/${deploy.id}`, `/next/task/${scratch.id}`]) {
    await page.goto(`${url}${r}`, { waitUntil: "networkidle" });
    await page.waitForTimeout(300);
    const d = await page.evaluate(() => ({
      overflow: document.documentElement.scrollWidth - window.innerWidth,
      text: document.body.innerText.trim().length,
    }));
    await page.screenshot({ path: path.join(outdir, `${vp.name}${r.replaceAll("/", "_")}.png`) });
    layout.push({ viewport: vp.name, route: r, ...d, ok: d.overflow <= 1 && d.text > 20 });
  }
}

const list = Object.fromEntries((await apiGet("/api/tasks?fields=full")).map((t) => [t.id, t]));
const detail = async (id) => {
  const d = await apiGet(`/api/tasks/${id}`);
  return d.task ?? d;
};
const row = async (t) => {
  const d = await detail(t.id);
  return {
    id: t.id,
    filedThrough: t.route,
    list: Object.hasOwn(list[t.id] ?? {}, "templateId") ? list[t.id].templateId : "<omitted>",
    detail: Object.hasOwn(d, "templateId") ? d.templateId : "<omitted>",
  };
};
const summary = {
  declaredTemplate: await row(decl),
  deploymentTemplate: await row(deploy),
  scratch: await row(scratch),
  layout,
  pageErrors,
};
console.log(JSON.stringify(summary, null, 2));
await browser.close();

const ok =
  summary.declaredTemplate.list === declTmpl &&
  summary.declaredTemplate.detail === declTmpl &&
  summary.deploymentTemplate.list === deployTmpl &&
  summary.deploymentTemplate.detail === deployTmpl &&
  summary.scratch.list === "<omitted>" &&
  summary.scratch.detail === "<omitted>" &&
  layout.every((l) => l.ok) &&
  pageErrors.length === 0;
process.exit(ok ? 0 : 1);
