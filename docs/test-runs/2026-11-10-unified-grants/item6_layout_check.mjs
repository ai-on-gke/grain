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

// item6_layout_check.mjs: Bounding-box geometry & overflow audit for §3 item 6
// (GrantsPicker with many repository lines and long repository/branch names)
// across task pane, template editor, New task overlay, and console session page
// at 1440x900 (desktop) and 390x844 (phone), in light and dark themes.
import { createRequire } from "node:module";
import fs from "node:fs";

const { chromium } = createRequire(`${process.cwd()}/`)("playwright");

const [base, token, idsJson, outFile] = process.argv.slice(2);
const ids = JSON.parse(idsJson);

const VIEWPORTS = [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
];
const THEMES = ["light", "dark"];

const browser = await chromium.launch();
const checks = [];
const pageErrors = [];

for (const [vName, size] of VIEWPORTS) {
  for (const theme of THEMES) {
    const context = await browser.newContext({
      viewport: size,
      colorScheme: theme,
      extraHTTPHeaders: { Authorization: `Bearer ${token}` },
    });
    await context.addInitScript((t) => {
      window.localStorage.setItem("grain.themeMode", t);
    }, theme);
    const page = await context.newPage();
    page.on("pageerror", (e) => pageErrors.push(`${vName}/${theme}: ${e}`));

    const surfaces = [
      {
        name: "taskPane",
        url: `/tasks/${ids.richTask}`,
        testId: `task-${ids.richTask}-grants`,
      },
      {
        name: "templateEditor",
        url: `/processes/templates/${ids.richTemplate}`,
        testId: "template-grants",
      },
      {
        name: "newTaskOverlay",
        url: "/",
        open: async () => {
          await page.getByRole("button", { name: /New task$/ }).first().click();
          const dialog = page.getByRole("dialog");
          const repoInput = dialog.getByLabel(/^Repos/);
          await repoInput.click();
          await page.getByRole("option", { name: /acme\/widgets/ }).first().click();
        },
        testId: "new-task-grants",
      },
      {
        name: "consoleSession",
        url: "/session/ops",
        open: async () => {
          await page.getByRole("button", { name: "Edit Ops capabilities" }).click();
        },
        testId: "session-ops-grants",
      },
    ];

    for (const s of surfaces) {
      await page.goto(`${base}${s.url}`);
      if (s.open) await s.open();
      const picker = page.getByTestId(s.testId);
      await picker.waitFor({ timeout: 15000 });
      await picker.scrollIntoViewIfNeeded();
      await page.waitForTimeout(300);

      const geom = await picker.evaluate((el, vpWidth) => {
        const pickerRect = el.getBoundingClientRect();
        const docOverflow =
          document.documentElement.scrollWidth > vpWidth + 1;
        const rows = [...el.querySelectorAll(".grants-held [data-grant]")];
        const problems = [];
        let prevBottom = null;

        rows.forEach((row, idx) => {
          const r = row.getBoundingClientRect();
          if (r.left < pickerRect.left - 1) {
            problems.push(`row ${idx} left (${r.left}) < picker (${pickerRect.left})`);
          }
          if (r.right > pickerRect.right + 1) {
            problems.push(`row ${idx} right (${r.right}) > picker (${pickerRect.right})`);
          }
          if (r.right > vpWidth + 1) {
            problems.push(`row ${idx} right (${r.right}) > viewport (${vpWidth})`);
          }
          if (prevBottom !== null && r.top < prevBottom - 1) {
            problems.push(`row ${idx} overlaps previous (${r.top} < ${prevBottom})`);
          }
          prevBottom = r.bottom;

          const label = row.querySelector(".grant-label");
          if (label) {
            const lr = label.getBoundingClientRect();
            if (lr.width < 40) {
              problems.push(`row ${idx} label crushed (${lr.width.toFixed(1)}px): ${label.textContent}`);
            }
          }
          const remove = row.querySelector("button.grant-remove");
          if (remove) {
            const br = remove.getBoundingClientRect();
            if (br.right > r.right + 1 || br.right < r.right - 20) {
              problems.push(`row ${idx} remove button misplaced (br.right=${br.right}, r.right=${r.right})`);
            }
            if (label) {
              const lr = label.getBoundingClientRect();
              const isSection = row.classList.contains("grant-section");
              const aligned = isSection
                ? br.top >= r.top - 1 && br.top <= lr.top + 8
                : Math.abs((br.top + br.height / 2) - (lr.top + lr.height / 2)) <= 8;
              if (!aligned) {
                problems.push(`row ${idx} remove button wrapped off label row`);
              }
            }
          }
          const branchInput = row.querySelector('input[aria-label^="Base branch in "]');
          if (branchInput && label) {
            const ir = branchInput.getBoundingClientRect();
            const lr = label.getBoundingClientRect();
            if (ir.top < lr.bottom - 2) {
              problems.push(`row ${idx} write branch box not below label row`);
            }
            if (ir.right > r.right + 1) {
              problems.push(`row ${idx} write branch box overflows row`);
            }
          }
        });

        return {
          rowCount: rows.length,
          docOverflow,
          pickerWidth: Math.round(pickerRect.width),
          problems,
        };
      }, size.width);

      checks.push({
        surface: s.name,
        viewport: vName,
        theme,
        pass: !geom.docOverflow && geom.problems.length === 0 && geom.rowCount > 0,
        ...geom,
      });
    }
    await context.close();
  }
}

await browser.close();
const summary = {
  total: checks.length,
  passed: checks.filter((c) => c.pass).length,
  failed: checks.filter((c) => !c.pass),
  pageErrors,
  checks,
};
fs.writeFileSync(outFile, JSON.stringify(summary, null, 2));
console.log(`Layout checks: ${summary.passed}/${summary.total} passed, pageErrors=${pageErrors.length}`);
if (summary.failed.length > 0) {
  console.error("Failed checks:", JSON.stringify(summary.failed, null, 2));
  process.exit(1);
}
