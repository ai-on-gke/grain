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

// Faults the 2026-10-13 repository, release and memory walkthrough found
// (docs/test-runs/2026-10-13-repository-release-memory-ui.md), driven the
// way the walkthrough met them: the real `grain demo` server, its seeded
// store, and nothing stubbed.
test.describe.configure({ mode: "serial" });

const ownership = "/api/repos/acme/widgets/work-ownership";

// The repo page draws the release's on/off twice: Work ownership's
// "release" row is the release item's override, and Release workflows'
// "Automatic releases" switch writes that same override. Saving either
// left the other saying what was true before until a reload.
test("the repo page's two release switches follow each other without a reload", async ({
  page,
}) => {
  try {
    await page.goto("/repos/acme/widgets#releases");
    const main = page.getByRole("main");
    await expect(main.getByText("Automatic releases on")).toBeVisible();

    await main.getByRole("button", { name: "release: off" }).click();
    await expect(main.getByText("Automatic releases off")).toBeVisible();

    await main.getByRole("button", { name: "release: say nothing" }).click();
    await expect(main.getByText("Automatic releases on")).toBeVisible();

    await main
      .getByRole("button", { name: "Turn off automatic releases" })
      .click();
    await expect(
      main.getByRole("button", { name: "release: off" }),
    ).toHaveAttribute("aria-pressed", "true");

    await main
      .getByRole("button", { name: "Turn on automatic releases" })
      .click();
    await expect(
      main.getByRole("button", { name: "release: on" }),
    ).toHaveAttribute("aria-pressed", "true");

    // The repository default decides a release that says nothing.
    await main.getByRole("button", { name: "release: say nothing" }).click();
    await main
      .getByRole("button", { name: "the repository default: off" })
      .click();
    await expect(main.getByText("Automatic releases off")).toBeVisible();

    await page.reload();
    await expect(main.getByText("Automatic releases off")).toBeVisible();
    await expect(
      main.getByRole("button", { name: "release: say nothing" }),
    ).toHaveAttribute("aria-pressed", "true");
  } finally {
    await page.request.put(ownership, { data: { default: "" } });
    await page.request.put(ownership, { data: { item: "release", state: "" } });
  }
});

// The memory panel's introduction once sent the reader to "the standing
// instructions on the Models tab", which has none; on main it is the
// team's own Memory page and names System → Agents & Task Branches.
test("the memory panel says where standing instructions are edited", async ({
  page,
}) => {
  await page.goto("/settings/memory");
  const intro = page.getByText(/What grain has learned about working here/);
  await expect(intro).toContainText(
    "standing instructions (Settings → Agents)",
  );
  await expect(intro).not.toContainText("Models tab");
});
