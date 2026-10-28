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

import { request as playwrightRequest } from "@playwright/test";
import { test, expect } from "./fixtures.js";

// grain-ci is granted by a repository's CI mode, never attached by hand
// (task 2015; docs/test-plans/2026-10-05.md §10 is where this started).
//
// 6172e1205 put grain-ci back in ui.OfferedCapabilities, so the repo
// page's picker accepted it -- and since 57939261f a repository's CI
// mode is what turns grain CI on, so the capability changed nothing.
// Task 2015 took it back out and refuses it by name. This walks that as
// a system admin: the picker has no grain CI row, naming it anyway is
// refused with a message pointing at the CI mode, the CI API refuses
// the repo with the same pointer, and switching the CI mode on the same
// page starts a run.
//
test.use({ demoArgs: ["-fake-ci"] });
test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

test("grain-ci: not in the repo page's picker, and naming it is refused with the CI mode that grants it", async ({
  browser,
  baseURL,
}) => {
  const once = await playwrightRequest.newContext({ baseURL });
  await once.dispose();

  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();
  const startRun = () =>
    context.request.post("/api/ci/runs", {
      data: { repo: "acme/widgets", branch: "main" },
    });

  // The repo's default-capabilities picker offers no grain CI row.
  await page.goto("/repo/acme/widgets");
  await expect(
    page.getByRole("heading", { name: "acme/widgets" }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "Agents" }).click();
  const defaults = page.getByRole("combobox", {
    name: "Default capabilities",
  });
  await defaults.click();
  await expect(page.getByRole("listbox")).toBeVisible();
  await expect(page.getByRole("option", { name: /^grain CI/ })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await expect(page.getByRole("listbox")).toBeHidden();

  // Naming it anyway -- an old script, `grain repo capabilities -set` --
  // is refused, and the refusal names the switch rather than calling a
  // capability this build ships "unknown".
  let res = await context.request.put("/api/repos/acme/widgets/capabilities", {
    data: { defaultCapabilities: ["grain-ci"] },
  });
  expect(res.status()).toBe(400);
  let { error } = await res.json();
  expect(error).toContain("Use Grain CI");
  expect(error).not.toMatch(/unknown capability/i);

  // The CI API refuses the repo until its CI mode is grain, and says so.
  res = await startRun();
  expect(res.status()).toBe(403);
  ({ error } = await res.json());
  expect(error).toMatch(/grain CI is not enabled for acme\/widgets/);
  expect(error).toContain("Use Grain CI");

  // The switch the refusal names, on the same page. The radio is
  // controlled and only turns once the PUT answers, so it is clicked
  // and then waited on rather than check()ed.
  await page.getByRole("tab", { name: "CI" }).click();
  const grainMode = page.getByRole("radio", {
    name: "Make grain CI the gate for acme/widgets and stop reading GitHub's checks",
  });
  await grainMode.click();
  await expect(grainMode).toBeChecked();
  await expect
    .poll(async () => {
      const r = await context.request.get("/api/repos/acme/widgets/ci-mode");
      return (await r.json()).ciMode;
    })
    .toBe("grain");

  res = await startRun();
  expect(res.status(), await res.text()).toBe(202);
  const run = await res.json();
  expect(run.repo).toBe("acme/widgets");
  const runs = await (await context.request.get("/api/ci/runs")).json();
  expect(runs.enabled).toBe(true);
  expect(runs.runs.map((r) => r.id)).toContain(run.id);

  await context.close();
});
