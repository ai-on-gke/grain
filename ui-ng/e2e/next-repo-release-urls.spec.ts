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

// Per-release URLs on the /next/ repository page over a real `grain demo`
// (2026-11-14 repository walkthrough, item 3,
// docs/test-runs/2026-11-14-repository-release-memory-ui.md):
// - deep link to /next/repos/<owner>/<name>/releases/<release> opens the
//   Releases tab, selects that release, loads its candidates, and scrolls
//   its row into view;
// - reload keeps the selected release and its URL;
// - selecting a release puts its URL in the address bar, and Back steps
//   through release selections and back to the bare Releases list;
// - an unknown release name shows the classic warning wording and links
//   back to the Releases list.

import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

const RELEASES_PATH = "/next/repos/acme/widgets/releases";
const SEEDED = [
  "e2e-r1",
  "e2e-r2",
  "e2e-r3",
  "e2e-r4",
  "e2e-r5",
  "e2e-r6",
  "e2e-r7",
  "e2e-r8",
];

async function createReleases(page: Page, names: readonly string[]) {
  for (const name of names) {
    const res = await page.request.post("/api/repos/acme/widgets/releases", {
      data: { name },
    });
    expect(res.ok(), `POST release ${name}: ${res.status()}`).toBe(true);
  }
}

async function deleteReleases(page: Page, names: readonly string[]) {
  for (const name of names) {
    await page.request
      .delete(`/api/repos/acme/widgets/releases/${encodeURIComponent(name)}`)
      .catch(() => undefined);
  }
}

test("deep link selects and scrolls to the release, reload keeps it, and Back walks selections @phone @dark", async ({
  page,
  axe,
}) => {
  await createReleases(page, SEEDED);
  try {
    // Deep link to 1.0, which sits at the bottom of the 9-row release
    // table (newest first) and has candidate 1.0.rc.1.
    await page.goto(`${RELEASES_PATH}/1.0`);

    const releasesTab = page.getByRole("button", { name: "Releases" });
    await expect(releasesTab).toHaveAttribute("aria-current", "page");

    const width = page.viewportSize()!.width;
    await expect
      .poll(async () => {
        const b = await releasesTab.boundingBox();
        return !!b && b.x >= 0 && b.x + b.width <= width + 0.5;
      })
      .toBe(true);

    const table = page.getByRole("table", { name: "Releases" });
    const row10 = table.getByRole("row", { name: /^1\.0\b/ });
    const rowNewest = table.getByRole("row", { name: /^e2e-r8\b/ });
    await expect(row10).toHaveAttribute("aria-current", "true");
    await expect(rowNewest).not.toHaveAttribute("aria-current", "true");

    const candidatesTable = page.getByRole("table", {
      name: "Release candidates",
    });
    await expect(candidatesTable).toContainText("1.0.rc.1");

    // 1.0 was below the fold of the repository stage and has been
    // scrolled into its visible bounds without horizontal page overflow.
    const stage = page.getByRole("region", { name: "acme/widgets" });
    await expect
      .poll(async () => {
        const sb = await stage.boundingBox();
        const rb = await row10.boundingBox();
        if (!sb || !rb) return false;
        return rb.y >= sb.y - 1 && rb.y + rb.height <= sb.y + sb.height + 1;
      })
      .toBe(true);
    expect(
      await stage.evaluate((el) => el.firstElementChild?.scrollTop ?? 0),
    ).toBeGreaterThan(0);
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    ).toBeLessThanOrEqual(0);
    expect(await axe()).toEqual([]);

    // Reload keeps /releases/1.0 selected and scrolled into view.
    await page.reload();
    await expect(page).toHaveURL(
      /\/next\/repos\/acme\/widgets\/releases\/1\.0$/,
    );
    await expect(row10).toHaveAttribute("aria-current", "true");
    await expect(candidatesTable).toContainText("1.0.rc.1");

    // Start on the bare Releases list (newest selected), select 1.0, then
    // select e2e-r2 via the candidate picker, and step Back twice.
    await page.goto(RELEASES_PATH);
    await expect(rowNewest).toHaveAttribute("aria-current", "true");

    await table.getByRole("link", { name: "1.0", exact: true }).click();
    await expect(page).toHaveURL(
      /\/next\/repos\/acme\/widgets\/releases\/1\.0$/,
    );
    await expect(row10).toHaveAttribute("aria-current", "true");
    await expect(candidatesTable).toContainText("1.0.rc.1");

    await page
      .getByRole("button", { name: "Select release to inspect candidates" })
      .click();
    await page.getByRole("option", { name: "e2e-r2", exact: true }).click();
    await expect(page).toHaveURL(
      /\/next\/repos\/acme\/widgets\/releases\/e2e-r2$/,
    );
    await expect(table.getByRole("row", { name: /^e2e-r2\b/ })).toHaveAttribute(
      "aria-current",
      "true",
    );

    await page.goBack();
    await expect(page).toHaveURL(
      /\/next\/repos\/acme\/widgets\/releases\/1\.0$/,
    );
    await expect(row10).toHaveAttribute("aria-current", "true");
    await expect(candidatesTable).toContainText("1.0.rc.1");

    await page.goBack();
    await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/releases$/);
    await expect(rowNewest).toHaveAttribute("aria-current", "true");
  } finally {
    await deleteReleases(page, SEEDED);
  }
});

test("an unknown release shows the classic warning and links back to the Releases list @phone @dark", async ({
  page,
  axe,
}) => {
  await page.goto(`${RELEASES_PATH}/nosuch-release-5866`);

  const notice = page.getByRole("alert").filter({
    hasText:
      "acme/widgets has no release named \u201cnosuch-release-5866\u201d",
  });
  await expect(notice).toBeVisible();
  await expect(notice).toContainText(
    "showing 1.0 instead. The link may be stale, or the release deleted; pick another below.",
  );

  const width = page.viewportSize()!.width;
  const noticeBox = await notice.boundingBox();
  const tableBox = await page
    .getByRole("table", { name: "Releases" })
    .boundingBox();
  expect(noticeBox).not.toBeNull();
  expect(tableBox).not.toBeNull();
  expect(noticeBox!.x).toBeGreaterThanOrEqual(0);
  expect(noticeBox!.x + noticeBox!.width).toBeLessThanOrEqual(width + 0.5);
  expect(noticeBox!.y + noticeBox!.height).toBeLessThanOrEqual(tableBox!.y);
  expect(
    await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    ),
  ).toBeLessThanOrEqual(0);
  expect(await axe()).toEqual([]);

  const backLink = notice.getByRole("link", { name: "Back to Releases" });
  await expect(backLink).toBeVisible();
  await backLink.click();

  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/releases$/);
  await expect(notice).toHaveCount(0);
  await expect(
    page
      .getByRole("table", { name: "Releases" })
      .getByRole("row", { name: /^1\.0\b/ }),
  ).toHaveAttribute("aria-current", "true");
});
