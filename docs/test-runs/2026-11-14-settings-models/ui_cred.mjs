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

// docs/test-plans/2026-11-14.md §4 item 4: clear conn-c's stored secret
// from /next/settings/connections of the env.sh daemon and report, after
// each click, whether a confirmation was shown and whether the secret
// (GET /api/secrets: agent-backend-conn-c) and /api/settings' key-set
// flags are still there; then read the autofill-relevant attributes of
// every Settings search field and fill the credential box, to check the
// search fields never take its value. Usage: node ui_cred.mjs

import { createRequire } from "node:module";

const requireNg = createRequire(new URL("../../../ui-ng/package.json", import.meta.url));
const { chromium } = requireNg("@playwright/test");
const proxy = "http://127.0.0.1:4296";

const browser = await chromium.launch();
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
await context.addCookies([{ name: "iapsim_email", value: "alice@example.com", url: proxy }]);
const page = await context.newPage();

async function state(label) {
  const sec = await (await context.request.get(`${proxy}/api/secrets`)).json();
  const set = await (await context.request.get(`${proxy}/api/settings`)).json();
  const has = (sec.secrets || []).some((s) => s.name === "agent-backend-conn-c");
  const flags = ["geminiApiKeySet", "claudeOAuthTokenSet", "openaiApiKeySet", "codexAuthJSONSet"]
    .map((k) => `${k}=${set[k]}`)
    .join(" ");
  const dialog = await page.getByTestId("confirm-dialog").isVisible().catch(() => false);
  console.log(`${label}: confirm dialog shown=${dialog}; secret agent-backend-conn-c stored=${has}; ${flags}`);
}

await page.goto(`${proxy}/next/settings/connections`, { waitUntil: "domcontentloaded" });
await page.getByTestId("toggle-cred-conn-c").waitFor({ timeout: 15000 });
await state("before");
await page.getByTestId("toggle-cred-conn-c").click();
await page.getByTestId("clear-cred-conn-c").click();
await page.waitForTimeout(800);
await state("after clicking Clear stored secret");
if (await page.getByTestId("confirm-dialog").isVisible()) {
  await page.getByTestId("confirm-cancel").click();
  await page.waitForTimeout(500);
  await state("after Cancel");
  await page.getByTestId("clear-cred-conn-c").click();
  await page.getByTestId("confirm-accept").click();
  await page.waitForTimeout(800);
  await state("after confirming");
}

// Autofill isolation: what Chromium's form heuristics read.
for (const section of ["models", "connections", "secrets"]) {
  await page.goto(`${proxy}/next/settings/${section}`, { waitUntil: "domcontentloaded" });
  await page.waitForSelector(".main-stage > *:not(router-outlet)", { timeout: 15000 });
  await page.waitForTimeout(800);
  const fields = await page.locator('input[type="search"]').evaluateAll((els) =>
    els.map((e) => ({
      testid: e.dataset.testid || e.getAttribute("aria-label"),
      name: e.getAttribute("name"),
      autocomplete: e.getAttribute("autocomplete"),
      form: e.form ? { role: e.form.getAttribute("role"), autocomplete: e.form.getAttribute("autocomplete"),
        passwordInputs: e.form.querySelectorAll('input[type="password"]').length } : null,
      value: e.value,
    })),
  );
  console.log(`/next/settings/${section} search fields: ${JSON.stringify(fields)}`);
}
await page.goto(`${proxy}/next/settings/connections`, { waitUntil: "domcontentloaded" });
await page.getByTestId("toggle-cred-conn-c").waitFor({ timeout: 15000 });
await page.getByTestId("toggle-cred-conn-c").click();
await page.locator('input[aria-label="Credential for conn-c"]').fill("walk-typed-secret");
const searchValues = await page.locator('input[type="search"]').evaluateAll((els) => els.map((e) => e.value));
console.log(`after typing into the credential box, search field values: ${JSON.stringify(searchValues)}`);
await browser.close();
