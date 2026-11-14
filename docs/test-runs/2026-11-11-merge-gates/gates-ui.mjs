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

// §4 item 5 of docs/test-plans/2026-11-11.md: the merge gates on the task
// pane, the pull request page and the Inbox, with gates pending, passed,
// findings and overridden, at 1440x900 and 390x844 in light and dark, and
// the override dialog's pull-request-number requirement.
//
// Run from ui/ against a running `grain demo -github-sim -fake-ci
// -review-questions` (the demo seeds "Publish both daemon images" with a
// code-review gate holding findings); the other states are laid over that
// task's real answer with page.route, the way screenshot.mjs's MERGE= does:
//
//   cd ui && GRAIN_URL=http://127.0.0.1:4291 GRAIN_CODE=<code> \
//     PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-11-merge-gates/gates-ui.mjs <out-dir>

import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("playwright");

const url = process.env.GRAIN_URL || "http://127.0.0.1:4291";
const code = process.env.GRAIN_CODE;
const out = process.argv[2] || "screenshots/merge-gates";
fs.mkdirSync(out, { recursive: true });

const gate = (id, label, state, detail, extra = {}) => ({
  id,
  label,
  state,
  detail,
  overridable: state !== "passed" && state !== "overridden",
  ...extra,
});

// Each scenario replaces the held task's mergeGates (and gateOverride).
const scenarios = {
  pending: {
    mergeGates: [
      gate("ci", "CI", "running", "waiting for checks"),
      gate("security-review", "Security review", "running", "running (task #31)", { reviewTask: "31" }),
      // Two trigger reviews (one per review template) behind one gate,
      // as ComputeMergeGates reports them since finding F4.
      gate("review", "Code review", "running", "running (tasks #32, #33)", { reviewTask: "32", tasks: ["32", "33"] }),
      gate("approval", "Approval", "passed", "given by alice@example.com", { approvedBy: "alice@example.com" }),
    ],
  },
  passed: {
    mergeGates: [
      gate("ci", "CI", "passed", "passed"),
      gate("security-review", "Security review", "passed", "passed (task #31)", { reviewTask: "31" }),
      gate("review", "Code review", "passed", "passed (task #32)", { reviewTask: "32" }),
      gate("approval", "Approval", "passed", "given by alice@example.com", { approvedBy: "alice@example.com" }),
    ],
  },
  findings: null, // the demo's own answer
  overridden: {
    mergeGates: [
      gate("ci", "CI", "passed", "passed"),
      gate("security-review", "Security review", "passed", "passed (task #31)", { reviewTask: "31" }),
      gate("review", "Code review", "overridden", "overridden by alice@example.com: release blocker", { reviewTask: "32" }),
      gate("approval", "Approval", "passed", "given by alice@example.com", { approvedBy: "alice@example.com" }),
    ],
    gateOverride: {
      gates: ["review"],
      reason: "release blocker, the trigger review continues post-merge",
      by: "alice@example.com",
      headSha: "0123456789abcdef0123456789abcdef01234567",
      at: "2026-11-11T23:00:00Z",
    },
  },
};

const viewports = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];

async function main() {
  const headers = { Authorization: `Bearer ${code}` };
  const tasks = await (await fetch(`${url}/api/tasks`, { headers })).json();
  const held = tasks.find((t) => t.title === "Publish both daemon images");
  if (!held) throw new Error("the demo has no 'Publish both daemon images' task");
  const pr = held.pullRequest; // acme/widgets#106
  const prNumber = pr.split("#")[1];
  const comparePath = `/repos/${held.repo}/compare/main...${held.branch}`;
  const results = [];

  const browser = await chromium.launch();
  for (const theme of ["light", "dark"]) {
    for (const vp of viewports) {
      const context = await browser.newContext({
        viewport: { width: vp.width, height: vp.height },
        colorScheme: theme,
        extraHTTPHeaders: headers,
      });
      // Sign in so the SPA's own fetches carry a session.
      await context.request.post(`${url}/api/auth/access-code`, {
        data: { code, name: "alice@example.com" },
      }).catch(() => {});
      for (const [name, patch] of Object.entries(scenarios)) {
        const page = await context.newPage();
        await page.addInitScript((t) => {
          try {
            localStorage.setItem("grain.themeMode", t);
          } catch {}
        }, theme);
        if (patch) {
          const overlay = (obj) => (obj && obj.id === held.id ? { ...obj, ...patch } : obj);
          await page.route(new RegExp(`/api/tasks/${held.id}(\\?.*)?$`), async (route) => {
            const res = await route.fetch();
            const body = await res.json();
            await route.fulfill({ response: res, json: overlay(body) });
          });
          await page.route(/\/api\/tasks(\?.*)?$/, async (route) => {
            const res = await route.fetch();
            const body = await res.json();
            const list = Array.isArray(body) ? body.map(overlay) : { ...body, tasks: (body.tasks || []).map(overlay) };
            await route.fulfill({ response: res, json: list });
          });
        }
        for (const [where, route, testId] of [
          ["task", `/tasks/${held.id}`, "merge-gates"],
          ["pr", comparePath, "pr-merge-gates"],
          ["inbox", "/inbox", "inbox-merge-gates"],
        ]) {
          await page.goto(`${url}${route}`);
          const el = page.getByTestId(testId).first();
          let visible = true;
          try {
            await el.waitFor({ state: "visible", timeout: 8000 });
          } catch {
            visible = false;
          }
          const overflow = await page.evaluate(
            () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
          );
          const box = visible ? await el.boundingBox() : null;
          const text = visible ? (await el.innerText()).replace(/\s+/g, " ").trim() : "";
          const file = path.join(out, `${name}-${where}-${vp.name}-${theme}.png`);
          if (visible) await el.scrollIntoViewIfNeeded();
          await page.screenshot({ path: file, fullPage: false });
          results.push({
            scenario: name, where, viewport: vp.name, theme, visible, overflow,
            withinViewport: box ? box.x >= 0 && box.x + box.width <= vp.width + 1 : null,
            text: text.slice(0, 200),
          });
        }
        // The override dialog asks for the pull request number.
        if (name === "findings" && theme === "light") {
          await page.goto(`${url}/tasks/${held.id}`);
          const btn = page.getByTestId("override-review-gates");
          await btn.click();
          const dialog = page.getByTestId("gate-override-dialog");
          await dialog.waitFor({ state: "visible" });
          const confirm = page.getByTestId("confirm-gate-override");
          const step = async (label) => results.push({ dialog: label, viewport: vp.name, confirmEnabled: await confirm.isEnabled() });
          await step("opened");
          await page.getByTestId("override-reason-input").fill("release blocker");
          await step("reason only");
          const prInput = page.getByTestId("override-pr-input");
          await prInput.fill(String(Number(prNumber) + 1));
          await step("wrong PR number");
          await prInput.fill(prNumber);
          await step("right PR number");
          await page.screenshot({ path: path.join(out, `override-dialog-${vp.name}-${theme}.png`) });
        }
        await page.close();
      }
      await context.close();
    }
  }
  await browser.close();
  fs.writeFileSync(path.join(out, "results.json"), JSON.stringify(results, null, 2));
  console.log(JSON.stringify(results));
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
