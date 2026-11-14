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

// End-to-end live-daemon UI walkthrough for docs/test-plans/2026-11-13-afternoon.md §5:
//   1. /next/processes/suites (and classic /processes) with pass: 0, tasks: null
//   2. /next/ New-task sheet missing-field validation & viewer gating, plus
//      real-browser touch/hover check of classic NewTaskOverlay's [data-create-blocked]
//   3. /next/tasks batch Close confirmation (1 vs 2+ tasks, Keep, Escape, confirm),
//      simulated partial failure (1 of 2 tasks HTTP 500), and task(s) pluralization
//      across processes-page, settings-instance, and settings-code.

import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import { createRequire } from "node:module";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../../..");
const require = createRequire(path.join(repoRoot, "ui-ng/package.json"));
const { chromium } = require("@playwright/test");
const AxeBuilder = require("@axe-core/playwright").default;

const modeArg =
  process.argv.find((a) => a.startsWith("--mode="))?.slice(7) || "after";
const outJsonArg =
  process.argv.find((a) => a.startsWith("--out-json="))?.slice(11) || "";
const shotsDirArg =
  process.argv.find((a) => a.startsWith("--shots="))?.slice(8) || "";
const grainBin = process.env.GRAIN_BIN || "/home/debian/bin/grain";

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.unref();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      srv.close(() => resolve(port));
    });
  });
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

async function waitHttp(url, headers = {}, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(url, { headers });
      if (r.ok) return;
    } catch {
      // retry
    }
    await sleep(150);
  }
  throw new Error(`timed out waiting for ${url}`);
}

async function apiCall(baseUrl, code, method, urlPath, body) {
  const headers = { Authorization: `Bearer ${code}` };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(`${baseUrl}${urlPath}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = text;
  }
  return { status: res.status, body: json };
}

function startFaultProxy(targetPort, shouldFail) {
  return new Promise((resolve, reject) => {
    const srv = http.createServer((req, res) => {
      const fail = shouldFail(req);
      if (fail) {
        let _body = "";
        req.on("data", (c) => (_body += c));
        req.on("end", () => {
          res.writeHead(fail.status, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ error: fail.message }));
        });
        return;
      }
      const upstream = http.request(
        {
          hostname: "127.0.0.1",
          port: targetPort,
          path: req.url,
          method: req.method,
          headers: req.headers,
        },
        (uRes) => {
          res.writeHead(uRes.statusCode || 502, uRes.headers);
          uRes.pipe(res);
        },
      );
      upstream.on("error", (err) => {
        res.writeHead(502, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: String(err) }));
      });
      req.pipe(upstream);
    });
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      resolve({ server: srv, port, url: `http://127.0.0.1:${port}` });
    });
  });
}

async function apiMust(baseUrl, code, method, urlPath, body) {
  const res = await apiCall(baseUrl, code, method, urlPath, body);
  if (res.status < 200 || res.status >= 300) {
    throw new Error(
      `${method} ${urlPath} -> ${res.status}: ${JSON.stringify(res.body)}`,
    );
  }
  return res.body;
}

async function saveShot(page, name) {
  if (!shotsDirArg) return;
  fs.mkdirSync(shotsDirArg, { recursive: true });
  await page.screenshot({ path: path.join(shotsDirArg, `${name}.png`) });
}

async function setTheme(page, theme) {
  await page.evaluate((t) => {
    try {
      localStorage.setItem("grain.themeMode", t);
    } catch {
      // ignore before navigation
    }
  }, theme);
}

async function severeAxe(page) {
  const { violations } = await new AxeBuilder({ page }).analyze();
  return violations
    .filter((v) => v.impact === "serious" || v.impact === "critical")
    .map((v) => ({ id: v.id, impact: v.impact, nodes: v.nodes.length }));
}

async function main() {
  const workDir = fs.mkdtempSync(path.join(os.tmpdir(), "grain-walk-5487-"));
  const procs = [];
  let faultProxy = null;
  let browser = null;
  const report = {
    mode: modeArg,
    grainVersion: execFileSync(grainBin, ["version"], {
      encoding: "utf8",
    }).trim(),
    item1: {},
    item2: {},
    item3: {},
    consoleErrors: [],
    pageErrors: [],
  };

  try {
    for (const sub of ["data", "sandboxes", "bin", "home", "bares"]) {
      fs.mkdirSync(path.join(workDir, sub), { recursive: true });
    }
    fs.chmodSync(path.join(here, "fake-agy.py"), 0o755);

    const ghsimBin = path.join(workDir, "bin/ghsim");
    const iapsimBin = path.join(workDir, "bin/iapsim");
    execFileSync("go", ["build", "-o", ghsimBin, path.join(here, "ghsim.go")], {
      cwd: repoRoot,
    });
    execFileSync("go", ["build", "-o", iapsimBin, "tests/iap/iapsim.go"], {
      cwd: repoRoot,
    });

    for (const r of ["widgets", "api", "docs"]) {
      const bare = path.join(workDir, "bares", `${r}.git`);
      execFileSync("git", ["init", "-q", "--bare", "-b", "main", bare]);
      execFileSync("git", ["-C", bare, "config", "http.receivepack", "true"]);
      const tmp = fs.mkdtempSync(path.join(workDir, "seedgit-"));
      execFileSync("git", ["-C", tmp, "init", "-q", "-b", "main"]);
      fs.mkdirSync(path.join(tmp, ".grain/workflows"), { recursive: true });
      fs.writeFileSync(path.join(tmp, "README.md"), `# acme/${r}\n`);
      fs.writeFileSync(
        path.join(tmp, ".grain/workflows/ci.yml"),
        "name: ci\non: [push]\njobs:\n  unit:\n    runs-on: linux\n    steps:\n      - name: unit\n        run: echo ok\n",
      );
      execFileSync("git", ["-C", tmp, "add", "."]);
      execFileSync("git", [
        "-C",
        tmp,
        "-c",
        "user.name=Seed",
        "-c",
        "user.email=seed@example.com",
        "commit",
        "-qm",
        "chore: seed",
      ]);
      execFileSync("git", ["-C", tmp, "push", "-q", bare, "main"]);
      fs.rmSync(tmp, { recursive: true, force: true });
    }

    const ghPort = await freePort();
    const iapIssuerPort = await freePort();
    const iapProxyPort = await freePort();
    const daemonPort = await freePort();
    const daemonUrl = `http://127.0.0.1:${daemonPort}`;
    const iapProxyUrl = `http://127.0.0.1:${iapProxyPort}`;
    const iapAud = "/projects/123456/global/backendServices/789012";
    const iapIss = "https://iap.test.local";

    const botTokenFile = path.join(workDir, "bot.token");
    const modelKeyFile = path.join(workDir, "model.key");
    fs.writeFileSync(botTokenFile, "ghp_walkthrough\n");
    fs.writeFileSync(modelKeyFile, "walkthrough-key\n");
    fs.writeFileSync(
      path.join(workDir, "data/state-repo.json"),
      JSON.stringify({ directConfigWrites: true }),
    );
    const seedGitYaml = path.join(workDir, "seed-git.yaml");
    fs.writeFileSync(
      seedGitYaml,
      `git:\n  - credential: bot\n    token_file: ${botTokenFile}\n`,
    );
    const seedModelsYaml = path.join(workDir, "seed-models.yaml");
    fs.writeFileSync(
      seedModelsYaml,
      `git:\n  - credential: bot\n    token_file: ${botTokenFile}\nmodels:\n  - id: primary-agy\n    framework: antigravity\n    models: [claude-sonnet-4-6, gemini-2.5-pro]\n    credential_file: ${modelKeyFile}\ndefault_model: claude-sonnet-4-6\n`,
    );
    execFileSync(grainBin, [
      "connections",
      "-data-dir",
      path.join(workDir, "data"),
      "import",
      seedGitYaml,
    ]);

    procs.push(
      spawn(
        ghsimBin,
        [
          "-addr",
          `127.0.0.1:${ghPort}`,
          "-bares-dir",
          path.join(workDir, "bares"),
          "-repos",
          "widgets,api,docs",
          "-token",
          "ghp_walkthrough",
          "-default",
          "main",
        ],
        { stdio: "ignore" },
      ),
    );
    await waitHttp(`http://127.0.0.1:${ghPort}/repos/acme/widgets`);

    let failCloseTaskId = null;
    faultProxy = await startFaultProxy(daemonPort, (req) => {
      if (
        failCloseTaskId &&
        req.method === "POST" &&
        req.url === `/api/tasks/${failCloseTaskId}/close`
      ) {
        return { status: 500, message: "simulated upstream close failure" };
      }
      return null;
    });

    procs.push(
      spawn(
        iapsimBin,
        [
          "-issuer",
          `127.0.0.1:${iapIssuerPort}`,
          "-proxy",
          `127.0.0.1:${iapProxyPort}`,
          "-upstream",
          faultProxy.url,
          "-aud",
          iapAud,
          "-iss",
          iapIss,
        ],
        { stdio: "ignore" },
      ),
    );
    await waitHttp(`http://127.0.0.1:${iapIssuerPort}/jwks`);

    // Deliberately omit -default-target-repo so defaultTarget is "" on the daemon!
    procs.push(
      spawn(
        grainBin,
        [
          "daemon",
          "-data-dir",
          path.join(workDir, "data"),
          "-sandbox-dir",
          path.join(workDir, "sandboxes"),
          "-host-sandboxes",
          "-ui-addr",
          `127.0.0.1:${daemonPort}`,
          "-sandbox-port-base=0",
          "-as",
          "alice@example.com",
          "-deployment-owner",
          "alice@example.com",
          "-ui-iap-audience",
          iapAud,
          "-ui-iap-issuer",
          iapIss,
          "-ui-iap-keys-url",
          `http://127.0.0.1:${iapIssuerPort}/jwks`,
          "-poll-interval",
          "1s",
          "-max-workers",
          "2",
          "-max-mergers",
          "0",
          "-agy-path",
          path.join(here, "fake-agy.py"),
          "-github-host",
          `127.0.0.1:${ghPort}`,
          "-github-insecure-http",
        ],
        {
          env: {
            PATH: process.env.PATH || "/usr/local/bin:/usr/bin:/bin",
            HOME: path.join(workDir, "home"),
            PYTHONDONTWRITEBYTECODE: "1",
            GCE_METADATA_HOST: "127.0.0.1:9",
          },
          stdio: "ignore",
        },
      ),
    );

    const tokenPath = path.join(workDir, "data/local-api-token");
    let code = "";
    const deadline = Date.now() + 30_000;
    while (Date.now() < deadline) {
      if (fs.existsSync(tokenPath)) {
        code = fs.readFileSync(tokenPath, "utf8").trim();
        if (code) {
          try {
            const r = await apiCall(daemonUrl, code, "GET", "/api/config");
            if (r.status === 200) break;
          } catch {
            // retry
          }
        }
      }
      await sleep(150);
    }
    if (!code) throw new Error("daemon did not come up");

    execFileSync(grainBin, [
      "connections",
      "-data-dir",
      path.join(workDir, "data"),
      "import",
      seedModelsYaml,
    ]);

    // Pause dispatch during initial seeding so suite tasks stay queued while SyncSuites still runs!
    await apiCall(daemonUrl, code, "POST", "/api/pipeline-pause", {
      paused: true,
      reason: "walkthrough seeding",
    });
    await apiCall(daemonUrl, code, "POST", "/api/repos", {
      repo: "acme/widgets",
    });
    await apiCall(daemonUrl, code, "POST", "/api/repos", { repo: "acme/api" });
    await apiMust(daemonUrl, code, "PUT", "/api/api-scope-sets", {
      set: {
        name: "ops/widgets-writer",
        description: "Write tasks in acme/widgets only",
        scopes: ["tasks:write"],
        repos: ["acme/widgets"],
      },
    });
    await apiMust(daemonUrl, code, "PUT", "/api/api-scope-sets", {
      set: {
        name: "ops/widgets-contributor",
        description: "Viewer plus tasks:write on acme/widgets",
        includes: ["grain/viewer", "ops/widgets-writer"],
      },
    });
    await apiMust(daemonUrl, code, "POST", "/api/access/principals", {
      entry: "user:viewer@example.com",
      role: "grain/viewer",
      reason: "viewer walkthrough check",
    });
    await apiMust(daemonUrl, code, "POST", "/api/access/principals", {
      entry: "user:contrib@example.com",
      role: "ops/widgets-contributor",
      reason: "repo-scoped contributor walkthrough check",
    });

    // =========================================================================
    // ITEM 1 SETUP: Real suite runs #1 (pass 1), #2 (pass 0, tasks: null via SyncSuites), #3 (pass 1)
    // =========================================================================
    const tmpl1 = await apiMust(daemonUrl, code, "POST", "/api/templates", {
      name: "Audit pass check",
      title: "Audit widgets pass",
      description: "Check widgets status",
    });
    const tmpl2 = await apiMust(daemonUrl, code, "POST", "/api/templates", {
      name: "Audit second check",
      title: "Audit api pass",
      description: "Check api status",
    });
    const suite1 = await apiMust(daemonUrl, code, "POST", "/api/suites", {
      name: "Nightly audit suite",
      mode: "count",
      count: 1,
      templateIds: [tmpl1.id],
    });
    const suite2 = await apiMust(daemonUrl, code, "POST", "/api/suites", {
      name: "Dual audit suite",
      mode: "count",
      count: 1,
      templateIds: [tmpl1.id, tmpl2.id],
    });

    // Run #1 (subsequent row in newest-first order)
    const sr1 = await apiMust(daemonUrl, code, "POST", "/api/suite-runs", {
      suiteId: suite1.id,
      repo: "acme/widgets",
      base: "main",
    });
    await sleep(1100);

    // Run #2: active suite_run with 0 tasks whose item template is missing -> live SyncSuites marks it failed with pass: 0, tasks: null!
    const dbPath = path.join(workDir, "data/store/grain.db");
    const nowIso = new Date().toISOString();
    execFileSync("sqlite3", [
      dbPath,
      `INSERT INTO suite_run (suite_id, suite_name, schedule_id, owner, repo, base, mode, count, max_passes, require_approval, auto_merge, auto_approve_proposals, status, created_at)
       VALUES ('${suite1.id}', 'Nightly audit suite', NULL, 'acme', 'widgets', 'main', 'count', 2, 0, 0, 0, 0, 'active', '${nowIso}');
       INSERT INTO suite_run_item (run_id, template_id, order_key)
       VALUES ((SELECT MAX(id) FROM suite_run), 'declared:template:acme:widgets:deleted-template', 0);`,
    ]);

    // Wait for live daemon SyncSuites (-poll-interval 1s) to transition Run #2 to failed with pass: 0, tasks: null!
    let sr2 = null;
    for (let i = 0; i < 40; i++) {
      const list = await apiMust(daemonUrl, code, "GET", "/api/suite-runs");
      const runs = Array.isArray(list) ? list : list?.suiteRuns || [];
      const found = runs.find((r) => r.id !== sr1.id);
      if (found && found.status === "failed") {
        sr2 = found;
        break;
      }
      await sleep(250);
    }
    if (!sr2)
      throw new Error("SyncSuites did not fail empty-pass suite run #2");

    await sleep(1100);
    // Run #3 (newest row, ahead of Run #2)
    const sr3 = await apiMust(daemonUrl, code, "POST", "/api/suite-runs", {
      suiteId: suite1.id,
      repo: "acme/widgets",
      base: "main",
    });

    const suiteRunsApi = (
      await apiCall(daemonUrl, code, "GET", "/api/suite-runs")
    ).body;
    const suiteRunsList = Array.isArray(suiteRunsApi)
      ? suiteRunsApi
      : suiteRunsApi?.suiteRuns || [];
    report.item1.apiRuns = suiteRunsList.map((r) => ({
      id: r.id,
      status: r.status,
      pass: r.pass,
      tasks: r.tasks,
      error: r.error,
    }));

    browser = await chromium.launch({ headless: true });

    const attachMonitors = (page, tag) => {
      page.on("pageerror", (err) =>
        report.pageErrors.push({ tag, error: err.stack || String(err) }),
      );
      page.on("console", (msg) => {
        if (msg.type() === "error") {
          report.consoleErrors.push({ tag, text: msg.text() });
        }
      });
    };

    // =========================================================================
    // ITEM 1 BROWSER WALK: /next/processes/suites at 1440x900 & 390x844, light & dark + classic /processes
    // =========================================================================
    report.item1.matrix = [];
    for (const vp of [
      { name: "desktop", width: 1440, height: 900 },
      { name: "phone", width: 390, height: 844 },
    ]) {
      for (const theme of ["light", "dark"]) {
        const ctx = await browser.newContext({
          viewport: { width: vp.width, height: vp.height },
          colorScheme: theme,
        });
        await ctx.addCookies([
          {
            name: "iapsim_email",
            value: "alice@example.com",
            url: iapProxyUrl,
          },
        ]);
        const page = await ctx.newPage();
        attachMonitors(page, `item1-${vp.name}-${theme}`);
        await page.addInitScript((t) => {
          localStorage.setItem("grain.themeMode", t);
        }, theme);

        await page.goto(`${iapProxyUrl}/next/processes/suites`, {
          waitUntil: "domcontentloaded",
        });
        await page.waitForSelector(".main-stage > *:not(router-outlet)");
        await page.getByTestId("suites-subview-runs").click();
        await page
          .getByTestId(`suite-run-row-${sr2.id}`)
          .waitFor({ state: "visible" });

        const row3Text = await page
          .getByTestId(`suite-run-row-${sr3.id}`)
          .innerText();
        const row2Text = await page
          .getByTestId(`suite-run-row-${sr2.id}`)
          .innerText();
        const row1Text = await page
          .getByTestId(`suite-run-row-${sr1.id}`)
          .innerText();

        await page.getByTestId(`suite-run-row-${sr2.id}`).click();
        await page
          .getByTestId("suite-run-inspector")
          .waitFor({ state: "visible" });
        const detail2Text = await page
          .getByTestId("suite-run-inspector")
          .innerText();
        const passCardsOnEmpty = await page
          .locator('[data-testid^="suite-pass-"]')
          .count();
        const overflowEmpty = await page.evaluate(
          () => document.documentElement.scrollWidth - window.innerWidth,
        );
        const axeViolations = await severeAxe(page);
        await saveShot(page, `suites-empty-pass-${vp.name}-${theme}`);

        // Close mobile drawer if on phone before clicking subsequent row #1
        const backBtn = page
          .getByTestId("suite-run-inspector")
          .getByRole("button", { name: /Back/ });
        if ((await backBtn.count()) > 0 && (await backBtn.isVisible())) {
          await backBtn.click();
        }

        await page.getByTestId(`suite-run-row-${sr1.id}`).click();
        await page
          .getByTestId("suite-run-inspector")
          .waitFor({ state: "visible" });
        const detail1Text = await page
          .getByTestId("suite-run-inspector")
          .innerText();
        const passCardsOnSubsequent = await page
          .locator('[data-testid^="suite-pass-"]')
          .count();
        const overflowSub = await page.evaluate(
          () => document.documentElement.scrollWidth - window.innerWidth,
        );

        report.item1.matrix.push({
          viewport: `${vp.width}x${vp.height}`,
          theme,
          row3Rendered: row3Text.includes(`#${sr3.id}`),
          row2Text: row2Text.replace(/\s+/g, " ").trim(),
          row2HasPass0: /pass\s*0/i.test(row2Text),
          row1SubsequentRendered:
            row1Text.includes(`#${sr1.id}`) && row1Text.includes("Pass 1"),
          detail2HasEndedWithoutPass: detail2Text.includes(
            "ended without filing a pass",
          ),
          detail2HasErrorBanner: detail2Text.includes(
            "pass 1 could not be filed",
          ),
          passCardsOnEmpty,
          detail1HasPass1: /pass\s*1/i.test(detail1Text),
          passCardsOnSubsequent,
          maxHorizontalOverflowPx: Math.max(overflowEmpty, overflowSub),
          axeViolations,
        });
        await ctx.close();
      }
    }

    // Brief check on classic /processes?kind=suite and /processes/suites/runs/2
    {
      const ctx = await browser.newContext({
        viewport: { width: 1440, height: 900 },
      });
      await ctx.addCookies([
        { name: "iapsim_email", value: "alice@example.com", url: iapProxyUrl },
      ]);
      const page = await ctx.newPage();
      attachMonitors(page, "item1-classic");
      await page.goto(`${iapProxyUrl}/processes/suites/runs/${sr2.id}`, {
        waitUntil: "domcontentloaded",
      });
      await page
        .getByText("This run ended without filing any tasks.")
        .waitFor({ state: "visible", timeout: 15_000 });
      const bodyText = await page.locator("body").innerText();
      report.item1.classicPane = {
        hasEndedWithoutTasks: bodyText.includes(
          "This run ended without filing any tasks.",
        ),
        hasPass0: /pass\s*0/i.test(bodyText),
      };
      await ctx.close();
    }

    // =========================================================================
    // ITEM 2: /next/ New-task sheet validation & viewer gating + classic touch check
    // =========================================================================
    report.item2.nextValidation = [];
    for (const vp of [
      { name: "desktop", width: 1440, height: 900 },
      { name: "phone", width: 390, height: 844 },
    ]) {
      const ctx = await browser.newContext({
        viewport: { width: vp.width, height: vp.height },
      });
      await ctx.addCookies([
        { name: "iapsim_email", value: "alice@example.com", url: iapProxyUrl },
      ]);
      const page = await ctx.newPage();
      attachMonitors(page, `item2-next-${vp.name}`);
      const postTaskCalls = [];
      page.on("request", (req) => {
        if (req.method() === "POST" && req.url().endsWith("/api/tasks")) {
          postTaskCalls.push(req.postDataJSON());
        }
      });

      // Open on /next/inbox via `c` shortcut
      await page.goto(`${iapProxyUrl}/next/inbox`, {
        waitUntil: "domcontentloaded",
      });
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page.locator("body").press("c");
      await page.getByTestId("new-task-sheet").waitFor({ state: "visible" });
      await sleep(250);

      // (a) Missing title and no template
      postTaskCalls.length = 0;
      await page.getByTestId("new-task-submit").click();
      await sleep(150);
      const errTitle = await page
        .getByTestId("new-task-error")
        .innerText()
        .catch(() => "");
      const titleAriaInvalid = await page
        .getByTestId("nt-title")
        .getAttribute("aria-invalid");
      const postAfterMissingTitle = postTaskCalls.length;

      // (b) Missing repository with No-repo unchecked (daemon has defaultTarget === "")
      await page.getByTestId("nt-title").fill(`Missing repo check ${vp.name}`);
      const initialRepoLabel = (
        await page.getByTestId("nt-repo").innerText()
      ).trim();
      postTaskCalls.length = 0;
      await page.getByTestId("new-task-submit").click();
      await sleep(300);
      const errMissingRepo = await page
        .getByTestId("new-task-error")
        .innerText()
        .catch(() => "");
      const repoAriaInvalid = await page
        .getByTestId("nt-repo")
        .getAttribute("aria-invalid");
      const postAfterMissingRepo = postTaskCalls.length;
      await saveShot(page, `new-task-missing-repo-${vp.name}`);

      // (c) Pick repo, tick custom branch with empty branch name
      await page.getByTestId("nt-repo").click();
      await page.getByTestId("nt-repo-opt-acme/widgets").click();
      await page.getByTestId("nt-new-branch").check();
      postTaskCalls.length = 0;
      await page.getByTestId("new-task-submit").click();
      await sleep(150);
      const errMissingBranch = await page
        .getByTestId("new-task-error")
        .innerText()
        .catch(() => "");
      const branchAriaInvalid = await page
        .getByTestId("nt-new-branch-name")
        .getAttribute("aria-invalid");
      const postAfterMissingBranch = postTaskCalls.length;

      // (d) Complete branch name and file cleanly; then open via FAB on /next/tasks and file No-repo
      await page
        .getByTestId("nt-new-branch-name")
        .fill(`feat/walk-5487-${vp.name}`);
      const createRespP = page.waitForResponse(
        (res) =>
          res.url().endsWith("/api/tasks") && res.request().method() === "POST",
      );
      await page.getByTestId("new-task-submit").click();
      const createResp = await createRespP;
      const createdJson = await createResp.json();
      await page.getByTestId("new-task-sheet").waitFor({ state: "detached" });
      if (createdJson?.id) {
        await apiCall(
          daemonUrl,
          code,
          "POST",
          `/api/tasks/${createdJson.id}/close`,
          {
            close_pull_request: false,
          },
        );
      }

      // Open via rail FAB (desktop) or mobile FAB (phone) on /next/tasks and file No-repo task
      await page.goto(`${iapProxyUrl}/next/tasks`, {
        waitUntil: "domcontentloaded",
      });
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      const fabTestId =
        vp.name === "desktop" ? "rail-new-task" : "phone-new-task";
      await page.getByTestId(fabTestId).click();
      await page.getByTestId("new-task-sheet").waitFor({ state: "visible" });
      await page.getByTestId("nt-title").fill(`No-repo walk ${vp.name}`);
      await page.getByTestId("nt-repo").click();
      await page.getByTestId("nt-repo-opt-__none__").click();
      const noRepoRespP = page.waitForResponse(
        (res) =>
          res.url().endsWith("/api/tasks") && res.request().method() === "POST",
      );
      await page.getByTestId("new-task-submit").click();
      const noRepoResp = await noRepoRespP;
      const noRepoJson = await noRepoResp.json();
      if (noRepoJson?.id) {
        await apiCall(
          daemonUrl,
          code,
          "POST",
          `/api/tasks/${noRepoJson.id}/close`,
          {
            close_pull_request: false,
          },
        );
      }

      report.item2.nextValidation.push({
        viewport: `${vp.width}x${vp.height}`,
        missingTitle: {
          error: errTitle.trim(),
          ariaInvalid: titleAriaInvalid,
          networkPosts: postAfterMissingTitle,
        },
        missingRepo: {
          initialRepoLabel,
          error: errMissingRepo.trim(),
          ariaInvalid: repoAriaInvalid,
          networkPosts: postAfterMissingRepo,
        },
        missingBranch: {
          error: errMissingBranch.trim(),
          ariaInvalid: branchAriaInvalid,
          networkPosts: postAfterMissingBranch,
        },
        filedBranchStatus: createResp.status(),
        filedNoRepoStatus: noRepoResp.status(),
      });
      await ctx.close();
    }

    // (e) Viewer & repo-scoped gating on /next/inbox & /next/tasks + classic /
    {
      // 1) Pure read-only viewer (grain/viewer): FAB disabled with gTip, `c` shortcut blocked
      const vCtx = await browser.newContext({
        viewport: { width: 1440, height: 900 },
      });
      await vCtx.addCookies([
        { name: "iapsim_email", value: "viewer@example.com", url: iapProxyUrl },
      ]);
      const vPage = await vCtx.newPage();
      attachMonitors(vPage, "item2-viewer");

      await vPage.goto(`${iapProxyUrl}/next/tasks`, {
        waitUntil: "domcontentloaded",
      });
      await vPage.waitForSelector(".main-stage > *:not(router-outlet)");
      const railFabDisabled = await vPage
        .getByTestId("rail-new-task")
        .isDisabled();
      await vPage.getByTestId("rail-new-task").hover({ force: true });
      await sleep(250);
      const railFabTip = await vPage
        .locator('[role="tooltip"]')
        .innerText()
        .catch(() => "");
      await vPage.locator("body").press("c");
      await sleep(200);
      const viewerSheetOpened =
        (await vPage.getByTestId("new-task-sheet").count()) > 0;

      // Classic viewer check: [data-create-blocked] yields to AccessGate
      await vPage.goto(`${iapProxyUrl}/new`, { waitUntil: "domcontentloaded" });
      await vPage
        .getByRole("button", { name: "Create task" })
        .waitFor({ state: "visible", timeout: 15_000 });
      const createBlockedCountOnViewer = await vPage
        .locator("[data-create-blocked]")
        .count();
      const classicCreateDisabled = await vPage
        .getByRole("button", { name: "Create task" })
        .isDisabled();
      await vCtx.close();

      // 2) Repo-scoped contributor (tasks:write on acme/widgets only): opens sheet, !canFile() on No-repo / acme/api
      const cCtx = await browser.newContext({
        viewport: { width: 1440, height: 900 },
      });
      await cCtx.addCookies([
        {
          name: "iapsim_email",
          value: "contrib@example.com",
          url: iapProxyUrl,
        },
      ]);
      const cPage = await cCtx.newPage();
      attachMonitors(cPage, "item2-contrib");
      const contribPosts = [];
      cPage.on("request", (req) => {
        if (req.method() === "POST" && req.url().endsWith("/api/tasks")) {
          contribPosts.push(req.postDataJSON());
        }
      });

      await cPage.goto(`${iapProxyUrl}/next/tasks`, {
        waitUntil: "domcontentloaded",
      });
      await cPage.waitForSelector(".main-stage > *:not(router-outlet)");
      await cPage.locator("body").press("c");
      await cPage.getByTestId("new-task-sheet").waitFor({ state: "visible" });
      await sleep(250);
      // Select acme/api (not allowed for contrib@example.com)
      await cPage.getByTestId("nt-repo").click();
      await cPage.getByTestId("nt-repo-opt-acme/api").click();
      await cPage.getByTestId("nt-title").fill("Forbidden repo attempt");
      const deniedBanner = await cPage
        .getByTestId("new-task-denied")
        .innerText()
        .catch(() => "");
      const submitDisabled = await cPage
        .getByTestId("new-task-submit")
        .isDisabled();
      // Adversarial check: press Ctrl+Enter while !canFile()!
      await cPage.getByTestId("nt-title").press("Control+Enter");
      await sleep(250);
      const ctrlEnterBypassedCanFile = contribPosts.length > 0;
      await saveShot(cPage, "new-task-viewer-desktop");
      await cCtx.close();

      report.item2.viewer = {
        viewerRailFabDisabled: railFabDisabled,
        viewerRailFabTip: railFabTip.trim(),
        viewerCShortcutOpenedSheet: viewerSheetOpened,
        nextDeniedBannerOnRefusedRepo: deniedBanner.trim(),
        nextSubmitDisabledOnRefusedRepo: submitDisabled,
        ctrlEnterBypassedCanFile,
        classicCreateBlockedCountOnViewer: createBlockedCountOnViewer,
        classicCreateDisabled,
      };
    }

    // Classic NewTaskOverlay real-browser touch (390x844, hasTouch: true, page.tap) & desktop hover (1440x900)
    {
      const tCtx = await browser.newContext({
        viewport: { width: 390, height: 844 },
        hasTouch: true,
        isMobile: true,
      });
      await tCtx.addCookies([
        { name: "iapsim_email", value: "alice@example.com", url: iapProxyUrl },
      ]);
      const tPage = await tCtx.newPage();
      attachMonitors(tPage, "item2-classic-touch");
      await tPage.goto(`${iapProxyUrl}/new`, { waitUntil: "domcontentloaded" });
      await tPage
        .locator("[data-create-blocked]")
        .waitFor({ state: "visible", timeout: 15_000 });

      const blocked = tPage.locator("[data-create-blocked]");
      await blocked.scrollIntoViewIfNeeded();
      await tPage.tap("[data-create-blocked]");
      await sleep(250);
      const tipAfter250ms = await tPage
        .getByRole("tooltip")
        .innerText()
        .catch(() => "");
      await sleep(950);
      const tipStillOpenAt1200ms =
        (await tPage.getByRole("tooltip").count()) > 0;
      await sleep(2200);
      const tipClosedAt3400ms =
        (await tPage.getByRole("tooltip").count()) === 0;

      // Fill title -> missing repo tooltip on tap + bounding box check
      await tPage.getByLabel(/Title/i).first().fill("Touch tap repo check");
      await blocked.scrollIntoViewIfNeeded();
      await tPage.tap("[data-create-blocked]");
      await sleep(250);
      const repoTipText = await tPage
        .getByRole("tooltip")
        .innerText()
        .catch(() => "");
      const repoTipBox = await tPage.getByRole("tooltip").boundingBox();
      await sleep(3200);
      const repoTipAutoClosed =
        (await tPage.getByRole("tooltip").count()) === 0;
      await tCtx.close();

      // Desktop hover check at 1440x900
      const dCtx = await browser.newContext({
        viewport: { width: 1440, height: 900 },
      });
      await dCtx.addCookies([
        { name: "iapsim_email", value: "alice@example.com", url: iapProxyUrl },
      ]);
      const dPage = await dCtx.newPage();
      attachMonitors(dPage, "item2-classic-hover");
      await dPage.goto(`${iapProxyUrl}/new`, { waitUntil: "domcontentloaded" });
      await dPage
        .locator("[data-create-blocked]")
        .waitFor({ state: "visible", timeout: 15_000 });
      await dPage.locator("[data-create-blocked]").hover();
      await sleep(250);
      const hoverTipText = await dPage
        .getByRole("tooltip")
        .innerText()
        .catch(() => "");
      await dPage.mouse.move(5, 5);
      await sleep(400);
      const hoverClosedOnLeave =
        (await dPage.getByRole("tooltip").count()) === 0;
      await dCtx.close();

      report.item2.classicTouchAndHover = {
        touchMissingTitleTip250ms: tipAfter250ms.trim(),
        touchStillOpenAt1200ms: tipStillOpenAt1200ms,
        touchAutoClosedAt3400ms: tipClosedAt3400ms,
        touchMissingRepoTip: repoTipText.trim(),
        touchRepoTipFits390px:
          !!repoTipBox &&
          repoTipBox.x >= 0 &&
          repoTipBox.x + repoTipBox.width <= 390,
        touchRepoTipAutoClosed: repoTipAutoClosed,
        desktopHoverTip: hoverTipText.trim(),
        desktopHoverClosedOnLeave: hoverClosedOnLeave,
      };
    }

    // =========================================================================
    // ITEM 3: /next/tasks batch Close (1 vs 2+ tasks, Keep, Escape, confirm, partial failure) + task(s) pluralization
    // =========================================================================
    // First test task(s) pluralization on /next/processes/suites:
    // Cancel sr3 (1 open task) and a dual-audit run (2 open tasks) while tasks are still queued!
    const srDual = await apiMust(daemonUrl, code, "POST", "/api/suite-runs", {
      suiteId: suite2.id,
      repo: "acme/widgets",
      base: "main",
    });
    // Close sr1's task first so it doesn't consume a worker slot when we unpause later
    for (const st of sr1.tasks || []) {
      await apiCall(daemonUrl, code, "POST", `/api/tasks/${st.taskId}/close`, {
        close_pull_request: false,
      });
    }

    {
      const ctx = await browser.newContext({
        viewport: { width: 1440, height: 900 },
      });
      await ctx.addCookies([
        { name: "iapsim_email", value: "alice@example.com", url: iapProxyUrl },
      ]);
      const page = await ctx.newPage();
      attachMonitors(page, "item3-suite-cancel");
      await page.goto(`${iapProxyUrl}/next/processes/suites`, {
        waitUntil: "domcontentloaded",
      });
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page.getByTestId("suites-subview-runs").click();

      // Cancel sr3 (1 open suite task)
      await page.getByTestId(`suite-run-row-${sr3.id}`).click();
      await page.getByTestId("suite-run-cancel-btn").click();
      await page.getByTestId("confirm-accept").click();
      await page.getByTestId("snackbar").waitFor({ state: "visible" });
      const cancel1Snack = (await page.getByTestId("snackbar").innerText())
        .replace(/\s+/g, " ")
        .trim();

      // Cancel srDual (2 open suite tasks)
      await page.getByTestId(`suite-run-row-${srDual.id}`).click();
      await page.getByTestId("suite-run-cancel-btn").click();
      await page.getByTestId("confirm-accept").click();
      await sleep(300);
      const cancel2Snack = (await page.getByTestId("snackbar").innerText())
        .replace(/\s+/g, " ")
        .trim();

      report.item3.suiteCancelPluralization = { cancel1Snack, cancel2Snack };

      // Check stranded tasks pluralization on /next/settings/repositories (0, 1, 2 open tasks on acme/docs)
      const stranded1 = (
        await apiCall(daemonUrl, code, "POST", "/api/tasks", {
          title: "Stranded docs task 1",
          repo: "acme/docs",
          approved: true,
        })
      ).body;
      // Temporarily remove acme/docs if it was auto-added, or remove acme/api after filing on acme/api!
      // Filing on acme/api while acme/api is in targetRepos succeeds; then removing acme/api from targetRepos strands open tasks on acme/api!
      if (stranded1?.id) {
        await apiCall(
          daemonUrl,
          code,
          "POST",
          `/api/tasks/${stranded1.id}/close`,
          {
            close_pull_request: false,
          },
        );
      }
      const apiTask1 = (
        await apiCall(daemonUrl, code, "POST", "/api/tasks", {
          title: "Stranded api task 1",
          repo: "acme/api",
          approved: true,
        })
      ).body;
      await apiCall(daemonUrl, code, "DELETE", "/api/repos/acme/api");

      await page.goto(`${iapProxyUrl}/next/settings/repositories`, {
        waitUntil: "domcontentloaded",
      });
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page.getByTestId("check-repos-plan-btn").click();
      await page.getByTestId("repos-plan-notice").waitFor({ state: "visible" });
      const stranded1Notice = (
        await page.getByTestId("repos-plan-notice").innerText()
      ).trim();

      // Add acme/api back, file 2nd task, remove acme/api again -> 2 stranded tasks
      await apiCall(daemonUrl, code, "POST", "/api/repos", {
        repo: "acme/api",
      });
      const apiTask2 = (
        await apiCall(daemonUrl, code, "POST", "/api/tasks", {
          title: "Stranded api task 2",
          repo: "acme/api",
          approved: true,
        })
      ).body;
      await apiCall(daemonUrl, code, "DELETE", "/api/repos/acme/api");
      // Reload repositories settings and click Check stranded tasks
      await page.goto(`${iapProxyUrl}/next/settings/repositories`, {
        waitUntil: "domcontentloaded",
      });
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page.getByTestId("check-repos-plan-btn").click();
      await page.getByTestId("repos-plan-notice").waitFor({ state: "visible" });
      const stranded2Notice = (
        await page.getByTestId("repos-plan-notice").innerText()
      ).trim();

      // Clean up stranded tasks and restore acme/api
      await apiCall(
        daemonUrl,
        code,
        "POST",
        `/api/tasks/${apiTask1.id}/close`,
        {
          close_pull_request: false,
        },
      );
      await apiCall(
        daemonUrl,
        code,
        "POST",
        `/api/tasks/${apiTask2.id}/close`,
        {
          close_pull_request: false,
        },
      );
      await apiCall(daemonUrl, code, "POST", "/api/repos", {
        repo: "acme/api",
      });
      report.item3.strandedPluralization = { stranded1Notice, stranded2Notice };

      // Broadcast pluralization on /next/settings (1 active task vs 2 active tasks)
      const bTask1 = (
        await apiCall(daemonUrl, code, "POST", "/api/tasks", {
          title: "Broadcast target 1",
          repo: "acme/widgets",
          approved: true,
        })
      ).body;
      await page.goto(`${iapProxyUrl}/next/settings/general`, {
        waitUntil: "domcontentloaded",
      });
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page
        .getByTestId("input-broadcast-message")
        .fill("Hold merges for audit");
      await page.getByTestId("send-broadcast-btn").click();
      await page.getByTestId("confirm-accept").click();
      await page.getByTestId("snackbar").waitFor({ state: "visible" });
      const broadcast1Snack = (await page.getByTestId("snackbar").innerText())
        .replace(/\s+/g, " ")
        .trim();

      const bTask2 = (
        await apiCall(daemonUrl, code, "POST", "/api/tasks", {
          title: "Broadcast target 2",
          repo: "acme/widgets",
          approved: true,
        })
      ).body;
      await page
        .getByTestId("input-broadcast-message")
        .fill("Hold merges for audit 2");
      await page.getByTestId("send-broadcast-btn").click();
      await page.getByTestId("confirm-accept").click();
      await sleep(300);
      const broadcast2Snack = (await page.getByTestId("snackbar").innerText())
        .replace(/\s+/g, " ")
        .trim();

      await apiCall(daemonUrl, code, "POST", `/api/tasks/${bTask1.id}/close`, {
        close_pull_request: false,
      });
      await apiCall(daemonUrl, code, "POST", `/api/tasks/${bTask2.id}/close`, {
        close_pull_request: false,
      });
      report.item3.broadcastPluralization = {
        broadcast1Snack,
        broadcast2Snack,
      };
      await ctx.close();
    }

    // Now test /next/tasks batch Close confirmation (1 vs 2 live tasks, Keep, Escape, confirm) at 1440x900 & 390x844!
    // Create live awaiting_reply tasks on ?tab=asked (which use the exact same live confirmClose path as running tasks, deterministic without sandbox VM spinup!)
    report.item3.batchCloseConfirm = [];
    for (const vp of [
      { name: "desktop", width: 1440, height: 900 },
      { name: "phone", width: 390, height: 844 },
    ]) {
      const live1 = (
        await apiCall(daemonUrl, code, "POST", "/api/tasks", {
          title: `[hold-running] Live task A (${vp.name})`,
          repo: "acme/widgets",
          approved: true,
        })
      ).body;
      const live2 = (
        await apiCall(daemonUrl, code, "POST", "/api/tasks", {
          title: `[hold-running] Live task B (${vp.name})`,
          repo: "acme/widgets",
          approved: true,
        })
      ).body;
      // Put both into awaiting_reply so they sit on ?tab=asked as live non-reversible tasks
      // (or let's also check state: awaiting_reply is in !["queued","proposed","deferred"] -> live === true!)
      await apiCall(
        daemonUrl,
        code,
        "POST",
        `/api/tasks/${live1.id}/messages`,
        { summary: "Need confirmation A", body: "Question A" },
      );
      await apiCall(
        daemonUrl,
        code,
        "POST",
        `/api/tasks/${live2.id}/messages`,
        { summary: "Need confirmation B", body: "Question B" },
      );

      const ctx = await browser.newContext({
        viewport: { width: vp.width, height: vp.height },
      });
      await ctx.addCookies([
        { name: "iapsim_email", value: "alice@example.com", url: iapProxyUrl },
      ]);
      const page = await ctx.newPage();
      attachMonitors(page, `item3-batch-close-${vp.name}`);
      await page.goto(
        `${iapProxyUrl}/next/tasks?tab=asked${vp.name === "desktop" ? `&task=${live1.id}` : ""}`,
        { waitUntil: "domcontentloaded" },
      );
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page.getByTestId(`row-${live1.id}`).waitFor({ state: "visible" });

      // Select 1 task -> click batch-close
      await page
        .locator(`[data-testid="row-${live1.id}"] input[type="checkbox"]`)
        .check();
      await page.getByTestId("batch-close").click();
      await page.getByTestId("close-confirm").waitFor({ state: "visible" });
      const confirm1Text = (
        await page.getByTestId("close-confirm").innerText()
      ).trim();
      await saveShot(page, `tasks-batch-close-1-${vp.name}`);

      // Press Escape -> check whether confirm-close dismissed AND whether desktop pane stayed open!
      await page.keyboard.press("Escape");
      await sleep(150);
      const confirmDismissedOnEscape =
        (await page.getByTestId("close-confirm").count()) === 0;
      const paneKeptOpenOnEscape =
        vp.name === "desktop" ? page.url().includes(`task=${live1.id}`) : true;

      // Re-open if dismissed, then test Keep (Cancel) button
      if (confirmDismissedOnEscape) {
        await page.getByTestId("batch-close").click();
        await page.getByTestId("close-confirm").waitFor({ state: "visible" });
      }
      await page
        .getByTestId("batch-bar")
        .getByRole("button", { name: "Keep" })
        .click();
      await sleep(100);
      const confirmDismissedOnKeep =
        (await page.getByTestId("close-confirm").count()) === 0;

      // Select 2nd task as well -> 2 selected -> click batch-close
      await page
        .locator(`[data-testid="row-${live2.id}"] input[type="checkbox"]`)
        .check();
      await page.getByTestId("batch-close").click();
      await page.getByTestId("close-confirm").waitFor({ state: "visible" });
      const confirm2Text = (
        await page.getByTestId("close-confirm").innerText()
      ).trim();
      await saveShot(page, `tasks-batch-close-2-${vp.name}`);

      // Confirm Close
      await page.getByTestId("close-yes").click();
      await page.getByTestId("snackbar").waitFor({ state: "visible" });
      const closed2Snack = (await page.getByTestId("snackbar").innerText())
        .replace(/\s+/g, " ")
        .trim();
      const hasUndo = (await page.getByTestId("snackbar-action").count()) > 0;
      const overflowPx = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );

      report.item3.batchCloseConfirm.push({
        viewport: `${vp.width}x${vp.height}`,
        confirm1Text,
        confirmDismissedOnEscape,
        paneKeptOpenOnEscape,
        confirmDismissedOnKeep,
        confirm2Text,
        closed2Snack,
        hasUndo,
        overflowPx,
      });
      await ctx.close();
    }

    // Simulated partial failure on batch Close (2 queued tasks on /next/tasks, 2nd returns HTTP 500 via faultProxy)
    {
      const q1 = (
        await apiCall(daemonUrl, code, "POST", "/api/tasks", {
          title: "Partial close succeeds",
          repo: "acme/widgets",
          approved: true,
        })
      ).body;
      const q2 = (
        await apiCall(daemonUrl, code, "POST", "/api/tasks", {
          title: "Partial close fails 500",
          repo: "acme/widgets",
          approved: true,
        })
      ).body;
      failCloseTaskId = q2.id;

      const ctx = await browser.newContext({
        viewport: { width: 1440, height: 900 },
      });
      await ctx.addCookies([
        { name: "iapsim_email", value: "alice@example.com", url: iapProxyUrl },
      ]);
      const page = await ctx.newPage();
      attachMonitors(page, "item3-partial-failure");
      await page.goto(`${iapProxyUrl}/next/tasks`, {
        waitUntil: "domcontentloaded",
      });
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
      await page.getByTestId(`row-${q1.id}`).waitFor({ state: "visible" });
      await page.getByTestId(`row-${q2.id}`).waitFor({ state: "visible" });

      await page
        .locator(`[data-testid="row-${q1.id}"] input[type="checkbox"]`)
        .check();
      await page
        .locator(`[data-testid="row-${q2.id}"] input[type="checkbox"]`)
        .check();
      await page.getByTestId("batch-close").click();
      await page.getByTestId("action-error").waitFor({ state: "visible" });
      await sleep(250);

      const actionErrorText = (
        await page.getByTestId("action-error").innerText()
      )
        .replace(/\s+/g, " ")
        .trim();
      const snackbarText = await page
        .getByTestId("snackbar")
        .innerText()
        .then((s) => s.replace(/\s+/g, " ").trim())
        .catch(() => "");
      const q2StillChecked = await page
        .locator(`[data-testid="row-${q2.id}"] input[type="checkbox"]`)
        .isChecked()
        .catch(() => false);
      await saveShot(page, "tasks-batch-close-partial-failure");

      failCloseTaskId = null;
      await apiCall(daemonUrl, code, "POST", `/api/tasks/${q2.id}/close`, {
        close_pull_request: false,
      });

      report.item3.partialFailure = {
        succeededTaskId: q1.id,
        failedTaskId: q2.id,
        actionErrorText,
        snackbarText,
        failedTaskKeptSelected: q2StillChecked,
      };
      await ctx.close();
    }
  } finally {
    if (browser) await browser.close().catch(() => {});
    if (faultProxy?.server) faultProxy.server.close();
    for (const p of procs) {
      try {
        p.kill("SIGTERM");
      } catch {
        // ignore
      }
    }
    await sleep(300);
    fs.rmSync(workDir, { recursive: true, force: true });
  }

  const outText = JSON.stringify(report, null, 2);
  if (outJsonArg) {
    fs.mkdirSync(path.dirname(outJsonArg), { recursive: true });
    fs.writeFileSync(outJsonArg, outText + "\n");
  }
  console.log(outText);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
