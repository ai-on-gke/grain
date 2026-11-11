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

// Playwright verification for §6.3:
//   1. Opens /settings/hostds and /settings/capabilities against the live daemon
//      with a connected `grain-agentd` ("agentd-walk") and a pre-placed failed
//      capability server ("https://walk.example/cap/preplaced").
//   2. Verifies the failed server's detail page displays "the host agent refused:"
//      with zero standalone "hostd" leaks and zero horizontal overflow.
//   3. Drives "Add a capability server" -> Run location: host -> Host: agentd-walk ->
//      "Test server settings" & "Save server settings" and verifies the UI displays
//      the clear `grain-agentd` / `grain host` diagnostic with zero "hostd" leaks.

import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const url = process.env.GRAIN_URL || "http://127.0.0.1:4290";
const code = process.env.GRAIN_CODE;
const hostName = process.env.AGENTD_NAME || "agentd-walk";
const preplacedId =
  process.env.PREPLACED_ID || "https://walk.example/cap/preplaced";

const res = await fetch(`${url}/api/auth/access-code`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ code }),
});
if (!res.ok) {
  throw new Error(`sign-in failed: ${res.status}`);
}
const cookies = res.headers.getSetCookie().map((h) => {
  const [pair] = h.split(";");
  const at = pair.indexOf("=");
  return {
    name: pair.slice(0, at).trim(),
    value: pair.slice(at + 1).trim(),
    domain: new URL(url).hostname,
    path: "/",
  };
});

function countHostdLeaks(text) {
  // Ignore file/URL paths (/hostd/install, hostd.sock, hostd.yaml, -hostd-*),
  // flagging any standalone prose word "hostd" or "hostds".
  const matches = text.match(/(?<![/\w.-])hostds?(?![\w./-])/gi) || [];
  return matches;
}

const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  colorScheme: "dark",
});
await ctx.addCookies(cookies);
const page = await ctx.newPage();

// 1. Check /settings/hostds
await page.goto(`${url}/settings/hostds`, { waitUntil: "load" });
await page.waitForTimeout(1500);
const hostsText = await page.evaluate(() => document.body.innerText);
const hostsOverflow = await page.evaluate(
  () =>
    document.documentElement.scrollWidth - document.documentElement.clientWidth,
);
const hostsLeaks = countHostdLeaks(hostsText);
if (!hostsText.includes(hostName)) {
  throw new Error(
    `/settings/hostds did not show enrolled grain-agentd ${hostName}`,
  );
}
if (hostsLeaks.length > 0 || hostsOverflow > 0) {
  throw new Error(
    `/settings/hostds failed hygiene: leaks=${JSON.stringify(hostsLeaks)} overflow=${hostsOverflow}`,
  );
}
console.log(
  `PASS UI /settings/hostds: "${hostName}" visible, overflow=${hostsOverflow}px, hostd_leaks=0`,
);

// 2. Check pre-placed capability server runtime refusal on /settings/capabilities
await page.goto(`${url}/settings/capabilities#capability-servers`, {
  waitUntil: "load",
});
await page.waitForTimeout(1500);
await page.locator(`[data-capability-server-row="${preplacedId}"]`).click();
await page.waitForTimeout(800);
const detailText = await page.evaluate(() => document.body.innerText);
const detailLeaks = countHostdLeaks(detailText);
if (
  !detailText.includes("runs agent CLIs only (grain-agentd)") ||
  !detailText.includes("grain host")
) {
  throw new Error(
    `Expected grain-agentd / grain host refusal on pre-placed server detail page; got:\n${detailText}`,
  );
}
if (detailLeaks.length > 0) {
  throw new Error(
    `Pre-placed server detail leaked hostd: ${JSON.stringify(detailLeaks)}`,
  );
}
console.log(
  `PASS UI pre-placed server detail: displays "runs agent CLIs only (grain-agentd)" & "grain host" with hostd_leaks=0`,
);

// 3. Drive "+ Add server" form placing onto agentd-walk
await page.getByTestId("capability-servers-back").click();
await page.waitForTimeout(500);
await page.getByRole("button", { name: /^\+ add server$/i }).click();
await page
  .getByTestId("capability-field-capabilityId")
  .locator("input")
  .fill("https://walk.example/cap/ui-attempt");
await page
  .getByTestId("capability-field-command")
  .locator("input")
  .fill("walk-echo");

// Select Run location -> host
await page.getByTestId("capability-server-run").getByRole("combobox").click();
await page.getByRole("option", { name: /host \(through the host agent\)/i }).click();

// Select placed host -> agentd-walk
await page.getByTestId("capability-server-hostd").getByRole("combobox").click();
await page.getByRole("option", { name: new RegExp(hostName) }).click();

// Click "Test"
await page.getByRole("button", { name: /^test$/i }).click();
await page.waitForTimeout(1000);
let addText = await page.evaluate(() => document.body.innerText);
if (
  !addText.includes("runs agent CLIs only (grain-agentd)") ||
  !addText.includes("grain host")
) {
  throw new Error(
    `Expected grain-agentd / grain host diagnostic after Test; got:\n${addText}`,
  );
}

// Click "Add server" and confirm the error banner/toast also shows the diagnostic
await page.getByRole("button", { name: /^add server$/i }).click();
await page.waitForTimeout(1000);
addText = await page.evaluate(() => document.body.innerText);
const addLeaks = countHostdLeaks(addText);
const addOverflow = await page.evaluate(
  () =>
    document.documentElement.scrollWidth - document.documentElement.clientWidth,
);
if (
  !addText.includes("runs agent CLIs only (grain-agentd)") ||
  !addText.includes("enrolled with `grain host`")
) {
  throw new Error(
    `Expected grain-agentd / enrolled with grain host diagnostic after Add server; got:\n${addText}`,
  );
}
if (addLeaks.length > 0 || addOverflow > 0) {
  throw new Error(
    `Add capability server page failed hygiene: leaks=${JSON.stringify(addLeaks)} overflow=${addOverflow}`,
  );
}
console.log(
  `PASS UI Add capability server onto ${hostName}: displays "runs agent CLIs only (grain-agentd)" & "enrolled with \`grain host\`", overflow=${addOverflow}px, hostd_leaks=0`,
);

await browser.close();
