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
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { newAccessCode, signIn } from "../../ui-shared/e2e/accesscode.js";
import { expect, test } from "./fixtures.js";
import {
  BUDGET_HIDDEN_SAMPLE_SECONDS,
  BUDGET_IDLE_SAMPLE_SECONDS,
  BUDGET_SETTLE_MS,
  MAX_HIDDEN_TAB_REQUESTS,
  NEXT_IDLE_REQUEST_BUDGETS,
} from "./request-budgets.js";
import {
  REALISTIC_DEMO_ARGS,
  attachRequestRecorder,
  seedRealisticDemo,
  summarizeRequests,
} from "./request-harness.mjs";

const REPO_ROOT = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);

function nextPagePath(
  id: string,
  seed: { primaryRunningTaskId: string },
): string {
  switch (id) {
    case "inbox":
      return "/next/inbox";
    case "tasks":
      return "/next/tasks";
    case "agents":
      return "/next/agents";
    case "workstreams":
      return "/next/workstreams";
    case "task-detail":
      return `/next/task/${seed.primaryRunningTaskId}?tab=runs`;
    case "sessions":
      return "/next/sessions/director";
    case "processes":
      return "/next/processes";
    case "ci":
      return "/next/ci";
    case "repos":
      return "/next/repos";
    case "telemetry":
      return "/next/telemetry";
    case "context":
      return "/next/context";
    case "fleet":
      return "/next/fleet";
    case "settings":
      return "/next/settings";
    default:
      throw new Error(`Unknown budget page id: ${id}`);
  }
}

async function startIsolatedDemo(tmpDir: string) {
  const bin =
    process.env["GRAIN_E2E_DEMO_BIN"] || path.join(REPO_ROOT, "bin/grain");
  const code = newAccessCode();
  const codeFile = path.join(tmpDir, "local-api-token");
  await fs.writeFile(codeFile, `${code}\n`, { mode: 0o600 });

  const proc = spawn(
    bin,
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
  const url = await new Promise<string>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`grain demo start timeout:\n${output}`)),
      60_000,
    );
    const onData = (chunk: Buffer) => {
      output += chunk.toString();
      const m = output.match(
        /at http:\/\/(127\.0\.0\.1|0\.0\.0\.0|\[::\]):(\d+)/,
      );
      if (m) {
        clearTimeout(timer);
        resolve(`http://127.0.0.1:${m[2]}`);
      }
    };
    proc.stdout.on("data", onData);
    proc.stderr.on("data", onData);
    proc.on("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
    proc.on("exit", (exitCode, sig) => {
      clearTimeout(timer);
      reject(new Error(`grain demo exited (${exitCode}, ${sig}):\n${output}`));
    });
  });

  return { url, code, proc };
}

test("every main /next/ page stays within its steady-state idle request budget and pauses completely when hidden", async ({
  browser,
}) => {
  test.setTimeout(180_000);

  const tmpDir = await fs.mkdtemp(
    path.join(os.tmpdir(), "grain-ng-req-budget-"),
  );
  const server = await startIsolatedDemo(tmpDir);
  const stopped = new Promise<void>((resolve) =>
    server.proc.on("exit", () => resolve()),
  );

  const openTabs: {
    id: string;
    label: string;
    context: import("@playwright/test").BrowserContext;
    page: import("@playwright/test").Page;
    rec: Awaited<ReturnType<typeof attachRequestRecorder>>;
  }[] = [];

  try {
    const seed = await seedRealisticDemo(server.url, server.code, tmpDir);
    expect(seed.totalTasks).toBeGreaterThanOrEqual(100);
    expect(seed.runningCount).toBeGreaterThanOrEqual(12);

    const session = await signIn(server.url, server.code);

    await Promise.all(
      NEXT_IDLE_REQUEST_BUDGETS.map(async (budget) => {
        const route = nextPagePath(budget.id, seed);
        const context = await browser.newContext({
          viewport: { width: 1440, height: 900 },
          storageState: session,
        });
        const page = await context.newPage();
        const rec = await attachRequestRecorder(
          context,
          page,
          server.url,
          null,
        );
        await page.goto(`${server.url}${route}`, {
          waitUntil: "domcontentloaded",
          timeout: 45_000,
        });
        await page
          .waitForSelector(".main-stage > *:not(router-outlet)", {
            timeout: 25_000,
          })
          .catch(() => {});
        openTabs.push({
          id: budget.id,
          label: budget.label,
          context,
          page,
          rec,
        });
      }),
    );

    // Allow initial cold-boot reads and ETag priming to settle.
    await new Promise((r) => setTimeout(r, BUDGET_SETTLE_MS));

    const idleStartMs = Date.now();
    await new Promise((r) => setTimeout(r, BUDGET_IDLE_SAMPLE_SECONDS * 1_000));
    const idleEndMs = Date.now();

    const tabById = new Map(openTabs.map((t) => [t.id, t]));
    const violations: string[] = [];

    for (const budget of NEXT_IDLE_REQUEST_BUDGETS) {
      const tab = tabById.get(budget.id)!;
      const summary = summarizeRequests(
        tab.rec.records,
        "next",
        idleStartMs,
        idleEndMs,
      );

      if (summary.perMinute > budget.maxRequestsPerMin) {
        const routes = summary.rows
          .map(
            (r: { pattern: string; count: number; perMinute: number }) =>
              `${r.pattern}=${r.count} (${r.perMinute.toFixed(1)}/min)`,
          )
          .join(", ");
        violations.push(
          `${budget.label}: ${summary.perMinute.toFixed(2)} req/min exceeds budget ${budget.maxRequestsPerMin} req/min [${routes}]`,
        );
      }

      if (summary.duplicates > budget.maxDuplicates) {
        const dupRoutes = summary.rows
          .filter((r: { duplicates: number }) => r.duplicates > 0)
          .map(
            (r: { pattern: string; duplicates: number }) =>
              `${r.pattern}=${r.duplicates}`,
          )
          .join(", ");
        violations.push(
          `${budget.label}: ${summary.duplicates} duplicate fetches exceed budget ${budget.maxDuplicates} [${dupRoutes}]`,
        );
      }

      if (budget.forbidPerRowTaskFetch) {
        const perRow = summary.rows.filter(
          (r: { pattern: string }) =>
            r.pattern === "GET /api/tasks/{id}" ||
            r.pattern.startsWith("GET /api/tasks/{id} ") ||
            r.pattern.startsWith("GET /api/tasks/{id}?"),
        );
        if (perRow.length > 0) {
          violations.push(
            `${budget.label}: unexpected per-row task reads in steady state: ${perRow.map((r: { pattern: string; count: number }) => `${r.pattern}=${r.count}`).join(", ")}`,
          );
        }
      }
    }

    expect(
      violations,
      `Request budget violations:\n${violations.join("\n")}`,
    ).toEqual([]);

    // Verify background tabs pause polling and live transcripts completely when hidden.
    const hiddenCheckIds = [
      "inbox",
      "tasks",
      "agents",
      "task-detail",
      "sessions",
    ];
    await Promise.all(
      hiddenCheckIds.map(async (id) => {
        const tab = tabById.get(id);
        if (!tab) return;
        await tab.page.evaluate(() => {
          Object.defineProperty(document, "hidden", {
            configurable: true,
            get: () => true,
          });
          Object.defineProperty(document, "visibilityState", {
            configurable: true,
            get: () => "hidden",
          });
          document.dispatchEvent(new Event("visibilitychange"));
        });
      }),
    );

    // Let any in-flight request triggered before visibilitychange finish.
    await new Promise((r) => setTimeout(r, 500));
    const hiddenStartMs = Date.now();
    await new Promise((r) =>
      setTimeout(r, BUDGET_HIDDEN_SAMPLE_SECONDS * 1_000),
    );
    const hiddenEndMs = Date.now();

    const hiddenViolations: string[] = [];
    for (const id of hiddenCheckIds) {
      const tab = tabById.get(id);
      if (!tab) continue;
      const hiddenReqs = tab.rec.records.filter(
        (r: { timestampMs: number }) =>
          r.timestampMs >= hiddenStartMs && r.timestampMs < hiddenEndMs,
      );
      if (hiddenReqs.length > MAX_HIDDEN_TAB_REQUESTS) {
        hiddenViolations.push(
          `${tab.label}: made ${hiddenReqs.length} requests while document.hidden (${hiddenReqs.map((r: { method: string; pathname: string }) => `${r.method} ${r.pathname}`).join(", ")})`,
        );
      }
    }

    expect(
      hiddenViolations,
      `Hidden-tab suspension violations:\n${hiddenViolations.join("\n")}`,
    ).toEqual([]);
  } finally {
    await Promise.all(
      openTabs.map(async (t) => {
        await t.rec.detach();
        await t.context.close().catch(() => {});
      }),
    );
    if (server.proc.exitCode === null && server.proc.signalCode === null) {
      server.proc.kill("SIGTERM");
      const exited = await Promise.race([
        stopped.then(() => true),
        new Promise<boolean>((r) => setTimeout(() => r(false), 5_000)),
      ]);
      if (!exited) {
        server.proc.kill("SIGKILL");
        await stopped;
      }
    }
    await fs.rm(tmpDir, { recursive: true, force: true });
  }
});
