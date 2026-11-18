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

// Executes Item 4 of docs/test-plans/2026-11-16.md §1:
// 1. Cold & warm loads over trusted HTTPS (cold.har + warm.har): verifies bundles
//    are served from browser cache and index.html revalidates with 304.
// 2. Simulated daemon upgrade with open stale tabs: rebuilds ui-ng with changed
//    bundle hashes while tabs stay open, restarts daemon onto new build, checks
//    404 no-store on missing old chunks, records what /next/ shows on navigation,
//    and verifies ordinary reload picks up new bundle hashes without hard refresh.

import fs from "node:fs";
import path from "node:path";
import { execSync } from "node:child_process";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, "../../..");
const requireFromNg = createRequire(path.join(ROOT, "ui-ng/package.json"));
const { chromium } = requireFromNg("playwright");

const WORK = process.env.WORK_DIR || "/tmp/walk-id-sessions";
const PROXY_HTTPS = process.env.PROXY_HTTPS || "https://127.0.0.1:4292";
const CONTROL_HTTP = process.env.CONTROL_HTTP || "http://127.0.0.1:4293";
const CAPTURES = path.join(WORK, "captures");
fs.mkdirSync(CAPTURES, { recursive: true });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function ctlReset() {
  await fetch(`${CONTROL_HTTP}/reset`, { method: "POST" });
}
async function ctlConfig(patch) {
  await fetch(`${CONTROL_HTTP}/config`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
}
async function ctlStats() {
  return (await fetch(`${CONTROL_HTTP}/stats`)).json();
}

function currentMainBundle() {
  const files = fs
    .readdirSync(path.join(ROOT, "pkg/ui/static-next"))
    .filter((f) => /^main-[A-Z0-9]{8}\.js$/.test(f));
  return files[0] || "";
}

async function main() {
  const tasksPagePath = path.join(
    ROOT,
    "ui-ng/src/app/tasks/tasks-page.component.ts",
  );
  const helpDialogPath = path.join(
    ROOT,
    "ui-ng/src/app/shell/help-dialog.component.ts",
  );
  const origTasks = fs.readFileSync(tasksPagePath, "utf8");
  const origHelp = fs.readFileSync(helpDialogPath, "utf8");

  const out = {
    trustedTlsCache: {},
    simulatedUpgrade: {},
  };

  try {
    await ctlConfig({
      mode: "ok",
      delayMs: 100,
      ttlMs: 60000,
      proxyMode: "sign",
      proxyEmail: "alice@example.com",
      stripRetryAfter: false,
    });

    const profileDir = path.join(WORK, "trusted-profile-" + Date.now());
    const coldHar = path.join(CAPTURES, "cold-trusted.har");
    const warmHar = path.join(CAPTURES, "warm-trusted.har");

    // 1. Cold load with trusted CA (ignoreHTTPSErrors: false -> real Chromium HTTP cache!)
    await ctlReset();
    {
      const ctx = await chromium.launchPersistentContext(profileDir, {
        ignoreHTTPSErrors: false,
        viewport: { width: 1440, height: 900 },
        recordHar: { path: coldHar, mode: "minimal" },
      });
      const page = ctx.pages()[0] || (await ctx.newPage());
      await page.goto(`${PROXY_HTTPS}/next/inbox`);
      await page
        .locator('[data-testid="next-inbox-queue"]')
        .waitFor({ timeout: 15000 });
      await sleep(2500);
      const coldStats = await ctlStats();
      out.trustedTlsCache.cold = {
        fullVerifies: coldStats.fullVerifies,
        localVerifies: coldStats.localVerifies,
        proxyTotalRequests: coldStats.proxyEvents.length,
        hashedBundleHitsOnServer: coldStats.proxyEvents.filter((e) =>
          /^\/next\/(?:main|polyfills|styles|chunk)-[A-Z0-9]{8}\.(?:js|css)$/.test(
            e.path,
          ),
        ).length,
      };
      await ctx.close();
    }

    // 2. Warm load in the same persistent Chromium profile -> warm-trusted.har
    await ctlReset();
    {
      const ctx = await chromium.launchPersistentContext(profileDir, {
        ignoreHTTPSErrors: false,
        viewport: { width: 1440, height: 900 },
        recordHar: { path: warmHar, mode: "minimal" },
      });
      const page = ctx.pages()[0] || (await ctx.newPage());
      await page.goto(`${PROXY_HTTPS}/next/inbox`);
      await page
        .locator('[data-testid="next-inbox-queue"]')
        .waitFor({ timeout: 15000 });
      await sleep(2200);
      const warmStats = await ctlStats();
      const indexEvent = warmStats.proxyEvents.find(
        (e) => e.path === "/next/inbox",
      );
      const hashedHits = warmStats.proxyEvents.filter((e) =>
        /^\/next\/(?:main|polyfills|styles|chunk)-[A-Z0-9]{8}\.(?:js|css)$/.test(
          e.path,
        ),
      );
      out.trustedTlsCache.warm = {
        fullVerifies: warmStats.fullVerifies,
        localVerifies: warmStats.localVerifies,
        indexStatusOnServer: indexEvent?.status ?? null,
        indexCacheControl: indexEvent?.cacheControl ?? null,
        indexETag: indexEvent?.etag ?? null,
        hashedBundleHitsOnServer: hashedHits.length,
        proxyPaths: warmStats.proxyEvents.map((e) => `${e.method} ${e.path} -> ${e.status}`),
      };
      await ctx.close();
    }

    out.trustedTlsCache.harEntries = {
      coldHarEntries: JSON.parse(fs.readFileSync(coldHar, "utf8")).log.entries
        .length,
      warmHarEntries: JSON.parse(fs.readFileSync(warmHar, "utf8")).log.entries
        .length,
    };

    // 3. Simulated upgrade while two tabs stay open!
    const mainBefore = currentMainBundle();
    const browser = await chromium.launch();
    try {
      // Tab A: Save-Data tab (simulating unpreloaded route chunk on stale tab)
      const saveDataCtx = await browser.newContext({
        ignoreHTTPSErrors: false,
        viewport: { width: 1440, height: 900 },
      });
      await saveDataCtx.addInitScript(() => {
        Object.defineProperty(navigator, "connection", {
          configurable: true,
          get: () => ({ saveData: true }),
        });
      });
      const staleRouteTab = await saveDataCtx.newPage();
      const routeTab404s = [];
      const routeTabErrors = [];
      staleRouteTab.on("response", (res) => {
        if (res.status() === 404) {
          routeTab404s.push({
            url: new URL(res.url()).pathname,
            status: res.status(),
            cacheControl: res.headers()["cache-control"] || "",
          });
        }
      });
      staleRouteTab.on("pageerror", (err) => routeTabErrors.push(err.message));
      staleRouteTab.on("console", (msg) => {
        if (msg.type() === "error") routeTabErrors.push(msg.text());
      });
      await staleRouteTab.goto(`${PROXY_HTTPS}/next/inbox`);
      await staleRouteTab
        .locator('[data-testid="next-inbox-queue"]')
        .waitFor({ timeout: 15000 });

      // Tab B: Normal preloaded tab (route chunks preloaded, @defer Help chunk NOT yet loaded)
      const normalCtx = await browser.newContext({
        ignoreHTTPSErrors: false,
        viewport: { width: 1440, height: 900 },
      });
      const staleDeferTab = await normalCtx.newPage();
      const deferTab404s = [];
      staleDeferTab.on("response", (res) => {
        if (res.status() === 404) {
          deferTab404s.push({
            url: new URL(res.url()).pathname,
            status: res.status(),
            cacheControl: res.headers()["cache-control"] || "",
          });
        }
      });
      await staleDeferTab.goto(`${PROXY_HTTPS}/next/inbox`);
      await staleDeferTab
        .locator('[data-testid="next-inbox-queue"]')
        .waitFor({ timeout: 15000 });
      await sleep(2500);

      // Mutate TasksPageComponent & HelpDialogComponent so bundle hashes change, rebuild ui-ng & grain-slowverifier, restart daemon!
      const stamp = `UPGRADE_STAMP_${Date.now()}`;
      fs.writeFileSync(
        tasksPagePath,
        origTasks + `\nexport const __WALK_UPGRADE_TASKS = "${stamp}";\n`,
      );
      fs.writeFileSync(
        helpDialogPath,
        origHelp + `\nexport const __WALK_UPGRADE_HELP = "${stamp}";\n`,
      );
      execSync(
        `find pkg/ui/static-next -mindepth 1 -not -name '.gitignore' -not -name '.gitkeep' -not -name 'placeholder.html' -delete && cd ui-ng && npm run build`,
        { cwd: ROOT, stdio: "pipe" },
      );
      const mainAfter = currentMainBundle();
      execSync(
        `go build -o "${WORK}/bin/grain-slowverifier" ./docs/test-runs/2026-11-16-identity-sessions/grain_slowverifier.go`,
        { cwd: ROOT, stdio: "pipe" },
      );
      execSync(
        `bash -c 'source "${__dirname}/env.sh" && WORK_DIR="${WORK}" SLOW_DELAY_MS=100 SLOW_TTL_MS=60000 start_walk_daemon "${WORK}"'`,
        { cwd: ROOT, stdio: "pipe" },
      );

      // Tab A (stale unpreloaded route): click Tasks in rail!
      await staleRouteTab.locator('[data-testid="rail-tasks"]').click();
      await sleep(1200);
      const routeTabUrlAfterClick = new URL(staleRouteTab.url()).pathname;
      const routeTabSnackbarCount = await staleRouteTab
        .locator('[data-testid="snackbar"]')
        .count();
      const routeTabSnackbarText =
        routeTabSnackbarCount > 0
          ? await staleRouteTab.locator('[data-testid="snackbar"]').innerText()
          : null;
      const routeTabActionCount = await staleRouteTab
        .locator('[data-testid="snackbar-action"]')
        .count();
      await staleRouteTab.screenshot({
        path: path.join(CAPTURES, "stale-chunk-snackbar-desktop-light.png"),
      });

      // Click the Reload action button in the snackbar -> must load new index.html & land on /next/tasks!
      let urlAfterReloadAction = null;
      if (routeTabActionCount > 0) {
        await staleRouteTab.locator('[data-testid="snackbar-action"]').click();
        await staleRouteTab
          .locator('[data-testid="backlog-toolbar"]')
          .waitFor({ timeout: 15000 });
        urlAfterReloadAction = new URL(staleRouteTab.url()).pathname;
      }

      // Tab B (stale preloaded tab): click Help (?) trigger (@defer chunk not prefetched)!
      await staleDeferTab.locator('[data-testid="help-trigger"]').click();
      await sleep(1200);
      const deferTabSnackbarCount = await staleDeferTab
        .locator('[data-testid="snackbar"]')
        .count();
      const deferTabSnackbarText =
        deferTabSnackbarCount > 0
          ? await staleDeferTab.locator('[data-testid="snackbar"]').innerText()
          : null;

      // Ordinary reload (NOT hard refresh) on Tab B -> must pick up new index.html (200) and new main-<HASH>.js!
      await ctlReset();
      const reloadLoadedScripts = [];
      staleDeferTab.on("response", (res) => {
        const p = new URL(res.url()).pathname;
        if (p === "/next/inbox" || /^\/next\/main-[A-Z0-9]{8}\.js$/.test(p)) {
          reloadLoadedScripts.push({ path: p, status: res.status() });
        }
      });
      await staleDeferTab.reload();
      await staleDeferTab
        .locator('[data-testid="next-inbox-queue"]')
        .waitFor({ timeout: 15000 });
      await staleDeferTab.locator('[data-testid="rail-tasks"]').click();
      await staleDeferTab
        .locator('[data-testid="backlog-toolbar"]')
        .waitFor({ timeout: 10000 });
      const postReloadStats = await ctlStats();

      out.simulatedUpgrade = {
        mainBundleBefore: mainBefore,
        mainBundleAfter: mainAfter,
        bundleHashChanged: mainBefore !== mainAfter,
        staleRouteClick: {
          missingChunk404s: routeTab404s,
          urlAfterClick: routeTabUrlAfterClick,
          snackbarShown: routeTabSnackbarCount > 0,
          snackbarText: routeTabSnackbarText,
          snackbarHasActionBtn: routeTabActionCount > 0,
          urlAfterReloadAction,
          consoleErrorsSample: routeTabErrors.slice(0, 3),
        },
        staleDeferClick: {
          missingChunk404s: deferTab404s,
          snackbarShown: deferTabSnackbarCount > 0,
          snackbarText: deferTabSnackbarText,
        },
        ordinaryReloadAfterUpgrade: {
          responses: reloadLoadedScripts,
          loadedNewMainBundle: reloadLoadedScripts.some(
            (r) => r.path === `/next/${mainAfter}` && r.status === 200,
          ),
          fullVerifiesAfterDaemonRestart: postReloadStats.fullVerifies,
          tasksRouteLoadedCleanly: new URL(staleDeferTab.url()).pathname === "/next/tasks",
        },
      };

      await saveDataCtx.close();
      await normalCtx.close();
    } finally {
      await browser.close();
    }
  } finally {
    // Always restore source files and rebuild clean HEAD static-next + grain-slowverifier
    fs.writeFileSync(tasksPagePath, origTasks);
    fs.writeFileSync(helpDialogPath, origHelp);
    execSync(
      `find pkg/ui/static-next -mindepth 1 -not -name '.gitignore' -not -name '.gitkeep' -not -name 'placeholder.html' -delete && cd ui-ng && npm run build`,
      { cwd: ROOT, stdio: "pipe" },
    );
    execSync(
      `go build -o "${WORK}/bin/grain-slowverifier" ./docs/test-runs/2026-11-16-identity-sessions/grain_slowverifier.go`,
      { cwd: ROOT, stdio: "pipe" },
    );
  }

  const outFile = path.join(CAPTURES, "upgrade_results.json");
  fs.writeFileSync(outFile, JSON.stringify(out, null, 2));
  console.log(JSON.stringify(out, null, 2));
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
