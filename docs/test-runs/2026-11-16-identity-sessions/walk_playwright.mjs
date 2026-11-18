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

// Playwright walkthrough for docs/test-plans/2026-11-16.md §1 (Items 1, 4a, 5, 6)
// against a live grain-slowverifier daemon over HTTPS.

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, "../../..");
const requireFromNg = createRequire(path.join(ROOT, "ui-ng/package.json"));
const { chromium } = requireFromNg("playwright");

const WORK = process.env.WORK_DIR || "/tmp/walk-id-sessions";
const PROXY_HTTPS = process.env.PROXY_HTTPS || "https://127.0.0.1:4292";
const PROXY_ALT_HTTPS = process.env.PROXY_ALT_HTTPS || "https://127.0.0.1:4295";
const CONTROL_HTTP = process.env.CONTROL_HTTP || "http://127.0.0.1:4293";
const CAPTURES = path.join(WORK, "captures");
fs.mkdirSync(CAPTURES, { recursive: true });
const SEEDED_TASK_ID = JSON.parse(
  fs.readFileSync(path.join(WORK, "task1.json"), "utf8"),
).id;

async function ctlReset() {
  await fetch(`${CONTROL_HTTP}/reset`, { method: "POST" });
}

async function ctlConfig(patch) {
  const r = await fetch(`${CONTROL_HTTP}/config`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
  return r.json();
}

async function ctlStats() {
  const r = await fetch(`${CONTROL_HTTP}/stats`);
  return r.json();
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function run() {
  const report = {
    browserVersion: "",
    item1: {},
    item4Cache: {},
    item5Retry: {},
    item6PhoneSaveData: {},
  };

  // --- ITEM 1 & ITEM 4a: Cold load + Warm load HARs with persistent disk cache ---
  const profileDir = path.join(WORK, "chrome-profile-" + Date.now());
  const coldHarPath = path.join(CAPTURES, "cold.har");
  const warmHarPath = path.join(CAPTURES, "warm.har");

  await ctlConfig({
    mode: "ok",
    delayMs: 1500,
    ttlMs: 60000,
    proxyMode: "sign",
    proxyEmail: "alice@example.com",
    stripRetryAfter: false,
  });
  await ctlReset();

  {
    const ctx = await chromium.launchPersistentContext(profileDir, {
      ignoreHTTPSErrors: true,
      viewport: { width: 1440, height: 900 },
      recordHar: { path: coldHarPath, mode: "minimal" },
    });
    report.browserVersion = ctx.browser()?.version() || "chromium";
    const page = ctx.pages()[0] || (await ctx.newPage());
    const coldReqs = [];
    page.on("response", (res) => {
      coldReqs.push({
        method: res.request().method(),
        url: res.url().replace(PROXY_HTTPS, ""),
        status: res.status(),
        fromServiceWorker: res.fromServiceWorker(),
        cacheControl: res.headers()["cache-control"] || "",
        etag: res.headers()["etag"] || "",
        setCookie: Boolean(res.headers()["set-cookie"]),
      });
    });

    const t0 = Date.now();
    await page.goto(`${PROXY_HTTPS}/next/inbox`, { waitUntil: "domcontentloaded" });
    await page.locator('[data-testid="next-inbox-queue"]').waitFor({ timeout: 15000 });
    const coldRenderMs = Date.now() - t0;
    // Wait past ROUTE_PRELOAD_DELAY_MS (1500ms) so lazy route chunks finish preloading.
    await sleep(2500);
    const afterColdStats = await ctlStats();
    const cookiesAfterCold = await ctx.cookies(PROXY_HTTPS);

    // Navigate client-side to /next/tasks and /next/task/<id>
    await ctlReset();
    const navT0 = Date.now();
    await page.locator('[data-testid="rail-tasks"]').click();
    await page.locator('[data-testid="backlog-toolbar"]').waitFor({ timeout: 10000 });
    const tasksNavMs = Date.now() - navT0;

    const taskNavT0 = Date.now();
    await page.goto(`${PROXY_HTTPS}/next/task/${SEEDED_TASK_ID}`, {
      waitUntil: "domcontentloaded",
    });
    await page.locator('[data-testid="task-details"]').waitFor({ timeout: 10000 });
    const taskDetailMs = Date.now() - taskNavT0;
    const afterNavStats = await ctlStats();

    report.item1.coldLoad = {
      coldRenderMs,
      totalResponses: coldReqs.length,
      fullVerifies: afterColdStats.fullVerifies,
      localVerifies: afterColdStats.localVerifies,
      verifyEvents: afterColdStats.verifyEvents,
      cookies: cookiesAfterCold.map((c) => ({
        name: c.name,
        httpOnly: c.httpOnly,
        secure: c.secure,
        sameSite: c.sameSite,
        path: c.path,
        domain: c.domain,
      })),
    };
    report.item1.subsequentNav = {
      tasksNavMs,
      taskDetailMs,
      fullVerifies: afterNavStats.fullVerifies,
      localVerifies: afterNavStats.localVerifies,
    };
    await ctx.close();
  }

  // Warm load in persistent profile -> warm.har
  await ctlReset();
  {
    const ctx = await chromium.launchPersistentContext(profileDir, {
      ignoreHTTPSErrors: true,
      viewport: { width: 1440, height: 900 },
      recordHar: { path: warmHarPath, mode: "minimal" },
    });
    const page = ctx.pages()[0] || (await ctx.newPage());
    const t0 = Date.now();
    await page.goto(`${PROXY_HTTPS}/next/inbox`, { waitUntil: "domcontentloaded" });
    await page.locator('[data-testid="next-inbox-queue"]').waitFor({ timeout: 15000 });
    const warmRenderMs = Date.now() - t0;
    await sleep(1000);
    const warmStats = await ctlStats();
    const perfResources = await page.evaluate(() =>
      performance.getEntriesByType("resource").map((e) => ({
        name: new URL(e.name).pathname,
        transferSize: e.transferSize,
        encodedBodySize: e.encodedBodySize,
      })),
    );
    await ctx.close();

    // Summarize proxy events on warm load: index 304 vs hashed bundles hitting proxy
    const proxyPaths = warmStats.proxyEvents.map((e) => ({
      method: e.method,
      path: e.path,
      status: e.status,
      cacheControl: e.cacheControl,
    }));
    const hashedHitsOnServer = proxyPaths.filter((p) =>
      /^\/next\/(?:main|polyfills|styles|chunk)-[A-Z0-9]{8}\.(?:js|css)$/.test(p.path),
    );
    report.item4Cache.warmLoad = {
      warmRenderMs,
      fullVerifies: warmStats.fullVerifies,
      localVerifies: warmStats.localVerifies,
      proxyRequests: proxyPaths,
      hashedBundleServerHits: hashedHitsOnServer.length,
      browserCachedResources: perfResources.filter((r) => r.transferSize === 0).length,
      totalResources: perfResources.length,
    };
  }

  // Parse cold.har and warm.har entry counts
  const coldHar = JSON.parse(fs.readFileSync(coldHarPath, "utf8"));
  const warmHar = JSON.parse(fs.readFileSync(warmHarPath, "utf8"));
  report.item4Cache.harSummary = {
    coldHarEntries: coldHar.log.entries.length,
    warmHarEntries: warmHar.log.entries.length,
  };

  const browser = await chromium.launch();
  try {
    // --- ITEM 1b & 1c: Cookie re-issuance, staging proxy isolation, cross-port isolation, logout ---
    {
      await ctlConfig({
        mode: "ok",
        delayMs: 200,
        ttlMs: 1200,
        proxyMode: "sign",
        proxyEmail: "alice@example.com",
      });
      await ctlReset();
      const ctx = await browser.newContext({
        ignoreHTTPSErrors: true,
        viewport: { width: 1440, height: 900 },
      });
      const page = await ctx.newPage();
      await page.goto(`${PROXY_HTTPS}/next/inbox`);
      await page.locator('[data-testid="next-inbox-queue"]').waitFor({ timeout: 15000 });
      const c1 = (await ctx.cookies(PROXY_HTTPS)).find(
        (c) => c.name === "__Host-grain_verified",
      );

      // Wait for 1.2s TTL to expire, then trigger another API call -> cookie re-issued!
      await sleep(1500);
      await ctlConfig({ ttlMs: 60000 });
      await ctlReset();
      await page.goto(`${PROXY_HTTPS}/next/tasks`);
      await page.locator('[data-testid="backlog-toolbar"]').waitFor({ timeout: 15000 });
      const reissueStats = await ctlStats();
      const c2 = (await ctx.cookies(PROXY_HTTPS)).find(
        (c) => c.name === "__Host-grain_verified",
      );

      // Staging proxy check inside the real browser session:
      // 1. /staging/preview/plant tries to plant __Host-grain_verified=evil & staging_app=ok
      // 2. /staging/preview/echo returns cookies & headers seen by the staging environment
      const plantRes = await page.evaluate(async () => {
        const r = await fetch("/staging/preview/plant", { credentials: "include" });
        return { status: r.status, body: await r.json() };
      });
      const echoRes = await page.evaluate(async () => {
        const r = await fetch("/staging/preview/echo", { credentials: "include" });
        return { status: r.status, body: await r.json() };
      });
      const cookiesAfterStaging = await ctx.cookies(PROXY_HTTPS);
      const cAfterPlant = cookiesAfterStaging.find(
        (c) => c.name === "__Host-grain_verified",
      );

      // Cross-port / host check: navigate a second tab to 127.0.0.1:4295
      await ctlReset();
      const altPage = await ctx.newPage();
      await altPage.goto(`${PROXY_ALT_HTTPS}/api/auth/user`);
      const altStats = await ctlStats();
      await altPage.close();

      // Logout through the /next/ Account Popover UI!
      let logoutSetCookie = "";
      page.on("response", (res) => {
        if (res.url().endsWith("/api/auth/logout")) {
          logoutSetCookie = res.headers()["set-cookie"] || "";
        }
      });
      await page.locator('[data-testid="avatar-btn"]').click();
      await page.locator('[data-testid="account-sign-out"]').waitFor({ timeout: 5000 });
      // Drop proxy assertion right before sign-out so landing on /next/ tests signed-out state
      await ctlConfig({ proxyMode: "none" });
      await page.locator('[data-testid="account-sign-out"]').click();
      await page.waitForLoadState("domcontentloaded");
      await sleep(500);
      // Note: POST /api/auth/logout was sent with proxyMode=none (which still works because logout is an open route!)
      const cookiesAfterLogout = await ctx.cookies(PROXY_HTTPS);
      const postLogoutBody = await page.locator("body").innerText();

      report.item1.reissueStagingLogout = {
        initialCookiePresent: Boolean(c1?.value),
        reissuedCookieChanged: Boolean(c1 && c2 && c1.value !== c2.value),
        reissueFullVerifies: reissueStats.fullVerifies,
        stagingPlantStatus: plantRes.status,
        stagingEchoStatus: echoRes.status,
        stagingSawCookie: echoRes.body.cookie,
        stagingSawVerifierCookie: echoRes.body.cookie.includes("grain_verified"),
        stagingSawAssertionHeader: Boolean(
          echoRes.body.headers?.["x-slow-proxy-assertion"],
        ),
        browserVerifierCookieUnchangedAfterEvilPlant: cAfterPlant?.value === c2?.value,
        allCookiesAfterStaging: cookiesAfterStaging.map((c) => ({
          name: c.name,
          path: c.path,
          valuePrefix: c.value.slice(0, 12),
        })),
        altPortSent4292Cookie: altStats.verifyEvents.some((e) => e.hasCookie),
        altPortFullVerifies: altStats.fullVerifies,
        logoutSetCookie,
        verifierCookieClearedAfterLogout: !cookiesAfterLogout.some(
          (c) => c.name === "__Host-grain_verified",
        ),
        signedOutPageHeading: postLogoutBody.split("\n")[0],
      };
      await ctx.close();
    }

    // --- ITEM 5: Retry storm behaviour (with & without Retry-After, POST during blip, sustained banner) ---
    {
      await ctlConfig({
        mode: "ok",
        delayMs: 0,
        ttlMs: 60000,
        proxyMode: "sign",
        proxyEmail: "alice@example.com",
        stripRetryAfter: false,
      });
      const ctx = await browser.newContext({
        ignoreHTTPSErrors: true,
        viewport: { width: 1440, height: 900 },
      });
      const page = await ctx.newPage();
      await page.goto(`${PROXY_HTTPS}/next/tasks`);
      await page.locator('[data-testid="backlog-toolbar"]').waitFor({ timeout: 15000 });
      await sleep(500);

      // 5a: Blip WITH Retry-After: 5 (7.5s outage, then recover) + POST during blip
      await ctx.clearCookies();
      await ctlReset();
      await ctlConfig({ mode: "down", stripRetryAfter: false });

      const bannersDuringBlipWithRA = [];
      const blipStart = Date.now();

      // Open New Task sheet and submit a POST while verifier is returning 503 verifier_unavailable
      await page.locator('[data-testid="rail-new-task"]').click();
      await page.locator('[data-testid="nt-title"]').waitFor({ timeout: 5000 });
      await page.locator('[data-testid="nt-title"]').fill("Task filed during verifier blip");
      const postT0 = Date.now();
      await page.locator('[data-testid="new-task-submit"]').click();
      // Wait for form error banner inside new-task-sheet
      const postErrorText = await page
        .locator('[data-testid="new-task-error"]')
        .innerText({ timeout: 5000 });
      const postSurfaceMs = Date.now() - postT0;
      // Close the new task sheet so we watch the Tasks page itself
      await page.keyboard.press("Escape");
      await sleep(200);

      while (Date.now() - blipStart < 7500) {
        const alerts = await page.locator('main [role="alert"]').allInnerTexts();
        if (alerts.length > 0) {
          bannersDuringBlipWithRA.push({ atMs: Date.now() - blipStart, alerts });
        }
        await sleep(400);
      }
      const statsDuringWithRA = await ctlStats();
      await ctlConfig({ mode: "ok" });
      await sleep(6500);
      const alertsAfterRecoveryWithRA = await page
        .locator('main [role="alert"]')
        .allInnerTexts();
      const postCallsInBlip = statsDuringWithRA.proxyEvents.filter(
        (e) => e.method === "POST" && e.path === "/api/tasks",
      ).length;

      // 5b: Blip WITHOUT Retry-After (stripRetryAfter: true, 4.5s outage, then recover)
      await ctx.clearCookies();
      await ctlReset();
      await ctlConfig({ mode: "down", stripRetryAfter: true });
      const bannersDuringBlipNoRA = [];
      const blip2Start = Date.now();
      while (Date.now() - blip2Start < 4500) {
        const alerts = await page.locator('main [role="alert"]').allInnerTexts();
        if (alerts.length > 0) {
          bannersDuringBlipNoRA.push({ atMs: Date.now() - blip2Start, alerts });
        }
        await sleep(400);
      }
      const statsDuringNoRA = await ctlStats();
      await ctlConfig({ mode: "ok", stripRetryAfter: false });
      await sleep(4000);
      const alertsAfterRecoveryNoRA = await page
        .locator('main [role="alert"]')
        .allInnerTexts();

      // 5c: Sustained 503 verifier_unavailable outage -> wait until banner appears, check count & text
      await ctx.clearCookies();
      await ctlReset();
      await ctlConfig({ mode: "down", stripRetryAfter: true });
      const sustainedStart = Date.now();
      let firstBannerAtMs = null;
      let sustainedAlerts = [];
      while (Date.now() - sustainedStart < 45000) {
        const alerts = await page.locator('main [role="alert"]').allInnerTexts();
        if (alerts.length > 0) {
          firstBannerAtMs = Date.now() - sustainedStart;
          sustainedAlerts = alerts;
          break;
        }
        await sleep(500);
      }
      // Wait 5s more to verify a second banner never stacks up
      await sleep(5000);
      const sustainedAlertsAfter5s = await page
        .locator('main [role="alert"]')
        .allInnerTexts();

      await ctlConfig({ mode: "ok", stripRetryAfter: false });
      await sleep(5000);
      const alertsAfterSustainedRecovery = await page
        .locator('main [role="alert"]')
        .allInnerTexts();

      report.item5Retry = {
        withRetryAfter: {
          bannersDuringRecovery: bannersDuringBlipWithRA,
          alertsAfterRecovery: alertsAfterRecoveryWithRA,
          proxy503Count: statsDuringWithRA.proxyEvents.filter((e) => e.status === 503)
            .length,
          sampleRetryAfterHeader:
            statsDuringWithRA.proxyEvents.find((e) => e.status === 503)?.retryAfter ||
            "",
          postCallsCount: postCallsInBlip,
          postSurfaceMs,
          postErrorText,
        },
        withoutRetryAfter: {
          bannersDuringRecovery: bannersDuringBlipNoRA,
          alertsAfterRecovery: alertsAfterRecoveryNoRA,
          proxy503Count: statsDuringNoRA.proxyEvents.filter((e) => e.status === 503)
            .length,
          sampleRetryAfterHeader:
            statsDuringNoRA.proxyEvents.find((e) => e.status === 503)?.retryAfter ||
            "<absent>",
        },
        sustainedOutage: {
          firstBannerAtMs,
          bannerCountAtTrigger: sustainedAlerts.length,
          bannersAtTrigger: sustainedAlerts,
          bannerCountAfter5sMore: sustainedAlertsAfter5s.length,
          bannersAfter5sMore: sustainedAlertsAfter5s,
          bannersAfterBackendRestored: alertsAfterSustainedRecovery,
        },
      };
      await ctx.close();
    }

    // --- ITEM 6: Navigation progress at phone width (390px) & Save-Data disables preloading ---
    {
      await ctlConfig({
        mode: "ok",
        delayMs: 0,
        ttlMs: 60000,
        proxyMode: "sign",
        proxyEmail: "alice@example.com",
        stripRetryAfter: false,
      });
      const phoneCtx = await browser.newContext({
        ignoreHTTPSErrors: true,
        viewport: { width: 390, height: 844 },
        isMobile: true,
        hasTouch: true,
      });
      await phoneCtx.addInitScript(() => {
        Object.defineProperty(navigator, "connection", {
          configurable: true,
          get: () => ({ saveData: true, effectiveType: "4g" }),
        });
      });
      const phonePage = await phoneCtx.newPage();
      await phonePage.goto(`${PROXY_HTTPS}/next/inbox`);
      await phonePage
        .locator('[data-testid="next-inbox-queue"]')
        .waitFor({ timeout: 15000 });

      const chunksAtBoot = await phonePage.evaluate(
        () =>
          performance
            .getEntriesByType("resource")
            .filter((e) => /\/next\/chunk-[^/]+\.js$/.test(e.name)).length,
      );
      // Wait 3.2s (well past ROUTE_PRELOAD_DELAY_MS = 1500ms)
      await sleep(3200);
      const chunksAfter3sSaveData = await phonePage.evaluate(
        () =>
          performance
            .getEntriesByType("resource")
            .filter((e) => /\/next\/chunk-[^/]+\.js$/.test(e.name)).length,
      );

      // Hold the lazy Tasks chunk for 900ms to inspect g-nav-progress at 390px phone width
      const heldChunks = [];
      await phonePage.route("**/next/chunk-*.js", async (route) => {
        heldChunks.push(new URL(route.request().url()).pathname);
        await sleep(900);
        await route.continue();
      });

      const clickT0 = Date.now();
      const navClickPromise = phonePage
        .locator('nav[aria-label="Primary destinations (compact)"] a[href$="/tasks"]')
        .click();
      const bar = phonePage.locator('[data-testid="nav-progress"]');
      await bar.waitFor({ state: "visible", timeout: 2000 });
      const barShownAtMs = Date.now() - clickT0;
      const barBox = await bar.boundingBox();
      const barStyle = await bar.evaluate((el) => {
        const cs = getComputedStyle(el);
        return {
          position: cs.position,
          pointerEvents: cs.pointerEvents,
          zIndex: cs.zIndex,
          height: cs.height,
        };
      });
      await navClickPromise;
      await phonePage
        .locator('[data-testid="backlog-toolbar"]')
        .waitFor({ timeout: 10000 });
      const barCountAfterNav = await bar.count();

      report.item6PhoneSaveData = {
        viewport: { width: 390, height: 844 },
        chunksAtBoot,
        chunksAfter3200msWithSaveData: chunksAfter3sSaveData,
        preloadedExtraChunksUnderSaveData: chunksAfter3sSaveData - chunksAtBoot,
        onDemandFetchedChunksCount: heldChunks.length,
        barShownAtMs,
        barBoundingBox: barBox,
        barComputedStyle: barStyle,
        barCountAfterNavComplete: barCountAfterNav,
      };
      await phoneCtx.close();
    }
  } finally {
    await browser.close();
  }

  const outPath = path.join(CAPTURES, "playwright_results.json");
  fs.writeFileSync(outPath, JSON.stringify(report, null, 2));
  console.log(JSON.stringify(report, null, 2));
}

run().catch((err) => {
  console.error(err);
  process.exit(1);
});
