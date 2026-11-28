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
import { test, expect } from "./fixtures.js";

// Regression from the 2026-11-18 §8 /next/ fixes walkthrough
// (docs/test-runs/2026-11-18-next-fixes.md, item 5): PR #1440 stepped an
// embedded task detail down to h2, but /next/agents' ungrouped cards
// titled with an h3 straight under the page's h1, and a workstream's
// "In this stream" section did the same -- a skipped level axe reports
// as heading-order. Each page, with a task selected, its edit panel open
// and the Director shaper open in it, has exactly one h1 and no skip.

async function outline(page) {
  return page.evaluate(() =>
    [...document.querySelectorAll("h1,h2,h3,h4,h5,h6,[role=heading]")]
      .filter(
        (h) =>
          h.getClientRects().length > 0 &&
          !h.closest("[aria-hidden=true],[inert]"),
      )
      .map((h) => ({
        level: Number(
          h.getAttribute("aria-level") ||
            (/^H\d$/.test(h.tagName) ? h.tagName[1] : 2),
        ),
        text: h.textContent.replace(/\s+/g, " ").trim().slice(0, 60),
      })),
  );
}

async function expectSoundOutline(page, label) {
  const hs = await outline(page);
  expect(
    hs.filter((h) => h.level === 1),
    `${label}: one h1 in ${JSON.stringify(hs)}`,
  ).toHaveLength(1);
  for (let i = 1; i < hs.length; i++) {
    expect(
      hs[i].level,
      `${label}: "${hs[i].text}" (h${hs[i].level}) follows h${hs[i - 1].level}`,
    ).toBeLessThanOrEqual(hs[i - 1].level + 1);
  }
  const axe = await new AxeBuilder({ page })
    .withRules(["heading-order", "page-has-heading-one", "empty-heading"])
    .analyze();
  expect(
    axe.violations.map((v) => v.id),
    label,
  ).toEqual([]);
}

async function editAndShape(page, label) {
  await expectSoundOutline(page, `${label}, task selected`);
  await page.getByTestId("act-edit").first().click();
  await expect(page.getByTestId("task-edit-panel")).toBeVisible();
  await expectSoundOutline(page, `${label}, edit panel`);
  await page.getByTestId("edit-shape-director").click();
  await expect(page.getByTestId("director-shaper-pane")).toBeVisible();
  await expectSoundOutline(page, `${label}, Director shaper`);
}

test("Agents, Workstreams and Tasks panes keep one h1 and skip no heading level", async ({
  page,
  pageErrors,
}) => {
  await page.goto("agents?task=3&pane=details");
  await expect(page.locator(".agent-grid .agent-title").first()).toBeVisible();
  await expect(
    page.getByRole("heading", { level: 2, name: "Running agents" }),
  ).toHaveCount(1);
  await editAndShape(page, "agents");

  await page.goto("workstreams");
  await page
    .locator("[data-testid^=ws-card-],[data-testid^=ws-row-]")
    .first()
    .click();
  await expect(page.getByTestId("ws-detail")).toBeVisible();
  await expect(
    page.getByRole("heading", { level: 2, name: "In this stream" }),
  ).toBeVisible();
  await page
    .locator("[data-testid^=ws-tree-node-],[data-testid^=ws-gnode-]")
    .first()
    .click();
  await expect(page.getByTestId("act-edit").first()).toBeVisible();
  await editAndShape(page, "workstreams");

  await page.goto("tasks?task=3");
  await expect(page.getByTestId("act-edit").first()).toBeVisible();
  await editAndShape(page, "tasks");

  expect(pageErrors).toEqual([]);
});
