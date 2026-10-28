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

// docs/test-plans/2026-10-13.md §9 step 2, in a browser on the real
// `grain demo` server (docs/test-runs/2026-10-14-port-settings-revert-porting.md).
// `-mechanical-port` pairs acme/widgets' main and candidate as the
// operator (main->candidate narrowed to fix), and reads a
// .grain/settings.yml whose port-types widens that list, puts a list on
// candidate->main (an operator pair with no list of its own) and
// misspells candidate in a third direction. The repository page shows
// the pairs, each direction's filter and both warnings, read-only for
// to the deployment's owner, and they match GET .../settings. Nothing of grain's is
// stubbed.
test.use({ demoArgs: ["-github-sim", "-mechanical-port", "-fake-ci"] });

test.setTimeout(120_000);

const ROWS = [
  ["candidate->main", "—", "fix", "every merge"],
  ["main->candidate", "fix", "fix, revert", "fix, revert"],
  ["main->candidtae", "—", "fix", "nothing (not a paired branch)"],
];

async function expectPortSection(page, settings) {
  await page.goto("/repos/acme/widgets#advanced");
  const ports = page.locator(".repo-ports");
  await expect(ports).toBeVisible();
  await expect(ports).toContainText(
    "Grain ports each merge between main, candidate",
  );
  const table = ports.getByRole("table", { name: "Port directions" });
  const rows = table.locator("tbody tr");
  await expect(rows).toHaveCount(ROWS.length);
  for (let i = 0; i < ROWS.length; i++) {
    await expect(rows.nth(i).locator("td")).toHaveText(ROWS[i]);
  }
  const warnings = ports.locator(".port-warning");
  await expect(warnings).toHaveText(
    settings.portWarnings.map((w) => `Warning: ${w}`),
  );
  // Read-only: nothing in the section can be edited or pressed.
  await expect(
    ports.locator("input, textarea, select, button, [role=button]"),
  ).toHaveCount(0);
}

test("the repo page shows port pairs, filters and port-types warnings read-only, as GET settings reports them", async ({
  page,
}) => {
  const res = await page.request.get("/api/repos/acme/widgets/settings");
  expect(res.ok()).toBe(true);
  const settings = await res.json();
  expect(settings.pairedBranches).toEqual(["main", "candidate"]);
  expect(settings.portBranches).toEqual(["main", "candidate"]);
  expect(
    settings.portDirections.map((d) => [
      `${d.from}->${d.to}`,
      d.paired,
      d.operatorTypes || [],
      d.declaredTypes || [],
    ]),
  ).toEqual([
    ["candidate->main", true, [], ["fix"]],
    ["main->candidate", true, ["fix"], ["fix", "revert"]],
    ["main->candidtae", false, [], ["fix"]],
  ]);
  expect(settings.portWarnings).toHaveLength(2);
  expect(settings.portWarnings[0]).toContain("candidate->main is ignored");
  expect(settings.portWarnings[1]).toContain(
    "main->candidtae names candidtae, which is not a paired branch",
  );

  await expectPortSection(page, settings);
});
