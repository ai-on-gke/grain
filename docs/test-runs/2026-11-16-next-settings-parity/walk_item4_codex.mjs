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

// Section 9 Item 4 live walkthrough against a real grain daemon:
// - Create two Codex subscription connections (codex-a, codex-b) in /next/settings/models
// - Verify invalid JSON rejected with 400
// - Upload fake auth.json on codex-a ONLY -> verify 0600 secret, never echoed, classic parity
// - Dispatch fake-codex.py on codex-a -> verifies auth.json mounted & account matches
// - Dispatch on codex-b -> verifies codex-a's auth.json is NOT placed for codex-b
// - Clear login on codex-a with confirm dialog (Cancel keeps, Confirm removes) ->
//   verify file removed and next codex-a dispatch has no auth.json.

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

function makeFutureJWT(claims) {
  const b64url = (obj) =>
    Buffer.from(JSON.stringify(obj))
      .toString("base64")
      .replace(/=/g, "")
      .replace(/\+/g, "-")
      .replace(/\//g, "_");
  return `${b64url({ alg: "RS256", typ: "JWT" })}.${b64url(claims)}.walkthroughsig`;
}

const FAKE_SECRET_MARKER = "walk-codex-a-secret-refresh-token-9942";
const FAKE_AUTH_JSON = JSON.stringify({
  tokens: {
    refresh_token: FAKE_SECRET_MARKER,
    access_token: makeFutureJWT({
      exp: Math.floor(Date.now() / 1000) + 86400,
      sub: "walk-codex-a-sub",
    }),
    id_token: makeFutureJWT({
      exp: Math.floor(Date.now() / 1000) + 86400,
      email: "codex-a@example.com",
    }),
    account_id: "acct-walk-codex-a",
  },
  last_refresh: new Date().toISOString(),
});

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

async function createCodexConnection(page, id, displayName, modelName) {
  await page.getByTestId("input-new-backend-id").fill(id);
  await page.getByTestId("input-new-backend-name").fill(displayName);
  await page.getByTestId("select-new-backend-fw").click();
  await page.getByTestId("select-new-backend-fw-opt-codex").click();
  await page.getByTestId("select-new-backend-auth").click();
  await page.getByTestId("select-new-backend-auth-opt-subscription").click();
  await page.getByTestId("input-new-backend-models").fill(modelName);
  const pSave = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/deployment/agent-backends/${id}`) &&
      r.request().method() === "PUT",
  );
  await page.getByTestId("add-model-backend").click();
  const res = await pSave;
  return res.status();
}

async function dispatchCodexTask(modelName, title) {
  const cr = await api("POST", "/api/tasks", {
    title,
    description: `Verify Codex per-connection auth.json for model ${modelName}.`,
    repo: "acme/widgets",
    model: modelName,
  });
  if (!cr.ok) {
    return { created: false, status: cr.status, error: cr.text };
  }
  const tid = String(cr.json.id);
  const app = await api("POST", `/api/tasks/${tid}/approve`, {});
  if (!app.ok) {
    return { created: true, tid, approved: false, status: app.status, error: app.text };
  }
  let task = null;
  let comments = [];
  let runs = [];
  for (let i = 0; i < 40; i++) {
    const g = await api("GET", `/api/tasks/${tid}`);
    task = g.json;
    comments = task?.comments || [];
    runs = task?.runs || [];
    const st = task?.state;
    if (
      st === "awaiting_reply" ||
      st === "failed" ||
      st === "closed" ||
      st === "merged" ||
      st === "paused" ||
      st === "waiting_for_approval" ||
      st === "stopped"
    ) {
      break;
    }
    if (runs.some((r) => r.error || r.outcome === "failed") && i >= 6) {
      break;
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  let probe = null;
  for (const c of comments) {
    const body = (c.body || "").trim();
    const m = body.match(/(?:CODEX_AUTH_PROBE\s+)?(\{[\s\S]*\})/);
    if (m) {
      try {
        const parsed = JSON.parse(m[1]);
        if (parsed && "auth_exists" in parsed) probe = parsed;
      } catch {}
    }
  }
  return {
    created: true,
    approved: true,
    tid,
    state: task?.state,
    runErrors: runs.map((r) => r.error).filter(Boolean),
    comments: comments.map((c) => (c.body || "").slice(0, 400)),
    probe,
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
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));

  // Ensure acme/widgets is registered and clean up prior codex-a / codex-b connections
  await api("POST", "/api/repos", { repo: "acme/widgets" });
  await api("DELETE", "/api/deployment/agent-backends/codex-a").catch(() => {});
  await api("DELETE", "/api/deployment/agent-backends/codex-b").catch(() => {});

  // =========================================================================
  // 1. Create codex-a and codex-b subscription connections in /next/settings/models
  // =========================================================================
  await page.goto(`${DAEMON}/next/settings/models`);
  await page.getByTestId("card-model-backends").waitFor({ state: "visible" });

  const stA = await createCodexConnection(page, "codex-a", "Codex A", "gpt-5.1-codex-walk-a");
  await page.getByTestId("backend-row-codex-a").waitFor({ state: "visible" });
  const stB = await createCodexConnection(page, "codex-b", "Codex B", "gpt-5.1-codex-walk-b");
  await page.getByTestId("backend-row-codex-b").waitFor({ state: "visible" });

  // Open codex-a credential editor: test client-side + server-side invalid JSON validation, then valid fake auth.json
  await page.getByTestId("toggle-cred-codex-a").click();
  await page.getByTestId("codex-auth-codex-a").waitFor({ state: "visible" });

  await page.getByTestId("input-codex-auth-codex-a").fill("{not valid json");
  await page.getByTestId("save-codex-auth-codex-a").click();
  await page.getByTestId("codex-auth-error-codex-a").waitFor({ state: "visible", timeout: 5000 });
  const badJsonUiErr = (
    await page.getByTestId("codex-auth-error-codex-a").textContent()
  )?.trim();
  const badJsonApi = await api("PUT", "/api/agent-backends/codex-a/codex-auth", {
    value: "{not valid json",
  });

  // Upload valid fake auth.json to codex-a ONLY
  await page.getByTestId("input-codex-auth-codex-a").fill(FAKE_AUTH_JSON);
  const pGoodAuth = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/agent-backends/codex-a/codex-auth") &&
      r.request().method() === "PUT",
  );
  await page.getByTestId("save-codex-auth-codex-a").click();
  const goodAuthRes = await pGoodAuth;
  await page.waitForTimeout(300);
  const statusPillA = (
    await page.getByTestId("codex-auth-status-codex-a").textContent()
  )?.trim();

  // Check API status for codex-a vs codex-b, file mode on disk, secret non-echo, and classic parity
  const authApiA = await api("GET", "/api/agent-backends/codex-a/codex-auth");
  const authApiB = await api("GET", "/api/agent-backends/codex-b/codex-auth");
  const depApi = await api("GET", "/api/deployment");
  const secretsCli = grainCli(["secrets", "list"]);
  grainCli(["state", "sync"]);
  const cfgRaw = fs.readFileSync(
    path.join(WORK_DIR, "data/state-repo/tables/grain_config.json"),
    "utf8",
  );

  // Inspect secret file on disk for 0600 mode
  const secretsDir = path.join(WORK_DIR, "data/secrets");
  let secretFileMatches = [];
  if (fs.existsSync(secretsDir)) {
    const walkFiles = (dir) => {
      for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        const full = path.join(dir, entry.name);
        if (entry.isDirectory()) walkFiles(full);
        else if (entry.name.includes("codex") || entry.name.includes("auth")) {
          const st = fs.statSync(full);
          secretFileMatches.push({
            path: path.relative(secretsDir, full),
            mode: (st.mode & 0o777).toString(8),
          });
        }
      }
    };
    walkFiles(secretsDir);
  }

  const domNext = await page.content();
  await page.goto(`${DAEMON}/settings/models`);
  await page.waitForLoadState("domcontentloaded");
  await page.getByText(/Codex A|codex-a/).first().waitFor({ state: "visible", timeout: 15000 });
  const domClassic = await page.content();

  const leaked =
    authApiA.text.includes(FAKE_SECRET_MARKER) ||
    depApi.text.includes(FAKE_SECRET_MARKER) ||
    secretsCli.includes(FAKE_SECRET_MARKER) ||
    cfgRaw.includes(FAKE_SECRET_MARKER) ||
    domNext.includes(FAKE_SECRET_MARKER) ||
    domClassic.includes(FAKE_SECRET_MARKER);

  record(
    "codex-auth-upload",
    "invalid JSON rejected in UI & API 400; valid auth.json saved only on codex-a, 0600 on disk, never echoed, classic sees connections",
    stA === 200 &&
      stB === 200 &&
      badJsonApi.status === 400 &&
      Boolean(badJsonUiErr) &&
      goodAuthRes.status() === 200 &&
      authApiA.json?.set === true &&
      authApiB.json?.set === false &&
      secretFileMatches.some((f) => f.mode === "600") &&
      !leaked &&
      (domClassic.includes("codex-a") || domClassic.includes("Codex A")) &&
      (domClassic.includes("codex-b") || domClassic.includes("Codex B")),
    {
      stA,
      stB,
      badJsonApiStatus: badJsonApi.status,
      badJsonUiErr,
      goodAuthStatus: goodAuthRes.status(),
      statusPillA,
      authApiA: authApiA.json,
      authApiB: authApiB.json,
      secretFileMatches,
      leaked,
    },
  );

  // =========================================================================
  // 2. Dispatch fake-codex.py on codex-a -> gets auth.json (acct-walk-codex-a)
  // =========================================================================
  const runA1 = await dispatchCodexTask(
    "gpt-5.1-codex-walk-a",
    "Walkthrough Item 4: Codex connection A with auth.json",
  );
  record(
    "codex-dispatch-a",
    "dispatched task on codex-a receives codex-a's auth.json and no OPENAI_API_KEY",
    runA1.created &&
      runA1.state === "awaiting_reply" &&
      runA1.probe?.auth_exists === true &&
      runA1.probe?.auth_account === "acct-walk-codex-a" &&
      runA1.probe?.openai_api_key_set === false,
    runA1,
  );

  // =========================================================================
  // 3. Dispatch on codex-b -> must NOT receive codex-a's auth.json
  // =========================================================================
  const runB = await dispatchCodexTask(
    "gpt-5.1-codex-walk-b",
    "Walkthrough Item 4: Codex connection B without auth.json",
  );
  const bIsolated =
    runB.probe?.auth_account !== "acct-walk-codex-a" &&
    (runB.probe === null || runB.probe?.auth_exists === false);
  record(
    "codex-dispatch-b-isolated",
    "dispatched task on codex-b does NOT receive codex-a's auth.json",
    bIsolated,
    runB,
  );

  // =========================================================================
  // 4. Clear login on codex-a (Cancel keeps, Confirm deletes) -> next run has no auth.json
  // =========================================================================
  await page.goto(`${DAEMON}/next/settings/models`);
  await page.getByTestId("backend-row-codex-a").waitFor({ state: "visible" });
  await page.getByTestId("toggle-cred-codex-a").click();
  await page.getByTestId("clear-codex-auth-codex-a").waitFor({ state: "visible" });

  // Cancel first -> auth.json stays set
  await page.getByTestId("clear-codex-auth-codex-a").click();
  const dlg1 = page.getByRole("dialog");
  await dlg1.waitFor({ state: "visible", timeout: 5000 });
  const dlgText = (await dlg1.textContent())?.trim();
  await dlg1.getByRole("button", { name: "Cancel" }).click();
  const afterCancel = (await api("GET", "/api/agent-backends/codex-a/codex-auth")).json;

  // Confirm Clear -> DELETE succeeds & set becomes false
  await page.getByTestId("clear-codex-auth-codex-a").click();
  const dlg2 = page.getByRole("dialog");
  await dlg2.waitFor({ state: "visible", timeout: 5000 });
  const pClear = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/agent-backends/codex-a/codex-auth") &&
      r.request().method() === "DELETE",
  );
  await dlg2.getByRole("button", { name: /clear/i }).click();
  const clearRes = await pClear;
  await page.waitForTimeout(300);
  const afterClear = (await api("GET", "/api/agent-backends/codex-a/codex-auth")).json;

  // Dispatch on codex-a after Clear -> must NOT see auth.json
  const runA2 = await dispatchCodexTask(
    "gpt-5.1-codex-walk-a",
    "Walkthrough Item 4: Codex connection A after Clear login",
  );
  const aClearedOnDispatch =
    runA2.probe?.auth_account !== "acct-walk-codex-a" &&
    (runA2.probe === null || runA2.probe?.auth_exists === false);

  record(
    "codex-auth-clear",
    "Clear login confirms before clearing (Cancel keeps, Confirm removes) and next codex-a dispatch has no auth.json",
    Boolean(dlgText) &&
      afterCancel?.set === true &&
      (clearRes.status() === 200 || clearRes.status() === 204) &&
      afterClear?.set === false &&
      aClearedOnDispatch,
    {
      dlgText,
      afterCancel,
      clearStatus: clearRes.status(),
      afterClear,
      runA2,
    },
  );

  // Cleanup connections (and cancel any parked/queued tasks from codex-b / codex-a2)
  for (const r of [runA1, runB, runA2]) {
    if (r?.tid) {
      await api("POST", `/api/tasks/${r.tid}/close`, { reason: "walkthrough cleanup" }).catch(() => {});
    }
  }
  await api("DELETE", "/api/deployment/agent-backends/codex-a").catch(() => {});
  await api("DELETE", "/api/deployment/agent-backends/codex-b").catch(() => {});

  record("codex-health", "zero uncaught page errors", pageErrors.length === 0, {
    pageErrors,
  });

  await browser.close();
  fs.mkdirSync(path.join(WORK_DIR, "results"), { recursive: true });
  fs.writeFileSync(
    path.join(WORK_DIR, "results/item4.json"),
    JSON.stringify({ findings }, null, 2),
  );
  const failed = findings.filter((f) => !f.ok);
  console.log(
    `\nItem 4 summary: ${findings.length - failed.length}/${findings.length} passed, ${failed.length} failed.`,
  );
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
