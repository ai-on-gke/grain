#!/usr/bin/env node
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

// Real-browser Playwright inspector for the 2026-11-16 §3 walkthrough.
// Drives real Chromium against a live grain daemon authenticated via real
// Grain Hub JWT assertions (X-Grain-Hub-Assertion) with zero route mocking.

import fs from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const require = createRequire(
  path.resolve(__dirname, "../../../ui-ng/package.json"),
);
const { chromium } = require("@playwright/test");

function parseArgs(argv) {
  const out = { cmd: argv[0], opts: {} };
  for (let i = 1; i < argv.length; i++) {
    const a = argv[i];
    if (a.startsWith("--")) {
      const key = a.slice(2);
      const next = argv[i + 1];
      if (!next || next.startsWith("--")) {
        out.opts[key] = "true";
      } else {
        out.opts[key] = next;
        i++;
      }
    }
  }
  return out;
}

async function openContext(browser, baseURL, jwt, width = 1440, height = 900) {
  const context = await browser.newContext({
    baseURL,
    viewport: { width, height },
    extraHTTPHeaders: {
      "X-Grain-Hub-Assertion": jwt,
    },
  });
  const page = await context.newPage();
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));
  return { context, page, pageErrors };
}

async function inspectNext(browser, opts) {
  const { context, page, pageErrors } = await openContext(
    browser,
    opts.url,
    opts.jwt,
    Number(opts.width || 1440),
    Number(opts.height || 900),
  );
  try {
    await page.goto(`/next/task/${opts.task}?tab=changes`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("merge-gates-list").waitFor({ state: "visible", timeout: 15000 });
    // Let any initial gate card / config render settle.
    await page.waitForTimeout(500);

    const gates = await page.locator('[data-testid^="merge-gate-"]').evaluateAll((els) =>
      els.map((el) => ({
        testid: el.getAttribute("data-testid"),
        text: el.textContent.trim().replace(/\s+/g, " "),
      })),
    );
    const acceptButtons = await page
      .locator('[data-testid^="accept-findings-"]')
      .evaluateAll((els) =>
        els.map((el) => ({
          testid: el.getAttribute("data-testid"),
          text: el.textContent.trim(),
          disabled: el.disabled,
        })),
      );

    const holdCard = page.getByTestId("human-review-hold");
    const holdVisible = await holdCard.isVisible().catch(() => false);
    let hold = { visible: false };
    if (holdVisible) {
      const releaseBtn = holdCard.getByTestId("release-hold-btn");
      const overrideBtn = holdCard.getByTestId("hold-force-merge-open");
      const noteEl = holdCard.getByTestId("hold-findings-note");
      hold = {
        visible: true,
        releaseVisible: await releaseBtn.isVisible().catch(() => false),
        overrideVisible: await overrideBtn.isVisible().catch(() => false),
        noteText: (await noteEl.isVisible().catch(() => false))
          ? (await noteEl.textContent()).trim().replace(/\s+/g, " ")
          : "",
        reasonsText: (await holdCard.locator(".gate-access-list li").allTextContents()).map((s) =>
          s.trim().replace(/\s+/g, " "),
        ),
      };
    }

    // Check Force merge dialog Accept findings hint if Force merge button is present and enabled.
    let forceMerge = { buttonVisible: false, acceptHintVisible: false, gatesInDialog: [] };
    const fmBtn = page.getByTestId("changes-force-merge-open");
    if (await fmBtn.isVisible().catch(() => false)) {
      forceMerge.buttonVisible = true;
      forceMerge.buttonDisabled = await fmBtn.isDisabled();
      if (!forceMerge.buttonDisabled && opts.openForceMerge === "true") {
        await fmBtn.click();
        const dlg = page.getByTestId("force-merge-dialog");
        await dlg.waitFor({ state: "visible", timeout: 5000 });
        forceMerge.acceptHintVisible = await dlg
          .getByTestId("force-merge-accept-findings-hint")
          .isVisible()
          .catch(() => false);
        forceMerge.gatesInDialog = await dlg
          .locator('[data-testid^="override-gate-"]')
          .evaluateAll((els) => els.map((e) => e.getAttribute("data-testid")));
        await page.keyboard.press("Escape");
      }
    }

    // Optional: try clicking an accept-findings button and report HTTP response!
    let acceptClickResult = null;
    if (opts.clickAccept) {
      const btn = page.getByTestId(`accept-findings-${opts.clickAccept}`);
      if (await btn.isVisible().catch(() => false)) {
        await btn.click();
        const dlg = page.getByTestId("accept-findings-dialog");
        await dlg.waitFor({ state: "visible", timeout: 5000 });
        const submitBtn = dlg.getByTestId("accept-findings-confirm");
        const initiallyDisabled = await submitBtn.isDisabled();
        const reason = opts.reason || "Walkthrough acceptance reason";
        await dlg.getByTestId("accept-findings-reason").fill(reason);
        const enabledAfterFill = !(await submitBtn.isDisabled());
        const respPromise = page.waitForResponse(
          (r) => r.url().includes(`/api/tasks/${opts.task}/accept-findings`) && r.request().method() === "POST",
        );
        await submitBtn.click();
        const resp = await respPromise;
        acceptClickResult = {
          initiallyDisabled,
          enabledAfterFill,
          status: resp.status(),
          body: await resp.text(),
        };
        await page.waitForTimeout(400);
      }
    }

    // Optional: click Release hold button and report HTTP response!
    let releaseClickResult = null;
    if (opts.clickRelease === "true" && hold.releaseVisible) {
      const respPromise = page.waitForResponse(
        (r) => r.url().includes(`/api/tasks/${opts.task}/human-review/release`) && r.request().method() === "POST",
      );
      await holdCard.getByTestId("release-hold-btn").click();
      const resp = await respPromise;
      releaseClickResult = {
        status: resp.status(),
        body: await resp.text(),
      };
      await page.waitForTimeout(400);
    }

    console.log(
      JSON.stringify({
        gates,
        acceptButtons,
        hold,
        forceMerge,
        acceptClickResult,
        releaseClickResult,
        pageErrors,
      }),
    );
  } finally {
    await context.close();
  }
}

async function inspectClassic(browser, opts) {
  const { context, page, pageErrors } = await openContext(
    browser,
    opts.url,
    opts.jwt,
    Number(opts.width || 1440),
    Number(opts.height || 900),
  );
  try {
    await page.goto(`/tasks/${opts.task}`, { waitUntil: "domcontentloaded" });
    await page.locator('[data-testid="human-review-hold"], [data-testid^="merge-gate-"]').first().waitFor({ state: "visible", timeout: 15000 }).catch(() => {});
    await page.waitForTimeout(700);
    const holdEl = page.getByTestId("human-review-hold");
    const holdVisible = await holdEl.isVisible().catch(() => false);
    let hold = { visible: false };
    if (holdVisible) {
      const releaseBtn = page.getByTestId("release-human-review");
      const releaseCount = await releaseBtn.count();
      const releaseVisible = releaseCount > 0 ? await releaseBtn.isVisible() : false;
      const releaseDisabled = releaseVisible ? await releaseBtn.isDisabled() : null;
      hold = {
        visible: true,
        text: (await holdEl.textContent()).trim().replace(/\s+/g, " "),
        releaseVisible,
        releaseDisabled,
      };
    }

    // Look for merge gates rendered on classic task detail (including targeted-review).
    const gateChips = await page.locator('[data-testid^="merge-gate-"]').evaluateAll((els) =>
      els.map((el) => ({
        testid: el.getAttribute("data-testid"),
        text: el.textContent.trim().replace(/\s+/g, " "),
      })),
    );
    const bodyText = await page.locator("body").textContent();

    let releaseClickResult = null;
    if (opts.clickRelease === "true" && hold.releaseVisible && !hold.releaseDisabled) {
      const respPromise = page.waitForResponse(
        (r) => r.url().includes(`/api/tasks/${opts.task}/human-review/release`) && r.request().method() === "POST",
      );
      await page.getByTestId("release-human-review").click();
      const resp = await respPromise;
      releaseClickResult = {
        status: resp.status(),
        body: await resp.text(),
      };
      await page.waitForTimeout(300);
    }

    console.log(
      JSON.stringify({
        hold,
        gateChips,
        hasTargetedReviewText: /Targeted review/i.test(bodyText || ""),
        releaseClickResult,
        pageErrors,
      }),
    );
  } finally {
    await context.close();
  }
}

async function inspectDeclarations(browser, opts) {
  const { context, page, pageErrors } = await openContext(browser, opts.url, opts.jwt, 1440, 900);
  try {
    await page.goto(`/next/processes/declarations?repo=${encodeURIComponent(opts.repo || "acme/widgets")}`, {
      waitUntil: "domcontentloaded",
    });
    await page.waitForTimeout(1200);
    const bodyText = (await page.locator("body").textContent()) || "";
    const acceptButtons = await page.locator("button").evaluateAll((els) =>
      els
        .map((e) => ({ text: e.textContent.trim(), disabled: e.disabled, testid: e.getAttribute("data-testid") }))
        .filter((e) => /accept/i.test(e.text)),
    );
    console.log(
      JSON.stringify({
        url: page.url(),
        hasRepoText: bodyText.includes(opts.repo || "acme/widgets"),
        hasCredentialText: /credential|repo#write|awaiting/i.test(bodyText),
        acceptButtons,
        pageErrors,
      }),
    );
  } finally {
    await context.close();
  }
}

async function captureShots(browser, opts) {
  const outDir = opts.out;
  await fs.mkdir(outDir, { recursive: true });
  const prefix = opts.prefix || "shot";
  const pagePath = opts.path;
  const waitSelector = opts.waitFor || "";
  const viewports = [
    { name: "desktop", width: 1440, height: 900, isMobile: false },
    { name: "phone", width: 390, height: 844, isMobile: true },
  ];
  const themes = ["light", "dark"];
  const results = [];
  for (const vp of viewports) {
    for (const theme of themes) {
      const context = await browser.newContext({
        baseURL: opts.url,
        viewport: { width: vp.width, height: vp.height },
        colorScheme: theme,
        isMobile: vp.isMobile,
        hasTouch: vp.isMobile,
        extraHTTPHeaders: { "X-Grain-Hub-Assertion": opts.jwt },
      });
      await context.addInitScript(
        ([k, v]) => localStorage.setItem(k, v),
        ["grain.themeMode", theme],
      );
      const page = await context.newPage();
      const pageErrors = [];
      page.on("pageerror", (e) => pageErrors.push(String(e)));
      try {
        await page.goto(pagePath, { waitUntil: "domcontentloaded" });
        if (waitSelector) {
          await page.locator(waitSelector).first().waitFor({ state: "visible", timeout: 15000 }).catch(() => {});
        }
        await page.waitForTimeout(1000);
        const overflow = await page.evaluate(() => ({
          scrollWidth: document.documentElement.scrollWidth,
          clientWidth: document.documentElement.clientWidth,
        }));
        const file = path.join(outDir, `${prefix}-${vp.name}-${theme}.png`);
        await page.screenshot({ path: file, fullPage: false });
        results.push({
          file,
          viewport: `${vp.width}x${vp.height}`,
          theme,
          horizontalOverflow: overflow.scrollWidth > overflow.clientWidth + 1,
          overflow,
          pageErrors,
        });
      } finally {
        await context.close();
      }
    }
  }
  console.log(JSON.stringify({ shots: results }));
}

async function buildContactSheet(browser, opts) {
  const dir = opts.dir;
  const entries = (await fs.readdir(dir))
    .filter((f) => f.endsWith(".png") && f !== "contact-sheet.png")
    .sort();
  const cells = await Promise.all(
    entries.map(async (name) => {
      const data = (await fs.readFile(path.join(dir, name))).toString("base64");
      return `<figure><img src="data:image/png;base64,${data}"><figcaption>${name}</figcaption></figure>`;
    }),
  );
  const html = `<!doctype html><style>
    body { margin: 0; padding: 12px; background: #888; font: 13px sans-serif;
      display: flex; flex-wrap: wrap; gap: 12px; align-items: flex-start; }
    figure { margin: 0; }
    img { display: block; max-width: 480px; max-height: 600px; border: 1px solid #333; }
    figcaption { padding-top: 4px; color: #111; font-weight: 600; }
  </style>${cells.join("")}`;
  const page = await browser.newPage({ viewport: { width: 2000, height: 800 } });
  const out = path.join(dir, "contact-sheet.png");
  await page.setContent(html);
  await page.screenshot({ path: out, fullPage: true });
  await page.close();
  console.log(JSON.stringify({ contactSheet: out, count: entries.length }));
}

async function main() {
  const { cmd, opts } = parseArgs(process.argv.slice(2));
  const browser = await chromium.launch({ headless: true });
  try {
    if (cmd === "inspect-next") {
      await inspectNext(browser, opts);
    } else if (cmd === "inspect-classic") {
      await inspectClassic(browser, opts);
    } else if (cmd === "inspect-declarations") {
      await inspectDeclarations(browser, opts);
    } else if (cmd === "capture") {
      await captureShots(browser, opts);
    } else if (cmd === "contact-sheet") {
      await buildContactSheet(browser, opts);
    } else {
      throw new Error(`unknown command: ${cmd}`);
    }
  } finally {
    await browser.close();
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
