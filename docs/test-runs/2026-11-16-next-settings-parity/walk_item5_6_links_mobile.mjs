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

// Section 9 Items 5 & 6 live walkthrough against a real grain daemon:
// - Item 5a: Framework API keys card retired on /next/settings/models; git-push-any-ref in retired grants;
//            no /api/agent-keys GET fired by /next/settings/models while API remains usable
// - Item 5b: Open every /settings/ link found in docs/ on /next/, plus legacy /t/<team>/ URLs,
//            checking landed pathname, active section, fragment/target element presence, and errors
// - Item 6: Seed long realistic values across all 8 settings pages, render EVERY page at 390x844
//           in both light and dark modes, audit horizontal overflow (scrollWidth > innerWidth),
//           card clipping, console/page errors, and save PNGs to $WORK_DIR/screens/.

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

// Every concrete /settings/... and /t/<team>/... link found in docs/ (excluding placeholders/filenames)
const DOC_LINKS = [
  // Canonical settings pages
  { path: "/next/settings", expectSection: "general" },
  { path: "/next/settings/general", expectSection: "general" },
  { path: "/next/settings/general#autoUpgradeTime", expectSection: "general", expectTarget: "autoUpgradeTime" },
  { path: "/next/settings/general#environmentName", expectSection: "general", expectTarget: "environmentName" },
  { path: "/next/settings/general#updates", expectSection: "general", expectTarget: "updates" },
  { path: "/next/settings/access", expectSection: "access" },
  { path: "/next/settings/access#allowed-principals", expectSection: "access", expectTarget: "allowed-principals" },
  { path: "/next/settings/access#external-agents", expectSection: "access", expectTarget: "external-agents" },
  { path: "/next/settings/agents", expectSection: "agents" },
  { path: "/next/settings/agents#commit-identity", expectSection: "agents", expectTarget: "commit-identity" },
  { path: "/next/settings/agents#external-agents", expectSection: "access", expectTarget: "external-agents" },
  { path: "/next/settings/agents#maxAgentTurns", expectSection: "agents", expectTarget: "maxAgentTurns" },
  { path: "/next/settings/agents#promptExtension", expectSection: "agents", expectTarget: "promptExtension" },
  { path: "/next/settings/models", expectSection: "models" },
  { path: "/next/settings/models#cli-run", expectSection: "models", expectTarget: "cli-run" },
  { path: "/next/settings/models#usage", expectSection: "models", expectTarget: "usage" },
  { path: "/next/settings/connections", expectSection: "models" },
  { path: "/next/settings/sandboxes", expectSection: "sandboxes" },
  { path: "/next/settings/sandboxes#backend-and-size", expectSection: "sandboxes", expectTarget: "backend-and-size" },
  { path: "/next/settings/sandboxes#kubernetes", expectSection: "sandboxes", expectTarget: "kubernetes" },
  { path: "/next/settings/sandboxes#k8sNamespace", expectSection: "sandboxes", expectTarget: "k8sNamespace" },
  { path: "/next/settings/sandboxes#k8sSandboxImage", expectSection: "sandboxes", expectTarget: "k8sSandboxImage" },
  { path: "/next/settings/sandboxes#sandboxCpuOvercommit", expectSection: "sandboxes", expectTarget: "sandboxCpuOvercommit" },
  { path: "/next/settings/sandboxes#sandboxCpus", expectSection: "sandboxes", expectTarget: "sandboxCpus" },
  { path: "/next/settings/hostds", expectSection: "hostds" },
  { path: "/next/settings/hosts", expectSection: "hostds" },
  { path: "/next/settings/executors", expectSection: "hostds" },
  { path: "/next/settings/git", expectSection: "git" },
  { path: "/next/settings/git-connections", expectSection: "git" },
  { path: "/next/settings/repositories", expectSection: "repositories" },
  { path: "/next/settings/repos", expectSection: "repositories" },
  { path: "/next/settings/tasks", expectSection: "tasks" },
  { path: "/next/settings/tasks#autoMergeByDefault", expectSection: "tasks", expectTarget: "autoMergeByDefault" },
  { path: "/next/settings/tasks#task-fields", expectSection: "tasks", expectTarget: "task-fields" },
  { path: "/next/settings/ci", expectSection: "ci" },
  { path: "/next/settings/ci#ci-artifacts", expectSection: "ci", expectTarget: "ci-artifacts" },
  { path: "/next/settings/caches", expectSection: "caches" },
  { path: "/next/settings/capabilities", expectSection: "capabilities" },
  { path: "/next/settings/capabilities#api-scope-sets", expectSection: "capabilities", expectTarget: "api-scope-sets" },
  { path: "/next/settings/capabilities#capability-policy", expectSection: "capabilities", expectTarget: "capability-policy" },
  { path: "/next/settings/capabilities#capability-servers", expectSection: "capabilities", expectTarget: "capability-servers" },
  { path: "/next/settings/capabilities#credentials", expectSection: "capabilities", expectTarget: "credentials" },
  { path: "/next/settings/capabilities#declarations", expectSection: "capabilities", expectTarget: "declarations" },
  { path: "/next/settings/capabilities#gcp-settings", expectSection: "capabilities", expectTarget: "gcp-settings" },
  { path: "/next/settings/capabilities#gcpProject", expectSection: "capabilities", expectTarget: "gcpProject" },
  { path: "/next/settings/capabilities#githubHost", expectSection: "capabilities", expectTarget: "githubHost" },
  { path: "/next/settings/capabilities#other-secrets", expectSection: "capabilities", expectTarget: "other-secrets" },
  { path: "/next/settings/capabilities/config-map", expectSection: "capabilities" },
  { path: "/next/settings/capability-servers", expectSection: "capabilities" },
  { path: "/next/settings/credentials", expectSection: "capabilities", expectTarget: "credentials" },
  { path: "/next/settings/legacy-capabilities", expectSection: "capabilities", expectTarget: "credentials" },
  { path: "/next/settings/secrets", expectSection: "capabilities", expectTarget: "other-secrets" },
  { path: "/next/settings/engine", expectSection: "engine" },
  { path: "/next/settings/engine#maxWorkers", expectSection: "engine", expectTarget: "maxWorkers" },
  { path: "/next/settings/engine#pollInterval", expectSection: "engine", expectTarget: "pollInterval" },
  { path: "/next/settings/registry", expectSection: "registry" },
  { path: "/next/settings/config-repo", expectSection: "config-repo" },
  { path: "/next/settings/config-repo/tables/grain_config.json", expectSection: "config-repo" },
  { path: "/next/settings/cli", expectSection: "cli" },
  { path: "/next/settings/cli-run", expectSection: "models" },
  { path: "/next/settings/upgrade", expectSection: "upgrade" },
  { path: "/next/settings/audit", expectSection: "audit" },
  { path: "/next/settings/operations", expectSection: "engine" },
  { path: "/next/settings/deployment", expectSection: "general" },
  { path: "/next/settings/merge-queue", expectSection: "tasks" },
  { path: "/next/settings/releases", expectSection: "repositories" },
  { path: "/next/settings/memory", expectPathPrefix: "/next/context" },
  // Legacy /t/<team>/ links on /next/
  { path: "/next/t/alpha/settings/models", expectSection: "models" },
  { path: "/next/t/alpha/settings/connections", expectSection: "models" },
  { path: "/next/t/alpha/settings/agents", expectSection: "agents" },
  { path: "/next/t/alpha/settings/audit", expectSection: "audit" },
  { path: "/next/t/default/settings", expectSection: "general" },
  { path: "/next/t/platform/settings/capabilities#credentials", expectSection: "capabilities", expectTarget: "credentials" },
  { path: "/next/t/acme/tasks", expectPathPrefix: "/next/tasks" },
  { path: "/next/t/acme", expectPathPrefix: "/next/inbox" },
];

async function main() {
  const storageState = await signIn(DAEMON, CODE, "alice@example.com");
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    storageState,
  });
  const page = await context.newPage();
  const pageErrors = [];
  const consoleErrors = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));
  page.on("console", (m) => {
    if (m.type() === "error") consoleErrors.push(m.text());
  });

  // =========================================================================
  // Item 5a: Framework API keys card retired & git-push-any-ref in retired grants
  // =========================================================================
  {
    const agentKeysCalls = [];
    page.on("request", (req) => {
      if (req.url().includes("/api/agent-keys")) agentKeysCalls.push(`${req.method()} ${req.url()}`);
    });
    await page.goto(`${DAEMON}/next/settings/models`);
    await page.getByTestId("card-model-backends").waitFor({ state: "visible" });
    await page.waitForTimeout(400);
    const hasRetiredCard = (await page.locator('[data-testid="card-agent-keys"]').count()) > 0;
    const modelsText = (await page.locator("main").textContent()) || "";
    const hasHeading = /Framework API keys/i.test(modelsText);
    // Verify /api/agent-keys endpoint itself is still accessible for headless scripts
    const akApi = await api("GET", "/api/agent-keys");

    // Check git-push-any-ref mentioned in retired grants on /next/settings/capabilities
    await page.goto(`${DAEMON}/next/settings/capabilities`);
    await page.getByTestId("settings-stage").waitFor({ state: "visible" });
    await page.waitForTimeout(400);
    const capsText = (await page.locator("main").textContent()) || "";
    const hasGitPushAnyRef = capsText.includes("git-push-any-ref");

    record(
      "item5-retirement",
      "Framework API keys card retired (zero /api/agent-keys reads from UI, API still alive) & git-push-any-ref in retired grants",
      !hasRetiredCard &&
        !hasHeading &&
        agentKeysCalls.length === 0 &&
        akApi.status === 200 &&
        hasGitPushAnyRef,
      {
        hasRetiredCard,
        hasHeading,
        agentKeysCalls,
        agentKeysApiStatus: akApi.status,
        hasGitPushAnyRef,
      },
    );
  }

  // =========================================================================
  // Item 5b: Open every /settings/ link from docs/ and /t/<team>/ URLs on /next/
  // =========================================================================
  const linkResults = [];
  for (const spec of DOC_LINKS) {
    await page.goto(`${DAEMON}${spec.path}`);
    await page.waitForTimeout(350);
    const landed = new URL(page.url());
    const landedPath = landed.pathname + landed.hash;
    let ok = true;
    let detail = { requested: spec.path, landed: landedPath };

    if (spec.expectPathPrefix) {
      ok = landed.pathname.startsWith(spec.expectPathPrefix);
      detail.expectPathPrefix = spec.expectPathPrefix;
    } else if (spec.expectSection) {
      const stageVisible = await page
        .getByTestId("settings-stage")
        .isVisible()
        .catch(() => false);
      const heading = stageVisible
        ? ((await page.locator("#settings-stage-heading").textContent().catch(() => "")) || "").trim()
        : "";
      const activeNav = await page
        .locator(".settings-nav-item.is-active, .settings-nav-link.is-active, [aria-current='page']")
        .first()
        .getAttribute("href")
        .catch(() => null);
      const landedSection = landed.pathname.replace(/^\/next\/settings\/?/, "").split("/")[0] || "general";
      detail.stageVisible = stageVisible;
      detail.landedSection = landedSection;
      detail.heading = heading;
      detail.activeNav = activeNav;
      ok = stageVisible && landed.pathname.startsWith("/next/settings") && landedSection === spec.expectSection;
      if (ok && spec.expectTarget) {
        // Check if target fragment matches an id, data-testid="card-<frag>", or CARD_ANCHORS target
        const targetFound = await page.evaluate((frag) => {
          const map = { usage: "card-token-budget", "external-agents": "card-external-agent-tokens" };
          const byId = document.getElementById(frag);
          const byCard = document.querySelector(`[data-testid="card-${frag}"]`);
          const byMapped = map[frag] ? document.querySelector(`[data-testid="${map[frag]}"]`) : null;
          const byInput = document.querySelector(`[data-testid="input-${frag}"], [name="${frag}"]`);
          return Boolean(byId || byCard || byMapped || byInput);
        }, spec.expectTarget);
        detail.expectTarget = spec.expectTarget;
        detail.targetFound = targetFound;
        if (!targetFound) ok = false;
      }
    }
    linkResults.push({ ...detail, ok });
    if (!ok) {
      record("item5-deeplink", `FAILED link ${spec.path}`, false, detail);
    }
  }
  const brokenLinks = linkResults.filter((r) => !r.ok);
  record(
    "item5-deeplinks-summary",
    `All ${DOC_LINKS.length} docs /settings/ and /t/<team>/ links land cleanly on /next/`,
    brokenLinks.length === 0,
    {
      total: DOC_LINKS.length,
      passed: DOC_LINKS.length - brokenLinks.length,
      failedCount: brokenLinks.length,
      broken: brokenLinks,
    },
  );

  // =========================================================================
  // Item 6: Every settings page at 390x844 light & dark with long values
  // =========================================================================
  // Seed realistic long values (we'll restore afterwards!)
  const origSettings = (await api("GET", "/api/settings")).json;
  const longHost = "very-long-enterprise-git-host-subdomain.internal-platform-team.example.com:8443";
  const longImage = "us-central1-docker.pkg.dev/example-long-project-identifier-123456/grain-sandboxes/agent-sandbox-runtime:v3.12.0-rc.4-sha256-abcdef0123456789";
  const longHubUrl = "https://grain-hub-production-us-central1.internal-platform.example.com/hub/v1/clusters/primary";
  const longSecretName = "walkthrough_very_long_stored_secret_name_for_mobile_layout_overflow_check";
  const longScopeSetName = "team/very-long-scope-set-name-checking-mobile-wrapping";

  await api("POST", "/api/settings", {
    hubUrl: longHubUrl,
    hubEntry: "primary-production-walkthrough-cluster-entry-name",
    hubPurpose: "Long deployment purpose string verifying clean text wrapping at 390px without horizontal overflow across light and dark themes.",
    k8sSandboxImage: longImage,
    k8sNamespace: "grain-sandboxes-production-long-namespace-name",
    sandboxImage: longImage,
    gcpProject: "example-very-long-gcp-project-identifier-for-mobile-check",
    ciArtifactsPrefix: "gs://example-long-bucket-name-for-ci-artifacts/deployments/production/ci-logs/",
  });
  await api("PUT", `/api/secrets/${longSecretName}`, { value: "super-secret-mobile-check-value" });
  await api("PUT", `/api/api-scope-sets/${encodeURIComponent(longScopeSetName)}`, {
    description: "Long description for mobile wrapping test at 390px viewport width.",
    scopes: ["tasks:read", "repos:read", "ci:read", "settings:read"],
  });
  await api("PUT", "/api/deployment/agent-backends/very-long-model-connection-identifier-01", {
    id: "very-long-model-connection-identifier-01",
    displayName: "Very Long Model Connection Display Name For 390px Mobile Test",
    framework: "claude",
    authMode: "api_key",
    models: ["claude-sonnet-4-6-very-long-model-alias-name", "claude-opus-4-6-another-long-model-name"],
    hostd: "very-long-host-selector-name-workstation-01",
  });
  await api("POST", "/api/hostds", {
    name: "very-long-workstation-hostname-for-mobile-check-01",
    owner: "very.long.email.address.for.mobile.check@enterprise.example.com",
    labels: {
      environment: "production-long-label-value",
      region: "us-central1-a-long-zone-name",
    },
  });
  await api("POST", "/api/deployment/git-connections", {
    host: longHost,
    kind: "git",
    type: "pat",
    scope: "very-long-organization-name/very-long-repository-name-for-mobile-check",
    value: "walk-mobile-pat-value",
  });

  const PAGES = [
    "capabilities",
    "registry",
    "sandboxes",
    "engine",
    "ci",
    "git",
    "hostds",
    "models",
    "general",
    "access",
    "agents",
    "repositories",
    "tasks",
    "caches",
    "config-repo",
    "cli",
    "upgrade",
    "audit",
  ];
  const screensDir = path.join(WORK_DIR, "screens");
  fs.mkdirSync(screensDir, { recursive: true });

  const mobileAudit = [];
  for (const theme of ["light", "dark"]) {
    const mobileCtx = await browser.newContext({
      viewport: { width: 390, height: 844 },
      colorScheme: theme,
      storageState,
    });
    const mPage = await mobileCtx.newPage();
    const mErrors = [];
    mPage.on("pageerror", (e) => mErrors.push(String(e)));

    // Set theme preference in localStorage so the app renders the exact requested theme
    await mPage.addInitScript((t) => {
      try {
        localStorage.setItem("grain-theme", t);
        localStorage.setItem("theme", t);
        document.documentElement.dataset.theme = t;
      } catch {}
    }, theme);

    for (const sec of PAGES) {
      await mPage.goto(`${DAEMON}/next/settings/${sec}`);
      await mPage.getByTestId("settings-stage").waitFor({ state: "visible", timeout: 10000 }).catch(() => {});
      await mPage.waitForTimeout(350);
      // Force data-theme attribute match in case app uses class/attr
      await mPage.evaluate((t) => {
        document.documentElement.dataset.theme = t;
        document.documentElement.classList.toggle("dark", t === "dark");
      }, theme);
      await mPage.waitForTimeout(100);

      const metrics = await mPage.evaluate(() => {
        const vw = window.innerWidth;
        const docScroll = document.documentElement.scrollWidth;
        const bodyScroll = document.body.scrollWidth;
        const overflowEls = [];
        for (const el of document.querySelectorAll("main *, .settings-stage *")) {
          const r = el.getBoundingClientRect();
          if (r.width === 0 || r.height === 0) continue;
          // Ignore elements inside intentional horizontally-scrollable wrappers (.s-table-wrap, pre, code)
          if (el.closest(".s-table-wrap, pre.s-code")) continue;
          if (r.right > vw + 2) {
            overflowEls.push({
              tag: el.tagName.toLowerCase(),
              cls: String(el.className || "").slice(0, 60),
              testid: el.getAttribute("data-testid") || "",
              right: Math.round(r.right),
              vw,
              text: (el.textContent || "").trim().slice(0, 60),
            });
          }
        }
        return {
          vw,
          docScroll,
          bodyScroll,
          pageOverflows: docScroll > vw + 1 || bodyScroll > vw + 1,
          overflowCount: overflowEls.length,
          overflowSample: overflowEls.slice(0, 5),
        };
      });

      const shotPath = path.join(screensDir, `390-${theme}-${sec}.png`);
      await mPage.screenshot({ path: shotPath, fullPage: true });
      const ok = !metrics.pageOverflows && metrics.overflowCount === 0 && mErrors.length === 0;
      mobileAudit.push({ theme, section: sec, ok, ...metrics, shotPath });
      if (!ok) {
        record("item6-mobile", `390px ${theme} /next/settings/${sec} overflow/error`, false, {
          theme,
          section: sec,
          ...metrics,
          errors: mErrors,
        });
      }
    }
    await mobileCtx.close();
  }

  const mobileFailed = mobileAudit.filter((m) => !m.ok);
  record(
    "item6-mobile-summary",
    `All ${PAGES.length} settings pages x 2 themes (36 views) at 390px with long values have zero overflow`,
    mobileFailed.length === 0,
    {
      total: mobileAudit.length,
      passed: mobileAudit.length - mobileFailed.length,
      failedCount: mobileFailed.length,
      failed: mobileFailed.map((f) => ({
        theme: f.theme,
        section: f.section,
        docScroll: f.docScroll,
        vw: f.vw,
        overflowCount: f.overflowCount,
        overflowSample: f.overflowSample,
      })),
    },
  );

  // Restore seeded long values
  await api("DELETE", `/api/secrets/${longSecretName}`).catch(() => {});
  await api("DELETE", `/api/api-scope-sets/${encodeURIComponent(longScopeSetName)}`).catch(() => {});
  await api("DELETE", "/api/deployment/agent-backends/very-long-model-connection-identifier-01").catch(() => {});
  await api("POST", "/api/hostds/very-long-workstation-hostname-for-mobile-check-01/revoke", {}).catch(() => {});
  await api("DELETE", "/api/hostds/very-long-workstation-hostname-for-mobile-check-01").catch(() => {});
  const curConns = (await api("GET", "/api/deployment")).json?.gitConnections || [];
  for (const c of curConns) {
    if (c.host === longHost) {
      await api("DELETE", `/api/deployment/git-connections/${encodeURIComponent(c.credential)}`).catch(() => {});
    }
  }
  await api("POST", "/api/settings", {
    hubUrl: origSettings?.hubUrl ?? "",
    hubEntry: origSettings?.hubEntry ?? "",
    hubPurpose: origSettings?.hubPurpose ?? "",
    k8sSandboxImage: origSettings?.k8sSandboxImage ?? "",
    k8sNamespace: origSettings?.k8sNamespace ?? "",
    sandboxImage: origSettings?.sandboxImage ?? "",
    gcpProject: origSettings?.gcpProject ?? "",
    ciArtifactsPrefix: origSettings?.ciArtifactsPrefix ?? "",
  });

  record("item5_6-health", "zero uncaught page errors", pageErrors.length === 0, {
    pageErrors,
    consoleErrorCount: consoleErrors.length,
    consoleErrorSample: consoleErrors.slice(0, 5),
  });

  await browser.close();
  fs.mkdirSync(path.join(WORK_DIR, "results"), { recursive: true });
  fs.writeFileSync(
    path.join(WORK_DIR, "results/item5_6.json"),
    JSON.stringify({ findings, linkResults, mobileAudit }, null, 2),
  );
  const failed = findings.filter((f) => !f.ok);
  console.log(
    `\nItems 5 & 6 summary: ${findings.length - failed.length}/${findings.length} passed, ${failed.length} failed.`,
  );
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
