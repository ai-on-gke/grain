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

// §2 item 4: Settings → CLI and Settings → Agents#external-agents in a real
// Chromium, at 1440×900 and 390×844, light and dark, against a normal
// `grain demo` (toggle Accept external agents off → follow the link → on)
// and one started with -external-config-repo (every write refused with the
// edit to commit instead). Asserts no horizontal overflow, every command
// inside the viewport, and that each copy button puts exactly the command
// it shows on the clipboard.
//
//   UI_DIR=<repo>/ui NORMAL=<url> NORMAL_CODE=<code> EXTERNAL=<url> \
//   EXTERNAL_CODE=<code> OUT=<dir> node settings-cli-walkthrough.mjs

import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.env.UI_DIR, "package.json"));
const { chromium } = require("@playwright/test");

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};
const OUT = process.env.OUT || "/tmp/grain-settings-cli-shots";
const results = [];

function check(label, ok, detail = "") {
  results.push({ label, ok });
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${detail ? `  -- ${detail}` : ""}`);
}

async function signIn(url, code) {
  const res = await fetch(`${url}/api/auth/access-code`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code }),
  });
  if (!res.ok) throw new Error(`sign-in to ${url}: ${res.status}`);
  const cookie = res.headers.get("set-cookie").split(";")[0];
  const [name, value] = cookie.split("=");
  return { name, value, url };
}

async function api(url, code, method, p, body) {
  const res = await fetch(url + p, {
    method,
    headers: { Authorization: `Bearer ${code}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: res.status, text: await res.text() };
}

function expectedCommands(origin) {
  const host = new URL(origin).hostname.replace(/[^a-z0-9]+/gi, "-");
  return {
    "cli-install-command": `curl -fsSL ${origin}/cli/install.sh | sh`,
    "cli-login-command": `grain login ${origin} --name ${host} --issue-token --scope-set grain/external-dev --expires 30d`,
    "cli-skill-command-jetski": "grain skill install --jetski",
    "cli-skill-command-claude": "grain skill install --claude",
    "cli-skill-command-codex": "grain skill install --codex",
    "cli-skill-command-gemini": "grain skill install --gemini",
  };
}

async function noOverflow(page, label, width) {
  const sw = await page.evaluate(() => document.documentElement.scrollWidth);
  check(`${label}: no horizontal overflow`, sw <= width, `scrollWidth=${sw} viewport=${width}`);
}

async function cliPage(page, origin, label, width, wantOffNote) {
  await page.goto(`${origin}/settings/cli`);
  await page.getByTestId("cli-install-command").waitFor();
  const want = expectedCommands(origin);
  for (const [id, text] of Object.entries(want)) {
    if (id === "cli-login-command" && wantOffNote) {
      // The login card still shows the command with the API off.
    }
    const el = page.getByTestId(id);
    const shown = (await el.textContent()).trim();
    check(`${label}: ${id} text`, shown === text, shown === text ? "" : `got ${JSON.stringify(shown)}`);
    const box = await el.boundingBox();
    check(`${label}: ${id} inside viewport`, box.x >= 0 && box.x + box.width <= width + 0.5,
      `x=${box.x.toFixed(1)} w=${box.width.toFixed(1)}`);
    // The copy button beside it.
    const btn = el.locator("xpath=following-sibling::*[1]").locator("button").first();
    const button = (await btn.count()) ? btn : el.locator("xpath=following-sibling::*[1]");
    await page.evaluate(() => navigator.clipboard.writeText("<cleared>"));
    await button.click();
    await page.waitForTimeout(150);
    const clip = await page.evaluate(() => navigator.clipboard.readText());
    check(`${label}: copy ${id}`, clip === text, clip === text ? "" : `clipboard ${JSON.stringify(clip)}`);
  }
  const off = await page.getByTestId("cli-external-agent-api-off").count();
  check(`${label}: external-agent-API-off note ${wantOffNote ? "shown" : "absent"}`, (off > 0) === wantOffNote);
  await noOverflow(page, label, width);
}

async function run() {
  await fs.mkdir(OUT, { recursive: true });
  const browser = await chromium.launch();
  const servers = [
    { kind: "normal", url: process.env.NORMAL, code: process.env.NORMAL_CODE },
    { kind: "external-config-repo", url: process.env.EXTERNAL, code: process.env.EXTERNAL_CODE },
  ];
  try {
    for (const srv of servers.filter((x) => !process.env.ONLY || x.kind === process.env.ONLY)) {
      const cookie = await signIn(srv.url, srv.code);
      const before = JSON.parse((await api(srv.url, srv.code, "GET", "/api/settings")).text);
      const wasOn = Boolean(before.settings?.externalAgentApi ?? before.externalAgentApi);
      console.log(`\n== ${srv.kind} (${srv.url}); externalAgentApi was ${wasOn}; configRepoOnly=${before.configRepoOnly}`);
      for (const [vw, viewport] of Object.entries(VIEWPORTS)) {
        for (const theme of ["light", "dark"]) {
          const label = `${srv.kind} ${vw} ${theme}`;
          const ctx = await browser.newContext({
            viewport,
            colorScheme: theme,
            isMobile: vw === "phone",
            hasTouch: vw === "phone",
            permissions: ["clipboard-read", "clipboard-write"],
          });
          await ctx.addCookies([cookie]);
          const page = await ctx.newPage();
          try {
            if (srv.kind === "normal") {
              const offRes = await api(srv.url, srv.code, "PUT", "/api/settings", { externalAgentApi: false });
              check(`${label}: operator turns the API off`, offRes.status === 200, `${offRes.status}`);
              await cliPage(page, srv.url, label, viewport.width, true);
              await page.screenshot({ path: path.join(OUT, `cli-${srv.kind}-${vw}-${theme}-off.png`), fullPage: true });
              await page.getByTestId("cli-external-agent-api-link").click();
              await page.waitForURL(/\/settings\/agents/);
              check(`${label}: link lands on /settings/agents#external-agents`,
                page.url().endsWith("/settings/agents#external-agents"), page.url());
              const toggle = page.locator('input[name="externalAgentApi"]');
              await toggle.waitFor({ state: "attached" });
              check(`${label}: Accept external agents is off`, !(await toggle.isChecked()));
              const tb = await toggle.locator("xpath=ancestor::label[1]").boundingBox();
              check(`${label}: the switch is on screen`, tb && tb.y >= 0 && tb.y + tb.height <= viewport.height,
                tb ? `y=${tb.y.toFixed(0)}` : "no box");
              await noOverflow(page, `${label} agents`, viewport.width);
              await page.screenshot({ path: path.join(OUT, `agents-${srv.kind}-${vw}-${theme}.png`) });
              await toggle.check();
              await page.getByTestId("settings-status").filter({ hasText: "All changes saved" }).waitFor();
              const now = JSON.parse((await api(srv.url, srv.code, "GET", "/api/settings")).text);
              check(`${label}: switch saved externalAgentApi=true`,
                Boolean(now.settings?.externalAgentApi ?? now.externalAgentApi));
              await cliPage(page, srv.url, `${label} (on)`, viewport.width, false);
              await page.screenshot({ path: path.join(OUT, `cli-${srv.kind}-${vw}-${theme}-on.png`), fullPage: true });
            } else {
              await cliPage(page, srv.url, label, viewport.width, !wasOn);
              await page.screenshot({ path: path.join(OUT, `cli-${srv.kind}-${vw}-${theme}.png`), fullPage: true });
              await page.goto(`${srv.url}/settings/agents#external-agents`);
              const toggle = page.locator('input[name="externalAgentApi"]');
              await toggle.waitFor({ state: "attached" });
              // The read-only flow: a banner naming the config repository and
              // the exact file to edit there, and a switch that cannot save.
              const notice = page.getByTestId("external-config-repo-banner").first();
              const where = (await page.getByTestId("config-refusal-where").first().innerText()).trim();
              check(`${label}: external-config-repo banner names the file to edit`,
                (await notice.isVisible()) && where.includes("tables/grain_config.json") && where.includes("acme/grain-config"), where);
              check(`${label}: Accept external agents switch is read-only`, await toggle.isDisabled());
              await noOverflow(page, `${label} agents`, viewport.width);
              const nb = await notice.boundingBox();
              check(`${label}: notice inside viewport`, nb && nb.x >= 0 && nb.x + nb.width <= viewport.width + 0.5,
                nb ? `x=${nb.x.toFixed(1)} w=${nb.width.toFixed(1)}` : "");
              await page.screenshot({ path: path.join(OUT, `agents-${srv.kind}-${vw}-${theme}.png`) });
              const put = await api(srv.url, srv.code, "PUT", "/api/settings", { externalAgentApi: true });
              check(`${label}: PUT is refused 409 with the edit to commit`, put.status === 409 && put.text.includes("external_agent_api"),
                `${put.status}`);
            }
          } catch (err) {
            check(`${label}: no exception`, false, String(err).split("\n")[0]);
            await page.screenshot({ path: path.join(OUT, `error-${srv.kind}-${vw}-${theme}.png`), fullPage: true }).catch(() => {});
          } finally {
            await ctx.close();
          }
        }
      }
      if (srv.kind === "normal") {
        await api(srv.url, srv.code, "PUT", "/api/settings", { externalAgentApi: wasOn });
      }
    }
  } finally {
    await browser.close();
  }
  const fails = results.filter((r) => !r.ok);
  console.log(`\n== ${results.length} checks, ${fails.length} failed${fails.length ? ": " + fails.map((f) => f.label).join("; ") : ""}`);
  process.exitCode = fails.length ? 1 : 0;
}

run();
