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

import { test, expect } from "./fixtures.js";

// docs/test-plans/2026-10-15.md §4, steps 1-2: a suite and a template
// open in two separate browser contexts -- two sessions, not two tabs of
// one -- each saving its own change. The second save is refused as stale
// (9d55be291), its pane stays open with its edit and offers Reload
// (f98d67b43), and Reload shows the first save and nothing from the
// stale copy. recurring-composed-walkthrough.spec.js step 17 covers the
// refusal from a second tab; this is the Reload half for the suite and
// the template, which only the schedule had.
test.describe.configure({ mode: "serial" });

test("a second browser's stale save of a suite and of a template is refused, kept, and reloads to the first save", async ({
  browser,
  baseURL,
  page,
}) => {
  const stamp = Date.now();
  const seed = async (path, data) => {
    const res = await page.request.post(path, { data });
    expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
    return res.json();
  };
  const row = async (path, id) =>
    (await (await page.request.get(path)).json()).find((r) => r.id === id);

  const tmpl = await seed("/api/templates", {
    name: `stale-${stamp}`,
    title: `Stale ${stamp}`,
    description: "first",
  });
  const suite = await seed("/api/suites", {
    name: `stale-suite-${stamp}`,
    templateIds: [tmpl.id],
    mode: "until_clean",
    maxPasses: 2,
  });

  const open = async () => {
    const context = await browser.newContext({
      baseURL,
    });
    return { context, tab: await context.newPage() };
  };
  const a = await open();
  const b = await open();
  const refused = async (pane) => {
    const notice = b.tab.getByRole("alert").filter({
      hasText: /changed by somebody else/,
    });
    await expect(notice).toBeVisible();
    await expect(notice).toContainText("Reload replaces them");
    await expect(
      b.tab.getByRole("heading", { name: `Edit ${pane}` }),
    ).toBeVisible();
    return notice;
  };

  // Suite: A raises max passes, B (opened on 2) ticks Require approval.
  await a.tab.goto(`/suites/${suite.id}`);
  await b.tab.goto(`/suites/${suite.id}`);
  const passes = (tab) => tab.getByRole("spinbutton", { name: "Max passes" });
  const approval = (tab) =>
    tab.getByRole("checkbox", { name: /Require approval/i });
  await expect(passes(b.tab)).toHaveValue("2");
  await passes(a.tab).fill("4");
  await a.tab.getByRole("button", { name: "Save" }).click();
  await expect(a.tab).toHaveURL(/\/processes(\?kind=suite)?$/);
  await approval(b.tab).check();
  await b.tab.getByRole("button", { name: "Save" }).click();
  const suiteNotice = await refused("suite");
  // The pane keeps B's edit to copy out.
  await expect(approval(b.tab)).toBeChecked();
  await expect(passes(b.tab)).toHaveValue("2");
  let savedSuite = await row("/api/suites", suite.id);
  expect(savedSuite.maxPasses).toBe(4);
  expect(savedSuite.requireApproval).toBe(false);
  await suiteNotice.getByRole("button", { name: "Reload" }).click();
  await expect(suiteNotice).toHaveCount(0);
  await expect(passes(b.tab)).toHaveValue("4");
  await expect(approval(b.tab)).not.toBeChecked();
  // And a save made after Reload goes through, keeping A's change.
  await approval(b.tab).check();
  await b.tab.getByRole("button", { name: "Save" }).click();
  await expect(b.tab).toHaveURL(/\/processes(\?kind=suite)?$/);
  savedSuite = await row("/api/suites", suite.id);
  expect(savedSuite.maxPasses).toBe(4);
  expect(savedSuite.requireApproval).toBe(true);

  // Template: A retitles, B (opened on "first") rewrites the description.
  await a.tab.goto(`/templates/${tmpl.id}`);
  await b.tab.goto(`/templates/${tmpl.id}`);
  const title = (tab) => tab.locator('input[name="title"]');
  const desc = (tab) => tab.locator('textarea[name="description"]');
  await expect(title(b.tab)).toHaveValue(`Stale ${stamp}`);
  await title(a.tab).fill(`Stale ${stamp} (A)`);
  await a.tab.getByRole("button", { name: "Save" }).click();
  await expect(a.tab).toHaveURL(/\/processes(\?kind=template)?$/);
  await desc(b.tab).fill("second");
  await b.tab.getByRole("button", { name: "Save" }).click();
  const tmplNotice = await refused("template");
  await expect(desc(b.tab)).toHaveValue("second");
  await expect(title(b.tab)).toHaveValue(`Stale ${stamp}`);
  let savedTmpl = await row("/api/templates", tmpl.id);
  expect(savedTmpl.title).toBe(`Stale ${stamp} (A)`);
  expect(savedTmpl.description).toBe("first");
  await tmplNotice.getByRole("button", { name: "Reload" }).click();
  await expect(tmplNotice).toHaveCount(0);
  await expect(title(b.tab)).toHaveValue(`Stale ${stamp} (A)`);
  await expect(desc(b.tab)).toHaveValue("first");
  await desc(b.tab).fill("second");
  await b.tab.getByRole("button", { name: "Save" }).click();
  await expect(b.tab).toHaveURL(/\/processes(\?kind=template)?$/);
  savedTmpl = await row("/api/templates", tmpl.id);
  expect(savedTmpl.title).toBe(`Stale ${stamp} (A)`);
  expect(savedTmpl.description).toBe("second");

  // Step 2: a PATCH with no revision is not checked, and applies.
  const res = await page.request.patch(`/api/templates/${tmpl.id}`, {
    data: { description: "no revision" },
  });
  expect(res.ok(), `PATCH without revision: ${res.status()}`).toBe(true);
  expect((await row("/api/templates", tmpl.id)).description).toBe(
    "no revision",
  );

  await a.context.close();
  await b.context.close();
  for (const path of [`/api/suites/${suite.id}`, `/api/templates/${tmpl.id}`]) {
    expect((await page.request.delete(path)).ok(), path).toBe(true);
  }
});
