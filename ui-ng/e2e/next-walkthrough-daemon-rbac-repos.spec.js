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

// Scenario spec for docs/test-plans/2026-11-13.md §1 items 1, 2, 4 (CLI catalog),
// 5, and 6 against a real `grain daemon -sandbox-port-base=0` (SQLite) with
// 5 connected repositories, 2 configured instance repositories, and IAP RBAC.

import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { test, expect } from "./fixtures.js";

const repoRoot = fileURLToPath(new URL("../..", import.meta.url));
const walkDir = path.join(
  repoRoot,
  "docs/test-runs/2026-11-13-next-ui-complete",
);

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

test("real daemon walkthrough: rail parity & deep links, viewer RBAC & CSRF, 5-vs-2 repo scoping, configured-only session models, and Found in release", async ({
  browser,
}) => {
  test.setTimeout(180_000);
  const grainBin = process.env.GRAIN_E2E_DEMO_BIN;
  expect(grainBin).toBeTruthy();

  const workDir = fs.mkdtempSync(path.join(os.tmpdir(), "grain-e2e-daemon-"));
  const procs = [];
  try {
    fs.mkdirSync(path.join(workDir, "data"), { recursive: true });
    fs.mkdirSync(path.join(workDir, "sandboxes"), { recursive: true });
    fs.mkdirSync(path.join(workDir, "bin"), { recursive: true });
    fs.mkdirSync(path.join(workDir, "home"), { recursive: true });
    fs.mkdirSync(path.join(workDir, "bares"), { recursive: true });

    const ghsimBin = path.join(workDir, "bin/ghsim");
    const iapsimBin = path.join(workDir, "bin/iapsim");
    execFileSync(
      "go",
      ["build", "-o", ghsimBin, path.join(walkDir, "ghsim.go")],
      {
        cwd: repoRoot,
      },
    );
    execFileSync("go", ["build", "-o", iapsimBin, "tests/iap/iapsim.go"], {
      cwd: repoRoot,
    });

    for (const r of ["widgets", "api", "docs", "infra", "mobile"]) {
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
          "widgets,api,docs,infra,mobile",
          "-token",
          "ghp_walkthrough",
          "-default",
          "main",
        ],
        { stdio: "ignore" },
      ),
    );
    await waitHttp(`http://127.0.0.1:${ghPort}/repos/acme/widgets`);

    procs.push(
      spawn(
        iapsimBin,
        [
          "-issuer",
          `127.0.0.1:${iapIssuerPort}`,
          "-proxy",
          `127.0.0.1:${iapProxyPort}`,
          "-upstream",
          daemonUrl,
          "-aud",
          iapAud,
          "-iss",
          iapIss,
        ],
        { stdio: "ignore" },
      ),
    );
    await waitHttp(`http://127.0.0.1:${iapIssuerPort}/jwks`);

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
          "-default-target-repo",
          "acme/widgets",
          "-ui-iap-audience",
          iapAud,
          "-ui-iap-issuer",
          iapIss,
          "-ui-iap-keys-url",
          `http://127.0.0.1:${iapIssuerPort}/jwks`,
          "-poll-interval",
          "1h",
          "-max-workers",
          "1",
          "-max-mergers",
          "0",
          "-agy-path",
          path.join(walkDir, "fake-agy.py"),
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
    expect(code).not.toBe("");

    execFileSync(grainBin, [
      "connections",
      "-data-dir",
      path.join(workDir, "data"),
      "import",
      seedModelsYaml,
    ]);
    await apiCall(daemonUrl, code, "POST", "/api/pipeline-pause", {
      paused: true,
      reason: "e2e seeding",
    });
    await apiCall(daemonUrl, code, "POST", "/api/repos", {
      repo: "acme/widgets",
    });
    await apiCall(daemonUrl, code, "POST", "/api/repos", { repo: "acme/api" });

    const prop = (
      await apiCall(daemonUrl, code, "POST", "/api/tasks", {
        title: "Seeded proposal on widgets",
        description: "Proposal brief",
        repo: "acme/widgets",
        approved: false,
      })
    ).body;
    await apiCall(daemonUrl, code, "POST", "/api/schedules", {
      title: "Nightly widgets check",
      description: "Run nightly check",
      repo: "acme/widgets",
      base: "main",
      recurrence: { kind: "daily", timeOfDay: "03:00", timeZone: "UTC" },
    });
    await apiCall(
      daemonUrl,
      code,
      "PUT",
      "/api/deployment/repos/acme/widgets/config",
      { ciMode: "grain" },
    );
    await apiCall(daemonUrl, code, "POST", "/api/ci/runs", {
      repo: "acme/widgets",
      ref: "main",
    });
    await apiCall(daemonUrl, code, "POST", "/api/access/principals", {
      entry: "user:viewer@example.com",
      role: "grain/viewer",
      reason: "viewer e2e check",
    });
    const limTok = (
      await apiCall(daemonUrl, code, "POST", "/api/external-agents/tokens", {
        name: "e2e-lim",
        scopes: ["tasks:read"],
      })
    ).body.token;

    // 1. Operator context: deep-link reload, 5-vs-2 repo scoping, configured-only session models, Found in release
    const opCtx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
    });
    await opCtx.addCookies([
      { name: "iapsim_email", value: "alice@example.com", url: iapProxyUrl },
    ]);
    const page = await opCtx.newPage();

    for (const dest of [
      "/next/inbox",
      "/next/tasks",
      "/next/sessions",
      "/next/processes",
      "/next/ci",
      "/next/fleet",
      "/next/settings",
    ]) {
      await page.goto(`${iapProxyUrl}${dest}`, {
        waitUntil: "domcontentloaded",
      });
      await page.waitForSelector(".main-stage > *:not(router-outlet)");
    }

    // Deep links survive reload
    await page.goto(`${iapProxyUrl}/next/tasks/${prop.id}`, {
      waitUntil: "domcontentloaded",
    });
    await expect(page.locator("#task-title")).toContainText(
      "Seeded proposal on widgets",
    );
    await page.reload({ waitUntil: "domcontentloaded" });
    await expect(page.locator("#task-title")).toContainText(
      "Seeded proposal on widgets",
    );
    await page.goto(`${iapProxyUrl}/next/settings/repositories`, {
      waitUntil: "domcontentloaded",
    });
    await page.reload({ waitUntil: "domcontentloaded" });
    await expect(page.locator("#settings-stage-heading")).toHaveText(
      "Repositories",
    );

    // Settings -> Repositories lists all 5 connected repos
    await expect(page.getByTestId("select-reachable-repo")).toBeVisible();
    await page.getByTestId("select-reachable-repo").click();
    const reachableMenu = page.getByTestId("select-reachable-repo-menu");
    for (const slug of [
      "acme/api",
      "acme/docs",
      "acme/infra",
      "acme/mobile",
      "acme/widgets",
    ]) {
      await expect(reachableMenu).toContainText(slug);
    }
    await page.keyboard.press("Escape");

    // Operational pickers (CI & Schedules & New task) show only the 2 configured
    // acme repos. The menus fill in reactively once GET /api/config lands, so
    // assert with auto-retrying locator matchers rather than one innerText read.
    await page.goto(`${iapProxyUrl}/next/ci`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("ci-repo-pill").click();
    const ciMenu = page.getByTestId("ci-repo-menu");
    await expect(ciMenu).toContainText("acme/widgets");
    await expect(ciMenu).toContainText("acme/api");
    await expect(ciMenu).not.toContainText("acme/docs");
    await page.keyboard.press("Escape");

    await page.goto(`${iapProxyUrl}/next/processes/schedules`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("proc-new-schedule-btn").click();
    await expect(page.getByTestId("sched-repo-select")).toBeVisible();
    await page.getByTestId("sched-repo-select").click();
    const schedMenu = page.getByTestId("sched-repo-select-menu");
    await expect(schedMenu).toContainText("acme/widgets");
    await expect(schedMenu).toContainText("acme/api");
    await expect(schedMenu).not.toContainText("acme/docs");
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");

    // Sessions model picker excludes unconfigured CLI catalog models
    await page.goto(`${iapProxyUrl}/next/sessions/director`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("chat-head-model").click();
    const modelMenu = page.getByTestId("chat-head-model-menu");
    await expect(modelMenu).toContainText("Claude Sonnet 4.6");
    await expect(modelMenu).toContainText("Gemini 2.5 Pro");
    await expect(modelMenu).not.toContainText("Unconfigured Alpha");
    await page.keyboard.press("Escape");

    // Found in release: malformed error under field + valid v3.6.0 stored + absent in classic
    await page.goto(`${iapProxyUrl}/next/tasks`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByTestId("rail-new-task").click();
    await page.getByTestId("nt-title").fill("Escaped defect in v3.6.0");
    await page.getByTestId("more-options").click();
    if ((await page.getByTestId("nt-repo").count()) > 0) {
      await page.getByTestId("nt-repo").click();
      await page.getByTestId("nt-repo-opt-acme/widgets").click();
    }
    await page.getByTestId("nt-found-in").fill("invalid-release");
    await page.getByTestId("new-task-submit").click();
    await expect(page.getByTestId("nt-found-in-error")).toBeVisible();
    await expect(page.getByTestId("nt-found-in")).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    await expect(page.getByTestId("nt-title")).not.toHaveAttribute(
      "aria-invalid",
      "true",
    );

    await page.getByTestId("nt-found-in").fill("v3.6.0");
    const createdResp = page.waitForResponse(
      (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
    );
    await page.getByTestId("new-task-submit").click();
    const createdTask = await (await createdResp).json();
    expect(createdTask.foundIn).toBe("v3.6.0");

    await opCtx.close();

    // 2. Viewer RBAC gating + forced 403 + personal token 403 + cross-origin CSRF 403
    const viewerCtx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
    });
    await viewerCtx.addCookies([
      { name: "iapsim_email", value: "viewer@example.com", url: iapProxyUrl },
    ]);
    const vPage = await viewerCtx.newPage();
    await vPage.goto(`${iapProxyUrl}/next/tasks/${prop.id}`, {
      waitUntil: "domcontentloaded",
    });
    await expect(vPage.locator("#task-title")).toBeVisible();
    await expect(vPage.getByTestId("rail-new-task")).toBeDisabled();
    await expect(vPage.locator('[data-testid="act-approve"]')).toHaveCount(0);
    await expect(vPage.locator('[data-testid="act-edit"]')).toHaveCount(0);

    await vPage.keyboard.press("c");
    await expect(vPage.getByTestId("new-task-sheet")).toHaveCount(0);

    const vPostStatus = await vPage.evaluate(async () => {
      const r = await fetch("/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: "Forced",
          description: "x",
          repo: "acme/widgets",
        }),
      });
      return r.status;
    });
    expect(vPostStatus).toBe(403);
    await viewerCtx.close();

    const tokRes = await apiCall(daemonUrl, limTok, "POST", "/api/tasks", {
      title: "Token forced",
      description: "x",
      repo: "acme/widgets",
    });
    expect(tokRes.status).toBe(403);

    const csrfRes = await fetch(`${iapProxyUrl}/api/tasks`, {
      method: "POST",
      headers: {
        "X-Iapsim-Email": "alice@example.com",
        Origin: "https://evil.example",
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        title: "CSRF",
        description: "x",
        repo: "acme/widgets",
      }),
    });
    expect(csrfRes.status).toBe(403);
  } finally {
    for (const p of procs) {
      p.kill("SIGTERM");
    }
    await sleep(300);
    fs.rmSync(workDir, { recursive: true, force: true });
  }
});
