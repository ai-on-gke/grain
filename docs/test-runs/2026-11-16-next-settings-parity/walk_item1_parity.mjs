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

// Section 9 Item 1 live walkthrough against a real grain daemon:
// Saves each /next/settings/* page in Playwright, diffs against classic
// /settings/*, grain settings/config/secrets CLI, and grain state sync,
// plus reverse direction, blank/default semantics, scope set used by a
// dispatched fake-agent task, secrets never echoed, and PR #1587/#1602 checks.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { signIn } from "../../../ui-shared/e2e/accesscode.js";

const require = createRequire(
  path.resolve("/home/debian/work/ui-ng/package.json"),
);
const { chromium } = require("playwright");

const DAEMON = process.env.DAEMON || "http://127.0.0.1:4280";
const WORK_DIR = process.env.WORK_DIR || "/tmp/walk-pg";
const CODE =
  process.env.CODE ||
  fs.readFileSync(path.join(WORK_DIR, "data/local-api-token"), "utf8").trim();
const GRAIN = process.env.GRAIN || "/home/debian/bin/grain";

const findings = [];
function record(area, check, ok, detail) {
  const entry = { area, check, ok, detail };
  findings.push(entry);
  const tag = ok ? "PASS" : "FAIL";
  console.log(`[${tag}] ${area} :: ${check} :: ${typeof detail === "string" ? detail : JSON.stringify(detail)}`);
}

async function api(method, urlPath, body) {
  const res = await fetch(`${DAEMON}${urlPath}`, {
    method,
    headers: {
      Authorization: `Bearer ${CODE}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    // plain text
  }
  return { status: res.status, ok: res.ok, text, json };
}

function grainCli(args, input) {
  const env = {
    PATH: "/home/debian/bin:/usr/local/bin:/usr/bin:/bin",
    HOME: path.join(WORK_DIR, "home"),
    GRAIN_SERVER: DAEMON,
    GRAIN_TOKEN: CODE,
    GRAIN_DATA_DIR: path.join(WORK_DIR, "data"),
  };
  return execFileSync(GRAIN, args, {
    env,
    input,
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
  });
}

function stateSync() {
  const out = grainCli(["state", "sync"]);
  const repoDir = path.join(WORK_DIR, "data/state-repo");
  const rawCfg = JSON.parse(
    fs.readFileSync(path.join(repoDir, "tables/grain_config.json"), "utf8"),
  );
  const grainConfig = Array.isArray(rawCfg) ? (rawCfg[0] || {}) : rawCfg;
  let apiScopeSets = [];
  const scopeSetPath = path.join(repoDir, "tables/api_scope_set.json");
  if (fs.existsSync(scopeSetPath)) {
    apiScopeSets = JSON.parse(fs.readFileSync(scopeSetPath, "utf8"));
  }
  let gitConns = [];
  const gitConnPath = path.join(repoDir, "tables/git_connection.json");
  if (fs.existsSync(gitConnPath)) {
    gitConns = JSON.parse(fs.readFileSync(gitConnPath, "utf8"));
  }
  const allRepoFiles = [];
  function walk(dir) {
    for (const ent of fs.readdirSync(dir, { withFileTypes: true })) {
      if (ent.name === ".git") continue;
      const p = path.join(dir, ent.name);
      if (ent.isDirectory()) walk(p);
      else allRepoFiles.push(p);
    }
  }
  walk(repoDir);
  const fullDump = allRepoFiles
    .map((p) => `--- ${path.relative(repoDir, p)} ---\n${fs.readFileSync(p, "utf8")}`)
    .join("\n");
  return { out: out.trim(), grainConfig, apiScopeSets, gitConns, fullDump };
}

async function clickSaveNext(page) {
  const btn = page.getByTestId("settings-save-btn");
  await btn.waitFor({ state: "visible", timeout: 10000 });
  const respP = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/settings") && r.request().method() === "POST",
  );
  await btn.click();
  const resp = await respP;
  const reqBody = resp.request().postDataJSON();
  const resText = await resp.text();
  // Wait for save bar to disappear (dirty cleared)
  await btn.waitFor({ state: "hidden", timeout: 10000 }).catch(() => {});
  return { status: resp.status(), reqBody, resText };
}

async function waitClassicLoaded(page, selector) {
  await page.waitForLoadState("domcontentloaded");
  if (selector) {
    await page.locator(selector).first().waitFor({ state: "visible", timeout: 15000 });
  } else {
    await page.waitForTimeout(800);
  }
}

async function main() {
  const storageState = await signIn(DAEMON, CODE, "alice@example.com");
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    storageState,
  });
  const page = await context.newPage();
  const consoleErrors = [];
  page.on("pageerror", (e) => consoleErrors.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() === "error") consoleErrors.push(`console.error: ${m.text()}`);
  });

  // =========================================================================
  // 1. ENGINE (/next/settings/engine) + blank/default + reverse
  // =========================================================================
  {
    await page.goto(`${DAEMON}/next/settings/engine`);
    await page.getByTestId("input-poll-interval").waitFor({ state: "visible" });
    await page.getByTestId("input-poll-interval").fill("1500ms");
    await page.getByTestId("input-max-workers").fill("4");
    await page.getByTestId("input-max-merges-per-repo").fill("3");
    const save = await clickSaveNext(page);
    const gotApi = (await api("GET", "/api/settings")).json;
    const cliOut = grainCli(["settings"]);
    const synced = stateSync();

    // Classic readback
    await page.goto(`${DAEMON}/settings/engine`);
    await waitClassicLoaded(page, 'input[name="pollInterval"]');
    const cPoll = await page.locator('input[name="pollInterval"]').inputValue();
    const cWorkers = await page.locator('input[name="maxWorkers"]').inputValue();
    const cMerges = await page.locator('input[name="maxMergesPerRepo"]').inputValue();

    record(
      "engine",
      "next->classic/cli/state-repo parity",
      save.status === 200 &&
        gotApi.pollInterval === "1.5s" &&
        gotApi.maxWorkers === 4 &&
        gotApi.maxMergesPerRepo === 3 &&
        cPoll === "1.5s" &&
        cWorkers === "4" &&
        cMerges === "3" &&
        cliOut.includes("poll interval:  1.5s") &&
        cliOut.includes("max workers:    4") &&
        cliOut.includes("max merges/repo: 3") &&
        synced.grainConfig.poll_interval_ms === 1500 &&
        synced.grainConfig.max_workers === 4 &&
        synced.grainConfig.max_merges_per_repo === 3,
      {
        saveStatus: save.status,
        api: {
          pollInterval: gotApi.pollInterval,
          maxWorkers: gotApi.maxWorkers,
          maxMergesPerRepo: gotApi.maxMergesPerRepo,
        },
        classic: { cPoll, cWorkers, cMerges },
        stateRepo: {
          poll_interval_ms: synced.grainConfig.poll_interval_ms,
          max_workers: synced.grainConfig.max_workers,
          max_merges_per_repo: synced.grainConfig.max_merges_per_repo,
        },
        cliMatch: {
          poll: cliOut.match(/poll interval:.*/)?.[0],
          workers: cliOut.match(/max workers:.*/)?.[0],
          merges: cliOut.match(/max merges\/repo:.*/)?.[0],
        },
      },
    );

    // Reverse: edit in classic /settings/engine, read back in /next/settings/engine
    const pollBox = page.locator('input[name="pollInterval"]');
    const pSave1 = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") &&
        (r.request().method() === "PUT" || r.request().method() === "POST"),
    );
    await pollBox.fill("2s");
    await pollBox.blur();
    await pSave1;
    const wBox = page.locator('input[name="maxWorkers"]');
    const pSave2 = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") &&
        (r.request().method() === "PUT" || r.request().method() === "POST"),
    );
    await wBox.fill("3");
    await wBox.blur();
    await pSave2;

    await page.goto(`${DAEMON}/next/settings/engine`);
    await page.getByTestId("input-poll-interval").waitFor({ state: "visible" });
    const nPoll = await page.getByTestId("input-poll-interval").inputValue();
    const nWorkers = await page.getByTestId("input-max-workers").inputValue();
    record(
      "engine",
      "reverse classic->next parity",
      nPoll === "2s" && nWorkers === "3",
      { nPoll, nWorkers },
    );

    // Blank/default semantics on /next/settings/engine
    await page.getByTestId("input-poll-interval").fill("");
    await page.getByTestId("input-poll-interval").blur();
    await page.getByTestId("input-max-workers").fill("");
    await page.getByTestId("input-max-workers").blur();
    await page.getByTestId("input-max-merges-per-repo").fill("");
    await page.getByTestId("input-max-merges-per-repo").blur();
    const saveBtnVisible = await page.getByTestId("settings-save-btn").isVisible();
    let blankSave = null;
    if (saveBtnVisible) {
      blankSave = await clickSaveNext(page);
    }
    const afterBlank = (await api("GET", "/api/settings")).json;
    const afterBlankCli = grainCli(["settings"]);
    record(
      "engine",
      "blank/default semantics on /next/settings/engine",
      saveBtnVisible &&
        blankSave?.status === 200 &&
        afterBlank.pollInterval === "10s" &&
        afterBlank.maxWorkers === 10 &&
        afterBlank.maxMergesPerRepo === 5,
      {
        saveBtnVisible,
        blankSaveStatus: blankSave?.status,
        reqBody: blankSave?.reqBody,
        afterBlank: {
          pollInterval: afterBlank.pollInterval,
          pollIntervalDefault: afterBlank.pollIntervalDefault,
          maxWorkers: afterBlank.maxWorkers,
          maxMergesPerRepo: afterBlank.maxMergesPerRepo,
        },
        cliLines: {
          poll: afterBlankCli.match(/poll interval:.*/)?.[0],
          workers: afterBlankCli.match(/max workers:.*/)?.[0],
          merges: afterBlankCli.match(/max merges\/repo:.*/)?.[0],
        },
      },
    );

    // Restore fast poll interval for dispatched tasks later
    await api("POST", "/api/settings", {
      pollInterval: "500ms",
      maxWorkers: 2,
      maxMergesPerRepo: 5,
    });
  }

  // =========================================================================
  // 2. CI (/next/settings/ci) + blank/default + reverse
  // =========================================================================
  {
    await page.goto(`${DAEMON}/next/settings/ci`);
    await page.getByTestId("input-ci-jobs-per-run").waitFor({ state: "visible" });
    await page.getByTestId("input-ci-jobs-per-run").fill("4");
    await page.locator("#ci-hist").fill("1234");
    await page.getByTestId("input-ciStepOutputMb").fill("32");
    await page.getByTestId("input-ciStepOutputDays").fill("7");
    await page.getByTestId("input-ciRunOutputKb").fill("128");
    await page.locator("#ci-cache-mb").fill("512");
    await page.locator("#ci-art-mb").fill("4096");
    await page.locator("#ci-reserve").fill("45s");
    await page.getByTestId("input-ciAgeBoost").fill("3m");
    const save = await clickSaveNext(page);
    const gotApi = (await api("GET", "/api/settings")).json;
    const cliOut = grainCli(["settings"]);
    const synced = stateSync();

    await page.goto(`${DAEMON}/settings/ci`);
    await waitClassicLoaded(page, 'input[name="ciJobsPerRun"]');
    const cJobs = await page.locator('input[name="ciJobsPerRun"]').inputValue();
    const cHist = await page.locator('input[name="ciRunHistory"]').inputValue();
    const cCache = await page.locator('input[name="ciCacheMb"]').inputValue();
    const cBoost = await page.locator('input[name="ciAgeBoost"]').inputValue();
    const cRes = await page.locator('input[name="ciReserveAfter"]').inputValue();

    record(
      "ci",
      "next->classic/cli/state-repo parity",
      save.status === 200 &&
        gotApi.ciJobsPerRun === 4 &&
        gotApi.ciRunHistory === 1234 &&
        gotApi.ciCacheMb === 512 &&
        gotApi.ciArtifactMb === 4096 &&
        cJobs === "4" &&
        cHist === "1234" &&
        cCache === "512" &&
        (cBoost === "3m" || cBoost === "3m0s") &&
        cRes === "45s" &&
        synced.grainConfig.ci_jobs_per_run === 4 &&
        synced.grainConfig.ci_run_history === 1234 &&
        synced.grainConfig.ci_cache_mb === 512 &&
        cliOut.includes("ci jobs per run: 4") &&
        cliOut.includes("ci run history: 1234 runs"),
      {
        saveStatus: save.status,
        api: {
          ciJobsPerRun: gotApi.ciJobsPerRun,
          ciRunHistory: gotApi.ciRunHistory,
          ciCacheMb: gotApi.ciCacheMb,
          ciArtifactMb: gotApi.ciArtifactMb,
          ciAgeBoost: gotApi.ciAgeBoost,
          ciReserveAfter: gotApi.ciReserveAfter,
        },
        classic: { cJobs, cHist, cCache, cBoost, cRes },
        stateRepo: {
          ci_jobs_per_run: synced.grainConfig.ci_jobs_per_run,
          ci_run_history: synced.grainConfig.ci_run_history,
          ci_cache_mb: synced.grainConfig.ci_cache_mb,
          ci_age_boost: synced.grainConfig.ci_age_boost,
        },
      },
    );

    // Reverse: classic -> next
    const jobsBox = page.locator('input[name="ciJobsPerRun"]');
    const pSave = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") &&
        (r.request().method() === "PUT" || r.request().method() === "POST"),
    );
    await jobsBox.fill("6");
    await jobsBox.blur();
    await pSave;

    await page.goto(`${DAEMON}/next/settings/ci`);
    await page.getByTestId("input-ci-jobs-per-run").waitFor({ state: "visible" });
    const nJobs = await page.getByTestId("input-ci-jobs-per-run").inputValue();
    record("ci", "reverse classic->next parity", nJobs === "6", { nJobs });

    // Blank/default semantics on /next/settings/ci
    await page.getByTestId("input-ci-jobs-per-run").fill("");
    await page.locator("#ci-hist").fill("");
    await page.locator("#ci-cache-mb").fill("");
    await page.getByTestId("input-ciAgeBoost").fill("");
    await page.locator("#ci-reserve").fill("");
    const ciBlankSave = await clickSaveNext(page);
    const afterCiBlank = (await api("GET", "/api/settings")).json;
    const afterCiCli = grainCli(["settings"]);
    await page.reload();
    await page.locator("#ci-cache-mb").waitFor({ state: "visible" });
    const nextCacheValAfterBlank = await page.locator("#ci-cache-mb").inputValue();
    record(
      "ci",
      "blank/default semantics on /next/settings/ci",
      ciBlankSave.status === 200 &&
        afterCiBlank.ciJobsPerRun === 0 &&
        afterCiBlank.ciRunHistory === 0 &&
        afterCiBlank.ciCacheMb === 0 &&
        afterCiCli.includes("ci job cache: 256 MiB (grain default, unset)") &&
        afterCiBlank.ciAgeBoost === "" &&
        afterCiBlank.ciReserveAfter === "" &&
        nextCacheValAfterBlank === "",
      {
        reqBody: ciBlankSave.reqBody,
        afterCiBlank: {
          ciJobsPerRun: afterCiBlank.ciJobsPerRun,
          ciRunHistory: afterCiBlank.ciRunHistory,
          ciCacheMb: afterCiBlank.ciCacheMb,
          ciCacheMbDefault: afterCiBlank.ciCacheMbDefault,
          ciAgeBoost: afterCiBlank.ciAgeBoost,
          ciReserveAfter: afterCiBlank.ciReserveAfter,
        },
        nextCacheValAfterBlank,
        cliLines: {
          history: afterCiCli.match(/ci run history:.*/)?.[0],
          cache: afterCiCli.match(/ci job cache:.*/)?.[0],
          boost: afterCiCli.match(/ci queue age boost:.*/)?.[0],
        },
      },
    );
  }

  // =========================================================================
  // 3. SANDBOXES (/next/settings/sandboxes) + blank/default + missing fields + probe
  // =========================================================================
  {
    await page.goto(`${DAEMON}/next/settings/sandboxes`);
    await page.getByTestId("input-sandbox-cpus").waitFor({ state: "visible" });

    // Check parity presence of Disk overcommit & Host memory reserve
    const hasDiskOcNext =
      (await page.locator('label:has-text("Disk overcommit"), input[name="sandboxDiskOvercommit"]').count()) > 0;
    const hasMemResNext =
      (await page.locator('label:has-text("Host memory reserve"), input[name="sandboxMemoryReserveMb"]').count()) > 0;
    const hasProbeBtn =
      (await page.locator('[data-testid="sandbox-probe-btn"]').count()) > 0;
    const probeApi = await api("POST", "/api/sandbox-probe", {});

    record(
      "sandboxes",
      "field parity check: sandboxDiskOvercommit & sandboxMemoryReserveMb present in /next/",
      hasDiskOcNext && hasMemResNext,
      { hasDiskOcNext, hasMemResNext },
    );
    record(
      "sandboxes",
      "sandbox probe hidden on host backend & POST /api/sandbox-probe rejects cleanly",
      !hasProbeBtn && probeApi.status >= 400 && probeApi.status < 500,
      { hasProbeBtn, probeStatus: probeApi.status, probeText: probeApi.text.trim() },
    );

    // Write values in /next/settings/sandboxes
    await page.getByTestId("input-sandbox-cpus").fill("4");
    await page.getByTestId("input-sandbox-memory").fill("4096");
    await page.getByTestId("input-sandbox-disk").fill("40");
    await page.locator("#sb-cpu-oc").fill("3");
    await page.locator("#sb-mem-oc").fill("1.5");
    await page.getByTestId("input-disk-floor-share").fill("0.15");
    await page.getByTestId("input-disk-floor-min").fill("4096");
    await page.getByTestId("select-overcommit-mode").click();
    await page.getByTestId("select-overcommit-mode-opt-auto").click();
    await page.getByTestId("input-overcommit-cpuMin").fill("1.25");
    await page.getByTestId("input-overcommit-cpuMax").fill("4.5");
    await page.getByTestId("input-overcommit-cpuPressureTarget").fill("30");
    await page.getByTestId("card-custom-sandbox-image").getByTestId("input-sandbox-image").fill("ghcr.io/acme/walk-sb:v1");
    await page.getByTestId("card-kubernetes").getByTestId("input-k8sNamespace").fill("walk-ns");
    await page.getByTestId("card-kubernetes").getByTestId("input-k8sPriorityClassAgent").fill("walk-agent");
    const sbSave = await clickSaveNext(page);
    const gotApi = (await api("GET", "/api/settings")).json;
    const cliOut = grainCli(["settings"]);
    const synced = stateSync();

    // Classic readback
    await page.goto(`${DAEMON}/settings/sandboxes`);
    await waitClassicLoaded(page, 'input[name="sandboxCpus"]');
    const cCpus = await page.locator('input[name="sandboxCpus"]').inputValue();
    const cMem = await page.locator('input[name="sandboxMemoryMb"]').inputValue();
    const cDisk = await page.locator('input[name="sandboxDiskGb"]').inputValue();
    const cCpuOc = await page.locator('input[name="sandboxCpuOvercommit"]').inputValue();
    const cMemOc = await page.locator('input[name="sandboxMemoryOvercommit"]').inputValue();
    const cFloorShare = await page.locator('input[name="sandboxDiskFloorShare"]').inputValue();
    const cFloorMin = await page.locator('input[name="sandboxDiskFloorMinMb"]').inputValue();
    const cHasDiskOc = (await page.locator('input[name="sandboxDiskOvercommit"]').count()) > 0;
    const cHasMemRes = (await page.locator('input[name="sandboxMemoryReserveMb"]').count()) > 0;

    record(
      "sandboxes",
      "next->classic/cli/state-repo parity",
      sbSave.status === 200 &&
        gotApi.sandboxCpus === 4 &&
        gotApi.sandboxMemoryMb === 4096 &&
        gotApi.sandboxDiskGb === 40 &&
        gotApi.sandboxCpuOvercommit === 3 &&
        gotApi.sandboxMemoryOvercommit === 1.5 &&
        gotApi.sandboxDiskFloorShare === 0.15 &&
        gotApi.sandboxDiskFloorMinMb === 4096 &&
        gotApi.sandboxOvercommit?.mode === "auto" &&
        gotApi.sandboxOvercommit?.cpuMin === 1.25 &&
        gotApi.sandboxImage === "ghcr.io/acme/walk-sb:v1" &&
        cCpus === "4" &&
        cMem === "4096" &&
        cDisk === "40" &&
        cCpuOc === "3" &&
        cMemOc === "1.5" &&
        cFloorShare === "0.15" &&
        cFloorMin === "4096" &&
        synced.grainConfig.sandbox_cpus === 4 &&
        synced.grainConfig.sandbox_cpu_overcommit === 3 &&
        synced.grainConfig.sandbox_disk_floor_min_mb === 4096,
      {
        sbSaveStatus: sbSave.status,
        classic: { cCpus, cMem, cDisk, cCpuOc, cMemOc, cFloorShare, cFloorMin, cHasDiskOc, cHasMemRes },
        apiOvercommit: gotApi.sandboxOvercommit,
        stateRepo: {
          sandbox_cpus: synced.grainConfig.sandbox_cpus,
          sandbox_cpu_overcommit: synced.grainConfig.sandbox_cpu_overcommit,
          sandbox_disk_floor_share: synced.grainConfig.sandbox_disk_floor_share,
          sandbox_disk_floor_min_mb: synced.grainConfig.sandbox_disk_floor_min_mb,
          sandbox_image: synced.grainConfig.sandbox_image,
        },
        cliFloor: cliOut.match(/sandbox disk floor:.*/)?.[0],
      },
    );

    // Reverse in classic: edit sandboxCpuOvercommit=2.5, sandboxDiskOvercommit=3.5, sandboxMemoryReserveMb=1024
    for (const [name, val] of [
      ["sandboxCpuOvercommit", "2.5"],
      ["sandboxDiskOvercommit", "3.5"],
      ["sandboxMemoryReserveMb", "1024"],
    ]) {
      const box = page.locator(`input[name="${name}"]`);
      const p = page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/settings") &&
          (r.request().method() === "PUT" || r.request().method() === "POST"),
      );
      await box.fill(val);
      await box.blur();
      await p;
    }
    await page.goto(`${DAEMON}/next/settings/sandboxes`);
    await page.locator("#sb-cpu-oc").waitFor({ state: "visible" });
    const nCpuOcRev = await page.locator("#sb-cpu-oc").inputValue();
    const afterRevApi = (await api("GET", "/api/settings")).json;
    record(
      "sandboxes",
      "reverse classic->next parity (cpuOvercommit visible; diskOvercommit & memReserve stored)",
      nCpuOcRev === "2.5" &&
        afterRevApi.sandboxDiskOvercommit === 3.5 &&
        afterRevApi.sandboxMemoryReserveMb === 1024,
      {
        nCpuOcRev,
        apiDiskOvercommit: afterRevApi.sandboxDiskOvercommit,
        apiMemReserve: afterRevApi.sandboxMemoryReserveMb,
      },
    );

    // Blank/default semantics on /next/settings/sandboxes!
    // 1) Switch to fixed mode, explicit 0 on CPU overcommit -> must save 0 (unbounded) and show warning
    await page.getByTestId("select-overcommit-mode").click();
    await page.getByTestId("select-overcommit-mode-opt-fixed").click();
    await page.locator("#sb-cpu-oc").fill("0");
    await page.locator("#sb-cpu-oc").blur();
    const zeroWarnVisible = await page.getByTestId("cpu-overcommit-unbounded").isVisible();
    const zeroSave = await clickSaveNext(page);
    const afterZero = (await api("GET", "/api/settings")).json;
    record(
      "sandboxes",
      "explicit 0 on CPU overcommit saves 0 (unbounded)",
      zeroWarnVisible && zeroSave.status === 200 && afterZero.sandboxCpuOvercommit === 0,
      { zeroWarnVisible, reqBody: zeroSave.reqBody, stored: afterZero.sandboxCpuOvercommit },
    );

    // 2) Clear CPU overcommit, Memory overcommit, Disk floor share, Disk floor min -> must save grain's default!
    // Test clearing CPU overcommit WITHOUT blurring first (only input event) to see if Save bar appears!
    await page.locator("#sb-cpu-oc").fill("");
    const saveVisibleBeforeBlur = await page.getByTestId("settings-save-btn").isVisible();
    await page.locator("#sb-cpu-oc").blur();
    const saveVisibleAfterBlur = await page.getByTestId("settings-save-btn").isVisible();
    await page.locator("#sb-mem-oc").fill("");
    await page.locator("#sb-mem-oc").blur();
    await page.getByTestId("input-disk-floor-share").fill("");
    await page.getByTestId("input-disk-floor-share").blur();
    await page.getByTestId("input-disk-floor-min").fill("");
    await page.getByTestId("input-disk-floor-min").blur();
    await page.getByTestId("input-sandbox-cpus").fill("");
    await page.getByTestId("input-sandbox-memory").fill("");
    await page.getByTestId("input-sandbox-disk").fill("");
    await page.getByTestId("sandbox-image-reset").click();
    const blankSbSave = await clickSaveNext(page);
    const afterSbBlank = (await api("GET", "/api/settings")).json;
    record(
      "sandboxes",
      "clearing overcommit/floor/shape saves grain defaults",
      blankSbSave.status === 200 &&
        afterSbBlank.sandboxCpuOvercommit === 2 &&
        afterSbBlank.sandboxMemoryOvercommit === 1 &&
        afterSbBlank.sandboxDiskFloorShare === 0.1 &&
        afterSbBlank.sandboxDiskFloorMinMb === 5120 &&
        afterSbBlank.sandboxCpus === 0 &&
        afterSbBlank.sandboxMemoryMb === 0 &&
        afterSbBlank.sandboxDiskGb === 0 &&
        afterSbBlank.sandboxImage === "",
      {
        saveVisibleBeforeBlur,
        saveVisibleAfterBlur,
        reqBody: blankSbSave.reqBody,
        afterSbBlank: {
          sandboxCpuOvercommit: afterSbBlank.sandboxCpuOvercommit,
          sandboxMemoryOvercommit: afterSbBlank.sandboxMemoryOvercommit,
          sandboxDiskFloorShare: afterSbBlank.sandboxDiskFloorShare,
          sandboxDiskFloorMinMb: afterSbBlank.sandboxDiskFloorMinMb,
          sandboxCpus: afterSbBlank.sandboxCpus,
          sandboxMemoryMb: afterSbBlank.sandboxMemoryMb,
          sandboxDiskGb: afterSbBlank.sandboxDiskGb,
          sandboxImage: afterSbBlank.sandboxImage,
        },
      },
    );

    // Also test classic blank behavior on sandboxCpuOvercommit for parity comparison!
    await api("POST", "/api/settings", { sandboxCpuOvercommit: 3, sandboxDiskOvercommit: 2, sandboxMemoryReserveMb: -1, sandboxOvercommit: { mode: "fixed", cpuMin: 1, cpuMax: 6, memoryMin: 1, memoryMax: 2, cpuPressureTarget: 25, memoryPressureTarget: 5 }, k8sNamespace: "", k8sPriorityClassAgent: "grain-agent" });
    await page.goto(`${DAEMON}/settings/sandboxes`);
    await waitClassicLoaded(page);
    const cCpuOcBox = page.locator('input[name="sandboxCpuOvercommit"]');
    const pClsBlank = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") &&
        (r.request().method() === "PUT" || r.request().method() === "POST"),
    );
    await cCpuOcBox.fill("");
    await cCpuOcBox.blur();
    await pClsBlank;
    const afterClsBlank = (await api("GET", "/api/settings")).json;
    record(
      "sandboxes",
      "classic vs next blank overcommit difference observed",
      true,
      {
        classicBlankStored: afterClsBlank.sandboxCpuOvercommit,
        note: "Classic emptyVal=0 saves 0; /next/ saves reportedDefault=2 (per PR #1611)",
      },
    );
    await api("POST", "/api/settings", { sandboxCpuOvercommit: 2 });
  }

  // =========================================================================
  // 4. HUB (/next/settings/registry) + token never echoed + reverse
  // =========================================================================
  {
    const HUB_SECRET = "grc_walk_secret_token_never_echo_9182";
    await page.goto(`${DAEMON}/next/settings/registry`);
    await page.getByTestId("input-hub-url").waitFor({ state: "visible" });
    await page.getByTestId("input-hub-url").fill("https://hub.walk.example");
    await page.getByTestId("input-hub-entry").fill("walk-pg");
    await page.getByTestId("select-hub-auth-mode").click();
    await page.getByTestId("select-hub-auth-mode-opt-oidc").click();
    await page.getByTestId("input-hub-oidc-audience").fill("https://hub.walk.example/aud");
    await page.getByTestId("select-hub-host-kind").click();
    await page.getByTestId("select-hub-host-kind-opt-kubernetes").click();
    await page.getByTestId("input-hub-display-name").fill("Walk PG Grain");
    await page.getByTestId("input-hub-purpose").fill("Section 9 parity");
    await page.getByTestId("input-scope-rule-repo").fill("acme/widgets");
    await page.getByTestId("input-scope-rule-prefix").fill("/pkg/api/");
    await page.getByTestId("add-scope-rule").click();
    const hubSave = await clickSaveNext(page);

    // Also save a hub token and check it's never echoed
    await page.getByTestId("input-hub-token").fill(HUB_SECRET);
    const pTok = page.waitForResponse(
      (r) =>
        (r.url().includes("/api/secrets") || r.url().includes("/api/registry")) &&
        (r.request().method() === "PUT" || r.request().method() === "POST"),
    );
    await page.getByTestId("save-hub-token").click();
    const tokRes = await pTok;

    const gotApi = (await api("GET", "/api/settings")).json;
    const gotApiRaw = (await api("GET", "/api/settings")).text;
    const synced = stateSync();
    const cliSecrets = grainCli(["secrets", "list"]);
    const domNext = await page.content();

    await page.goto(`${DAEMON}/settings/registry`);
    await waitClassicLoaded(page, 'input[name="registryUrl"]');
    const cUrl = await page.locator('input[name="registryUrl"]').inputValue();
    const cName = await page.locator('input[name="registryDisplayName"]').inputValue();
    const cPurp = await page.locator('input[name="registryPurpose"]').inputValue();
    const cAud = await page.locator('input[name="registryOidcAudience"]').inputValue();
    const domClassic = await page.content();

    const secretLeaked =
      gotApiRaw.includes(HUB_SECRET) ||
      synced.fullDump.includes(HUB_SECRET) ||
      cliSecrets.includes(HUB_SECRET) ||
      domNext.includes(HUB_SECRET) ||
      domClassic.includes(HUB_SECRET);

    record(
      "hub",
      "next->classic/state-repo parity & hub token never echoed",
      hubSave.status === 200 &&
        tokRes.status() === 200 &&
        gotApi.registry?.url === "https://hub.walk.example" &&
        gotApi.registry?.authMode === "oidc" &&
        gotApi.registry?.oidcAudience === "https://hub.walk.example/aud" &&
        gotApi.registry?.hostKind === "kubernetes" &&
        gotApi.registry?.displayName === "Walk PG Grain" &&
        gotApi.registryTokenSet === true &&
        cUrl === "https://hub.walk.example" &&
        cName === "Walk PG Grain" &&
        cPurp === "Section 9 parity" &&
        cAud === "https://hub.walk.example/aud" &&
        !secretLeaked,
      {
        hubSaveStatus: hubSave.status,
        tokStatus: tokRes.status(),
        registry: gotApi.registry,
        registryTokenSet: gotApi.registryTokenSet,
        classic: { cUrl, cName, cPurp, cAud },
        secretLeaked,
      },
    );

    // Reverse: edit displayName in classic -> read back in /next/settings/registry
    const cNameBox = page.locator('input[name="registryDisplayName"]');
    const pHubRev = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") &&
        (r.request().method() === "PUT" || r.request().method() === "POST"),
    );
    await cNameBox.fill("Classic Hub Edit");
    await cNameBox.blur();
    await pHubRev;
    await page.goto(`${DAEMON}/next/settings/registry`);
    await page.getByTestId("input-hub-display-name").waitFor({ state: "visible" });
    const nNameRev = await page.getByTestId("input-hub-display-name").inputValue();
    record("hub", "reverse classic->next parity", nNameRev === "Classic Hub Edit", { nNameRev });

    // Clean up hub token (through confirm dialog) and hub URL so daemon doesn't spam heartbeats
    await page.getByTestId("clear-hub-token").click();
    const clearTokConfirm = page.getByRole("button", { name: "Clear token" });
    await clearTokConfirm.waitFor({ state: "visible", timeout: 5000 });
    await clearTokConfirm.click();
    await page.waitForTimeout(400);
    await api("POST", "/api/settings", {
      registry: { url: "", listed: true, authMode: "token", hostKind: "", oidcAudience: "", displayName: "", purpose: "", scopes: [] },
    });
  }

  // =========================================================================
  // 5. CAPABILITIES (/next/settings/capabilities): secrets, creds, scope sets, real task!
  // =========================================================================
  {
    const SEC_VAL = "super_secret_walk_val_999_never_echo";
    const CLI_SEC_VAL = "cli_secret_val_777_never_echo";

    await page.goto(`${DAEMON}/next/settings/capabilities#other-secrets`);
    await page.getByTestId("input-secret-name").waitFor({ state: "visible" });
    await page.getByTestId("input-secret-name").fill("walk-parity-sec");
    await page.getByTestId("input-secret-key").fill("token");
    await page.getByTestId("input-secret-value").fill(SEC_VAL);
    const pSec = page.waitForResponse(
      (r) => r.url().includes("/api/secrets") && r.request().method() === "PUT",
    );
    await page.getByTestId("store-secret-btn").click();
    const secRes = await pSec;
    await page.getByTestId("secret-row-walk-parity-sec").waitFor({ state: "visible" });

    // Check API, CLI, state-repo, DOM, classic for presence + no echo!
    const secApi = await api("GET", "/api/secrets");
    const cliSecList = grainCli(["secrets", "list"]);
    const synced = stateSync();
    const domNext = await page.content();

    await page.goto(`${DAEMON}/settings/capabilities`);
    await waitClassicLoaded(page);
    const domClassic = await page.content();

    const leaked =
      secApi.text.includes(SEC_VAL) ||
      cliSecList.includes(SEC_VAL) ||
      synced.fullDump.includes(SEC_VAL) ||
      domNext.includes(SEC_VAL) ||
      domClassic.includes(SEC_VAL);

    record(
      "capabilities-secrets",
      "next secret store -> api/cli/classic/state-repo parity & never echoed",
      secRes.status() === 200 &&
        cliSecList.includes("walk-parity-sec") &&
        cliSecList.includes("token") &&
        domClassic.includes("walk-parity-sec") &&
        !leaked,
      {
        putStatus: secRes.status(),
        cliSecList: cliSecList.trim(),
        classicSeesName: domClassic.includes("walk-parity-sec"),
        leaked,
      },
    );

    // Reverse: CLI `grain secrets set` -> read back in /next/settings/capabilities
    grainCli(["secrets", "set", "walk-cli-sec", "api_key"], CLI_SEC_VAL);
    await page.goto(`${DAEMON}/next/settings/capabilities#other-secrets`);
    await page.getByTestId("secret-row-walk-cli-sec").waitFor({ state: "visible" });
    const cliSecRowText = await page.getByTestId("secret-row-walk-cli-sec").textContent();
    const cliLeaked = (await page.content()).includes(CLI_SEC_VAL);
    record(
      "capabilities-secrets",
      "reverse cli->next secret visibility & never echoed",
      cliSecRowText?.includes("api_key") && !cliLeaked,
      { cliSecRowText: cliSecRowText?.trim(), cliLeaked },
    );

    // Deployment credentials (#credentials): save gcpProject + count, diff classic/cli/state-repo
    await page.goto(`${DAEMON}/next/settings/capabilities#credentials`);
    await page.getByTestId("input-credentials-gcp-project").waitFor({ state: "visible" });
    await page.getByTestId("input-credentials-gcp-project").fill("walk-gcp-proj");
    await page.getByTestId("input-credentials-gcp-count").fill("3");
    const credSave = await clickSaveNext(page);
    const credApi = (await api("GET", "/api/settings")).json;
    const credSync = stateSync();
    await page.goto(`${DAEMON}/settings/credentials`);
    await waitClassicLoaded(page, 'input[name="gcpProject"]');
    const cGcpProj = await page.locator('input[name="gcpProject"]').inputValue();
    record(
      "capabilities-credentials",
      "next->classic/api/state-repo parity",
      credSave.status === 200 &&
        credApi.gcpProject === "walk-gcp-proj" &&
        credApi.gcpServiceAccountCount === 3 &&
        cGcpProj === "walk-gcp-proj" &&
        credSync.grainConfig.gcp_project === "walk-gcp-proj",
      {
        credSaveStatus: credSave.status,
        api: { gcpProject: credApi.gcpProject, count: credApi.gcpServiceAccountCount },
        cGcpProj,
        stateRepo: credSync.grainConfig.gcp_project,
      },
    );
    // Reset gcpProject back to empty
    await api("POST", "/api/settings", { gcpProject: "", gcpServiceAccountCount: 1 });

    // API scope sets (#api-scope-sets): create walk/reader, diff classic/cli/state-repo, then dispatch real task!
    await page.goto(`${DAEMON}/next/settings/capabilities#api-scope-sets`);
    await page.getByTestId("input-scope-set-name").waitFor({ state: "visible" });
    await page.getByTestId("input-scope-set-name").fill("walk/reader");
    await page.getByTestId("input-scope-set-scopes").fill("tasks:read, repos:read");
    const pSet = page.waitForResponse(
      (r) => r.url().includes("/api/api-scope-sets") && r.request().method() === "PUT",
    );
    await page.getByTestId("save-scope-set-btn").click();
    const setRes = await pSet;
    await page.getByTestId("scope-set-row-walk/reader").waitFor({ state: "visible" });

    const ssApi = (await api("GET", "/api/api-scope-sets")).json;
    const ssCli = grainCli(["config"]);
    const ssSync = stateSync();
    await page.goto(`${DAEMON}/settings/capabilities`);
    await waitClassicLoaded(page);
    const classicSeesSet = (await page.content()).includes("walk/reader");
    const syncedSet = ssSync.apiScopeSets.find((s) => s.name === "walk/reader");

    record(
      "capabilities-scope-sets",
      "next->classic/cli/state-repo parity",
      setRes.status() === 200 &&
        classicSeesSet &&
        ssCli.includes("walk/reader") &&
        Boolean(syncedSet),
      {
        putStatus: setRes.status(),
        classicSeesSet,
        cliHasSet: ssCli.includes("walk/reader"),
        syncedSet,
        apiCount: Array.isArray(ssApi) ? ssApi.length : ssApi?.scopeSets?.length,
      },
    );

    // File and dispatch a REAL task using the walk/reader API scope set!
    // First seed action-default.json so fake-agy runs grain api routes + GET /api/repos + POST /api/tasks (expect forbidden)
    const actionObj = {
      kind: "mcp_sequence",
      calls: [
        {
          tool: "run_command",
          args: {
            command: [
              "echo '=== ROUTES ==='",
              "grain api routes 2>&1 || true",
              "echo '=== GET_REPO_SETTINGS ==='",
              "grain api GET /api/repos/acme/widgets/settings 2>&1 || true",
              "echo '=== POST_TASKS ==='",
              "grain api POST /api/tasks --body '{\"title\":\"should fail\",\"repo\":\"acme/widgets\"}' 2>&1 || true",
            ].join("\n"),
            description: "probe custom API scope set walk/reader from sandbox",
          },
        },
        {
          tool: "comment_on_issue",
          args: { comment: "scope-set probe complete" },
        },
      ],
    };
    fs.writeFileSync(
      path.join(WORK_DIR, "action-default.json"),
      JSON.stringify(actionObj, null, 2),
    );
    const taskResp = await api("POST", "/api/tasks", {
      title: "Verify custom API scope set walk/reader",
      description: "Dispatched by section 9 item 1 walkthrough to verify walk/reader scope set.",
      repo: "acme/widgets",
      grants: ["https://grain.dev/capabilities/api#walk/reader"],
    });
    let scopeProbeOk = false;
    let scopeProbeDetail = { createStatus: taskResp.status, createBody: taskResp.text };
    if (taskResp.ok && taskResp.json?.id) {
      const tid = taskResp.json.id;
      await api("POST", `/api/tasks/${tid}/approve`);
      let taskState = "";
      for (let i = 0; i < 90; i++) {
        const cur = (await api("GET", `/api/tasks/${tid}`)).json;
        taskState = cur?.state || "";
        if (["done", "closed", "failed", "cancelled", "completed", "awaiting_reply"].includes(taskState)) break;
        await new Promise((r) => setTimeout(r, 500));
      }
      const mcpLogPath = path.join(WORK_DIR, "runs", `mcp-${tid}.jsonl`);
      let runOut = "";
      if (fs.existsSync(mcpLogPath)) {
        runOut = fs.readFileSync(mcpLogPath, "utf8");
      }
      const hasReposGet =
        runOut.includes("GET    /api/repos/{owner}/{name}/settings") &&
        runOut.includes("GET_REPO_SETTINGS") &&
        !runOut.includes("GET_REPO_SETTINGS\\ngrain: {\\\"error\\\"");
      const blockedPostTasks =
        runOut.includes("POST_TASKS") &&
        runOut.includes("holds no api grant for scope tasks:write");
      scopeProbeOk = hasReposGet && blockedPostTasks;
      scopeProbeDetail = {
        tid,
        taskState,
        hasReposGet,
        blockedPostTasks,
        outputSnippet: runOut.slice(0, 1200),
      };
    }
    record(
      "capabilities-scope-sets",
      "real dispatched task enforces custom API scope set walk/reader",
      scopeProbeOk,
      scopeProbeDetail,
    );
  }

  // =========================================================================
  // 6. MODELS / CONNECTIONS + PR #1587/#1602 CHECKS
  // =========================================================================
  {
    const CONN_SECRET = "sk-ant-walk-secret-never-echo-4455";
    await api("DELETE", "/api/deployment/agent-backends/walk-conn-1").catch(() => {});
    await page.goto(`${DAEMON}/next/settings/models`);
    await page.getByTestId("input-new-backend-id").waitFor({ state: "visible" });
    await page.getByTestId("input-new-backend-id").fill("walk-conn-1");
    await page.getByTestId("input-new-backend-name").fill("Walkthrough Claude Conn");
    await page.getByTestId("select-new-backend-fw").click();
    await page.getByTestId("select-new-backend-fw-opt-claude").click();
    await page.getByTestId("select-new-backend-auth").click();
    await page.getByTestId("select-new-backend-auth-opt-api_key").click();
    await page.getByTestId("input-new-backend-models").fill("claude-walk-1");
    const pPutBackend = page.waitForResponse(
      (r) =>
        r.url().includes("/api/deployment/agent-backends/walk-conn-1") &&
        r.request().method() === "PUT",
    );
    await page.getByTestId("add-model-backend").click();
    await pPutBackend;

    // Open credential editor and set connection credential on walk-conn-1
    await page.getByTestId("toggle-cred-walk-conn-1").waitFor({ state: "visible" });
    await page.getByTestId("toggle-cred-walk-conn-1").click();
    const credRow = page.getByTestId("backend-cred-row-walk-conn-1");
    await credRow.waitFor({ state: "visible" });
    await credRow.locator('input[type="password"], input[type="text"]').first().fill(CONN_SECRET);
    const pCred = page.waitForResponse(
      (r) =>
        r.url().includes("/api/agent-backends/walk-conn-1/credential") &&
        r.request().method() === "PUT",
    );
    await credRow.locator('button[type="submit"]').click();
    const credRes = await pCred;

    const hasBackendSecret = async () => {
      const secs = (await api("GET", "/api/secrets")).json?.secrets || [];
      return secs.some((s) => s.name === "agent-backend-walk-conn-1");
    };
    const secretSetInitial = await hasBackendSecret();
    const settingsRaw = (await api("GET", "/api/settings")).text;
    const synced = stateSync();
    const domNext = await page.content();

    // Check classic /settings/models sees connection AND never echoes secret
    await page.goto(`${DAEMON}/settings/models`);
    await waitClassicLoaded(page, 'text="Walkthrough Claude Conn"');
    const domClassic = await page.content();
    const connSecretLeaked =
      settingsRaw.includes(CONN_SECRET) ||
      synced.fullDump.includes(CONN_SECRET) ||
      domNext.includes(CONN_SECRET) ||
      domClassic.includes(CONN_SECRET);

    record(
      "models-connections",
      "next connection + secret -> classic/state-repo parity & never echoed",
      credRes.status() === 200 &&
        secretSetInitial &&
        domClassic.includes("Walkthrough Claude Conn") &&
        !connSecretLeaked,
      {
        credPutStatus: credRes.status(),
        secretSetInitial,
        classicSeesConn: domClassic.includes("Walkthrough Claude Conn"),
        connSecretLeaked,
      },
    );

    // PR #1602: confirm before clearing connection secret in CLASSIC UI
    const edits = page.getByRole("button", { name: /^edit$/i });
    const editIdx = (await edits.count()) - 1;
    await edits.nth(editIdx).click();
    const classicClearBtn = page.getByRole("button", { name: /clear credential/i });
    await classicClearBtn.waitFor({ state: "visible", timeout: 10000 });
    let dialogMsg = "";
    page.once("dialog", async (d) => {
      dialogMsg = d.message();
      await d.dismiss();
    });
    await classicClearBtn.click();
    await page.waitForTimeout(300);
    const stillSetAfterClassicCancel = await hasBackendSecret();
    record(
      "models-connections",
      "PR #1602 confirm before clearing connection secret in classic (cancel keeps secret)",
      dialogMsg.includes("Clear the credential for walk-conn-1") && stillSetAfterClassicCancel,
      { dialogMsg, stillSetAfterClassicCancel },
    );

    // PR #1602: confirm before clearing connection secret in /next/settings/models (cancel keeps, confirm clears)
    await page.goto(`${DAEMON}/next/settings/models`);
    await page.getByTestId("toggle-cred-walk-conn-1").waitFor({ state: "visible" });
    await page.getByTestId("toggle-cred-walk-conn-1").click();
    const nextCredRow = page.getByTestId("backend-cred-row-walk-conn-1");
    await nextCredRow.waitFor({ state: "visible" });
    await page.getByTestId("clear-cred-walk-conn-1").click();
    const confirmDialog = page.getByRole("dialog");
    await confirmDialog.waitFor({ state: "visible", timeout: 5000 });
    const hasTwoStep = (await confirmDialog.textContent())?.includes("Clear the credential for walk-conn-1");
    await confirmDialog.getByRole("button", { name: "Cancel" }).click();
    const stillSetAfterCancel = await hasBackendSecret();
    // Now actually confirm clear
    await page.getByTestId("clear-cred-walk-conn-1").click();
    await confirmDialog.waitFor({ state: "visible", timeout: 5000 });
    const pDelCred = page.waitForResponse(
      (r) =>
        r.url().includes("/api/agent-backends/walk-conn-1/credential") &&
        r.request().method() === "DELETE",
    );
    await confirmDialog.getByRole("button", { name: "Clear secret" }).click();
    const delCredRes = await pDelCred;
    const stillSetAfterConfirm = await hasBackendSecret();
    record(
      "models-connections",
      "PR #1602 confirm before clearing connection secret in /next/ (cancel keeps, confirm clears)",
      Boolean(hasTwoStep) &&
        stillSetAfterCancel === true &&
        delCredRes.status() === 200 &&
        stillSetAfterConfirm === false,
      { hasTwoStep, stillSetAfterCancel, delStatus: delCredRes.status(), stillSetAfterConfirm },
    );

    // PR #1602: Refresh button visible with access and hidden without access
    const refreshBtn = page.getByRole("button", { name: /refresh models/i }).first();
    const refreshVisibleAdmin = await refreshBtn.isVisible().catch(() => false);
    const noAccessPage = await context.newPage();
    await noAccessPage.route("**/api/auth/me", async (route) => {
      const resp = await route.fetch();
      const json = await resp.json();
      if (json.permissions) {
        delete json.permissions["POST /api/agent-backends/refresh"];
        delete json.permissions["POST /api/deployment/agent-backends/{backendId}/refresh"];
      }
      if (Array.isArray(json.allowedRoutes)) {
        json.allowedRoutes = json.allowedRoutes.filter((r) => !r.includes("refresh"));
      }
      await route.fulfill({ response: resp, json });
    });
    await noAccessPage.goto(`${DAEMON}/next/settings/models`);
    await noAccessPage.getByTestId("card-model-backends").waitFor({ state: "visible" });
    await noAccessPage.waitForTimeout(400);
    const refreshCountWithoutAccess = await noAccessPage
      .locator('button:has-text("Refresh models"), [data-testid*="refresh-models"]')
      .count();
    await noAccessPage.close();
    record(
      "models-connections",
      "PR #1602 Refresh button hidden without access",
      refreshVisibleAdmin && refreshCountWithoutAccess === 0,
      { refreshVisibleAdmin, refreshCountWithoutAccess },
    );

    // PR #1587: Customize navigation applies to every open tab
    const page2 = await context.newPage();
    await page.goto(`${DAEMON}/next/inbox`);
    await page2.goto(`${DAEMON}/next/inbox`);
    await page2.getByTestId("rail-ci").waitFor({ state: "visible", timeout: 10000 });
    await page.getByTestId("rail-more").click();
    await page.getByTestId("rail-customize-nav").click();
    const navDialog = page.getByRole("dialog", { name: "Customize navigation" });
    await navDialog.waitFor({ state: "visible", timeout: 5000 });
    const ciSwitch = navDialog.getByRole("switch", { name: "Show CI on the rail" });
    await ciSwitch.click();
    await navDialog.getByRole("button", { name: "Save" }).click();
    await page2.getByTestId("rail-ci").waitFor({ state: "hidden", timeout: 5000 }).catch(() => {});
    const ciHiddenInTab2 = !(await page2.getByTestId("rail-ci").isVisible());
    // Restore CI on the rail
    await page.getByTestId("rail-more").click();
    await page.getByTestId("rail-customize-nav").click();
    await navDialog.waitFor({ state: "visible", timeout: 5000 });
    await navDialog.getByRole("switch", { name: "Show CI on the rail" }).click();
    await navDialog.getByRole("button", { name: "Save" }).click();
    await page2.getByTestId("rail-ci").waitFor({ state: "visible", timeout: 5000 }).catch(() => {});
    const ciRestoredInTab2 = await page2.getByTestId("rail-ci").isVisible();
    await page2.close();
    record("pr1587", "Customize navigation applies to second open tab", ciHiddenInTab2 && ciRestoredInTab2, {
      ciHiddenInTab2,
      ciRestoredInTab2,
    });

    // PR #1587: Session settings save on the session between runs (no open taskId)
    const sessList = (await api("GET", "/api/console-sessions")).json?.sessions || [];
    let idleSess = sessList.find((s) => !s.taskId);
    if (!idleSess) {
      const cr = await api("POST", "/api/console-sessions", {
        title: "Walkthrough idle session 2",
        repo: "acme/widgets",
      });
      const created = (cr.json?.sessions || []).find((s) => s.title === "Walkthrough idle session 2");
      if (created?.taskId) {
        await api("POST", `/api/tasks/${created.taskId}/close`, { reason: "park between runs" });
      }
      idleSess = { id: created?.id };
    }
    let sessSettingsOk = false;
    let sessDetail = { idleSess };
    if (idleSess?.id) {
      const sid = idleSess.id;
      await page.goto(`${DAEMON}/next/sessions/${sid}`);
      await page.getByTestId("session-settings-toggle").waitFor({ state: "visible", timeout: 10000 });
      await page.getByTestId("session-settings-toggle").click();
      const readsAll = page.getByTestId("session-reads-all");
      await readsAll.waitFor({ state: "visible", timeout: 5000 });
      const initialChecked = await readsAll.isChecked();
      let hitSettingsPost = false;
      const onReq = (req) => {
        if (req.url().endsWith("/api/settings") && req.method() === "POST") hitSettingsPost = true;
      };
      page.on("request", onReq);
      const pPutSess = page.waitForResponse(
        (r) =>
          r.url().includes(`/api/console-sessions/${sid}`) &&
          r.request().method() === "PUT",
      );
      await readsAll.click();
      const putRes = await pPutSess;
      page.off("request", onReq);
      await page.reload();
      await page.getByTestId("session-settings-toggle").click();
      const afterReloadChecked = await page.getByTestId("session-reads-all").isChecked();
      sessSettingsOk =
        putRes.status() === 200 &&
        !hitSettingsPost &&
        afterReloadChecked === !initialChecked;
      sessDetail = {
        sid,
        putStatus: putRes.status(),
        hitSettingsPost,
        initialChecked,
        afterReloadChecked,
      };
    }
    record(
      "pr1587",
      "session settings save on session between runs (PUT /api/console-sessions/{id})",
      sessSettingsOk,
      sessDetail,
    );
  }

  record("runtime", "no browser pageerror or console.error", consoleErrors.length === 0, {
    count: consoleErrors.length,
    errors: consoleErrors.slice(0, 10),
  });

  await browser.close();

  fs.mkdirSync(path.join(WORK_DIR, "results"), { recursive: true });
  fs.writeFileSync(
    path.join(WORK_DIR, "results/item1.json"),
    JSON.stringify({ findings, consoleErrors }, null, 2),
  );
  const failed = findings.filter((f) => !f.ok);
  console.log(`\nItem 1 summary: ${findings.length - failed.length}/${findings.length} passed, ${failed.length} failed.`);
  if (failed.length > 0) {
    console.log("Failed checks:");
    for (const f of failed) {
      console.log(`  - ${f.area} :: ${f.check}`);
    }
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
