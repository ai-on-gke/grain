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

// Classic repository addresses pasted under /next/ (2026-11-20 repository
// walkthrough, docs/test-runs/2026-11-20-repository-release-memory-ui.md):
//
//   - /next/repos/<o>/<n>/releases/<release> read as host <o>, owner <n>,
//     repo "releases", tab <release>: it loaded a repository that does not
//     exist, with a row of 400s. It now opens the Releases tab on that
//     release, and a release that is not there falls back to the newest
//     with a note saying so.
//   - /next/repos/<o>/<n>/compare/<base>...<head> fell through to the
//     Inbox. The compare page is classic-only for now, so the address --
//     slashes and "#" in branch names included -- goes there.
//
// Over a real `grain demo` with its GitHub simulation, which cuts release
// 1.0 and the release/1.0 branch.

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

const R = "/next/repos/acme/widgets";

test("a per-release address opens the Releases tab, and a stale one says so", async ({
  page,
  pageErrors,
}) => {
  const bad = [];
  page.on("response", (r) => {
    if (r.status() >= 400 && r.url().includes("/api/")) bad.push(r.url());
  });

  await page.goto(`${R}/releases/1.0`);
  await expect(page).toHaveURL(
    /\/next\/repos\/acme\/widgets\/releases\?release=1\.0$/,
  );
  await expect(page.getByTestId("repo-detail-heading")).toHaveText(
    "acme/widgets",
  );
  await expect(page.getByTestId("repo-tab-releases")).toHaveAttribute(
    "aria-current",
    "page",
  );
  await expect(page.getByTestId("release-row-1.0")).toBeVisible();
  await expect(page.getByTestId("release-missing-note")).toHaveCount(0);

  await page.goto(`${R}/releases/9.9`);
  await expect(page.getByTestId("release-missing-note")).toHaveText(
    /acme\/widgets has no release named “9\.9” -- showing 1\.0 instead\./,
  );
  await page.reload();
  await expect(page.getByTestId("release-missing-note")).toBeVisible();

  expect(bad).toEqual([]);
  expect(pageErrors).toEqual([]);
});

test("a compare address goes to the classic compare page, branch names intact", async ({
  page,
}) => {
  await page.goto(`${R}/compare/main...release/1.0`);
  await expect(page).toHaveURL(
    /\/repos\/acme\/widgets\/compare\/main\.\.\.release\/1\.0$/,
  );
  expect(new URL(page.url()).pathname.startsWith("/next/")).toBe(false);
  // The classic page names the head branch in its heading; its bundle
  // loads cold here, so give it longer than the default.
  await expect(
    page.getByRole("heading", { name: "release/1.0", exact: true }),
  ).toBeVisible({ timeout: 20_000 });

  await page.goto(`${R}/compare/main...fix/%23nope`);
  await expect(page).toHaveURL(
    /\/repos\/acme\/widgets\/compare\/main\.\.\.fix\/%23nope$/,
  );
  await expect(
    page.getByRole("heading", { name: "fix/#nope", exact: true }),
  ).toBeVisible({ timeout: 20_000 });
});

test("a repository on a named host still takes a tab", async ({ page }) => {
  await page.goto("/next/repos/github.com/acme/widgets/ci");
  await expect(page.getByTestId("repo-detail-heading")).toHaveText(
    "github.com/acme/widgets",
  );
  await expect(page.getByTestId("repo-tab-ci")).toHaveAttribute(
    "aria-current",
    "page",
  );
});
