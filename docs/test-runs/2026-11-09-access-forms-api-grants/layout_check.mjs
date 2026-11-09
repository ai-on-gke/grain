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

// docs/test-plans/2026-11-09.md §1 item 6, measured rather than eyeballed:
// every form that lost its Legacy capabilities picker, at 1440×900 and
// 390×844 in light and dark, over the real daemon item1_seed.sh seeded.
// Per form it records the grants group's box, the gap to whatever comes
// next (an empty slot where the picker was would show as a large gap),
// horizontal overflow, any "Legacy capabilities" text left behind, and
// headings with nothing under them.
//
//   cd ui && node ../docs/test-runs/2026-11-09-access-forms-api-grants/layout_check.mjs \
//     http://127.0.0.1:4295 ACCESS_CODE IDS_JSON OUT_JSON
import { createRequire } from "node:module";
import fs from "node:fs";

const { chromium } = createRequire(`${process.cwd()}/`)("playwright");
const [base, code, idsFile, outFile] = process.argv.slice(2);
const ids = JSON.parse(fs.readFileSync(idsFile, "utf8"));

const forms = [
  {
    name: "task-pane",
    url: `/tasks/${ids.task}`,
    testId: `task-${ids.task}-grants`,
  },
  {
    name: "template",
    url: `/processes/templates/${ids.template}`,
    testId: "template-grants",
  },
  {
    name: "schedule",
    url: `/processes/schedules/${ids.schedule}`,
    testId: "schedule-grants",
  },
  {
    name: "settings-tasks",
    url: "/settings/tasks",
    testId: "settings-default-grants",
  },
  {
    name: "repo-defaults",
    url: "/repos/acme/widgets#agents",
    testId: "repo-default-grants",
  },
  { name: "new-task", url: "/", testId: "new-task-grants", open: true },
];

const browser = await chromium.launch();
const out = [];
let failures = 0;
for (const [width, height] of [
  [1440, 900],
  [390, 844],
]) {
  for (const theme of ["light", "dark"]) {
    const context = await browser.newContext({
      viewport: { width, height },
      colorScheme: theme,
      extraHTTPHeaders: { Authorization: `Bearer ${code}` },
    });
    await context.addInitScript(
      (t) => localStorage.setItem("grain.themeMode", t),
      theme,
    );
    const page = await context.newPage();
    for (const f of forms) {
      await page.goto(base + f.url);
      if (f.open)
        await page.getByRole("button", { name: /^\+? ?New task$/ }).click();
      const group = page.getByTestId(f.testId);
      await group.waitFor({ timeout: 15000 });
      await group.scrollIntoViewIfNeeded();
      const m = await group.evaluate((el) => {
        const box = el.getBoundingClientRect();
        // The next thing drawn below the group within its container.
        let next = null;
        for (let n = el; n && !next; n = n.parentElement) {
          let s = n.nextElementSibling;
          while (s && s.getBoundingClientRect().height === 0)
            s = s.nextElementSibling;
          if (s) next = s;
        }
        const nb = next ? next.getBoundingClientRect() : null;
        const fieldset = el.closest("fieldset");
        const legend =
          fieldset?.querySelector("legend")?.textContent?.trim() ?? null;
        const headings = [
          ...document.querySelectorAll(
            "h1,h2,h3,h4,h5,h6,legend,.MuiTypography-subtitle2",
          ),
        ]
          .filter((h) => h.getBoundingClientRect().height > 0)
          // An outlined field's notch is a <legend> of its own, not a heading.
          .filter(
            (h) =>
              !h.closest(
                ".MuiOutlinedInput-notchedOutline, .MuiFormControl-root, .MuiInputBase-root",
              ),
          )
          .filter((h) => {
            // A heading is orphaned when nothing visible follows it in its parent.
            let s = h.nextElementSibling;
            while (s && s.getBoundingClientRect().height === 0)
              s = s.nextElementSibling;
            return (
              !s && h.parentElement && h.parentElement.children.length === 1
            );
          })
          .map((h) => h.textContent.trim());
        return {
          box: { x: box.x, y: box.y, w: box.width, h: box.height },
          gapToNext: nb ? Math.round(nb.top - box.bottom) : null,
          nextText: next ? next.textContent.trim().slice(0, 60) : null,
          fieldsetLegend: legend,
          fieldsetExtraHeight: fieldset
            ? Math.round(fieldset.getBoundingClientRect().height - box.height)
            : null,
          overflowX: document.documentElement.scrollWidth - window.innerWidth,
          legacyText: document.body.innerText.includes("Legacy capabilities"),
          orphanHeadings: headings,
        };
      });
      const bad = [];
      if (m.overflowX > 1) bad.push(`horizontal overflow ${m.overflowX}px`);
      if (m.legacyText) bad.push("Legacy capabilities text");
      if (m.box.x < 0 || m.box.x + m.box.w > width + 1)
        bad.push("grants outside the viewport");
      if (m.gapToNext !== null && m.gapToNext > 48)
        bad.push(`gap ${m.gapToNext}px below grants`);
      if (m.orphanHeadings.length)
        bad.push(`orphaned headings ${m.orphanHeadings.join(", ")}`);
      failures += bad.length;
      out.push({ form: f.name, width, theme, ...m, bad });
      console.log(
        `${bad.length ? "FAIL" : "PASS"} ${f.name} ${width} ${theme}${bad.length ? ": " + bad.join("; ") : ""} (gap ${m.gapToNext}px to "${m.nextText}")`,
      );
    }
    await context.close();
  }
}
fs.writeFileSync(outFile, JSON.stringify(out, null, 2));
await browser.close();
process.exit(failures ? 1 : 0);
