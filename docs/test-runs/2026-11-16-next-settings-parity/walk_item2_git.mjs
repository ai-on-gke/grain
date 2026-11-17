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

// Section 9 Item 2 live walkthrough against a real grain daemon:
// - HTTPS git connection with real credential cloning through git proxy
// - SSH git connection with known_hosts cloning through git proxy
// - Removing a git connection in use (confirm + behavior)
// - Typo'd/missing credential name returning 400 and leaving nothing behind.

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
const HTTPS_GIT_ADDR = process.env.HTTPS_GIT_ADDR || "127.0.0.1:4294";
const SSH_GIT_ADDR = process.env.SSH_GIT_ADDR || "127.0.0.1:4295";
const HTTPS_GIT_TOKEN = process.env.HTTPS_GIT_TOKEN || "walk_https_secret_pat_99";

const findings = [];
function record(area, check, ok, detail) {
  findings.push({ area, check, ok, detail });
  console.log(`[${ok ? "PASS" : "FAIL"}] ${area} :: ${check} :: ${JSON.stringify(detail)}`);
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
  } catch {}
  return { status: res.status, ok: res.ok, text, json };
}

function grainCli(args) {
  return execFileSync(GRAIN, args, {
    env: {
      PATH: "/home/debian/bin:/usr/local/bin:/usr/bin:/bin",
      HOME: path.join(WORK_DIR, "home"),
      GRAIN_SERVER: DAEMON,
      GRAIN_TOKEN: CODE,
      GRAIN_DATA_DIR: path.join(WORK_DIR, "data"),
    },
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
  });
}

function readAuditLog() {
  const p = path.join(WORK_DIR, "gitforges-audit.jsonl");
  if (!fs.existsSync(p)) return [];
  return fs
    .readFileSync(p, "utf8")
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((l) => JSON.parse(l));
}

async function dispatchInspectRepo(repo, title) {
  const actionObj = {
    kind: "mcp_sequence",
    calls: [
      {
        tool: "run_command",
        args: {
          command: [
            "echo '=== PWD ==='",
            "pwd",
            "echo '=== REMOTE ==='",
            "git -C work remote -v 2>&1 || true",
            "echo '=== MARKER ==='",
            "cat work/MARKER.txt 2>&1 || true",
            "echo '=== LOG ==='",
            "git -C work log -1 --oneline 2>&1 || true",
          ].join("\n"),
          description: `inspect cloned ${repo} in sandbox`,
        },
      },
      {
        tool: "comment_on_issue",
        args: { comment: `git clone inspect complete for ${repo}` },
      },
    ],
  };
  fs.writeFileSync(
    path.join(WORK_DIR, "action-default.json"),
    JSON.stringify(actionObj, null, 2),
  );
  const cr = await api("POST", "/api/tasks", {
    title,
    description: `Verify git proxy clone for ${repo}`,
    repo,
  });
  if (!cr.ok || !cr.json?.id) {
    return { created: false, createStatus: cr.status, createText: cr.text };
  }
  const tid = cr.json.id;
  await api("POST", `/api/tasks/${tid}/approve`);
  let task = null;
  for (let i = 0; i < 90; i++) {
    task = (await api("GET", `/api/tasks/${tid}`)).json;
    if (
      ["done", "closed", "failed", "cancelled", "completed", "awaiting_reply"].includes(
        task?.state || "",
      )
    ) {
      break;
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  const mcpPath = path.join(WORK_DIR, "runs", `mcp-${tid}.jsonl`);
  const mcpText = fs.existsSync(mcpPath) ? fs.readFileSync(mcpPath, "utf8") : "";
  return {
    created: true,
    tid,
    state: task?.state,
    error: task?.error || task?.lastError || null,
    mcpText,
  };
}

async function main() {
  const storageState = await signIn(DAEMON, CODE, "alice@example.com");
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    storageState,
  });
  const page = await context.newPage();

  // Ensure acme/widgets is registered and clean up stale walk connections/repos so script is idempotent
  await api("POST", "/api/repos", { repo: "acme/widgets" });
  await api("DELETE", "/api/repos/acme/https-repo").catch(() => {});
  await api("DELETE", `/api/repos/${encodeURIComponent(HTTPS_GIT_ADDR)}/acme/https-repo`).catch(() => {});
  await api("DELETE", `/api/repos/${encodeURIComponent(SSH_GIT_ADDR)}/acme/ssh-repo`).catch(() => {});
  const existing = (await api("GET", "/api/deployment")).json?.gitConnections || [];
  for (const c of existing) {
    if (c.host === HTTPS_GIT_ADDR || c.host === SSH_GIT_ADDR || c.scope === "acme/https-repo" || c.scope === "acme/ssh-repo") {
      await api("DELETE", `/api/deployment/git-connections/${encodeURIComponent(c.credential)}`);
    }
  }

  let httpsCredName = "";

  // =========================================================================
  // 1. HTTPS git connection via /next/settings/git + real proxy clone
  // =========================================================================
  {
    await page.goto(`${DAEMON}/next/settings/git`);
    await page.getByTestId("card-git-connections").waitFor({ state: "visible" });
    await page.getByTestId("select-git-conn-kind").click();
    await page.getByTestId("select-git-conn-kind-opt-git").click();
    await page.getByTestId("select-git-conn-type").click();
    await page.getByTestId("select-git-conn-type-opt-pat").click();
    await page.getByTestId("input-git-conn-host").fill(HTTPS_GIT_ADDR);
    await page.getByTestId("input-git-conn-scope").fill("acme/https-repo");
    await page.getByTestId("input-git-conn-secret").fill(HTTPS_GIT_TOKEN);

    const pAddHttps = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/deployment/git-connections") &&
        r.request().method() === "POST",
    );
    await page.getByTestId("save-git-conn-btn").click();
    const resHttps = await pAddHttps;
    const httpsRespText = await resHttps.text();

    // Verify classic /settings/git, API, secrets list, state-repo parity + no secret echo
    const depApi = await api("GET", "/api/deployment");
    const httpsEntry = (depApi.json?.gitConnections || []).find(
      (c) => c.host === HTTPS_GIT_ADDR || c.scope === "acme/https-repo",
    );
    httpsCredName = httpsEntry?.credential || "";
    const secretsCli = grainCli(["secrets", "list"]);
    grainCli(["state", "sync"]);
    const cfgRaw = fs.readFileSync(
      path.join(WORK_DIR, "data/state-repo/tables/grain_config.json"),
      "utf8",
    );
    const domNext = await page.content();

    await page.goto(`${DAEMON}/settings/git`);
    await page.waitForLoadState("domcontentloaded");
    await page.getByText(HTTPS_GIT_ADDR).first().waitFor({ state: "visible", timeout: 10000 }).catch(() => {});
    const domClassic = await page.content();

    const leaked =
      depApi.text.includes(HTTPS_GIT_TOKEN) ||
      secretsCli.includes(HTTPS_GIT_TOKEN) ||
      cfgRaw.includes(HTTPS_GIT_TOKEN) ||
      domNext.includes(HTTPS_GIT_TOKEN) ||
      domClassic.includes(HTTPS_GIT_TOKEN);

    record(
      "git-https",
      "add HTTPS PAT connection via /next/settings/git -> classic/api/state-repo & secret never echoed",
      resHttps.status() === 200 &&
        Boolean(httpsCredName) &&
        domClassic.includes(HTTPS_GIT_ADDR) &&
        cfgRaw.includes(httpsCredName) &&
        !leaked,
      {
        status: resHttps.status(),
        credential: httpsCredName,
        classicSees: domClassic.includes(HTTPS_GIT_ADDR),
        stateRepoSees: Boolean(httpsCredName) && cfgRaw.includes(httpsCredName),
        leaked,
      },
    );

    // Add host-qualified repo 127.0.0.1:4294/acme/https-repo and dispatch fake-agy task
    const httpsRepo = `${HTTPS_GIT_ADDR}/acme/https-repo`;
    const addRepoResp = await api("POST", "/api/repos", { repo: httpsRepo });
    const auditBefore = readAuditLog().length;
    const run = await dispatchInspectRepo(
      httpsRepo,
      "Walkthrough Item 2: HTTPS git proxy clone",
    );
    const auditAfter = readAuditLog().slice(auditBefore);
    const httpsAuthHit = auditAfter.some(
      (e) => e.proto === "https" && e.authOk === true && e.path?.includes("acme/https-repo"),
    );
    const markerFound = run.mcpText?.includes("walkthrough-https-repo-marker");

    record(
      "git-https",
      "dispatched task clones 127.0.0.1:4294/acme/https-repo through git proxy over authenticated HTTPS",
      Boolean(markerFound) && httpsAuthHit,
      {
        repo: httpsRepo,
        addRepoStatus: addRepoResp.status,
        addRepoText: addRepoResp.text.slice(0, 200),
        tid: run.tid,
        state: run.state,
        error: run.error,
        markerFound,
        httpsAuthHit,
        auditNew: auditAfter,
        mcpSnippet: run.mcpText?.slice(0, 700),
      },
    );
  }

  // =========================================================================
  // 2. SSH git connection with known_hosts via /next/settings/git + proxy clone
  // =========================================================================
  {
    const clientKey = fs.readFileSync(path.join(WORK_DIR, "certs/ssh_client_key"), "utf8");
    const knownHosts = fs.readFileSync(path.join(WORK_DIR, "certs/known_hosts"), "utf8").trim();

    await page.goto(`${DAEMON}/next/settings/git`);
    await page.getByTestId("card-git-connections").waitFor({ state: "visible" });
    await page.getByTestId("select-git-conn-kind").click();
    await page.getByTestId("select-git-conn-kind-opt-git").click();
    await page.getByTestId("select-git-conn-type").click();
    await page.getByTestId("select-git-conn-type-opt-ssh").click();
    await page.getByTestId("input-git-conn-host").fill(SSH_GIT_ADDR);
    await page.getByTestId("input-git-conn-scope").fill("acme/ssh-repo");
    await page.getByTestId("input-git-conn-ssh-key").fill(clientKey);
    await page.getByTestId("input-git-conn-known-hosts").fill(knownHosts);

    const pAddSsh = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/deployment/git-connections") &&
        r.request().method() === "POST",
    );
    await page.getByTestId("save-git-conn-btn").click();
    const resSsh = await pAddSsh;
    const sshRespText = await resSsh.text();

    let sshUiErr = "";
    if (resSsh.status() !== 200) {
      await page.waitForTimeout(300);
      sshUiErr = (await page.getByTestId("git-connections-error").textContent().catch(() => "")) || "";
    }

    const keyBodyLine = clientKey
      .split("\n")
      .map((l) => l.trim())
      .find((l) => l && !l.startsWith("-----"));
    const sshSecretLeaked =
      (await api("GET", "/api/deployment")).text.includes(keyBodyLine) ||
      (await page.content()).includes(keyBodyLine);

    record(
      "git-ssh",
      "add SSH connection with non-22 host:port and known_hosts via /next/settings/git",
      resSsh.status() === 200 && !sshSecretLeaked,
      {
        status: resSsh.status(),
        host: SSH_GIT_ADDR,
        knownHostsLine: knownHosts,
        respText: sshRespText.slice(0, 400),
        uiError: sshUiErr.trim(),
        sshSecretLeaked,
      },
    );

    if (resSsh.status() === 200) {
      const sshRepo = `${SSH_GIT_ADDR}/acme/ssh-repo`;
      const addSshRepo = await api("POST", "/api/repos", { repo: sshRepo });
      const auditBefore = readAuditLog().length;
      const run = await dispatchInspectRepo(
        sshRepo,
        "Walkthrough Item 2: SSH git proxy clone",
      );
      const auditAfter = readAuditLog().slice(auditBefore);
      const sshAuthHit = auditAfter.some(
        (e) =>
          e.proto === "ssh" &&
          e.authOk === true &&
          (e.command?.includes("acme/ssh-repo") ||
            e.path?.includes("acme/ssh-repo")),
      );
      const markerFound = run.mcpText?.includes("walkthrough-ssh-repo-marker");
      record(
        "git-ssh",
        "dispatched task clones 127.0.0.1:4295/acme/ssh-repo through git proxy over authenticated SSH",
        Boolean(markerFound) && sshAuthHit,
        {
          repo: sshRepo,
          addRepoStatus: addSshRepo.status,
          tid: run.tid,
          state: run.state,
          error: run.error,
          markerFound,
          sshAuthHit,
          auditNew: auditAfter,
          mcpSnippet: run.mcpText?.slice(0, 700),
        },
      );
    }
  }

  // =========================================================================
  // 3. Typo'd / missing credential name via UI + API -> HTTP 400 & nothing left behind
  // =========================================================================
  {
    const beforeConns = (await api("GET", "/api/deployment")).json?.gitConnections || [];
    await page.goto(`${DAEMON}/next/settings/git`);
    await page.getByTestId("card-git-connections").waitFor({ state: "visible" });
    await page.getByTestId("select-git-conn-mode").click();
    await page.getByTestId("select-git-conn-mode-opt-existing").click();
    await page.getByTestId("input-git-conn-scope").fill("acme/typo-repo");
    await page.getByTestId("input-git-conn-name").fill("typo-missing-cred-xyz");
    const pTypo = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/deployment/git-connections") &&
        r.request().method() === "POST",
    );
    await page.getByTestId("save-git-conn-btn").click();
    const typoResp = await pTypo;
    const typoText = await typoResp.text();
    await page.waitForTimeout(200);
    const typoUiErr = (await page.getByTestId("git-connections-error").textContent().catch(() => "")) || "";

    const afterConns = (await api("GET", "/api/deployment")).json?.gitConnections || [];
    grainCli(["state", "sync"]);
    const cfgRaw = fs.readFileSync(
      path.join(WORK_DIR, "data/state-repo/tables/grain_config.json"),
      "utf8",
    );
    const leakedIntoStore =
      afterConns.length !== beforeConns.length ||
      afterConns.some((c) => c.credential === "typo-missing-cred-xyz") ||
      cfgRaw.includes("typo-missing-cred-xyz");

    record(
      "git-typo-400",
      "missing credential via /next/settings/git answers 400, shows error, leaves nothing behind",
      typoResp.status() === 400 && Boolean(typoUiErr.trim()) && !leakedIntoStore,
      {
        status: typoResp.status(),
        body: typoText.trim(),
        uiError: typoUiErr.trim(),
        beforeCount: beforeConns.length,
        afterCount: afterConns.length,
        leakedIntoStore,
      },
    );
  }

  // =========================================================================
  // 4. Removing a git connection in use (confirm dialog + repo pinned / active)
  // =========================================================================
  {
    const conns = (await api("GET", "/api/deployment")).json?.gitConnections || [];
    const httpsConn = conns.find((c) => c.credential === httpsCredName);
    let removeDetail = { found: Boolean(httpsConn) };
    let removeOk = false;
    if (httpsConn) {
      // 127.0.0.1:4294/acme/https-repo is in targetRepos and uses httpsConn (which is the only connection on 127.0.0.1:4294).
      // Also check removing while an active task exists vs after removing the repo.
      await page.goto(`${DAEMON}/next/settings/git`);
      await page.getByTestId("card-git-connections").waitFor({ state: "visible" });
      const removeBtn = page.getByTestId(`remove-git-conn-${httpsConn.credential}`);
      await removeBtn.waitFor({ state: "visible", timeout: 10000 });
      await removeBtn.click();
      // Confirm dialog must appear first!
      const dialog = page.getByRole("dialog");
      await dialog.waitFor({ state: "visible", timeout: 5000 });
      const dialogText = await dialog.textContent();
      // Cancel first -> connection remains
      await dialog.getByRole("button", { name: "Cancel" }).click();
      const afterCancel = ((await api("GET", "/api/deployment")).json?.gitConnections || []).some(
        (c) => c.credential === httpsConn.credential,
      );
      // Now confirm removal while repo is still registered -> should refuse (409) because repo is in use!
      await removeBtn.click();
      await dialog.waitFor({ state: "visible", timeout: 5000 });
      const pDelInUse = page.waitForResponse(
        (r) =>
          r.url().includes(`/api/deployment/git-connections/${encodeURIComponent(httpsConn.credential)}`) &&
          r.request().method() === "DELETE",
      );
      await dialog.getByRole("button", { name: /remove/i }).click();
      const delInUseRes = await pDelInUse;
      const delInUseText = await delInUseRes.text();
      await page.waitForTimeout(250);
      const uiBanner = (await page.getByTestId("git-connections-error").textContent().catch(() => "")) || "";
      const stillPresentWhileInUse = ((await api("GET", "/api/deployment")).json?.gitConnections || []).some(
        (c) => c.credential === httpsConn.credential,
      );
      removeOk = Boolean(dialogText) && afterCancel && (delInUseRes.status() === 409 || delInUseRes.status() === 400) && stillPresentWhileInUse && Boolean(uiBanner.trim());
      removeDetail = {
        credential: httpsConn.credential,
        dialogText: dialogText?.trim(),
        afterCancel,
        deleteInUseStatus: delInUseRes.status(),
        deleteInUseBody: delInUseText.slice(0, 300),
        uiBanner: uiBanner.trim(),
        stillPresentWhileInUse,
      };
    }
    record(
      "git-remove-in-use",
      "removing a git connection in use asks confirmation and reports outcome cleanly",
      removeOk,
      removeDetail,
    );
  }

  await browser.close();
  fs.mkdirSync(path.join(WORK_DIR, "results"), { recursive: true });
  fs.writeFileSync(
    path.join(WORK_DIR, "results/item2.json"),
    JSON.stringify({ findings }, null, 2),
  );
  const failed = findings.filter((f) => !f.ok);
  console.log(`\nItem 2 summary: ${findings.length - failed.length}/${findings.length} passed, ${failed.length} failed.`);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
