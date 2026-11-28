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

// Captures before/after screenshots and stitches contact-sheet.png (1440px and
// 390px, light and dark) for the UI fixes found in docs/test-plans/2026-11-19.md
// §9:
//   - F2: New task sheet -> click Settings -> Capabilities (must close sheet
//         and reveal #capability-policy)
//   - F3: /next/workstreams -> merge_failed / unreadable nodes in error tone
//   - F4: /next/inbox -> request_access card with substitute grant label

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(
  path.resolve(process.cwd(), "ui-ng/package.json"),
);
const { chromium } = require("playwright");

const PROXY = process.env.PROXY || "http://127.0.0.1:4286";
const OUT = process.env.OUT || "/tmp/walk-s9/shots/after";
const IDS = JSON.parse(
  fs.readFileSync(process.env.IDS_FILE || "/tmp/walk-s9/pg/ids.json", "utf8"),
);
fs.mkdirSync(OUT, { recursive: true });

async function api(method, p, body) {
  const res = await fetch(`${PROXY}${p}`, {
    method,
    headers: {
      "X-Iapsim-Email": "alice@example.com",
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    throw new Error(`${method} ${p}: ${res.status} ${await res.text()}`);
  }
}

const shots = [];
const browser = await chromium.launch();
try {
  await api("PUT", "/api/repos/acme/widgets/settings", {
    defaultCapabilities: ["https://grain.dev/capabilities/github#read-issues"],
  });
  await api("PUT", "/api/capability-policy", {
    policy: {
      rules: [
        { capability: "https://grain.dev/capabilities/github#*", deny: true },
      ],
    },
  });

  const combos = [
    ["1440-light", 1440, 900, "light"],
    ["1440-dark", 1440, 900, "dark"],
    ["390-light", 390, 844, "light"],
    ["390-dark", 390, 844, "dark"],
  ];

  for (const [tag, w, h, scheme] of combos) {
    const ctx = await browser.newContext({
      viewport: { width: w, height: h },
      colorScheme: scheme,
      extraHTTPHeaders: { "X-Iapsim-Email": "alice@example.com" },
    });
    const page = await ctx.newPage();
    try {
      await page.goto(`${PROXY}/next/inbox`, { waitUntil: "domcontentloaded" });
      await page.getByRole("button", { name: /^New/ }).first().click();
      await page.getByTestId("new-task-sheet").waitFor({ timeout: 15000 });
      await page.getByText(/Capabilities & repo context/).click();
      const link = page.getByTestId("nt-grants-policy-link").first();
      await link.waitFor({ timeout: 10000 });
      await link.click();
      await page.waitForURL(
        /\/next\/settings\/capabilities#capability-policy$/,
        { timeout: 10000 },
      );
      await new Promise((r) => setTimeout(r, 350));
      const f2Path = path.join(OUT, `f2-policy-link-${tag}.png`);
      await page.screenshot({ path: f2Path });
      shots.push({ label: `F2 Policy link (${tag})`, file: f2Path });
      if (
        await page
          .getByTestId("new-task-sheet")
          .isVisible()
          .catch(() => false)
      ) {
        await page.keyboard.press("Escape");
      }

      await page.goto(`${PROXY}/next/workstreams`, {
        waitUntil: "domcontentloaded",
      });
      await page
        .getByText("Merge badges workstream root")
        .first()
        .waitFor({ timeout: 15000 });
      await page.getByText("Merge badges workstream root").first().click();
      await page
        .getByTestId(`ws-tree-node-${IDS.t_mf}`)
        .waitFor({ timeout: 15000 });
      await new Promise((r) => setTimeout(r, 250));
      const f3Path = path.join(OUT, `f3-workstreams-${tag}.png`);
      await page.screenshot({ path: f3Path });
      shots.push({ label: `F3 Workstreams (${tag})`, file: f3Path });

      await page.goto(`${PROXY}/next/inbox`, { waitUntil: "domcontentloaded" });
      const row = page
        .getByTestId("queue-row")
        .filter({ hasText: "Mixed access tick check" })
        .first();
      await row.waitFor({ timeout: 15000 });
      await row.locator(".row-main").click();
      await page.getByTestId("grant-access").waitFor({ timeout: 15000 });
      await new Promise((r) => setTimeout(r, 250));
      const f4Path = path.join(OUT, `f4-inbox-sub-${tag}.png`);
      await page.screenshot({ path: f4Path });
      shots.push({ label: `F4 Inbox access (${tag})`, file: f4Path });
    } finally {
      await ctx.close();
    }
  }

  const sheetCtx = await browser.newContext({
    viewport: { width: 1760, height: 1200 },
  });
  const sheetPage = await sheetCtx.newPage();
  const order = ["f2-policy-link", "f3-workstreams", "f4-inbox-sub"];
  const cells = [];
  for (const prefix of order) {
    for (const [tag] of combos) {
      const s = shots.find((x) => x.file.endsWith(`${prefix}-${tag}.png`));
      const b64 = fs.readFileSync(s.file).toString("base64");
      cells.push(
        `<figure><figcaption>${s.label}</figcaption><img src="data:image/png;base64,${b64}" /></figure>`,
      );
    }
  }
  await sheetPage.setContent(
    `<!doctype html><html><head><style>
    body { margin: 16px; background: #14171c; color: #e6ecf5; font: 13px/1.3 system-ui, sans-serif; }
    .grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; align-items: start; }
    figure { margin: 0; background: #1e2128; border: 1px solid #383d4a; border-radius: 8px; padding: 8px; }
    figcaption { margin-bottom: 6px; font-weight: 600; }
    img { width: 100%; height: auto; display: block; border-radius: 4px; }
  </style></head><body><div class="grid">${cells.join("")}</div></body></html>`,
  );
  await sheetPage.waitForLoadState("load");
  const sheetPath = path.join(OUT, "contact-sheet.png");
  await sheetPage.screenshot({ path: sheetPath, fullPage: true });
  await sheetCtx.close();
  console.log("Wrote contact sheet:", sheetPath, fs.statSync(sheetPath).size);
} finally {
  await api("PUT", "/api/capability-policy", { policy: { rules: [] } });
  await api("PUT", "/api/repos/acme/widgets/settings", {
    defaultCapabilities: [],
  });
  await browser.close();
}
