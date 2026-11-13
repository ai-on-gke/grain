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

import AxeBuilder from "@axe-core/playwright";
import fs from "node:fs";
import path from "node:path";
import { test, expect } from "./fixtures.js";

// Every destination of the Angular UI at /next/ (ui-ng/src/app/app.routes.ts)
// against a real `grain demo`: in light and dark, at a desktop and a phone
// size, each page renders its own content (not the shell alone), nothing
// scrolls sideways, the page throws nothing (the pageErrors fixture), and
// axe reports no serious or critical violation.
//
// Set GRAIN_UI_SHOTS=<dir> to also write one PNG per page, theme and size:
// that directory is the screenshot set a UI change is reviewed against.

const SHOTS = process.env.GRAIN_UI_SHOTS || "";

const SIZES = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];
const THEMES = ["light", "dark"];

// Static destinations. Parameterised ones (a task, a repository) are added
// from what the demo store holds, in the test itself.
const ROUTES = [
  "inbox",
  "workstreams",
  "tasks",
  "agents",
  "sessions",
  "processes",
  "processes/templates",
  "processes/suites",
  "ci",
  "telemetry",
  "context",
  "fleet",
  "settings",
];

async function api(baseURL, code, p) {
  const res = await fetch(`${baseURL}${p}`, {
    headers: { Authorization: `Bearer ${code}` },
  });
  if (!res.ok) throw new Error(`GET ${p}: ${res.status}`);
  return res.json();
}

async function settle(page) {
  // The shell renders first; a destination has rendered once the router
  // outlet has a sibling and the first data load has had a moment.
  await page.waitForSelector(".main-stage > *:not(router-outlet)", {
    timeout: 30_000,
  });
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(400);
}

for (const size of SIZES) {
  for (const theme of THEMES) {
    test(`every /next/ destination renders cleanly — ${size.name}, ${theme}`, async ({
      page,
      demoServer,
      accessCode,
    }) => {
      test.setTimeout(240_000);
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.addInitScript(
        (t) => localStorage.setItem("grain.themeMode", t),
        theme,
      );

      const routes = [...ROUTES];
      const tasks = await api(demoServer, accessCode, "/api/tasks");
      const list = Array.isArray(tasks) ? tasks : (tasks.tasks ?? []);
      if (list[0]?.id) routes.push(`tasks/${list[0].id}`);
      const cfg = await api(demoServer, accessCode, "/api/config");
      const repo = (cfg.targetRepos ?? [])[0];
      if (repo && repo.split("/").length === 2) routes.push(`repos/${repo}`);

      const failures = [];
      for (const r of routes) {
        await page.goto(`/next/${r}`);
        await settle(page);

        const sideways = await page.evaluate(
          () =>
            document.documentElement.scrollWidth -
            document.documentElement.clientWidth,
        );
        if (sideways > 0)
          failures.push(`${r}: scrolls sideways by ${sideways}px`);

        const applied = await page.evaluate(
          () => document.documentElement.getAttribute("data-theme") || "",
        );
        if (applied && applied !== theme)
          failures.push(`${r}: theme ${applied}, wanted ${theme}`);

        const axe = await new AxeBuilder({ page })
          .disableRules(["color-contrast"])
          .analyze();
        const bad = axe.violations.filter((v) =>
          ["serious", "critical"].includes(v.impact ?? ""),
        );
        for (const v of bad)
          failures.push(`${r}: axe ${v.id} (${v.nodes.length}) ${v.help}`);

        if (SHOTS) {
          fs.mkdirSync(SHOTS, { recursive: true });
          const file = `${r.replace(/[/:]/g, "_")}-${size.name}-${theme}.png`;
          await page.screenshot({ path: path.join(SHOTS, file) });
        }
      }
      expect(failures, failures.join("\n")).toEqual([]);
    });
  }
}

test("colour contrast holds on the primary destinations in both themes", async ({
  page,
}) => {
  test.setTimeout(120_000);
  const failures = [];
  for (const theme of THEMES) {
    await page.addInitScript(
      (t) => localStorage.setItem("grain.themeMode", t),
      theme,
    );
    for (const r of ["inbox", "tasks", "workstreams", "agents", "settings"]) {
      await page.goto(`/next/${r}`);
      await settle(page);
      const axe = await new AxeBuilder({ page })
        .withRules(["color-contrast"])
        .analyze();
      for (const v of axe.violations)
        failures.push(
          `${theme} ${r}: ${v.nodes.length}× ${v.nodes
            .slice(0, 3)
            .map((n) => n.target.join(" "))
            .join(", ")}`,
        );
    }
  }
  expect(failures, failures.join("\n")).toEqual([]);
});
