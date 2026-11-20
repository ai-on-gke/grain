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

// Section 9 Item 3 live walkthrough against a real grain daemon + real grain-agentd:
// - Invite hosts: create enrollment code, verify Invite hosts snippet, run real grain-agentd -join
// - Accept pending host in /next/settings/hostds -> verify it connects live + parity with classic & API
// - Seed usage-limit pause for that host in PostgreSQL -> verify Delete disabled before Revoke
// - Verify Delete refused while a model connection references the host (check reference error)
// - Revoke host -> Delete host -> verify Delete report lists exact cleared pause & DB row is gone
// - Verify bulk "Delete revoked" report (skipped referenced host + deleted revoked host).

import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
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
const AGENTD = process.env.AGENTD || "/home/debian/bin/grain-agentd";
const PG_URL =
  process.env.PG_URL ||
  "postgres://grain:grain@127.0.0.1:5432/grain_walk?sslmode=disable";

const findings = [];
function record(area, check, ok, detail) {
  findings.push({ area, check, ok, detail });
  console.log(
    `[${ok ? "PASS" : "FAIL"}] ${area} :: ${check} :: ${JSON.stringify(detail)}`,
  );
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

function psql(sql) {
  return execFileSync("psql", [PG_URL, "-tAc", sql], {
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
  }).trim();
}

async function main() {
  const storageState = await signIn(DAEMON, CODE, "alice@example.com");
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    storageState,
  });
  const page = await context.newPage();
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  // Clean up any leftover walk-host-2 / walk-code from earlier runs
  const existingHostds = (await api("GET", "/api/hostds")).json || [];
  for (const h of existingHostds) {
    if (h.name === "walk-host-2" || h.name === "walk-host-skip") {
      if (!h.revokedAt && !h.revoked_at && h.state !== "revoked") {
        await api("POST", `/api/hostds/${h.name}/revoke`, {});
      }
      await api("DELETE", `/api/hostds/${h.name}`);
    }
  }
  await api("DELETE", "/api/deployment/agent-backends/walk-host-ref-conn").catch(() => {});

  // =========================================================================
  // 1. Open /next/settings/hostds, inspect default Invite hosts card, create a code
  // =========================================================================
  await page.goto(`${DAEMON}/next/settings/hostds`);
  await page.getByTestId("card-hostds-enrollment").waitFor({ state: "visible" });
  await page.getByTestId("invite-hosts").waitFor({ state: "visible" });

  const defaultSnippet = (
    await page.getByTestId("invite-hosts-snippet").textContent()
  )?.trim();

  // Verify Show code / Hide code toggle works
  await page.getByTestId("toggle-enrollment-secret").click();
  const secretText = (
    await page.getByTestId("enrollment-secret").textContent()
  )?.trim();
  await page.getByTestId("toggle-enrollment-secret").click();

  // Verify the /hostd/install endpoint referenced by the invite command returns a real shell script
  const installUrlMatch = defaultSnippet?.match(/https?:\/\/[^\s|]+/);
  let installStatus = 0;
  let installHead = "";
  if (installUrlMatch) {
    const r = await fetch(installUrlMatch[0]);
    installStatus = r.status;
    installHead = (await r.text()).slice(0, 60);
  }

  // Create a custom enrollment code via UI
  const customCodeName = `walk-invite-${Date.now().toString(36).slice(-4)}`;
  await page.getByTestId("input-enrollment-code-name").fill(customCodeName);
  await page.getByTestId("input-enrollment-code-expires").fill("24h");
  await page.getByTestId("input-enrollment-code-max-uses").fill("5");
  await page.getByTestId("input-enrollment-code-capacity").fill("2");
  await page.getByTestId("input-enrollment-code-labels").fill("role=walkthrough,os=linux");
  const pCreateCode = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/hostds/enrollment-codes") &&
      r.request().method() === "POST",
  );
  await page.getByTestId("create-enrollment-code-btn").click();
  const createCodeRes = await pCreateCode;

  // Select our new code in the Invite hosts dropdown (or verify it auto-selected)
  await page.waitForTimeout(300);
  const selectCode = page.getByTestId("select-invite-code");
  if (await selectCode.isVisible()) {
    await selectCode.click();
    const opt = page.getByTestId(`select-invite-code-opt-${customCodeName}`);
    if (await opt.isVisible()) {
      await opt.click();
    } else {
      await page.keyboard.press("Escape");
    }
  }
  await page.waitForTimeout(200);
  const customSnippet = (
    await page.getByTestId("invite-hosts-snippet").textContent()
  )?.trim();
  const joinMatch = customSnippet?.match(/--join\s+(\S+)/);
  const joinCode = joinMatch ? joinMatch[1] : "";

  // Check UI prose says "host" / "hosts" and never exposes "hostd" (excluding the /hostd/install URL path)
  const pageText = ((await page.locator("main").textContent()) || "").replace(
    /\/hostd\/install\b/g,
    "",
  );
  const hasHostdCopy = /\bhostds?\b/i.test(pageText);

  record(
    "hosts-invite",
    "Invite hosts renders command, creates code, serves install script, uses 'host' copy",
    (createCodeRes.status() === 200 || createCodeRes.status() === 201) &&
      Boolean(defaultSnippet) &&
      Boolean(secretText) &&
      installStatus === 200 &&
      installHead.startsWith("#!/bin/sh") &&
      Boolean(joinCode) &&
      !hasHostdCopy,
    {
      createStatus: createCodeRes.status(),
      defaultSnippet,
      secretPrefix: secretText?.slice(0, 12),
      installStatus,
      customCodeName,
      customSnippet,
      joinCodePrefix: joinCode.slice(0, 12),
      hasHostdCopy,
    },
  );

  // =========================================================================
  // 2. Run a real second grain-agentd using the exact --join code from the
  //    Invite hosts snippet (enrolling through a local auth-injecting proxy
  //    so local access-code auth stands in for GitHub Device OAuth since
  //    githubsim has no /login/device/code), accept in /next/settings/hostds,
  //    then start grain-agentd directly against the daemon to verify live connect.
  // =========================================================================
  const agentDir = path.join(WORK_DIR, "agentd-2");
  fs.rmSync(agentDir, { recursive: true, force: true });
  fs.mkdirSync(agentDir, { recursive: true });
  const allowlistPath = path.join(agentDir, "agentd.yaml");
  fs.writeFileSync(
    allowlistPath,
    "clis:\n  probe:\n    path: /bin/true\n",
    { mode: 0o600 },
  );
  const agentLogPath = path.join(agentDir, "agentd.log");
  const agentOut = fs.openSync(agentLogPath, "a");

  const daemonUrl = new URL(DAEMON);
  const joinProxy = http.createServer((req, res) => {
    const headers = { ...req.headers, authorization: `Bearer ${CODE}` };
    const upstream = http.request(
      {
        hostname: daemonUrl.hostname,
        port: daemonUrl.port,
        path: req.url,
        method: req.method,
        headers,
      },
      (upRes) => {
        res.writeHead(upRes.statusCode || 502, upRes.headers);
        upRes.pipe(res);
      },
    );
    upstream.on("error", (err) => {
      res.writeHead(502);
      res.end(String(err));
    });
    req.pipe(upstream);
  });
  await new Promise((resolve) => joinProxy.listen(0, "127.0.0.1", resolve));
  const proxyAddr = joinProxy.address();
  const proxyUrl = `http://127.0.0.1:${proxyAddr.port}`;

  const keyFile = path.join(agentDir, "agentd-key");
  const tokenFile = path.join(agentDir, "agentd-token");
  const sockFile = path.join(agentDir, "agentd.sock");
  const stateDir = path.join(agentDir, "state");

  const joinProc = spawn(
    AGENTD,
    [
      "-server",
      proxyUrl,
      "-join",
      joinCode,
      "-join-only",
      "-allowlist",
      allowlistPath,
      "-key-file",
      keyFile,
      "-token-file",
      tokenFile,
      "-socket",
      sockFile,
      "-state-dir",
      stateDir,
    ],
    {
      stdio: ["ignore", agentOut, agentOut],
      env: { ...process.env, HOME: path.join(WORK_DIR, "home") },
    },
  );
  const joinExitPromise = new Promise((resolve) =>
    joinProc.on("exit", (code) => resolve(code)),
  );

  let agentProc = null;
  let pendingId = "";
  try {
    // Wait for pending join request to appear in API
    for (let i = 0; i < 40; i++) {
      const pRes = await api("GET", "/api/hostds/pending");
      const list = pRes.json || [];
      const hit = list.find((p) => p.enrollment_code === customCodeName) || list[0];
      if (hit) {
        pendingId = hit.id;
        break;
      }
      await new Promise((r) => setTimeout(r, 250));
    }

    // Reload /next/settings/hostds and accept the pending host as walk-host-2
    await page.reload();
    await page.getByTestId("card-hostds-enrollment").waitFor({ state: "visible" });
    let pendingVisible = false;
    let fingerprint = "";
    let acceptStatus = 0;
    if (pendingId) {
      const row = page.getByTestId(`pending-hostd-row-${pendingId}`);
      await row.waitFor({ state: "visible", timeout: 10000 });
      pendingVisible = true;
      fingerprint = (
        await page.getByTestId(`pending-key-fingerprint-${pendingId}`).textContent()
      )?.trim();
      await page.getByTestId(`pending-name-${pendingId}`).fill("walk-host-2");
      const pAccept = page.waitForResponse(
        (r) =>
          r.url().includes(`/api/hostds/pending/${pendingId}/accept`) &&
          r.request().method() === "POST",
      );
      await page.getByTestId(`accept-pending-${pendingId}`).click();
      acceptStatus = (await pAccept).status();
    }

    // Wait for grain-agentd -join-only to finish saving key + credential
    const joinExitCode = await Promise.race([
      joinExitPromise,
      new Promise((resolve) => setTimeout(() => resolve(-99), 15000)),
    ]);
    joinProxy.close();

    // Start grain-agentd directly against the real daemon using the enrolled Ed25519 key & token
    if (joinExitCode === 0) {
      agentProc = spawn(
        AGENTD,
        [
          "-server",
          DAEMON,
          "-allowlist",
          allowlistPath,
          "-key-file",
          keyFile,
          "-token-file",
          tokenFile,
          "-socket",
          sockFile,
          "-state-dir",
          stateDir,
        ],
        {
          stdio: ["ignore", agentOut, agentOut],
          env: { ...process.env, HOME: path.join(WORK_DIR, "home") },
        },
      );
    }

    // Wait for walk-host-2 to connect live via WebSocket
    let connectedHost = null;
    for (let i = 0; i < 40; i++) {
      const list = (await api("GET", "/api/hostds")).json || [];
      const h = list.find((x) => x.name === "walk-host-2");
      if (h && (h.connected || h.state === "connected")) {
        connectedHost = h;
        break;
      }
      await new Promise((r) => setTimeout(r, 250));
    }

    // Check /next/settings/hostds row + classic /settings/hostds parity
    await page.reload();
    await page.getByTestId("hostd-row-walk-host-2").waitFor({ state: "visible", timeout: 10000 });
    const nextRowText = (
      await page.getByTestId("hostd-row-walk-host-2").textContent()
    )?.trim();
    const deleteDisabledBeforeRevoke = await page
      .getByTestId("delete-hostd-walk-host-2")
      .isDisabled();

    await page.goto(`${DAEMON}/settings/hostds`);
    await page.waitForLoadState("domcontentloaded");
    await page.getByText("walk-host-2").first().waitFor({ state: "visible", timeout: 10000 });
    const classicHasHost = (await page.content()).includes("walk-host-2");

    record(
      "hosts-join",
      "real grain-agentd joins via Invite hosts code, accepted in /next/, connects live, parity with classic",
      Boolean(pendingId) &&
        pendingVisible &&
        acceptStatus === 200 &&
        Boolean(connectedHost) &&
        deleteDisabledBeforeRevoke &&
        classicHasHost,
      {
        pendingId,
        fingerprint,
        acceptStatus,
        connected: connectedHost?.connected,
        state: connectedHost?.state,
        labels: connectedHost?.labels,
        capacity: connectedHost?.capacity,
        nextRowText,
        deleteDisabledBeforeRevoke,
        classicHasHost,
      },
    );

    // =========================================================================
    // 3. Seed usage-limit pause in Postgres for walk-host-2, revoke, delete,
    //    and verify Delete host's report matches cleared pause & DB state
    // =========================================================================
    const scopeKey = "walk-conn@hostd/walk-host-2|claude-sonnet-4-6";
    const connKey = "walk-conn@hostd/walk-host-2";
    psql(
      `INSERT INTO usage_limit_pause (scope_key, connection_id, model, labels, team, framework, until_at, reason, escalated, updated_at)
       VALUES ('${scopeKey}', '${connKey}', 'claude-sonnet-4-6', '', 'default', 'claude', NOW() + INTERVAL '2 hours', 'walkthrough seeded pause', '', NOW())
       ON CONFLICT (scope_key) DO UPDATE SET until_at = EXCLUDED.until_at;`,
    );
    const seededBefore = psql(
      `SELECT count(*) FROM usage_limit_pause WHERE connection_id = '${connKey}';`,
    );

    // Also test that deleting while referenced by a model connection is refused!
    await api("PUT", "/api/deployment/agent-backends/walk-host-ref-conn", {
      id: "walk-host-ref-conn",
      framework: "claude",
      authMode: "api_key",
      hostd: "walk-host-2",
    });

    await page.goto(`${DAEMON}/next/settings/hostds`);
    await page.getByTestId("hostd-row-walk-host-2").waitFor({ state: "visible" });

    // Revoke walk-host-2 via UI
    await page.getByTestId("revoke-hostd-walk-host-2").click();
    const revokeDlg = page.getByRole("dialog");
    await revokeDlg.waitFor({ state: "visible", timeout: 5000 });
    const pRevoke = page.waitForResponse(
      (r) =>
        r.url().includes("/api/hostds/walk-host-2/revoke") &&
        r.request().method() === "POST",
    );
    await revokeDlg.getByRole("button", { name: /revoke/i }).click();
    const revokeStatus = (await pRevoke).status();
    await page.waitForTimeout(400);

    // Try deleting while walk-host-ref-conn still names walk-host-2 -> must refuse and report reference!
    await page.getByTestId("delete-hostd-walk-host-2").click();
    const refDelDlg = page.getByRole("dialog");
    await refDelDlg.waitFor({ state: "visible", timeout: 5000 });
    const pDelRef = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/hostds/walk-host-2") &&
        r.request().method() === "DELETE",
    );
    await refDelDlg.getByRole("button", { name: /delete/i }).click();
    const delRefRes = await pDelRef;
    const delRefBody = await delRefRes.text();
    await page.waitForTimeout(300);

    // Remove the referencing connection, then delete walk-host-2 cleanly and inspect Delete host report
    await api("DELETE", "/api/deployment/agent-backends/walk-host-ref-conn");
    await page.getByTestId("delete-hostd-walk-host-2").click();
    const delDlg = page.getByRole("dialog");
    await delDlg.waitFor({ state: "visible", timeout: 5000 });
    const pDel = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/hostds/walk-host-2") &&
        r.request().method() === "DELETE",
    );
    await delDlg.getByRole("button", { name: /delete/i }).click();
    const delRes = await pDel;
    const delJson = await delRes.json();

    await page.getByTestId("hostd-delete-notice").waitFor({ state: "visible", timeout: 5000 });
    const noticeText = (
      await page.getByTestId("hostd-delete-notice").textContent()
    )?.trim();

    const pausesAfter = psql(
      `SELECT count(*) FROM usage_limit_pause WHERE connection_id = '${connKey}';`,
    );
    const hostsAfter = (await api("GET", "/api/hostds")).json || [];
    const hostStillInApi = hostsAfter.some((h) => h.name === "walk-host-2");
    const wantPauseLabel = `${connKey} (claude-sonnet-4-6)`;

    record(
      "hosts-delete-report",
      "Delete refuses while referenced; after clearing ref, deletes revoked host, clears pause, and reports exact cleared pause",
      revokeStatus === 200 &&
        delRefRes.status() === 400 &&
        delRefBody.includes("walk-host-ref-conn") &&
        delRes.status() === 200 &&
        seededBefore === "1" &&
        pausesAfter === "0" &&
        !hostStillInApi &&
        delJson?.clearedPauses?.includes(wantPauseLabel) &&
        noticeText?.includes(wantPauseLabel),
      {
        revokeStatus,
        delRefStatus: delRefRes.status(),
        delRefBody: delRefBody.trim(),
        delStatus: delRes.status(),
        delJson,
        noticeText,
        seededBefore,
        pausesAfter,
        hostStillInApi,
      },
    );

    // =========================================================================
    // 4. Bulk "Delete revoked" report (one skipped for reference, one deleted with pause)
    // =========================================================================
    await api("POST", "/api/hostds", { name: "walk-host-skip", owner: "alice@example.com" });
    await api("POST", "/api/hostds", { name: "walk-host-prune", owner: "alice@example.com" });
    // Reference walk-host-skip BEFORE revoking it
    await api("PUT", "/api/deployment/agent-backends/walk-host-ref-conn", {
      id: "walk-host-ref-conn",
      framework: "claude",
      authMode: "api_key",
      hostd: "walk-host-skip",
    });
    await api("POST", "/api/hostds/walk-host-skip/revoke", {});
    await api("POST", "/api/hostds/walk-host-prune/revoke", {});
    psql(
      `INSERT INTO usage_limit_pause (scope_key, connection_id, model, labels, team, framework, until_at, reason, escalated, updated_at)
       VALUES ('prune-conn@hostd/walk-host-prune|gemini-2.5-pro', 'prune-conn@hostd/walk-host-prune', 'gemini-2.5-pro', '', 'default', 'gemini', NOW() + INTERVAL '1 hour', 'walkthrough bulk prune pause', '', NOW())
       ON CONFLICT (scope_key) DO UPDATE SET until_at = EXCLUDED.until_at;`,
    );

    await page.reload();
    await page.getByTestId("prune-revoked-hostds").waitFor({ state: "visible" });
    await page.getByTestId("prune-revoked-hostds").click();
    const pruneDlg = page.getByRole("dialog");
    await pruneDlg.waitFor({ state: "visible", timeout: 5000 });
    const pPrune = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/hostds/delete-revoked") &&
        r.request().method() === "POST",
    );
    await pruneDlg.getByRole("button", { name: /delete/i }).click();
    const pruneRes = await pPrune;
    const pruneJson = await pruneRes.json();
    await page.getByTestId("hostd-delete-notice").waitFor({ state: "visible", timeout: 5000 });
    const pruneNotice = (
      await page.getByTestId("hostd-delete-notice").textContent()
    )?.trim();

    // Clean up walk-host-ref-conn and walk-host-skip + custom enrollment code
    await api("DELETE", "/api/deployment/agent-backends/walk-host-ref-conn");
    await api("DELETE", "/api/hostds/walk-host-skip");
    await api("POST", `/api/hostds/enrollment-codes/${customCodeName}/revoke`, {});

    const pruneDeletedMatch = pruneJson?.deleted?.some(
      (d) =>
        d.name === "walk-host-prune" &&
        d.clearedPauses?.includes("prune-conn@hostd/walk-host-prune (gemini-2.5-pro)"),
    );
    const pruneSkippedMatch = pruneJson?.skipped?.some(
      (s) => s.name === "walk-host-skip" && s.reason?.includes("walk-host-ref-conn"),
    );

    record(
      "hosts-delete-revoked",
      "Bulk Delete revoked reports both deleted (with cleared pause) and skipped (with referencing connection)",
      pruneRes.status() === 200 &&
        Boolean(pruneDeletedMatch) &&
        Boolean(pruneSkippedMatch) &&
        pruneNotice?.includes("walk-host-prune") &&
        pruneNotice?.includes("walk-host-skip") &&
        pruneNotice?.includes("prune-conn@hostd/walk-host-prune (gemini-2.5-pro)"),
      {
        status: pruneRes.status(),
        pruneJson,
        pruneNotice,
      },
    );
  } finally {
    try {
      joinProxy.close();
    } catch {}
    try {
      joinProc.kill("SIGTERM");
    } catch {}
    try {
      agentProc?.kill("SIGTERM");
    } catch {}
    try {
      fs.closeSync(agentOut);
    } catch {}
  }

  record("hosts-health", "zero uncaught page errors", pageErrors.length === 0, {
    pageErrors,
  });

  await browser.close();
  fs.mkdirSync(path.join(WORK_DIR, "results"), { recursive: true });
  fs.writeFileSync(
    path.join(WORK_DIR, "results/item3.json"),
    JSON.stringify({ findings }, null, 2),
  );
  const failed = findings.filter((f) => !f.ok);
  console.log(
    `\nItem 3 summary: ${findings.length - failed.length}/${findings.length} passed, ${failed.length} failed.`,
  );
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
