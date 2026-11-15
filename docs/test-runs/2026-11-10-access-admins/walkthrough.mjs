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

// End-to-end driver for docs/test-plans/2026-11-10.md §2 (items 1-5) against
// the real `grain daemon` + iapsim + ghsim started by env.sh.
//
//   cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-10-access-admins/walkthrough.mjs \
//     <mode: main|nogroup> <OUT_DIR>

import { createRequire } from "node:module";
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";

const require = createRequire(`${process.cwd()}/`);
const { chromium } = require("@playwright/test");

const [mode = "main", outDir = "/var/tmp/access-admins-out"] =
  process.argv.slice(2);
mkdirSync(outDir, { recursive: true });

const PROXY = process.env.PROXY || "http://127.0.0.1:4293";
const DAEMON = process.env.DAEMON || "http://127.0.0.1:4291";
const ISSUER = process.env.ISSUER || "http://127.0.0.1:4292";
const AUD = process.env.AUD || "/projects/123456/global/backendServices/789012";
const ISS = process.env.ISS || "https://iap.test.local";
const CODE = (process.env.CODE || "").trim();
const DB = process.env.DB || "";
const WORK = process.env.WORK || "";
const GRAIN = process.env.GRAIN || "/home/debian/bin/grain";

if (!CODE) {
  throw new Error("CODE env var (daemon local-api-token) is required");
}

function redact(text, secrets = []) {
  let out = String(text ?? "");
  for (const s of [CODE, ...secrets].filter(Boolean)) {
    out = out.split(s).join("<REDACTED>");
  }
  out = out.replace(/gma_[A-Za-z0-9_-]+/g, "gma_<REDACTED>");
  return out;
}

async function signJWT(email) {
  const r = await fetch(
    `${ISSUER}/sign?email=${encodeURIComponent(email)}&aud=${encodeURIComponent(AUD)}&iss=${encodeURIComponent(ISS)}`,
  );
  if (!r.ok) throw new Error(`signJWT failed: ${r.status}`);
  return (await r.text()).trim();
}

async function rawReq(base, method, path, body, headers = {}) {
  const hdrs = { ...headers };
  let payload;
  if (body !== undefined && body !== null && body !== "") {
    hdrs["Content-Type"] = "application/json";
    payload = typeof body === "string" ? body : JSON.stringify(body);
  }
  const r = await fetch(`${base}${path}`, {
    method,
    headers: hdrs,
    body: payload,
  });
  const text = await r.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    json = null;
  }
  return {
    status: r.status,
    code: json?.code || "",
    message: json?.error || text.trim(),
    json,
  };
}

const asCode = (method, path, body, extraHeaders = {}) =>
  rawReq(DAEMON, method, path, body, {
    Authorization: `Bearer ${CODE}`,
    ...extraHeaders,
  });

const asIAP = async (email, method, path, body, extraHeaders = {}) =>
  rawReq(DAEMON, method, path, body, {
    "X-Goog-IAP-JWT-Assertion": await signJWT(email),
    ...extraHeaders,
  });

const asActingAgent = (taskId, method, path, body, reason = "agent probe") =>
  rawReq(DAEMON, method, path, body, {
    Authorization: `Bearer ${CODE}`,
    "X-Grain-Acting-Task": String(taskId),
    ...(reason ? { "X-Grain-Acting-Reason": reason } : {}),
  });

const asGma = (token, method, path, body, reason = "external agent probe") =>
  rawReq(DAEMON, method, path, body, {
    Authorization: `Bearer ${token}`,
    ...(reason ? { "X-Grain-Acting-Reason": reason } : {}),
  });

function runCLI(server, token, args, extraEnv = {}) {
  try {
    const stdout = execFileSync(
      GRAIN,
      ["-server", server, "-token", token, ...args],
      {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "pipe"],
        env: {
          ...process.env,
          GRAIN_SANDBOX_CLI: "0",
          GRAIN_SERVER: "",
          GRAIN_TOKEN: "",
          GRAIN_ID_TOKEN: "",
          ...extraEnv,
        },
      },
    );
    return { exit: 0, output: stdout.trim() };
  } catch (err) {
    const out = `${err.stdout || ""}${err.stderr || ""}`.trim();
    return { exit: err.status ?? 1, output: out };
  }
}

function sqlJSON(query) {
  const out = execFileSync("sqlite3", ["-json", DB, query], {
    encoding: "utf8",
  }).trim();
  return out ? JSON.parse(out) : [];
}

const checks = [];
function record(section, id, desc, ok, details = {}) {
  const clean = JSON.parse(redact(JSON.stringify(details)));
  checks.push({ section, id, desc, ok: Boolean(ok), details: clean });
  console.log(
    `${ok ? "PASS" : "FAIL"}\t[${section}] ${id}\t${desc}\t${redact(JSON.stringify(details)).slice(0, 220)}`,
  );
}

async function seedScopeSetsAndRepos() {
  const sets = [
    {
      name: "widgets-admin",
      description: "Tasks admin limited to acme/widgets",
      scopes: ["tasks:read", "tasks:write", "tasks:admin"],
      repos: ["acme/widgets"],
      version: 1,
    },
    {
      name: "carol-limited",
      description: "Carol: tasks:admin@acme/widgets + dispatch:write",
      scopes: [
        "tasks:read",
        "repos:read",
        "templates:read",
        "dispatch:read",
        "dispatch:write",
      ],
      includes: ["widgets-admin"],
      version: 1,
    },
    {
      name: "viewer-no-secrets",
      description: "Viewer denying secrets:read",
      includes: ["grain/viewer"],
      deny: ["secrets:read"],
      version: 1,
    },
    {
      name: "operator-no-secrets",
      description: "Operator denying secrets:read",
      includes: ["grain/operator"],
      deny: ["secrets:read"],
      version: 1,
    },
  ];
  for (const set of sets) {
    const r = await asCode("PUT", "/api/api-scope-sets", { set });
    if (r.status !== 200) {
      throw new Error(
        `PUT /api/api-scope-sets ${set.name}: ${r.status} ${r.message}`,
      );
    }
  }
  await asCode("PUT", "/api/settings", { externalAgentApi: true });
  await asCode("PUT", "/api/repos/acme/widgets/declarations", {
    declarations: true,
  });
  // Wait until repo:template:acme:widgets:review is synced.
  for (let i = 0; i < 40; i++) {
    const t = await asCode("GET", "/api/templates");
    const list = Array.isArray(t.json) ? t.json : t.json?.templates || [];
    if (list.some((x) => x.id === "repo:template:acme:widgets:review")) break;
    await new Promise((r) => setTimeout(r, 250));
  }
}

async function resetPrincipals(spec) {
  const cur = await asCode("GET", "/api/access/principals");
  for (const e of cur.json?.entries || []) {
    await asCode(
      "DELETE",
      `/api/access/principals/${encodeURIComponent(e.entry)}?reason=reset`,
    );
  }
  for (const [entry, role] of spec) {
    const r = await asCode("POST", "/api/access/principals", {
      entry,
      role,
      reason: "seed",
    });
    if (r.status !== 201 && r.status !== 200) {
      throw new Error(
        `seed principal ${entry}=${role}: ${r.status} ${r.message}`,
      );
    }
  }
}

const BASE_PRINCIPALS = [
  ["user:alice@example.com", "grain/operator"],
  ["user:bob@example.com", "grain/operator"],
  ["group:ops@example.com", "grain/dispatch-control"],
  ["user:carol@example.com", "carol-limited"],
];

async function mintExternalToken(name, scopes, owner = "") {
  const body = { name, scopes };
  if (owner) body.owner = owner;
  const r = await asCode("POST", "/api/external-agents/tokens", body);
  if (r.status !== 201 && r.status !== 200) {
    throw new Error(`mint token ${name}: ${r.status} ${r.message}`);
  }
  return r.json.token;
}

async function openSignedInPage(
  browser,
  who,
  viewport = { width: 1440, height: 900 },
  colorScheme = "light",
) {
  const context = await browser.newContext({ viewport, colorScheme });
  const page = await context.newPage();
  const pageErrors = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));
  if (who === "access-code") {
    await context.setExtraHTTPHeaders({ Authorization: `Bearer ${CODE}` });
    await page.goto(`${DAEMON}/`);
  } else {
    await page.goto(
      `${PROXY}/_iapsim/login?email=${encodeURIComponent(who)}&next=/`,
    );
  }
  return { context, page, pageErrors };
}

// Drive one role/remove change through Settings -> Access UI in Chromium and
// return { outcome, code, message } observed from the UI + HTTP response.
async function uiAttemptAccessChange(
  browser,
  caller,
  { kind, entry, toRole, reason, confirmSelf },
) {
  if (caller === "agent") {
    return {
      outcome: "n/a-no-browser-session",
      status: 403,
      code: "",
      message:
        "agents do not sign in to the browser UI; verified over REST and CLI",
    };
  }
  const email =
    caller === "access-code" ? "access-code" : `${caller}@example.com`;
  const { context, page } = await openSignedInPage(browser, email);
  try {
    const base = caller === "access-code" ? DAEMON : PROXY;
    let settingsForbidden = null;
    let principalsForbidden = null;
    page.on("response", async (resp) => {
      if (resp.url().includes("/api/settings") && resp.status() === 403) {
        settingsForbidden = await resp.json().catch(() => ({}));
      }
      if (
        resp.url().includes("/api/access/principals") &&
        resp.request().method() === "GET" &&
        resp.status() === 403
      ) {
        principalsForbidden = await resp.json().catch(() => ({}));
      }
    });
    await page.goto(`${base}/settings/access`);
    await page.waitForTimeout(400);

    // Check if SettingsPage blocked AccessSection or caller has no write controls.
    const readOnlyNote = page.getByTestId("access-read-only");
    if ((await readOnlyNote.count()) > 0) {
      const text = (await readOnlyNote.innerText()).trim();
      return {
        outcome: "gated-ui",
        status: 403,
        code: "ui_gated",
        message: text,
        settings403: Boolean(settingsForbidden),
        principals403: Boolean(principalsForbidden),
      };
    }
    const settingsErrAlert = page.getByText(
      /Deployment settings could not be read/,
    );
    if ((await settingsErrAlert.count()) > 0) {
      return {
        outcome: "blocked-by-settings-read",
        status: 403,
        code: "settings_read_bug",
        message: (await settingsErrAlert.first().innerText()).trim(),
      };
    }

    const row = page.getByTestId(`principal-row-${entry}`);
    await row.waitFor({ timeout: 5000 });

    const responses = [];
    const onResp = async (r) => {
      const m = r.request().method();
      if (
        r.url().includes("/api/access/principals/") &&
        (m === "PATCH" || m === "DELETE")
      ) {
        const body = await r.json().catch(() => ({}));
        responses.push({
          status: r.status(),
          code: body?.code || "",
          message: body?.error || "",
        });
      }
    };
    page.on("response", onResp);

    if (kind === "remove") {
      await row.getByRole("button", { name: `Remove ${entry}` }).click();
    } else {
      await row.getByRole("combobox").click();
      await page.getByRole("option", { name: toRole, exact: true }).click();
    }
    await page.waitForTimeout(250);

    // If the reason/warning panel opened before sending, fill reason and submit.
    const panel = page.locator(
      '[data-testid="access-change-panel"], [data-testid="access-remove-warning"]',
    );
    if (responses.length === 0 && (await panel.count()) > 0) {
      if (reason) {
        await page.getByTestId("access-change-reason").fill(reason);
      }
      await page.getByTestId("access-change-apply").click();
      await page.waitForTimeout(300);
    }

    // If server answered confirm_self and caller wants to confirm, click confirm!
    if (
      confirmSelf &&
      responses.length > 0 &&
      responses[responses.length - 1].code === "confirm_self"
    ) {
      const confirmBtn = page.getByRole("button", {
        name: "Yes, narrow my own access",
      });
      if ((await confirmBtn.count()) > 0) {
        await confirmBtn.click();
        await page.waitForTimeout(350);
      }
    }

    page.off("response", onResp);
    const last = responses[responses.length - 1] || {
      status: 0,
      code: "",
      message: "no request sent",
    };
    const panelErr =
      (await page.getByTestId("access-change-error").count()) > 0
        ? (await page.getByTestId("access-change-error").innerText()).trim()
        : (await page.getByTestId("access-confirm-self").count()) > 0
          ? (await page.getByTestId("access-confirm-self").innerText()).trim()
          : "";
    return {
      outcome: last.status >= 200 && last.status < 300 ? "accepted" : "refused",
      status: last.status,
      code: last.code,
      message: last.message,
      panelText: panelErr,
    };
  } finally {
    await context.close();
  }
}

async function runItem1Matrix(browser, gmaAccessWriteToken) {
  const matrix = [];

  // Seed approved task 1 as verified human alice with api#access:read + api#access:write for acting-agent probes.
  await asIAP("alice@example.com", "POST", "/api/tasks", {
    title: "Seeded agent task",
    repo: "acme/widgets",
    approved: true,
    capabilities: [
      "https://grain.dev/capabilities/api#access:read",
      "https://grain.dev/capabilities/api#access:write",
    ],
  });

  const maxHistId = async () => {
    const h = (await asCode("GET", "/api/access/history")).json || [];
    return h.length > 0 ? h[0].id : 0;
  };
  const newRowsSince = async (prevMaxId) => {
    const h = (await asCode("GET", "/api/access/history")).json || [];
    return {
      count: h.filter((r) => r.id > prevMaxId).length,
      latest: h[0] || null,
    };
  };

  // Define each change scenario with a deterministic pre-state setup!
  const scenarios = [
    {
      id: "other:narrow",
      desc: "Narrow another entry (user:dave@example.com grain/operator -> grain/viewer)",
      setup: [...BASE_PRINCIPALS, ["user:dave@example.com", "grain/operator"]],
      target: () => "user:dave@example.com",
      kind: "role",
      toRole: "grain/viewer",
      reason: "narrow dave",
      confirmSelf: false,
    },
    {
      id: "other:widen",
      desc: "Widen another entry (user:dave@example.com grain/viewer -> grain/operator)",
      setup: [...BASE_PRINCIPALS, ["user:dave@example.com", "grain/viewer"]],
      target: () => "user:dave@example.com",
      kind: "role",
      toRole: "grain/operator",
      reason: "widen dave",
      confirmSelf: false,
    },
    {
      id: "other:add-deny",
      desc: "Add a deny to another entry (user:dave@example.com grain/viewer -> viewer-no-secrets)",
      setup: [...BASE_PRINCIPALS, ["user:dave@example.com", "grain/viewer"]],
      target: () => "user:dave@example.com",
      kind: "role",
      toRole: "viewer-no-secrets",
      reason: "add deny on dave",
      confirmSelf: false,
    },
    {
      id: "other:lift-deny",
      desc: "Lift a deny from another entry (user:dave@example.com viewer-no-secrets -> grain/viewer)",
      setup: [
        ...BASE_PRINCIPALS,
        ["user:dave@example.com", "viewer-no-secrets"],
      ],
      target: () => "user:dave@example.com",
      kind: "role",
      toRole: "grain/viewer",
      reason: "lift deny on dave",
      confirmSelf: false,
    },
    {
      id: "self-direct:narrow-unconfirmed",
      desc: "Narrow own user: entry without confirmSelf (-> grain/viewer)",
      setupFor: (caller) => {
        if (caller === "alice" || caller === "bob" || caller === "carol") {
          return BASE_PRINCIPALS;
        }
        return [
          ...BASE_PRINCIPALS,
          ["user:dave@example.com", "grain/operator"],
        ];
      },
      target: (caller) =>
        caller === "alice" || caller === "bob" || caller === "carol"
          ? `user:${caller}@example.com`
          : "user:dave@example.com",
      kind: "role",
      toRole: "grain/viewer",
      reason: "self narrow unconfirmed",
      confirmSelf: false,
    },
    {
      id: "self-direct:narrow-confirmed",
      desc: "Narrow own user: entry WITH confirmSelf (-> grain/viewer)",
      setupFor: () => BASE_PRINCIPALS,
      target: (caller) =>
        caller === "alice" || caller === "bob" || caller === "carol"
          ? `user:${caller}@example.com`
          : "user:bob@example.com",
      kind: "role",
      toRole: "grain/viewer",
      reason: "self narrow confirmed",
      confirmSelf: true,
    },
    {
      id: "self-direct:widen",
      desc: "Widen own user: entry (grain/contributor -> grain/operator, even if caller holds full admin via group)",
      setupFor: (caller) => {
        if (caller === "alice") {
          return [
            ["user:alice@example.com", "grain/contributor"],
            ["user:bob@example.com", "grain/operator"],
            ["group:ops@example.com", "grain/dispatch-control"],
            ["user:carol@example.com", "carol-limited"],
          ];
        }
        if (caller === "bob") {
          // bob holds full grain/operator via group:ops@example.com, yet still cannot widen user:bob@example.com!
          return [
            ["user:alice@example.com", "grain/operator"],
            ["user:bob@example.com", "grain/contributor"],
            ["group:ops@example.com", "grain/operator"],
            ["user:carol@example.com", "carol-limited"],
          ];
        }
        return BASE_PRINCIPALS;
      },
      target: (caller) =>
        caller === "alice" || caller === "bob" || caller === "carol"
          ? `user:${caller}@example.com`
          : "user:carol@example.com",
      kind: "role",
      toRole: "grain/operator",
      reason: "self widen attempt",
      confirmSelf: true,
    },
    {
      id: "self-direct:add-deny",
      desc: "Add a deny to own user: entry (-> operator-no-secrets)",
      setupFor: () => BASE_PRINCIPALS,
      target: (caller) =>
        caller === "alice" || caller === "bob" || caller === "carol"
          ? `user:${caller}@example.com`
          : "user:bob@example.com",
      kind: "role",
      toRole: "operator-no-secrets",
      reason: "self add deny",
      confirmSelf: true,
    },
    {
      id: "self-direct:lift-deny",
      desc: "Lift a deny from own user: entry (operator-no-secrets -> grain/operator)",
      setupFor: (caller) => [
        [
          "user:alice@example.com",
          caller === "alice" ? "operator-no-secrets" : "grain/operator",
        ],
        [
          "user:bob@example.com",
          caller === "bob" || caller === "agent" || caller === "access-code"
            ? "operator-no-secrets"
            : "grain/operator",
        ],
        ["group:ops@example.com", "grain/operator"],
        [
          "user:carol@example.com",
          caller === "carol" ? "viewer-no-secrets" : "carol-limited",
        ],
      ],
      target: (caller) =>
        caller === "alice" || caller === "bob" || caller === "carol"
          ? `user:${caller}@example.com`
          : "user:bob@example.com",
      kind: "role",
      toRole: "grain/operator",
      reason: "self lift deny",
      confirmSelf: true,
    },
    {
      id: "group:narrow-unconfirmed",
      desc: "Narrow group:ops@example.com without confirmSelf (grain/dispatch-control -> grain/pipeline-control; matches bob via group, other for alice)",
      setup: BASE_PRINCIPALS,
      target: () => "group:ops@example.com",
      kind: "role",
      toRole: "grain/pipeline-control",
      reason: "narrow ops group",
      confirmSelf: false,
    },
    {
      id: "group:narrow-confirmed",
      desc: "Narrow group:ops@example.com WITH confirmSelf (grain/dispatch-control -> grain/pipeline-control)",
      setup: BASE_PRINCIPALS,
      target: () => "group:ops@example.com",
      kind: "role",
      toRole: "grain/pipeline-control",
      reason: "narrow ops group confirmed",
      confirmSelf: true,
    },
    {
      id: "group:widen",
      desc: "Widen group:ops@example.com (grain/dispatch-control -> grain/operator; matches bob via group, other for alice)",
      setup: BASE_PRINCIPALS,
      target: () => "group:ops@example.com",
      kind: "role",
      toRole: "grain/operator",
      reason: "widen ops group",
      confirmSelf: true,
    },
    {
      id: "group:add-deny",
      desc: "Add deny to group:ops@example.com (grain/dispatch-control -> viewer-no-secrets; matches bob via group, other for alice)",
      setup: BASE_PRINCIPALS,
      target: () => "group:ops@example.com",
      kind: "role",
      toRole: "viewer-no-secrets",
      reason: "add deny on ops group",
      confirmSelf: true,
    },
    {
      id: "group:lift-deny",
      desc: "Lift deny from group:ops@example.com (viewer-no-secrets -> grain/dispatch-control; matches bob via group, other for alice)",
      setup: [
        ["user:alice@example.com", "grain/operator"],
        ["user:bob@example.com", "grain/operator"],
        ["group:ops@example.com", "viewer-no-secrets"],
        ["user:carol@example.com", "carol-limited"],
      ],
      target: () => "group:ops@example.com",
      kind: "role",
      toRole: "grain/dispatch-control",
      reason: "lift deny on ops group",
      confirmSelf: true,
    },
    {
      id: "last-admin:remove",
      desc: "Remove the last full admin (user:alice@example.com when bob is grain/viewer)",
      setup: [
        ["user:alice@example.com", "grain/operator"],
        ["user:bob@example.com", "grain/viewer"],
        ["group:ops@example.com", "grain/dispatch-control"],
        ["user:carol@example.com", "carol-limited"],
      ],
      target: () => "user:alice@example.com",
      kind: "remove",
      reason: "remove last admin",
      confirmSelf: true,
    },
  ];

  const callers = ["alice", "bob", "carol", "agent", "access-code"];

  for (const sc of scenarios) {
    for (const caller of callers) {
      const setupSpec = sc.setupFor ? sc.setupFor(caller) : sc.setup;
      const entry = sc.target(caller);

      // 1) REST surface
      await resetPrincipals(setupSpec);
      const idBeforeRest = await maxHistId();
      let restRes;
      const restPath =
        sc.kind === "remove"
          ? `/api/access/principals/${encodeURIComponent(entry)}?reason=${encodeURIComponent(sc.reason)}${sc.confirmSelf ? "&confirmSelf=1" : ""}`
          : `/api/access/principals/${encodeURIComponent(entry)}`;
      const restMethod = sc.kind === "remove" ? "DELETE" : "PATCH";
      const restBody =
        sc.kind === "remove"
          ? null
          : {
              role: sc.toRole,
              reason: sc.reason,
              ...(sc.confirmSelf ? { confirmSelf: true } : {}),
            };

      if (caller === "access-code") {
        restRes = await asCode(restMethod, restPath, restBody);
      } else if (caller === "agent") {
        const gmaRes = await asGma(
          gmaAccessWriteToken,
          restMethod,
          restPath,
          restBody,
          sc.reason,
        );
        const taskRes = await asActingAgent(
          1,
          restMethod,
          restPath,
          restBody,
          sc.reason,
        );
        restRes = {
          ...taskRes,
          gmaStatus: gmaRes.status,
          gmaMessage: gmaRes.message,
        };
      } else {
        restRes = await asIAP(
          `${caller}@example.com`,
          restMethod,
          restPath,
          restBody,
        );
      }
      const afterRest = await newRowsSince(idBeforeRest);
      const restAccepted = restRes.status >= 200 && restRes.status < 300;
      const restAuditDelta = afterRest.count;

      // 2) CLI (`grain access`) surface
      await resetPrincipals(setupSpec);
      const idBeforeCLI = await maxHistId();
      const cliArgs = ["access", sc.kind === "remove" ? "deny" : "role"];
      if (sc.reason) cliArgs.push("--reason", sc.reason);
      if (sc.confirmSelf) cliArgs.push("--confirm-self");
      cliArgs.push(entry);
      if (sc.kind === "role") cliArgs.push(sc.toRole);

      let cliRes;
      if (caller === "access-code") {
        cliRes = runCLI(DAEMON, CODE, cliArgs);
      } else if (caller === "agent") {
        cliRes = runCLI(DAEMON, gmaAccessWriteToken, cliArgs);
      } else {
        cliRes = runCLI(PROXY, `iapsim-idtoken.${caller}@example.com`, cliArgs);
      }
      const afterCLI = await newRowsSince(idBeforeCLI);
      const cliAccepted = cliRes.exit === 0;
      const cliAuditDelta = afterCLI.count;

      // 3) UI (Settings -> Access) surface
      await resetPrincipals(setupSpec);
      const idBeforeUI = await maxHistId();
      const uiRes = await uiAttemptAccessChange(browser, caller, {
        kind: sc.kind,
        entry,
        toRole: sc.toRole,
        reason: sc.reason,
        confirmSelf: sc.confirmSelf,
      });
      const afterUI = await newRowsSince(idBeforeUI);
      const uiAccepted = uiRes.outcome === "accepted";
      const uiAuditDelta = afterUI.count;

      const surfacesAgree =
        restAccepted === cliAccepted &&
        (caller === "agent" || restAccepted === uiAccepted) &&
        uiRes.code !== "settings_read_bug";
      const auditConsistent =
        restAuditDelta === (restAccepted ? 1 : 0) &&
        cliAuditDelta === (cliAccepted ? 1 : 0) &&
        (caller === "agent" || uiAuditDelta === (uiAccepted ? 1 : 0));

      record(
        "item1",
        `${sc.id}:${caller}`,
        `${sc.id} as ${caller}`,
        surfacesAgree && auditConsistent,
        {
          entry,
          rest: {
            status: restRes.status,
            code: restRes.code,
            message: restRes.message,
            auditDelta: restAuditDelta,
          },
          cli: {
            exit: cliRes.exit,
            output: cliRes.output,
            auditDelta: cliAuditDelta,
          },
          ui: {
            outcome: uiRes.outcome,
            status: uiRes.status,
            code: uiRes.code,
            message: uiRes.message,
            auditDelta: uiAuditDelta,
          },
          latestAudit: restAccepted ? afterRest.latest?.summary : null,
        },
      );

      matrix.push({
        scenario: sc.id,
        description: sc.desc,
        caller,
        entry,
        rest: {
          outcome: restAccepted ? "accepted" : "refused",
          status: restRes.status,
          code: restRes.code,
          message: redact(restRes.message),
          auditDelta: restAuditDelta,
        },
        cli: {
          outcome: cliAccepted ? "accepted" : "refused",
          exit: cliRes.exit,
          output: redact(cliRes.output),
          auditDelta: cliAuditDelta,
        },
        ui: {
          outcome: uiRes.outcome,
          status: uiRes.status,
          code: uiRes.code,
          message: redact(uiRes.message),
          panelText: redact(uiRes.panelText || ""),
          auditDelta: uiAuditDelta,
        },
        surfacesAgree,
        auditConsistent,
      });
    }
  }
  writeFileSync(`${outDir}/item1-matrix.json`, JSON.stringify(matrix, null, 2));
}

async function runItem2Bypasses(gmaAccessWriteToken) {
  // 2a. Group-admitted admin tries to POST a direct user: entry for themselves,
  //     and DELETE?confirmSelf=1 on own entry followed by POST wider!
  await resetPrincipals([
    ["user:alice@example.com", "grain/operator"],
    ["group:ops@example.com", "grain/operator"],
  ]);
  // temp@example.com is ONLY admitted via group:ops@example.com (grain/operator).
  const entrenchDirect = await asIAP(
    "temp@example.com",
    "POST",
    "/api/access/principals",
    {
      entry: "user:temp@example.com",
      role: "grain/operator",
      reason: "persist after group removal",
    },
  );
  record(
    "item2",
    "2a-group-to-direct-post",
    "Group-admitted temp@example.com POST user:temp@example.com refused self_escalation",
    entrenchDirect.status === 403 && entrenchDirect.code === "self_escalation",
    entrenchDirect,
  );

  // bob@example.com has user:bob@example.com (grain/contributor) AND group:ops@example.com (grain/operator):
  // tries DELETE ?confirmSelf=1 on user:bob@example.com, then POST user:bob@example.com as grain/operator!
  await resetPrincipals([
    ["user:alice@example.com", "grain/operator"],
    ["user:bob@example.com", "grain/contributor"],
    ["group:ops@example.com", "grain/operator"],
  ]);
  const delOwn = await asIAP(
    "bob@example.com",
    "DELETE",
    `/api/access/principals/${encodeURIComponent("user:bob@example.com")}?confirmSelf=1&reason=drop`,
  );
  const rePostWider = await asIAP(
    "bob@example.com",
    "POST",
    "/api/access/principals",
    {
      entry: "user:bob@example.com",
      role: "grain/operator",
      reason: "re-add wider",
    },
  );
  record(
    "item2",
    "2a-delete-then-post-user",
    "DELETE ?confirmSelf=1 on user:bob then POST wider refused self_escalation",
    delOwn.status === 200 &&
      rePostWider.status === 403 &&
      rePostWider.code === "self_escalation",
    { delOwn, rePostWider },
  );

  // Also DELETE ?confirmSelf=1 on own group entry (group:ops@example.com as grain/viewer) by bob (full admin via user:bob), then POST group:ops@example.com wider!
  await resetPrincipals([
    ["user:alice@example.com", "grain/operator"],
    ["user:bob@example.com", "grain/operator"],
    ["group:ops@example.com", "grain/viewer"],
  ]);
  const delGroup = await asIAP(
    "bob@example.com",
    "DELETE",
    `/api/access/principals/${encodeURIComponent("group:ops@example.com")}?confirmSelf=1`,
  );
  const rePostGroupWider = await asIAP(
    "bob@example.com",
    "POST",
    "/api/access/principals",
    {
      entry: "group:ops@example.com",
      role: "grain/dispatch-control",
      reason: "re-add group wider",
    },
  );
  const rePostDirectFromGroup = await asIAP(
    "temp@example.com",
    "POST",
    "/api/access/principals",
    {
      entry: "user:temp@example.com",
      role: "grain/operator",
      reason: "after group deleted",
    },
  );
  record(
    "item2",
    "2a-delete-group-then-post",
    "DELETE ?confirmSelf=1 on group:ops then POST group wider (bob: 403 self_escalation) and POST direct user (temp: 403)",
    delGroup.status === 200 &&
      rePostGroupWider.status === 403 &&
      rePostGroupWider.code === "self_escalation" &&
      rePostDirectFromGroup.status === 403,
    { delGroup, rePostGroupWider, rePostDirectFromGroup },
  );

  // 2b. POST of an already-listed entry from an agent token (both acting-task and gma_).
  await resetPrincipals(BASE_PRINCIPALS);
  const agentIdemTask = await asActingAgent(
    1,
    "POST",
    "/api/access/principals",
    {
      entry: "user:alice@example.com",
      role: "grain/operator",
      reason: "idempotent probe",
    },
  );
  const agentIdemGma = await asGma(
    gmaAccessWriteToken,
    "POST",
    "/api/access/principals",
    {
      entry: "user:alice@example.com",
      role: "grain/operator",
      reason: "idempotent probe",
    },
  );
  record(
    "item2",
    "2b-agent-post-listed",
    "POST of already-listed entry from both acting-task agent and gma_ token gets 403 (not 200 idempotent)",
    agentIdemTask.status === 403 && agentIdemGma.status === 403,
    { agentIdemTask, agentIdemGma },
  );

  // 2d. Reason containing \r\n and ANSI escapes (CSI + OSC), plus pure-ANSI reason!
  const dirtyReason =
    "rotate\r\nforged-line\x1b[31mRED\x1b[0m\x1b]52;c;dGVzdA==\x07end";
  const patchDirty = await asIAP(
    "alice@example.com",
    "PATCH",
    `/api/access/principals/${encodeURIComponent("user:bob@example.com")}`,
    { role: "grain/viewer", reason: dirtyReason },
  );
  const pureEscape = await asIAP(
    "alice@example.com",
    "PATCH",
    `/api/access/principals/${encodeURIComponent("user:bob@example.com")}`,
    { role: "grain/operator", reason: "\x1b[31m\x1b[0m\r\n" },
  );
  const hist = (await asCode("GET", "/api/access/history")).json;
  const auditRow = hist[0] || {};
  const dbRows = sqlJSON(
    "SELECT summary FROM config_audit WHERE surface='access' ORDER BY id DESC LIMIT 3",
  );
  const daemonLog = readFileSync(`${WORK}/daemon.log`, "utf8");
  const accessLogLines = daemonLog
    .split("\n")
    .filter((l) => l.includes("grain access:"));
  const lastLogLine = accessLogLines[accessLogLines.length - 1] || "";

  const hasCRLF =
    /[\r\n]/.test(auditRow.summary || "") ||
    dbRows.some((r) => /[\r\n]/.test(r.summary || ""));
  const hasANSIOrCtrl =
    // eslint-disable-next-line no-control-regex
    /[\x00-\x1f\x7f]/.test(auditRow.summary || "") ||
    // eslint-disable-next-line no-control-regex
    /[\x00-\x1f\x7f]/.test(lastLogLine);

  record(
    "item2",
    "2d-crlf-ansi-reason",
    "Reason with \\r\\n and ANSI escapes leaves no CR/LF or escape/control bytes in audit row or daemon log, and pure-ANSI reason is refused",
    patchDirty.status === 200 &&
      !hasCRLF &&
      !hasANSIOrCtrl &&
      pureEscape.status === 400,
    {
      patchDirtyStatus: patchDirty.status,
      pureEscapeStatus: pureEscape.status,
      pureEscapeMsg: pureEscape.message,
      auditSummary: JSON.stringify(auditRow.summary),
      lastLogLine: JSON.stringify(lastLogLine),
      hasCRLF,
      hasANSIOrCtrl,
    },
  );
}

async function runItem2NoGroupChecker() {
  await seedScopeSetsAndRepos();
  await resetPrincipals([
    ["user:alice@example.com", "grain/operator"],
    ["user:bob@example.com", "grain/operator"],
    ["group:ops@example.com", "grain/viewer"],
  ]);

  // Group-only member temp@example.com cannot sign in with no group checker.
  const tempWhoami = await asIAP("temp@example.com", "GET", "/api/whoami");
  // alice (full admin, NOT in group:ops) trying to widen/POST/deny/narrow group:ops fails closed as matching!
  const aliceWidenGroup = await asIAP(
    "alice@example.com",
    "PATCH",
    `/api/access/principals/${encodeURIComponent("group:ops@example.com")}`,
    { role: "grain/dispatch-control", reason: "try widen" },
  );
  const alicePostNewGroup = await asIAP(
    "alice@example.com",
    "POST",
    "/api/access/principals",
    {
      entry: "group:newteam@example.com",
      role: "grain/viewer",
      reason: "try post group",
    },
  );
  const aliceAddDenyGroup = await asIAP(
    "alice@example.com",
    "PATCH",
    `/api/access/principals/${encodeURIComponent("group:ops@example.com")}`,
    { role: "viewer-no-secrets", reason: "try deny" },
  );
  const aliceDelGroupUnconf = await asIAP(
    "alice@example.com",
    "DELETE",
    `/api/access/principals/${encodeURIComponent("group:ops@example.com")}`,
  );
  const aliceDelGroupConf = await asIAP(
    "alice@example.com",
    "DELETE",
    `/api/access/principals/${encodeURIComponent("group:ops@example.com")}?confirmSelf=1`,
  );

  // Also if a group has a deny role (added via access code), even alice's session fails closed!
  await asCode("POST", "/api/access/principals", {
    entry: "group:veto@example.com",
    role: "viewer-no-secrets",
    reason: "veto group",
  });
  const aliceWithUncheckedDenyGroup = await asIAP(
    "alice@example.com",
    "GET",
    "/api/tasks",
  );

  record(
    "item2",
    "2c-no-group-checker",
    "Daemon with group entries and no group checker fails closed on membership, group edits, and deny groups",
    tempWhoami.status === 403 &&
      aliceWidenGroup.status === 403 &&
      aliceWidenGroup.code === "self_escalation" &&
      aliceWidenGroup.message.includes("membership could not be checked") &&
      alicePostNewGroup.status === 403 &&
      alicePostNewGroup.code === "self_escalation" &&
      aliceAddDenyGroup.status === 403 &&
      aliceAddDenyGroup.code === "self_access_change" &&
      aliceDelGroupUnconf.status === 403 &&
      aliceDelGroupUnconf.code === "confirm_self" &&
      aliceDelGroupConf.status === 200 &&
      aliceWithUncheckedDenyGroup.status === 403,
    {
      tempWhoamiStatus: tempWhoami.status,
      aliceWidenGroup,
      alicePostNewGroup,
      aliceAddDenyGroup,
      aliceDelGroupUnconf,
      aliceDelGroupConfStatus: aliceDelGroupConf.status,
      aliceWithUncheckedDenyGroup,
    },
  );
}

async function runItem3SelfLockoutUX(browser) {
  // 3a. As bob (sole entry for bob: user:bob@example.com = grain/operator, alongside user:alice = grain/operator),
  // narrow user:bob@example.com -> grain/viewer in Chromium WITHOUT reloading the page!
  await resetPrincipals([
    ["user:alice@example.com", "grain/operator"],
    ["user:bob@example.com", "grain/operator"],
  ]);
  {
    const { context, page } = await openSignedInPage(
      browser,
      "bob@example.com",
    );
    try {
      await page.goto(`${PROXY}/settings/access`);
      const row = page.getByTestId("principal-row-user:bob@example.com");
      await row.waitFor({ timeout: 5000 });
      await row.getByRole("combobox").click();
      await page
        .getByRole("option", { name: "grain/viewer", exact: true })
        .click();

      const panel = page.getByTestId("access-change-panel");
      await panel.waitFor({ timeout: 5000 });
      await page
        .getByTestId("access-change-reason")
        .fill("stepping down to viewer");
      await page.getByTestId("access-change-apply").click();

      // Expect confirm_self prompt on the panel!
      const confirmBox = page.getByTestId("access-confirm-self");
      await confirmBox.waitFor({ timeout: 5000 });
      const confirmText = (await confirmBox.innerText()).trim();

      await page
        .getByRole("button", { name: "Yes, narrow my own access" })
        .click();
      await page.waitForTimeout(500);

      // WITHOUT page.reload(), check whether the page reflects bob's reduced rights!
      const ownRoleText =
        (await page.getByTestId("access-own-role").count()) > 0
          ? (await page.getByTestId("access-own-role").innerText()).trim()
          : "";
      const readOnlyVisible =
        (await page.getByTestId("access-read-only").count()) > 0;
      const addBtnStillVisible =
        (await page
          .getByRole("button", { name: "Add allowed principal" })
          .count()) > 0;
      await page.screenshot({
        path: `${outDir}/item3-bob-narrowed-viewer.png`,
      });

      record(
        "item3",
        "3a-bob-narrow-to-viewer-no-reload",
        "Bob narrows own entry to grain/viewer: confirm_self appears, applies, and page reflects reduced rights without reload",
        confirmText.includes("narrows your own access") &&
          ownRoleText.includes("grain/viewer") &&
          readOnlyVisible &&
          !addBtnStillVisible,
        {
          confirmText,
          ownRoleText,
          readOnlyVisible,
          addBtnStillVisible,
        },
      );
    } finally {
      await context.close();
    }
  }

  // 3b. As bob, narrow user:bob@example.com -> carol-limited (which lacks both settings:read and access:read!)
  await resetPrincipals([
    ["user:alice@example.com", "grain/operator"],
    ["user:bob@example.com", "grain/operator"],
  ]);
  {
    const { context, page } = await openSignedInPage(
      browser,
      "bob@example.com",
    );
    try {
      const unexpected403s = [];
      page.on("response", (r) => {
        if (r.status() === 403 && r.request().method() === "GET") {
          unexpected403s.push(`${r.request().method()} ${r.url()}`);
        }
      });
      await page.goto(`${PROXY}/settings/access`);
      const row = page.getByTestId("principal-row-user:bob@example.com");
      await row.waitFor({ timeout: 5000 });
      await row.getByRole("combobox").click();
      await page
        .getByRole("option", { name: "carol-limited", exact: true })
        .click();
      await page
        .getByTestId("access-change-reason")
        .fill("narrowing to carol-limited");
      await page.getByTestId("access-change-apply").click();
      await page.getByTestId("access-confirm-self").waitFor({ timeout: 5000 });
      await page
        .getByRole("button", { name: "Yes, narrow my own access" })
        .click();
      await page.waitForTimeout(500);

      const ownRoleText =
        (await page.getByTestId("access-own-role").count()) > 0
          ? (await page.getByTestId("access-own-role").innerText()).trim()
          : "";
      const tableStillShown =
        (await page.getByTestId("allowed-principals-table").count()) > 0;
      const noticeText =
        (await page.getByTestId("access-read-only").count()) > 0
          ? (await page.getByTestId("access-read-only").innerText()).trim()
          : "";
      await page.screenshot({
        path: `${outDir}/item3-bob-narrowed-no-access-read.png`,
      });

      record(
        "item3",
        "3b-bob-narrow-to-no-access-read",
        "Bob narrows own entry to role without access:read: clean notice, stale list cleared, no 403 GETs",
        ownRoleText.includes("carol-limited") &&
          !tableStillShown &&
          noticeText.includes("access:read") &&
          unexpected403s.length === 0,
        {
          ownRoleText,
          tableStillShown,
          noticeText,
          unexpected403s,
        },
      );
    } finally {
      await context.close();
    }
  }

  // 3c. Check that last_admin reaches the panel verbatim!
  await resetPrincipals([
    ["user:alice@example.com", "grain/operator"],
    ["user:bob@example.com", "grain/viewer"],
  ]);
  {
    // Exact server message from REST so we compare verbatim:
    const restLastAdmin = await asIAP(
      "alice@example.com",
      "PATCH",
      `/api/access/principals/${encodeURIComponent("user:alice@example.com")}`,
      { role: "grain/viewer", reason: "stepping down", confirmSelf: true },
    );
    const { context, page } = await openSignedInPage(
      browser,
      "alice@example.com",
    );
    try {
      await page.goto(`${PROXY}/settings/access`);
      const row = page.getByTestId("principal-row-user:alice@example.com");
      await row.waitFor({ timeout: 5000 });
      await row.getByRole("combobox").click();
      await page
        .getByRole("option", { name: "grain/viewer", exact: true })
        .click();
      await page.getByTestId("access-change-reason").fill("stepping down");
      await page.getByTestId("access-change-apply").click();
      await page.waitForTimeout(350);

      // If confirm_self appeared first, click confirm to see if last_admin arrives or if last_admin arrived right away:
      let hadConfirmSelfBeforeLastAdmin = false;
      if ((await page.getByTestId("access-confirm-self").count()) > 0) {
        hadConfirmSelfBeforeLastAdmin = true;
        await page
          .getByRole("button", { name: "Yes, narrow my own access" })
          .click();
        await page.waitForTimeout(350);
      }

      const errEl = page.getByTestId("access-change-error");
      const errText =
        (await errEl.count()) > 0 ? (await errEl.innerText()).trim() : "";
      const confirmStillStuck =
        (await page.getByTestId("access-confirm-self").count()) > 0;
      await page.screenshot({ path: `${outDir}/item3-last-admin-panel.png` });

      record(
        "item3",
        "3c-last-admin-verbatim-on-panel",
        "last_admin message reaches the panel verbatim without stale confirm_self prompt",
        errText === restLastAdmin.message &&
          !hadConfirmSelfBeforeLastAdmin &&
          !confirmStillStuck,
        {
          want: restLastAdmin.message,
          got: errText,
          hadConfirmSelfBeforeLastAdmin,
          confirmStillStuck,
        },
      );
    } finally {
      await context.close();
    }
  }
}

async function runItem4LimitedApproval(browser) {
  await resetPrincipals(BASE_PRINCIPALS);

  // Create two non-default review templates exceeding carol's role:
  // 1) heavy-review: carries a dangerous grant (api#settings:write)
  // 2) other-write-review: carries writes outside acme/widgets (acme/other)
  const t1 = await asCode("POST", "/api/templates", {
    name: "heavy-review",
    title: "Heavy security review",
    body: "Review with settings:write.",
    repo: "acme/widgets",
    capabilities: ["https://grain.dev/capabilities/api#settings:write"],
  });
  const heavyReviewID = t1.json?.id;
  const t2 = await asCode("POST", "/api/templates", {
    name: "other-write-review",
    title: "Cross-repo review",
    body: "Review writing to acme/other.",
    repo: "acme/widgets",
    writes: [{ repo: "acme/other" }],
  });
  const otherWriteReviewID = t2.json?.id;
  const defaultReviewID = "repo:template:acme:widgets:review";

  if (!heavyReviewID || !otherWriteReviewID) {
    throw new Error(
      `failed to create review templates: ${JSON.stringify({ t1, t2 })}`,
    );
  }

  async function seedProposal(label, payload) {
    const r = await asCode("POST", "/api/tasks", {
      title: `Proposal ${label}`,
      description: `Test proposal for ${label}`,
      approved: false,
      ...payload,
    });
    if (r.status !== 201) {
      throw new Error(`seedProposal ${label}: ${r.status} ${r.message}`);
    }
    const id = r.json.id;
    // Ensure unapproved even if approvedByDefault was ever set:
    const cur = (await asCode("GET", `/api/tasks/${id}`)).json;
    if (cur.approved || cur.state !== "proposed") {
      await asCode("POST", `/api/tasks/${id}/withdraw-approval`);
    }
    return id;
  }

  async function verifyTaskState(id) {
    const r = await asCode("GET", `/api/tasks/${id}`);
    return {
      state: r.json?.state,
      approved: Boolean(r.json?.approved),
      runNowAt: r.json?.runNowAt || null,
    };
  }

  const cases = [
    {
      id: "widgets-plain",
      desc: "acme/widgets without autoMerge",
      wantAccepted: true,
      payload: { repo: "acme/widgets", autoMerge: false },
    },
    {
      id: "widgets-automerge",
      desc: "acme/widgets with autoMerge",
      wantAccepted: false,
      payload: { repo: "acme/widgets", autoMerge: true },
    },
    {
      id: "widgets-dangerous-grant",
      desc: "acme/widgets with dangerous grant (api#settings:write)",
      wantAccepted: false,
      payload: {
        repo: "acme/widgets",
        capabilities: ["https://grain.dev/capabilities/api#settings:write"],
      },
    },
    {
      id: "widgets-writes-outside",
      desc: "acme/widgets with writes outside acme/widgets (acme/other)",
      wantAccepted: false,
      payload: {
        repo: "acme/widgets",
        writes: [{ repo: "acme/other" }],
      },
    },
    {
      id: "widgets-nondefault-review-grant",
      desc: "acme/widgets with non-default review template granting more than carol holds",
      wantAccepted: false,
      payload: {
        repo: "acme/widgets",
        reviewTemplateId: heavyReviewID,
      },
    },
    {
      id: "widgets-nondefault-review-writes",
      desc: "acme/widgets with non-default review template writing outside acme/widgets",
      wantAccepted: false,
      payload: {
        repo: "acme/widgets",
        reviewTemplateId: otherWriteReviewID,
      },
    },
    {
      id: "widgets-default-review",
      desc: "acme/widgets with the repo's default review template",
      wantAccepted: true,
      payload: {
        repo: "acme/widgets",
        reviewTemplateId: defaultReviewID,
      },
    },
    {
      id: "other-repo",
      desc: "on acme/other",
      wantAccepted: false,
      payload: { repo: "acme/other", autoMerge: false },
    },
  ];

  const rows = [];
  const { context, page } = await openSignedInPage(
    browser,
    "carol@example.com",
  );
  try {
    for (const c of cases) {
      for (const action of ["approve", "run-now"]) {
        // 1) REST
        const restTask = await seedProposal(
          `${c.id}-rest-${action}`,
          c.payload,
        );
        const restResp = await asIAP(
          "carol@example.com",
          "POST",
          `/api/tasks/${restTask}/${action}`,
          {},
        );
        const restAfter = await verifyTaskState(restTask);
        if (restAfter.approved) {
          await asCode("POST", `/api/tasks/${restTask}/close`, {
            close_pull_request: false,
          });
        } else {
          await asCode("POST", `/api/tasks/${restTask}/close`, {
            close_pull_request: false,
          });
        }

        // 2) CLI (`grain approve` / `grain run-now`)
        const cliTask = await seedProposal(`${c.id}-cli-${action}`, c.payload);
        const cliResp = runCLI(PROXY, "iapsim-idtoken.carol@example.com", [
          action,
          cliTask,
        ]);
        const cliAfter = await verifyTaskState(cliTask);
        await asCode("POST", `/api/tasks/${cliTask}/close`, {
          close_pull_request: false,
        });

        // 3) UI Task Pane (`/tasks/<id>`)
        const paneTask = await seedProposal(
          `${c.id}-pane-${action}`,
          c.payload,
        );
        const cfgLoaded = page.waitForResponse(
          (r) => r.url().includes("/api/config") && r.status() === 200,
        );
        await page.goto(`${PROXY}/tasks/${paneTask}`);
        await cfgLoaded;
        const btnName = action === "approve" ? "Approve" : "Run now";
        const btn = page
          .locator(".actions")
          .getByRole("button", { name: btnName, exact: true });
        await btn.waitFor({ timeout: 8000 });
        await page.waitForTimeout(100);
        const paneDisabled = await btn.isDisabled();
        let paneStatus = 0;
        let paneMessage = "";
        if (paneDisabled) {
          const gate = btn.locator("xpath=ancestor::*[@data-access-denied][1]");
          paneMessage =
            (await gate.count()) > 0
              ? `disabled by AccessGate (${await gate.getAttribute("data-access-denied")})`
              : "disabled";
        } else {
          const waitResp = page.waitForResponse(
            (r) =>
              r.url().includes(`/api/tasks/${paneTask}/${action}`) &&
              r.request().method() === "POST",
            { timeout: 5000 },
          );
          await btn.click();
          const resp = await waitResp;
          paneStatus = resp.status();
          const body = await resp.json().catch(() => ({}));
          paneMessage = body?.error || "";
          await page.waitForTimeout(250);
        }
        const paneAfter = await verifyTaskState(paneTask);
        await asCode("POST", `/api/tasks/${paneTask}/close`, {
          close_pull_request: false,
        });

        // 4) UI Inbox (`/inbox`) -- Inbox rows offer Approve on proposed tasks (no Run now button on proposed rows).
        let inboxResult = {
          note: "Inbox proposed rows offer Approve/Defer/Decline (no Run now button)",
        };
        let inboxOk = true;
        if (action === "approve") {
          const inboxTask = await seedProposal(
            `${c.id}-inbox-approve`,
            c.payload,
          );
          const inboxCfgLoaded = page.waitForResponse(
            (r) => r.url().includes("/api/config") && r.status() === 200,
          );
          await page.goto(`${PROXY}/inbox`);
          await inboxCfgLoaded;
          const itemTitle = `Proposal ${c.id}-inbox-approve`;
          await page.getByText(itemTitle).first().waitFor({ timeout: 8000 });
          await page.waitForTimeout(100);
          const rowEl = page
            .locator("li, tr, .inbox-row, [data-task-id]")
            .filter({ hasText: itemTitle })
            .first();
          const inboxBtn = rowEl.getByRole("button", {
            name: "Approve",
            exact: true,
          });
          const inboxDisabled = await inboxBtn.isDisabled();
          let inboxStatus = 0;
          let inboxMsg = "";
          if (inboxDisabled) {
            inboxMsg = "disabled by AccessGate (tasks:admin)";
          } else {
            const waitResp = page.waitForResponse(
              (r) =>
                r.url().includes(`/api/tasks/${inboxTask}/approve`) &&
                r.request().method() === "POST",
              { timeout: 5000 },
            );
            await inboxBtn.click();
            const resp = await waitResp;
            inboxStatus = resp.status();
            const body = await resp.json().catch(() => ({}));
            inboxMsg = body?.error || "";
            await page.waitForTimeout(250);
          }
          const inboxAfter = await verifyTaskState(inboxTask);
          await asCode("POST", `/api/tasks/${inboxTask}/close`, {
            close_pull_request: false,
          });
          inboxResult = {
            disabled: inboxDisabled,
            status: inboxStatus,
            message: inboxMsg,
            after: inboxAfter,
          };
          inboxOk = c.wantAccepted
            ? !inboxDisabled &&
              inboxStatus === 200 &&
              (inboxAfter.state === "queued" ||
                inboxAfter.state === "running") &&
              inboxAfter.approved
            : (inboxDisabled || inboxStatus === 403) &&
              inboxAfter.state === "proposed" &&
              !inboxAfter.approved;
        }

        const checkState = (after) =>
          c.wantAccepted
            ? (after.state === "queued" || after.state === "running") &&
              after.approved === true
            : after.state === "proposed" &&
              after.approved === false &&
              !after.runNowAt;

        const restOk =
          (c.wantAccepted
            ? restResp.status === 200
            : restResp.status === 403) && checkState(restAfter);
        const cliOk =
          (c.wantAccepted ? cliResp.exit === 0 : cliResp.exit !== 0) &&
          checkState(cliAfter);
        // On acme/other, both Approve AND Run now on an unapproved task lack tasks:admin@acme/other,
        // so AccessGate should disable Run now just like Approve (rather than leaving an enabled button that 403s!).
        const wantPaneDisabled = c.id === "other-repo";
        const paneOk = c.wantAccepted
          ? !paneDisabled && paneStatus === 200 && checkState(paneAfter)
          : wantPaneDisabled
            ? paneDisabled && checkState(paneAfter)
            : !paneDisabled && paneStatus === 403 && checkState(paneAfter);

        record(
          "item4",
          `${c.id}:${action}`,
          `carol ${action} (${c.desc})`,
          restOk && cliOk && paneOk && inboxOk,
          {
            wantAccepted: c.wantAccepted,
            rest: {
              status: restResp.status,
              message: restResp.message,
              after: restAfter,
            },
            cli: {
              exit: cliResp.exit,
              output: cliResp.output,
              after: cliAfter,
            },
            pane: {
              disabled: paneDisabled,
              status: paneStatus,
              message: paneMessage,
              after: paneAfter,
            },
            inbox: inboxResult,
          },
        );
        rows.push({
          caseId: c.id,
          description: c.desc,
          action,
          wantAccepted: c.wantAccepted,
          rest: {
            status: restResp.status,
            message: redact(restResp.message),
            after: restAfter,
          },
          cli: {
            exit: cliResp.exit,
            output: redact(cliResp.output),
            after: cliAfter,
          },
          pane: {
            disabled: paneDisabled,
            status: paneStatus,
            message: redact(paneMessage),
            after: paneAfter,
          },
          inbox: inboxResult,
        });
      }
    }
  } finally {
    await context.close();
  }
  writeFileSync(
    `${outDir}/item4-approvals.json`,
    JSON.stringify(rows, null, 2),
  );
}

async function runItem5Visual(browser) {
  await resetPrincipals([
    ["user:alice@example.com", "grain/operator"],
    ["user:bob@example.com", "grain/viewer"],
    ["group:ops@example.com", "grain/dispatch-control"],
    ["user:carol@example.com", "carol-limited"],
    [
      "serviceAccount:ci-runner@example-project.iam.gserviceaccount.com",
      "grain/release-manager",
    ],
  ]);

  const longReason =
    "Quarterly access audit rotation: narrowing contractor and secondary operator sessions after incident review #4821 while preserving primary break-glass coverage and documenting the ticket trail in full detail for compliance.";
  // Generate 28 history entries including long reasons!
  for (let i = 0; i < 14; i++) {
    await asIAP(
      "alice@example.com",
      "PATCH",
      `/api/access/principals/${encodeURIComponent("user:bob@example.com")}`,
      {
        role: i % 2 === 0 ? "grain/operator" : "grain/viewer",
        reason: i === 13 ? longReason : `scheduled rotation check #${i + 1}`,
      },
    );
  }
  // Ensure bob ends at grain/viewer so alice is the last full admin!
  await asCode(
    "PATCH",
    `/api/access/principals/${encodeURIComponent("user:bob@example.com")}`,
    { role: "grain/viewer", reason: longReason },
  );

  for (const [vpName, viewport] of [
    ["desktop", { width: 1440, height: 900 }],
    ["phone", { width: 390, height: 844 }],
  ]) {
    for (const scheme of ["light", "dark"]) {
      const { context, page, pageErrors } = await openSignedInPage(
        browser,
        "alice@example.com",
        viewport,
        scheme,
      );
      try {
        await page.goto(`${PROXY}/settings/access`);
        const row = page.getByTestId("principal-row-user:alice@example.com");
        await row.waitFor({ timeout: 8000 });
        // Trigger a refusal on the panel with a long reason!
        await row.getByRole("combobox").click();
        await page
          .getByRole("option", { name: "grain/viewer", exact: true })
          .click();
        await page.getByTestId("access-change-reason").fill(longReason);
        await page.getByTestId("access-change-apply").click();
        await page.waitForTimeout(350);
        if ((await page.getByTestId("access-confirm-self").count()) > 0) {
          await page
            .getByRole("button", { name: "Yes, narrow my own access" })
            .click();
          await page.waitForTimeout(350);
        }

        const overflow = await page.evaluate(
          () =>
            document.documentElement.scrollWidth >
            document.documentElement.clientWidth + 1,
        );
        const errVisible = await page
          .getByTestId("access-change-error")
          .isVisible()
          .catch(() => false);
        const histCount = await page
          .locator('[data-testid^="access-history-"]')
          .count();

        await page.screenshot({
          path: `${outDir}/item5-access-${vpName}-${scheme}.png`,
          fullPage: true,
        });

        record(
          "item5",
          `visual-${vpName}-${scheme}`,
          `Settings -> Access with long history (${histCount} rows), long reason and panel refusal fits ${vpName} ${scheme}`,
          !overflow && errVisible && histCount >= 20 && pageErrors.length === 0,
          { overflow, errVisible, histCount, pageErrors },
        );
      } finally {
        await context.close();
      }
    }
  }
}

const browser = await chromium.launch();
try {
  if (mode === "nogroup") {
    await runItem2NoGroupChecker();
  } else {
    await seedScopeSetsAndRepos();
    await resetPrincipals(BASE_PRINCIPALS);
    const gmaAccessWriteToken = await mintExternalToken("agent-access-probe", [
      "access:read",
      "access:write",
      "tasks:read",
    ]);
    await runItem1Matrix(browser, gmaAccessWriteToken);
    await runItem2Bypasses(gmaAccessWriteToken);
    await runItem3SelfLockoutUX(browser);
    await runItem4LimitedApproval(browser);
    await runItem5Visual(browser);
  }
} finally {
  await browser.close();
  writeFileSync(
    `${outDir}/checks-${mode}.json`,
    JSON.stringify(checks, null, 2),
  );
}

const failed = checks.filter((c) => !c.ok);
console.log(
  `\nSummary (${mode}): ${checks.length - failed.length}/${checks.length} passed, ${failed.length} failed.`,
);
if (failed.length > 0) {
  process.exitCode = 1;
}
