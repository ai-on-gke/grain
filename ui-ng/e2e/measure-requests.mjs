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

// Measures every HTTP request `/next/` (`ui-ng/`) and classic `/` (`ui/`)
// make per main page against a `grain demo` seeded with realistic scale
// (100+ tasks, 12 running agents, open notifications, and a live session
// with a streaming transcript), both over a 5-minute steady-state idle
// window and a 1-minute scripted normal-use window.
//
// Usage:
//   PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ui-ng/e2e/measure-requests.mjs \
//       --idle-seconds 300 --active-seconds 60 \
//       --out docs/test-runs/2026-11-16-ui-ng-request-budget.md \
//       --json /tmp/request-baseline.json

import { chromium } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import fsp from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { newAccessCode, signIn } from "../../ui-shared/e2e/accesscode.js";
import {
  REALISTIC_DEMO_ARGS,
  attachRequestRecorder,
  loadSourceMapResolver,
  seedRealisticDemo,
  summarizeRequests,
} from "./request-harness.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, "../..");

function parseArgs(argv) {
  const opts = {
    idleSeconds: 300,
    activeSeconds: 60,
    out: "",
    json: "",
    bin: "",
    sourcemaps: true,
    sourcemapsCache: "",
    pagesFilter: null,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--idle-seconds") opts.idleSeconds = Number(argv[++i]);
    else if (a === "--active-seconds") opts.activeSeconds = Number(argv[++i]);
    else if (a === "--out") opts.out = argv[++i];
    else if (a === "--json") opts.json = argv[++i];
    else if (a === "--bin") opts.bin = argv[++i];
    else if (a === "--no-sourcemaps") opts.sourcemaps = false;
    else if (a === "--sourcemaps-cache") opts.sourcemapsCache = argv[++i];
    else if (a === "--pages") {
      opts.pagesFilter = new Set(
        argv[++i]
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
      );
    }
  }
  return opts;
}

async function deleteMapFiles(dir) {
  if (!fs.existsSync(dir)) return;
  const entries = await fsp.readdir(dir, { withFileTypes: true });
  for (const ent of entries) {
    const full = path.join(dir, ent.name);
    if (ent.isDirectory()) await deleteMapFiles(full);
    else if (ent.name.endsWith(".map")) await fsp.unlink(full);
  }
}

async function copyMapFiles(srcDir, dstDir) {
  if (!fs.existsSync(srcDir)) return;
  await fsp.mkdir(dstDir, { recursive: true });
  const entries = await fsp.readdir(srcDir, { withFileTypes: true });
  for (const ent of entries) {
    const srcFull = path.join(srcDir, ent.name);
    const dstFull = path.join(dstDir, ent.name);
    if (ent.isDirectory()) await copyMapFiles(srcFull, dstFull);
    else if (ent.name.endsWith(".js.map")) await fsp.copyFile(srcFull, dstFull);
  }
}

async function prepareBinaryAndSourceMaps(opts, scratchDir) {
  const staticClassic = path.join(REPO_ROOT, "pkg/ui/static");
  const staticNext = path.join(REPO_ROOT, "pkg/ui/static-next");
  const cacheDir = opts.sourcemapsCache
    ? path.resolve(opts.sourcemapsCache)
    : "";
  const cacheClassic = cacheDir ? path.join(cacheDir, "classic") : "";
  const cacheNext = cacheDir ? path.join(cacheDir, "next") : "";
  const cacheReady =
    cacheDir && fs.existsSync(cacheClassic) && fs.existsSync(cacheNext);

  if (opts.sourcemaps && !cacheReady) {
    console.error(
      "[measure] Building ui and ui-ng with source maps for initiator attribution...",
    );
    execFileSync("npx", ["vite", "build", "--sourcemap", "hidden"], {
      cwd: path.join(REPO_ROOT, "ui"),
      stdio: "inherit",
    });
    execFileSync(
      "npx",
      ["ng", "build", "--configuration", "production", "--source-map"],
      {
        cwd: path.join(REPO_ROOT, "ui-ng"),
        stdio: "inherit",
      },
    );
    if (cacheDir) {
      await fsp.rm(cacheDir, { recursive: true, force: true }).catch(() => {});
      await copyMapFiles(staticClassic, cacheClassic);
      await copyMapFiles(staticNext, cacheNext);
    }
  }

  const resolveInitiator = await loadSourceMapResolver(
    cacheReady
      ? [
          { urlPrefix: "/", dir: cacheClassic },
          { urlPrefix: "/next/", dir: cacheNext },
        ]
      : [
          { urlPrefix: "/", dir: staticClassic },
          { urlPrefix: "/next/", dir: staticNext },
        ],
  );

  if (opts.sourcemaps && !cacheReady) {
    await deleteMapFiles(staticClassic);
    await deleteMapFiles(staticNext);
  }

  let binPath = opts.bin ? path.resolve(opts.bin) : "";
  if (!binPath) {
    binPath = path.join(scratchDir, "grain");
    console.error("[measure] Building grain binary embedding static assets...");
    execFileSync("go", ["build", "-o", binPath, "./cmd/grain"], {
      cwd: REPO_ROOT,
      stdio: "inherit",
    });
  }
  return { binPath, resolveInitiator };
}

async function startSeededDemo(binPath, parentScratch) {
  const tmpDir = await fsp.mkdtemp(path.join(parentScratch, "demo-"));
  const code = newAccessCode();
  const codeFile = path.join(tmpDir, "local-api-token");
  await fsp.writeFile(codeFile, `${code}\n`, { mode: 0o600 });
  const proc = spawn(
    binPath,
    [
      "demo",
      "-addr",
      "127.0.0.1:0",
      "-open=false",
      ...REALISTIC_DEMO_ARGS,
      "-access-code-file",
      codeFile,
    ],
    {
      cwd: REPO_ROOT,
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, TMPDIR: tmpDir },
    },
  );

  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`grain demo start timeout:\n${output}`)),
      60_000,
    );
    const onChunk = (chunk) => {
      output += chunk;
      const m = output.match(
        /at http:\/\/(127\.0\.0\.1|0\.0\.0\.0|\[::\]):(\d+)/,
      );
      if (m) {
        clearTimeout(timer);
        resolve(`http://127.0.0.1:${m[2]}`);
      }
    };
    proc.stdout.on("data", onChunk);
    proc.stderr.on("data", onChunk);
    proc.on("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
    proc.on("exit", (exitCode, sig) => {
      clearTimeout(timer);
      reject(new Error(`grain demo exited (${exitCode}, ${sig}):\n${output}`));
    });
  });

  const seed = await seedRealisticDemo(url, code, tmpDir);
  return {
    url,
    code,
    seed,
    stop: async () => {
      if (proc.exitCode === null && proc.signalCode === null) {
        await new Promise((resolve) => {
          proc.once("exit", resolve);
          proc.kill("SIGTERM");
          setTimeout(() => {
            try {
              proc.kill("SIGKILL");
            } catch {
              // Ignore.
            }
          }, 5_000).unref();
        });
      }
      await fsp.rm(tmpDir, { recursive: true, force: true }).catch(() => {});
    },
  };
}

async function safeClick(locator) {
  try {
    if ((await locator.count()) > 0) {
      const first = locator.first();
      if (await first.isVisible({ timeout: 800 })) {
        await first.click({ timeout: 2_000 });
        return true;
      }
    }
  } catch {
    // Ignore transient layout/detach during scripted browsing.
  }
  return false;
}

async function safeFill(locator, value) {
  try {
    if ((await locator.count()) > 0) {
      const first = locator.first();
      if (await first.isVisible({ timeout: 800 })) {
        await first.fill(value, { timeout: 2_000 });
        return true;
      }
    }
  } catch {
    // Ignore.
  }
  return false;
}

export function buildPageSpecs(seed) {
  const runId = seed.primaryRunningTaskId;
  const wsId = seed.workstreamRootId;
  const secondSess = seed.secondSessionId;

  return [
    {
      id: "inbox",
      label: "Inbox",
      nextPath: "/next/inbox",
      classicPath: "/inbox",
      drive: async (page, uiKind, stepsWait) => {
        if (uiKind === "next") {
          const rows = page.locator(".queue-row, [data-testid='inbox-row']");
          const count = await rows.count();
          for (let i = 1; i < Math.min(4, count); i++) {
            await safeClick(rows.nth(i));
            await stepsWait();
          }
          await safeFill(
            page
              .getByRole("searchbox")
              .or(page.locator("input[type='search']")),
            "shard",
          );
          await stepsWait();
          await safeFill(
            page
              .getByRole("searchbox")
              .or(page.locator("input[type='search']")),
            "",
          );
          await stepsWait();
          await safeClick(page.getByTestId("director-trigger"));
          await stepsWait();
          await page.keyboard.press("Escape").catch(() => {});
        } else {
          const items = page.locator(".inbox-item, .inbox-row, button");
          await safeClick(items.nth(2));
          await stepsWait();
          await safeClick(items.nth(3));
          await stepsWait();
        }
      },
    },
    {
      id: "tasks",
      label: "Tasks",
      nextPath: "/next/tasks",
      classicPath: "/tasks",
      drive: async (page, uiKind, stepsWait) => {
        if (uiKind === "next") {
          for (const tabName of ["Running", "Closed", "Backlog"]) {
            await safeClick(
              page.getByRole("tab", { name: new RegExp(tabName, "i") }),
            );
            await stepsWait();
          }
          const rows = page.locator("[data-task-id], .task-row");
          const n = await rows.count();
          for (let i = 1; i < Math.min(3, n); i++) {
            await safeClick(rows.nth(i));
            await stepsWait();
          }
          await safeFill(
            page
              .getByRole("searchbox")
              .or(page.locator("input[type='search']")),
            "Backlog task 1",
          );
          await stepsWait();
          await safeFill(
            page
              .getByRole("searchbox")
              .or(page.locator("input[type='search']")),
            "",
          );
        } else {
          await safeFill(
            page.locator("input[type='search'], input[placeholder*='Filter']"),
            "Backlog",
          );
          await stepsWait();
          await safeFill(
            page.locator("input[type='search'], input[placeholder*='Filter']"),
            "",
          );
          await stepsWait();
        }
      },
    },
    {
      id: "agents",
      label: "Agents",
      nextPath: "/next/agents",
      classicPath: "/tasks?state=running",
      drive: async (page, uiKind, stepsWait) => {
        if (uiKind === "next") {
          const cards = page.locator(".agent-card");
          await safeClick(cards.nth(0));
          await stepsWait();
          await safeClick(page.getByRole("tab", { name: /Runs/i }));
          await stepsWait();
          await safeClick(cards.nth(1));
          await stepsWait();
          await page.keyboard.press("Escape").catch(() => {});
        } else {
          await stepsWait();
        }
      },
    },
    {
      id: "workstreams",
      label: "Workstreams",
      nextPath: "/next/workstreams",
      classicPath: "/tasks",
      drive: async (page, uiKind, stepsWait) => {
        if (uiKind === "next") {
          await safeClick(
            page.locator(
              `a[href*='workstreams/${wsId}'], .ws-row, [data-stream-key]`,
            ),
          );
          await stepsWait();
          const nodes = page.locator("[data-node-id], .ws-tree-row");
          await safeClick(nodes.nth(1));
          await stepsWait();
          await safeClick(nodes.nth(2));
          await stepsWait();
          await safeClick(page.locator("nav a[href*='/workstreams']"));
        } else {
          await stepsWait();
        }
      },
    },
    {
      id: "task-detail",
      label: "Task detail (live run + transcript)",
      nextPath: `/next/task/${runId}?tab=runs`,
      classicPath: `/tasks/${runId}`,
      afterSettle: async (page, uiKind) => {
        if (uiKind === "classic") {
          await safeClick(
            page.locator(
              "button[title*='transcript'], button:has-text('Transcript')",
            ),
          );
        }
      },
      drive: async (page, uiKind, stepsWait) => {
        if (uiKind === "next") {
          for (const tabName of ["Conversation", "Changes", "Tree", "Runs"]) {
            await safeClick(
              page.getByRole("tab", { name: new RegExp(tabName, "i") }),
            );
            await stepsWait();
          }
        } else {
          await stepsWait();
        }
      },
    },
    {
      id: "sessions",
      label: "Sessions (live session + transcript)",
      nextPath: "/next/sessions/director",
      classicPath: "/sessions/director",
      afterSettle: async (page, uiKind) => {
        // Open Director drawer on /next/sessions so live transcript watch is active,
        // or keep viewing the live session.
        if (uiKind === "next") {
          await safeClick(page.getByTestId("director-trigger"));
        }
      },
      drive: async (page, uiKind, stepsWait) => {
        if (uiKind === "next" && secondSess) {
          await safeClick(
            page.locator(`a[href*='${encodeURIComponent(secondSess)}']`),
          );
          await stepsWait();
          await safeClick(page.locator("a[href*='/sessions/director']"));
          await stepsWait();
          await safeFill(
            page.getByTestId("composer"),
            "Drafting next milestone check",
          );
          await stepsWait();
          await safeFill(page.getByTestId("composer"), "");
        } else {
          await stepsWait();
        }
      },
    },
    {
      id: "processes",
      label: "Processes",
      nextPath: "/next/processes",
      classicPath: "/processes",
      drive: async (page, uiKind, stepsWait) => {
        for (const label of ["Templates", "Suites", "Schedules"]) {
          const clickedTab = await safeClick(
            page.getByRole("tab", { name: new RegExp(label, "i") }),
          );
          if (!clickedTab && uiKind === "next") {
            await safeClick(
              page.getByRole("link", { name: new RegExp(label, "i") }),
            );
          }
          await stepsWait();
        }
      },
    },
    {
      id: "ci",
      label: "CI",
      nextPath: "/next/ci",
      classicPath: "/ci",
      drive: async (page, _uiKind, stepsWait) => {
        const rows = page.locator("[data-run-id], .ci-row, tbody tr");
        await safeClick(rows.nth(0));
        await stepsWait();
        await safeClick(rows.nth(1));
        await stepsWait();
      },
    },
    {
      id: "repos",
      label: "Repos",
      nextPath: "/next/repos",
      classicPath: "/repos",
      drive: async (page, _uiKind, stepsWait) => {
        await safeClick(page.locator("a[href*='acme/widgets']"));
        await stepsWait();
        await safeClick(page.locator("nav a[href*='/repos']"));
        await stepsWait();
      },
    },
    {
      id: "telemetry",
      label: "Telemetry",
      nextPath: "/next/telemetry",
      classicPath: "/metrics",
      drive: async (page, _uiKind, stepsWait) => {
        for (const label of ["7d", "30d", "People", "All"]) {
          await safeClick(
            page.getByRole("button", { name: label, exact: true }),
          );
          await stepsWait();
        }
      },
    },
    {
      id: "context",
      label: "Context",
      nextPath: "/next/context",
      classicPath: "/settings/memory",
      drive: async (page, _uiKind, stepsWait) => {
        const buttons = page.locator("main button");
        await safeClick(buttons.nth(1));
        await stepsWait();
        await safeClick(buttons.nth(2));
        await stepsWait();
      },
    },
    {
      id: "fleet",
      label: "Fleet",
      nextPath: "/next/fleet",
      classicPath: "/system",
      drive: async (page, _uiKind, stepsWait) => {
        for (const label of [
          "Hosts",
          "Controls",
          "Logs",
          "Sandboxes",
          "Top",
          "Health",
        ]) {
          await safeClick(
            page.getByRole("tab", { name: new RegExp(label, "i") }).or(
              page.getByRole("button", {
                name: new RegExp(`^${label}$`, "i"),
              }),
            ),
          );
          await stepsWait();
        }
      },
    },
    {
      id: "settings",
      label: "Settings",
      nextPath: "/next/settings",
      classicPath: "/settings",
      drive: async (page, _uiKind, stepsWait) => {
        for (const label of ["Models", "Git", "Tasks", "General"]) {
          await safeClick(
            page
              .getByRole("link", { name: new RegExp(`^${label}`, "i") })
              .or(
                page.getByRole("tab", { name: new RegExp(`^${label}`, "i") }),
              ),
          );
          await stepsWait();
        }
      },
    },
  ];
}

async function waitForPageReady(page, uiKind) {
  if (uiKind === "next") {
    await page
      .waitForSelector(".main-stage > *:not(router-outlet)", {
        timeout: 30_000,
      })
      .catch(() => {});
  } else {
    await page
      .waitForSelector("#root > *", { timeout: 30_000 })
      .catch(() => {});
  }
  await page.waitForTimeout(2_500);
}

async function measureIdleGroup(
  browser,
  demo,
  uiKind,
  specs,
  idleMs,
  resolveInitiator,
) {
  const storageState = await signIn(demo.url, demo.code);
  const sessions = await Promise.all(
    specs.map(async (spec) => {
      const context = await browser.newContext({
        viewport: { width: 1440, height: 900 },
        storageState,
      });
      const page = await context.newPage();
      const rec = await attachRequestRecorder(
        context,
        page,
        demo.url,
        resolveInitiator,
      );
      const targetPath = uiKind === "next" ? spec.nextPath : spec.classicPath;
      const loadStartMs = Date.now();
      await page.goto(`${demo.url}${targetPath}`, {
        waitUntil: "domcontentloaded",
      });
      await waitForPageReady(page, uiKind);
      if (spec.afterSettle) {
        await spec.afterSettle(page, uiKind);
        await page.waitForTimeout(500);
      }
      return { spec, context, page, rec, loadStartMs, idleStartMs: 0 };
    }),
  );

  // Start the exact idle window simultaneously across all settled tabs.
  const sharedIdleStart = Date.now();
  for (const s of sessions) s.idleStartMs = sharedIdleStart;
  console.error(
    `[measure] Observing ${uiKind} (${sessions.length} pages) idle for ${Math.round(idleMs / 1000)}s...`,
  );
  await new Promise((r) => setTimeout(r, idleMs));
  const sharedIdleEnd = Date.now();

  const out = new Map();
  for (const s of sessions) {
    const loadSummary = summarizeRequests(
      s.rec.records,
      uiKind,
      s.loadStartMs,
      s.idleStartMs,
    );
    const idleSummary = summarizeRequests(
      s.rec.records,
      uiKind,
      s.idleStartMs,
      sharedIdleEnd,
    );
    out.set(s.spec.id, { loadSummary, idleSummary });
    await s.rec.detach();
    await s.context.close();
  }
  return out;
}

async function measureActiveGroup(
  browser,
  demo,
  uiKind,
  specs,
  activeMs,
  resolveInitiator,
) {
  const storageState = await signIn(demo.url, demo.code);
  const sessions = await Promise.all(
    specs.map(async (spec) => {
      const context = await browser.newContext({
        viewport: { width: 1440, height: 900 },
        storageState,
      });
      const page = await context.newPage();
      const rec = await attachRequestRecorder(
        context,
        page,
        demo.url,
        resolveInitiator,
      );
      const targetPath = uiKind === "next" ? spec.nextPath : spec.classicPath;
      await page.goto(`${demo.url}${targetPath}`, {
        waitUntil: "domcontentloaded",
      });
      await waitForPageReady(page, uiKind);
      if (spec.afterSettle) {
        await spec.afterSettle(page, uiKind);
        await page.waitForTimeout(500);
      }
      return { spec, context, page, rec };
    }),
  );

  const sharedStart = Date.now();
  const deadline = sharedStart + activeMs;
  const stepDelay = Math.max(1_500, Math.floor(activeMs / 8));
  console.error(
    `[measure] Driving ${uiKind} (${sessions.length} pages) normal use for ${Math.round(activeMs / 1000)}s...`,
  );

  await Promise.all(
    sessions.map(async (s) => {
      const stepsWait = async () => {
        const rem = deadline - Date.now();
        if (rem > 0) await s.page.waitForTimeout(Math.min(stepDelay, rem));
      };
      if (s.spec.drive) {
        await s.spec.drive(s.page, uiKind, stepsWait);
      }
      const rem = deadline - Date.now();
      if (rem > 0) await s.page.waitForTimeout(rem);
    }),
  );

  const sharedEnd = Date.now();
  const out = new Map();
  for (const s of sessions) {
    out.set(
      s.spec.id,
      summarizeRequests(s.rec.records, uiKind, sharedStart, sharedEnd),
    );
    await s.rec.detach();
    await s.context.close();
  }
  return out;
}

function renderRouteTable(summary, windowLabel) {
  if (summary.rows.length === 0) {
    return "_0 requests recorded during this window._\n";
  }
  const lines = [
    `| Route pattern | Count (${windowLabel}) | Req / min | Component / service (initiator) | Duplicates |`,
    `|---|---:|---:|---|---:|`,
  ];
  for (const r of summary.rows) {
    lines.push(
      `| \`${r.pattern}\` | ${r.count} | ${r.perMinute.toFixed(2)} | \`${r.initiator}\` | ${r.duplicates} |`,
    );
  }
  lines.push(
    `| **Total** | **${summary.totalCount}** | **${summary.perMinute.toFixed(2)}** | — | **${summary.duplicates}** |`,
  );
  return `${lines.join("\n")}\n`;
}

function formatMarkdownReport(meta, pageResults, idleSeconds, activeSeconds) {
  const idleLabel = `${Math.round((idleSeconds / 60) * 10) / 10}m idle`;
  const activeLabel = `${Math.round((activeSeconds / 60) * 10) / 10}m active`;
  const out = [];
  out.push(`# UI Request Rate Measurement (\`/next/\` vs Classic \`/\`)`);
  out.push("");
  out.push(
    `Seeded against \`grain demo ${REALISTIC_DEMO_ARGS.join(" ")}\`: **${meta.totalTasks} tasks** (${meta.openTasks} open, ${meta.closedTasks} closed), **${meta.runningCount} running agents** with transcripts, **6+ open notifications**, and **live \`director\` session** with active run + streaming transcript.`,
  );
  out.push("");
  out.push(`## Summary by Page`);
  out.push("");
  out.push(
    `| Page | \`/next/\` initial load | \`/next/\` idle (${idleLabel}) | \`/next/\` idle req/min | Classic \`/\` idle (${idleLabel}) | Classic \`/\` idle req/min | Ratio (\`/next/\` ÷ \`/\`) | \`/next/\` active (${activeLabel}) req/min | Classic \`/\` active (${activeLabel}) req/min |`,
  );
  out.push(`|---|---:|---:|---:|---:|---:|---:|---:|---:|`);
  for (const p of pageResults) {
    const ratio =
      p.classic.idle.perMinute > 0
        ? `${(p.next.idle.perMinute / p.classic.idle.perMinute).toFixed(2)}×`
        : "—";
    out.push(
      `| ${p.label} (\`${p.nextPath}\`) | ${p.next.load.totalCount} | ${p.next.idle.totalCount} | **${p.next.idle.perMinute.toFixed(2)}** | ${p.classic.idle.totalCount} | ${p.classic.idle.perMinute.toFixed(2)} | ${ratio} | ${p.next.active.perMinute.toFixed(2)} | ${p.classic.active.perMinute.toFixed(2)} |`,
    );
  }
  out.push("");
  out.push(`## Angular UI (\`/next/\`) — Per-Page Breakdown`);
  out.push("");
  for (const p of pageResults) {
    out.push(`### ${p.label} (\`${p.nextPath}\`)`);
    out.push("");
    out.push(`**Steady-state idle (${idleLabel}):**`);
    out.push("");
    out.push(renderRouteTable(p.next.idle, idleLabel));
    out.push(`**Scripted normal use (${activeLabel}):**`);
    out.push("");
    out.push(renderRouteTable(p.next.active, activeLabel));
  }
  out.push(`## Classic UI (\`/\`) — Per-Page Breakdown`);
  out.push("");
  for (const p of pageResults) {
    out.push(`### ${p.label} (\`${p.classicPath}\`)`);
    out.push("");
    out.push(`**Steady-state idle (${idleLabel}):**`);
    out.push("");
    out.push(renderRouteTable(p.classic.idle, idleLabel));
    out.push(`**Scripted normal use (${activeLabel}):**`);
    out.push("");
    out.push(renderRouteTable(p.classic.active, activeLabel));
  }
  return out.join("\n");
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  const scratchDir = await fsp.mkdtemp(
    path.join(os.tmpdir(), "grain-req-measure-"),
  );

  let browser;
  try {
    const { binPath, resolveInitiator } = await prepareBinaryAndSourceMaps(
      opts,
      scratchDir,
    );
    browser = await chromium.launch();

    // Start two dedicated seeded demo servers for the concurrent idle run
    // (one for /next/, one for classic /) so they never contend.
    const [demoNext, demoClassic] = await Promise.all([
      startSeededDemo(binPath, scratchDir),
      startSeededDemo(binPath, scratchDir),
    ]);

    let nextSpecs = buildPageSpecs(demoNext.seed);
    let classicSpecs = buildPageSpecs(demoClassic.seed);
    if (opts.pagesFilter) {
      nextSpecs = nextSpecs.filter((s) => opts.pagesFilter.has(s.id));
      classicSpecs = classicSpecs.filter((s) => opts.pagesFilter.has(s.id));
    }

    const idleMs = opts.idleSeconds * 1_000;
    const activeMs = opts.activeSeconds * 1_000;

    let nextIdleMap;
    let classicIdleMap;
    let nextActiveMap;
    let classicActiveMap;
    try {
      [nextIdleMap, classicIdleMap] = await Promise.all([
        measureIdleGroup(
          browser,
          demoNext,
          "next",
          nextSpecs,
          idleMs,
          resolveInitiator,
        ),
        measureIdleGroup(
          browser,
          demoClassic,
          "classic",
          classicSpecs,
          idleMs,
          resolveInitiator,
        ),
      ]);
      [nextActiveMap, classicActiveMap] = await Promise.all([
        measureActiveGroup(
          browser,
          demoNext,
          "next",
          nextSpecs,
          activeMs,
          resolveInitiator,
        ),
        measureActiveGroup(
          browser,
          demoClassic,
          "classic",
          classicSpecs,
          activeMs,
          resolveInitiator,
        ),
      ]);
    } finally {
      await Promise.all([demoNext.stop(), demoClassic.stop()]);
    }

    const pageResults = nextSpecs.map((s, idx) => {
      const cs = classicSpecs[idx];
      const ni = nextIdleMap.get(s.id);
      const ci = classicIdleMap.get(s.id);
      return {
        id: s.id,
        label: s.label,
        nextPath: s.nextPath,
        classicPath: cs.classicPath,
        next: {
          load: ni.loadSummary,
          idle: ni.idleSummary,
          active: nextActiveMap.get(s.id),
        },
        classic: {
          load: ci.loadSummary,
          idle: ci.idleSummary,
          active: classicActiveMap.get(s.id),
        },
      };
    });

    const markdown = formatMarkdownReport(
      demoNext.seed,
      pageResults,
      opts.idleSeconds,
      opts.activeSeconds,
    );

    if (opts.out) {
      const outPath = path.resolve(opts.out);
      await fsp.mkdir(path.dirname(outPath), { recursive: true });
      await fsp.writeFile(outPath, markdown, "utf8");
      console.error(`[measure] Wrote Markdown report to ${outPath}`);
    } else {
      process.stdout.write(markdown);
    }

    if (opts.json) {
      const jsonPath = path.resolve(opts.json);
      await fsp.mkdir(path.dirname(jsonPath), { recursive: true });
      await fsp.writeFile(
        jsonPath,
        JSON.stringify(
          {
            measuredAt: new Date().toISOString(),
            idleSeconds: opts.idleSeconds,
            activeSeconds: opts.activeSeconds,
            seed: demoNext.seed,
            pages: pageResults,
          },
          null,
          2,
        ),
        "utf8",
      );
      console.error(`[measure] Wrote JSON data to ${jsonPath}`);
    }
  } finally {
    if (browser) await browser.close();
    await fsp.rm(scratchDir, { recursive: true, force: true }).catch(() => {});
  }
}

if (
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  main().catch((err) => {
    console.error(err);
    process.exit(1);
  });
}
