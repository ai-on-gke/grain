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

import { spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import puppeteer from "puppeteer-core";
import {
  newAccessCode,
  signIn as fetchAccessCookies,
} from "../../ui-shared/e2e/accesscode.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "../..");

function parseArgs(argv) {
  let stage = "all";
  let outDir = process.env.PUPPETEER_OUT_DIR || "";
  let baseUrl = process.env.BASE_URL || "";
  for (const arg of argv) {
    if (arg.startsWith("--stage=")) stage = arg.slice("--stage=".length);
    if (arg.startsWith("--out=")) outDir = arg.slice("--out=".length);
    if (arg.startsWith("--base-url="))
      baseUrl = arg.slice("--base-url=".length);
  }
  if (!outDir) {
    outDir = fs.mkdtempSync(path.join(os.tmpdir(), "grain-puppeteer-wf-"));
  }
  return { stage, outDir, baseUrl };
}

function findChromeExecutable() {
  if (process.env.CHROME_PATH && fs.existsSync(process.env.CHROME_PATH)) {
    return process.env.CHROME_PATH;
  }
  const candidates = [
    "/usr/bin/google-chrome",
    "/usr/bin/google-chrome-stable",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
  ];
  for (const c of candidates) {
    if (fs.existsSync(c)) return c;
  }
  const pwCache = path.join(os.homedir(), ".cache", "ms-playwright");
  if (fs.existsSync(pwCache)) {
    for (const entry of fs.readdirSync(pwCache).sort().reverse()) {
      if (!entry.startsWith("chromium-")) continue;
      for (const sub of ["chrome-linux64/chrome", "chrome-linux/chrome"]) {
        const p = path.join(pwCache, entry, sub);
        if (fs.existsSync(p)) return p;
      }
    }
  }
  throw new Error("No Chrome/Chromium executable found; set CHROME_PATH.");
}

async function getFreePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      srv.close((err) => (err ? reject(err) : resolve(port)));
    });
    srv.on("error", reject);
  });
}

async function startDemoServer() {
  const grainBin = process.env.GRAIN_BIN || path.join(repoRoot, "bin", "grain");
  if (!fs.existsSync(grainBin)) {
    throw new Error(
      `Grain binary not found at ${grainBin}; run make grain first.`,
    );
  }
  const port = await getFreePort();
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "grain-demo-pup-"));
  const tokFile = path.join(tmpDir, "token");
  const code = newAccessCode();
  fs.writeFileSync(tokFile, `${code}\n`, { mode: 0o600 });
  const child = spawn(
    grainBin,
    [
      "demo",
      "-addr",
      `127.0.0.1:${port}`,
      "-open=false",
      "-access-code-file",
      tokFile,
    ],
    {
      cwd: tmpDir,
      env: {
        ...process.env,
        HOME: tmpDir,
        TMPDIR: tmpDir,
        GRAIN_DATA_DIR: "",
        GRAIN_SERVER: "",
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );

  const baseUrl = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + 25000;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${baseUrl}/api/config`);
      if (res.status === 200 || res.status === 401) {
        return {
          baseUrl,
          token: code,
          loginName: "alice@example.com",
          stop: () => {
            child.kill("SIGTERM");
            fs.rmSync(tmpDir, { recursive: true, force: true });
          },
        };
      }
    } catch {
      // Wait for listener.
    }
    await new Promise((r) => setTimeout(r, 150));
  }
  child.kill("SIGTERM");
  throw new Error("Timed out waiting for grain demo server to start.");
}

async function signIn(browser, baseUrl, token, loginName) {
  if (!token) return;
  const state = await fetchAccessCookies(baseUrl, token, loginName);
  await browser.setCookie(...state.cookies);
}

async function inspectPopup(page, selector) {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel);
    if (!el) return { exists: false };
    const cs = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    const bgMatch = cs.backgroundColor.match(
      /rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)(?:\s*,\s*([0-9.]+))?\s*\)/,
    );
    const alpha = bgMatch
      ? bgMatch[4] !== undefined
        ? Number(bgMatch[4])
        : 1
      : 0;
    const insp = el.closest(".qa-step-inspector");
    return {
      exists: true,
      backgroundColor: cs.backgroundColor,
      alpha,
      borderTopWidth: parseFloat(cs.borderTopWidth || "0"),
      boxShadow: cs.boxShadow,
      zIndex: cs.zIndex,
      rect: {
        left: Math.round(r.left),
        top: Math.round(r.top),
        right: Math.round(r.right),
        bottom: Math.round(r.bottom),
        width: Math.round(r.width),
        height: Math.round(r.height),
      },
      viewport: { width: window.innerWidth, height: window.innerHeight },
      inspectorOverflowX: insp ? insp.scrollWidth - insp.clientWidth : 0,
    };
  }, selector);
}

async function getStageSnapshot(page) {
  return page.evaluate(() => {
    const groups = Array.from(
      document.querySelectorAll('[data-testid^="qa-stage-s"]'),
    ).filter((el) =>
      /^qa-stage-(s0|s1|s2|s3|s4|s5)-/.test(
        el.getAttribute("data-testid") || "",
      ),
    );
    const byId = {};
    for (const g of groups) {
      const id = (g.getAttribute("data-testid") || "").replace("qa-stage-", "");
      const toggle = g.querySelector(`[data-testid="qa-stage-toggle-${id}"]`);
      const expanded = toggle?.getAttribute("aria-expanded") === "true";
      const rows = Array.from(g.querySelectorAll(".qa-step-node")).map(
        (btn) => ({
          testId: btn.getAttribute("data-testid") || "",
          stepId: (btn.getAttribute("data-testid") || "").replace(
            "qa-step-row-",
            "",
          ),
          label: btn.querySelector(".qa-step-label")?.textContent?.trim() || "",
          selected: btn.classList.contains("is-selected"),
        }),
      );
      const hasAddBtn = Boolean(
        g.querySelector('[data-testid="qa-add-step-btn"]'),
      );
      byId[id] = {
        expanded,
        count: rows.length,
        rows,
        hasAddBtn,
      };
    }
    return {
      visibleStageIds: Object.keys(byId),
      byId,
    };
  });
}

async function ensureStageExpanded(page, stageId) {
  const toggleSel = `[data-testid="qa-stage-toggle-${stageId}"]`;
  await page.waitForSelector(toggleSel, { timeout: 5000 });
  const expanded = await page.$eval(
    toggleSel,
    (el) => el.getAttribute("aria-expanded") === "true",
  );
  if (!expanded) {
    await page.click(toggleSel);
    await new Promise((r) => setTimeout(r, 150));
  }
}

async function run() {
  const {
    stage,
    outDir,
    baseUrl: externalUrl,
  } = parseArgs(process.argv.slice(2));
  fs.mkdirSync(outDir, { recursive: true });
  const runS1 = stage === "all" || stage === "1";
  const runS2 = stage === "all" || stage === "2";
  const runS3 = stage === "all" || stage === "3";

  const demo = externalUrl
    ? {
        baseUrl: externalUrl,
        token: process.env.TOKEN_FILE
          ? fs.readFileSync(process.env.TOKEN_FILE, "utf8").trim()
          : "",
        loginName: process.env.LOGIN_NAME || "operator@example.com",
        stop: () => {},
      }
    : await startDemoServer();

  const browser = await puppeteer.launch({
    executablePath: findChromeExecutable(),
    headless: true,
    args: ["--no-sandbox", "--disable-setuid-sandbox"],
  });

  const assertions = [];
  const record = (name, ok, detail) => {
    assertions.push({ name, ok: Boolean(ok), detail });
  };

  let shotIdx = 1;
  const snap = async (page, slug) => {
    const file = `${String(shotIdx++).padStart(2, "0")}-${slug}.png`;
    await page.screenshot({ path: path.join(outDir, file) });
    return file;
  };

  try {
    await signIn(browser, demo.baseUrl, demo.token, demo.loginName);
    const page = await browser.newPage();
    await page.setViewport({ width: 1440, height: 900 });

    // =========================================================================
    // STAGE 1: Dropdown rendering, Guide reference dialog, & ? inline help
    // =========================================================================
    if (runS1) {
      await page.goto(
        `${demo.baseUrl}/next/processes/quality?flow=engineering-workflow-v4@1`,
        { waitUntil: "domcontentloaded" },
      );
      await page.waitForSelector('[data-testid="qa-studio-toolbar"]');
      await new Promise((r) => setTimeout(r, 200));

      // 1A. More actions ("...") overflow dropdown must be opaque, bordered, elevated, and unclipped
      await page.click('[data-testid="qa-more-actions-btn"]');
      await new Promise((r) => setTimeout(r, 150));
      await snap(page, "s1-more-menu-light");
      const morePop = await inspectPopup(
        page,
        '.qa-action-pop[data-qa-menu="more"]',
      );
      record(
        "s1.more_menu.opaque_and_elevated",
        morePop.exists &&
          morePop.alpha === 1 &&
          morePop.borderTopWidth >= 1 &&
          morePop.boxShadow !== "none" &&
          morePop.rect.left >= 0 &&
          morePop.rect.right <= morePop.viewport.width &&
          morePop.rect.bottom <= morePop.viewport.height,
        morePop,
      );
      await page.keyboard.press("Escape");
      await new Promise((r) => setTimeout(r, 100));

      // 1B. Toolbar "Guide" button must open a visible Guide dialog on arrival and close cleanly
      await page.click('[data-testid="qa-open-guide-btn"]');
      await new Promise((r) => setTimeout(r, 200));
      await snap(page, "s1-guide-dialog");
      const guideDialog = await inspectPopup(
        page,
        '[data-testid="qa-guide-dialog"]',
      );
      record(
        "s1.guide_button.opens_dialog_on_arrival",
        guideDialog.exists &&
          guideDialog.alpha === 1 &&
          guideDialog.rect.width >= 320 &&
          guideDialog.rect.height >= 200 &&
          guideDialog.rect.right <= guideDialog.viewport.width,
        guideDialog,
      );
      await page.keyboard.press("Escape");
      await new Promise((r) => setTimeout(r, 150));
      const guideClosed = await page.$('[data-testid="qa-guide-dialog"]');
      record("s1.guide_button.closes_on_escape", !guideClosed, {
        closed: !guideClosed,
      });

      // 1C. Contextual "?" button in right drawer must toggle visible inline help on click
      const helpBtn = await page.$(
        '[data-testid="proc-inspector"] .qa-help-btn',
      );
      if (helpBtn) {
        await helpBtn.click();
        await new Promise((r) => setTimeout(r, 150));
      }
      await snap(page, "s1-inline-help-open");
      const inlineHelpVisible = await page.evaluate(() => {
        const el = document.querySelector('[data-testid="qa-inline-help"]');
        if (!el) return false;
        const r = el.getBoundingClientRect();
        return r.width > 40 && r.height > 20;
      });
      record("s1.help_question_btn.toggles_inline_help", inlineHelpVisible, {
        inlineHelpVisible,
      });

      // 1C-bis (isolated demo only): Set workflow as default -> verify "Turn off default…" -> turn off default -> verify no workflow active
      if (!externalUrl) {
        await page.click('[data-testid="qa-set-default-btn"]');
        await new Promise((r) => setTimeout(r, 150));
        await page.click('[data-testid="confirm-accept"]');
        await new Promise((r) => setTimeout(r, 250));
        await snap(page, "s1-default-active-with-turn-off");
        const activeState = await page.evaluate(() => ({
          pill:
            document
              .querySelector('[data-testid="qa-rollout-status-pill"]')
              ?.textContent?.trim() || "",
          hasClearBtn: Boolean(
            document.querySelector('[data-testid="qa-clear-default-btn"]'),
          ),
        }));
        if (activeState.hasClearBtn) {
          await page.click('[data-testid="qa-clear-default-btn"]');
          await new Promise((r) => setTimeout(r, 150));
          await page.click('[data-testid="confirm-accept"]');
          await new Promise((r) => setTimeout(r, 250));
        }
        await snap(page, "s1-default-turned-off");
        await page.evaluate(() => {
          const closeBtns = Array.from(
            document.querySelectorAll("button[aria-label], .snackbar button"),
          );
          const dismiss = closeBtns.find(
            (b) =>
              b.textContent?.trim() === "×" ||
              /dismiss/i.test(b.getAttribute("aria-label") || ""),
          );
          if (dismiss instanceof HTMLElement) dismiss.click();
        });
        await new Promise((r) => setTimeout(r, 100));
        const clearedState = await page.evaluate(() => {
          const catalogPills = Array.from(
            document.querySelectorAll(".qa-flow-family-card .status-pill"),
          ).map((el) => el.textContent?.trim() || "");
          return {
            pill:
              document
                .querySelector('[data-testid="qa-rollout-status-pill"]')
                ?.textContent?.trim() || "",
            hasSetBtn: Boolean(
              document.querySelector('[data-testid="qa-set-default-btn"]'),
            ),
            hasClearBtn: Boolean(
              document.querySelector('[data-testid="qa-clear-default-btn"]'),
            ),
            provenanceTitle:
              document
                .querySelector(
                  '[data-testid="qa-flow-detail-card"] .qa-drawer-title',
                )
                ?.textContent?.trim() || "",
            allCatalogAvailable: catalogPills.every((t) => t === "Available"),
          };
        });
        record(
          "s1.set_and_clear_default_workflow",
          activeState.pill.includes("Active") &&
            activeState.hasClearBtn &&
            clearedState.pill === "Available" &&
            clearedState.hasSetBtn &&
            !clearedState.hasClearBtn &&
            clearedState.provenanceTitle === "No default workflow active" &&
            clearedState.allCatalogAvailable,
          { activeState, clearedState },
        );
      }

      // 1D. Enter Customize mode -> check "Test on recent PRs" menu + right-drawer <g-pill-select> bounds
      await page.click('[data-testid="qa-new-version-btn"]');
      await new Promise((r) => setTimeout(r, 200));
      await page.click('[data-testid="qa-test-menu-btn"]');
      await new Promise((r) => setTimeout(r, 150));
      await snap(page, "s1-test-menu-draft");
      const testPop = await inspectPopup(
        page,
        '.qa-action-pop[data-qa-menu="test"]',
      );
      record(
        "s1.test_menu.opaque_and_unclipped",
        testPop.exists &&
          testPop.alpha === 1 &&
          testPop.borderTopWidth >= 1 &&
          testPop.boxShadow !== "none" &&
          testPop.rect.left >= 0 &&
          testPop.rect.right <= testPop.viewport.width,
        testPop,
      );
      await page.click('[data-testid="qa-test-menu-btn"]');
      await new Promise((r) => setTimeout(r, 100));

      // Open Stage 5 -> click a built-in non-floor check -> open right-column "Enforcement" pill-select
      await ensureStageExpanded(page, "s3-checks");
      await page.click(
        '[data-testid="qa-step-row-golden:sibling-export-hazard"]',
      );
      await new Promise((r) => setTimeout(r, 150));
      await page.click('[data-testid="qa-builtin-check-severity"]');
      await new Promise((r) => setTimeout(r, 150));
      await snap(page, "s1-drawer-pill-select");
      const sevMenu = await inspectPopup(
        page,
        '[data-testid="qa-builtin-check-severity-menu"]',
      );
      record(
        "s1.drawer_pill_select.fits_without_overflow",
        sevMenu.exists &&
          sevMenu.alpha === 1 &&
          sevMenu.rect.left >= 0 &&
          sevMenu.rect.right <= sevMenu.viewport.width &&
          sevMenu.inspectorOverflowX === 0,
        sevMenu,
      );
      await page.keyboard.press("Escape");
      await new Promise((r) => setTimeout(r, 100));

      // Open Stage 8 -> click s5:merge-triage -> open Merge triage mode select near bottom
      await ensureStageExpanded(page, "s5-triage");
      await page.click('[data-testid="qa-step-row-s5:merge-triage"]');
      await new Promise((r) => setTimeout(r, 150));
      await page.click('[data-testid="qa-s5-triage-select"]');
      await new Promise((r) => setTimeout(r, 150));
      await snap(page, "s1-s5-triage-pill-select");
      const s5Menu = await inspectPopup(
        page,
        '[data-testid="qa-s5-triage-select-menu"]',
      );
      record(
        "s1.bottom_pill_select.unclipped_in_viewport",
        s5Menu.exists &&
          s5Menu.alpha === 1 &&
          s5Menu.rect.top >= 0 &&
          s5Menu.rect.bottom <= s5Menu.viewport.height &&
          s5Menu.inspectorOverflowX === 0,
        s5Menu,
      );
      await page.keyboard.press("Escape");
    }

    // =========================================================================
    // STAGE 2: Partial-workflow draft entry & Stage-accurate substep creation
    // =========================================================================
    if (runS2) {
      // Reload clean read-only state on a PARTIAL workflow (golden-v1-minimal: 4 active stages)
      const p2 = await browser.newPage();
      await p2.setViewport({ width: 1440, height: 900 });
      await p2.goto(
        `${demo.baseUrl}/next/processes/quality?flow=golden-v1-minimal`,
        { waitUntil: "domcontentloaded" },
      );
      await p2.waitForSelector('[data-testid="qa-studio-toolbar"]');
      await new Promise((r) => setTimeout(r, 200));

      const beforePartial = await getStageSnapshot(p2);
      await ensureStageExpanded(p2, "s2-coding");
      await snap(p2, "s2-partial-before-add");
      await p2.click(
        '[data-testid="qa-add-step-btn"][data-stage-id="s2-coding"]',
      );
      await new Promise((r) => setTimeout(r, 200));
      await snap(p2, "s2-partial-after-first-add-click");

      const afterFirstAdd = await p2.evaluate(() => {
        const catalog = document.querySelector(
          '[data-testid="qa-add-step-catalog"]',
        );
        const eyebrow = catalog
          ?.querySelector(".qa-drawer-eyebrow")
          ?.textContent?.trim();
        const draftPill = Boolean(
          document.querySelector('[data-testid="qa-draft-pill"]'),
        );
        return {
          catalogOpen: Boolean(catalog),
          eyebrow,
          draftPill,
        };
      });
      const afterPartialStages = await getStageSnapshot(p2);
      record(
        "s2.partial_readonly_add.opens_catalog_on_first_click",
        afterFirstAdd.catalogOpen &&
          afterFirstAdd.draftPill &&
          (afterFirstAdd.eyebrow || "").includes("Stage 4"),
        afterFirstAdd,
      );
      record(
        "s2.partial_readonly_add.does_not_spawn_extra_stages",
        JSON.stringify(beforePartial.visibleStageIds) ===
          JSON.stringify(afterPartialStages.visibleStageIds),
        {
          before: beforePartial.visibleStageIds,
          after: afterPartialStages.visibleStageIds,
        },
      );
      await p2.close();

      // Full 8-stage workflow: test adding every offered substep type across Stages 1..8
      const pFull = await browser.newPage();
      await pFull.setViewport({ width: 1440, height: 900 });
      await pFull.goto(
        `${demo.baseUrl}/next/processes/quality?flow=engineering-workflow-v4@1`,
        { waitUntil: "domcontentloaded" },
      );
      await pFull.waitForSelector('[data-testid="qa-studio-toolbar"]');
      await new Promise((r) => setTimeout(r, 200));
      await pFull.click('[data-testid="qa-new-version-btn"]');
      await new Promise((r) => setTimeout(r, 200));

      // Stages 2 & 8 must NOT offer misleading + Add substep buttons that redirect elsewhere
      for (const fixedStage of ["s1-context", "s5-triage"]) {
        await ensureStageExpanded(pFull, fixedStage);
      }
      const fixedSnap = await getStageSnapshot(pFull);
      record(
        "s2.fixed_stages_2_and_8.no_misleading_add_button",
        !fixedSnap.byId["s1-context"]?.hasAddBtn &&
          !fixedSnap.byId["s5-triage"]?.hasAddBtn,
        {
          s1HasAdd: fixedSnap.byId["s1-context"]?.hasAddBtn,
          s5HasAdd: fixedSnap.byId["s5-triage"]?.hasAddBtn,
        },
      );

      // Matrix of (stageId, buttonTestId, label) that MUST land in that exact stageId
      const placementMatrix = [
        {
          stageId: "s0-decompose",
          cardTestId: "qa-v3-add-skill",
          name: "stage1-skill",
        },
        {
          stageId: "s2-plan",
          cardTestId: "qa-v3-add-skill",
          name: "stage3-skill",
        },
        {
          stageId: "s2-plan",
          cardTestId: "qa-v3-add-stage",
          name: "stage3-critic",
        },
        {
          stageId: "s2-coding",
          cardTestId: "qa-v3-add-skill",
          name: "stage4-skill",
        },
        {
          stageId: "s3-checks",
          cardTestId: "qa-v3-add-pattern",
          name: "stage5-pattern",
        },
        {
          stageId: "s3-checks",
          cardTestId: "qa-v3-add-companion",
          name: "stage5-companion",
        },
        {
          stageId: "s3-checks",
          cardTestId: "qa-v3-add-diff-budget",
          name: "stage5-budget",
        },
        {
          stageId: "s3-checks",
          cardTestId: "qa-v3-add-jsonschema",
          name: "stage5-schema",
        },
        {
          stageId: "s3-checks",
          cardTestId: "qa-v3-add-stage",
          name: "stage5-critic",
        },
        {
          stageId: "s3-pr",
          cardTestId: "qa-v3-add-skill",
          name: "stage6-skill",
        },
        {
          stageId: "s4-verify",
          cardTestId: "qa-v3-add-skill",
          name: "stage7-skill",
        },
        {
          stageId: "s4-verify",
          cardTestId: "qa-v3-add-stage",
          name: "stage7-critic",
        },
      ];

      // Also verify Stage 1 does NOT offer deterministic pattern check card
      await ensureStageExpanded(pFull, "s0-decompose");
      await pFull.click(
        '[data-testid="qa-add-step-btn"][data-stage-id="s0-decompose"]',
      );
      await new Promise((r) => setTimeout(r, 150));
      const stage1HasPatternCard = Boolean(
        await pFull.$('[data-testid="qa-v3-add-pattern"]'),
      );
      const stage5HasSkillCard = await (async () => {
        await ensureStageExpanded(pFull, "s3-checks");
        await pFull.click(
          '[data-testid="qa-add-step-btn"][data-stage-id="s3-checks"]',
        );
        await new Promise((r) => setTimeout(r, 150));
        return Boolean(await pFull.$('[data-testid="qa-v3-add-skill"]'));
      })();
      record(
        "s2.catalog_scoped_to_valid_stage_types",
        !stage1HasPatternCard && !stage5HasSkillCard,
        { stage1HasPatternCard, stage5HasSkillCard },
      );

      for (const item of placementMatrix) {
        await ensureStageExpanded(pFull, item.stageId);
        const pre = await getStageSnapshot(pFull);
        await pFull.click(
          `[data-testid="qa-add-step-btn"][data-stage-id="${item.stageId}"]`,
        );
        await new Promise((r) => setTimeout(r, 120));
        const card = await pFull.$(`[data-testid="${item.cardTestId}"]`);
        if (!card) {
          record(`s2.place.${item.name}`, false, {
            reason: `Card ${item.cardTestId} missing for ${item.stageId}`,
          });
          continue;
        }
        await card.click();
        await new Promise((r) => setTimeout(r, 150));
        await snap(pFull, `s2-added-${item.name}`);
        const post = await getStageSnapshot(pFull);

        const targetDelta =
          (post.byId[item.stageId]?.count ?? 0) -
          (pre.byId[item.stageId]?.count ?? 0);
        const otherDeltas = Object.keys(post.byId)
          .filter((k) => k !== item.stageId)
          .map((k) => ({
            stage: k,
            delta: (post.byId[k]?.count ?? 0) - (pre.byId[k]?.count ?? 0),
          }))
          .filter((d) => d.delta !== 0);
        const selectedInsideTarget = Boolean(
          post.byId[item.stageId]?.rows.some((r) => r.selected),
        );

        record(
          `s2.place.${item.name}`,
          targetDelta === 1 &&
            otherDeltas.length === 0 &&
            selectedInsideTarget &&
            post.byId[item.stageId]?.expanded === true,
          { targetDelta, otherDeltas, selectedInsideTarget },
        );
      }

      // =======================================================================
      // STAGE 3: Inline & drawer-header substep deletion, UID stability, Publish
      // =======================================================================
      if (runS3) {
        // 3A. Delete a custom substep directly via its inline stage-row delete button
        await ensureStageExpanded(pFull, "s3-checks");
        const preRowDel = await getStageSnapshot(pFull);
        const rowDelBtn = await pFull.$(
          '[data-testid="qa-stage-s3-checks"] [data-testid^="qa-row-delete-step-"]',
        );
        if (!rowDelBtn) {
          record("s3.inline_row_delete", false, {
            reason: "No inline row delete button found in Stage 5",
          });
        } else {
          await rowDelBtn.click();
          await new Promise((r) => setTimeout(r, 180));
          await snap(pFull, "s3-after-inline-row-delete");
          const postRowDel = await getStageSnapshot(pFull);
          const delta =
            (postRowDel.byId["s3-checks"]?.count ?? 0) -
            (preRowDel.byId["s3-checks"]?.count ?? 0);
          record("s3.inline_row_delete", delta === -1, { delta });

          // 3B. Click Undo in snackbar -> restores the deleted substep in Stage 5!
          const undoClicked = await pFull.evaluate(() => {
            const btns = Array.from(document.querySelectorAll("button"));
            const undo = btns.find((b) => b.textContent?.trim() === "Undo");
            if (!undo) return false;
            undo.click();
            return true;
          });
          await new Promise((r) => setTimeout(r, 180));
          const postUndo = await getStageSnapshot(pFull);
          record(
            "s3.undo_restores_deleted_substep",
            undoClicked &&
              postUndo.byId["s3-checks"]?.count ===
                preRowDel.byId["s3-checks"]?.count,
            {
              undoClicked,
              expected: preRowDel.byId["s3-checks"]?.count,
              actual: postUndo.byId["s3-checks"]?.count,
            },
          );
        }

        // 3C. Select a custom check in Stage 5, clear/rename Rule identifier, verify drawer Delete stays visible & deletes cleanly
        const customCheckRow = await pFull.$(
          '[data-testid="qa-stage-s3-checks"] [data-testid^="qa-step-row-custom:check-"]',
        );
        if (!customCheckRow) {
          record("s3.header_delete_after_id_rename", false, {
            reason: "No custom:check-* row found in Stage 5",
          });
        } else {
          await customCheckRow.click();
          await new Promise((r) => setTimeout(r, 150));
          const idInput = await pFull.$('[data-testid="qa-check-id-input"]');
          if (idInput) {
            await idInput.click({ clickCount: 3 });
            await pFull.keyboard.press("Backspace");
            await new Promise((r) => setTimeout(r, 120));
            const stillHasDeleteWhenEmpty = Boolean(
              await pFull.$('[data-testid="qa-header-delete-step-btn"]'),
            );
            await idInput.type("custom:renamed-in-drawer");
            await new Promise((r) => setTimeout(r, 120));
            const preHdrDel = await getStageSnapshot(pFull);
            const hdrDelBtn = await pFull.$(
              '[data-testid="qa-header-delete-step-btn"]',
            );
            if (hdrDelBtn) {
              await hdrDelBtn.click();
              await new Promise((r) => setTimeout(r, 180));
            }
            await snap(pFull, "s3-after-header-delete");
            const postHdrDel = await getStageSnapshot(pFull);
            const deltaHdr =
              (postHdrDel.byId["s3-checks"]?.count ?? 0) -
              (preHdrDel.byId["s3-checks"]?.count ?? 0);
            record(
              "s3.header_delete_after_id_rename",
              stillHasDeleteWhenEmpty && Boolean(hdrDelBtn) && deltaHdr === -1,
              { stillHasDeleteWhenEmpty, deltaHdr },
            );
          } else {
            record("s3.header_delete_after_id_rename", false, {
              reason: "qa-check-id-input not rendered",
            });
          }
        }

        // 3D. Publish new version & reload -> verify stage counts persist across reload!
        const prePublish = await getStageSnapshot(pFull);
        await pFull.click('[data-testid="qa-publish-version-btn"]');
        await new Promise((r) => setTimeout(r, 200));
        const confirmBtn = await pFull.evaluate(() => {
          const dlg = document.querySelector("g-confirm-dialog");
          const btn = dlg?.querySelector(".btn-primary");
          if (!btn) return false;
          btn.click();
          return true;
        });
        await new Promise((r) => setTimeout(r, 600));
        await pFull.reload({ waitUntil: "domcontentloaded" });
        await pFull.waitForSelector('[data-testid="qa-studio-toolbar"]');
        await new Promise((r) => setTimeout(r, 250));
        for (const stId of Object.keys(prePublish.byId)) {
          await ensureStageExpanded(pFull, stId);
        }
        await snap(pFull, "s3-after-publish-reload");
        const postReload = await getStageSnapshot(pFull);
        const countMismatches = Object.keys(prePublish.byId)
          .filter(
            (k) => prePublish.byId[k]?.count !== postReload.byId[k]?.count,
          )
          .map((k) => ({
            stage: k,
            beforePublish: prePublish.byId[k]?.count,
            afterReload: postReload.byId[k]?.count,
          }));
        record(
          "s3.publish_and_reload_persists_exact_stages",
          confirmBtn && countMismatches.length === 0,
          { confirmBtn, countMismatches },
        );
      }

      await pFull.close();
    }
  } finally {
    await browser.close();
    demo.stop();
  }

  const failed = assertions.filter((a) => !a.ok);
  const summary = {
    stage,
    outDir,
    total: assertions.length,
    passed: assertions.length - failed.length,
    failed: failed.length,
    assertions,
  };
  fs.writeFileSync(
    path.join(outDir, "puppeteer-summary.json"),
    JSON.stringify(summary, null, 2) + "\n",
  );
  console.log(JSON.stringify(summary, null, 2));
  if (failed.length > 0) {
    process.exitCode = 1;
  }
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
