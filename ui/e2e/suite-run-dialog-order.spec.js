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

import { expect, test } from "./fixtures.js";

// The run dialog's suites are in the suites page's own order, sort
// included, and the bare "+ Run" preselects the page's first row
// (4fc2ac115). Until then the dialog took the API's newest-first order
// and preselected whichever suite was created last. Run against the real
// `grain demo` server: the suites are posted to its API, and the page
// and dialog are read as they render.
test("the run dialog lists suites in the order the Processes page does", async ({
  page,
}) => {
  const stamp = Date.now().toString(36);
  const post = async (path, data) => {
    const res = await page.request.post(path, { data });
    expect(res.ok(), `POST ${path}: ${await res.text()}`).toBe(true);
    return res.json();
  };
  const templates = [];
  for (const n of [1, 2]) {
    templates.push(
      await post("/api/templates", {
        name: `Order template ${n} ${stamp}`,
        title: `Order step ${n} ${stamp}`,
        description: "A suite step.",
      }),
    );
  }
  // Filed in name order, so the API (newest first) lists them the other
  // way round; a schedule fires the second, so Processes' default sort
  // (Next run) puts it first again, ahead of a suite run only on demand.
  await post("/api/suites", {
    name: `Aardvark order ${stamp}`,
    templateIds: [templates[0].id],
    mode: "count",
    count: 1,
  });
  const zebra = await post("/api/suites", {
    name: `Zebra order ${stamp}`,
    templateIds: templates.map((t) => t.id),
    mode: "count",
    count: 1,
  });
  await post("/api/schedules", {
    suiteId: zebra.id,
    repo: "acme/widgets",
    base: "main",
    recurrence: { kind: "everyNHours", everyNHours: 1 },
  });

  await page.goto("/suites");
  await expect(page.getByRole("heading", { name: "Processes" })).toBeVisible();
  const rows = page.locator(".process-row-suite .template-name");
  await expect(rows.filter({ hasText: stamp })).toHaveCount(2);

  const dialogMatchesPage = async () => {
    const order = await rows.allTextContents();
    expect(order.indexOf(`Aardvark order ${stamp}`)).toBeGreaterThanOrEqual(0);
    await page.getByRole("button", { name: "+ Run" }).click();
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toBeVisible();
    const picker = page.getByRole("combobox", { name: "Suite" });
    await expect(picker).toHaveText(order[0]);
    await picker.click();
    await expect(page.getByRole("listbox").getByRole("option")).toHaveText(
      order,
    );
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Cancel" }).click();
    await expect(
      page.getByRole("heading", { name: "Run a suite" }),
    ).toHaveCount(0);
    return order;
  };

  const byNext = await dialogMatchesPage();
  expect(byNext.indexOf(`Zebra order ${stamp}`)).toBeLessThan(
    byNext.indexOf(`Aardvark order ${stamp}`),
  );

  // Sorted by name, the toolbar's sort as a link writes it.
  await page.goto("/processes?kind=suite&sort=name:asc");
  await expect(rows.filter({ hasText: stamp })).toHaveCount(2);
  const byName = await dialogMatchesPage();
  expect(byName.indexOf(`Aardvark order ${stamp}`)).toBeLessThan(
    byName.indexOf(`Zebra order ${stamp}`),
  );
});
